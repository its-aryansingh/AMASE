package gitws

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Checkpoint is one recorded worktree state.
type Checkpoint struct {
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
}

// TurnRef names the snapshot ref for one phase ("pre", "post", "post-crash") of a turn.
func TurnRef(session string, turn int, phase string) string {
	return fmt.Sprintf("refs/amase/turns/%s/%d/%s", session, turn, phase)
}

// snapshotIdentity makes snapshot commits independent of the user's git
// identity, so they work on a machine with no user.name configured.
var snapshotIdentity = []string{
	"GIT_AUTHOR_NAME=amase", "GIT_AUTHOR_EMAIL=amase@localhost",
	"GIT_COMMITTER_NAME=amase", "GIT_COMMITTER_EMAIL=amase@localhost",
}

// Snapshot records the whole worktree state — tracked files plus untracked
// files that are not ignored — as a commit under ref.
//
// It builds the tree in a throwaway index (GIT_INDEX_FILE), so the worktree's
// own index, the branch and the stash are never touched, and it uses plumbing
// (write-tree, commit-tree, update-ref), which runs no hooks. Because it reads
// the filesystem rather than an agent's edit log, it captures whatever a shell
// command did: rm, mv, sed -i, a formatter, a migration.
func (m *Manager) Snapshot(ctx context.Context, wt Worktree, ref string) (Checkpoint, error) {
	if !strings.HasPrefix(ref, "refs/amase/") {
		return Checkpoint{}, fmt.Errorf("gitws: snapshot ref %q must be under refs/amase/", ref)
	}
	tree, err := m.worktreeTree(ctx, wt)
	if err != nil {
		return Checkpoint{}, err
	}
	parent, err := m.Git.Run(ctx, wt.Path, nil, "rev-parse", "HEAD")
	if err != nil {
		return Checkpoint{}, err
	}
	commit, err := m.Git.Run(ctx, wt.Path, snapshotIdentity,
		"commit-tree", "--no-gpg-sign", tree, "-p", trim(parent), "-m", "amase snapshot "+ref)
	if err != nil {
		return Checkpoint{}, err
	}
	if _, err := m.Git.Run(ctx, wt.Path, nil, "update-ref", ref, trim(commit)); err != nil {
		return Checkpoint{}, err
	}
	return Checkpoint{Ref: ref, Commit: trim(commit), Tree: tree}, nil
}

// worktreeTree writes the current worktree state as a tree object and returns its id.
func (m *Manager) worktreeTree(ctx context.Context, wt Worktree) (string, error) {
	env, cleanup, err := tempIndex()
	if err != nil {
		return "", err
	}
	defer cleanup()
	if _, err := m.Git.Run(ctx, wt.Path, env, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := m.Git.Run(ctx, wt.Path, env, "add", "-A"); err != nil {
		return "", err
	}
	tree, err := m.Git.Run(ctx, wt.Path, env, "write-tree")
	if err != nil {
		return "", err
	}
	return trim(tree), nil
}

// tempIndex returns the environment for a private index file and a cleanup
// func. The file must not exist beforehand: git rejects an empty index file.
func tempIndex() ([]string, func(), error) {
	dir, err := os.MkdirTemp("", "amase-index-")
	if err != nil {
		return nil, nil, err
	}
	return []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index")}, func() { _ = os.RemoveAll(dir) }, nil
}
