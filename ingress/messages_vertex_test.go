package ingress

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/FL-Penly/proxy-gate/broker"
	"github.com/FL-Penly/proxy-gate/provider"
	"github.com/tidwall/gjson"
)

type testVertexToken string

func (s testVertexToken) Token(context.Context) (string, error) { return string(s), nil }
func (s testVertexToken) Method() string                        { return "test" }

func TestMessagesFallsBackToVertexWhenClaudePoolExhausted(t *testing.T) {
	var gotPath string
	var gotBody []byte
	vertex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_v","model":"claude-sonnet-4-6","usage":{"input_tokens":5,"output_tokens":7},"content":[{"type":"text","text":"ok"}]}`))
	}))
	defer vertex.Close()

	pool := broker.NewClaudePool()
	acc := &broker.ClaudeAccount{Email: "off@example.com", AccessToken: "tok"}
	acc.ApplyStats(broker.ClaudeAccountStats{Disabled: true})
	pool.Add(acc)
	rec := &fakeRecorder{}
	h := &MessagesHandler{
		ClaudePool: pool,
		Vertex:     testVertexClient(vertex),
		Recorder:   rec,
		FallbackRuntime: NewClaudeFallbackRuntime(ClaudeFallbackPolicy{Enabled: true, Rules: []ClaudeFallbackRule{
			{Enabled: true, FromModel: "claude-opus4.7", ToModel: "claude-opus-4-6", ToVariant: "thinking-16k"},
		}}),
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"claude-opus-4-7","max_tokens":1000,"messages":[]}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains([]byte(gotPath), []byte("/models/claude-opus-4-6:rawPredict")) {
		t.Fatalf("path=%q", gotPath)
	}
	if gjson.GetBytes(gotBody, "model").Exists() {
		t.Fatalf("vertex body should omit model: %s", gotBody)
	}
	if got := gjson.GetBytes(gotBody, "thinking.budget_tokens").Int(); got != 16000 {
		t.Fatalf("thinking budget=%d body=%s", got, gotBody)
	}
	records := rec.records_()
	if len(records) != 1 || records[0].Provider != "vertex-ai" || records[0].KeyID != "vertex-ai" || records[0].Model != "claude-sonnet-4-6" {
		t.Fatalf("records=%+v", records)
	}
}

func TestMessagesFallbackPolicySkipsAnthropicAPIKey(t *testing.T) {
	var keyHits atomic.Int32
	keyUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		keyHits.Add(1)
		_, _ = w.Write([]byte(`{"id":"msg_key","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer keyUpstream.Close()
	vertex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"msg_v","model":"claude-sonnet-4-6","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer vertex.Close()

	keyPool := broker.NewAPIKeyPool()
	k := &broker.APIKey{ID: "ant", Type: broker.KeyTypeAnthropic, APIKey: "sk-ant", BaseURL: keyUpstream.URL}
	k.ApplyStats(broker.APIKeyStats{})
	keyPool.Add(k)
	h := &MessagesHandler{
		KeyPool: keyPool,
		Anthropic: &provider.AnthropicClient{
			HTTPClient: keyUpstream.Client(),
			BaseURL:    keyUpstream.URL,
		},
		Vertex: testVertexClient(vertex),
		FallbackRuntime: NewClaudeFallbackRuntime(ClaudeFallbackPolicy{Enabled: true, Rules: []ClaudeFallbackRule{
			{Enabled: true, FromModel: "claude-haiku-4-5", ToModel: "claude-sonnet-4-6", ToVariant: "preserve"},
		}}),
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"claude-haiku4.5","messages":[]}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if keyHits.Load() != 0 {
		t.Fatalf("anthropic key upstream was called %d times", keyHits.Load())
	}
}

func TestMessagesFallbackNoMatchingRule(t *testing.T) {
	vertex := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer vertex.Close()
	h := &MessagesHandler{
		Vertex: testVertexClient(vertex),
		FallbackRuntime: NewClaudeFallbackRuntime(ClaudeFallbackPolicy{Enabled: true, Rules: []ClaudeFallbackRule{
			{Enabled: true, FromModel: "claude-opus-4-7", ToModel: "claude-opus-4-6", ToVariant: "preserve"},
		}}),
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"claude-sonnet-4-6","messages":[]}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func testVertexClient(server *httptest.Server) *provider.AnthropicVertexClient {
	return &provider.AnthropicVertexClient{
		HTTPClient: server.Client(),
		BaseURL:    server.URL,
		Config: provider.VertexAnthropicConfig{
			ProjectID: "p",
			Location:  "us-east5",
		},
		TokenSource: testVertexToken("tok"),
	}
}
