package control_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codeking-ai/cligate-v2/broker"
	"github.com/codeking-ai/cligate-v2/control"
	"github.com/codeking-ai/cligate-v2/ingress"
	"github.com/codeking-ai/cligate-v2/pricing"
	"github.com/codeking-ai/cligate-v2/provider"
	"github.com/codeking-ai/cligate-v2/store"
)

const sseGPT54PriorityResponse = `event: response.created
data: {"type":"response.created","response":{"id":"resp_e2e","model":"gpt-5.4","status":"in_progress","service_tier":"priority"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_e2e","model":"gpt-5.4","status":"completed","service_tier":"priority","usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":200},"output_tokens":500,"total_tokens":1500}}}

`

const sseGPT55Response = `event: response.created
data: {"type":"response.created","response":{"id":"resp_e2e2","model":"gpt-5.5"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_e2e2","model":"gpt-5.5","status":"completed","usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":0},"output_tokens":300,"total_tokens":2300}}}

`

func newE2EFixture(t *testing.T) (*control.Recorder, *control.AdminAPI, *pricing.Source, func()) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "e2e.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	q := control.NewQueue(st, control.WithBatchSize(1), control.WithMaxWait(time.Millisecond))
	pool := broker.NewPool(broker.PoolConfig{})
	rec := control.NewRecorder(st, q, pool)

	embed, err := pricing.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	src := pricing.NewSource(embed)
	svc := &pricing.Service{Source: src}

	admin := &control.AdminAPI{
		Pool:     pool,
		Recorder: rec,
		Pricing:  svc,
		Queue:    q,
		Token:    "test-token",
	}

	cleanup := func() {
		q.Close()
		_ = st.Close()
	}
	return rec, admin, src, cleanup
}

func newAccountForE2E(email string) *broker.Account {
	a := &broker.Account{
		Email:       email,
		AccountID:   "acc-" + email,
		PlanType:    broker.PlanPro,
		AccessToken: "fake-token",
	}
	a.ApplyStats(broker.AccountStats{})
	return a
}

func newAPIKeyForE2E(id string) *broker.APIKey {
	k := &broker.APIKey{ID: id, Name: id, Type: broker.KeyTypeOpenAI, APIKey: "sk-fake"}
	k.ApplyStats(broker.APIKeyStats{})
	return k
}

func TestE2E_AccountPool_RecordsEquivalentCost(t *testing.T) {
	rec, admin, src, cleanup := newE2EFixture(t)
	defer cleanup()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(sseGPT54PriorityResponse))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer upstream.Close()

	pool := broker.NewPool(broker.PoolConfig{})
	pool.Add(newAccountForE2E("alice@x.com"))

	h := &ingress.ResponsesHandler{
		Pool:     pool,
		ChatGPT:  &provider.ChatGPTClient{HTTPClient: upstream.Client(), BaseURL: upstream.URL},
		Recorder: rec,
		Pricer:   src,
	}

	body := strings.NewReader(`{"model":"gpt-5.4","service_tier":"priority","input":[{"type":"message","role":"user","content":"hi"}]}`)
	req := httptest.NewRequest("POST", "/v1/responses", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	mux := http.NewServeMux()
	admin.Mount(mux)

	costReq := httptest.NewRequest("GET", "/admin/usage/cost", nil)
	costReq.Header.Set("X-Admin-Token", "test-token")
	costW := httptest.NewRecorder()
	mux.ServeHTTP(costW, costReq)
	if costW.Code != 200 {
		t.Fatalf("/admin/usage/cost status=%d body=%s", costW.Code, costW.Body.String())
	}

	var report struct {
		RealCost       float64 `json:"real_cost"`
		EquivalentCost float64 `json:"equivalent_cost"`
		ByAccount      []struct {
			ID        string  `json:"id"`
			TotalCost float64 `json:"total_cost"`
			ByModel   []struct {
				Model       string  `json:"model"`
				ServiceTier string  `json:"service_tier"`
				TotalCost   float64 `json:"total_cost"`
				Requests    int64   `json:"requests"`
			} `json:"by_model"`
		} `json:"by_account"`
	}
	if err := json.Unmarshal(costW.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v body=%s", err, costW.Body.String())
	}

	if report.RealCost != 0 {
		t.Errorf("RealCost should be 0 for account-only traffic, got %v", report.RealCost)
	}

	wantCost := float64(800)*5e-6 + float64(200)*5e-7 + float64(500)*3e-5
	if !nearly(report.EquivalentCost, wantCost) {
		t.Errorf("EquivalentCost=%v want %v (gpt-5.4 priority: 800*5e-6 input + 200*5e-7 cached + 500*3e-5 output)", report.EquivalentCost, wantCost)
	}

	if len(report.ByAccount) != 1 {
		t.Fatalf("by_account rows=%d", len(report.ByAccount))
	}
	row := report.ByAccount[0]
	if row.ID != "alice@x.com" {
		t.Errorf("account id=%q", row.ID)
	}
	if !nearly(row.TotalCost, wantCost) {
		t.Errorf("alice total=%v want %v", row.TotalCost, wantCost)
	}
	if len(row.ByModel) != 1 {
		t.Fatalf("by_model rows=%d", len(row.ByModel))
	}
	m := row.ByModel[0]
	if m.Model != "gpt-5.4" || m.ServiceTier != "priority" {
		t.Errorf("model=%q tier=%q", m.Model, m.ServiceTier)
	}
	if m.Requests != 1 {
		t.Errorf("requests=%d", m.Requests)
	}
}

func TestE2E_APIKeyPool_RecordsRealCost(t *testing.T) {
	rec, admin, src, cleanup := newE2EFixture(t)
	defer cleanup()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(sseGPT55Response))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer upstream.Close()

	keyPool := broker.NewAPIKeyPool()
	keyPool.Add(newAPIKeyForE2E("sk_e2e"))

	h := &ingress.ResponsesHandler{
		KeyPool:  keyPool,
		OpenAI:   &provider.OpenAIClient{HTTPClient: upstream.Client(), BaseURL: upstream.URL},
		Recorder: rec,
		Pricer:   src,
		Priority: "apikey-first",
	}

	body := strings.NewReader(`{"model":"gpt-5.5","input":[]}`)
	req := httptest.NewRequest("POST", "/v1/responses", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	mux := http.NewServeMux()
	admin.Mount(mux)

	costReq := httptest.NewRequest("GET", "/admin/usage/cost", nil)
	costReq.Header.Set("X-Admin-Token", "test-token")
	costW := httptest.NewRecorder()
	mux.ServeHTTP(costW, costReq)
	if costW.Code != 200 {
		t.Fatalf("status=%d body=%s", costW.Code, costW.Body.String())
	}
	var report struct {
		RealCost       float64 `json:"real_cost"`
		EquivalentCost float64 `json:"equivalent_cost"`
		ByKey          []struct {
			ID        string  `json:"id"`
			TotalCost float64 `json:"total_cost"`
			ByModel   []struct {
				Model     string  `json:"model"`
				TotalCost float64 `json:"total_cost"`
			} `json:"by_model"`
		} `json:"by_key"`
	}
	if err := json.Unmarshal(costW.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if report.EquivalentCost != 0 {
		t.Errorf("EquivalentCost should be 0 for apikey-only traffic, got %v", report.EquivalentCost)
	}

	wantCost := float64(2000)*5e-6 + float64(0) + float64(300)*3e-5
	if !nearly(report.RealCost, wantCost) {
		t.Errorf("RealCost=%v want %v (gpt-5.5 default: 2000*5e-6 + 300*3e-5)", report.RealCost, wantCost)
	}

	if len(report.ByKey) != 1 || report.ByKey[0].ID != "sk_e2e" {
		t.Fatalf("by_key=%+v", report.ByKey)
	}
	if len(report.ByKey[0].ByModel) != 1 || report.ByKey[0].ByModel[0].Model != "gpt-5.5" {
		t.Errorf("by_model: %+v", report.ByKey[0].ByModel)
	}
}

func TestE2E_PricingStatusEndpoint(t *testing.T) {
	_, admin, _, cleanup := newE2EFixture(t)
	defer cleanup()

	mux := http.NewServeMux()
	admin.Mount(mux)
	req := httptest.NewRequest("GET", "/admin/pricing/status", nil)
	req.Header.Set("X-Admin-Token", "test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var st struct {
		Origin      string `json:"origin"`
		ModelsCount int    `json:"models_count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Origin != pricing.OriginEmbedded {
		t.Errorf("origin=%q want %q", st.Origin, pricing.OriginEmbedded)
	}
	if st.ModelsCount < 50 {
		t.Errorf("models_count=%d, expected at least 50", st.ModelsCount)
	}
}

func TestE2E_UnpricedReportSurfacesUnknownModel(t *testing.T) {
	rec, admin, src, cleanup := newE2EFixture(t)
	defer cleanup()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`event: response.created
data: {"type":"response.created","response":{"id":"r","model":"gpt-future-9000"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"r","model":"gpt-future-9000","status":"completed","usage":{"input_tokens":100,"output_tokens":50,"total_tokens":150}}}

`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer upstream.Close()

	pool := broker.NewPool(broker.PoolConfig{})
	pool.Add(newAccountForE2E("alice@x.com"))

	h := &ingress.ResponsesHandler{
		Pool:     pool,
		ChatGPT:  &provider.ChatGPTClient{HTTPClient: upstream.Client(), BaseURL: upstream.URL},
		Recorder: rec,
		Pricer:   src,
	}
	body := strings.NewReader(`{"model":"gpt-future-9000","input":[]}`)
	req := httptest.NewRequest("POST", "/v1/responses", body)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}

	mux := http.NewServeMux()
	admin.Mount(mux)

	upReq := httptest.NewRequest("GET", "/admin/pricing/unpriced", nil)
	upReq.Header.Set("X-Admin-Token", "test-token")
	upW := httptest.NewRecorder()
	mux.ServeHTTP(upW, upReq)
	if upW.Code != 200 {
		t.Fatalf("unpriced status=%d body=%s", upW.Code, upW.Body.String())
	}
	var rows []struct {
		BillingKey string `json:"billing_key"`
		Count      int64  `json:"count"`
	}
	if err := json.Unmarshal(upW.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("unpriced should be non-empty")
	}
	found := false
	for _, r := range rows {
		if r.BillingKey == "gpt-future-9000" {
			found = true
			if r.Count < 1 {
				t.Errorf("count=%d", r.Count)
			}
		}
	}
	if !found {
		t.Errorf("gpt-future-9000 not in unpriced: %+v", rows)
	}
}

func TestE2E_PricingRefreshUsesLiveLiteLLMMock(t *testing.T) {
	_, admin, src, cleanup := newE2EFixture(t)
	defer cleanup()

	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"gpt-future-1": {"litellm_provider":"openai","mode":"chat","input_cost_per_token":1e-5,"output_cost_per_token":3e-5}}`)
	}))
	defer llm.Close()

	fetcher := pricing.NewFetcher(src, pricing.FetcherConfig{
		URL:        llm.URL,
		HTTPClient: llm.Client(),
	})
	admin.Pricing = &pricing.Service{Source: src, Fetcher: fetcher}

	mux := http.NewServeMux()
	admin.Mount(mux)

	refreshReq := httptest.NewRequest("POST", "/admin/pricing/refresh", nil)
	refreshReq.Header.Set("X-Admin-Token", "test-token")
	refreshW := httptest.NewRecorder()
	mux.ServeHTTP(refreshW, refreshReq)
	if refreshW.Code != 200 {
		t.Fatalf("status=%d body=%s", refreshW.Code, refreshW.Body.String())
	}

	if _, ok := src.Lookup("gpt-future-1"); !ok {
		t.Error("post-refresh source should know gpt-future-1")
	}
	statusReq := httptest.NewRequest("GET", "/admin/pricing/status", nil)
	statusReq.Header.Set("X-Admin-Token", "test-token")
	statusW := httptest.NewRecorder()
	mux.ServeHTTP(statusW, statusReq)
	var st struct {
		Origin string `json:"origin"`
	}
	_ = json.Unmarshal(statusW.Body.Bytes(), &st)
	if st.Origin != pricing.OriginLiteLLM {
		t.Errorf("origin after refresh=%q want %q", st.Origin, pricing.OriginLiteLLM)
	}
}

func nearly(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

