package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSome(t *testing.T, path string, n int) {
	t.Helper()
	j, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for i := 0; i < n; i++ {
		if _, err := j.Append(KindAudit, "s1", "goose", map[string]any{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
}

func verifyFile(t *testing.T, path string) VerifyResult {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := Verify(f)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestChainVerifies(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.ndjson")
	writeSome(t, p, 10)
	if res := verifyFile(t, p); !res.OK || res.Records != 10 {
		t.Fatalf("expected an intact 10-record chain, got %+v", res)
	}
}

// TestReopenContinuesChain is the case a naive implementation gets wrong: the
// journal must recover its sequence number and tip hash from the file, or a
// restart silently forks the chain.
func TestReopenContinuesChain(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.ndjson")
	writeSome(t, p, 3)
	writeSome(t, p, 3)
	res := verifyFile(t, p)
	if !res.OK || res.Records != 6 {
		t.Fatalf("chain did not survive reopen: %+v", res)
	}
}

func TestEditIsDetected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.ndjson")
	writeSome(t, p, 5)

	lines := strings.Split(strings.TrimSpace(readFile(t, p)), "\n")
	var rec Record
	if err := json.Unmarshal([]byte(lines[2]), &rec); err != nil {
		t.Fatal(err)
	}
	rec.Body = json.RawMessage(`{"i":999}`)
	edited, _ := json.Marshal(rec)
	lines[2] = string(edited)
	os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)

	res := verifyFile(t, p)
	if res.OK {
		t.Fatal("an edited record verified as intact")
	}
	if res.BreakAt != 3 {
		t.Errorf("expected the break at record 3, got %d", res.BreakAt)
	}
}

func TestDeletionIsDetected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.ndjson")
	writeSome(t, p, 5)

	lines := strings.Split(strings.TrimSpace(readFile(t, p)), "\n")
	lines = append(lines[:2], lines[3:]...) // drop record 3
	os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)

	if res := verifyFile(t, p); res.OK {
		t.Fatal("a chain with a missing record verified as intact")
	}
}

func TestEmptyJournalVerifies(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.ndjson")
	if _, err := Open(p, false); err != nil {
		t.Fatal(err)
	}
	if res := verifyFile(t, p); !res.OK || res.Records != 0 {
		t.Fatalf("empty journal should verify, got %+v", res)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
