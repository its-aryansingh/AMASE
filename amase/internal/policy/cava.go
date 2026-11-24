package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/its-aryansingh/amase/internal/runtime"
	"github.com/its-aryansingh/toolgate/guard"
)

// Action is the canonical form of a tool call (schema v1), after the idea of
// Canonical Action Verification and Attestation: two spellings of one effect
// produce one Action and therefore one fingerprint, so an approval or a loop
// detector keyed on it cannot be dodged by re-quoting a command.
type Action struct {
	SchemaVersion  int      `json:"schema_version"`
	Runtime        string   `json:"runtime"`
	Executable     string   `json:"executable,omitempty"`
	Operation      string   `json:"operation"`
	Category       string   `json:"category"`
	SystemsTouched []string `json:"systems_touched"`
	Reversible     bool     `json:"reversible"` // true iff a worktree rewind fully undoes it
	Target         []string `json:"target"`
}

// Categories, ordered from least to most consequential.
var categoryRank = map[string]int{
	"read": 0, "test": 1, "exec": 2, "write": 3, "package": 4, "delete": 5,
	"vcs": 6, "network": 7, "privilege": 8, "unknown": 9,
}

// Fingerprint is the SHA-256 of the action's canonical JSON. encoding/json
// emits struct fields in declaration order, and Canonicalize sorts every
// slice, so equal actions hash equally across runs and machines.
func Fingerprint(a Action) string {
	b, _ := json.Marshal(a)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func runtimeOf(source string) string {
	switch source {
	case runtime.SourceCanUseTool, runtime.SourcePreToolUse:
		return "claude-code"
	case runtime.SourceCodexRPC:
		return "codex"
	case runtime.SourceACP:
		return "acp"
	default:
		return "unknown"
	}
}

// Canonicalize maps a request onto an Action.
func Canonicalize(req runtime.ApprovalRequest, ws Workspace) Action {
	a := Action{SchemaVersion: 1, Runtime: runtimeOf(req.Source)}
	switch classify(req.Tool) {
	case classShell:
		cmd, dialect := commandOf(req)
		if dialect == dialectPOSIX && cmd != "" {
			shellAction(&a, cmd, ws)
		} else {
			a.Executable, a.Operation, a.Category = dialect, "proc.exec", "unknown"
			a.SystemsTouched = []string{"process"}
		}
	case classUnparsedShell:
		a.Executable, a.Operation, a.Category = strings.ToLower(req.Tool), "proc.exec", "unknown"
		a.SystemsTouched = []string{"process"}
	case classWrite:
		a.Operation, a.Category, a.SystemsTouched = "fs.write", "write", []string{"fs"}
		if req.Tool == "delete" {
			a.Operation, a.Category = "fs.delete", "delete"
		}
		a.Reversible = true
		for _, p := range pathsOf(req) {
			t, inside := normTarget(p, ws)
			a.Target = append(a.Target, t)
			a.Reversible = a.Reversible && inside
		}
	case classRead:
		a.Operation, a.Category, a.SystemsTouched, a.Reversible = "fs.read", "read", []string{"fs"}, true
		for _, p := range pathsOf(req) {
			t, _ := normTarget(p, ws)
			a.Target = append(a.Target, t)
		}
	case classWeb:
		a.Operation, a.Category, a.SystemsTouched, a.Reversible = "net.fetch", "network", []string{"network"}, true
		if u := inputString(req, "url"); u != "" {
			a.Target = append(a.Target, hostOf(u))
		}
	case classSubagent:
		a.Operation, a.Category, a.Reversible = "agent.delegate", "exec", true
	case classMCP:
		a.Executable, a.Operation, a.Category = req.Tool, "mcp.call", "unknown"
	default:
		a.Executable, a.Operation, a.Category = req.Tool, "unknown", "unknown"
	}
	a.SystemsTouched = sortedUnique(a.SystemsTouched)
	a.Target = sortedUnique(a.Target)
	if a.SystemsTouched == nil {
		a.SystemsTouched = []string{}
	}
	if a.Target == nil {
		a.Target = []string{}
	}
	return a
}

// shellAction folds every command of a script into one Action: the most
// consequential command names the executable, operation and category; systems
// and targets are the union; reversible only if every command is.
func shellAction(a *Action, cmd string, ws Workspace) {
	script := guard.ParseShell(cmd)
	readOnly := guard.IsReadOnly(script, &guard.Context{Workspace: ws.Root, HomeDir: ws.Home})
	a.Reversible = true
	best := -1
	for _, c := range script.Commands {
		op, cat, systems, reversible, targets := classifyCommand(c, readOnly, ws)
		for _, r := range c.Redirects {
			if r.Writes() && r.Target.Literal != "" {
				t, inside := normTarget(r.Target.Literal, ws)
				targets = append(targets, t)
				systems = append(systems, "fs")
				reversible = reversible && inside
				if categoryRank[cat] < categoryRank["write"] {
					op, cat = "fs.write", "write"
				}
			}
		}
		if rank := categoryRank[cat]; rank > best {
			best = rank
			a.Executable, a.Operation, a.Category = c.EffectiveBase(), op, cat
		}
		a.SystemsTouched = append(a.SystemsTouched, systems...)
		a.Target = append(a.Target, targets...)
		a.Reversible = a.Reversible && reversible
	}
	if script.Partial || len(script.Commands) == 0 {
		a.Category, a.Reversible = "unknown", false
	}
}

var (
	networkTools = set("curl", "wget", "nc", "ncat", "netcat", "ssh", "scp", "sftp", "rsync", "ftp", "telnet", "http", "https", "aria2c")
	deleteTools  = set("rm", "rmdir", "unlink", "shred")
	writeTools   = set("mv", "cp", "mkdir", "touch", "tee", "ln", "chmod", "chown", "truncate", "dd", "install", "patch")
	pkgManagers  = set("npm", "pnpm", "yarn", "pip", "pip3", "uv", "poetry", "cargo", "go", "gem", "bundle", "apt", "apt-get", "brew", "choco", "winget", "dotnet", "mvn", "gradle", "composer")
	pkgInstall   = set("install", "add", "i", "get", "update", "upgrade", "remove", "uninstall", "sync")
	testRunners  = set("pytest", "py.test", "jest", "vitest", "mocha", "ctest", "rspec", "phpunit", "tox", "nox")
	privilege    = set("sudo", "doas", "su", "runas", "pkexec")
	printOnly    = set("echo", "printf", "true", "false", ":", "cat", "type")
	interpreters = set("python", "python3", "node", "deno", "bun", "ruby", "perl", "php", "sh", "bash", "zsh", "dash", "pwsh", "powershell")
	gitRemote    = set("push", "fetch", "pull", "clone", "remote", "ls-remote")
	gitRead      = set("status", "log", "diff", "show", "blame", "rev-parse", "describe", "shortlog", "grep", "ls-files", "branch")
)

func classifyCommand(c guard.Command, readOnly bool, ws Workspace) (op, cat string, systems []string, reversible bool, targets []string) {
	base := strings.TrimSuffix(strings.ToLower(c.EffectiveBase()), ".exe")
	ops := effectiveOperands(c)

	if privilege[strings.ToLower(c.Base())] {
		return "proc.privileged", "privilege", []string{"process", "fs"}, false, nil
	}
	if readOnly {
		return "fs.read", "read", []string{"fs"}, true, pathTargets(ops, ws)
	}
	sub := ""
	if len(ops) > 0 {
		sub = strings.ToLower(ops[0])
	}
	switch {
	case networkTools[base]:
		for _, o := range ops {
			if strings.Contains(o, "://") {
				targets = append(targets, hostOf(o))
			}
		}
		return "net.fetch", "network", []string{"network"}, false, targets
	case base == "git":
		switch {
		case sub == "push":
			return "vcs.push", "vcs", []string{"network", "vcs-remote"}, false, nil
		case gitRemote[sub]:
			return "vcs.fetch", "vcs", []string{"network", "vcs-remote", "fs"}, false, nil
		case gitRead[sub]:
			return "fs.read", "read", []string{"fs"}, true, nil
		default:
			return "vcs.local", "vcs", []string{"fs"}, false, nil
		}
	case deleteTools[base]:
		t, all := insideTargets(ops[min(1, len(ops)):], ops, ws)
		return "fs.delete", "delete", []string{"fs"}, all, t
	case base == "sed" && c.HasFlag("-i", "--in-place"):
		t, all := insideTargets(nil, ops, ws)
		return "fs.write", "write", []string{"fs"}, all, t
	case writeTools[base]:
		t, all := insideTargets(nil, ops, ws)
		return "fs.write", "write", []string{"fs"}, all, t
	case printOnly[base]:
		// Harmless by themselves; any redirect they carry is judged by its target.
		return "fs.read", "read", []string{"fs"}, true, nil
	case testRunners[base] || isTestSubcommand(base, ops):
		return "test.run", "test", []string{"process", "fs"}, true, nil
	case pkgManagers[base] && pkgInstall[sub]:
		return "pkg.install", "package", []string{"network", "fs"}, false, nil
	case interpreters[base]:
		return "proc.exec", "exec", []string{"process"}, false, nil
	default:
		return "proc.exec", "exec", []string{"process"}, false, nil
	}
}

func isTestSubcommand(base string, ops []string) bool {
	if len(ops) == 0 {
		return false
	}
	sub := strings.ToLower(ops[0])
	switch base {
	case "go", "cargo", "dotnet", "mvn", "gradle", "make":
		return sub == "test"
	case "npm", "pnpm", "yarn", "bun":
		return sub == "test" || (sub == "run" && len(ops) > 1 && strings.HasPrefix(strings.ToLower(ops[1]), "test"))
	case "python", "python3":
		return sub == "pytest" || sub == "unittest"
	}
	return false
}

// effectiveOperands drops wrapper words (sudo, env, timeout) so operands start
// after the program that actually runs.
func effectiveOperands(c guard.Command) []string {
	ops := c.Operands()
	eff := c.EffectiveBase()
	if eff == c.Base() {
		return ops
	}
	for i, o := range ops {
		if filepath.Base(filepath.ToSlash(o)) == eff {
			return ops[i+1:]
		}
	}
	return ops
}

func insideTargets(_ []string, ops []string, ws Workspace) ([]string, bool) {
	all := len(ops) > 0
	var out []string
	for _, o := range ops {
		t, inside := normTarget(o, ws)
		out = append(out, t)
		all = all && inside
	}
	return out, all
}

func pathTargets(ops []string, ws Workspace) []string {
	var out []string
	for _, o := range ops {
		if strings.Contains(o, "://") {
			continue
		}
		t, _ := normTarget(o, ws)
		out = append(out, t)
	}
	return out
}

// normTarget returns p relative to the worktree with forward slashes when it
// is inside it, otherwise its cleaned absolute (or literal) form; and whether
// it is inside. Anything it cannot place counts as outside: a reversibility
// claim must never rest on a guess.
func normTarget(p string, ws Workspace) (string, bool) {
	switch {
	case strings.Contains(p, "$") || strings.Contains(p, "\x00"):
		return p, false // a variable or command substitution: unknown until run time
	case p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`):
		if ws.Home == "" {
			return p, false
		}
		p = filepath.Join(ws.Home, strings.TrimLeft(p[1:], `/\`))
	case !filepath.IsAbs(p) && (strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`)):
		return filepath.ToSlash(p), false // rooted but driveless on Windows: the current drive's root
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(ws.Root, p)
	}
	abs = filepath.Clean(abs)
	if ws.Root != "" {
		if rel, ok := relInside(filepath.Clean(ws.Root), abs); ok {
			return filepath.ToSlash(rel), true
		}
	}
	return filepath.ToSlash(abs), false
}

func relInside(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return strings.ToLower(u.Hostname())
}

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	sort.Strings(in)
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}
