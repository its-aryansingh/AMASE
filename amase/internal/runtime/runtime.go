// Package runtime defines the contract between the workspace and the coding
// agents it drives. Each harness (Claude Code, Codex, an ACP agent) gets an
// Adapter that speaks its native protocol and normalises what it sees into
// these types; everything above the adapters is harness-agnostic.
package runtime

import (
	"context"
	"encoding/json"
)

// Harness names a family of coding agents.
type Harness string

const (
	Claude Harness = "claude"
	Codex  Harness = "codex"
	ACP    Harness = "acp"
)

// Approval request sources: which interception point saw the tool call.
const (
	SourceCanUseTool = "can_use_tool"    // Claude Code stream-json control request
	SourcePreToolUse = "pretooluse_hook" // Claude Code HTTP hook
	SourceCodexRPC   = "codex_rpc"       // Codex app-server approval request
	SourceACP        = "acp_permission"  // ACP session/request_permission
)

// SessionSpec says how to start one agent session.
type SessionSpec struct {
	TaskID   string
	Harness  Harness
	Agent    string  // ACP: which agent, e.g. "gemini"
	Workdir  string  // absolute path of the task worktree
	Model    string  // empty: the harness default (pin one for Gemini — it hangs on "auto")
	ResumeID string  // provider session or thread id to resume
	HookURL  string  // Claude: resident hook endpoint including its token
	MaxUSD   float64 // spending cap where the harness supports one (Claude --max-budget-usd)
}

// Adapter starts sessions for one harness.
type Adapter interface {
	Harness() Harness
	Start(ctx context.Context, spec SessionSpec, gate Gate) (Session, error)
}

// Gate decides approval requests. The broker implements it; adapters call it
// from their own goroutine, and it may block while a person decides.
type Gate interface {
	Decide(ctx context.Context, req ApprovalRequest) Decision
}

// Session is one running agent conversation.
type Session interface {
	ID() string         // amase session id
	ProviderID() string // Claude session_id, Codex thread id, ACP sessionId
	SendTurn(ctx context.Context, prompt string) (turn int, err error)
	Interrupt(ctx context.Context) error
	Events() <-chan Event // normalised; closed when the session ends
	Close(ctx context.Context) error
}

// ApprovalRequest is one proposed tool call, normalised across harnesses.
type ApprovalRequest struct {
	SessionID string          `json:"session_id"`
	CallID    string          `json:"call_id"`
	Turn      int             `json:"turn"`
	Source    string          `json:"source"`
	Tool      string          `json:"tool"`              // harness-native name: "Bash", "commandExecution", ACP kind "execute"
	Input     json.RawMessage `json:"input,omitempty"`   // harness-native input, verbatim
	Command   string          `json:"command,omitempty"` // shell text when the action is a command
	Paths     []string        `json:"paths,omitempty"`   // files the action touches, when known
}

// Verdict is a policy outcome.
type Verdict string

const (
	Allow    Verdict = "allow"
	Deny     Verdict = "deny"
	Escalate Verdict = "escalate" // never reaches an adapter: the broker turns it into allow or deny
)

// Decision is the answer to one ApprovalRequest.
type Decision struct {
	Verdict     Verdict  `json:"verdict"`
	Reason      string   `json:"reason,omitempty"` // shown to the model on deny so it can correct itself
	DecidedBy   string   `json:"decided_by"`       // "policy" | "human" | "timeout"
	Fingerprint string   `json:"fingerprint,omitempty"`
	Rules       []string `json:"rules,omitempty"`
}

// EventKind classifies a normalised session event.
type EventKind string

const (
	TurnStarted   EventKind = "TurnStarted"
	Text          EventKind = "Text"
	ToolCall      EventKind = "ToolCall"
	ToolResult    EventKind = "ToolResult"
	UsageEvent    EventKind = "Usage"
	Compacted     EventKind = "Compacted"
	TurnCompleted EventKind = "TurnCompleted"
	SessionError  EventKind = "SessionError"
)

// Event.Status values on TurnCompleted.
const (
	StatusOK          = "ok"
	StatusError       = "error"
	StatusInterrupted = "interrupted"
)

// Event is something a session reported, normalised across harnesses.
type Event struct {
	Kind     EventKind       `json:"kind"`
	Turn     int             `json:"turn"`
	CallID   string          `json:"call_id,omitempty"`
	Tool     string          `json:"tool,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Text     string          `json:"text,omitempty"` // a coalesced text segment, not every token
	IsError  bool            `json:"is_error,omitempty"`
	ExitCode *int            `json:"exit_code,omitempty"`
	Usage    *Usage          `json:"usage,omitempty"`
	Status   string          `json:"status,omitempty"` // TurnCompleted: ok | error | interrupted
	CostUSD  float64         `json:"cost_usd,omitempty"`
}

// Usage is token accounting for one provider message, already deduplicated
// by message id: Claude Code repeats one response's usage on every line of it.
type Usage struct {
	MessageID  string `json:"message_id"`
	Model      string `json:"model,omitempty"`
	Input      int64  `json:"input"`
	Output     int64  `json:"output"`
	CacheRead  int64  `json:"cache_read"`
	CacheWrite int64  `json:"cache_write"`
}
