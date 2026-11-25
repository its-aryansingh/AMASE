package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/its-aryansingh/amase/internal/runtime"
)

func readGolden(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join("..", "..", "..", "testdata", "golden", "claude", name))
}

// fakeCLI plays Claude Code's side of the pipes.
type fakeCLI struct {
	t      *testing.T
	conn   *Conn
	out    *io.PipeWriter // what the CLI writes (amase reads)
	lines  chan []byte    // what amase wrote
	events chan runtime.Event
	runErr chan error
}

func newFakeCLI(t *testing.T, gate runtime.Gate, provider string) *fakeCLI {
	t.Helper()
	outR, outW := io.Pipe()
	inR, inW := io.Pipe()
	f := &fakeCLI{
		t:      t,
		conn:   NewConn(outR, inW, Config{SessionID: "amase-s1", ProviderID: provider, Gate: gate}),
		out:    outW,
		lines:  make(chan []byte, 64),
		events: make(chan runtime.Event, 1024),
		runErr: make(chan error, 1),
	}
	go func() {
		r := bufio.NewReader(inR)
		for {
			l, err := r.ReadBytes('\n')
			if len(l) > 0 {
				f.lines <- l
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		for ev := range f.conn.Events() {
			f.events <- ev
		}
		close(f.events)
	}()
	go func() { f.runErr <- f.conn.Run(context.Background()) }()
	t.Cleanup(func() {
		outW.Close()
		select {
		case <-f.runErr:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after the stream closed")
		}
		inR.Close()
	})
	return f
}

func (f *fakeCLI) write(line string) {
	f.t.Helper()
	if _, err := f.out.Write([]byte(line + "\n")); err != nil {
		f.t.Fatalf("fake CLI write: %v", err)
	}
}

func (f *fakeCLI) read() map[string]json.RawMessage {
	f.t.Helper()
	select {
	case l := <-f.lines:
		var m map[string]json.RawMessage
		if err := json.Unmarshal(l, &m); err != nil {
			f.t.Fatalf("amase wrote invalid JSON: %s", l)
		}
		m["_raw"] = l
		return m
	case <-time.After(5 * time.Second):
		f.t.Fatal("timed out waiting for amase to write")
		return nil
	}
}

func (f *fakeCLI) noWrite(d time.Duration) {
	f.t.Helper()
	select {
	case l := <-f.lines:
		f.t.Fatalf("unexpected write: %s", l)
	case <-time.After(d):
	}
}

// until collects events up to and including the first of kind.
func (f *fakeCLI) until(kind runtime.EventKind) []runtime.Event {
	f.t.Helper()
	var got []runtime.Event
	for {
		select {
		case ev, ok := <-f.events:
			if !ok {
				f.t.Fatalf("events closed before %s; got %+v", kind, got)
			}
			got = append(got, ev)
			if ev.Kind == kind {
				return got
			}
		case <-time.After(5 * time.Second):
			f.t.Fatalf("timed out waiting for %s; got %+v", kind, got)
		}
	}
}

func (f *fakeCLI) startTurn(prompt string) int {
	f.t.Helper()
	type res struct {
		turn int
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		n, err := f.conn.SendTurn(context.Background(), prompt)
		ch <- res{n, err}
	}()
	m := f.read()
	if str(m["type"]) != "user" {
		f.t.Fatalf("first write was not the user turn: %s", m["_raw"])
	}
	r := <-ch
	if r.err != nil {
		f.t.Fatalf("SendTurn: %v", r.err)
	}
	return r.turn
}

func str(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// permission extracts the PermissionResult and request id from a reply.
func permission(t *testing.T, m map[string]json.RawMessage) (string, PermissionResult) {
	t.Helper()
	var body struct {
		Subtype   string           `json:"subtype"`
		RequestID string           `json:"request_id"`
		Response  PermissionResult `json:"response"`
	}
	if str(m["type"]) != "control_response" || json.Unmarshal(m["response"], &body) != nil || body.Subtype != "success" {
		t.Fatalf("not a success control_response: %s", m["_raw"])
	}
	return body.RequestID, body.Response
}

type gateFunc func(ctx context.Context, req runtime.ApprovalRequest) runtime.Decision

func (g gateFunc) Decide(ctx context.Context, req runtime.ApprovalRequest) runtime.Decision {
	return g(ctx, req)
}

func fixed(d runtime.Decision) runtime.Gate {
	return gateFunc(func(context.Context, runtime.ApprovalRequest) runtime.Decision { return d })
}

func canUseTool(id, tool, toolUseID, input string) string {
	return `{"type":"control_request","request_id":"` + id + `","request":{"subtype":"can_use_tool","tool_name":"` + tool +
		`","display_name":"` + tool + `","input":` + input + `,"tool_use_id":"` + toolUseID + `"}}`
}

func TestGoldenDuplexReplay(t *testing.T) {
	golden := goldenLines(t, "duplex-allow-then-hook-deny.ndjson")
	const provider = "96ea6bd6-e5d2-40b0-a722-1233bf898020"
	var seen []runtime.ApprovalRequest
	var mu sync.Mutex
	gate := gateFunc(func(_ context.Context, req runtime.ApprovalRequest) runtime.Decision {
		mu.Lock()
		seen = append(seen, req)
		mu.Unlock()
		return runtime.Decision{Verdict: runtime.Allow, DecidedBy: "policy"}
	})
	f := newFakeCLI(t, gate, provider)

	initDone := make(chan json.RawMessage, 1)
	go func() {
		resp, err := f.conn.Initialize(context.Background())
		if err != nil {
			t.Errorf("Initialize: %v", err)
		}
		initDone <- resp
	}()
	initReq := f.read()
	ourInitID := str(initReq["request_id"])
	var initBody, goldenInit map[string]json.RawMessage
	_ = json.Unmarshal(golden[0].Msg, &goldenInit)
	_ = json.Unmarshal(initReq["_raw"], &initBody)
	assertJSONEqual(t, initBody["request"], goldenInit["request"])

	var prompt struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	_ = json.Unmarshal(golden[1].Msg, &prompt)
	go func() {
		if _, err := f.conn.SendTurn(context.Background(), prompt.Message.Content); err != nil {
			t.Errorf("SendTurn: %v", err)
		}
	}()
	user := f.read()
	assertJSONEqual(t, user["_raw"], golden[1].Msg)

	for i, g := range golden[2:] {
		if g.Dir == "in" {
			got := f.read()
			assertJSONEqual(t, got["_raw"], g.Msg)
			continue
		}
		line := string(g.Msg)
		if i == 0 {
			line = strings.Replace(line, `"request_id": "init_1"`, `"request_id": "`+ourInitID+`"`, 1)
		}
		f.write(line)
	}
	if resp := <-initDone; !strings.Contains(string(resp), `"commands"`) {
		t.Fatalf("initialize payload missing commands: %.200s", resp)
	}

	evs := f.until(runtime.TurnCompleted)
	var kinds []string
	var usage runtime.Usage
	var usageEvents int
	for _, ev := range evs {
		kinds = append(kinds, string(ev.Kind))
		if ev.Turn != 1 {
			t.Errorf("event %s has turn %d, want 1", ev.Kind, ev.Turn)
		}
		if ev.Kind == runtime.UsageEvent {
			usageEvents++
			usage.Input += ev.Usage.Input
			usage.Output += ev.Usage.Output
			usage.CacheRead += ev.Usage.CacheRead
			usage.CacheWrite += ev.Usage.CacheWrite
		}
	}
	want := "TurnStarted ToolCall ToolResult ToolCall ToolResult Text Usage Usage Usage TurnCompleted"
	if strings.Join(kinds, " ") != want {
		t.Fatalf("events:\n got %s\nwant %s", strings.Join(kinds, " "), want)
	}
	// Three messages, each seen on two frames: counted once (C13). The result
	// frame's totals win for output tokens, which the frames under-report.
	if usageEvents != 3 || usage.Input != 6 || usage.Output != 561 || usage.CacheRead != 73449 || usage.CacheWrite != 14570 {
		t.Fatalf("usage events=%d totals=%+v", usageEvents, usage)
	}
	last := evs[len(evs)-1]
	if last.Status != runtime.StatusOK || last.CostUSD != 0.00392959 {
		t.Fatalf("TurnCompleted = %+v", last)
	}
	if evs[2].CallID != "toolu_01V77wNXFA6vwZGF5LK6LKmL" || evs[2].IsError || !strings.Contains(evs[2].Text, "Shell cwd was reset") {
		t.Fatalf("first ToolResult = %+v", evs[2])
	}
	if !evs[4].IsError || !strings.Contains(evs[4].Text, "deletion denied by the hook") {
		t.Fatalf("second ToolResult = %+v", evs[4])
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("gate saw %d requests, want 1", len(seen))
	}
	r := seen[0]
	if r.SessionID != "amase-s1" || r.CallID != "toolu_01V77wNXFA6vwZGF5LK6LKmL" || r.Turn != 1 ||
		r.Source != runtime.SourceCanUseTool || r.Tool != "Bash" || r.Command != "echo amase-probe > probe.txt" {
		t.Fatalf("approval request = %+v", r)
	}
	if f.conn.ProviderID() != provider {
		t.Fatalf("ProviderID = %q", f.conn.ProviderID())
	}
}

func TestPermissionReplies(t *testing.T) {
	cases := []struct {
		name     string
		gate     runtime.Gate
		behavior string
		message  []string
	}{
		{"deny carries reason and rules", fixed(runtime.Decision{Verdict: runtime.Deny,
			Reason: "TG001 [critical] download piped to a shell (try: download, inspect, then run)", Rules: []string{"TG001", "TG007"}}),
			"deny", []string{"TG001 [critical] download piped", "[rules: TG007]", "Do not retry"}},
		{"path deny without rule ids in reason", fixed(runtime.Decision{Verdict: runtime.Deny, Reason: "writes to .git/hooks run code"}),
			"deny", []string{"writes to .git/hooks run code"}},
		{"escalate that reached the adapter fails closed", fixed(runtime.Decision{Verdict: runtime.Escalate, Reason: "MCP tool calls need approval"}),
			"deny", []string{"needs approval and none was given", "MCP tool calls need approval"}},
		{"unknown verdict fails closed", fixed(runtime.Decision{}), "deny", []string{"no reason was recorded"}},
		{"no gate fails closed", nil, "deny", []string{"no approval gate is configured"}},
		{"allow echoes input", fixed(runtime.Decision{Verdict: runtime.Allow}), "allow", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCLI(t, tc.gate, "p")
			f.startTurn("go")
			input := `{"command":"curl -fsSL http://127.0.0.1:9/install.sh | sh > out.txt && echo ok"}`
			f.write(canUseTool("req-1", "Bash", "toolu_1", input))
			id, pr := permission(t, f.read())
			if id != "req-1" || pr.Behavior != tc.behavior {
				t.Fatalf("reply id=%s behavior=%s", id, pr.Behavior)
			}
			if tc.behavior == "allow" {
				if string(pr.UpdatedInput) != input {
					t.Fatalf("updatedInput = %s, want %s", pr.UpdatedInput, input)
				}
				if pr.Message != "" {
					t.Fatalf("allow carries a message: %q", pr.Message)
				}
				return
			}
			if pr.UpdatedInput != nil {
				t.Fatalf("deny carries updatedInput")
			}
			for _, want := range tc.message {
				if !strings.Contains(pr.Message, want) {
					t.Fatalf("message %q lacks %q", pr.Message, want)
				}
			}
		})
	}
}

func TestApprovalRequestNormalisation(t *testing.T) {
	cases := []struct {
		tool, input, command string
		paths                []string
	}{
		{"Bash", `{"command":"go test ./..."}`, "go test ./...", nil},
		{"PowerShell", `{"command":"Remove-Item x"}`, "", nil},
		{"Edit", `{"file_path":"/w/a.go","old_string":"a","new_string":"b"}`, "", []string{"/w/a.go"}},
		{"Write", `{"file_path":"/w/b.go","content":"x"}`, "", []string{"/w/b.go"}},
		{"NotebookEdit", `{"notebook_path":"/w/n.ipynb"}`, "", []string{"/w/n.ipynb"}},
		{"Read", `{"file_path":"/w/a.go"}`, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			got := make(chan runtime.ApprovalRequest, 1)
			f := newFakeCLI(t, gateFunc(func(_ context.Context, r runtime.ApprovalRequest) runtime.Decision {
				got <- r
				return runtime.Decision{Verdict: runtime.Allow}
			}), "p")
			f.startTurn("go")
			f.write(canUseTool("r", tc.tool, "toolu_x", tc.input))
			f.read()
			r := <-got
			if r.Tool != tc.tool || r.Command != tc.command || strings.Join(r.Paths, ",") != strings.Join(tc.paths, ",") || string(r.Input) != tc.input {
				t.Fatalf("request = %+v", r)
			}
		})
	}
}

func TestEscalationBlocksOnlyItsOwnRequest(t *testing.T) {
	release := make(chan struct{})
	gate := gateFunc(func(ctx context.Context, _ runtime.ApprovalRequest) runtime.Decision {
		select {
		case <-release:
			return runtime.Decision{Verdict: runtime.Allow, DecidedBy: "human"}
		case <-ctx.Done():
			return runtime.Decision{Verdict: runtime.Deny, DecidedBy: "timeout"}
		}
	})
	f := newFakeCLI(t, gate, "p")
	f.startTurn("go")
	f.until(runtime.TurnStarted)
	f.write(canUseTool("req-wait", "Bash", "toolu_w", `{"command":"make deploy"}`))
	// The stream keeps flowing while a person decides.
	f.write(`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"still streaming"}]}}`)
	if ev := f.until(runtime.Text); ev[len(ev)-1].Text != "still streaming" {
		t.Fatalf("got %+v", ev)
	}
	f.noWrite(50 * time.Millisecond)
	close(release)
	if id, pr := permission(t, f.read()); id != "req-wait" || pr.Behavior != "allow" {
		t.Fatalf("reply %s %+v", id, pr)
	}
}

func TestCancelRequestWithdrawsDecision(t *testing.T) {
	cancelled := make(chan struct{})
	gate := gateFunc(func(ctx context.Context, _ runtime.ApprovalRequest) runtime.Decision {
		<-ctx.Done()
		close(cancelled)
		return runtime.Decision{Verdict: runtime.Deny}
	})
	f := newFakeCLI(t, gate, "p")
	f.startTurn("go")
	f.write(canUseTool("req-c", "Bash", "toolu_c", `{"command":"echo x > y"}`))
	f.write(`{"type":"control_cancel_request","request_id":"req-c"}`)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("gate context was not cancelled")
	}
	f.noWrite(50 * time.Millisecond)
}

func TestInterruptEndsTurnAsInterrupted(t *testing.T) {
	f := newFakeCLI(t, fixed(runtime.Decision{Verdict: runtime.Allow}), "p")
	f.startTurn("go")
	errc := make(chan error, 1)
	go func() { errc <- f.conn.Interrupt(context.Background()) }()
	req := f.read()
	if str(req["type"]) != "control_request" || string(req["request"]) != `{"subtype":"interrupt"}` {
		t.Fatalf("interrupt frame = %s", req["_raw"])
	}
	id := str(req["request_id"])
	f.write(`{"type":"control_response","response":{"subtype":"success","request_id":"` + id + `","response":{"still_queued":[]}}}`)
	if err := <-errc; err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	// Shape recorded live from 2.1.331 after an interrupt during a running tool.
	f.write(`{"type":"result","subtype":"error_during_execution","is_error":true,"stop_reason":"tool_use","terminal_reason":"aborted_tools","total_cost_usd":0.0016,"usage":{"input_tokens":2,"output_tokens":135,"cache_read_input_tokens":19256,"cache_creation_input_tokens":7158},"errors":["[ede_diagnostic] result_type=user"]}`)
	evs := f.until(runtime.TurnCompleted)
	if last := evs[len(evs)-1]; last.Status != runtime.StatusInterrupted {
		t.Fatalf("TurnCompleted = %+v", last)
	}
	// The session stays usable.
	if n := f.startTurn("again"); n != 2 {
		t.Fatalf("next turn = %d", n)
	}
}

func TestInterruptErrorResponse(t *testing.T) {
	f := newFakeCLI(t, nil, "p")
	errc := make(chan error, 1)
	go func() { errc <- f.conn.Interrupt(context.Background()) }()
	id := str(f.read()["request_id"])
	f.write(`{"type":"control_response","response":{"subtype":"error","request_id":"` + id + `","error":"nope","error_code":"x"}}`)
	var ce *ControlError
	if err := <-errc; !errors.As(err, &ce) || ce.Message != "nope" || ce.Code != "x" {
		t.Fatalf("err = %v", err)
	}
}

func TestUnsupportedControlRequest(t *testing.T) {
	f := newFakeCLI(t, nil, "p")
	f.write(`{"type":"control_request","request_id":"hk-1","request":{"subtype":"hook_callback","callback_id":"c"}}`)
	got := f.read()
	assertJSONEqual(t, got["_raw"], []byte(`{"type":"control_response","response":{"subtype":"error","request_id":"hk-1","error":"Unsupported control request subtype: hook_callback","error_code":"unsupported_request"}}`))
}

func TestMalformedAndHugeLines(t *testing.T) {
	f := newFakeCLI(t, nil, "p")
	f.startTurn("go")
	f.write("this is not json")
	f.write(`{"type":"amase_unknown","x":1}`)
	big := strings.Repeat("z", 2<<20)
	f.write(`{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":"` + big + `"}]}}`)
	evs := f.until(runtime.Text)
	if len(evs[len(evs)-1].Text) != 2<<20 {
		t.Fatalf("2 MiB text came through as %d bytes", len(evs[len(evs)-1].Text))
	}
	if n := f.conn.MalformedLines(); n != 1 {
		t.Fatalf("MalformedLines = %d, want 1", n)
	}
}

func TestStreamEndMidTurn(t *testing.T) {
	f := newFakeCLI(t, nil, "p")
	f.startTurn("go")
	f.write(`{"type":"assistant","message":{"id":"m1","model":"haiku","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":3,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`)
	f.out.Close()
	evs := f.until(runtime.TurnCompleted)
	kinds := make([]string, len(evs))
	for i, ev := range evs {
		kinds[i] = string(ev.Kind)
	}
	if got := strings.Join(kinds, " "); got != "TurnStarted Text Usage SessionError TurnCompleted" {
		t.Fatalf("events = %s", got)
	}
	if last := evs[len(evs)-1]; last.Status != runtime.StatusError {
		t.Fatalf("TurnCompleted = %+v", last)
	}
	if _, ok := <-f.events; ok {
		t.Fatal("events not closed after the stream ended")
	}
	if err := <-f.runErr; err != nil {
		t.Fatalf("Run = %v, want nil on EOF", err)
	}
	f.runErr <- nil // for the cleanup
	if _, err := f.conn.SendTurn(context.Background(), "late"); !errors.Is(err, ErrClosed) {
		t.Fatalf("SendTurn after close = %v", err)
	}
	if err := f.conn.Interrupt(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Interrupt after close = %v", err)
	}
}

func TestOneTurnAtATimeAndCostPerTurn(t *testing.T) {
	f := newFakeCLI(t, nil, "p")
	f.startTurn("one")
	if _, err := f.conn.SendTurn(context.Background(), "two"); !errors.Is(err, ErrTurnInProgress) {
		t.Fatalf("second SendTurn = %v", err)
	}
	// total_cost_usd accumulates across a process's turns (measured live).
	f.write(`{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.003}`)
	first := f.until(runtime.TurnCompleted)
	f.startTurn("two")
	f.write(`{"type":"system","subtype":"init","session_id":"p2"}`)
	f.write(`{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.0045}`)
	second := f.until(runtime.TurnCompleted)
	c1, c2 := first[len(first)-1].CostUSD, second[len(second)-1].CostUSD
	if c1 != 0.003 || c2 < 0.00149 || c2 > 0.00151 {
		t.Fatalf("per-turn costs = %v, %v", c1, c2)
	}
	if f.conn.ProviderID() != "p2" {
		t.Fatalf("ProviderID = %q", f.conn.ProviderID())
	}
}

func TestErrorResultAndUsageTopUp(t *testing.T) {
	f := newFakeCLI(t, nil, "p")
	f.startTurn("go")
	frame := `{"type":"assistant","message":{"id":"m1","model":"haiku","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":2,"output_tokens":6,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}`
	f.write(frame)
	f.write(frame) // a repeated frame: same message, same tool_use
	f.write(`{"type":"result","subtype":"error_max_budget_usd","is_error":true,"errors":["budget"],"usage":{"input_tokens":2,"output_tokens":90,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}`)
	evs := f.until(runtime.TurnCompleted)
	var calls, usages int
	for _, ev := range evs {
		switch ev.Kind {
		case runtime.ToolCall:
			calls++
		case runtime.UsageEvent:
			usages++
			if ev.Usage.MessageID != "m1" || ev.Usage.Output != 90 || ev.Usage.Input != 2 || ev.Usage.Model != "haiku" {
				t.Fatalf("usage = %+v", ev.Usage)
			}
		}
	}
	if calls != 1 || usages != 1 {
		t.Fatalf("calls=%d usages=%d", calls, usages)
	}
	last := evs[len(evs)-1]
	if last.Status != runtime.StatusError || !last.IsError || !strings.Contains(last.Text, "error_max_budget_usd") {
		t.Fatalf("TurnCompleted = %+v", last)
	}
}
