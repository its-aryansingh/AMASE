package fake

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestParseTrace(t *testing.T) {
	big := strings.Repeat("x", 2<<20)
	trace := `{"dir": "in", "msg": {"type": "user"}, "ms": 3}` + "\r\n\n" +
		`{"dir":"out","msg":{"type":"assistant","text":"` + big + `"}}` // no trailing newline
	steps, err := ParseTrace(strings.NewReader(trace))
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[0].Dir != In || steps[1].Dir != Out {
		t.Fatalf("steps = %+v", steps)
	}
	if string(steps[0].Msg) != `{"type": "user"}` {
		t.Fatalf("msg not kept verbatim: %s", steps[0].Msg)
	}
	if len(steps[1].Msg) < 2<<20 {
		t.Fatalf("2 MiB frame truncated to %d bytes", len(steps[1].Msg))
	}

	for _, bad := range []string{`{"dir":"sideways","msg":{}}`, `not json`} {
		if _, err := ParseTrace(strings.NewReader(bad)); err == nil {
			t.Errorf("ParseTrace(%q) succeeded", bad)
		}
	}
}

// adapterEcho stands in for an adapter: it answers every line with a reply
// built from it.
func adapterEcho(t *testing.T, stdout io.Reader, stdin io.WriteCloser, reply func(string) string) {
	t.Helper()
	go func() {
		defer stdin.Close()
		r := bufio.NewReader(stdout)
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if _, err := io.WriteString(stdin, reply(strings.TrimSpace(l))+"\n"); err != nil {
				return
			}
		}
	}()
}

func ctx5s(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestReplayExactAndJSON(t *testing.T) {
	steps := []Step{
		{Dir: Out, Msg: []byte(`{"type": "ping", "id": "rec_1"}`)},
		{Dir: In, Msg: []byte(`{"type": "pong", "id": "rec_1"}`)},
	}
	cases := []struct {
		name  string
		reply string
		exact bool
		ok    bool
	}{
		{"exact match after compaction", `{"type":"pong","id":"ours_1"}`, true, true},
		{"exact rejects key order", `{"id":"ours_1","type":"pong"}`, true, false},
		{"exact rejects extra spaces", `{"type": "pong","id":"ours_1"}`, true, false},
		{"json accepts key order", `{"id":"ours_1","type":"pong"}`, false, true},
		{"json rejects other values", `{"type":"pong","id":"rec_1"}`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stdin, peer := Pipe()
			defer peer.Close()
			var sent string
			adapterEcho(t, stdout, stdin, func(l string) string { sent = l; return tc.reply })
			err := peer.Replay(ctx5s(t), steps, Options{Exact: tc.exact, Replace: strings.NewReplacer(`"rec_1"`, `"ours_1"`)})
			if sent != `{"type": "ping", "id": "ours_1"}` {
				t.Fatalf("Out step not sent verbatim after replacement: %s", sent)
			}
			var mm *MismatchError
			switch {
			case tc.ok && err != nil:
				t.Fatalf("Replay: %v", err)
			case !tc.ok && !errors.As(err, &mm):
				t.Fatalf("Replay = %v, want a MismatchError", err)
			case !tc.ok && mm.Step != 1:
				t.Fatalf("mismatch at step %d", mm.Step)
			}
		})
	}
}

func TestReplayBeforeAndMalformedOut(t *testing.T) {
	stdout, stdin, peer := Pipe()
	defer peer.Close()
	got := make(chan string, 4)
	go func() {
		r := bufio.NewReader(stdout)
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				close(got)
				return
			}
			got <- strings.TrimSpace(l)
		}
	}()
	steps := []Step{
		{Dir: Out, Msg: []byte(`this is not json`)},
		{Dir: In, Msg: []byte(`{"type":"user"}`)},
	}
	var calls []int
	err := peer.Replay(ctx5s(t), steps, Options{Exact: true, Before: func(i int, s Step) {
		calls = append(calls, i)
		if s.Dir == In {
			// The adapter acts at the recorded moment.
			go func() { _, _ = io.WriteString(stdin, `{"type":"user"}`+"\n") }()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != 0 || calls[1] != 1 {
		t.Fatalf("Before calls = %v", calls)
	}
	if l := <-got; l != "this is not json" {
		t.Fatalf("malformed Out step arrived as %q", l)
	}
}

func TestRecvEOFAndTimeout(t *testing.T) {
	_, stdin, peer := Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := peer.Recv(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Recv on a quiet adapter = %v", err)
	}
	go func() {
		_, _ = io.WriteString(stdin, "last\n")
		stdin.Close()
	}()
	if l, err := peer.Recv(ctx5s(t)); err != nil || string(l) != "last" {
		t.Fatalf("Recv = %q, %v", l, err)
	}
	if _, err := peer.Recv(ctx5s(t)); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv after the adapter closed = %v", err)
	}
}

// TestFixturesScrubbed applies the golden-fixture scrub rule (SPEC 8.5) to the
// traces stored here.
func TestFixturesScrubbed(t *testing.T) {
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`C:\\\\Users\\\\`),
		regexp.MustCompile(`(?i)/c/Users/`),
		regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`),
		regexp.MustCompile(`sk-[A-Za-z0-9]`),
		regexp.MustCompile(`ghp_`),
		regexp.MustCompile(`eyJ[A-Za-z0-9_-]{20,}`),
	}
	files, err := filepath.Glob(filepath.Join("testdata", "*", "*.ndjson"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, re := range forbidden {
			if loc := re.FindIndex(raw); loc != nil {
				t.Errorf("%s contains %q", f, raw[loc[0]:loc[1]])
			}
		}
		if _, err := LoadTrace(f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}
