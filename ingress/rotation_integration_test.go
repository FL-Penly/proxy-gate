package ingress

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/codeking-ai/cligate-v2/broker"
	"github.com/codeking-ai/cligate-v2/provider"
)

func TestRotationOn429(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("ChatGPT-Account-Id")
		hits.Add(1)
		if auth == "acc-hot" {
			w.Header().Set("retry-after", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(fakeSSEStream))
	}))
	defer upstream.Close()

	pool := broker.NewPool(broker.PoolConfig{})
	hot := &broker.Account{Email: "a@x.com", AccountID: "acc-hot", PlanType: broker.PlanPro, AccessToken: "tok-hot"}
	hot.ApplyStats(broker.AccountStats{})
	cool := &broker.Account{Email: "b@x.com", AccountID: "acc-cool", PlanType: broker.PlanPro, AccessToken: "tok-cool"}
	cool.ApplyStats(broker.AccountStats{})
	pool.Add(hot)
	pool.Add(cool)

	rec := &fakeRecorder{}
	h := &ResponsesHandler{
		Pool:     pool,
		ChatGPT:  &provider.ChatGPTClient{HTTPClient: upstream.Client(), BaseURL: upstream.URL},
		Recorder: rec,
	}

	body := bytes.NewBufferString(`{"model":"gpt-5"}`)
	req := httptest.NewRequest("POST", "/v1/responses", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "PHASE_1_PASS") {
		t.Errorf("response missing payload: %s", w.Body.String())
	}
	if hits.Load() < 2 {
		t.Errorf("upstream hits = %d, want >= 2 (rotation)", hits.Load())
	}
	if hot.Stats().CooldownUntil.IsZero() {
		t.Errorf("hot account cooldown not recorded")
	}
}

func TestRotationOnRefreshFailureMarksDead(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer expired-token" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(fakeSSEStream))
	}))
	defer upstream.Close()

	pool := broker.NewPool(broker.PoolConfig{})
	expired := &broker.Account{Email: "expired@x.com", AccountID: "acc-exp", PlanType: broker.PlanPro, AccessToken: "expired-token", RefreshToken: "rt"}
	expired.ApplyStats(broker.AccountStats{})
	live := &broker.Account{Email: "live@x.com", AccountID: "acc-live", PlanType: broker.PlanPlus, AccessToken: "live-token"}
	live.ApplyStats(broker.AccountStats{})
	pool.Add(expired)
	pool.Add(live)

	rec := &fakeRecorder{}
	h := &ResponsesHandler{
		Pool:      pool,
		ChatGPT:   &provider.ChatGPTClient{HTTPClient: upstream.Client(), BaseURL: upstream.URL},
		Recorder:  rec,
		Refresher: &alwaysFailRefresher{},
	}

	body := bytes.NewBufferString(`{"model":"gpt-5"}`)
	req := httptest.NewRequest("POST", "/v1/responses", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !expired.Stats().Dead {
		t.Errorf("expired account should be marked dead after refresh failure")
	}
}

func TestPassthrough4xxNotRetried(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer upstream.Close()

	pool := broker.NewPool(broker.PoolConfig{})
	a := &broker.Account{Email: "a@x.com", AccountID: "a", PlanType: broker.PlanPro, AccessToken: "t1"}
	a.ApplyStats(broker.AccountStats{})
	b := &broker.Account{Email: "b@x.com", AccountID: "b", PlanType: broker.PlanPro, AccessToken: "t2"}
	b.ApplyStats(broker.AccountStats{})
	pool.Add(a)
	pool.Add(b)

	rec := &fakeRecorder{}
	h := &ResponsesHandler{
		Pool:     pool,
		ChatGPT:  &provider.ChatGPTClient{HTTPClient: upstream.Client(), BaseURL: upstream.URL},
		Recorder: rec,
	}

	body := bytes.NewBufferString(`{"model":"gpt-5"}`)
	req := httptest.NewRequest("POST", "/v1/responses", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400 passthrough", w.Code)
	}
	if hits.Load() != 1 {
		t.Errorf("upstream hits = %d, 4xx should not rotate", hits.Load())
	}
}

type alwaysFailRefresher struct{}

func (alwaysFailRefresher) RefreshToken(_ context.Context, _ *broker.Account) error {
	return errAuth
}

var errAuth = stringError("invalid_grant")

type stringError string

func (e stringError) Error() string { return string(e) }
