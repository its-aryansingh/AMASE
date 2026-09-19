// Package tools wires the analyser, journal and snapshotter into the five MCP
// tools toolgate exposes.
//
// Five is not an accident. goose's own guidance is that an agent performs best
// with fewer than twenty-five tools enabled across all extensions, and every
// tool a server publishes is prompt text the model pays for on every turn. A
// guardrail that degrades the agent it protects has not helped.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/its-aryansingh/toolgate/internal/audit"
	"github.com/its-aryansingh/toolgate/internal/journal"
	"github.com/its-aryansingh/toolgate/internal/mcp"
	"github.com/its-aryansingh/toolgate/internal/snapshot"
)

// Deps are what the tools need from the process.
type Deps struct {
	Workspace string
	HomeDir   string
	Policy    audit.Policy
	Journal   *journal.Journal
	Signer    *mcp.HandleSigner
	// ExecTimeout bounds a guarded command.
	ExecTimeout time.Duration
	// AllowExec gates exec_guarded entirely. Off by default: a server that can
	// run commands is a bigger thing to trust than one that only judges them,
	// and the two use cases should not be bundled by accident.
	AllowExec bool

	// snapshots holds captured trees by digest for the life of the process.
	// Bounded, because an agent that snapshots in a loop should not exhaust
	// memory; the handle survives eviction as a signature, so the failure is a
	// clear "unknown snapshot" rather than a wrong diff.
	snapshots map[string]*snapshot.Snapshot
	order     []string
}

const maxRetainedSnapshots = 64

// Register installs every tool on the server.
func Register(s *mcp.Server, d *Deps) {
	d.snapshots = make(map[string]*snapshot.Snapshot)

	s.Register(mcp.Tool{
		Name:        "audit_command",
		Title:       "Audit a shell command",
		Description: "Statically analyse a shell command and return a deterministic verdict (allow, require_approval, deny), whether it is read-only, and every rule it triggered. Does not execute anything.",
		InputSchema: schema(`{
			"type":"object",
			"properties":{
				"command":{"type":"string","description":"the exact shell command to analyse"},
				"cwd":{"type":"string","description":"working directory the command would run in"}
			},
			"required":["command"],
			"additionalProperties":false
		}`),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    mcp.Bool(true),
			DestructiveHint: mcp.Bool(false),
			IdempotentHint:  mcp.Bool(true),
			OpenWorldHint:   mcp.Bool(false),
		},
	}, d.auditCommand)

	s.Register(mcp.Tool{
		Name:        "exec_guarded",
		Title:       "Run a shell command through the guard",
		Description: "Audit a shell command and run it only if the policy allows. Denied commands return the reason and the rule that refused them. Every call is journalled.",
		InputSchema: schema(`{
			"type":"object",
			"properties":{
				"command":{"type":"string"},
				"cwd":{"type":"string"},
				"justification":{"type":"string","description":"why this command is needed; recorded in the journal"}
			},
			"required":["command"],
			"additionalProperties":false
		}`),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    mcp.Bool(false),
			DestructiveHint: mcp.Bool(true),
			OpenWorldHint:   mcp.Bool(true),
		},
	}, d.execGuarded)

	s.Register(mcp.Tool{
		Name:        "snapshot_workspace",
		Title:       "Record workspace state",
		Description: "Compute a deterministic content address for the workspace and return a signed handle naming it. Pass the handle to diff_snapshot later to see exactly what changed.",
		InputSchema: schema(`{
			"type":"object",
			"properties":{"path":{"type":"string","description":"subdirectory of the workspace; defaults to the whole workspace"}},
			"additionalProperties":false
		}`),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: mcp.Bool(true), OpenWorldHint: mcp.Bool(false)},
	}, d.snapshotWorkspace)

	s.Register(mcp.Tool{
		Name:        "diff_snapshot",
		Title:       "Compare against a snapshot",
		Description: "Given a handle from snapshot_workspace, list the files added, removed or modified since it was taken.",
		InputSchema: schema(`{
			"type":"object",
			"properties":{"handle":{"type":"string"},"path":{"type":"string"}},
			"required":["handle"],
			"additionalProperties":false
		}`),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: mcp.Bool(true), OpenWorldHint: mcp.Bool(false)},
	}, d.diffSnapshot)

	s.Register(mcp.Tool{
		Name:        "explain_rule",
		Title:       "Explain a guard rule",
		Description: "Return the rationale for a rule id such as TG001, or list every rule when called with no argument. Use this after a denial to choose a different approach rather than retrying.",
		InputSchema: schema(`{
			"type":"object",
			"properties":{"rule_id":{"type":"string"}},
			"additionalProperties":false
		}`),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: mcp.Bool(true), IdempotentHint: mcp.Bool(true)},
	}, d.explainRule)
}

func schema(s string) json.RawMessage {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic("tools: bad inline schema: " + err.Error())
	}
	b, _ := json.Marshal(v)
	return b
}

func (d *Deps) auditCtx(cwd string) *audit.Context {
	ws := d.Workspace
	if cwd != "" {
		if abs, err := filepath.Abs(cwd); err == nil {
			ws = abs
		}
	}
	return &audit.Context{Workspace: ws, HomeDir: d.HomeDir}
}

type auditArgs struct {
	Command       string `json:"command"`
	Cwd           string `json:"cwd"`
	Justification string `json:"justification"`
}

func (d *Deps) auditCommand(_ context.Context, meta *mcp.Meta, raw json.RawMessage) (*mcp.CallToolResult, error) {
	var a auditArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return mcp.Errorf("invalid arguments: %v", err), nil
	}
	if strings.TrimSpace(a.Command) == "" {
		return mcp.Errorf("command is required"), nil
	}

	v := audit.Analyze(a.Command, d.auditCtx(a.Cwd), d.Policy)
	d.record(journal.KindAudit, meta, map[string]any{"command": a.Command, "verdict": v})
	return mcp.Result(renderVerdict(a.Command, v), v)
}

func (d *Deps) execGuarded(ctx context.Context, meta *mcp.Meta, raw json.RawMessage) (*mcp.CallToolResult, error) {
	if !d.AllowExec {
		return mcp.Errorf("exec_guarded is disabled on this server; start toolgate with -allow-exec to enable it"), nil
	}
	var a auditArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return mcp.Errorf("invalid arguments: %v", err), nil
	}
	if strings.TrimSpace(a.Command) == "" {
		return mcp.Errorf("command is required"), nil
	}

	v := audit.Analyze(a.Command, d.auditCtx(a.Cwd), d.Policy)
	if v.Action == audit.ActionDeny.String() {
		d.record(journal.KindExec, meta, map[string]any{
			"command": a.Command, "verdict": v, "executed": false,
			"justification": a.Justification,
		})
		// A refusal is a tool-level error, not a protocol error, so the model
		// receives the text and can act on it. See the note on CallToolResult.
		return mcp.Errorf("%s\n\nThe command was not run. Call explain_rule with the rule id for the reasoning, then propose a different approach.",
			renderVerdict(a.Command, v)), nil
	}

	cwd := a.Cwd
	if cwd == "" {
		cwd = d.Workspace
	}
	timeout := d.ExecTimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The command is handed to a shell because the agent wrote shell. Parsing
	// it does not make it safe to reconstruct: re-assembling argv from the
	// parse and executing that directly would run something subtly different
	// from what was audited, and "audited one thing, ran another" is a worse
	// failure than any it would prevent.
	cmd := exec.CommandContext(runCtx, "/bin/sh", "-c", a.Command)
	cmd.Dir = cwd
	out, runErr := cmd.CombinedOutput()

	result := map[string]any{
		"verdict":   v,
		"exit_code": cmd.ProcessState.ExitCode(),
		"output":    string(out),
		"timed_out": runCtx.Err() != nil,
	}
	rec := d.record(journal.KindExec, meta, map[string]any{
		"command": a.Command, "verdict": v, "executed": true,
		"exit_code": cmd.ProcessState.ExitCode(), "justification": a.Justification,
	})
	result["journal_seq"] = rec

	text := fmt.Sprintf("%s\n\nexit %d\n%s", renderVerdict(a.Command, v),
		cmd.ProcessState.ExitCode(), truncateOutput(string(out)))
	res, err := mcp.Result(text, result)
	if err != nil {
		return nil, err
	}
	res.IsError = runErr != nil
	return res, nil
}

func (d *Deps) snapshotWorkspace(_ context.Context, meta *mcp.Meta, raw json.RawMessage) (*mcp.CallToolResult, error) {
	var a struct {
		Path string `json:"path"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &a)
	}
	root := d.Workspace
	if a.Path != "" {
		root = filepath.Join(d.Workspace, a.Path)
	}

	snap, err := snapshot.Capture(root, snapshot.DefaultLimits())
	if err != nil {
		return mcp.Errorf("snapshot failed: %v", err), nil
	}
	d.retain(snap)

	handle, err := d.Signer.Mint("snapshot", snap.Tree, meta.Principal(), mcp.DefaultHandleTTL)
	if err != nil {
		return nil, err
	}
	d.record(journal.KindSnapshot, meta, map[string]any{
		"root": snap.Root, "tree": snap.Tree, "files": len(snap.Entries), "skipped": snap.Skipped,
	})

	text := fmt.Sprintf("snapshot %s\n  %d files, %d skipped, %s\n  handle: %s",
		snap.Tree[:12], len(snap.Entries), snap.Skipped, humanBytes(snap.TotalSize), handle)
	return mcp.Result(text, map[string]any{
		"handle": handle, "tree": snap.Tree, "files": len(snap.Entries),
		"skipped": snap.Skipped, "total_size": snap.TotalSize,
	})
}

func (d *Deps) diffSnapshot(_ context.Context, meta *mcp.Meta, raw json.RawMessage) (*mcp.CallToolResult, error) {
	var a struct {
		Handle string `json:"handle"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return mcp.Errorf("invalid arguments: %v", err), nil
	}

	payload, err := d.Signer.Redeem(a.Handle, meta.Principal())
	if err != nil {
		return mcp.Errorf("handle rejected: %v", err), nil
	}
	before, ok := d.snapshots[payload.Value]
	if !ok {
		return mcp.Errorf("snapshot %s is no longer held by this server; take a new one", payload.Value[:12]), nil
	}

	root := d.Workspace
	if a.Path != "" {
		root = filepath.Join(d.Workspace, a.Path)
	}
	after, err := snapshot.Capture(root, snapshot.DefaultLimits())
	if err != nil {
		return mcp.Errorf("snapshot failed: %v", err), nil
	}

	changes := snapshot.Diff(before, after)
	var b strings.Builder
	fmt.Fprintf(&b, "%d change(s) since %s\n", len(changes), payload.Value[:12])
	for i, c := range changes {
		if i == 50 {
			fmt.Fprintf(&b, "  ... and %d more\n", len(changes)-50)
			break
		}
		fmt.Fprintf(&b, "  %-8s %s\n", c.Kind, c.Path)
	}
	return mcp.Result(b.String(), map[string]any{
		"from": payload.Value, "to": after.Tree, "changes": changes,
	})
}

func (d *Deps) explainRule(_ context.Context, _ *mcp.Meta, raw json.RawMessage) (*mcp.CallToolResult, error) {
	var a struct {
		RuleID string `json:"rule_id"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &a)
	}

	if id := strings.ToUpper(strings.TrimSpace(a.RuleID)); id != "" {
		for _, r := range audit.Rules {
			if r.ID == id {
				text := fmt.Sprintf("%s  %s  [%s]\n\n%s", r.ID, r.Title, r.Severity, r.Rationale)
				return mcp.Result(text, map[string]any{
					"rule_id": r.ID, "title": r.Title,
					"severity": r.Severity.String(), "rationale": r.Rationale,
				})
			}
		}
		return mcp.Errorf("no rule %q", a.RuleID), nil
	}

	rules := make([]map[string]string, 0, len(audit.Rules))
	var b strings.Builder
	for _, r := range audit.Rules {
		fmt.Fprintf(&b, "%s  %-10s %s\n", r.ID, r.Severity, r.Title)
		rules = append(rules, map[string]string{
			"rule_id": r.ID, "title": r.Title, "severity": r.Severity.String(),
		})
	}
	return mcp.Result(b.String(), map[string]any{"rules": rules, "policy": d.Policy.Name})
}

// retain keeps a bounded set of snapshots, evicting oldest first.
func (d *Deps) retain(s *snapshot.Snapshot) {
	if _, exists := d.snapshots[s.Tree]; exists {
		return
	}
	d.snapshots[s.Tree] = s
	d.order = append(d.order, s.Tree)
	for len(d.order) > maxRetainedSnapshots {
		delete(d.snapshots, d.order[0])
		d.order = d.order[1:]
	}
}

func (d *Deps) record(kind journal.Kind, meta *mcp.Meta, body any) uint64 {
	if d.Journal == nil {
		return 0
	}
	rec, err := d.Journal.Append(kind, "", meta.Principal(), body)
	if err != nil {
		// A journal failure must not stop the guard from answering. The verdict
		// is still correct; what is lost is the record of it, and trading a
		// working guardrail for a complete audit trail is the wrong way round.
		fmt.Fprintf(os.Stderr, "toolgate: journal append failed: %v\n", err)
		return 0
	}
	return rec.Seq
}

func renderVerdict(cmd string, v audit.Verdict) string {
	var b strings.Builder
	mark := map[string]string{"allow": "ALLOW", "require_approval": "APPROVE", "deny": "DENY"}[v.Action]
	fmt.Fprintf(&b, "%s  %s\n", mark, oneLine(cmd))
	fmt.Fprintf(&b, "  read-only: %v   policy: %s   %.0fus\n",
		v.ReadOnly, v.Policy, float64(v.ElapsedNs)/1000)
	if len(v.Findings) == 0 {
		b.WriteString("  no rule matched\n")
		return b.String()
	}
	seen := map[string]bool{}
	for _, f := range v.Findings {
		key := f.RuleID + f.Detail
		if seen[key] {
			continue
		}
		seen[key] = true
		fmt.Fprintf(&b, "  %s [%s] %s\n", f.RuleID, f.Severity, f.Detail)
		if f.Fix != "" {
			fmt.Fprintf(&b, "        try: %s\n", f.Fix)
		}
	}
	return b.String()
}

func oneLine(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", "")
	if len(s) > 120 {
		return s[:117] + "..."
	}
	return s
}

func truncateOutput(s string) string {
	const max = 8000
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n... (%d bytes truncated)", len(s)-max)
}

func humanBytes(n int64) string {
	switch {
	case n > 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n > 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n > 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// RuleIDs returns every rule id, sorted. Used by the CLI's list output.
func RuleIDs() []string {
	ids := make([]string, 0, len(audit.Rules))
	for _, r := range audit.Rules {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	return ids
}
