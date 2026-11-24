package ledger

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestAppendVerifyAndTamper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.ndjson")
	j, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := j.Append(Kind("event"), "sess", "amase", map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	if seq, _ := j.Tip(); seq != 3 {
		t.Fatalf("tip seq %d, want 3", seq)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Verify(bytes.NewReader(raw))
	if err != nil || !res.OK || res.Records != 3 {
		t.Fatalf("clean chain did not verify: %+v %v", res, err)
	}

	tampered := bytes.Replace(raw, []byte(`"n":1`), []byte(`"n":7`), 1)
	res, err = Verify(bytes.NewReader(tampered))
	if err != nil || res.OK || res.BreakAt != 2 {
		t.Fatalf("an edited record was not caught at seq 2: %+v %v", res, err)
	}
}
