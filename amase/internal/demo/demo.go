// Package demo writes a tiny Go project with exactly one failing test, so
// amase can be tried on something real, cheaply, and identically every time.
package demo

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/its-aryansingh/amase/internal/gitws"
)

// Go sources and go.mod are stored with a .txt suffix: as .go files they would
// be compiled and tested as part of the amase module (the failing test would
// fail amase's own CI), and a go.mod would make the tree a separate module,
// which go:embed refuses to include.
//
//go:embed all:files
var files embed.FS

// fixedIdentity makes the demo commit byte-for-byte reproducible.
var fixedIdentity = []string{
	"GIT_AUTHOR_NAME=amase demo", "GIT_AUTHOR_EMAIL=demo@amase.invalid", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
	"GIT_COMMITTER_NAME=amase demo", "GIT_COMMITTER_EMAIL=demo@amase.invalid", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
}

// Create writes the demo project into dir, which must not exist or must be
// empty, initialises a repository without any hooks, commits, and returns the
// commit id. noHooks must be an empty directory; it doubles as the template
// directory, so no hooks are copied in either.
func Create(ctx context.Context, dir, noHooks string) (string, error) {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return "", fmt.Errorf("demo: %s is not empty", dir)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	err := fs.WalkDir(files, "files", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(p, "files/"), ".txt")
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		return "", fmt.Errorf("demo: writing files: %w", err)
	}

	git := gitws.Git{NoHooks: noHooks}
	steps := [][]string{
		{"init", "-q", "--template=" + noHooks, "-b", "main", "."},
		{"config", "core.autocrlf", "false"},
		{"add", "-A"},
		{"-c", "commit.gpgsign=false", "commit", "-q", "-m", "Demo project for amase: one failing test"},
	}
	for _, args := range steps {
		if _, err := git.Run(ctx, dir, fixedIdentity, args...); err != nil {
			return "", fmt.Errorf("demo: %w", err)
		}
	}
	head, err := git.Run(ctx, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(head), nil
}
