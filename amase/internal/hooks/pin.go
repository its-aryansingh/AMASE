package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// MaxPinTokens caps the size of one re-injected constraint bundle so
	// pinning invariants cannot crowd out post-compaction working context.
	MaxPinTokens = 200

	// maxInvariantsBytes bounds how much of .amase/invariants.toml we read: a
	// file much larger than a few kilobytes cannot fit under MaxPinTokens anyway.
	maxInvariantsBytes = 64 << 10
)

// InvariantsRelPath is the per-repository TOML file holding pinned rules.
var InvariantsRelPath = filepath.Join(".amase", "invariants.toml")

// Rule is one standing constraint declared in a [[rule]] table of
// .amase/invariants.toml.
type Rule struct {
	ID   string
	Text string
}

// ParseInvariants parses the restricted TOML subset allowed in
// .amase/invariants.toml: comments ('#'), blank lines, and [[rule]] tables
// each declaring string keys "id" and "text". Unknown sections, unknown keys,
// duplicate keys, duplicate ids, non-string values, and missing fields error.
func ParseInvariants(data []byte) ([]Rule, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("hooks: invariants file is not valid UTF-8")
	}
	src := strings.TrimPrefix(string(data), "\xef\xbb\xbf")
	lines := strings.Split(src, "\n")

	var rules []Rule
	var cur *Rule
	var hasID, hasText bool
	seenIDs := map[string]struct{}{}

	flush := func(lineNo int) error {
		if cur == nil {
			return nil
		}
		if !hasID || strings.TrimSpace(cur.ID) == "" {
			return fmt.Errorf("hooks: invariants line %d: [[rule]] missing non-empty id", lineNo)
		}
		if !hasText || strings.TrimSpace(cur.Text) == "" {
			return fmt.Errorf("hooks: invariants line %d: [[rule]] %q missing non-empty text", lineNo, cur.ID)
		}
		if strings.ContainsAny(cur.ID, "\r\n") {
			return fmt.Errorf("hooks: invariants line %d: rule id must be single-line", lineNo)
		}
		if _, dup := seenIDs[cur.ID]; dup {
			return fmt.Errorf("hooks: invariants line %d: duplicate rule id %q", lineNo, cur.ID)
		}
		seenIDs[cur.ID] = struct{}{}
		rules = append(rules, *cur)
		cur, hasID, hasText = nil, false, false
		return nil
	}

	for i, rawLine := range lines {
		lineNo := i + 1
		line := strings.TrimSuffix(rawLine, "\r")
		trimmed := strings.TrimSpace(stripTOMLComment(line))
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			if trimmed != "[[rule]]" {
				return nil, fmt.Errorf("hooks: invariants line %d: unsupported section %q (expected [[rule]])", lineNo, trimmed)
			}
			if err := flush(lineNo); err != nil {
				return nil, err
			}
			cur = &Rule{}
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("hooks: invariants line %d: key assignment outside [[rule]] section", lineNo)
		}
		key, val, err := parseKeyString(trimmed, lineNo)
		if err != nil {
			return nil, err
		}
		switch key {
		case "id":
			if hasID {
				return nil, fmt.Errorf("hooks: invariants line %d: duplicate key \"id\"", lineNo)
			}
			cur.ID = val
			hasID = true
		case "text":
			if hasText {
				return nil, fmt.Errorf("hooks: invariants line %d: duplicate key \"text\"", lineNo)
			}
			cur.Text = val
			hasText = true
		default:
			return nil, fmt.Errorf("hooks: invariants line %d: unknown key %q (allowed: id, text)", lineNo, key)
		}
	}
	if err := flush(len(lines)); err != nil {
		return nil, err
	}
	return rules, nil
}

// EstimateTokens estimates token count as ceil(chars / 4).
func EstimateTokens(s string) int {
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

// BuildPin formats rules into the compact AMASE-PIN v1 bundle, computes its
// 12-hex SHA-256 fingerprint, estimates tokens, and rejects bundles above
// MaxPinTokens. An empty rule slice produces the zero Pin cleanly.
func BuildPin(rules []Rule) (Pin, error) {
	if len(rules) == 0 {
		return Pin{}, nil
	}
	var body strings.Builder
	for i, r := range rules {
		if i > 0 {
			body.WriteByte('\n')
		}
		id := strings.TrimSpace(r.ID)
		text := strings.Join(strings.Fields(r.Text), " ")
		if id == "" || text == "" {
			return Pin{}, fmt.Errorf("hooks: rule %d must have non-empty id and text", i+1)
		}
		fmt.Fprintf(&body, "- [%s] %s", id, text)
	}
	sum := sha256.Sum256([]byte(body.String()))
	shortHash := hex.EncodeToString(sum[:])[:12]
	bundle := fmt.Sprintf("AMASE-PIN v1 sha256:%s\n%s", shortHash, body.String())
	tokens := EstimateTokens(bundle)
	if tokens > MaxPinTokens {
		return Pin{}, fmt.Errorf("hooks: constraint pin exceeds %d-token limit (%d tokens est)", MaxPinTokens, tokens)
	}
	return Pin{
		Context:      bundle,
		BundleSHA256: shortHash,
		TokensEst:    tokens,
	}, nil
}

// LoadPin reads <workspaceRoot>/.amase/invariants.toml and returns its compiled
// Pin. A missing file is not an error and returns the zero Pin.
func LoadPin(workspaceRoot string) (Pin, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return Pin{}, nil
	}
	p := filepath.Join(workspaceRoot, InvariantsRelPath)
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Pin{}, nil
		}
		return Pin{}, fmt.Errorf("hooks: open invariants: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxInvariantsBytes+1))
	closeErr := f.Close()
	if readErr != nil {
		return Pin{}, fmt.Errorf("hooks: read invariants: %w", readErr)
	}
	if closeErr != nil {
		return Pin{}, fmt.Errorf("hooks: close invariants: %w", closeErr)
	}
	if len(data) > maxInvariantsBytes {
		return Pin{}, fmt.Errorf("hooks: invariants file exceeds %d-byte read cap", maxInvariantsBytes)
	}
	rules, err := ParseInvariants(data)
	if err != nil {
		return Pin{}, err
	}
	return BuildPin(rules)
}

// NewPinner returns a Pinner that loads invariants from each binding's
// Workspace.Root. Errors are handed to onError (if non-nil) and the Pinner
// fails open per SPEC I3 by returning false.
func NewPinner(onError func(Binding, error)) Pinner {
	return func(b Binding) (Pin, bool) {
		pin, err := LoadPin(b.Workspace.Root)
		if err != nil {
			if onError != nil {
				onError(b, err)
			}
			return Pin{}, false
		}
		if pin.Context == "" {
			return Pin{}, false
		}
		return pin, true
	}
}

func stripTOMLComment(line string) string {
	inBasic, inLit, escaped := false, false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		if inBasic {
			if c == '\\' {
				escaped = true
			} else if c == '"' {
				inBasic = false
			}
			continue
		}
		if inLit {
			if c == '\'' {
				inLit = false
			}
			continue
		}
		if c == '"' {
			inBasic = true
		} else if c == '\'' {
			inLit = true
		} else if c == '#' {
			return line[:i]
		}
	}
	return line
}

func parseKeyString(line string, lineNo int) (string, string, error) {
	eq := strings.IndexByte(line, '=')
	if eq <= 0 {
		return "", "", fmt.Errorf("hooks: invariants line %d: expected key = \"value\"", lineNo)
	}
	key := strings.TrimSpace(line[:eq])
	if !isBareKey(key) {
		return "", "", fmt.Errorf("hooks: invariants line %d: invalid key %q", lineNo, key)
	}
	rawVal := strings.TrimSpace(line[eq+1:])
	if len(rawVal) < 2 {
		return "", "", fmt.Errorf("hooks: invariants line %d: expected quoted string for %q", lineNo, key)
	}
	switch rawVal[0] {
	case '"':
		val, err := parseBasicString(rawVal, lineNo)
		return key, val, err
	case '\'':
		val, err := parseLiteralString(rawVal, lineNo)
		return key, val, err
	default:
		return "", "", fmt.Errorf("hooks: invariants line %d: value for %q must be a quoted string", lineNo, key)
	}
}

func isBareKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func parseLiteralString(raw string, lineNo int) (string, error) {
	if strings.HasPrefix(raw, "'''") {
		return "", fmt.Errorf("hooks: invariants line %d: multiline literal strings are not supported", lineNo)
	}
	end := strings.IndexByte(raw[1:], '\'')
	if end < 0 {
		return "", fmt.Errorf("hooks: invariants line %d: unterminated literal string", lineNo)
	}
	if strings.TrimSpace(raw[end+2:]) != "" {
		return "", fmt.Errorf("hooks: invariants line %d: unexpected trailing tokens after string", lineNo)
	}
	return raw[1 : end+1], nil
}

func parseBasicString(raw string, lineNo int) (string, error) {
	if strings.HasPrefix(raw, `"""`) {
		return "", fmt.Errorf("hooks: invariants line %d: multiline basic strings are not supported", lineNo)
	}
	var b strings.Builder
	i := 1
	for i < len(raw) {
		c := raw[i]
		if c == '"' {
			if strings.TrimSpace(raw[i+1:]) != "" {
				return "", fmt.Errorf("hooks: invariants line %d: unexpected trailing tokens after string", lineNo)
			}
			return b.String(), nil
		}
		if c < 0x20 && c != '\t' {
			return "", fmt.Errorf("hooks: invariants line %d: unescaped control char in string", lineNo)
		}
		if c != '\\' {
			r, size := utf8.DecodeRuneInString(raw[i:])
			b.WriteRune(r)
			i += size
			continue
		}
		i++
		if i >= len(raw) {
			return "", fmt.Errorf("hooks: invariants line %d: trailing backslash in string", lineNo)
		}
		switch raw[i] {
		case '"', '\\':
			b.WriteByte(raw[i])
			i++
		case 'b':
			b.WriteByte('\b')
			i++
		case 'f':
			b.WriteByte('\f')
			i++
		case 'n':
			b.WriteByte('\n')
			i++
		case 'r':
			b.WriteByte('\r')
			i++
		case 't':
			b.WriteByte('\t')
			i++
		case 'u', 'U':
			hexLen := 4
			if raw[i] == 'U' {
				hexLen = 8
			}
			i++
			if i+hexLen > len(raw) {
				return "", fmt.Errorf("hooks: invariants line %d: short unicode escape", lineNo)
			}
			v, err := strconv.ParseUint(raw[i:i+hexLen], 16, 32)
			r := rune(v)
			if err != nil || !utf8.ValidRune(r) {
				return "", fmt.Errorf("hooks: invariants line %d: invalid unicode escape", lineNo)
			}
			b.WriteRune(r)
			i += hexLen
		default:
			return "", fmt.Errorf("hooks: invariants line %d: unknown escape \\%c", lineNo, raw[i])
		}
	}
	return "", fmt.Errorf("hooks: invariants line %d: unterminated basic string", lineNo)
}
