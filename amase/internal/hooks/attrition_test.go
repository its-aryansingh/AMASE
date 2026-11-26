package hooks

import (
	"encoding/json"
	"testing"

	"github.com/its-aryansingh/amase/internal/event"
	"github.com/its-aryansingh/amase/internal/policy"
)

func TestDetectAttritionCases(t *testing.T) {
	cases := []struct {
		name     string
		out      ToolOutput
		wantKind string
		wantHit  bool
	}{
		{
			name:     "clean shell output",
			out:      ToolOutput{Shell: true, Stdout: "ok\tgithub.com/its-aryansingh/amase/internal/hooks\t0.12s\n"},
			wantKind: "",
			wantHit:  false,
		},
		{
			name:     "clean json object",
			out:      ToolOutput{Shell: true, Stdout: "  {\"status\": \"ok\", \"items\": [1, 2]}\n"},
			wantKind: "",
			wantHit:  false,
		},
		{
			name:     "clean json array",
			out:      ToolOutput{Shell: true, Stdout: "[1, 2, 3]"},
			wantKind: "",
			wantHit:  false,
		},
		{
			name:     "clean bracket log prefix",
			out:      ToolOutput{Shell: true, Stdout: "[PASS] 12/12 checks passed\n"},
			wantKind: "",
			wantHit:  false,
		},
		{
			name:     "clean complete python traceback",
			out:      ToolOutput{Shell: true, Stderr: "Traceback (most recent call last):\n  File \"main.py\", line 4, in <module>\n    1 / 0\nZeroDivisionError: division by zero\n"},
			wantKind: "",
			wantHit:  false,
		},
		{
			name:     "clean complete python KeyboardInterrupt",
			out:      ToolOutput{Shell: true, Stderr: "Traceback (most recent call last):\n  File \"main.py\", line 1\nKeyboardInterrupt\n"},
			wantKind: "",
			wantHit:  false,
		},
		{
			name:     "clean Read response with broken json inside source file",
			out:      ToolOutput{Tool: "Read", Raw: json.RawMessage(`{"type":"text","file":{"filePath":"bad_fixture.json","content":"{\"broken\": "}}`)},
			wantKind: "",
			wantHit:  false,
		},
		{
			name:     "truncated json object in stdout",
			out:      ToolOutput{Shell: true, Stdout: `{"items": [1, 2, `},
			wantKind: AttritionTruncatedJSON,
			wantHit:  true,
		},
		{
			name:     "truncated json array in stdout",
			out:      ToolOutput{Shell: true, Stdout: `[{"name": "foo"}, {"name": "bar`},
			wantKind: AttritionTruncatedJSON,
			wantHit:  true,
		},
		{
			name:     "truncated json bracket without tag",
			out:      ToolOutput{Shell: true, Stdout: `[1, 2,`},
			wantKind: AttritionTruncatedJSON,
			wantHit:  true,
		},
		{
			name:     "truncated json in raw string response",
			out:      ToolOutput{Tool: "Mcp", Raw: json.RawMessage(`"{\"result\": [1, 2"`)},
			wantKind: AttritionTruncatedJSON,
			wantHit:  true,
		},
		{
			name:     "incomplete python traceback header only",
			out:      ToolOutput{Shell: true, Stderr: "Traceback (most recent call last):\n"},
			wantKind: AttritionTruncatedTraceback,
			wantHit:  true,
		},
		{
			name:     "incomplete python traceback stopped in frame",
			out:      ToolOutput{Shell: true, Stderr: "Traceback (most recent call last):\n  File \"app.py\", line 10, in run\n    helper()"},
			wantKind: AttritionTruncatedTraceback,
			wantHit:  true,
		},
		{
			name:     "incomplete second chained python traceback",
			out:      ToolOutput{Shell: true, Stderr: "Traceback (most recent call last):\n  File \"a.py\", line 1\nValueError: first\n\nTraceback (most recent call last):\n  File \"b.py\", line 2\n"},
			wantKind: AttritionTruncatedTraceback,
			wantHit:  true,
		},
		{
			name:     "bash persistedOutputPath metadata",
			out:      ToolOutput{Shell: true, Stdout: "first lines...", Raw: json.RawMessage(`{"stdout":"first lines...","stderr":"","interrupted":false,"persistedOutputPath":"/tmp/out.txt","persistedOutputSize":200000}`)},
			wantKind: AttritionHarnessTruncation,
			wantHit:  true,
		},
		{
			name:     "Read truncatedByTokenCap metadata",
			out:      ToolOutput{Tool: "Read", Raw: json.RawMessage(`{"type":"text","file":{"filePath":"big.go","content":"package main","truncatedByTokenCap":true}}`)},
			wantKind: AttritionHarnessTruncation,
			wantHit:  true,
		},
		{
			name:     "Glob truncated true metadata",
			out:      ToolOutput{Tool: "Glob", Raw: json.RawMessage(`{"numFiles":100,"filenames":["a.go"],"truncated":true}`)},
			wantKind: AttritionHarnessTruncation,
			wantHit:  true,
		},
		{
			name:     "harness output truncated banner",
			out:      ToolOutput{Shell: true, Stdout: "line1\nline2\n... [output truncated, 120 lines omitted]"},
			wantKind: AttritionHarnessTruncation,
			wantHit:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, found := DetectAttrition(tc.out)
			if found != tc.wantHit || kind != tc.wantKind {
				t.Errorf("DetectAttrition() = (%q, %v), want (%q, %v)", kind, found, tc.wantKind, tc.wantHit)
			}
		})
	}
}

func TestPostToolUseWithDetectAttrition(t *testing.T) {
	rec := &memRecorder{}
	srv, err := New(Options{Engine: policy.NewEngine(), Recorder: rec, Attrition: DetectAttrition})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Register(testToken, Binding{
		Repo:      "repo1",
		Task:      "task1",
		Session:   "ses1",
		Workspace: policy.Workspace{Root: t.TempDir(), Home: t.TempDir()},
		Turn:      func() int { return 3 },
	}); err != nil {
		t.Fatal(err)
	}

	g := loadGolden(t)[1]
	// Clean golden PostToolUse should emit no AttritionDetected.
	if w := postTo(srv.Handler(), "/hooks/claude/"+testToken, g.Payload); w.Code != 200 {
		t.Fatalf("clean status %d", w.Code)
	}
	if len(rec.ofType(event.AttritionDetected)) != 0 {
		t.Fatalf("golden clean output flagged: %+v", rec.ofType(event.AttritionDetected))
	}

	// PostToolUse with incomplete JSON should emit AttritionDetected.
	badBody := withFields(t, g.Payload, map[string]any{
		"tool_response": map[string]any{"stdout": `{"items":[1,2`, "stderr": "", "interrupted": false},
	})
	if w := postTo(srv.Handler(), "/hooks/claude/"+testToken, badBody); w.Code != 200 {
		t.Fatalf("attrition status %d", w.Code)
	}
	att := rec.ofType(event.AttritionDetected)
	if len(att) != 1 {
		t.Fatalf("got %d AttritionDetected, want 1", len(att))
	}
	var data event.AttritionDetectedData
	decodeData(t, att[0], &data)
	if data.Kind != AttritionTruncatedJSON {
		t.Errorf("Kind = %q, want %q", data.Kind, AttritionTruncatedJSON)
	}
}
