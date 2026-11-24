// Package guard is the exported face of toolgate's command analyser, so other
// modules (the amase workspace) can reuse the POSIX parser and the rule table
// instead of forking them. It adds no behaviour: every name is an alias or a
// direct call into internal/audit, where the policy lives and is tested.
package guard

import "github.com/its-aryansingh/toolgate/internal/audit"

type (
	Verdict  = audit.Verdict
	Finding  = audit.Finding
	Severity = audit.Severity
	Action   = audit.Action
	Context  = audit.Context
	Policy   = audit.Policy
	Rule     = audit.Rule
	Script   = audit.Script
	Command  = audit.Command
	Word     = audit.Word
	Redirect = audit.Redirect
)

const (
	SevInfo     = audit.SevInfo
	SevLow      = audit.SevLow
	SevMedium   = audit.SevMedium
	SevHigh     = audit.SevHigh
	SevCritical = audit.SevCritical

	ActionAllow           = audit.ActionAllow
	ActionRequireApproval = audit.ActionRequireApproval
	ActionDeny            = audit.ActionDeny

	AnalyzerVersion = audit.AnalyzerVersion
)

var (
	PolicyPermissive = audit.PolicyPermissive
	PolicyStandard   = audit.PolicyStandard
	PolicyStrict     = audit.PolicyStrict
)

// Analyze parses command and applies every rule under policy.
func Analyze(command string, ctx *Context, policy Policy) Verdict {
	return audit.Analyze(command, ctx, policy)
}

// ParseShell parses POSIX shell source into commands.
func ParseShell(src string) *Script { return audit.ParseShell(src) }

// IsReadOnly reports whether every command in s is known not to write.
func IsReadOnly(s *Script, ctx *Context) bool { return audit.IsReadOnly(s, ctx) }

// PolicyByName resolves "permissive", "standard" or "strict"; anything else is standard.
func PolicyByName(name string) Policy { return audit.PolicyByName(name) }
