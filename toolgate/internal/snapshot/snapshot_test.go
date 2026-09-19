package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func build(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestTreeDigestIsStable is the whole contract: the same content must produce
// the same address, in any order, on any run.
func TestTreeDigestIsStable(t *testing.T) {
	files := map[string]string{"a.txt": "one", "dir/b.txt": "two", "dir/c.txt": "three"}
	r1 := build(t, files)
	r2 := build(t, files)

	s1, err := Capture(r1, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Capture(r2, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if s1.Tree != s2.Tree {
		t.Fatalf("identical trees in different directories hashed differently:\n  %s\n  %s", s1.Tree, s2.Tree)
	}
	for i := 0; i < 5; i++ {
		again, _ := Capture(r1, DefaultLimits())
		if again.Tree != s1.Tree {
			t.Fatal("repeated capture of one tree changed its digest")
		}
	}
}

func TestContentChangesTheDigest(t *testing.T) {
	root := build(t, map[string]string{"a.txt": "one"})
	before, _ := Capture(root, DefaultLimits())
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("two"), 0o644)
	after, _ := Capture(root, DefaultLimits())
	if before.Tree == after.Tree {
		t.Fatal("editing a file did not change the tree digest")
	}
}

// TestPathAndDigestCannotBeConfused guards the separator: without the NUL
// between fields, a rename that shifts a character between path and digest
// would hash identically.
func TestPathAndDigestCannotBeConfused(t *testing.T) {
	a := treeDigest([]Entry{{Path: "ab", Digest: "c"}})
	b := treeDigest([]Entry{{Path: "a", Digest: "bc"}})
	if a == b {
		t.Fatal("two different trees produced the same digest")
	}
}

func TestDiff(t *testing.T) {
	root := build(t, map[string]string{"keep.txt": "k", "edit.txt": "before", "gone.txt": "g"})
	before, _ := Capture(root, DefaultLimits())

	os.WriteFile(filepath.Join(root, "edit.txt"), []byte("after"), 0o644)
	os.Remove(filepath.Join(root, "gone.txt"))
	os.WriteFile(filepath.Join(root, "new.txt"), []byte("n"), 0o644)
	after, _ := Capture(root, DefaultLimits())

	got := map[string]string{}
	for _, c := range Diff(before, after) {
		got[c.Path] = c.Kind
	}
	want := map[string]string{"edit.txt": "modified", "gone.txt": "removed", "new.txt": "added"}
	for p, k := range want {
		if got[p] != k {
			t.Errorf("%s: expected %s, got %q", p, k, got[p])
		}
	}
	if _, ok := got["keep.txt"]; ok {
		t.Error("an unchanged file appeared in the diff")
	}
}

func TestIgnoredDirectoriesAreSkipped(t *testing.T) {
	root := build(t, map[string]string{"a.txt": "one", "node_modules/x/y.js": "junk", ".git/HEAD": "ref"})
	s, err := Capture(root, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 1 || s.Entries[0].Path != "a.txt" {
		t.Fatalf("expected only a.txt, got %+v", s.Entries)
	}
}
