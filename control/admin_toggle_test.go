package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/FL-Penly/proxy-gate/broker"
	"github.com/FL-Penly/proxy-gate/store"
)

func newTestAdmin(t *testing.T) (*AdminAPI, *store.Store, *Queue, *http.ServeMux) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "admin.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	q := NewQueue(st, WithBatchSize(1), WithMaxWait(time.Millisecond))
	pool := broker.NewPool(broker.PoolConfig{})
	keyPool := broker.NewAPIKeyPool()
	a := &AdminAPI{
		Pool:    pool,
		KeyPool: keyPool,
		Queue:   q,
		Token:   "test-token",
	}
	mux := http.NewServeMux()
	a.Mount(mux)
	return a, st, q, mux
}

func TestToggleKeyPersistsDisabled(t *testing.T) {
	a, st, q, mux := newTestAdmin(t)
	defer st.Close()
	defer q.Close()

	k := &broker.APIKey{ID: "k_a", Type: broker.KeyTypeOpenAI, APIKey: "sk-test"}
	k.ApplyStats(broker.APIKeyStats{})
	a.KeyPool.Add(k)

	req := httptest.NewRequest(http.MethodPost, "/admin/keys/k_a/disable", nil)
	req.Header.Set("X-Admin-Token", a.Token)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !k.Stats().Disabled {
		t.Fatalf("in-memory not disabled")
	}

	q.Flush()
	raw, err := st.Get(store.BucketAPIKeys, "stats:k_a")
	if err != nil {
		t.Fatalf("read bucket: %v", err)
	}
	var got broker.APIKeyStats
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Disabled {
		t.Errorf("persisted Disabled=false, want true")
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/keys/k_a/enable", nil)
	req.Header.Set("X-Admin-Token", a.Token)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("enable status=%d", rr.Code)
	}
	q.Flush()
	raw, _ = st.Get(store.BucketAPIKeys, "stats:k_a")
	var got2 broker.APIKeyStats
	_ = json.Unmarshal(raw, &got2)
	if got2.Disabled {
		t.Errorf("after enable, persisted Disabled=true, want false")
	}
}

func TestToggleAccountPersistsDisabled(t *testing.T) {
	a, st, q, mux := newTestAdmin(t)
	defer st.Close()
	defer q.Close()

	acc := &broker.Account{Email: "u@x.com", AccessToken: "t"}
	acc.ApplyStats(broker.AccountStats{})
	a.Pool.Add(acc)

	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/u@x.com/disable", nil)
	req.Header.Set("X-Admin-Token", a.Token)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	q.Flush()
	raw, err := st.Get(store.BucketAccounts, "stats:u@x.com")
	if err != nil {
		t.Fatalf("read bucket: %v", err)
	}
	var got broker.AccountStats
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Disabled {
		t.Errorf("persisted Disabled=false, want true")
	}
}

func TestToggleKeyDisabledSurvivesPoolReload(t *testing.T) {
	a, st, q, mux := newTestAdmin(t)
	defer st.Close()
	defer q.Close()

	k := &broker.APIKey{ID: "k_b", Type: broker.KeyTypeOpenAI, APIKey: "sk-x"}
	k.ApplyStats(broker.APIKeyStats{})
	a.KeyPool.Add(k)

	req := httptest.NewRequest(http.MethodPost, "/admin/keys/k_b/disable", nil)
	req.Header.Set("X-Admin-Token", a.Token)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	q.Flush()

	pool2 := broker.NewAPIKeyPool()
	k2 := &broker.APIKey{ID: "k_b", Type: broker.KeyTypeOpenAI, APIKey: "sk-x"}
	k2.ApplyStats(broker.APIKeyStats{})
	pool2.Add(k2)

	raw, err := st.Get(store.BucketAPIKeys, "stats:k_b")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var s broker.APIKeyStats
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	k2.ApplyStats(s)

	if !k2.Stats().Disabled {
		t.Errorf("after reload, Disabled=false, want true")
	}
	if k2.IsAvailable(time.Now()) {
		t.Errorf("disabled key should not be available")
	}
}

func TestDisabledKeyExcludedFromLease(t *testing.T) {
	a, st, q, mux := newTestAdmin(t)
	defer st.Close()
	defer q.Close()

	k1 := &broker.APIKey{ID: "k1", Type: broker.KeyTypeOpenAI, APIKey: "sk-1"}
	k1.ApplyStats(broker.APIKeyStats{})
	k2 := &broker.APIKey{ID: "k2", Type: broker.KeyTypeOpenAI, APIKey: "sk-2"}
	k2.ApplyStats(broker.APIKeyStats{})
	a.KeyPool.Add(k1)
	a.KeyPool.Add(k2)

	req := httptest.NewRequest(http.MethodPost, "/admin/keys/k1/disable", nil)
	req.Header.Set("X-Admin-Token", a.Token)
	mux.ServeHTTP(httptest.NewRecorder(), req)

	for i := range 8 {
		lease, err := a.KeyPool.Lease(context.Background(), []broker.APIKeyType{broker.KeyTypeOpenAI})
		if err != nil {
			t.Fatalf("lease #%d: %v", i, err)
		}
		if lease.Key.ID == "k1" {
			lease.Release()
			t.Fatalf("disabled key k1 was leased on iteration %d", i)
		}
		lease.Release()
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/keys/k2/disable", nil)
	req.Header.Set("X-Admin-Token", a.Token)
	mux.ServeHTTP(httptest.NewRecorder(), req)

	if _, err := a.KeyPool.Lease(context.Background(), []broker.APIKeyType{broker.KeyTypeOpenAI}); err != broker.ErrAllExhausted {
		t.Fatalf("with both disabled, want ErrAllExhausted, got %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/keys/k2/enable", nil)
	req.Header.Set("X-Admin-Token", a.Token)
	mux.ServeHTTP(httptest.NewRecorder(), req)

	lease, err := a.KeyPool.Lease(context.Background(), []broker.APIKeyType{broker.KeyTypeOpenAI})
	if err != nil {
		t.Fatalf("after re-enable: %v", err)
	}
	if lease.Key.ID != "k2" {
		t.Errorf("after re-enable, leased %q, want k2", lease.Key.ID)
	}
	lease.Release()
}

func TestDisabledAccountExcludedFromLease(t *testing.T) {
	a, st, q, mux := newTestAdmin(t)
	defer st.Close()
	defer q.Close()

	a1 := &broker.Account{Email: "a@x.com", AccessToken: "t1", PlanType: broker.PlanPro}
	a1.ApplyStats(broker.AccountStats{})
	a2 := &broker.Account{Email: "b@x.com", AccessToken: "t2", PlanType: broker.PlanPro}
	a2.ApplyStats(broker.AccountStats{})
	a.Pool.Add(a1)
	a.Pool.Add(a2)

	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/a@x.com/disable", nil)
	req.Header.Set("X-Admin-Token", a.Token)
	mux.ServeHTTP(httptest.NewRecorder(), req)

	for i := range 8 {
		lease, err := a.Pool.Lease(context.Background(), broker.LeaseHint{})
		if err != nil {
			t.Fatalf("lease #%d: %v", i, err)
		}
		if lease.Account.Email == "a@x.com" {
			lease.Release()
			t.Fatalf("disabled account was leased on iteration %d", i)
		}
		lease.Release()
	}
}
