package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FL-Penly/proxy-gate/broker"
	"github.com/FL-Penly/proxy-gate/provider"
)

func newWhamAccount() *broker.Account {
	a := &broker.Account{
		Email:       "u@x.com",
		AccountID:   "acc",
		PlanType:    broker.PlanPro,
		AccessToken: "t",
	}
	a.ApplyStats(broker.AccountStats{})
	return a
}

func TestWhamPollerUpdatesUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"plan_type":"pro",
			"rate_limit":{
				"limit_reached":false,
				"primary_window":{"used_percent":42.0,"reset_after_seconds":900},
				"secondary_window":{"used_percent":75.0,"reset_after_seconds":3600}
			}
		}`))
	}))
	defer upstream.Close()

	pool := broker.NewPool(broker.PoolConfig{})
	acc := newWhamAccount()
	pool.Add(acc)

	wp := &WhamPoller{
		Pool: pool,
		Client: &provider.ChatGPTClient{
			HTTPClient: upstream.Client(),
			BaseURL:    upstream.URL + "/responses",
			UsageURL:   upstream.URL + "/wham/usage",
		},
		Interval: time.Hour,
	}

	wp.Start(context.Background())
	defer wp.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s := acc.Stats()
		if s.PrimaryUsedPct == 0.42 && s.SecondaryUsedPct == 0.75 {
			if s.LastWhamAt.IsZero() {
				t.Fatalf("LastWhamAt not set after success")
			}
			if s.WhamFailCount != 0 {
				t.Errorf("WhamFailCount=%d after success, want 0", s.WhamFailCount)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("usage not updated: stats=%+v", acc.Stats())
}

func TestWhamPollerLimitReachedSetsCooldown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"plan_type":"team",
			"rate_limit":{
				"limit_reached":true,
				"primary_window":{"used_percent":0,"reset_after_seconds":900},
				"secondary_window":{"used_percent":100,"reset_after_seconds":7200}
			}
		}`))
	}))
	defer upstream.Close()

	pool := broker.NewPool(broker.PoolConfig{})
	acc := newWhamAccount()
	pool.Add(acc)

	wp := &WhamPoller{
		Pool: pool,
		Client: &provider.ChatGPTClient{
			HTTPClient: upstream.Client(),
			BaseURL:    upstream.URL + "/responses",
			UsageURL:   upstream.URL + "/wham/usage",
		},
		Interval: time.Hour,
	}
	wp.Start(context.Background())
	defer wp.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s := acc.Stats()
		if s.WhamLimitReached {
			if !s.CooldownUntil.After(time.Now()) {
				t.Fatalf("CooldownUntil should be in the future, got %v", s.CooldownUntil)
			}
			if !s.SecondaryResetAt.Equal(s.CooldownUntil) {
				t.Errorf("CooldownUntil should equal SecondaryResetAt: cd=%v reset=%v", s.CooldownUntil, s.SecondaryResetAt)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("limit_reached not honored: stats=%+v", acc.Stats())
}

func TestWhamPollerMarksFailureOnError(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	pool := broker.NewPool(broker.PoolConfig{})
	acc := newWhamAccount()
	pool.Add(acc)

	wp := &WhamPoller{
		Pool: pool,
		Client: &provider.ChatGPTClient{
			HTTPClient: upstream.Client(),
			BaseURL:    upstream.URL + "/responses",
			UsageURL:   upstream.URL + "/wham/usage",
		},
		Interval: time.Hour,
	}
	wp.Start(context.Background())
	defer wp.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s := acc.Stats()
		if s.WhamFailCount > 0 && s.LastWhamErr != "" {
			if s.LastWhamErrAt.IsZero() {
				t.Errorf("LastWhamErrAt not set")
			}
			if s.PrimaryUsedPct != 0 {
				t.Errorf("failure should not change PrimaryUsedPct")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("failure not recorded after %d upstream calls: stats=%+v", calls.Load(), acc.Stats())
}
