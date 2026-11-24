// Package gitws manages task worktrees and the hidden-ref snapshots that let
// any turn be rewound, including changes made by shell commands, which an
// agent's own undo (Claude Code's /rewind tracks only its edit tools) misses.
//
// Every git invocation goes through Git.Run: argv only, a timeout, a scrubbed
// environment, and core.hooksPath pointed at an empty directory. The last one
// matters on real machines: a global post-checkout hook can rewrite a fresh
// worktree, and a post-commit hook can push.
package gitws

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Git runs git subprocesses for gitws.
type Git struct {
	// NoHooks is an empty directory used as core.hooksPath. Required.
	NoHooks string
	// Timeout bounds each invocation; zero means two minutes.
	Timeout time.Duration
}

// ErrNoHooksDir is returned when Git.NoHooks is unset: running git with the
// user's hooks enabled is never the right default for automation.
var ErrNoHooksDir = errors.New("gitws: Git.NoHooks must name an empty directory")

// Run executes `git <args>` in dir with extra environment variables and returns stdout.
func (g Git) Run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	if g.NoHooks == "" {
		return "", ErrNoHooksDir
	}
	timeout := g.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	full := []string{"-c", "core.hooksPath=" + g.NoHooks}
	if runtime.GOOS == "windows" {
		full = append(full, "-c", "core.longpaths=true")
	}
	if dir != "" {
		full = append(full, "-C", dir)
	}
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(scrubGitEnv(os.Environ()), append([]string{"GIT_TERMINAL_PROMPT=0"}, env...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// scrubGitEnv drops variables that would redirect git to a different
// repository, index or object store than the one named on the command line.
func scrubGitEnv(env []string) []string {
	drop := map[string]bool{
		"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true,
		"GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
		"GIT_COMMON_DIR": true, "GIT_PREFIX": true, "GIT_NAMESPACE": true,
	}
	out := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[strings.ToUpper(k)] {
			out = append(out, kv)
		}
	}
	return out
}

func trim(s string) string { return strings.TrimSpace(s) }
