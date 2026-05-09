package ingress

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FL-Penly/proxy-gate/broker"
	"github.com/FL-Penly/proxy-gate/provider"
)

const fakeAnthropicSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"cache_read_input_tokens":3}}}

event: content_block_delta
data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"pong"}}

event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}

`

func TestMessagesClaudePoolSuccess(t *testing.T) {
	var gotAuth string
	var gotBody []byte
	var gotBeta, gotVersion, gotApp string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		gotVersion = r.Header.Get("anthropic-version")
		gotApp = r.Header.Get("x-app")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("request-id", "req_test")
		_, _ = w.Write([]byte(`{"id":"msg_1","model":"claude-sonnet-4-5","usage":{"input_tokens":7,"output_tokens":2},"content":[{"type":"text","text":"pong"}]}`))
	}))
	defer upstream.Close()

	h, acc, rec := newClaudeMessagesHandler(upstream)
	body := `{"model":"claude-sonnet-4-5","messages":[],"tools":[{"name":"t","input_schema":{"type":"object"}}],"thinking":{"type":"enabled","budget_tokens":1024}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(body))
	req.Header.Set("anthropic-beta", "claude-code-20250219,interleaved-thinking-2025-05-14")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-app", "cli")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if gotAuth != "Bearer claude-token" {
		t.Fatalf("Authorization=%q", gotAuth)
	}
	if string(gotBody) != body {
		t.Fatalf("body changed: %s", string(gotBody))
	}
	if gotBeta != "oauth-2025-04-20,claude-code-20250219,interleaved-thinking-2025-05-14" || gotVersion != "2023-06-01" || gotApp != "cli" {
		t.Fatalf("headers beta=%q version=%q app=%q", gotBeta, gotVersion, gotApp)
	}
	if w.Header().Get("request-id") != "req_test" {
		t.Fatalf("missing upstream response header: %v", w.Header())
	}
	if s := acc.Stats(); s.TotalRequests != 1 || s.TotalInputTkn != 7 || s.TotalOutputTkn != 2 {
		t.Fatalf("stats=%+v", s)
	}
	records := rec.records_()
	if len(records) != 1 || records[0].Provider != "claude-pool" || records[0].Account != acc.Email {
		t.Fatalf("records=%+v", records)
	}
}

func TestMessagesClaudeStreamingUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fakeAnthropicSSE))
	}))
	defer upstream.Close()

	h, acc, rec := newClaudeMessagesHandler(upstream)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"claude-sonnet-4-5","stream":true,"messages":[]}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("message_stop")) {
		t.Fatalf("missing SSE body: %s", w.Body.String())
	}
	if s := acc.Stats(); s.TotalInputTkn != 10 || s.TotalOutputTkn != 4 {
		t.Fatalf("stats=%+v", s)
	}
	if records := rec.records_(); len(records) != 1 || records[0].CachedTokens != 3 || records[0].ResponseID != "msg_1" {
		t.Fatalf("records=%+v", records)
	}
}

func TestMessagesClaudeRefreshThenRetry(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") == "Bearer expired" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_1","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	h, acc, _ := newClaudeMessagesHandler(upstream)
	acc.AccessToken = "expired"
	h.ClaudeRefresher = fakeClaudeRefresher{access: "fresh"}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"claude","messages":[]}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if hits.Load() != 2 {
		t.Fatalf("hits=%d want 2", hits.Load())
	}
	if acc.AccessToken != "fresh" || acc.Stats().Dead {
		t.Fatalf("account after refresh token=%q stats=%+v", acc.AccessToken, acc.Stats())
	}
}

func TestMessagesClaudeFailureFallsBackToAPIKey(t *testing.T) {
	var gotKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		gotKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{"id":"msg_1","usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer upstream.Close()

	h, acc, _ := newClaudeMessagesHandler(upstream)
	h.KeyPool = broker.NewAPIKeyPool()
	k := &broker.APIKey{ID: "ant", Type: broker.KeyTypeAnthropic, APIKey: "sk-ant", BaseURL: upstream.URL}
	k.ApplyStats(broker.APIKeyStats{})
	h.KeyPool.Add(k)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"claude","messages":[]}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !acc.Stats().Dead || gotKey != "sk-ant" {
		t.Fatalf("dead=%v gotKey=%q", acc.Stats().Dead, gotKey)
	}
}

func TestMessagesAPIKeyFirstPriority(t *testing.T) {
	var gotKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{"id":"msg_1","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	h, _, _ := newClaudeMessagesHandler(upstream)
	h.Priority = "apikey-first"
	h.KeyPool = broker.NewAPIKeyPool()
	k := &broker.APIKey{ID: "ant", Type: broker.KeyTypeAnthropic, APIKey: "sk-ant", BaseURL: upstream.URL}
	k.ApplyStats(broker.APIKeyStats{})
	h.KeyPool.Add(k)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"claude","messages":[]}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK || gotKey != "sk-ant" {
		t.Fatalf("status=%d gotKey=%q body=%s", w.Code, gotKey, w.Body.String())
	}
}

func newClaudeMessagesHandler(upstream *httptest.Server) (*MessagesHandler, *broker.ClaudeAccount, *fakeRecorder) {
	pool := broker.NewClaudePool()
	acc := &broker.ClaudeAccount{Email: "claude@example.com", AccessToken: "claude-token", RefreshToken: "rt"}
	acc.ApplyStats(broker.ClaudeAccountStats{})
	pool.Add(acc)
	rec := &fakeRecorder{}
	return &MessagesHandler{
		ClaudePool: pool,
		Anthropic:  &provider.AnthropicClient{HTTPClient: upstream.Client(), BaseURL: upstream.URL},
		Recorder:   rec,
	}, acc, rec
}

type fakeClaudeRefresher struct{ access string }

func (f fakeClaudeRefresher) RefreshClaudeToken(_ context.Context, acc *broker.ClaudeAccount) error {
	acc.UpdateTokens(f.access, "", timeNowPlusHour())
	return nil
}

func timeNowPlusHour() time.Time { return time.Now().Add(time.Hour) }
