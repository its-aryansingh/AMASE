// Package policy decides, before a tool call executes, whether it may run.
// Shell commands go through toolgate's POSIX parser and rule table; file
// changes through a path policy; everything else through a small per-tool
// table. Decisions are pure: no I/O beyond resolving symlinks on the paths a
// request names, no model call, the same answer every time.
package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/its-aryansingh/amase/internal/runtime"
	"github.com/its-aryansingh/toolgate/guard"
)

// Workspace is what a decision needs to know about where the agent runs.
type Workspace struct {
	Root         string // absolute path of the task worktree
	Home         string // the user's home directory, for credential-path rules
	AllowNetwork bool
	Policy       string // permissive | standard | strict (toolgate policies); empty is standard
}

type toolClass int

const (
	classUnknown toolClass = iota
	classShell
	classUnparsedShell
	classWrite
	classRead
	classWeb
	classSubagent
	classMCP
)

// toolClasses spans the three harnesses; their tool names do not collide.
var toolClasses = map[string]toolClass{
	// Claude Code
	"Bash": classShell, "PowerShell": classUnparsedShell,
	"Edit": classWrite, "Write": classWrite, "MultiEdit": classWrite, "NotebookEdit": classWrite,
	"Read": classRead, "Glob": classRead, "Grep": classRead, "LS": classRead, "NotebookRead": classRead,
	"TodoWrite": classRead, "BashOutput": classRead, "KillShell": classRead,
	"WebFetch": classWeb, "WebSearch": classWeb,
	"Task": classSubagent, "Agent": classSubagent,
	// Codex app-server
	"commandExecution": classShell, "fileChange": classWrite,
	// ACP tool kinds
	"execute": classShell, "edit": classWrite, "delete": classWrite, "move": classWrite,
	"read": classRead, "search": classRead, "think": classRead, "fetch": classWeb,
}

func classify(tool string) toolClass {
	if strings.HasPrefix(tool, "mcp__") {
		return classMCP
	}
	if c, ok := toolClasses[tool]; ok {
		return c
	}
	return classUnknown
}

// Engine makes policy decisions. It holds no state; it exists so that later
// work (per-repo rules, a learned allowlist) has somewhere to live.
type Engine struct{}

func NewEngine() *Engine { return &Engine{} }

// Decide returns the policy decision for req.
func (e *Engine) Decide(req runtime.ApprovalRequest, ws Workspace) runtime.Decision {
	d, _ := e.Evaluate(req, ws)
	return d
}

// Evaluate returns the decision and the canonical action it was made about.
func (e *Engine) Evaluate(req runtime.ApprovalRequest, ws Workspace) (runtime.Decision, Action) {
	a := Canonicalize(req, ws)
	d := runtime.Decision{DecidedBy: "policy", Fingerprint: Fingerprint(a)}

	switch classify(req.Tool) {
	case classShell:
		cmd, dialect := commandOf(req)
		switch {
		case cmd == "":
			d.Verdict, d.Reason = runtime.Escalate, "a shell request carried no command text"
		case dialect != dialectPOSIX:
			d.Verdict, d.Reason = runtime.Escalate, dialect+" commands are not parsed, so amase cannot show this one is safe"
		default:
			shellVerdict(&d, cmd, ws)
		}
	case classUnparsedShell:
		d.Verdict, d.Reason = runtime.Escalate, req.Tool+" commands are not parsed, so amase cannot show this one is safe"
	case classWrite:
		ps := pathsOf(req)
		if len(ps) == 0 {
			d.Verdict, d.Reason = runtime.Escalate, "a file change named no target path"
			break
		}
		d.Verdict = runtime.Allow
		for _, p := range ps {
			v, why := pathVerdict(p, ws)
			if rank(v) > rank(d.Verdict) {
				d.Verdict, d.Reason = v, why
			}
		}
	case classRead, classSubagent:
		d.Verdict = runtime.Allow
	case classWeb:
		d.Verdict = runtime.Allow
		if strings.EqualFold(ws.Policy, "strict") {
			d.Verdict, d.Reason = runtime.Escalate, "network access needs approval under the strict policy"
		}
	case classMCP:
		d.Verdict, d.Reason = runtime.Escalate, "MCP tool calls need approval"
	default:
		d.Verdict, d.Reason = runtime.Escalate, fmt.Sprintf("unknown tool %q; unknown is not safe", req.Tool)
	}
	return d, a
}

func rank(v runtime.Verdict) int {
	switch v {
	case runtime.Deny:
		return 2
	case runtime.Escalate:
		return 1
	default:
		return 0
	}
}

func shellVerdict(d *runtime.Decision, cmd string, ws Workspace) {
	v := guard.Analyze(cmd, &guard.Context{Workspace: ws.Root, HomeDir: ws.Home, AllowNetwork: ws.AllowNetwork}, guard.PolicyByName(ws.Policy))
	seen := map[string]bool{}
	for _, f := range v.Findings {
		if !seen[f.RuleID] {
			seen[f.RuleID] = true
			d.Rules = append(d.Rules, f.RuleID)
		}
	}
	switch v.Action {
	case guard.ActionAllow.String():
		d.Verdict = runtime.Allow
	case guard.ActionDeny.String():
		d.Verdict = runtime.Deny
	default:
		d.Verdict = runtime.Escalate
	}
	if d.Verdict != runtime.Allow {
		d.Reason = explain(v)
	}
}

// explain renders findings for the model: the rule, its severity, and the fix,
// so a denied agent can change course instead of retrying the same command.
func explain(v guard.Verdict) string {
	if len(v.Findings) == 0 {
		if v.Reason != "" {
			return v.Reason
		}
		return "the command could not be fully parsed"
	}
	var b strings.Builder
	for i, f := range v.Findings {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s [%s] %s", f.RuleID, f.Severity, f.Title)
		if f.Fix != "" {
			fmt.Fprintf(&b, " (try: %s)", f.Fix)
		}
	}
	return b.String()
}

const (
	dialectPOSIX = "posix"
	dialectNone  = ""
)

// commandOf returns a shell request's command text and its dialect. Adapters
// normally fill req.Command; otherwise the input's "command" field is read,
// as a string or as an argv array.
func commandOf(req runtime.ApprovalRequest) (string, string) {
	if req.Command != "" {
		return req.Command, dialectPOSIX
	}
	var in struct {
		Command json.RawMessage `json:"command"`
	}
	if json.Unmarshal(req.Input, &in) != nil || len(in.Command) == 0 {
		return "", dialectNone
	}
	var s string
	if json.Unmarshal(in.Command, &s) == nil {
		return s, dialectPOSIX
	}
	var argv []string
	if json.Unmarshal(in.Command, &argv) == nil {
		return UnwrapArgv(argv)
	}
	return "", dialectNone
}

// UnwrapArgv turns an argv a harness will exec into shell text toolgate can
// parse: ["bash","-lc","script"] yields the script itself; a PowerShell or cmd
// argv is reported in its own dialect, which amase does not parse.
func UnwrapArgv(argv []string) (string, string) {
	if len(argv) == 0 {
		return "", dialectNone
	}
	base := strings.TrimSuffix(strings.ToLower(path.Base(strings.ReplaceAll(argv[0], `\`, "/"))), ".exe")
	switch base {
	case "bash", "sh", "zsh", "dash":
		if len(argv) >= 3 && (argv[1] == "-c" || argv[1] == "-lc" || argv[1] == "-ic") {
			return argv[2], dialectPOSIX
		}
	case "powershell", "pwsh", "cmd":
		return strings.Join(argv, " "), base
	}
	return shellJoin(argv), dialectPOSIX
}

func shellJoin(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`|&;<>()*?[]{}~#!") {
			out[i] = a
			continue
		}
		out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(out, " ")
}

// pathsOf returns the files a request touches: req.Paths when the adapter set
// them, else the usual input fields.
func pathsOf(req runtime.ApprovalRequest) []string {
	if len(req.Paths) > 0 {
		return req.Paths
	}
	var in map[string]json.RawMessage
	if json.Unmarshal(req.Input, &in) != nil {
		return nil
	}
	var out []string
	for _, k := range []string{"file_path", "path", "notebook_path", "filePath"} {
		var s string
		if raw, ok := in[k]; ok && json.Unmarshal(raw, &s) == nil && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func inputString(req runtime.ApprovalRequest, key string) string {
	var in map[string]json.RawMessage
	if json.Unmarshal(req.Input, &in) != nil {
		return ""
	}
	var s string
	if raw, ok := in[key]; ok && json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

var credentialDirs = []string{".ssh", ".aws", ".gnupg", ".kube", ".docker", ".claude", ".codex", ".gemini", filepath.Join(".config", "gh")}

// pathVerdict judges one file change.
//
// It does not resolve symlinks: on Windows, EvalSymlinks re-walks the whole
// absolute path on every call and cost 1.8 ms per decision. Instead it walks
// only the components inside the worktree with Lstat, stops at the first one
// that does not exist yet (nothing below it can be a link), and escalates any
// write whose path passes through a symlink or other reparse point. Links are
// how a write leaves a worktree, so a write through one goes to a person.
func pathVerdict(p string, ws Workspace) (runtime.Verdict, string) {
	t, inside := normTarget(p, ws)
	if !inside {
		if ws.Home != "" && filepath.IsAbs(filepath.FromSlash(t)) {
			abs := filepath.Clean(filepath.FromSlash(t))
			for _, d := range credentialDirs {
				if _, ok := relInside(filepath.Join(filepath.Clean(ws.Home), d), abs); ok {
					return runtime.Deny, "writes into a credential directory (" + filepath.ToSlash(d) + ")"
				}
			}
		}
		return runtime.Escalate, "writes outside the task worktree"
	}
	if link := firstLink(filepath.Clean(ws.Root), t); link != "" {
		return runtime.Escalate, "the path passes through a link (" + link + "), which can lead outside the worktree"
	}
	parts := strings.Split(t, "/")
	switch {
	case parts[0] == ".git" && len(parts) > 1 && parts[1] == "hooks":
		return runtime.Deny, "writes to .git/hooks run code on the next git command"
	case parts[0] == ".git":
		return runtime.Escalate, "writes inside .git change repository state directly"
	case parts[0] == ".amase":
		return runtime.Escalate, "writes to .amase/ change the rules the agent runs under"
	}
	base := strings.ToLower(parts[len(parts)-1])
	if strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".pem") || strings.HasPrefix(base, "id_") {
		return runtime.Escalate, "the file looks like it holds secrets"
	}
	return runtime.Allow, ""
}

// firstLink returns the slash-separated prefix of rel at which a symlink or
// reparse point sits under root, or "" if the existing part of the path has none.
func firstLink(root, rel string) string {
	cur := root
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			return ""
		}
		if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return strings.Join(parts[:i+1], "/")
		}
	}
	return ""
}
