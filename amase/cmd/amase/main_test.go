package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), version) {
		t.Fatalf("version missing from %q", out.String())
	}
}

func TestUsageAndUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != exitUsage || !strings.Contains(errOut.String(), "usage:") {
		t.Fatalf("no-args: exit %d, stderr %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"frobnicate"}, &out, &errOut); code != exitUsage || !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("unknown: exit %d, stderr %q", code, errOut.String())
	}
	out.Reset()
	if code := run([]string{"--help"}, &out, &errOut); code != exitOK || !strings.Contains(out.String(), "serve") {
		t.Fatalf("help: exit %d, stdout %q", code, out.String())
	}
}

func TestCommandNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range commands() {
		if seen[c.name] {
			t.Fatalf("duplicate command %q", c.name)
		}
		seen[c.name] = true
	}
}
