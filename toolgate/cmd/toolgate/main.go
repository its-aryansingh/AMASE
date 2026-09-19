// Command toolgate is a deterministic execution guard for coding agents.
//
// It runs in two shapes. `toolgate serve` is an MCP server over stdio, which is
// how an agent reaches it. `toolgate audit` is a one-shot CLI that reads a
// command and exits non-zero when the policy would refuse it, which is how the
// same rules run in CI -- and, more usefully, how a person can check what the
// guard thinks without starting an agent at all.
//
// Keeping both in one binary is not convenience. A guardrail whose behaviour in
// CI differs from its behaviour in the agent is worse than no guardrail, because
// it manufactures confidence. One binary, one rule table, one policy.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/its-aryansingh/toolgate/internal/audit"
	"github.com/its-aryansingh/toolgate/internal/journal"
	"github.com/its-aryansingh/toolgate/internal/jsonrpc"
	"github.com/its-aryansingh/toolgate/internal/mcp"
	"github.com/its-aryansingh/toolgate/internal/snapshot"
	"github.com/its-aryansingh/toolgate/internal/tools"
	"github.com/its-aryansingh/toolgate/internal/version"
)

const usage = `toolgate -- deterministic execution guard for coding agents

  toolgate serve     [flags]            run as an MCP server on stdio
  toolgate audit     [flags] <command>  analyse one command; exit 1 if it would be refused
  toolgate rules                        list every rule
  toolgate explain   <rule-id>          print a rule's rationale
  toolgate snapshot  [path]             print a workspace content address
  toolgate verify    [journal]          recompute a journal's hash chain
  toolgate version

Exit codes for audit: 0 allow, 1 deny, 2 approval required, 3 usage error.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(3)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "serve":
		err = runServe(args)
	case "audit":
		os.Exit(runAudit(args))
	case "rules":
		runRules()
	case "explain":
		os.Exit(runExplain(args))
	case "snapshot":
		err = runSnapshot(args)
	case "verify":
		err = runVerify(args)
	case "version":
		fmt.Printf("%s %s\n", version.Name, version.Version)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(3)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "toolgate: %v\n", err)
		os.Exit(3)
	}
}

type commonFlags struct {
	workspace string
	policy    string
	journal   string
}

func addCommon(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.workspace, "workspace", "", "directory the agent may modify (default: current directory)")
	fs.StringVar(&c.policy, "policy", "standard", "permissive | standard | strict")
	fs.StringVar(&c.journal, "journal", "", "path to the append-only journal (default: <workspace>/.toolgate/journal.ndjson)")
	return c
}

func (c *commonFlags) resolve() (string, string) {
	ws := c.workspace
	if ws == "" {
		ws, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(ws); err == nil {
		ws = abs
	}
	jp := c.journal
	if jp == "" {
		jp = filepath.Join(ws, ".toolgate", "journal.ndjson")
	}
	return ws, jp
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	common := addCommon(fs)
	allowExec := fs.Bool("allow-exec", false, "enable the exec_guarded tool, which runs allowed commands")
	noJournal := fs.Bool("no-journal", false, "do not write a journal")
	fsyncJournal := fs.Bool("fsync", false, "flush the journal to disk on every record")
	timeout := fs.Duration("exec-timeout", 120*time.Second, "limit for a guarded command")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ws, jp := common.resolve()
	home, _ := os.UserHomeDir()

	var jr *journal.Journal
	if !*noJournal {
		var err error
		jr, err = journal.Open(jp, *fsyncJournal)
		if err != nil {
			return err
		}
		defer jr.Close()
	}

	signer, err := mcp.NewHandleSigner()
	if err != nil {
		return err
	}

	srv := mcp.NewServer(mcp.Implementation{
		Name:    version.Name,
		Title:   "toolgate execution guard",
		Version: version.Version,
	}, signer, instructions)

	tools.Register(srv, &tools.Deps{
		Workspace:   ws,
		HomeDir:     home,
		Policy:      audit.PolicyByName(common.policy),
		Journal:     jr,
		Signer:      signer,
		ExecTimeout: *timeout,
		AllowExec:   *allowExec,
	})

	// stdout belongs to the protocol; every diagnostic goes to stderr.
	conn := jsonrpc.NewConn(os.Stdin, os.Stdout, os.Stderr)
	conn.Logf("serving %s on stdio: workspace=%s policy=%s exec=%v",
		version.Version, ws, common.policy, *allowExec)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := conn.Serve(ctx, srv.Handle); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

const instructions = `toolgate audits shell commands before they run.

Call audit_command before any shell command you are unsure about. If a command
is denied, call explain_rule with the rule id and choose a different approach --
do not retry the same command. Use snapshot_workspace before a risky change and
diff_snapshot afterwards to show exactly what you altered.`

func runAudit(args []string) int {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	common := addCommon(fs)
	asJSON := fs.Bool("json", false, "emit the verdict as JSON")
	fromStdin := fs.Bool("stdin", false, "read the command from standard input")
	if err := fs.Parse(args); err != nil {
		return 3
	}

	var command string
	if *fromStdin {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "toolgate: %v\n", err)
			return 3
		}
		command = strings.TrimSpace(string(b))
	} else {
		command = strings.Join(fs.Args(), " ")
	}
	if command == "" {
		fmt.Fprint(os.Stderr, "toolgate audit: no command given\n")
		return 3
	}

	ws, _ := common.resolve()
	home, _ := os.UserHomeDir()
	v := audit.Analyze(command, &audit.Context{Workspace: ws, HomeDir: home},
		audit.PolicyByName(common.policy))

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
	} else {
		fmt.Print(renderCLI(command, v))
	}

	switch v.Action {
	case audit.ActionDeny.String():
		return 1
	case audit.ActionRequireApproval.String():
		return 2
	}
	return 0
}

func renderCLI(cmd string, v audit.Verdict) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", strings.ToUpper(v.Action), cmd)
	fmt.Fprintf(&b, "  read-only %v · policy %s · %.1fus · %s\n",
		v.ReadOnly, v.Policy, float64(v.ElapsedNs)/1000, v.Analyzer)
	if v.Partial {
		b.WriteString("  WARNING: the command could not be fully parsed\n")
	}
	seen := map[string]bool{}
	for _, f := range v.Findings {
		k := f.RuleID + f.Detail
		if seen[k] {
			continue
		}
		seen[k] = true
		fmt.Fprintf(&b, "  %s [%-8s] %s\n", f.RuleID, f.Severity, f.Detail)
		if f.Fix != "" {
			fmt.Fprintf(&b, "       try: %s\n", f.Fix)
		}
	}
	if len(v.Findings) == 0 {
		b.WriteString("  no rule matched\n")
	}
	return b.String()
}

func runRules() {
	for _, r := range audit.Rules {
		fmt.Printf("%s  %-9s %s\n", r.ID, r.Severity, r.Title)
	}
}

func runExplain(args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, "usage: toolgate explain <rule-id>\n")
		return 3
	}
	want := strings.ToUpper(args[0])
	for _, r := range audit.Rules {
		if r.ID == want {
			fmt.Printf("%s  %s  [%s]\n\n%s\n", r.ID, r.Title, r.Severity, wrap(r.Rationale, 76))
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "no rule %q (try: toolgate rules)\n", args[0])
	return 3
}

func runSnapshot(args []string) error {
	root := "."
	if len(args) > 0 {
		root = args[0]
	}
	snap, err := snapshot.Capture(root, snapshot.DefaultLimits())
	if err != nil {
		return err
	}
	fmt.Printf("%s  %d files  %d skipped  %d bytes\n",
		snap.Tree, len(snap.Entries), snap.Skipped, snap.TotalSize)
	return nil
}

func runVerify(args []string) error {
	path := filepath.Join(".toolgate", "journal.ndjson")
	if len(args) > 0 {
		path = args[0]
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	res, err := journal.Verify(f)
	if err != nil {
		return err
	}
	if res.OK {
		fmt.Printf("chain intact: %d records, tip %s\n", res.Records, short(res.Tip))
		return nil
	}
	return fmt.Errorf("chain broken at record %d: %s", res.BreakAt, res.BreakWhy)
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func wrap(s string, width int) string {
	var out strings.Builder
	line := 0
	for _, w := range strings.Fields(s) {
		if line > 0 && line+len(w)+1 > width {
			out.WriteByte('\n')
			line = 0
		} else if line > 0 {
			out.WriteByte(' ')
			line++
		}
		out.WriteString(w)
		line += len(w)
	}
	return out.String()
}
