package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/its-aryansingh/amase/internal/runtime"
)

var (
	// ErrClosed is returned once the stream has ended and Run has returned.
	ErrClosed = errors.New("claude: session closed")
	// ErrTurnInProgress is returned by SendTurn while the previous turn runs.
	ErrTurnInProgress = errors.New("claude: a turn is already running")
)

// ControlError is an error control_response from Claude Code.
type ControlError struct {
	RequestID string
	Code      string
	Message   string
}

func (e *ControlError) Error() string {
	return fmt.Sprintf("claude: control request %s failed: %s (%s)", e.RequestID, e.Message, e.Code)
}

// Config configures a Conn.
type Config struct {
	SessionID   string       // amase session id, copied into every ApprovalRequest
	ProviderID  string       // the id passed as --session-id; system/init confirms or replaces it
	Gate        runtime.Gate // decides can_use_tool; nil denies every request
	EventBuffer int          // capacity of the Events channel; 0 means 256
	Logger      *slog.Logger // debug diagnostics only; nil discards
}

// Conn runs the stream-json control protocol over one duplex stream: r is
// Claude Code's stdout, w its stdin. It does not own a process, so process.go
// and the fake transport share it; together with Close it forms a
// runtime.Session.
//
// One goroutine (Run) owns the turn state and is the only producer of
// Events, so a turn's events are always ordered TurnStarted … TurnCompleted.
// Each can_use_tool request is decided on its own goroutine because the Gate
// may wait minutes for a person while the stream keeps flowing.
type Conn struct {
	cfg    Config
	log    *slog.Logger
	dec    *Decoder
	enc    *Encoder
	events chan runtime.Event
	cmds   chan command
	done   chan struct{}

	running   atomic.Bool
	malformed atomic.Uint64

	mu         sync.Mutex
	providerID string
	nextID     uint64
	pending    map[string]context.CancelFunc // can_use_tool request id → cancel its decision
	waiters    map[string]chan controlReply  // our request id → its control_response

	workers sync.WaitGroup

	// Owned by the Run goroutine.
	turn        int
	inFlight    bool
	interrupted bool
	acct        accounting
	costSeen    bool
	lastCostUSD float64
}

// NewConn returns a Conn reading frames from r and writing to w. Call Run.
func NewConn(r io.Reader, w io.Writer, cfg Config) *Conn {
	if cfg.EventBuffer <= 0 {
		cfg.EventBuffer = 256
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Conn{
		cfg:        cfg,
		log:        log,
		dec:        NewDecoder(r),
		enc:        NewEncoder(w),
		events:     make(chan runtime.Event, cfg.EventBuffer),
		cmds:       make(chan command),
		done:       make(chan struct{}),
		providerID: cfg.ProviderID,
		pending:    map[string]context.CancelFunc{},
		waiters:    map[string]chan controlReply{},
		acct:       newAccounting(),
	}
}

// ID returns the amase session id.
func (c *Conn) ID() string { return c.cfg.SessionID }

// ProviderID returns Claude Code's session id.
func (c *Conn) ProviderID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.providerID
}

// Events returns the normalised event stream. It is closed when Run returns.
func (c *Conn) Events() <-chan runtime.Event { return c.events }

// MalformedLines counts stream lines that were not JSON objects and were skipped.
func (c *Conn) MalformedLines() uint64 { return c.malformed.Load() }

// Initialize performs the initialize handshake (C5) and returns the response
// payload (commands, models, account). Claude Code accepts a user turn
// without waiting for it.
func (c *Conn) Initialize(ctx context.Context) (json.RawMessage, error) {
	return c.request(ctx, NewInitialize)
}

// SendTurn sends prompt as the next user turn (C1) and returns its number.
// The turn ends with a TurnCompleted event; one turn runs at a time.
func (c *Conn) SendTurn(ctx context.Context, prompt string) (int, error) {
	reply := make(chan beginResult, 1)
	if err := c.send(ctx, command{kind: cmdBeginTurn, prompt: prompt, reply: reply}); err != nil {
		return 0, err
	}
	r := <-reply
	if r.err != nil {
		return 0, r.err
	}
	if err := c.enc.Encode(NewUserMessage(c.ProviderID(), prompt)); err != nil {
		_ = c.send(context.Background(), command{kind: cmdAbortTurn, turn: r.turn, err: err})
		return 0, err
	}
	return r.turn, nil
}

// Interrupt stops the running turn (C4). Claude Code cancels any pending
// can_use_tool request, kills a running tool, and ends the turn with an
// error_during_execution result, reported here as status "interrupted". The
// session stays usable. Interrupting an idle session is a no-op.
func (c *Conn) Interrupt(ctx context.Context) error {
	if err := c.send(ctx, command{kind: cmdInterrupting}); err != nil {
		return err
	}
	_, err := c.request(ctx, NewInterrupt)
	return err
}

// Run reads the stream until it ends or ctx is cancelled, answering control
// requests and emitting events. It returns nil when the stream ends cleanly.
// The Gate must return promptly once its context is cancelled: Run waits for
// in-flight decisions before closing Events.
func (c *Conn) Run(ctx context.Context) error {
	if !c.running.CompareAndSwap(false, true) {
		return errors.New("claude: Run called twice")
	}
	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()

	frames := make(chan readResult, 64)
	go c.readLoop(frames)

	var runErr error
loop:
	for {
		select {
		case r := <-frames:
			if r.err != nil {
				if !errors.Is(r.err, io.EOF) {
					runErr = r.err
				}
				break loop
			}
			c.handle(ctx, workCtx, r.frame)
		case cmd := <-c.cmds:
			c.command(ctx, cmd)
		case <-ctx.Done():
			runErr = ctx.Err()
			break loop
		}
	}

	if c.inFlight {
		c.flushUsage(ctx, nil)
		why := "Claude Code's output ended mid-turn"
		if runErr != nil {
			why += ": " + runErr.Error()
		}
		c.emit(ctx, runtime.Event{Kind: runtime.SessionError, Turn: c.turn, Text: why})
		c.emit(ctx, runtime.Event{Kind: runtime.TurnCompleted, Turn: c.turn, Status: runtime.StatusError, IsError: true, Text: why})
		c.inFlight = false
	}
	cancelWork()
	close(c.done)
	c.workers.Wait()
	close(c.events)
	return runErr
}

type readResult struct {
	frame Frame
	err   error
}

func (c *Conn) readLoop(out chan<- readResult) {
	for {
		f, err := c.dec.Next()
		var se *SyntaxError
		if errors.As(err, &se) {
			c.malformed.Add(1)
			c.log.Debug("claude: skipped a malformed stream-json line", "bytes", len(se.Line))
			continue
		}
		select {
		case out <- readResult{frame: f, err: err}:
		case <-c.done:
			return
		}
		if err != nil {
			return
		}
	}
}

func (c *Conn) emit(ctx context.Context, ev runtime.Event) {
	select {
	case c.events <- ev:
	case <-ctx.Done():
		c.log.Debug("claude: event dropped after cancellation", "kind", ev.Kind)
	}
}

// ---- commands from SendTurn and Interrupt ---------------------------------

type cmdKind int

const (
	cmdBeginTurn cmdKind = iota
	cmdAbortTurn
	cmdInterrupting
)

type command struct {
	kind   cmdKind
	prompt string
	turn   int
	err    error
	reply  chan beginResult
}

type beginResult struct {
	turn int
	err  error
}

func (c *Conn) send(ctx context.Context, cmd command) error {
	select {
	case c.cmds <- cmd:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return ErrClosed
	}
}

func (c *Conn) command(ctx context.Context, cmd command) {
	switch cmd.kind {
	case cmdBeginTurn:
		if c.inFlight {
			cmd.reply <- beginResult{err: ErrTurnInProgress}
			return
		}
		c.turn++
		c.inFlight, c.interrupted = true, false
		// TurnStarted goes out before SendTurn writes the prompt, so no event
		// of this turn can precede it.
		c.emit(ctx, runtime.Event{Kind: runtime.TurnStarted, Turn: c.turn, Text: cmd.prompt})
		cmd.reply <- beginResult{turn: c.turn}
	case cmdAbortTurn:
		if c.inFlight && c.turn == cmd.turn {
			c.flushUsage(ctx, nil)
			c.emit(ctx, runtime.Event{Kind: runtime.TurnCompleted, Turn: c.turn, Status: runtime.StatusError,
				IsError: true, Text: "the prompt could not be sent: " + cmd.err.Error()})
			c.inFlight = false
		}
	case cmdInterrupting:
		if c.inFlight {
			c.interrupted = true
		}
	}
}

// ---- outbound control requests --------------------------------------------

type controlReply struct {
	Subtype   string          `json:"subtype"`
	RequestID string          `json:"request_id"`
	Response  json.RawMessage `json:"response"`
	Error     string          `json:"error"`
	ErrorCode string          `json:"error_code"`
}

func (c *Conn) request(ctx context.Context, build func(id string) ControlRequest) (json.RawMessage, error) {
	select {
	case <-c.done:
		return nil, ErrClosed
	default:
	}
	ch := make(chan controlReply, 1)
	c.mu.Lock()
	c.nextID++
	id := fmt.Sprintf("amase_%d", c.nextID)
	c.waiters[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.waiters, id)
		c.mu.Unlock()
	}()

	if err := c.enc.Encode(build(id)); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.Subtype != "success" {
			return nil, &ControlError{RequestID: id, Code: r.ErrorCode, Message: r.Error}
		}
		return r.Response, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, ErrClosed
	}
}

// ---- inbound frames --------------------------------------------------------

func (c *Conn) handle(ctx, workCtx context.Context, f Frame) {
	switch f.Type {
	case "control_request":
		c.onControlRequest(workCtx, f)
	case "control_cancel_request":
		c.mu.Lock()
		cancel := c.pending[f.RequestID]
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	case "control_response":
		var r controlReply
		if json.Unmarshal(f.Response, &r) != nil {
			return
		}
		c.mu.Lock()
		ch := c.waiters[r.RequestID]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- r:
			default: // a duplicate reply; the first one already settled the request
			}
		}
	case "assistant":
		c.onAssistant(ctx, f)
	case "user":
		c.onUser(ctx, f)
	case "result":
		c.onResult(ctx, f)
	case "system":
		if f.Subtype == "init" && f.SessionID != "" {
			c.mu.Lock()
			c.providerID = f.SessionID
			c.mu.Unlock()
		}
	}
}

type permissionRequest struct {
	Subtype   string          `json:"subtype"`
	ToolName  string          `json:"tool_name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
}

func (c *Conn) onControlRequest(ctx context.Context, f Frame) {
	var req permissionRequest
	err := json.Unmarshal(f.Request, &req)
	if req.Subtype != "can_use_tool" || err != nil {
		var reply ControlResponse
		if req.Subtype == "can_use_tool" {
			reply = NewPermissionResponse(f.RequestID, PermissionResult{Behavior: "deny",
				Message: "amase denied this tool call: the permission request could not be read."})
		} else {
			reply = NewErrorResponse(f.RequestID, "unsupported_request", "Unsupported control request subtype: "+req.Subtype)
		}
		c.workers.Add(1)
		go func() {
			defer c.workers.Done()
			c.write(reply)
		}()
		return
	}

	ar := runtime.ApprovalRequest{
		SessionID: c.cfg.SessionID,
		CallID:    req.ToolUseID,
		Turn:      c.turn,
		Source:    runtime.SourceCanUseTool,
		Tool:      req.ToolName,
		Input:     req.Input,
		Command:   commandOf(req),
		Paths:     pathsOf(req),
	}
	dctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.pending[f.RequestID] = cancel
	c.mu.Unlock()

	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		defer func() {
			c.mu.Lock()
			delete(c.pending, f.RequestID)
			c.mu.Unlock()
			cancel()
		}()
		d := c.decide(dctx, ar)
		if dctx.Err() != nil {
			// Claude Code withdrew the request (an interrupt sends
			// control_cancel_request) or the session ended: nobody is waiting.
			return
		}
		c.write(NewPermissionResponse(f.RequestID, permissionResult(d, ar.Input)))
	}()
}

func (c *Conn) decide(ctx context.Context, req runtime.ApprovalRequest) runtime.Decision {
	if c.cfg.Gate == nil {
		return runtime.Decision{Verdict: runtime.Deny, Reason: "no approval gate is configured"}
	}
	return c.cfg.Gate.Decide(ctx, req)
}

func (c *Conn) write(m Outbound) {
	if err := c.enc.Encode(m); err != nil {
		c.log.Debug("claude: write failed", "err", err)
	}
}

// permissionResult maps a decision onto the can_use_tool reply (section 4.9).
// Only an explicit allow lets the call run: escalate should have been resolved
// by the broker, and anything else fails closed.
func permissionResult(d runtime.Decision, input json.RawMessage) PermissionResult {
	if d.Verdict == runtime.Allow {
		// Claude Code runs updatedInput, not the original input, so allow must
		// always echo the input back (C3).
		if len(input) == 0 || string(input) == "null" {
			input = json.RawMessage(`{}`)
		}
		return PermissionResult{Behavior: "allow", UpdatedInput: input}
	}
	return PermissionResult{Behavior: "deny", Message: denyMessage(d)}
}

// denyMessage is what the model reads in the tool_result, verbatim (verified
// live): the reason and the rules, so it can change course instead of
// retrying a respelling of the same action.
func denyMessage(d runtime.Decision) string {
	reason := strings.TrimSpace(d.Reason)
	if reason == "" {
		reason = "no reason was recorded"
	}
	var b strings.Builder
	b.WriteString("amase denied this tool call: ")
	if d.Verdict != runtime.Deny {
		b.WriteString("it needs approval and none was given (")
		b.WriteString(reason)
		b.WriteString(")")
	} else {
		b.WriteString(reason)
	}
	var missing []string
	for _, r := range d.Rules {
		if !strings.Contains(reason, r) {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		b.WriteString(" [rules: ")
		b.WriteString(strings.Join(missing, ", "))
		b.WriteString("]")
	}
	b.WriteString(". Do not retry it in another form; if it is needed, ask the user.")
	return b.String()
}

// commandOf returns the shell text of a Bash call. PowerShell is left empty on
// purpose: Command is parsed as POSIX shell, and the policy escalates
// PowerShell by tool name.
func commandOf(req permissionRequest) string {
	if req.ToolName != "Bash" {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(req.Input, &in) != nil {
		return ""
	}
	return in.Command
}

func pathsOf(req permissionRequest) []string {
	key := ""
	switch req.ToolName {
	case "Edit", "Write", "MultiEdit":
		key = "file_path"
	case "NotebookEdit":
		key = "notebook_path"
	default:
		return nil
	}
	var in map[string]json.RawMessage
	if json.Unmarshal(req.Input, &in) != nil {
		return nil
	}
	var p string
	if json.Unmarshal(in[key], &p) != nil || p == "" {
		return nil
	}
	return []string{p}
}

// ---- frame → runtime.Event -------------------------------------------------

type wireUsage struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (c *Conn) onAssistant(ctx context.Context, f Frame) {
	var m struct {
		ID      string         `json:"id"`
		Model   string         `json:"model"`
		Content []contentBlock `json:"content"`
		Usage   *wireUsage     `json:"usage"`
	}
	if json.Unmarshal(f.Message, &m) != nil {
		c.malformed.Add(1)
		return
	}
	if m.ID != "" && m.Usage != nil {
		c.acct.observe(m.ID, m.Model, *m.Usage)
	}
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			if b.Text != "" {
				c.emit(ctx, runtime.Event{Kind: runtime.Text, Turn: c.turn, Text: b.Text})
			}
		case "tool_use":
			if b.ID != "" && c.acct.firstToolUse(b.ID) {
				c.emit(ctx, runtime.Event{Kind: runtime.ToolCall, Turn: c.turn, CallID: b.ID, Tool: b.Name, Input: b.Input})
			}
		}
	}
}

func (c *Conn) onUser(ctx context.Context, f Frame) {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(f.Message, &m) != nil {
		return
	}
	var blocks []contentBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		if b.Type == "tool_result" {
			c.emit(ctx, runtime.Event{Kind: runtime.ToolResult, Turn: c.turn, CallID: b.ToolUseID,
				IsError: b.IsError, Text: resultText(b.Content)})
		}
	}
}

// resultText flattens tool_result content: a string, or a list of blocks of
// which only the text ones are kept.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (c *Conn) onResult(ctx context.Context, f Frame) {
	var r struct {
		Subtype        string     `json:"subtype"`
		IsError        bool       `json:"is_error"`
		TerminalReason string     `json:"terminal_reason"`
		TotalCostUSD   *float64   `json:"total_cost_usd"`
		Usage          *wireUsage `json:"usage"`
		Errors         []string   `json:"errors"`
		UUID           string     `json:"uuid"`
	}
	_ = json.Unmarshal(f.Raw, &r)
	c.acct.resultID = r.UUID
	c.flushUsage(ctx, r.Usage)

	var cost float64
	if r.TotalCostUSD != nil {
		// total_cost_usd accumulates over the process's turns; report this
		// turn's share so that summing TurnCompleted costs does not overcount.
		cost = *r.TotalCostUSD
		if c.costSeen && cost >= c.lastCostUSD {
			cost -= c.lastCostUSD
		}
		c.costSeen, c.lastCostUSD = true, *r.TotalCostUSD
	}
	if !c.inFlight {
		return
	}

	ev := runtime.Event{Kind: runtime.TurnCompleted, Turn: c.turn, Status: runtime.StatusOK, CostUSD: cost}
	switch {
	case r.Subtype == "success" && !r.IsError:
	case c.interrupted || strings.HasPrefix(r.TerminalReason, "aborted"):
		ev.Status = runtime.StatusInterrupted
	default:
		ev.Status, ev.IsError = runtime.StatusError, true
		ev.Text = strings.TrimSpace(r.Subtype + " " + strings.Join(r.Errors, "; "))
	}
	c.emit(ctx, ev)
	c.inFlight, c.interrupted = false, false
}

// ---- usage accounting (C13) -------------------------------------------------

// accounting collects one turn's usage. Every assistant frame of a message
// repeats that message's usage, so it is counted once per message id. Those
// per-message counts are exact for input and cache tokens, but output_tokens
// is a snapshot taken when the message started (measured live on 2.1.331: 22
// against a turn total of 206); the result frame's usage carries the turn's
// real totals, so flushUsage tops the last message up to them.
type accounting struct {
	order    []string
	usage    map[string]*runtime.Usage
	tools    map[string]struct{}
	resultID string
}

func newAccounting() accounting {
	return accounting{usage: map[string]*runtime.Usage{}, tools: map[string]struct{}{}}
}

func (a *accounting) observe(id, model string, u wireUsage) {
	if _, ok := a.usage[id]; ok {
		return
	}
	a.order = append(a.order, id)
	a.usage[id] = &runtime.Usage{MessageID: id, Model: model,
		Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
}

func (a *accounting) firstToolUse(id string) bool {
	if _, ok := a.tools[id]; ok {
		return false
	}
	a.tools[id] = struct{}{}
	return true
}

// flushUsage emits the turn's Usage events and resets the accounting. total
// is the result frame's usage, or nil when the turn ended without one.
func (c *Conn) flushUsage(ctx context.Context, total *wireUsage) {
	a := &c.acct
	if total != nil {
		var sum wireUsage
		for _, id := range a.order {
			u := a.usage[id]
			sum.Input += u.Input
			sum.Output += u.Output
			sum.CacheRead += u.CacheRead
			sum.CacheWrite += u.CacheWrite
		}
		gap := wireUsage{
			Input:      max(total.Input-sum.Input, 0),
			Output:     max(total.Output-sum.Output, 0),
			CacheRead:  max(total.CacheRead-sum.CacheRead, 0),
			CacheWrite: max(total.CacheWrite-sum.CacheWrite, 0),
		}
		if gap != (wireUsage{}) {
			if len(a.order) == 0 {
				// Tokens were billed but no assistant frame named a message
				// (e.g. a call aborted before it streamed).
				a.observe("result:"+a.resultID, "", wireUsage{})
			}
			u := a.usage[a.order[len(a.order)-1]]
			u.Input += gap.Input
			u.Output += gap.Output
			u.CacheRead += gap.CacheRead
			u.CacheWrite += gap.CacheWrite
		}
	}
	for _, id := range a.order {
		c.emit(ctx, runtime.Event{Kind: runtime.UsageEvent, Turn: c.turn, Usage: a.usage[id]})
	}
	*a = newAccounting()
}
