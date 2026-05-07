package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPostOpenAITokenFormEncoded(t *testing.T) {
	var gotContentType string
	var gotBody url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		_ = r.ParseForm()
		gotBody = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","id_token":"it","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer srv.Close()

	form := url.Values{
		"grant_type":    []string{"refresh_token"},
		"refresh_token": []string{"rt-old"},
		"client_id":     []string{openAIClientID},
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()

	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded (spec correction #1)", gotContentType)
	}
	if gotBody.Get("grant_type") != "refresh_token" {
		t.Errorf("grant_type missing")
	}
}

func TestRefreshKeepsRefreshTokenWhenAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","expires_in":1200,"token_type":"Bearer"}`))
	}))
	defer srv.Close()

	prev := openAITokenURL
	openAITokenURLOverride = srv.URL
	t.Cleanup(func() { openAITokenURLOverride = ""; _ = prev })

	tok, err := RefreshOpenAIToken(context.Background(), "rt-original")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if tok.AccessToken != "new-access" {
		t.Errorf("AccessToken = %q", tok.AccessToken)
	}
	if tok.RefreshToken != "rt-original" {
		t.Errorf("RefreshToken = %q, want fallback to original rt-original", tok.RefreshToken)
	}
	if tok.ExpiresAt.IsZero() || time.Until(tok.ExpiresAt) <= 0 {
		t.Errorf("ExpiresAt not set: %v", tok.ExpiresAt)
	}
}

func TestRefreshErrorBubblesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	openAITokenURLOverride = srv.URL
	t.Cleanup(func() { openAITokenURLOverride = "" })

	_, err := RefreshOpenAIToken(context.Background(), "rt-bad")
	if err == nil {
		t.Fatal("expected error")
	}
	te, ok := err.(*TokenError)
	if !ok {
		t.Fatalf("err type %T, want *TokenError", err)
	}
	if te.Status != http.StatusUnauthorized {
		t.Errorf("Status = %d, want 401", te.Status)
	}
}
