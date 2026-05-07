package control

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/codeking-ai/cligate-v2/broker"
	"github.com/codeking-ai/cligate-v2/ingress"
	"github.com/codeking-ai/cligate-v2/store"
)

func newTestRecorder(t *testing.T) (*Recorder, *Queue, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "rec.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	q := NewQueue(s, WithBatchSize(1), WithMaxWait(time.Millisecond))
	pool := broker.NewPool(broker.PoolConfig{})
	rec := NewRecorder(s, q, pool)
	return rec, q, s
}

func TestRecorderTracksPerAccount(t *testing.T) {
	rec, q, s := newTestRecorder(t)
	defer s.Close()
	defer q.Close()

	rec.RecordRequest(ingress.UsageRecord{Account: "alice@x.com", InputTokens: 100, OutputTokens: 10, TotalTokens: 110, Success: true})
	rec.RecordRequest(ingress.UsageRecord{Account: "alice@x.com", InputTokens: 200, OutputTokens: 20, TotalTokens: 220, Success: true})
	rec.RecordRequest(ingress.UsageRecord{Account: "bob@x.com", InputTokens: 50, OutputTokens: 5, TotalTokens: 55, Success: true})
	rec.RecordRequest(ingress.UsageRecord{Account: "bob@x.com", InputTokens: 30, OutputTokens: 0, TotalTokens: 30, Success: false})

	by := rec.TodayByAccount()
	if a, ok := by["alice@x.com"]; !ok {
		t.Fatalf("alice missing")
	} else {
		if a.Requests != 2 || a.InputTokens != 300 || a.OutputTokens != 30 || a.Errors != 0 {
			t.Errorf("alice: %+v", a)
		}
	}
	if b, ok := by["bob@x.com"]; !ok {
		t.Fatalf("bob missing")
	} else {
		if b.Requests != 2 || b.InputTokens != 80 || b.Errors != 1 {
			t.Errorf("bob: %+v", b)
		}
	}

	if t1 := rec.Today(); t1.Requests != 4 || t1.InputTokens != 380 || t1.Errors != 1 {
		t.Errorf("global today: %+v", t1)
	}
}

func TestRecorderTracksPerKey(t *testing.T) {
	rec, q, s := newTestRecorder(t)
	defer s.Close()
	defer q.Close()

	rec.RecordRequest(ingress.UsageRecord{KeyID: "k_a", InputTokens: 5, TotalTokens: 5, Success: true})
	rec.RecordRequest(ingress.UsageRecord{KeyID: "k_a", InputTokens: 7, TotalTokens: 7, Success: true})

	by := rec.TodayByKey()
	if d, ok := by["k_a"]; !ok || d.Requests != 2 || d.InputTokens != 12 {
		t.Errorf("by_key k_a: ok=%v %+v", ok, d)
	}
}

func TestRecorderPersistsPerAccountAcrossInstance(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rec.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	q := NewQueue(st, WithBatchSize(1), WithMaxWait(time.Millisecond))
	pool := broker.NewPool(broker.PoolConfig{})
	rec1 := NewRecorder(st, q, pool)

	rec1.RecordRequest(ingress.UsageRecord{Account: "alice@x.com", InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100, Success: true})
	q.Flush()
	q.Close()
	st.Close()

	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	q2 := NewQueue(st2)
	defer q2.Close()
	rec2 := NewRecorder(st2, q2, pool)

	by := rec2.TodayByAccount()
	a, ok := by["alice@x.com"]
	if !ok {
		t.Fatalf("alice missing after restart, got=%+v", by)
	}
	if a.Requests != 1 || a.InputTokens != 1000 {
		t.Errorf("after restart: %+v", a)
	}
}

func TestRecorderBothAccountAndKeyOnSameRequest(t *testing.T) {
	rec, q, s := newTestRecorder(t)
	defer s.Close()
	defer q.Close()

	rec.RecordRequest(ingress.UsageRecord{Account: "a@x.com", KeyID: "k_a", InputTokens: 10, TotalTokens: 10, Success: true})

	if a, ok := rec.TodayByAccount()["a@x.com"]; !ok || a.Requests != 1 {
		t.Errorf("account side: %+v ok=%v", a, ok)
	}
	if k, ok := rec.TodayByKey()["k_a"]; !ok || k.Requests != 1 {
		t.Errorf("key side: %+v ok=%v", k, ok)
	}
}
