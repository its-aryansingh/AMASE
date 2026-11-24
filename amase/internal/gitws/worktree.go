package gitws

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/its-aryansingh/amase/internal/paths"
)

// Worktree is one task's isolated checkout.
type Worktree struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
	TaskID string `json:"task_id"`
}

// Manager owns the worktrees under one state directory.
type Manager struct {
	Git  Git
	Root string // <state>/wt

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewManager returns a Manager rooted at root, running git with hooks disabled via noHooks.
func NewManager(root, noHooks string) *Manager {
	return &Manager{Git: Git{NoHooks: noHooks}, Root: root, locks: map[string]*sync.Mutex{}}
}

// repoLock serialises git operations per repository: concurrent `worktree add`
// calls on one repo race on its .git/worktrees administrative files.
func (m *Manager) repoLock(repo string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[repo]
	if !ok {
		l = &sync.Mutex{}
		m.locks[repo] = l
	}
	return l
}

var taskIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidTaskID reports whether id is safe to use in a path and a ref name.
func ValidTaskID(id string) bool { return taskIDPattern.MatchString(id) }

// Toplevel resolves the repository root containing dir.
func (m *Manager) Toplevel(ctx context.Context, dir string) (string, error) {
	out, err := m.Git.Run(ctx, dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.FromSlash(trim(out))), nil
}

// CreateWorktree checks out baseRef (default HEAD) on a new branch amase/<taskID>
// in a fresh worktree under the state directory.
func (m *Manager) CreateWorktree(ctx context.Context, repo, taskID, baseRef string) (Worktree, error) {
	if !ValidTaskID(taskID) {
		return Worktree{}, fmt.Errorf("gitws: invalid task id %q", taskID)
	}
	top, err := m.Toplevel(ctx, repo)
	if err != nil {
		return Worktree{}, err
	}
	if baseRef == "" {
		baseRef = "HEAD"
	}
	l := m.repoLock(top)
	l.Lock()
	defer l.Unlock()

	path := filepath.Join(m.Root, paths.RepoID(top), taskID)
	if _, err := os.Stat(path); err == nil {
		return Worktree{}, fmt.Errorf("gitws: worktree path %s already exists", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Worktree{}, err
	}
	branch := "amase/" + taskID
	if _, err := m.Git.Run(ctx, top, nil, "worktree", "add", "-b", branch, path, baseRef); err != nil {
		return Worktree{}, err
	}
	return Worktree{Repo: top, Path: path, Branch: branch, TaskID: taskID}, nil
}

// RemoveWorktree deletes the worktree (discarding its changes) and optionally
// its branch. Hidden snapshot refs are kept: they are the audit trail.
func (m *Manager) RemoveWorktree(ctx context.Context, wt Worktree, deleteBranch bool) error {
	l := m.repoLock(wt.Repo)
	l.Lock()
	defer l.Unlock()
	if _, err := m.Git.Run(ctx, wt.Repo, nil, "worktree", "remove", "--force", wt.Path); err != nil {
		return err
	}
	if deleteBranch {
		if _, err := m.Git.Run(ctx, wt.Repo, nil, "branch", "-D", wt.Branch); err != nil {
			return err
		}
	}
	return nil
}

// List returns the amase-managed worktrees of repo.
func (m *Manager) List(ctx context.Context, repo string) ([]Worktree, error) {
	top, err := m.Toplevel(ctx, repo)
	if err != nil {
		return nil, err
	}
	out, err := m.Git.Run(ctx, top, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	root := filepath.Clean(m.Root)
	var list []Worktree
	for _, block := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n\n") {
		var wtPath, branch string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				wtPath = filepath.Clean(filepath.FromSlash(strings.TrimPrefix(line, "worktree ")))
			case strings.HasPrefix(line, "branch refs/heads/"):
				branch = strings.TrimPrefix(line, "branch refs/heads/")
			}
		}
		if wtPath == "" || !strings.HasPrefix(branch, "amase/") || !within(root, wtPath) {
			continue
		}
		list = append(list, Worktree{Repo: top, Path: wtPath, Branch: branch, TaskID: strings.TrimPrefix(branch, "amase/")})
	}
	return list, nil
}

// Prune removes administrative records of worktrees whose directories are gone.
func (m *Manager) Prune(ctx context.Context, repo string) error {
	_, err := m.Git.Run(ctx, repo, nil, "worktree", "prune")
	return err
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}
