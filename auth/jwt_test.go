package auth

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func encodeJWT(payload map[string]any) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	body, _ := json.Marshal(payload)
	return hdr + "." + base64.RawURLEncoding.EncodeToString(body) + ".sig"
}

func TestExtractAccountClaimsNamespaced(t *testing.T) {
	tok := encodeJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc_namespaced",
			"chatgpt_plan_type":  "pro",
			"chatgpt_user_id":    "user_1",
		},
		"https://api.openai.com/profile": map[string]any{
			"email": "x@example.com",
		},
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	})
	c, err := ExtractAccountClaims(tok)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if c.AccountID != "acc_namespaced" {
		t.Errorf("AccountID = %q, want acc_namespaced", c.AccountID)
	}
	if c.PlanType != "pro" {
		t.Errorf("PlanType = %q, want pro", c.PlanType)
	}
	if c.Email != "x@example.com" {
		t.Errorf("Email = %q", c.Email)
	}
	if c.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt zero")
	}
}

func TestExtractAccountClaimsTopLevelFallback(t *testing.T) {
	tok := encodeJWT(map[string]any{
		"chatgpt_account_id": "acc_toplevel",
		"sub":                "user_2",
		"email":              "y@example.com",
	})
	c, err := ExtractAccountClaims(tok)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if c.AccountID != "acc_toplevel" {
		t.Errorf("AccountID = %q, want acc_toplevel", c.AccountID)
	}
	if c.PlanType != "free" {
		t.Errorf("PlanType = %q, want free default", c.PlanType)
	}
	if c.UserID != "user_2" {
		t.Errorf("UserID = %q (sub fallback)", c.UserID)
	}
	if c.Email != "y@example.com" {
		t.Errorf("Email = %q (top-level fallback)", c.Email)
	}
}

func TestExtractAccountClaimsOrgFallback(t *testing.T) {
	tok := encodeJWT(map[string]any{
		"organizations": []any{
			map[string]any{"id": "org_first"},
			map[string]any{"id": "org_second"},
		},
		"sub": "user_3",
	})
	c, err := ExtractAccountClaims(tok)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if c.AccountID != "org_first" {
		t.Errorf("AccountID = %q, want org_first", c.AccountID)
	}
}

func TestDecodeJWTInvalid(t *testing.T) {
	if _, err := DecodeJWT("not.a.jwt.token"); err == nil {
		t.Error("expected error for invalid JWT")
	}
	if _, err := DecodeJWT("only-one-part"); err == nil {
		t.Error("expected error for one-part JWT")
	}
}
