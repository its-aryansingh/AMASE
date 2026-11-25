// Package hooks is the resident endpoint for Claude Code's HTTP hooks. A
// managed Claude session is started with a settings file (see Settings) that
// points PreToolUse, PostToolUse, SessionStart and Stop at
// POST /hooks/claude/{token}. PreToolUse is the enforcement point: every tool
// call is decided by the policy engine in-process and answered before Claude
// runs it. The other hooks only observe, and fail open.
//
// Each managed session registers its own token, so the token both
// authenticates the caller and says which task, session and worktree a call
// belongs to; a hook response only ever affects the session that asked.
package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/its-aryansingh/amase/internal/event"
	"github.com/its-aryansingh/amase/internal/policy"
	"github.com/its-aryansingh/amase/internal/runtime"
)

// Path is the route pattern the hook endpoint is mounted on.
const Path = "/hooks/claude/{token}"

// MaxBody caps one hook request. Write and Edit calls carry whole file
// contents, so the cap is generous; a larger body is refused, which blocks a
// PreToolUse call rather than letting it through undecided.
const MaxBody = 16 << 20

// Engine decides tool calls. *policy.Engine implements it; tests use stubs.
type Engine interface {
	Evaluate(req runtime.ApprovalRequest, ws policy.Workspace) (runtime.Decision, policy.Action)
}

// Recorder takes events and receipts without blocking. *store.Store implements it.
type Recorder interface {
	Emit(e event.Envelope) bool
	Receipt(v any) bool
}

// Advisor returns the governor's latest advice for an amase session. It must
// be an atomic read: it is called while Claude waits for the hook reply.
type Advisor interface {
	Advice(session string) (string, bool)
}

// ToolOutput is what a PostToolUse call reports a tool produced.
type ToolOutput struct {
	CallID      string
	Tool        string
	Shell       bool // the tool reported stdout/stderr (Bash and similar)
	Stdout      string
	Stderr      string
	Interrupted bool
	Raw         json.RawMessage // tool_response verbatim
}

// AttritionCheck inspects one tool's output for signs that the agent is
// working from damaged data (WP-2.3) and returns the kind it found.
type AttritionCheck func(out ToolOutput) (kind string, found bool)

// Pin is context re-injected after Claude compacts its conversation (WP-2.3).
type Pin struct {
	Context      string
	BundleSHA256 string
	TokensEst    int
}

// Pinner builds the pin for a session, or reports false when there is none.
type Pinner func(b Binding) (Pin, bool)

// Binding is what the server knows about the session behind one token.
type Binding struct {
	Repo, Task, Session string // amase correlation ids stamped on every event
	Workspace           policy.Workspace
	Turn                func() int // the session's current turn, read on the hot path: must not block; nil reports 0
}

// Options configures a Server. Only Engine is required.
type Options struct {
	Engine    Engine
	Recorder  Recorder // nil discards events and receipts
	Advisor   Advisor  // nil: PostToolUse never carries advice
	Attrition AttritionCheck
	Pinner    Pinner
	Receipts  *ReceiptSet // share with the can_use_tool path; nil makes a private one
}

// Server answers Claude Code hook calls. It is safe for concurrent use.
type Server struct {
	engine    Engine
	rec       Recorder
	advisor   Advisor
	attrition AttritionCheck
	pinner    Pinner
	receipts  *ReceiptSet

	mu       sync.RWMutex
	bindings map[string]*binding
}

type binding struct {
	Binding
	provider atomic.Pointer[string] // Claude's session_id, learned from the first call
}

func (b *binding) turn() int {
	if b.Turn == nil {
		return 0
	}
	return b.Turn()
}

// session is the id receipts and requests are keyed by: the amase session
// when the binding names one, else Claude's own session_id.
func (b *binding) session(claudeSession string) string {
	if b.Session != "" {
		return b.Session
	}
	return claudeSession
}

// New returns a server with no registered sessions.
func New(o Options) (*Server, error) {
	if o.Engine == nil {
		return nil, errors.New("hooks: an Engine is required")
	}
	s := &Server{
		engine: o.Engine, rec: o.Recorder, advisor: o.Advisor,
		attrition: o.Attrition, pinner: o.Pinner, receipts: o.Receipts,
		bindings: map[string]*binding{},
	}
	if s.rec == nil {
		s.rec = discard{}
	}
	if s.receipts == nil {
		s.receipts = NewReceiptSet()
	}
	return s, nil
}

// Register routes hook calls carrying token to the session b describes.
func (s *Server) Register(token string, b Binding) error {
	if err := checkToken(token); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.bindings[token]; dup {
		return errors.New("hooks: token already registered")
	}
	s.bindings[token] = &binding{Binding: b}
	return nil
}

// Unregister stops accepting token. Later calls with it get 404, which blocks
// any PreToolUse call a lingering process still makes.
func (s *Server) Unregister(token string) {
	s.mu.Lock()
	b := s.bindings[token]
	delete(s.bindings, token)
	s.mu.Unlock()
	if b == nil {
		return
	}
	if b.Session != "" {
		s.receipts.Forget(b.Session)
	} else if p := b.provider.Load(); p != nil {
		s.receipts.Forget(*p)
	}
}

// ProviderID returns the Claude session_id seen on token's first hook call.
func (s *Server) ProviderID(token string) string {
	s.mu.RLock()
	b := s.bindings[token]
	s.mu.RUnlock()
	if b == nil {
		return ""
	}
	if p := b.provider.Load(); p != nil {
		return *p
	}
	return ""
}

// Mount adds the hook route to mux, so the API listener can serve it.
func (s *Server) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST "+Path, s.serveHook)
}

// Handler returns a handler serving only the hook route.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.Mount(mux)
	return mux
}

var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func (s *Server) serveHook(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	b := s.bindings[r.PathValue("token")]
	s.mu.RUnlock()
	if b == nil {
		http.Error(w, "unknown hook token", http.StatusNotFound)
		return
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer func() {
		if buf.Cap() <= 1<<20 {
			bufPool.Put(buf)
		}
	}()
	if _, err := buf.ReadFrom(http.MaxBytesReader(w, r.Body, MaxBody)); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "hook body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "reading hook body", http.StatusBadRequest)
		return
	}
	var p payload
	if err := json.Unmarshal(buf.Bytes(), &p); err != nil {
		http.Error(w, "hook body is not valid JSON", http.StatusBadRequest)
		return
	}
	if p.HookEventName == "" {
		http.Error(w, "hook body has no hook_event_name", http.StatusBadRequest)
		return
	}
	if p.SessionID != "" && b.provider.Load() == nil {
		sid := p.SessionID
		b.provider.CompareAndSwap(nil, &sid)
	}

	var out []byte
	switch p.HookEventName {
	case "PreToolUse":
		out = s.preToolUse(b, &p)
	case "PostToolUse":
		out = s.postToolUse(b, &p)
	case "SessionStart":
		out = s.sessionStart(b, &p)
	case "Stop":
		out = s.stop(b, &p)
	default:
		out = passThrough
	}
	writeJSON(w, out)
}

func writeJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) emit(b *binding, t event.Type, turn int, data any) {
	e, err := event.New(t, data)
	if err != nil {
		return
	}
	s.rec.Emit(e.With(b.Repo, b.Task, b.Session, turn))
}

type discard struct{}

func (discard) Emit(event.Envelope) bool { return true }
func (discard) Receipt(any) bool         { return true }

// checkToken accepts only tokens that are safe to place in a URL path segment
// and long enough to be unguessable.
func checkToken(token string) error {
	if len(token) < 16 || len(token) > 128 {
		return fmt.Errorf("hooks: token must be 16 to 128 characters, got %d", len(token))
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
			return fmt.Errorf("hooks: token may contain only letters, digits, '-' and '_'")
		}
	}
	return nil
}
