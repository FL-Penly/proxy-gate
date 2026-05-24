package ingress

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/FL-Penly/proxy-gate/broker"
	"github.com/FL-Penly/proxy-gate/provider"
)

const fakeSSEStream = `event: response.created
data: {"type":"response.created","response":{"id":"resp_xyz","model":"gpt-5","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"PHASE_1_PASS"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_xyz","model":"gpt-5","status":"completed","usage":{"input_tokens":150,"input_tokens_details":{"cached_tokens":0},"output_tokens":12,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":162}}}

`

type fakeRecorder struct {
	mu      sync.Mutex
	records []UsageRecord
}

func (f *fakeRecorder) RecordRequest(rec UsageRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, rec)
}

func (f *fakeRecorder) records_() []UsageRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]UsageRecord, len(f.records))
	copy(out, f.records)
	return out
}

func newTestHandler(t *testing.T, upstream *httptest.Server, account *broker.Account, recorder RequestRecorder) *ResponsesHandler {
	t.Helper()
	pool := broker.NewPool(broker.PoolConfig{})
	pool.Add(account)
	return &ResponsesHandler{
		Pool:     pool,
		ChatGPT:  &provider.ChatGPTClient{HTTPClient: upstream.Client(), BaseURL: upstream.URL},
		Recorder: recorder,
	}
}

func newAccount() *broker.Account {
	a := &broker.Account{
		Email:       "test@example.com",
		AccountID:   "acc-test",
		PlanType:    broker.PlanPro,
		AccessToken: "fake-token",
	}
	a.ApplyStats(broker.AccountStats{})
	return a
}

func TestStreamingResponseRecordsUsage(t *testing.T) {
	var capturedReq *http.Request
	var capturedBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("openai-model", "gpt-5")
		w.WriteHeader(200)
		f, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(fakeSSEStream))
		if f != nil {
			f.Flush()
		}
	}))
	defer upstream.Close()

	rec := &fakeRecorder{}
	h := newTestHandler(t, upstream, newAccount(), rec)

	body := bytes.NewBufferString(`{"model":"gpt-5","input":[{"type":"message","role":"user","content":"hi"}],"max_output_tokens":1024}`)
	req := httptest.NewRequest("POST", "/v1/responses", body)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.Background())
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "PHASE_1_PASS") {
		t.Errorf("relayed body missing payload: %s", w.Body.String())
	}
	if got := w.Header().Get("openai-model"); got != "gpt-5" {
		t.Errorf("openai-model header relay missing, got %q", got)
	}
	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}

	if got := capturedReq.Header.Get("Authorization"); got != "Bearer fake-token" {
		t.Errorf("upstream Authorization = %q", got)
	}
	if strings.Contains(string(capturedBody), `"store":`) {
		t.Errorf("store must not be injected when client omits it: %s", capturedBody)
	}
	if !strings.Contains(string(capturedBody), `"instructions":`) {
		t.Errorf("body adaptation failed (instructions field MUST exist or upstream rejects with 400): %s", capturedBody)
	}
	if strings.Contains(string(capturedBody), "max_output_tokens") {
		t.Errorf("body adaptation failed: max_output_tokens not stripped: %s", capturedBody)
	}

	records := rec.records_()
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	r := records[0]
	if !r.Success {
		t.Errorf("record.Success = false, error=%q", r.Error)
	}
	if r.InputTokens != 150 {
		t.Errorf("InputTokens = %d, want 150 (P0 BUG: streaming MUST record non-zero)", r.InputTokens)
	}
	if r.OutputTokens != 12 {
		t.Errorf("OutputTokens = %d, want 12 (P0 BUG)", r.OutputTokens)
	}
	if r.TotalTokens != 162 {
		t.Errorf("TotalTokens = %d, want 162", r.TotalTokens)
	}
	if r.ResponseID != "resp_xyz" {
		t.Errorf("ResponseID = %q", r.ResponseID)
	}
	if r.Model != "gpt-5" {
		t.Errorf("Model = %q", r.Model)
	}
}

func TestCompactNonStreamingRecordsUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"resp_a","model":"gpt-5","usage":{"input_tokens":80,"output_tokens":40,"total_tokens":120}}`))
	}))
	defer upstream.Close()

	rec := &fakeRecorder{}
	h := newTestHandler(t, upstream, newAccount(), rec)

	body := bytes.NewBufferString(`{"model":"gpt-5","input":[]}`)
	req := httptest.NewRequest("POST", "/v1/responses/compact", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	records := rec.records_()
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
	r := records[0]
	if r.InputTokens != 80 || r.OutputTokens != 40 {
		t.Errorf("compact usage missed: in=%d out=%d", r.InputTokens, r.OutputTokens)
	}
}

func TestUpstream401TriggersRefresh(t *testing.T) {
	var attempts int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "Bearer fake-token" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"expired"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(fakeSSEStream))
		attempts++
	}))
	defer upstream.Close()

	rec := &fakeRecorder{}
	acc := newAccount()
	h := newTestHandler(t, upstream, acc, rec)
	h.Refresher = &fakeRefresher{newToken: "fresh-token"}

	body := bytes.NewBufferString(`{"model":"gpt-5"}`)
	req := httptest.NewRequest("POST", "/v1/responses", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if acc.AccessToken != "fresh-token" {
		t.Errorf("token not refreshed: %q", acc.AccessToken)
	}
}

func TestNoAccountsReturns401(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("upstream should not be called when pool is empty")
	}))
	defer upstream.Close()
	pool := broker.NewPool(broker.PoolConfig{})
	h := &ResponsesHandler{
		Pool:    pool,
		ChatGPT: &provider.ChatGPTClient{HTTPClient: upstream.Client(), BaseURL: upstream.URL},
	}
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestGzipBodyDecompressedOnce(t *testing.T) {
	var capturedBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_a","model":"gpt-5","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	rec := &fakeRecorder{}
	h := newTestHandler(t, upstream, newAccount(), rec)

	original := `{"model":"gpt-5","stream":false,"store":false}`
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, _ = gz.Write([]byte(original))
	_ = gz.Close()

	req := httptest.NewRequest("POST", "/v1/responses", &compressed)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var sent map[string]any
	if err := json.Unmarshal(capturedBody, &sent); err != nil {
		t.Fatalf("upstream body not valid JSON (decompressed once?): %v - %q", err, capturedBody)
	}
	if sent["store"] != false {
		t.Errorf("client store=false must be preserved through gzip decode: %v", sent)
	}
}

type fakeRefresher struct {
	newToken string
}

func (f *fakeRefresher) RefreshToken(_ context.Context, acc *broker.Account) error {
	acc.AccessToken = f.newToken
	return nil
}
