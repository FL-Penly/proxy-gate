package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestForwardSetsAuthAndStreamingHeaders(t *testing.T) {
	var captured *http.Request
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response.created\ndata: {}\n\n"))
	}))
	defer srv.Close()

	client := &ChatGPTClient{HTTPClient: srv.Client(), BaseURL: srv.URL}

	headers := http.Header{}
	headers.Set("x-codex-turn-state", "abc")
	headers.Set("x-not-allowed", "should not pass")

	resp, err := client.Forward(context.Background(), ForwardRequest{
		Body:            []byte(`{"model":"gpt-5"}`),
		ContentType:     "application/json",
		IncomingHeaders: headers,
		Credential:      Credential{AccessToken: "tok-1", AccountID: "acc-1"},
		Streaming:       true,
	})
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	defer resp.Body.Close()

	if got := captured.Header.Get("Authorization"); got != "Bearer tok-1" {
		t.Errorf("Authorization = %q", got)
	}
	if got := captured.Header.Get("ChatGPT-Account-Id"); got != "acc-1" {
		t.Errorf("ChatGPT-Account-Id = %q", got)
	}
	if got := captured.Header.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q for streaming, want text/event-stream", got)
	}
	if got := captured.Header.Get("Accept-Encoding"); got != "identity" {
		t.Errorf("Accept-Encoding = %q, want identity", got)
	}
	if got := captured.Header.Get("x-codex-turn-state"); got != "abc" {
		t.Errorf("x-codex-turn-state passthrough lost: %q", got)
	}
	if got := captured.Header.Get("x-not-allowed"); got != "" {
		t.Errorf("x-not-allowed should be stripped, got %q", got)
	}
	if string(capturedBody) != `{"model":"gpt-5"}` {
		t.Errorf("body = %q", capturedBody)
	}
}

func TestForwardNonStreamingAccept(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := &ChatGPTClient{HTTPClient: srv.Client(), BaseURL: srv.URL}
	resp, err := client.Forward(context.Background(), ForwardRequest{
		Body:       []byte(`{}`),
		Credential: Credential{AccessToken: "t", AccountID: "a"},
	})
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	resp.Body.Close()
	if got := captured.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q for non-streaming", got)
	}
}

func TestForwardForwardsContentEncoding(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(200)
	}))
	defer srv.Close()
	client := &ChatGPTClient{HTTPClient: srv.Client(), BaseURL: srv.URL}
	resp, err := client.Forward(context.Background(), ForwardRequest{
		Body:            []byte("compressed-bytes"),
		ContentEncoding: "zstd",
		Credential:      Credential{AccessToken: "t", AccountID: "a"},
	})
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	resp.Body.Close()
	if got := captured.Header.Get("Content-Encoding"); got != "zstd" {
		t.Errorf("Content-Encoding = %q", got)
	}
}

func TestParseWhamUsage(t *testing.T) {
	body := []byte(`{
		"plan_type":"plus",
		"rate_limit":{
			"limit_reached":false,
			"primary_window":{"used_percent":42.5,"reset_at":` + futureEpoch() + `},
			"secondary_window":{"used_percent":80.0,"reset_after_seconds":3600}
		}
	}`)
	u, err := parseWhamUsage(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.PrimaryUsedPct != 0.425 {
		t.Errorf("PrimaryUsedPct = %f, want 0.425 (÷100)", u.PrimaryUsedPct)
	}
	if u.SecondaryUsedPct != 0.80 {
		t.Errorf("SecondaryUsedPct = %f, want 0.80", u.SecondaryUsedPct)
	}
	if u.PlanType != "plus" {
		t.Errorf("PlanType = %q", u.PlanType)
	}
	if u.PrimaryResetAt.IsZero() {
		t.Errorf("PrimaryResetAt not parsed")
	}
	if u.SecondaryResetAt.IsZero() {
		t.Errorf("SecondaryResetAt not parsed")
	}
}

func futureEpoch() string {
	return strings.TrimSpace(itoa64(time.Now().Add(time.Hour).Unix()))
}

func itoa64(n int64) string {
	buf := make([]byte, 0, 20)
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
