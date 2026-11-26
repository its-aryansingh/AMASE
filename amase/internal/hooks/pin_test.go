package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/its-aryansingh/amase/internal/event"
	"github.com/its-aryansingh/amase/internal/policy"
)

func TestParseInvariantsValid(t *testing.T) {
	toml := "\xef\xbb\xbf# Project standing constraints\r\n" +
		"\r\n" +
		"[[rule]] # frozen path rule\r\n" +
		`id = "frozen-paths"` + "\r\n" +
		`text = "Never edit submission/ or \"code/\" directly."` + "\r\n\r\n" +
		"[[rule]]\n" +
		`id = 'stdlib-only'` + "\n" +
		`text = 'Keep go.mod stdlib + toolgate only # verified.'` + "\n"

	rules, err := ParseInvariants([]byte(toml))
	if err != nil {
		t.Fatalf("ParseInvariants: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2: %+v", len(rules), rules)
	}
	if rules[0].ID != "frozen-paths" || rules[0].Text != `Never edit submission/ or "code/" directly.` {
		t.Errorf("rule 0 = %+v", rules[0])
	}
	if rules[1].ID != "stdlib-only" || rules[1].Text != `Keep go.mod stdlib + toolgate only # verified.` {
		t.Errorf("rule 1 = %+v", rules[1])
	}

	empty, err := ParseInvariants([]byte("# only comments\n\n"))
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty invariants = %+v, %v", empty, err)
	}
}

func TestParseInvariantsErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"invalid utf8", "[[rule]]\nid = \"a\"\ntext = \"\xff\""},
		{"non-rule section", "[rule]\nid = \"a\"\ntext = \"b\"\n"},
		{"unknown table array", "[[rules]]\nid = \"a\"\ntext = \"b\"\n"},
		{"key before table", "id = \"a\"\n[[rule]]\nid = \"a\"\ntext = \"b\"\n"},
		{"unknown key", "[[rule]]\nid = \"a\"\nseverity = \"high\"\ntext = \"b\"\n"},
		{"duplicate key", "[[rule]]\nid = \"a\"\nid = \"b\"\ntext = \"c\"\n"},
		{"missing text", "[[rule]]\nid = \"a\"\n"},
		{"missing id", "[[rule]]\ntext = \"b\"\n"},
		{"empty text", "[[rule]]\nid = \"a\"\ntext = \"   \"\n"},
		{"duplicate rule id", "[[rule]]\nid = \"a\"\ntext = \"one\"\n[[rule]]\nid = \"a\"\ntext = \"two\"\n"},
		{"non-string number", "[[rule]]\nid = 42\ntext = \"b\"\n"},
		{"unclosed quote", "[[rule]]\nid = \"a\ntext = \"b\"\n"},
		{"trailing garbage", "[[rule]]\nid = \"a\" extra\ntext = \"b\"\n"},
		{"bad escape", "[[rule]]\nid = \"a\"\ntext = \"\\q\"\n"},
		{"multiline literal", "[[rule]]\nid = '''a'''\ntext = \"b\"\n"},
		{"newline in id", "[[rule]]\nid = \"a\\nb\"\ntext = \"ok\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rules, err := ParseInvariants([]byte(tc.src)); err == nil {
				t.Errorf("ParseInvariants(%q) succeeded: %+v", tc.name, rules)
			}
		})
	}
}

func TestBuildPinDeterminismAndCap(t *testing.T) {
	rules := []Rule{
		{ID: "r1", Text: "Never edit   frozen paths."},
		{ID: "r2", Text: "Run gofmt and go vet before commit."},
	}
	p1, err := BuildPin(rules)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := BuildPin(rules)
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Fatalf("BuildPin not deterministic: %+v vs %+v", p1, p2)
	}
	if len(p1.BundleSHA256) != 12 {
		t.Errorf("BundleSHA256 length = %d (%q), want 12", len(p1.BundleSHA256), p1.BundleSHA256)
	}
	wantPrefix := "AMASE-PIN v1 sha256:" + p1.BundleSHA256 + "\n"
	if !strings.HasPrefix(p1.Context, wantPrefix) {
		t.Errorf("Context = %q, want prefix %q", p1.Context, wantPrefix)
	}
	if p1.TokensEst == 0 || p1.TokensEst > MaxPinTokens {
		t.Errorf("TokensEst = %d, want 1..%d", p1.TokensEst, MaxPinTokens)
	}

	// Over 200 tokens (> 800 chars) must be rejected.
	tooLong := []Rule{{ID: "big", Text: strings.Repeat("word ", 200)}}
	if _, err := BuildPin(tooLong); err == nil {
		t.Error("BuildPin accepted a bundle over MaxPinTokens")
	}

	// Empty rules produce zero Pin cleanly.
	zero, err := BuildPin(nil)
	if err != nil || zero != (Pin{}) {
		t.Errorf("BuildPin(nil) = %+v, %v", zero, err)
	}
}

func TestLoadPinAndNewPinnerFailOpen(t *testing.T) {
	dir := t.TempDir()
	// Missing file returns zero Pin and no error; Pinner returns false.
	p, err := LoadPin(dir)
	if err != nil || p != (Pin{}) {
		t.Fatalf("missing invariants = %+v, %v", p, err)
	}
	pinner := NewPinner(nil)
	if got, ok := pinner(Binding{Workspace: policy.Workspace{Root: dir}}); ok || got != (Pin{}) {
		t.Fatalf("pinner on missing invariants = %+v, %v", got, ok)
	}

	// Write malformed TOML -> LoadPin errors, NewPinner fails open and invokes onError.
	amaseDir := filepath.Join(dir, ".amase")
	if err := os.MkdirAll(amaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	invPath := filepath.Join(amaseDir, "invariants.toml")
	if err := os.WriteFile(invPath, []byte("[not_rule]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var seenErr error
	pinnerLogged := NewPinner(func(_ Binding, err error) { seenErr = err })
	if got, ok := pinnerLogged(Binding{Session: "s1", Workspace: policy.Workspace{Root: dir}}); ok || got != (Pin{}) {
		t.Fatalf("pinner on bad TOML = %+v, %v", got, ok)
	}
	if seenErr == nil {
		t.Error("onError was not called for malformed invariants.toml")
	}
}

func TestPinGoldenSessionStartInjection(t *testing.T) {
	dir := t.TempDir()
	amaseDir := filepath.Join(dir, ".amase")
	if err := os.MkdirAll(amaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `[[rule]]
id = "no-frozen"
text = "Never edit submission/ or code/."

[[rule]]
id = "no-trailers"
text = "Never add AI trailers to git commits."
`
	if err := os.WriteFile(filepath.Join(amaseDir, "invariants.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := &memRecorder{}
	srv, err := New(Options{Engine: policy.NewEngine(), Recorder: rec, Pinner: NewPinner(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Register(testToken, Binding{
		Repo:      "repo1",
		Task:      "task1",
		Session:   "ses1",
		Workspace: policy.Workspace{Root: dir, Home: t.TempDir()},
		Turn:      func() int { return 2 },
	}); err != nil {
		t.Fatal(err)
	}

	base := loadGolden(t)[0].Payload
	compactReq := withFields(t, base, map[string]any{
		"hook_event_name": "SessionStart",
		"source":          "compact",
		"tool_name":       nil,
		"tool_input":      nil,
		"tool_use_id":     nil,
	})
	resp := postTo(srv.Handler(), "/hooks/claude/"+testToken, compactReq)
	if resp.Code != 200 {
		t.Fatalf("status %d: %s", resp.Code, resp.Body.String())
	}

	var out output
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName = %q", out.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(out.HookSpecificOutput.AdditionalContext, "AMASE-PIN v1 sha256:") ||
		!strings.Contains(out.HookSpecificOutput.AdditionalContext, "[no-frozen] Never edit submission/ or code/.") {
		t.Errorf("unexpected additionalContext: %q", out.HookSpecificOutput.AdditionalContext)
	}

	pins := rec.ofType(event.PinInjected)
	if len(pins) != 1 {
		t.Fatalf("got %d PinInjected events, want 1", len(pins))
	}
	var pd event.PinInjectedData
	decodeData(t, pins[0], &pd)
	if len(pd.BundleSHA256) != 12 || pd.TokensEst <= 0 || pd.TokensEst > MaxPinTokens {
		t.Errorf("PinInjectedData = %+v", pd)
	}
}
