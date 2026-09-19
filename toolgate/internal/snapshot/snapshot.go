// Package snapshot records the state of a workspace as a single content address.
//
// This is the part of the design borrowed most directly from Velra's thesis:
// a deterministic reduction of local state into a bounded artefact that can be
// compared across time. Two properties are load-bearing.
//
// Determinism: the same tree must produce the same digest on every machine and
// in every process. That rules out map iteration, filesystem readdir order,
// timestamps, inode numbers and absolute paths. What remains is a sorted list of
// relative paths, modes reduced to a single executable bit, sizes, and content
// digests.
//
// Content addressing: because the tree digest is a pure function of the tree, it
// can be handed to the model as a state handle under MCP's stateless rules and
// verified on the way back without the server having stored anything. Under the
// old session-based protocol this would have been a row id in a table; under
// 2026-07-28 it has to survive a round trip through an untrusted intermediary,
// and a content address is the right shape for that.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Entry is one file in a snapshot.
type Entry struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Executable bool   `json:"exec"`
	Digest     string `json:"digest"`
}

// Snapshot is a deterministic description of a directory tree.
type Snapshot struct {
	Root    string  `json:"root"`
	Tree    string  `json:"tree"`
	Entries []Entry `json:"entries"`
	// Skipped counts paths left out by the limits below. A snapshot that
	// silently omitted half the tree would make the diff lie, so the count
	// travels with the result.
	Skipped   int   `json:"skipped"`
	TotalSize int64 `json:"total_size"`
}

// Limits bound the work a snapshot may do.
type Limits struct {
	MaxFiles    int
	MaxFileSize int64
	Ignore      []string
}

// DefaultLimits are tuned for a source repository, not a data directory.
func DefaultLimits() Limits {
	return Limits{
		MaxFiles:    50_000,
		MaxFileSize: 8 << 20,
		Ignore: []string{
			".git", "node_modules", "target", "dist", "build", ".venv", "venv",
			"__pycache__", ".mypy_cache", ".pytest_cache", ".next", ".cache",
			"vendor", ".terraform", ".gradle", ".idea", ".DS_Store",
		},
	}
}

// Capture walks root and computes its tree digest.
func Capture(root string, lim Limits) (*Snapshot, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving %q: %w", root, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", abs)
	}

	ignore := make(map[string]bool, len(lim.Ignore))
	for _, name := range lim.Ignore {
		ignore[name] = true
	}

	snap := &Snapshot{Root: abs}

	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory that cannot be read is counted, not fatal. Refusing
			// to snapshot a workspace because one path is unreadable would make
			// the feature unusable on any machine with a stray permission.
			snap.Skipped++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == abs {
			return nil
		}
		name := d.Name()
		if ignore[name] {
			if d.IsDir() {
				return fs.SkipDir
			}
			snap.Skipped++
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// Symlinks are recorded by their target text, never followed: following
		// them would let a link out of the workspace pull unrelated files into
		// the digest, and a link cycle would not terminate.
		if d.Type()&fs.ModeSymlink != 0 {
			target, lerr := os.Readlink(p)
			if lerr != nil {
				snap.Skipped++
				return nil
			}
			rel, _ := filepath.Rel(abs, p)
			sum := sha256.Sum256([]byte("symlink:" + target))
			snap.Entries = append(snap.Entries, Entry{
				Path:   filepath.ToSlash(rel),
				Digest: hex.EncodeToString(sum[:]),
			})
			return nil
		}
		if !d.Type().IsRegular() {
			snap.Skipped++
			return nil
		}

		fi, ferr := d.Info()
		if ferr != nil {
			snap.Skipped++
			return nil
		}
		if lim.MaxFileSize > 0 && fi.Size() > lim.MaxFileSize {
			snap.Skipped++
			return nil
		}
		if lim.MaxFiles > 0 && len(snap.Entries) >= lim.MaxFiles {
			snap.Skipped++
			return nil
		}

		digest, derr := fileDigest(p)
		if derr != nil {
			snap.Skipped++
			return nil
		}
		rel, _ := filepath.Rel(abs, p)
		snap.Entries = append(snap.Entries, Entry{
			Path:       filepath.ToSlash(rel),
			Size:       fi.Size(),
			Executable: fi.Mode().Perm()&0o111 != 0,
			Digest:     digest,
		})
		snap.TotalSize += fi.Size()
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipDir) {
		return nil, fmt.Errorf("walking %q: %w", abs, err)
	}

	sort.Slice(snap.Entries, func(i, j int) bool { return snap.Entries[i].Path < snap.Entries[j].Path })
	snap.Tree = treeDigest(snap.Entries)
	return snap, nil
}

func fileDigest(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// treeDigest folds sorted entries into one hash.
//
// Fields are separated by a NUL rather than concatenated, so that two different
// trees cannot produce the same input string. Without a separator, a file named
// "ab" with digest "c" and one named "a" with digest "bc" would hash identically
// -- the classic length-extension-by-ambiguity mistake in ad-hoc hashing.
func treeDigest(entries []Entry) string {
	h := sha256.New()
	var b strings.Builder
	for _, e := range entries {
		b.Reset()
		b.WriteString(e.Path)
		b.WriteByte(0)
		fmt.Fprintf(&b, "%d", e.Size)
		b.WriteByte(0)
		if e.Executable {
			b.WriteByte('x')
		} else {
			b.WriteByte('-')
		}
		b.WriteByte(0)
		b.WriteString(e.Digest)
		b.WriteByte('\n')
		h.Write([]byte(b.String()))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Change is one difference between two snapshots.
type Change struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // added | removed | modified
}

// Diff compares two snapshots. Both entry lists are sorted, so this is a linear
// merge rather than a map lookup -- which keeps the output order deterministic
// without a second sort.
func Diff(before, after *Snapshot) []Change {
	var out []Change
	i, j := 0, 0
	for i < len(before.Entries) && j < len(after.Entries) {
		a, b := before.Entries[i], after.Entries[j]
		switch {
		case a.Path == b.Path:
			if a.Digest != b.Digest || a.Executable != b.Executable {
				out = append(out, Change{Path: a.Path, Kind: "modified"})
			}
			i++
			j++
		case a.Path < b.Path:
			out = append(out, Change{Path: a.Path, Kind: "removed"})
			i++
		default:
			out = append(out, Change{Path: b.Path, Kind: "added"})
			j++
		}
	}
	for ; i < len(before.Entries); i++ {
		out = append(out, Change{Path: before.Entries[i].Path, Kind: "removed"})
	}
	for ; j < len(after.Entries); j++ {
		out = append(out, Change{Path: after.Entries[j].Path, Kind: "added"})
	}
	if out == nil {
		out = []Change{}
	}
	return out
}
