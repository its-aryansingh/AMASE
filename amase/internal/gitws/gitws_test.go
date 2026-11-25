package gitws

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

var original = map[string]string{
	"main.go":   "package main\n\nfunc x() {}\n",
	"docs/a.md": "alpha\n",
	"docs/b.md": "beta\n",
}

type fixture struct {
	base, repo, marker string
	m                  *Manager
}

// newFixture builds a repo with a committed tree, a post-checkout hook that
// leaves a marker file when it runs, and a Manager with hooks disabled.
func newFixture(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	tmpl := filepath.Join(base, "empty-template")
	nohooks := filepath.Join(base, "nohooks")
	for _, d := range []string{tmpl, nohooks} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, base, "init", "-q", "--template="+tmpl, "-b", "main", repo)
	for _, kv := range [][2]string{{"user.name", "test"}, {"user.email", "test@example.invalid"}, {"core.autocrlf", "false"}, {"commit.gpgsign", "false"}} {
		gitIn(t, repo, "config", kv[0], kv[1])
	}
	for name, content := range original {
		write(t, filepath.Join(repo, filepath.FromSlash(name)), content)
	}
	write(t, filepath.Join(repo, ".gitignore"), "*.log\n")
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "init")

	marker := filepath.Join(base, "hook-ran")
	write(t, filepath.Join(repo, ".git", "hooks", "post-checkout"), "#!/bin/sh\ntouch '"+filepath.ToSlash(marker)+"'\n")
	if err := os.Chmod(filepath.Join(repo, ".git", "hooks", "post-checkout"), 0o755); err != nil {
		t.Fatal(err)
	}
	return fixture{base: base, repo: repo, marker: marker, m: NewManager(filepath.Join(base, "state", "wt"), nohooks)}
}

type userState struct{ head, branch, status, stash string }

func captureUserState(t *testing.T, repo string) userState {
	return userState{
		head:   gitIn(t, repo, "rev-parse", "HEAD"),
		branch: gitIn(t, repo, "symbolic-ref", "HEAD"),
		status: gitIn(t, repo, "status", "--porcelain"),
		stash:  gitIn(t, repo, "stash", "list"),
	}
}

func findBash() string {
	if runtime.GOOS != "windows" {
		p, _ := exec.LookPath("bash")
		return p
	}
	// Never take System32\bash.exe: that is WSL, which sees different paths.
	if p, err := exec.LookPath("bash"); err == nil && !strings.Contains(strings.ToLower(p), `\system32\`) {
		return p
	}
	// Git for Windows keeps bash.exe in Git\bin; git.exe may be in Git\cmd or
	// Git\mingw64\bin, so walk up from wherever git was found.
	g, err := exec.LookPath("git")
	if err != nil {
		return ""
	}
	for dir := filepath.Dir(g); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if p := filepath.Join(dir, "bin", "bash.exe"); exists(p) {
			return p
		}
	}
	return ""
}

// mutate does what an agent's shell commands might: delete a directory, edit a
// file in place, create files, and drop an ignored artefact.
func mutate(t *testing.T, dir string) string {
	t.Helper()
	if bash := findBash(); bash != "" {
		cmd := exec.Command(bash, "-c", `rm -rf docs && sed -i.bak 's/x/y/' main.go && rm main.go.bak && echo new > new.txt && mkdir -p sub && echo deep > sub/deep.txt && echo log > debug.log`)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bash mutation: %v\n%s", err, out)
		}
		return "bash"
	}
	if err := os.RemoveAll(filepath.Join(dir, "docs")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "main.go"), strings.Replace(original["main.go"], "x", "y", 1))
	write(t, filepath.Join(dir, "new.txt"), "new\n")
	write(t, filepath.Join(dir, "sub", "deep.txt"), "deep\n")
	write(t, filepath.Join(dir, "debug.log"), "log\n")
	return "go"
}

func TestRewindIsBashCompleteAndLeavesTheUserAlone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Control: with hooks enabled the marker hook fires, so its absence later means something.
	control := filepath.Join(f.base, "control")
	gitIn(t, f.repo, "worktree", "add", "-q", control, "-b", "control")
	if !exists(f.marker) {
		t.Fatal("control: the post-checkout hook did not fire; the hooks-disabled check would prove nothing")
	}
	if err := os.Remove(f.marker); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.repo, "worktree", "remove", "--force", control)
	gitIn(t, f.repo, "branch", "-D", "control")

	// The user's own work in the main checkout: a staged file and a stash.
	write(t, filepath.Join(f.repo, "staged.txt"), "staged\n")
	gitIn(t, f.repo, "add", "staged.txt")
	write(t, filepath.Join(f.repo, "docs", "a.md"), "user edit\n")
	gitIn(t, f.repo, "stash", "push", "-q", "-m", "userstash", "--", "docs/a.md")
	before := captureUserState(t, f.repo)

	wt, err := f.m.CreateWorktree(ctx, f.repo, "task_1", "")
	if err != nil {
		t.Fatal(err)
	}
	if exists(f.marker) {
		t.Fatal("creating a task worktree ran the repository's post-checkout hook")
	}
	if !within(filepath.Clean(f.m.Root), wt.Path) || wt.Branch != "amase/task_1" || read(t, filepath.Join(wt.Path, "main.go")) != original["main.go"] {
		t.Fatalf("unexpected worktree %+v", wt)
	}
	if list, err := f.m.List(ctx, f.repo); err != nil || len(list) != 1 || list[0].TaskID != "task_1" {
		t.Fatalf("list = %+v, %v", list, err)
	}

	pre, err := f.m.Snapshot(ctx, wt, TurnRef("s1", 1, "pre"))
	if err != nil {
		t.Fatal(err)
	}
	how := mutate(t, wt.Path)
	t.Logf("mutated the worktree with %s", how)
	post, err := f.m.Snapshot(ctx, wt, TurnRef("s1", 1, "post"))
	if err != nil {
		t.Fatal(err)
	}
	diff, err := f.m.Diff(ctx, wt, pre, post)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"main.go", "docs/a.md", "new.txt", "sub/deep.txt"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff does not mention %s:\n%s", want, diff)
		}
	}
	if strings.Contains(diff, "debug.log") {
		t.Fatal("an ignored file leaked into the snapshot")
	}

	res, err := f.m.Rewind(ctx, wt, pre)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range original {
		if got := read(t, filepath.Join(wt.Path, filepath.FromSlash(name))); got != content {
			t.Fatalf("%s after rewind = %q, want %q", name, got, content)
		}
	}
	for _, gone := range []string{"new.txt", filepath.Join("sub", "deep.txt"), "sub"} {
		if exists(filepath.Join(wt.Path, gone)) {
			t.Fatalf("%s survived the rewind", gone)
		}
	}
	if !exists(filepath.Join(wt.Path, "debug.log")) {
		t.Fatal("rewind deleted an ignored file; ignored files are outside snapshots and must be left alone")
	}
	if !reflect.DeepEqual(res.Removed, []string{"new.txt", "sub/deep.txt"}) {
		t.Fatalf("removed = %v", res.Removed)
	}
	if !reflect.DeepEqual(res.Restored, []string{"docs/a.md", "docs/b.md", "main.go"}) {
		t.Fatalf("restored = %v", res.Restored)
	}

	// A rewind is itself undoable.
	if _, err := f.m.Rewind(ctx, wt, res.Undo); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(wt.Path, "docs")) || !exists(filepath.Join(wt.Path, "new.txt")) || !strings.Contains(read(t, filepath.Join(wt.Path, "main.go")), "func y()") {
		t.Fatal("undoing the rewind did not bring back the agent's changes")
	}

	if after := captureUserState(t, f.repo); after != before {
		t.Fatalf("the user's checkout changed:\nbefore %+v\nafter  %+v", before, after)
	}

	if err := f.m.RemoveWorktree(ctx, wt, true); err != nil {
		t.Fatal(err)
	}
	if exists(wt.Path) {
		t.Fatal("worktree directory survived removal")
	}
	if list, _ := f.m.List(ctx, f.repo); len(list) != 0 {
		t.Fatalf("list after removal = %+v", list)
	}
	if exists(f.marker) {
		t.Fatal("a gitws operation ran a repository hook")
	}
}

func TestInvalidInputsAreRejected(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, id := range []string{"", "../escape", "a/b", "has space", strings.Repeat("x", 65)} {
		if _, err := f.m.CreateWorktree(ctx, f.repo, id, ""); err == nil {
			t.Errorf("task id %q accepted", id)
		}
	}
	wt, err := f.m.CreateWorktree(ctx, f.repo, "task_2", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Snapshot(ctx, wt, "refs/heads/sneaky"); err == nil {
		t.Fatal("snapshot outside refs/amase/ accepted")
	}
	if _, err := f.m.CreateWorktree(ctx, f.repo, "task_2", ""); err == nil {
		t.Fatal("a second worktree at the same path was accepted")
	}
	if _, err := (Git{}).Run(ctx, f.repo, nil, "status"); !errors.Is(err, ErrNoHooksDir) {
		t.Fatalf("git ran without a no-hooks directory: %v", err)
	}
}

func TestScrubGitEnv(t *testing.T) {
	got := scrubGitEnv([]string{"GIT_DIR=/elsewhere", "git_index_file=x", "GIT_SSH=ssh", "PATH=/bin"})
	if !reflect.DeepEqual(got, []string{"GIT_SSH=ssh", "PATH=/bin"}) {
		t.Fatalf("scrubbed env = %v", got)
	}
}
