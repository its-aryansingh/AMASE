// Package paths decides where amase keeps its state. Never inside a repository
// (amase must not pollute what it governs) and never inside a synced folder
// such as OneDrive, where file locks and sync conflicts corrupt git objects.
package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// StateDir returns the state directory for this machine. AMASE_HOME overrides it.
func StateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the home directory: %w", err)
	}
	return stateDir(runtime.GOOS, os.Getenv, home), nil
}

func stateDir(goos string, getenv func(string) string, home string) string {
	if v := getenv("AMASE_HOME"); v != "" {
		return filepath.Clean(v)
	}
	switch goos {
	case "windows":
		if v := getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "amase")
		}
		return filepath.Join(home, "AppData", "Local", "amase")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "amase")
	default:
		if v := getenv("XDG_STATE_HOME"); v != "" {
			return filepath.Join(v, "amase")
		}
		return filepath.Join(home, ".local", "state", "amase")
	}
}

// Layout names everything under one state directory.
type Layout struct {
	Root string
}

func (l Layout) Events() string    { return filepath.Join(l.Root, "events.ndjson") }
func (l Layout) Receipts() string  { return filepath.Join(l.Root, "receipts.ndjson") }
func (l Layout) Worktrees() string { return filepath.Join(l.Root, "wt") }
func (l Layout) NoHooks() string   { return filepath.Join(l.Root, "nohooks") }
func (l Layout) OTLP() string      { return filepath.Join(l.Root, "otlp") }

// Ensure creates the directories the layout needs. The nohooks directory must
// exist and stay empty: every internal git command points core.hooksPath at it.
func (l Layout) Ensure() error {
	for _, d := range []string{l.Root, l.Worktrees(), l.NoHooks()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
	}
	return nil
}

// RepoID is a short stable identifier for a repository path: the first twelve
// hex digits of the SHA-256 of its cleaned path. On Windows the path is
// case-folded first, because C:\Repo and c:\repo are the same directory there.
func RepoID(absPath string) string {
	return repoID(runtime.GOOS, absPath)
}

func repoID(goos, absPath string) string {
	// Normalise with slash semantics explicitly rather than the host's
	// filepath rules, so a Windows path hashes the same whichever OS computes it.
	p := filepath.ToSlash(absPath)
	if goos == "windows" {
		p = strings.ToLower(strings.ReplaceAll(p, `\`, "/"))
	}
	p = path.Clean(p)
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:])[:12]
}
