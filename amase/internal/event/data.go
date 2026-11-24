package event

import (
	"encoding/json"
	"time"
)

// Shutdown kinds recorded in DaemonStartedData.PrevShutdown.
const (
	ShutdownFirstRun = "first_run"
	ShutdownClean    = "clean"
	ShutdownUnclean  = "unclean"
)

// Turn statuses used by the projection and TurnCompletedData.
const (
	StatusRunning     = "running"
	StatusOK          = "ok"
	StatusError       = "error"
	StatusInterrupted = "interrupted"
)

// Approval states used by the projection.
const (
	StatePending  = "pending"
	StateApproved = "approved"
	StateDenied   = "denied"
	StateTimedOut = "timed_out"
	StateExpired  = "expired"
)

type DaemonStartedData struct {
	Version            string `json:"version"`
	PID                int    `json:"pid"`
	PrevShutdown       string `json:"prev_shutdown"`
	TornTailBytes      int64  `json:"torn_tail_bytes,omitempty"`
	InterruptedTurns   int    `json:"interrupted_turns,omitempty"`
	ExpiredApprovals   int    `json:"expired_approvals,omitempty"`
	PossibleReceiptGap bool   `json:"possible_receipt_gap,omitempty"`
}

type DaemonStoppedData struct {
	Reason string `json:"reason"`
}

type RepoRegisteredData struct {
	RepoID string `json:"repo_id"`
	Path   string `json:"path"`
}

type TaskCreatedData struct {
	TaskID  string `json:"task_id"`
	RepoID  string `json:"repo_id"`
	Harness string `json:"harness"`
	Agent   string `json:"agent,omitempty"`
	BaseRef string `json:"base_ref"`
	Prompt  string `json:"prompt"`
}

type WorktreeData struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

type SessionStartedData struct {
	ProviderID     string `json:"provider_id"`
	HarnessVersion string `json:"harness_version,omitempty"`
	Model          string `json:"model,omitempty"`
}

type SessionEndedData struct {
	Reason string `json:"reason"`
}

type TurnStartedData struct {
	Prompt string `json:"prompt"`
}

type AssistantTextData struct {
	Text string `json:"text"`
}

// ToolRequestedData records a tool call as the harness proposed it. Action is
// the canonical (CAVA) form as raw JSON so this package needs no policy import.
type ToolRequestedData struct {
	CallID      string          `json:"call_id"`
	Tool        string          `json:"tool"`
	Input       json.RawMessage `json:"input,omitempty"`
	Action      json.RawMessage `json:"action,omitempty"`
	Fingerprint string          `json:"fingerprint,omitempty"`
}

type PolicyDecidedData struct {
	CallID      string   `json:"call_id"`
	Verdict     string   `json:"verdict"`
	Rules       []string `json:"rules,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Source      string   `json:"source"`
	DecidedInNs int64    `json:"decided_in_ns"`
}

type ApprovalRequestedData struct {
	ApprovalID string    `json:"approval_id"`
	CallID     string    `json:"call_id"`
	Tool       string    `json:"tool"`
	Command    string    `json:"command,omitempty"`
	Rules      []string  `json:"rules,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Deadline   time.Time `json:"deadline"`
}

type ApprovalResolvedData struct {
	ApprovalID string `json:"approval_id"`
	CallID     string `json:"call_id"`
	Decision   string `json:"decision"`
	By         string `json:"by"`
	Reason     string `json:"reason,omitempty"`
}

type ApprovalExpiredData struct {
	ApprovalID string `json:"approval_id"`
	CallID     string `json:"call_id"`
	Reason     string `json:"reason"`
}

type ToolCompletedData struct {
	CallID       string `json:"call_id"`
	IsError      bool   `json:"is_error"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	OutputBytes  int    `json:"output_bytes"`
	OutputSHA256 string `json:"output_sha256,omitempty"`
	OutputPrefix string `json:"output_prefix,omitempty"`
}

type UsageReportedData struct {
	MessageID  string `json:"message_id"`
	Model      string `json:"model,omitempty"`
	Input      int64  `json:"input"`
	Output     int64  `json:"output"`
	CacheRead  int64  `json:"cache_read"`
	CacheWrite int64  `json:"cache_write"`
}

type CheckpointSavedData struct {
	Phase  string `json:"phase"`
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
}

type RewindPerformedData struct {
	ToRef    string   `json:"to_ref"`
	UndoRef  string   `json:"undo_ref"`
	Restored []string `json:"restored,omitempty"`
	Removed  []string `json:"removed,omitempty"`
}

type GovernorSignalData struct {
	Kind   string `json:"kind"`
	Steps  int    `json:"steps,omitempty"`
	Tokens int64  `json:"tokens,omitempty"`
	Detail string `json:"detail,omitempty"`
	Mode   string `json:"mode"`
}

type PinInjectedData struct {
	BundleSHA256 string `json:"bundle_sha256"`
	TokensEst    int    `json:"tokens_est"`
}

type AttritionDetectedData struct {
	CallID string `json:"call_id"`
	Kind   string `json:"kind"`
}

type CriticReportData struct {
	Findings json.RawMessage `json:"findings"`
}

type TurnCompletedData struct {
	Status     string  `json:"status"`
	StopReason string  `json:"stop_reason,omitempty"`
	DurationMs int64   `json:"duration_ms,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
}

type TurnInterruptedData struct {
	Reason string `json:"reason"`
}

type QueueDroppedData struct {
	Queue string `json:"queue"`
	Count int64  `json:"count"`
}
