package hooks

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Attrition kind values recorded on AttritionDetected events.
const (
	AttritionHarnessTruncation  = "harness_truncation"
	AttritionTruncatedJSON      = "truncated_json"
	AttritionTruncatedTraceback = "truncated_traceback"
)

// DetectAttrition inspects one PostToolUse ToolOutput for signs that the
// harness or tool damaged the data the model will reason over:
//  1. Harness truncation flags or banners (from Claude Code's schema/markers).
//  2. Output beginning with '{' or '[' that fails json.Valid (truncated JSON).
//  3. Python tracebacks that lack a terminating exception line.
//
// It is pure, allocation-light, and safe on the PostToolUse path.
func DetectAttrition(out ToolOutput) (string, bool) {
	if hasStructuredTruncation(out.Raw) {
		return AttritionHarnessTruncation, true
	}

	if out.Shell {
		for _, stream := range []string{out.Stdout, out.Stderr} {
			if kind, ok := inspectOutputText(stream); ok {
				return kind, true
			}
		}
		return "", false
	}

	if len(bytes.TrimSpace(out.Raw)) == 0 {
		return "", false
	}
	trimmedRaw := strings.TrimSpace(string(out.Raw))
	if trimmedRaw[0] == '"' {
		var s string
		if json.Unmarshal(out.Raw, &s) != nil {
			return AttritionTruncatedJSON, true
		}
		return inspectOutputText(s)
	}
	if !json.Valid(out.Raw) {
		if trimmedRaw[0] == '{' || trimmedRaw[0] == '[' {
			return AttritionTruncatedJSON, true
		}
		return "", false
	}
	return "", false
}

func hasStructuredTruncation(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return false
	}
	var meta struct {
		Truncated           bool   `json:"truncated"`
		PersistedOutputPath string `json:"persistedOutputPath"`
		PersistedOutputSize int64  `json:"persistedOutputSize"`
		File                *struct {
			TruncatedByTokenCap bool `json:"truncatedByTokenCap"`
		} `json:"file"`
	}
	if err := json.Unmarshal(trimmed, &meta); err != nil {
		return false
	}
	if meta.Truncated || meta.PersistedOutputPath != "" || meta.PersistedOutputSize > 0 {
		return true
	}
	return meta.File != nil && meta.File.TruncatedByTokenCap
}

func inspectOutputText(s string) (string, bool) {
	if hasHarnessTruncationMarker(s) {
		return AttritionHarnessTruncation, true
	}
	if isTruncatedJSON(s) {
		return AttritionTruncatedJSON, true
	}
	if isIncompletePythonTraceback(s) {
		return AttritionTruncatedTraceback, true
	}
	return "", false
}

// Known harness truncation banners from Claude Code 2.1.x and CLI runners.
var harnessTruncationMarkers = []string{
	"[output truncated",
	"... [output truncated",
	"output too large; full output saved to",
	"<persisted-output>",
	"... [truncated",
	"[truncated to ",
	"lines omitted]",
	"characters omitted]",
	"bytes omitted]",
}

func hasHarnessTruncationMarker(s string) bool {
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	for _, marker := range harnessTruncationMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isTruncatedJSON(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	switch trimmed[0] {
	case '{':
		return !json.Valid([]byte(trimmed))
	case '[':
		if isBracketLogPrefix(trimmed) {
			return false
		}
		return !json.Valid([]byte(trimmed))
	default:
		return false
	}
}

// isBracketLogPrefix avoids flagging normal CLI status lines such as
// "[PASS] all tests ok" or "[ok] built" while preserving truncated arrays.
func isBracketLogPrefix(trimmed string) bool {
	closeIdx := strings.IndexByte(trimmed, ']')
	if closeIdx <= 1 || closeIdx > 33 {
		return false
	}
	if closeIdx+1 >= len(trimmed) || (trimmed[closeIdx+1] != ' ' && trimmed[closeIdx+1] != '\t') {
		return false
	}
	tag := trimmed[1:closeIdx]
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-' || c == '.' || c == '/' || c == ':') {
			return false
		}
	}
	return true
}

const tracebackHeader = "Traceback (most recent call last):"

func isIncompletePythonTraceback(s string) bool {
	if !strings.Contains(s, tracebackHeader) {
		return false
	}
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	lastHeader := -1
	baseIndent := 0
	for i, l := range lines {
		ind, content := splitIndent(l)
		if content == tracebackHeader {
			lastHeader = i
			baseIndent = ind
		}
	}
	if lastHeader < 0 {
		return false
	}

	// Scan from the final Traceback header forward. A complete traceback ends
	// its indented frames with an unindented (<= baseIndent) exception line.
	for i := lastHeader + 1; i < len(lines); i++ {
		ind, content := splitIndent(lines[i])
		if content == "" {
			continue
		}
		if ind > baseIndent {
			continue
		}
		if isPythonExceptionLine(content) {
			return false
		}
		return true
	}
	return true
}

func splitIndent(line string) (int, string) {
	trimmedRight := strings.TrimRight(line, " \t\r")
	i := 0
	for i < len(trimmedRight) && (trimmedRight[i] == ' ' || trimmedRight[i] == '\t') {
		i++
	}
	return i, trimmedRight[i:]
}

func isPythonExceptionLine(content string) bool {
	for _, skip := range []string{"File ", "Traceback ", "During handling", "The above exception", "---", "==="} {
		if strings.HasPrefix(content, skip) {
			return false
		}
	}
	name := content
	if colon := strings.IndexByte(content, ':'); colon >= 0 {
		name = content[:colon]
	}
	if name == "" {
		return false
	}
	// Dotted or simple Python exception class name.
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '.'
		if i == 0 {
			ok = 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_'
		}
		if !ok {
			return false
		}
	}
	return true
}
