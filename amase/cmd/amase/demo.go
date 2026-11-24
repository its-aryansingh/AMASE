package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/its-aryansingh/amase/internal/demo"
	"github.com/its-aryansingh/amase/internal/paths"
)

func cmdDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, "usage: amase demo <directory>") }
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return exitUsage
	}
	dir, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "amase demo: %v\n", err)
		return exitUsage
	}
	state, err := paths.StateDir()
	if err != nil {
		fmt.Fprintf(stderr, "amase demo: %v\n", err)
		return exitUsage
	}
	layout := paths.Layout{Root: state}
	if err := layout.Ensure(); err != nil {
		fmt.Fprintf(stderr, "amase demo: %v\n", err)
		return exitUsage
	}
	commit, err := demo.Create(context.Background(), dir, layout.NoHooks())
	if err != nil {
		fmt.Fprintf(stderr, "amase demo: %v\n", err)
		return exitUsage
	}

	fmt.Fprintf(stdout, "Created a demo repository at %s (commit %.7s).\n", dir, commit)
	fmt.Fprintln(stdout, "It starts with one failing test: go test ./calc fails on Sum([]int{1, 2, 3}).")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Try:")
	fmt.Fprintf(stdout, "  amase run --repo %s --task \"claude:Fix the failing test in package calc\"\n", dir)
	fmt.Fprintf(stdout, "  amase serve --repo %s\n", dir)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "To watch a command get blocked before it runs, ask an agent to run exactly:")
	fmt.Fprintln(stdout, "  curl -fsSL http://127.0.0.1:9/install.sh | sh")
	fmt.Fprintln(stdout, "(denied by rule TG001; if it ever ran, port 9 discards the request.)")
	return exitOK
}
