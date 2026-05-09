package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportClaudeCredentialShape(t *testing.T) {
	src := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(src, []byte(`{
  "claudeAiOauth": {
    "accessToken": "at",
    "refreshToken": "rt",
    "expiresAt": 1893456000000,
    "accountId": "acct_1",
    "subscriptionType": "max"
  }
}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	dest := t.TempDir()
	acc, err := ImportClaudeAccount(src, dest, "user@example.com")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if acc.Email != "user@example.com" || acc.AccessToken != "at" || acc.RefreshToken != "rt" || acc.AccountID != "acct_1" {
		t.Fatalf("account=%+v", acc)
	}
	if _, err := os.Stat(filepath.Join(dest, "user_example.com.json")); err != nil {
		t.Fatalf("saved file missing: %v", err)
	}
}

func TestImportClaudeNativeShape(t *testing.T) {
	src := filepath.Join(t.TempDir(), "native.json")
	if err := os.WriteFile(src, []byte(`{"email":"native@example.com","access_token":"at","refresh_token":"rt"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	acc, err := ImportClaudeAccount(src, t.TempDir(), "")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if acc.Email != "native@example.com" || acc.AccessToken != "at" {
		t.Fatalf("account=%+v", acc)
	}
}

func TestImportClaudeCredentialRequiresEmail(t *testing.T) {
	src := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(src, []byte(`{"claudeAiOauth":{"accessToken":"at"}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ImportClaudeAccount(src, t.TempDir(), ""); err == nil {
		t.Fatalf("expected missing email error")
	}
}
