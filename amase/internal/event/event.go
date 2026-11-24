// Package event defines what amase records: one Envelope per thing that
// happened, typed by Type, correlated by repo / task / session / turn, and
// carrying a small JSON payload. The journal underneath assigns sequence
// numbers and the hash chain; this package only says what goes in.
package event

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"
)

// Type names an event. The string is what lands in the journal, so renaming a
// constant's value breaks replay of every existing log.
type Type string

const (
	DaemonStarted     Type = "DaemonStarted"
	DaemonStopped     Type = "DaemonStopped"
	RepoRegistered    Type = "RepoRegistered"
	TaskCreated       Type = "TaskCreated"
	WorktreeCreated   Type = "WorktreeCreated"
	WorktreeRemoved   Type = "WorktreeRemoved"
	SessionStarted    Type = "SessionStarted"
	SessionEnded      Type = "SessionEnded"
	TurnStarted       Type = "TurnStarted"
	AssistantText     Type = "AssistantText"
	ToolRequested     Type = "ToolRequested"
	PolicyDecided     Type = "PolicyDecided"
	ApprovalRequested Type = "ApprovalRequested"
	ApprovalResolved  Type = "ApprovalResolved"
	ApprovalExpired   Type = "ApprovalExpired"
	ToolCompleted     Type = "ToolCompleted"
	UsageReported     Type = "UsageReported"
	CheckpointSaved   Type = "CheckpointSaved"
	RewindPerformed   Type = "RewindPerformed"
	GovernorSignal    Type = "GovernorSignal"
	PinInjected       Type = "PinInjected"
	AttritionDetected Type = "AttritionDetected"
	CriticReport      Type = "CriticReport"
	TurnCompleted     Type = "TurnCompleted"
	TurnInterrupted   Type = "TurnInterrupted"
	QueueDropped      Type = "QueueDropped"
)

// All lists every type, in declaration order.
var All = []Type{
	DaemonStarted, DaemonStopped, RepoRegistered, TaskCreated, WorktreeCreated,
	WorktreeRemoved, SessionStarted, SessionEnded, TurnStarted, AssistantText,
	ToolRequested, PolicyDecided, ApprovalRequested, ApprovalResolved,
	ApprovalExpired, ToolCompleted, UsageReported, CheckpointSaved,
	RewindPerformed, GovernorSignal, PinInjected, AttritionDetected,
	CriticReport, TurnCompleted, TurnInterrupted, QueueDropped,
}

// Priority selects the writer queue. Critical events are never dropped by
// choice; low-priority ones may be shed when the writer falls behind, because
// losing a streamed text fragment costs nothing and blocking a decision does.
type Priority int

const (
	PriorityCritical Priority = iota
	PriorityLow
)

// Priority reports which queue t belongs on.
func (t Type) Priority() Priority {
	switch t {
	case AssistantText, UsageReported, AttritionDetected:
		return PriorityLow
	default:
		return PriorityCritical
	}
}

// MaxDataBytes caps one payload. A tool can print megabytes; the journal stores
// a hash and a prefix of that instead, so one noisy command cannot make replay slow.
const MaxDataBytes = 64 << 10

// Envelope is the body of one journal record.
type Envelope struct {
	Type    Type            `json:"type"`
	Repo    string          `json:"repo,omitempty"`
	Task    string          `json:"task,omitempty"`
	Session string          `json:"session,omitempty"`
	Turn    int             `json:"turn,omitempty"`
	Data    json.RawMessage `json:"data"`
}

// New builds an envelope of type t around data.
func New(t Type, data any) (Envelope, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Envelope{}, fmt.Errorf("encoding %s payload: %w", t, err)
	}
	if len(raw) > MaxDataBytes {
		return Envelope{}, fmt.Errorf("%s payload is %d bytes; the limit is %d", t, len(raw), MaxDataBytes)
	}
	return Envelope{Type: t, Data: raw}, nil
}

// MustNew is New for payloads that are statically small and always encodable.
// It panics on failure, which can only be a programming error.
func MustNew(t Type, data any) Envelope {
	e, err := New(t, data)
	if err != nil {
		panic(err)
	}
	return e
}

// With returns a copy of e carrying the given correlation ids.
func (e Envelope) With(repo, task, session string, turn int) Envelope {
	e.Repo, e.Task, e.Session, e.Turn = repo, task, session, turn
	return e
}

// Decode unmarshals the payload into v.
func (e Envelope) Decode(v any) error {
	if err := json.Unmarshal(e.Data, v); err != nil {
		return fmt.Errorf("decoding %s payload: %w", e.Type, err)
	}
	return nil
}

// NewID returns a time-sortable random identifier: the prefix, twelve hex
// digits of Unix milliseconds, and ten random hex digits. Sorting ids sorts
// them by creation time, which keeps task lists and logs in order for free.
func NewID(prefix string) string {
	var b [5]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error since Go 1.24
	return fmt.Sprintf("%s_%012x%s", prefix, time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}

// Clip shortens s to at most max bytes without splitting a UTF-8 sequence,
// appending a marker that records how much was cut.
func Clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s…[+%d bytes]", s[:cut], len(s)-cut)
}
