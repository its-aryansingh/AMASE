// Command amase is the AMASE workspace: a control plane that runs coding agents
// in isolated git worktrees and decides, before each tool call executes,
// whether it may run.
//
// Exit codes: 0 success, 1 a check failed (a broken ledger, a denied action),
// 2 usage or internal error.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is overwritten at release time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

const (
	exitOK    = 0
	exitCheck = 1
	exitUsage = 2
)

type command struct {
	name    string
	summary string
	run     func(args []string, stdout, stderr io.Writer) int
}

// commands is the subcommand table. New subcommands are appended; existing
// names never change, because scripts and hooks call them.
func commands() []command {
	return []command{
		{"version", "print the version", cmdVersion},
		{"serve", "run the workspace daemon and web UI", notYet("serve", "WP-5.1")},
		{"run", "run one or more agent tasks headless", notYet("run", "WP-3.4")},
		{"verify", "check the hash chain of a receipt ledger", cmdVerify},
		{"demo", "create a small demo repository to try amase on", cmdDemo},
		{"waste", "report cost and waste in local agent transcripts", notYet("waste", "WP-4.1")},
		{"review", "run an independent read-only review of a task's diff", notYet("review", "WP-6.1")},
		{"bench", "measure the decision hot path", notYet("bench", "WP-7.1")},
		{"doctor", "check the environment amase depends on", notYet("doctor", "WP-6.3")},
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		usage(stdout)
		return exitOK
	}
	for _, c := range commands() {
		if c.name == args[0] {
			return c.run(args[1:], stdout, stderr)
		}
	}
	fmt.Fprintf(stderr, "amase: unknown command %q\n\n", args[0])
	usage(stderr)
	return exitUsage
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "amase %s — run coding agents in isolated worktrees, gated before every tool call\n\n", version)
	fmt.Fprintln(w, "usage: amase <command> [flags]")
	fmt.Fprintln(w)
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-8s %s\n", c.name, c.summary)
	}
}

func cmdVersion(_ []string, stdout, _ io.Writer) int {
	fmt.Fprintf(stdout, "amase %s\n", version)
	return exitOK
}

func notYet(name, wp string) func([]string, io.Writer, io.Writer) int {
	return func(_ []string, _, stderr io.Writer) int {
		fmt.Fprintf(stderr, "amase %s is not implemented yet (%s)\n", name, wp)
		return exitUsage
	}
}
