package hooks

import "sync"

// ReceiptSet remembers which tool calls already have a receipt. Claude can
// show one call to both the PreToolUse hook and can_use_tool under the same
// tool_use_id (protocol fact C12); sharing one set between the two paths keeps
// the ledger at one receipt per call.
type ReceiptSet struct {
	mu sync.Mutex
	m  map[string]map[string]struct{} // session -> call ids
}

// NewReceiptSet returns an empty set.
func NewReceiptSet() *ReceiptSet {
	return &ReceiptSet{m: map[string]map[string]struct{}{}}
}

// First records (session, callID) and reports whether it was new.
func (r *ReceiptSet) First(session, callID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := r.m[session]
	if calls == nil {
		calls = map[string]struct{}{}
		r.m[session] = calls
	}
	if _, seen := calls[callID]; seen {
		return false
	}
	calls[callID] = struct{}{}
	return true
}

// Forget drops everything recorded for session, once it has ended.
func (r *ReceiptSet) Forget(session string) {
	r.mu.Lock()
	delete(r.m, session)
	r.mu.Unlock()
}
