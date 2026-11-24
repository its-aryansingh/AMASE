package demo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func noHooks(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "nohooks")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCreateIsDeterministic(t *testing.T) {
	ctx := context.Background()
	nh := noHooks(t)
	a, err := Create(ctx, filepath.Join(t.TempDir(), "one"), nh)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Create(ctx, filepath.Join(t.TempDir(), "two"), nh)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || len(a) != 40 {
		t.Fatalf("two demo repos have different commits: %s vs %s", a, b)
	}
}

func TestDemoHasExactlyOneFailingTest(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	dir := filepath.Join(t.TempDir(), "demo")
	if _, err := Create(context.Background(), dir, noHooks(t)); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"go.mod", "calc/calc.go", "calc/calc_test.go", "docs/notes.md", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
	if hooks, _ := os.ReadDir(filepath.Join(dir, ".git", "hooks")); len(hooks) != 0 {
		t.Fatalf("the demo repo got %d hook files; it must have none", len(hooks))
	}

	cmd := exec.Command("go", "test", "-v", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the demo's tests passed; it must start with one failing test:\n%s", out)
	}
	s := string(out)
	if strings.Count(s, "--- FAIL:") != 1 || !strings.Contains(s, "--- FAIL: TestSum ") || !strings.Contains(s, "--- PASS: TestSumEmpty") {
		t.Fatalf("expected exactly TestSum to fail:\n%s", s)
	}
}

func TestCreateRefusesANonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), dir, noHooks(t)); err == nil {
		t.Fatal("demo overwrote a non-empty directory")
	}
}
