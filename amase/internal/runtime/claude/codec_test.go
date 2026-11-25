package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestDecoderFraming(t *testing.T) {
	big := strings.Repeat("x", 2<<20)
	stream := "\n" +
		`{"type":"system","subtype":"init","session_id":"s1"}` + "\r\n" +
		"not json\n" +
		`[1,2,3]` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"` + big + `"}]}}` + "\n" +
		`{"type":"amase_unknown_frame","x":1}` + "\n" +
		`{"type":"result","subtype":"success"}` // no trailing newline

	d := NewDecoder(strings.NewReader(stream))
	var types []string
	var malformed int
	for {
		f, err := d.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		var se *SyntaxError
		if errors.As(err, &se) {
			malformed++
			if len(se.Line) > 256 {
				t.Fatalf("SyntaxError.Line not truncated: %d bytes", len(se.Line))
			}
			continue
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		types = append(types, f.Type)
		if f.Type == "assistant" && len(f.Raw) < 2<<20 {
			t.Fatalf("2 MiB line truncated to %d bytes", len(f.Raw))
		}
		if f.Type == "system" && (f.Subtype != "init" || f.SessionID != "s1" || bytes.HasSuffix(f.Raw, []byte("\r"))) {
			t.Fatalf("system frame decoded wrong: %+v raw=%q", f, f.Raw)
		}
	}
	want := []string{"system", "assistant", "amase_unknown_frame", "result"}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("frame types = %v, want %v", types, want)
	}
	if malformed != 2 {
		t.Fatalf("malformed = %d, want 2", malformed)
	}
}

func TestEncoderShapesMatchGolden(t *testing.T) {
	golden := goldenLines(t, "duplex-allow-then-hook-deny.ndjson")
	cases := []struct {
		name string
		msg  Outbound
		want json.RawMessage
	}{
		{"initialize", NewInitialize("init_1"), golden[0].Msg},
		{"user turn", NewUserMessage("96ea6bd6-e5d2-40b0-a722-1233bf898020",
			"Use the Bash tool (not PowerShell) to run exactly this command: echo amase-probe > probe.txt\nThen use the Bash tool to run exactly: rm probe.txt\nThen reply with the single word DONE. Do not run any other command."),
			golden[1].Msg},
		{"allow", NewPermissionResponse("89febd45-8131-4031-966c-7013d463984a", PermissionResult{Behavior: "allow",
			UpdatedInput: json.RawMessage(`{"command": "echo amase-probe > probe.txt", "description": "Write probe text to probe.txt"}`)}),
			golden[12].Msg},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := NewEncoder(&buf).Encode(tc.msg); err != nil {
				t.Fatal(err)
			}
			line := buf.Bytes()
			if bytes.Count(line, []byte("\n")) != 1 || line[len(line)-1] != '\n' {
				t.Fatalf("not exactly one NDJSON line: %q", line)
			}
			assertJSONEqual(t, line, tc.want)
			if bytes.Contains(line, []byte(`\u003e`)) {
				t.Fatalf("HTML-escaped output: %s", line)
			}
		})
	}
}

func TestErrorResponseMatchesClaudeShape(t *testing.T) {
	var buf bytes.Buffer
	if err := NewEncoder(&buf).Encode(NewErrorResponse("unk_1", "unsupported_request", "Unsupported control request subtype: x")); err != nil {
		t.Fatal(err)
	}
	// The shape Claude Code 2.1.331 itself replied with to an unknown subtype.
	assertJSONEqual(t, buf.Bytes(), json.RawMessage(`{"type":"control_response","response":{"subtype":"error","request_id":"unk_1","error":"Unsupported control request subtype: x","error_code":"unsupported_request"}}`))
}

func TestEncoderLinesNeverInterleave(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	payload := strings.Repeat("y", 100<<10)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = enc.Encode(NewUserMessage("s", payload))
		}()
	}
	wg.Wait()
	r := bufio.NewReader(&buf)
	n := 0
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if !json.Valid(line) {
				t.Fatalf("line %d is not valid JSON (interleaved write)", n)
			}
			n++
		}
		if err != nil {
			break
		}
	}
	if n != 16 {
		t.Fatalf("lines = %d, want 16", n)
	}
}

type goldenLine struct {
	Dir string          `json:"dir"`
	Msg json.RawMessage `json:"msg"`
}

func goldenLines(t *testing.T, name string) []goldenLine {
	t.Helper()
	raw, err := readGolden(name)
	if err != nil {
		t.Fatal(err)
	}
	var out []goldenLine
	for _, l := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var g goldenLine
		if err := json.Unmarshal(l, &g); err != nil {
			t.Fatal(err)
		}
		out = append(out, g)
	}
	return out
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got is not JSON: %v: %s", err, got)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("want is not JSON: %v: %s", err, want)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("JSON differs\n got: %s\nwant: %s", got, want)
	}
}
