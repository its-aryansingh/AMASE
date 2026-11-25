package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// goldenSettings is the --settings file Claude Code 2.1.331 loaded in WP-1.0
// (protocol facts C7, C8), with the port filled in.
func goldenSettings(t *testing.T, port string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "claude", "hook-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n") // a CRLF checkout must not change what the edits below match
	return []byte(strings.ReplaceAll(s, "<port>", port))
}

func TestSettingsMatchesVerifiedShape(t *testing.T) {
	got, err := Settings(testToken, 7337)
	if err != nil {
		t.Fatal(err)
	}
	var gen, want map[string]any
	if err := json.Unmarshal(got, &gen); err != nil {
		t.Fatal(err)
	}
	golden := strings.ReplaceAll(string(goldenSettings(t, "7337")), "probe-token", testToken)
	if err := json.Unmarshal([]byte(golden), &want); err != nil {
		t.Fatal(err)
	}
	// One deliberate difference: SessionStart only matters after compaction (C10).
	start := want["hooks"].(map[string]any)["SessionStart"].([]any)[0].(map[string]any)
	start["matcher"] = "compact"
	if !reflect.DeepEqual(gen, want) {
		t.Errorf("generated settings differ from the verified golden shape:\n%s", got)
	}
	if !strings.HasSuffix(string(got), "\n") {
		t.Error("settings file does not end with a newline")
	}
}

func TestSettingsFailClosedOnlyForPreToolUse(t *testing.T) {
	got, err := Settings(testToken, 7337)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Hooks map[string][]hookGroup `json:"hooks"`
	}
	if err := json.Unmarshal(got, &f); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PreToolUse", "PostToolUse", "SessionStart", "Stop"} {
		groups := f.Hooks[name]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s: %+v, want one hook", name, groups)
		}
		h := groups[0].Hooks[0]
		if h.Type != "http" || h.URL != "http://127.0.0.1:7337/hooks/claude/"+testToken || h.Timeout != DefaultTimeout {
			t.Errorf("%s hook = %+v", name, h)
		}
		wantFailure := ""
		if name == "PreToolUse" {
			wantFailure = "block"
		}
		if h.OnFailure != wantFailure {
			t.Errorf("%s onFailure = %q, want %q", name, h.OnFailure, wantFailure)
		}
	}
}

func TestGenerateRejectsLongTimeouts(t *testing.T) {
	for _, c := range []Config{
		{Token: testToken, Port: 7337, PreToolUseTimeout: 3600000},
		{Token: testToken, Port: 7337, Timeout: 3600000},
		{Token: testToken, Port: 7337, PreToolUseTimeout: MaxTimeout + 1},
		{Token: testToken, Port: 7337, PreToolUseTimeout: -1},
	} {
		if b, err := Generate(c); err == nil {
			t.Errorf("Generate(%+v) accepted it:\n%s", c, b)
		}
	}
	if _, err := Generate(Config{Token: testToken, Port: 7337, PreToolUseTimeout: MaxTimeout, Timeout: 1}); err != nil {
		t.Errorf("Generate rejected the boundary timeouts: %v", err)
	}
}

func TestGenerateValidatesTokenAndPort(t *testing.T) {
	for _, c := range []Config{
		{Token: "short", Port: 7337},
		{Token: "bad/token/0123456789", Port: 7337},
		{Token: testToken, Port: 0},
		{Token: testToken, Port: 65536},
	} {
		if _, err := Generate(c); err == nil {
			t.Errorf("Generate(%+v) accepted it", c)
		}
	}
}

func TestCheckSettings(t *testing.T) {
	golden := string(goldenSettings(t, "7337"))
	if err := CheckSettings([]byte(golden)); err != nil {
		t.Fatalf("the golden settings Claude loaded fail the check: %v", err)
	}
	firstTimeout := `"timeout": 5`
	bad := map[string]string{
		"PreToolUse timeout in milliseconds": strings.Replace(golden, firstTimeout, `"timeout": 3600000`, 1),
		"Stop timeout in milliseconds":       golden[:strings.LastIndex(golden, firstTimeout)] + `"timeout": 3600000` + golden[strings.LastIndex(golden, firstTimeout)+len(firstTimeout):],
		"PreToolUse fails open":              strings.Replace(golden, `"onFailure": "block"`, `"onFailure": "allow"`, 1),
		"no PreToolUse hook":                 `{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://127.0.0.1:1/hooks/claude/x","timeout":5}]}]}}`,
		"missing timeout":                    `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"http","url":"u","onFailure":"block"}]}]}}`,
		"malformed":                          `{"hooks":`,
	}
	for name, raw := range bad {
		if raw == golden {
			t.Fatalf("%s: the edit did not apply", name)
		}
		if err := CheckSettings([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNewToken(t *testing.T) {
	a, b := NewToken(), NewToken()
	if a == b {
		t.Error("two tokens are equal")
	}
	if err := checkToken(a); err != nil {
		t.Errorf("NewToken produced a token Register would refuse: %v", err)
	}
	if _, err := Settings(a, 7337); err != nil {
		t.Errorf("Settings refused a NewToken token: %v", err)
	}
}
