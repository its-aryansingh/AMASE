package hooks

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// Hook timeouts are in seconds (protocol fact C8). A PreToolUse hook that
// times out blocks the call, so a long timeout stalls the agent instead of
// opening a hole; anything over a minute is almost certainly a units mistake
// (3600000 written as milliseconds) and is refused.
const (
	DefaultTimeout = 5
	MaxTimeout     = 60
)

// Config describes the hook settings for one managed session.
type Config struct {
	Token             string
	Port              int
	PreToolUseTimeout int // seconds; 0 means DefaultTimeout
	Timeout           int // PostToolUse, SessionStart and Stop, seconds; 0 means DefaultTimeout
}

type settingsFile struct {
	Hooks hookTable `json:"hooks"`
}

type hookTable struct {
	PreToolUse   []hookGroup `json:"PreToolUse"`
	PostToolUse  []hookGroup `json:"PostToolUse"`
	SessionStart []hookGroup `json:"SessionStart"`
	Stop         []hookGroup `json:"Stop"`
}

type hookGroup struct {
	Matcher string     `json:"matcher,omitempty"`
	Hooks   []hookSpec `json:"hooks"`
}

type hookSpec struct {
	Type      string `json:"type"`
	URL       string `json:"url,omitempty"`
	Timeout   int    `json:"timeout"`
	OnFailure string `json:"onFailure,omitempty"`
}

// Settings returns the --settings file for a managed Claude session whose
// hooks call the resident server on 127.0.0.1:port with token.
func Settings(token string, port int) ([]byte, error) {
	return Generate(Config{Token: token, Port: port})
}

// Generate renders the settings file for c. PreToolUse fails closed
// (onFailure "block"); the other hooks fail open, because telemetry and
// pinning must never stop the agent's work.
func Generate(c Config) ([]byte, error) {
	if err := checkToken(c.Token); err != nil {
		return nil, err
	}
	if c.Port < 1 || c.Port > 65535 {
		return nil, fmt.Errorf("hooks: port %d is outside 1..65535", c.Port)
	}
	pre, other := orDefault(c.PreToolUseTimeout), orDefault(c.Timeout)
	url := URL(c.Token, c.Port)
	hook := func(timeout int, onFailure string) []hookSpec {
		return []hookSpec{{Type: "http", URL: url, Timeout: timeout, OnFailure: onFailure}}
	}
	f := settingsFile{Hooks: hookTable{
		PreToolUse:   []hookGroup{{Matcher: "*", Hooks: hook(pre, "block")}},
		PostToolUse:  []hookGroup{{Matcher: "*", Hooks: hook(other, "")}},
		SessionStart: []hookGroup{{Matcher: "compact", Hooks: hook(other, "")}},
		Stop:         []hookGroup{{Hooks: hook(other, "")}},
	}}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	b = append(b, '\n')
	if err := CheckSettings(b); err != nil {
		return nil, err
	}
	return b, nil
}

func orDefault(timeout int) int {
	if timeout == 0 {
		return DefaultTimeout
	}
	return timeout
}

// URL is the hook endpoint for token on the loopback listener at port.
func URL(token string, port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/hooks/claude/" + token
}

// CheckSettings validates a settings file meant for a managed session: it
// must install at least one HTTP PreToolUse hook, every PreToolUse HTTP hook
// must block on failure, and every HTTP hook timeout must be 1..MaxTimeout
// seconds. Generate runs it on its own output; the D-W13 fallback path
// (settings.local.json in the worktree) should run it too.
func CheckSettings(raw []byte) error {
	var f struct {
		Hooks map[string][]hookGroup `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("hooks: settings: %w", err)
	}
	events := make([]string, 0, len(f.Hooks))
	for name := range f.Hooks {
		events = append(events, name)
	}
	sort.Strings(events)

	gated := 0
	for _, name := range events {
		for _, g := range f.Hooks[name] {
			for _, h := range g.Hooks {
				if h.Type != "http" {
					continue
				}
				if h.Timeout < 1 || h.Timeout > MaxTimeout {
					return fmt.Errorf("hooks: %s hook timeout is %d s; it must be 1..%d seconds (Claude reads hook timeouts in seconds)", name, h.Timeout, MaxTimeout)
				}
				if name == "PreToolUse" {
					if h.OnFailure != "block" {
						return errors.New(`hooks: a PreToolUse hook must set "onFailure": "block", or a failed policy check lets the call run`)
					}
					gated++
				}
			}
		}
	}
	if gated == 0 {
		return errors.New("hooks: settings install no HTTP PreToolUse hook, so tool calls would run ungoverned")
	}
	return nil
}

// NewToken returns a random token for one session's hook URL.
func NewToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error since Go 1.24
	return base64.RawURLEncoding.EncodeToString(b[:])
}
