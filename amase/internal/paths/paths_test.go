package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestStateDir(t *testing.T) {
	home := filepath.Join("h", "me")
	cases := []struct {
		name string
		goos string
		env  map[string]string
		want string
	}{
		{"override wins everywhere", "linux", map[string]string{"AMASE_HOME": filepath.Join("x", "amase")}, filepath.Join("x", "amase")},
		{"windows LOCALAPPDATA", "windows", map[string]string{"LOCALAPPDATA": filepath.Join("C", "L")}, filepath.Join("C", "L", "amase")},
		{"windows fallback", "windows", nil, filepath.Join(home, "AppData", "Local", "amase")},
		{"macos", "darwin", nil, filepath.Join(home, "Library", "Application Support", "amase")},
		{"linux XDG", "linux", map[string]string{"XDG_STATE_HOME": filepath.Join("s")}, filepath.Join("s", "amase")},
		{"linux fallback", "linux", nil, filepath.Join(home, ".local", "state", "amase")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stateDir(c.goos, env(c.env), home); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestLayoutEnsure(t *testing.T) {
	l := Layout{Root: filepath.Join(t.TempDir(), "state")}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{l.Root, l.Worktrees(), l.NoHooks()} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Fatalf("%s was not created: %v", d, err)
		}
	}
	if filepath.Dir(l.Events()) != l.Root || filepath.Dir(l.Receipts()) != l.Root {
		t.Fatal("journals must live directly under the state root")
	}
}

func TestRepoID(t *testing.T) {
	a := repoID("windows", `C:\Work\Repo`)
	b := repoID("windows", `c:\work\repo\`)
	if a != b {
		t.Fatalf("windows ids differ by case/trailing slash: %s vs %s", a, b)
	}
	if len(a) != 12 {
		t.Fatalf("id length %d", len(a))
	}
	if repoID("linux", "/work/Repo") == repoID("linux", "/work/repo") {
		t.Fatal("linux paths are case-sensitive; ids must differ")
	}
}
