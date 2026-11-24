package gitws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RewindResult reports what a rewind changed and how to undo it.
type RewindResult struct {
	Undo     Checkpoint `json:"undo"`
	Restored []string   `json:"restored"` // modified or deleted since the target; written back
	Removed  []string   `json:"removed"`  // created since the target; deleted
}

// Rewind restores the worktree to target and verifies the result.
//
// It first snapshots the current state under refs/amase/undo/..., so a rewind
// is itself undoable. Then it deletes files that exist now but not in the
// target, writes every target file back from a throwaway index, and finally
// re-reads the worktree and checks that its tree equals the target's. Ignored
// files are outside snapshots and are left alone; that limit is by design.
func (m *Manager) Rewind(ctx context.Context, wt Worktree, target Checkpoint) (RewindResult, error) {
	undoRef := fmt.Sprintf("refs/amase/undo/%s/%d", wt.TaskID, time.Now().UnixNano())
	undo, err := m.Snapshot(ctx, wt, undoRef)
	if err != nil {
		return RewindResult{}, fmt.Errorf("gitws: recording undo point: %w", err)
	}

	added, err := m.diffNames(ctx, wt, target.Tree, undo.Tree, "A")
	if err != nil {
		return RewindResult{}, err
	}
	restored, err := m.diffNames(ctx, wt, target.Tree, undo.Tree, "MDT")
	if err != nil {
		return RewindResult{}, err
	}

	for _, rel := range added {
		p := filepath.Join(wt.Path, filepath.FromSlash(rel))
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return RewindResult{}, fmt.Errorf("gitws: removing %s: %w", rel, err)
		}
		removeEmptyParents(wt.Path, filepath.Dir(p))
	}

	env, cleanup, err := tempIndex()
	if err != nil {
		return RewindResult{}, err
	}
	defer cleanup()
	if _, err := m.Git.Run(ctx, wt.Path, env, "read-tree", target.Tree); err != nil {
		return RewindResult{}, err
	}
	if _, err := m.Git.Run(ctx, wt.Path, env, "checkout-index", "-a", "-f"); err != nil {
		return RewindResult{}, err
	}

	got, err := m.worktreeTree(ctx, wt)
	if err != nil {
		return RewindResult{}, err
	}
	if got != target.Tree {
		return RewindResult{Undo: undo}, fmt.Errorf("gitws: rewind verification failed: worktree tree %s, target %s (undo point %s)", got, target.Tree, undo.Ref)
	}
	return RewindResult{Undo: undo, Restored: restored, Removed: added}, nil
}

// diffNames lists paths that differ between trees a and b, filtered by git's
// --diff-filter letters, as slash-separated paths relative to the root.
func (m *Manager) diffNames(ctx context.Context, wt Worktree, a, b, filter string) ([]string, error) {
	out, err := m.Git.Run(ctx, wt.Path, nil, "diff-tree", "-r", "-z", "--name-only", "--no-renames", "--diff-filter="+filter, a, b)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, n := range strings.Split(out, "\x00") {
		if n != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

// removeEmptyParents deletes dir and its parents while they are empty, stopping at root.
func removeEmptyParents(root, dir string) {
	root = filepath.Clean(root)
	for dir = filepath.Clean(dir); dir != root && within(root, dir); dir = filepath.Dir(dir) {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if os.Remove(dir) != nil {
			return
		}
	}
}

// Diff returns the unified diff between two checkpoints.
func (m *Manager) Diff(ctx context.Context, wt Worktree, from, to Checkpoint) (string, error) {
	return m.Git.Run(ctx, wt.Path, nil, "diff", "--no-color", "--no-ext-diff", "--no-textconv", from.Commit, to.Commit)
}
