package claude

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/its-aryansingh/amase/internal/demo"
	"github.com/its-aryansingh/amase/internal/runtime"
)

// TestLiveDemoTurn drives the installed claude through one turn in a fresh
// `amase demo` repository (SPEC 8.3: live tests never touch a real repo). It
// uses subscription quota, so it runs only with AMASE_LIVE=1. The gate
// allows `go test` and denies everything else, so the model cannot change
// anything even inside the throwaway repo.
func TestLiveDemoTurn(t *testing.T) {
	if os.Getenv("AMASE_LIVE") != "1" {
		t.Skip("set AMASE_LIVE=1 to drive the installed claude (uses subscription quota)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	root := t.TempDir()
	noHooks := filepath.Join(root, "nohooks")
	if err := os.Mkdir(noHooks, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "demo")
	if _, err := demo.Create(ctx, repo, noHooks); err != nil {
		t.Fatal(err)
	}

	gate := &recorder{decide: func(_ context.Context, req runtime.ApprovalRequest) runtime.Decision {
		if req.Tool == "Bash" && (req.Command == "go test ./..." || req.Command == "go test") {
			return runtime.Decision{Verdict: runtime.Allow, DecidedBy: "policy"}
		}
		return runtime.Decision{Verdict: runtime.Deny, DecidedBy: "policy", Reason: "the live test only allows `go test ./...`"}
	}}
	a := &Adapter{SettingsDir: t.TempDir()}
	s, err := a.StartSession(ctx, runtime.SessionSpec{
		TaskID: "live", Harness: runtime.Claude, Workdir: repo, Model: "haiku", MaxUSD: 0.5,
	}, gate)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	prompt := "Use the Bash tool (not PowerShell) to run exactly this command: go test ./...\n" +
		"Then reply with one short sentence saying whether the tests pass. Do not edit any file and do not run any other command."
	if _, err := s.SendTurn(ctx, prompt); err != nil {
		t.Fatal(err)
	}
	var done runtime.Event
	var costUSD float64
	for done.Kind != runtime.TurnCompleted {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatalf("events closed mid-turn; stderr: %s", s.Stderr())
			}
			switch ev.Kind {
			case runtime.ToolCall:
				t.Logf("tool call %s %s", ev.Tool, ev.Input)
			case runtime.ToolResult:
				t.Logf("tool result is_error=%v (%d bytes)", ev.IsError, len(ev.Text))
			case runtime.Text:
				t.Logf("text: %.200s", ev.Text)
			case runtime.TurnCompleted:
				done, costUSD = ev, ev.CostUSD
			}
		case <-ctx.Done():
			t.Fatalf("no TurnCompleted: %v; stderr: %s", ctx.Err(), s.Stderr())
		}
	}
	for _, r := range gate.requests() {
		t.Logf("approval: %s %q", r.Tool, r.Command)
	}
	t.Logf("provider session %s, shadow price $%.5f", s.ProviderID(), costUSD)
	if done.Status != runtime.StatusOK {
		t.Fatalf("TurnCompleted = %+v", done)
	}

	start := time.Now()
	if err := s.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	t.Logf("graceful Close took %v", time.Since(start).Round(time.Millisecond))
	for range s.Events() { // Close guarantees the channel is closed
	}
}
