// Package mcp implements the Model Context Protocol server surface for toolgate,
// targeting the 2026-07-28 revision.
//
// That revision matters to the design. MCP used to be a stateful, bidirectional
// protocol: a client sent `initialize`, got a session id back, and the server
// held per-session state for the life of the connection. 2026-07-28 removed all
// of that. There is no initialize handshake and no Mcp-Session-Id; every request
// stands alone and carries its own protocol version, client identity and
// capabilities. Anything a server wants to remember between calls has to be
// handed to the model as an explicit handle and passed back as a tool argument.
//
// For a snapshotting tool that turns out to be an improvement rather than a
// tax, and it is the reason this file exists. A handle that must survive a round
// trip through the model is a handle that the model -- or anything that can
// influence the model -- can modify. So it cannot be a bare row id. It is
// authenticated, bound to a principal, and expiring.
package mcp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// handlePrefix versions the token format so a future change is detectable rather
// than merely broken.
const handlePrefix = "tg1"

// DefaultHandleTTL is how long a snapshot handle stays usable. Short, because a
// handle names a filesystem state that stops being true as soon as the agent
// edits anything.
const DefaultHandleTTL = 30 * time.Minute

var (
	// ErrHandleFormat means the token was not produced by this package.
	ErrHandleFormat = errors.New("malformed state handle")
	// ErrHandleSignature means the token was altered, or was signed by a
	// different server instance.
	ErrHandleSignature = errors.New("state handle failed integrity check")
	// ErrHandleExpired means the token is past its expiry.
	ErrHandleExpired = errors.New("state handle expired")
	// ErrHandlePrincipal means the token belongs to a different caller.
	ErrHandlePrincipal = errors.New("state handle bound to a different principal")
)

// HandlePayload is the claim set inside a state handle.
type HandlePayload struct {
	Kind      string `json:"k"`   // what the handle names, e.g. "snapshot"
	Value     string `json:"v"`   // the content address it points at
	Principal string `json:"sub"` // which client may redeem it
	Expires   int64  `json:"exp"` // unix seconds
	Nonce     string `json:"n"`   // makes otherwise-identical handles distinct
}

// HandleSigner mints and verifies state handles.
//
// The key is generated per process and never written to disk. That means
// handles do not survive a restart, which is the correct failure: a handle names
// a filesystem snapshot, and after a restart the server has no journal position
// to trust it against anyway. Treating "server restarted" as "your handle is
// void" is the safe direction to fail.
type HandleSigner struct {
	key []byte
	now func() time.Time
}

// NewHandleSigner creates a signer with a fresh random key.
func NewHandleSigner() (*HandleSigner, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating handle key: %w", err)
	}
	return &HandleSigner{key: key, now: time.Now}, nil
}

// Mint returns a signed handle naming value for principal.
func (s *HandleSigner) Mint(kind, value, principal string, ttl time.Duration) (string, error) {
	if kind == "" || value == "" {
		return "", errors.New("handle kind and value are required")
	}
	if ttl <= 0 {
		ttl = DefaultHandleTTL
	}
	nonce := make([]byte, 9)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating handle nonce: %w", err)
	}

	payload := HandlePayload{
		Kind:      kind,
		Value:     value,
		Principal: principal,
		Expires:   s.now().Add(ttl).Unix(),
		Nonce:     base64.RawURLEncoding.EncodeToString(nonce),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encoding handle: %w", err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(body)
	return handlePrefix + "." + encoded + "." + s.sign(encoded), nil
}

// Redeem verifies a handle and returns its payload.
//
// Order matters: signature before expiry before principal. Checking expiry first
// would let an attacker distinguish "well-formed but stale" from "forged", which
// is a small oracle but a free one to avoid.
func (s *HandleSigner) Redeem(token, principal string) (HandlePayload, error) {
	var zero HandlePayload

	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != handlePrefix {
		return zero, ErrHandleFormat
	}
	encoded, mac := parts[1], parts[2]

	if subtle.ConstantTimeCompare([]byte(mac), []byte(s.sign(encoded))) != 1 {
		return zero, ErrHandleSignature
	}

	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return zero, ErrHandleFormat
	}
	var payload HandlePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return zero, ErrHandleFormat
	}

	if s.now().Unix() > payload.Expires {
		return zero, ErrHandleExpired
	}
	if payload.Principal != "" && payload.Principal != principal {
		return zero, ErrHandlePrincipal
	}
	return payload, nil
}

func (s *HandleSigner) sign(encoded string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(handlePrefix))
	m.Write([]byte{0})
	m.Write([]byte(encoded))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
