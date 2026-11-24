package policy

import (
	"github.com/its-aryansingh/amase/internal/runtime"
	"github.com/its-aryansingh/toolgate/guard"
)

// Receipt is one line of the receipt ledger: what was proposed (as a canonical
// action and its fingerprint), what was decided, by whom, and under which
// rules and analyser version. A replay that reaches a different verdict can
// tell "the rules changed" from "the command changed".
type Receipt struct {
	Fingerprint     string   `json:"fingerprint"`
	Verdict         string   `json:"verdict"`
	DecidedBy       string   `json:"decided_by"`
	Rules           []string `json:"rules,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	Session         string   `json:"session,omitempty"`
	CallID          string   `json:"call_id,omitempty"`
	Source          string   `json:"source,omitempty"`
	AnalyzerVersion string   `json:"analyzer_version"`
	Action          Action   `json:"action"`
}

// NewReceipt builds the receipt for one decision.
func NewReceipt(req runtime.ApprovalRequest, d runtime.Decision, a Action) Receipt {
	return Receipt{
		Fingerprint: d.Fingerprint, Verdict: string(d.Verdict), DecidedBy: d.DecidedBy,
		Rules: d.Rules, Reason: d.Reason, Session: req.SessionID, CallID: req.CallID,
		Source: req.Source, AnalyzerVersion: guard.AnalyzerVersion, Action: a,
	}
}
