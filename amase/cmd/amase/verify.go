package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/its-aryansingh/amase/internal/paths"
	"github.com/its-aryansingh/toolgate/ledger"
)

// cmdVerify recomputes a ledger's hash chain. With no argument it checks the
// receipt ledger in the state directory. Exit 1 means the chain is broken: a
// record was edited, deleted or reordered after it was written.
func cmdVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: amase verify [ledger.ndjson]   (default: the receipt ledger in the state directory)")
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	path := fs.Arg(0)
	if path == "" {
		dir, err := paths.StateDir()
		if err != nil {
			fmt.Fprintf(stderr, "amase verify: %v\n", err)
			return exitUsage
		}
		path = paths.Layout{Root: dir}.Receipts()
	}

	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "amase verify: no ledger at %s\n", path)
		return exitUsage
	}
	if err != nil {
		fmt.Fprintf(stderr, "amase verify: %v\n", err)
		return exitUsage
	}
	defer f.Close()

	res, err := ledger.Verify(f)
	if err != nil {
		fmt.Fprintf(stdout, "BROKEN  %s\n  unreadable record: %v\n", path, err)
		return exitCheck
	}
	if !res.OK {
		fmt.Fprintf(stdout, "BROKEN  %s\n  record %d: %s\n  (%d records read before the break)\n", path, res.BreakAt, res.BreakWhy, res.Records-1)
		return exitCheck
	}
	fmt.Fprintf(stdout, "OK  %s\n  %d records, chain tip %s\n", path, res.Records, res.Tip)
	return exitOK
}
