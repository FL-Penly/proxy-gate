package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRefreshClaudeToken(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		body = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-at","expires_in":3600}`))
	}))
	defer server.Close()
	old := ClaudeTokenURLOverride
	ClaudeTokenURLOverride = server.URL
	defer func() { ClaudeTokenURLOverride = old }()

	tok, err := RefreshClaudeToken(context.Background(), "old-rt")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if tok.AccessToken != "new-at" || tok.RefreshToken != "old-rt" || tok.ExpiresAt.IsZero() {
		t.Fatalf("unexpected token: %+v", tok)
	}
	if !strings.Contains(body, `"refresh_token":"old-rt"`) {
		t.Fatalf("body missing refresh token: %s", body)
	}
}

func TestRefreshClaudeTokenNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`bad`))
	}))
	defer server.Close()
	old := ClaudeTokenURLOverride
	ClaudeTokenURLOverride = server.URL
	defer func() { ClaudeTokenURLOverride = old }()

	if _, err := RefreshClaudeToken(context.Background(), "rt"); err == nil {
		t.Fatalf("expected error")
	}
}
