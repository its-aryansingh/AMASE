package mcp

import (
	"strings"
	"testing"
	"time"
)

func TestHandleRoundTrip(t *testing.T) {
	s, err := NewHandleSigner()
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Mint("snapshot", "deadbeef", "goose", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Redeem(h, "goose")
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if p.Value != "deadbeef" || p.Kind != "snapshot" {
		t.Fatalf("payload round-tripped wrong: %+v", p)
	}
}

// TestHandleBoundToPrincipal is the property that matters under the stateless
// protocol: a handle travels through the model, so a second client that happens
// to see one must not be able to spend it.
func TestHandleBoundToPrincipal(t *testing.T) {
	s, _ := NewHandleSigner()
	h, _ := s.Mint("snapshot", "deadbeef", "goose", time.Minute)
	if _, err := s.Redeem(h, "impostor"); err != ErrHandlePrincipal {
		t.Fatalf("expected ErrHandlePrincipal, got %v", err)
	}
}

func TestHandleRejectsTampering(t *testing.T) {
	s, _ := NewHandleSigner()
	h, _ := s.Mint("snapshot", "deadbeef", "goose", time.Minute)
	parts := strings.Split(h, ".")

	// Flip one character of the payload and keep the original MAC.
	body := []byte(parts[1])
	body[len(body)/2] ^= 1
	forged := parts[0] + "." + string(body) + "." + parts[2]

	if _, err := s.Redeem(forged, "goose"); err == nil {
		t.Fatal("a modified handle was accepted")
	}
}

func TestHandleExpires(t *testing.T) {
	s, _ := NewHandleSigner()
	base := time.Now()
	s.now = func() time.Time { return base }
	h, _ := s.Mint("snapshot", "deadbeef", "goose", time.Minute)

	s.now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, err := s.Redeem(h, "goose"); err != ErrHandleExpired {
		t.Fatalf("expected ErrHandleExpired, got %v", err)
	}
}

// TestHandleIsNotPortableAcrossServers documents the restart behaviour: keys are
// per process, so a handle minted by one instance is refused by another. That is
// intended -- a handle names a filesystem state a fresh process has never seen.
func TestHandleIsNotPortableAcrossServers(t *testing.T) {
	a, _ := NewHandleSigner()
	b, _ := NewHandleSigner()
	h, _ := a.Mint("snapshot", "deadbeef", "goose", time.Minute)
	if _, err := b.Redeem(h, "goose"); err != ErrHandleSignature {
		t.Fatalf("expected ErrHandleSignature, got %v", err)
	}
}

func TestHandleRejectsGarbage(t *testing.T) {
	s, _ := NewHandleSigner()
	for _, bad := range []string{"", "tg1", "tg1.a", "tg2.a.b", "not-a-handle", "tg1..b"} {
		if _, err := s.Redeem(bad, "goose"); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
