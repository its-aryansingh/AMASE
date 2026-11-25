package claude

// Conformance: recorded Claude Code 2.1.331 sessions are replayed through the
// fake transport, and every line amase writes is checked against what was
// recorded. The WP-1.0 golden trace is compared byte for byte; the WP-2.1a
// probe traces were recorded by a driver that sorted JSON keys, so their
// amase-side lines are compared as JSON values.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/its-aryansingh/amase/internal/runtime"
	"github.com/its-aryansingh/amase/internal/runtime/fake"
)

const goldenProvider = "96ea6bd6-e5d2-40b0-a722-1233bf898020"

func goldenSteps(t *testing.T) []fake.Step {
	t.Helper()
	steps, err := fake.LoadTrace(filepath.Join("..", "..", "..", "testdata", "golden", "claude", "duplex-allow-then-hook-deny.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

func probeSteps(t *testing.T, name string) []fake.Step {
	t.Helper()
	steps, err := fake.LoadTrace(filepath.Join("..", "fake", "testdata", "claude", name))
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// session drives a Conn the way the recording shows Claude Code being
// driven, through a fake.Peer, and collects what the Conn reports.
type session struct {
	t    *testing.T
	conn *Conn
	peer *fake.Peer

	mu     sync.Mutex
	events []runtime.Event

	completed  chan runtime.Event // every TurnCompleted, in order
	nCompleted int                // TurnCompleted events consumed by waitCompleted
	results    int                // result frames the replay has sent so far
	actions    chan error         // results of asynchronous Initialize / Interrupt calls
	pending    int                // asynchronous actions not yet collected
}

func newSession(t *testing.T, gate runtime.Gate, provider string) *session {
	t.Helper()
	stdout, stdin, peer := fake.Pipe()
	s := &session{
		t:         t,
		conn:      NewConn(stdout, stdin, Config{SessionID: "amase-s1", ProviderID: provider, Gate: gate}),
		peer:      peer,
		completed: make(chan runtime.Event, 16),
		actions:   make(chan error, 16),
	}
	runErr := make(chan error, 1)
	go func() { runErr <- s.conn.Run(context.Background()) }()
	go func() {
		for ev := range s.conn.Events() {
			s.mu.Lock()
			s.events = append(s.events, ev)
			s.mu.Unlock()
			if ev.Kind == runtime.TurnCompleted {
				s.completed <- ev
			}
		}
	}()
	t.Cleanup(func() {
		_ = peer.CloseSend()
		select {
		case err := <-runErr:
			if err != nil {
				t.Errorf("Run = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after the stream closed")
		}
		_ = peer.Close()
	})
	return s
}

func (s *session) waitCompleted(n int) {
	s.t.Helper()
	for s.nCompleted < n {
		select {
		case <-s.completed:
			s.nCompleted++
		case <-time.After(5 * time.Second):
			s.t.Fatalf("timed out waiting for TurnCompleted #%d", n)
		}
	}
}

// act is a fake.Options.Before callback. For each amase-side step it makes
// the Conn do what the recording shows amase doing; replies to can_use_tool
// need no action because the Conn writes them itself. Before acting it waits
// until the Conn has finished every turn the recording has finished, so a
// turn never starts while the previous result frame is still in flight.
func (s *session) act(ctx context.Context) func(int, fake.Step) {
	return func(i int, st fake.Step) {
		var m struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Request struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		_ = json.Unmarshal(st.Msg, &m)
		if st.Dir == fake.Out {
			if m.Type == "result" {
				s.results++
			}
			return
		}
		if m.Type == "control_response" {
			return
		}
		s.waitCompleted(s.results)
		switch {
		case m.Type == "user":
			if _, err := s.conn.SendTurn(ctx, m.Message.Content); err != nil {
				s.t.Fatalf("step %d: SendTurn: %v", i, err)
			}
		case m.Type == "control_request" && m.Request.Subtype == "initialize":
			s.async(func() error { _, err := s.conn.Initialize(ctx); return err })
		case m.Type == "control_request" && m.Request.Subtype == "interrupt":
			s.async(func() error { return s.conn.Interrupt(ctx) })
		default:
			s.t.Fatalf("step %d: no action for amase-side frame %s", i, st.Msg)
		}
	}
}

func (s *session) async(f func() error) {
	s.pending++
	go func() { s.actions <- f() }()
}

// settle collects the asynchronous actions and checks that amase wrote
// nothing beyond the recording.
func (s *session) settle() {
	s.t.Helper()
	for ; s.pending > 0; s.pending-- {
		select {
		case err := <-s.actions:
			if err != nil {
				s.t.Errorf("action: %v", err)
			}
		case <-time.After(5 * time.Second):
			s.t.Fatal("an Initialize or Interrupt call never returned")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if l, err := s.peer.Recv(ctx); err == nil {
		s.t.Errorf("amase wrote a line the recording does not have: %.300s", l)
	}
}

func (s *session) snapshot() []runtime.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]runtime.Event(nil), s.events...)
}

func kinds(evs []runtime.Event) string {
	k := make([]string, len(evs))
	for i, ev := range evs {
		k[i] = string(ev.Kind)
	}
	return strings.Join(k, " ")
}

// recorder is a Gate that records requests and answers with decide.
type recorder struct {
	mu     sync.Mutex
	seen   []runtime.ApprovalRequest
	decide func(context.Context, runtime.ApprovalRequest) runtime.Decision
}

func (r *recorder) Decide(ctx context.Context, req runtime.ApprovalRequest) runtime.Decision {
	r.mu.Lock()
	r.seen = append(r.seen, req)
	r.mu.Unlock()
	return r.decide(ctx, req)
}

func (r *recorder) requests() []runtime.ApprovalRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]runtime.ApprovalRequest(nil), r.seen...)
}

func allowAll() *recorder {
	return &recorder{decide: func(context.Context, runtime.ApprovalRequest) runtime.Decision {
		return runtime.Decision{Verdict: runtime.Allow, DecidedBy: "policy"}
	}}
}

const goldenKinds = "TurnStarted ToolCall ToolResult ToolCall ToolResult Text Usage Usage Usage TurnCompleted"

func TestConformanceAllowByteForByte(t *testing.T) {
	ctx := testCtx(t)
	gate := allowAll()
	s := newSession(t, gate, goldenProvider)
	err := s.peer.Replay(ctx, goldenSteps(t), fake.Options{
		Exact:   true,
		Replace: strings.NewReplacer(`"init_1"`, `"amase_1"`),
		Before:  s.act(ctx),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.waitCompleted(1)
	s.settle()

	evs := s.snapshot()
	if got := kinds(evs); got != goldenKinds {
		t.Fatalf("events:\n got %s\nwant %s", got, goldenKinds)
	}
	if last := evs[len(evs)-1]; last.Status != runtime.StatusOK {
		t.Fatalf("TurnCompleted = %+v", last)
	}
	reqs := gate.requests()
	if len(reqs) != 1 || reqs[0].Command != "echo amase-probe > probe.txt" || reqs[0].CallID != "toolu_01V77wNXFA6vwZGF5LK6LKmL" {
		t.Fatalf("approval requests = %+v", reqs)
	}
}

func TestConformanceDeny(t *testing.T) {
	ctx := testCtx(t)
	decision := runtime.Decision{Verdict: runtime.Deny, DecidedBy: "policy",
		Reason: "deleting README.md is not allowed", Rules: []string{"TG-PROBE"}}
	want := denyMessage(decision)
	gate := &recorder{decide: func(context.Context, runtime.ApprovalRequest) runtime.Decision { return decision }}
	s := newSession(t, gate, "3851b2c3-c7f2-4663-bb96-d95a21750557")

	// Claude Code returns the deny message to the model verbatim (C3), so the
	// probe's own message is swapped for amase's on both sides of the trace.
	const recorded = "amase denied this call (rule TG-PROBE): deleting README.md is not allowed. Do not retry."
	err := s.peer.Replay(ctx, probeSteps(t, "can-use-tool-deny.ndjson"), fake.Options{
		Replace: strings.NewReplacer(`"init_1"`, `"amase_1"`, recorded, want),
		Before:  s.act(ctx),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.waitCompleted(1)
	s.settle()

	evs := s.snapshot()
	if got, w := kinds(evs), "TurnStarted ToolCall ToolResult Text Usage Usage TurnCompleted"; got != w {
		t.Fatalf("events:\n got %s\nwant %s", got, w)
	}
	if res := evs[2]; !res.IsError || res.Text != want || res.CallID != "toolu_01UUU3bUGVLvf416PfHbA6Dz" {
		t.Fatalf("ToolResult = %+v", res)
	}
	// A denied call does not fail the turn: the model reads the reason and stops.
	if last := evs[len(evs)-1]; last.Status != runtime.StatusOK || evs[3].Text != "BLOCKED" {
		t.Fatalf("Text = %q, TurnCompleted = %+v", evs[3].Text, last)
	}
	if reqs := gate.requests(); len(reqs) != 1 || reqs[0].Tool != "Bash" || reqs[0].Command != "rm README.md" {
		t.Fatalf("approval requests = %+v", reqs)
	}
}

func TestConformanceEscalateBlocksUntilBrokerAnswers(t *testing.T) {
	ctx := testCtx(t)
	asked := make(chan runtime.ApprovalRequest, 1)
	answer := make(chan runtime.Decision)
	broker := &recorder{decide: func(ctx context.Context, req runtime.ApprovalRequest) runtime.Decision {
		asked <- req
		select {
		case d := <-answer:
			return d
		case <-ctx.Done():
			return runtime.Decision{Verdict: runtime.Deny, DecidedBy: "timeout", Reason: "no decision"}
		}
	}}
	s := newSession(t, broker, goldenProvider)
	act := s.act(ctx)
	err := s.peer.Replay(ctx, goldenSteps(t), fake.Options{
		Exact:   true,
		Replace: strings.NewReplacer(`"init_1"`, `"amase_1"`),
		Before: func(i int, st fake.Step) {
			act(i, st)
			if i != 12 { // the recorded allow reply
				return
			}
			select {
			case req := <-asked:
				if req.Command != "echo amase-probe > probe.txt" {
					t.Fatalf("broker asked about %+v", req)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the broker was never asked")
			}
			quiet, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
			if l, err := s.peer.Recv(quiet); err == nil {
				t.Fatalf("amase replied before the broker answered: %s", l)
			}
			answer <- runtime.Decision{Verdict: runtime.Allow, DecidedBy: "human"}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.waitCompleted(1)
	s.settle()
	if got := kinds(s.snapshot()); got != goldenKinds {
		t.Fatalf("events:\n got %s\nwant %s", got, goldenKinds)
	}
}

func TestConformanceInterrupt(t *testing.T) {
	ctx := testCtx(t)
	withdrawn := make(chan struct{})
	gate := &recorder{decide: func(ctx context.Context, req runtime.ApprovalRequest) runtime.Decision {
		if !strings.Contains(req.Command, "pending-probe") {
			return runtime.Decision{Verdict: runtime.Allow, DecidedBy: "policy"}
		}
		// A person is still deciding when the interrupt arrives.
		<-ctx.Done()
		close(withdrawn)
		return runtime.Decision{Verdict: runtime.Deny, DecidedBy: "timeout"}
	}}
	s := newSession(t, gate, "e796569f-0067-4616-bae1-4590e08701c1")
	// Initialize and the three interrupts take amase's request ids in order.
	ids := strings.NewReplacer(`"init_1"`, `"amase_1"`, `"int_running"`, `"amase_2"`,
		`"int_pending"`, `"amase_3"`, `"int_idle"`, `"amase_4"`)
	err := s.peer.Replay(ctx, probeSteps(t, "interrupt-running-pending-idle.ndjson"), fake.Options{
		Replace: ids,
		Before:  s.act(ctx),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.settle()

	select {
	case <-withdrawn:
	case <-time.After(5 * time.Second):
		t.Fatal("control_cancel_request did not cancel the pending decision")
	}
	var statuses []string
	for _, ev := range s.snapshot() {
		if ev.Kind == runtime.TurnCompleted {
			statuses = append(statuses, fmt.Sprintf("%d:%s", ev.Turn, ev.Status))
		}
	}
	// A running tool killed, a pending approval withdrawn, then the session
	// still answers a third turn.
	if got := strings.Join(statuses, " "); got != "1:interrupted 2:interrupted 3:ok" {
		t.Fatalf("turn statuses = %s", got)
	}
}

func TestConformanceMalformedAnd2MiBLines(t *testing.T) {
	ctx := testCtx(t)
	gate := allowAll()
	s := newSession(t, gate, goldenProvider)

	big := strings.Repeat("z", 2<<20)
	input := `{"file_path": "C:\\work\\demo-repo\\big.txt", "content": "` + big + `"}`
	injected := []fake.Step{
		{Dir: fake.Out, Msg: json.RawMessage(`this is not json`)},
		{Dir: fake.Out, Msg: json.RawMessage(`{"type": "assistant", "message": {"id": "msg_big", "content": [{"type": "text", "text": "` + big + `"}]}}`)},
		{Dir: fake.Out, Msg: json.RawMessage(`{"type": "control_request", "request_id": "big-1", "request": {"subtype": "can_use_tool", "tool_name": "Write", "display_name": "Write", "input": ` + input + `, "tool_use_id": "toolu_big"}}`)},
		// The 2 MiB input comes back as updatedInput, byte for byte (C3).
		{Dir: fake.In, Msg: json.RawMessage(`{"type": "control_response", "response": {"subtype": "success", "request_id": "big-1", "response": {"behavior": "allow", "updatedInput": ` + input + `}}}`)},
	}
	steps := goldenSteps(t)
	steps = append(append(append([]fake.Step(nil), steps[:4]...), injected...), steps[4:]...)

	err := s.peer.Replay(ctx, steps, fake.Options{
		Exact:   true,
		Replace: strings.NewReplacer(`"init_1"`, `"amase_1"`),
		Before:  s.act(ctx),
	})
	if err != nil {
		var mm *fake.MismatchError
		if errors.As(err, &mm) {
			t.Fatalf("step %d differs (got %d bytes, want %d)", mm.Step, len(mm.Got), len(mm.Want))
		}
		t.Fatal(err)
	}
	s.waitCompleted(1)
	s.settle()

	evs := s.snapshot()
	if got, want := kinds(evs), "TurnStarted Text "+strings.TrimPrefix(goldenKinds, "TurnStarted "); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	if len(evs[1].Text) != 2<<20 {
		t.Fatalf("2 MiB text arrived as %d bytes", len(evs[1].Text))
	}
	if n := s.conn.MalformedLines(); n != 1 {
		t.Fatalf("MalformedLines = %d, want 1", n)
	}
	if reqs := gate.requests(); len(reqs) != 2 || reqs[0].Tool != "Write" || len(reqs[0].Input) < 2<<20 {
		t.Fatalf("approval requests: %d", len(reqs))
	}
}
