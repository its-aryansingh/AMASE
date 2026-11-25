package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/its-aryansingh/amase/internal/event"
	"github.com/its-aryansingh/amase/internal/policy"
	"github.com/its-aryansingh/amase/internal/runtime"
)

// payload holds the hook fields amase reads (protocol fact C12). Claude sends
// more; unknown fields are ignored so a harness upgrade cannot break decoding.
type payload struct {
	SessionID     string          `json:"session_id"`
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolUseID     string          `json:"tool_use_id"`
	ToolResponse  json.RawMessage `json:"tool_response"`
	Source        string          `json:"source"` // SessionStart: startup | resume | clear | compact
}

// output is the hook reply. Field order and names follow what Claude Code
// 2.1.331 was observed to honour (protocol fact C9).
type output struct {
	HookSpecificOutput specificOutput `json:"hookSpecificOutput"`
}

type specificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
	AdditionalContext        string `json:"additionalContext,omitempty"`
}

// passThrough is the empty reply: Claude carries on with its normal flow.
var passThrough = []byte(`{}`)

var allowPreToolUse = mustMarshal(output{specificOutput{HookEventName: "PreToolUse", PermissionDecision: "allow"}})

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// Hook permission decisions (protocol fact C9).
const (
	decisionAllow = "allow"
	decisionDeny  = "deny"
	decisionAsk   = "ask"
)

// preToolUse decides the call. Allow and deny are final, so they get a
// receipt here; "ask" makes Claude route the call to can_use_tool, where the
// broker resolves it and writes the receipt for the decision that stands.
func (s *Server) preToolUse(b *binding, p *payload) []byte {
	turn := b.turn()
	req := runtime.ApprovalRequest{
		SessionID: b.session(p.SessionID), CallID: p.ToolUseID, Turn: turn,
		Source: runtime.SourcePreToolUse, Tool: p.ToolName, Input: p.ToolInput,
	}
	start := time.Now()
	d, a := s.engine.Evaluate(req, b.Workspace)
	took := time.Since(start)

	var out []byte
	switch d.Verdict {
	case runtime.Allow:
		out = allowPreToolUse
	case runtime.Escalate:
		out = mustMarshal(output{specificOutput{
			HookEventName: "PreToolUse", PermissionDecision: decisionAsk,
			PermissionDecisionReason: withReason("amase needs a person to approve this call", d.Reason),
		}})
	default:
		// Anything that is not allow or escalate, including a verdict this code
		// does not know, is a deny: the hook is the enforcement point.
		d.Verdict = runtime.Deny
		out = mustMarshal(output{specificOutput{
			HookEventName: "PreToolUse", PermissionDecision: decisionDeny,
			PermissionDecisionReason: withReason("Blocked by amase policy", d.Reason),
		}})
	}

	action := clampJSON(mustMarshal(a), maxEventAction)
	s.emit(b, event.ToolRequested, turn, event.ToolRequestedData{
		CallID: p.ToolUseID, Tool: p.ToolName, Input: clampJSON(p.ToolInput, event.MaxDataBytes-len(action)-eventSlack),
		Action: action, Fingerprint: d.Fingerprint,
	})
	s.emit(b, event.PolicyDecided, turn, event.PolicyDecidedData{
		CallID: p.ToolUseID, Verdict: string(d.Verdict), Rules: d.Rules, Reason: d.Reason,
		Fingerprint: d.Fingerprint, Source: runtime.SourcePreToolUse, DecidedInNs: took.Nanoseconds(),
	})
	if d.Verdict != runtime.Escalate && (p.ToolUseID == "" || s.receipts.First(req.SessionID, p.ToolUseID)) {
		s.rec.Receipt(policy.NewReceipt(req, d, a))
	}
	return out
}

func withReason(lead, reason string) string {
	if reason == "" {
		return lead
	}
	return lead + ": " + reason
}

// A ToolRequested payload must fit event.MaxDataBytes or the event is lost,
// so the action is capped first and the input gets what remains.
const (
	maxEventAction = 16 << 10
	eventSlack     = 2 << 10 // call id, tool name, fingerprint, JSON framing
)

// clampJSON returns raw when it fits in max bytes, else its size, hash and a
// prefix, so one large Write cannot bloat the event log or drop the event.
func clampJSON(raw json.RawMessage, max int) json.RawMessage {
	if len(raw) <= max {
		return raw
	}
	sum := sha256.Sum256(raw)
	return mustMarshal(struct {
		Bytes  int    `json:"bytes"`
		SHA256 string `json:"sha256"`
		Prefix string `json:"prefix"`
	}{len(raw), hex.EncodeToString(sum[:]), event.Clip(string(raw), 4000)})
}

func (s *Server) postToolUse(b *binding, p *payload) []byte {
	turn := b.turn()
	o := toolOutput(p)
	text := o.text()
	sum := sha256.Sum256(text)
	s.emit(b, event.ToolCompleted, turn, event.ToolCompletedData{
		CallID: p.ToolUseID, IsError: o.Interrupted, OutputBytes: len(text),
		OutputSHA256: hex.EncodeToString(sum[:]), OutputPrefix: event.Clip(string(text), 4000),
	})
	if s.attrition != nil {
		if kind, found := s.attrition(o); found {
			s.emit(b, event.AttritionDetected, turn, event.AttritionDetectedData{CallID: p.ToolUseID, Kind: kind})
		}
	}
	if s.advisor != nil {
		if advice, ok := s.advisor.Advice(b.Session); ok && advice != "" {
			return mustMarshal(output{specificOutput{HookEventName: "PostToolUse", AdditionalContext: advice}})
		}
	}
	return passThrough
}

func toolOutput(p *payload) ToolOutput {
	o := ToolOutput{CallID: p.ToolUseID, Tool: p.ToolName, Raw: p.ToolResponse}
	var shell struct {
		Stdout      *string `json:"stdout"`
		Stderr      string  `json:"stderr"`
		Interrupted bool    `json:"interrupted"`
	}
	if len(p.ToolResponse) > 0 && p.ToolResponse[0] == '{' && json.Unmarshal(p.ToolResponse, &shell) == nil && shell.Stdout != nil {
		o.Shell, o.Stdout, o.Stderr, o.Interrupted = true, *shell.Stdout, shell.Stderr, shell.Interrupted
	}
	return o
}

// text is the output as the agent saw it: stdout then stderr for shell tools,
// the string itself for a string response, the JSON otherwise.
func (o ToolOutput) text() []byte {
	if o.Shell {
		return []byte(o.Stdout + o.Stderr)
	}
	var s string
	if len(o.Raw) > 0 && o.Raw[0] == '"' && json.Unmarshal(o.Raw, &s) == nil {
		return []byte(s)
	}
	return o.Raw
}

// sessionStart re-pins the session's invariants after compaction, the moment
// they are most likely to have been summarised away. Other sources pass.
func (s *Server) sessionStart(b *binding, p *payload) []byte {
	if p.Source != "compact" || s.pinner == nil {
		return passThrough
	}
	pin, ok := s.pinner(b.Binding)
	if !ok || pin.Context == "" {
		return passThrough
	}
	s.emit(b, event.PinInjected, b.turn(), event.PinInjectedData{BundleSHA256: pin.BundleSHA256, TokensEst: pin.TokensEst})
	return mustMarshal(output{specificOutput{HookEventName: "SessionStart", AdditionalContext: pin.Context}})
}

// StopReasonHook marks the TurnCompleted the Stop hook records. Claude runs
// Stop hooks before it writes the stream's result frame, so in a managed
// session the adapter's TurnCompleted (with cost and final status) follows
// this one and is the one the projection keeps.
const StopReasonHook = "stop_hook"

func (s *Server) stop(b *binding, _ *payload) []byte {
	s.emit(b, event.TurnCompleted, b.turn(), event.TurnCompletedData{Status: event.StatusOK, StopReason: StopReasonHook})
	return passThrough
}
