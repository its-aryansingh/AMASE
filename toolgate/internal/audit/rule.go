package audit

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Severity orders findings. It is deliberately coarse: a five-point scale that
// a human can apply consistently beats a numeric score that nobody can defend.
type Severity int

const (
	SevInfo Severity = iota
	SevLow
	SevMedium
	SevHigh
	SevCritical
)

func (s Severity) String() string {
	switch s {
	case SevCritical:
		return "critical"
	case SevHigh:
		return "high"
	case SevMedium:
		return "medium"
	case SevLow:
		return "low"
	default:
		return "info"
	}
}

// Action is the verdict, and maps one-to-one onto goose's InspectionAction so
// the Rust shim is a straight translation with no policy logic of its own.
// Policy belongs in one place, and that place is here, where it is versioned and
// testable.
type Action int

const (
	ActionAllow Action = iota
	ActionRequireApproval
	ActionDeny
)

func (a Action) String() string {
	switch a {
	case ActionDeny:
		return "deny"
	case ActionRequireApproval:
		return "require_approval"
	default:
		return "allow"
	}
}

// Finding is one rule hit.
type Finding struct {
	RuleID   string `json:"rule_id"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
	Command  string `json:"command,omitempty"`
	Fix      string `json:"fix,omitempty"`

	sev Severity
}

// Verdict is the complete answer for one proposed action.
type Verdict struct {
	Action   string    `json:"action"`
	ReadOnly bool      `json:"read_only"`
	Findings []Finding `json:"findings"`
	// Confidence is 1.0 for every verdict this analyser produces. The field
	// exists because goose's InspectionResult has one, and because stating the
	// value explicitly is the point: the incumbent classifier is a language
	// model whose confidence is unstated and unstable between identical calls.
	Confidence float64 `json:"confidence"`
	Analyzer   string  `json:"analyzer"`
	Policy     string  `json:"policy"`
	ElapsedNs  int64   `json:"elapsed_ns"`
	// Partial means the parse was incomplete. A partial parse can prove danger
	// but never safety, so it forces approval at minimum.
	Partial bool   `json:"partial"`
	Reason  string `json:"reason"`
}

// Rule is one deterministic check.
type Rule struct {
	ID       string
	Title    string
	Severity Severity
	// Rationale is printed by `toolgate explain` and included in the denial
	// message, so the model can correct itself instead of retrying blindly.
	Rationale string
	Check     func(self Rule, s *Script, ctx *Context) []Finding
}

// Context carries the environment a rule needs.
type Context struct {
	// Workspace is the absolute path the agent is allowed to modify.
	Workspace string
	// HomeDir is used to recognise credential paths; empty disables those rules.
	HomeDir string
	// AllowNetwork relaxes egress rules for workflows that genuinely need them.
	AllowNetwork bool
	// ExtraReadOnly names commands the operator asserts are read-only.
	ExtraReadOnly []string
}

// Policy maps severities onto actions.
type Policy struct {
	Name string
	// Threshold is the lowest severity that forces approval.
	Threshold Severity
	// DenyAt is the lowest severity that is refused outright.
	DenyAt Severity
}

// Named policies. Standard is the default and is tuned so that the rules which
// fire on ordinary development work are approval-worthy rather than refusals --
// a guardrail that blocks `git push` gets uninstalled within a day, and an
// uninstalled guardrail has a protection rate of zero.
var (
	PolicyPermissive = Policy{Name: "permissive", Threshold: SevHigh, DenyAt: SevCritical}
	PolicyStandard   = Policy{Name: "standard", Threshold: SevMedium, DenyAt: SevCritical}
	PolicyStrict     = Policy{Name: "strict", Threshold: SevLow, DenyAt: SevHigh}
)

// PolicyByName resolves a policy, falling back to standard.
func PolicyByName(name string) Policy {
	switch strings.ToLower(name) {
	case "permissive":
		return PolicyPermissive
	case "strict":
		return PolicyStrict
	default:
		return PolicyStandard
	}
}

// AnalyzerVersion changes whenever a rule changes behaviour. It is recorded in
// every journal entry so that a replay can tell "the rules changed" from "the
// command changed" -- without it, a re-run that produces a different verdict is
// unattributable.
const AnalyzerVersion = "toolgate-audit/0.1.0"

// Analyze parses a command and applies every rule.
func Analyze(command string, ctx *Context, policy Policy) Verdict {
	start := time.Now()
	if ctx == nil {
		ctx = &Context{}
	}

	script := ParseShell(command)
	var findings []Finding
	for _, rule := range Rules {
		findings = append(findings, rule.Check(rule, script, ctx)...)
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].sev != findings[j].sev {
			return findings[i].sev > findings[j].sev
		}
		return findings[i].RuleID < findings[j].RuleID
	})

	worst := SevInfo
	for _, f := range findings {
		if f.sev > worst {
			worst = f.sev
		}
	}

	action := ActionAllow
	switch {
	case worst >= policy.DenyAt:
		action = ActionDeny
	case worst >= policy.Threshold:
		action = ActionRequireApproval
	}

	readOnly := IsReadOnly(script, ctx)

	reason := "no rule matched"
	if len(findings) > 0 {
		reason = fmt.Sprintf("%s: %s", findings[0].RuleID, findings[0].Detail)
	}

	if script.Partial {
		// Escalate, never downgrade. The parse is incomplete, so absence of a
		// finding carries no information.
		readOnly = false
		if action == ActionAllow {
			action = ActionRequireApproval
		}
		reason = "command could not be fully parsed; " + reason
	}

	if findings == nil {
		findings = []Finding{}
	}

	return Verdict{
		Action:     action.String(),
		ReadOnly:   readOnly,
		Findings:   findings,
		Confidence: 1.0,
		Analyzer:   AnalyzerVersion,
		Policy:     policy.Name,
		ElapsedNs:  time.Since(start).Nanoseconds(),
		Partial:    script.Partial,
		Reason:     reason,
	}
}

func finding(r Rule, detail, cmd, fix string) Finding {
	return Finding{
		RuleID:   r.ID,
		Title:    r.Title,
		Severity: r.Severity.String(),
		Detail:   detail,
		Command:  cmd,
		Fix:      fix,
		sev:      r.Severity,
	}
}
