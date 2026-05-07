package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestNewPKCE(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE: %v", err)
	}
	if got := len(p.Verifier); got != 43 {
		t.Fatalf("verifier length = %d, want 43", got)
	}
	if strings.ContainsAny(p.Verifier, "+/=") {
		t.Fatalf("verifier must be base64url (no +/=): %q", p.Verifier)
	}
	sum := sha256.Sum256([]byte(p.Verifier))
	expected := base64.RawURLEncoding.EncodeToString(sum[:])
	if p.Challenge != expected {
		t.Fatalf("challenge mismatch: got %q want %q", p.Challenge, expected)
	}
}

func TestNewState(t *testing.T) {
	s, err := NewState()
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	if got := len(s); got != 32 {
		t.Fatalf("state length = %d, want 32 hex chars", got)
	}
}

func TestOpenAIAuthorizeURL(t *testing.T) {
	u := OpenAIAuthorizeURL("http://localhost:1455/auth/callback", "challenge_xyz", "state_abc")
	wants := []string{
		"client_id=" + OpenAIClientID,
		"response_type=code",
		"code_challenge=challenge_xyz",
		"code_challenge_method=S256",
		"state=state_abc",
		"prompt=login",
		"max_age=0",
		"originator=codex_cli_rs",
		"id_token_add_organizations=true",
	}
	for _, w := range wants {
		if !strings.Contains(u, w) {
			t.Errorf("missing %q in %s", w, u)
		}
	}
}
