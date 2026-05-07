package control

import (
	"encoding/json"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/codeking-ai/cligate-v2/broker"
	"github.com/codeking-ai/cligate-v2/ingress"
	"github.com/codeking-ai/cligate-v2/store"
)

func costEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestRecorderAggregatesCostByBilling(t *testing.T) {
	rec, q, s := newTestRecorder(t)
	defer s.Close()
	defer q.Close()

	rec.RecordRequest(ingress.UsageRecord{
		Account: "alice@x.com", Model: "gpt-5.4", ServiceTier: "priority",
		InputTokens: 1000, OutputTokens: 500, Cost: 0.0125, Success: true,
	})
	rec.RecordRequest(ingress.UsageRecord{
		Account: "alice@x.com", Model: "gpt-5.4", ServiceTier: "priority",
		InputTokens: 2000, OutputTokens: 1000, Cost: 0.025, Success: true,
	})
	rec.RecordRequest(ingress.UsageRecord{
		Account: "alice@x.com", Model: "gpt-5.4-mini", ServiceTier: "",
		InputTokens: 500, OutputTokens: 100, Cost: 0.001, Success: true,
	})

	by := rec.TodayByAccountBilling()
	alice, ok := by["alice@x.com"]
	if !ok {
		t.Fatalf("alice missing from billing aggregation: %+v", by)
	}
	if len(alice) != 2 {
		t.Errorf("expected 2 billing keys, got %d", len(alice))
	}

	priority, ok := alice["gpt-5.4@priority"]
	if !ok {
		t.Fatalf("gpt-5.4@priority missing: %+v", alice)
	}
	if priority.Requests != 2 {
		t.Errorf("priority requests=%d want 2", priority.Requests)
	}
	if !costEq(priority.TotalCost, 0.0375) {
		t.Errorf("priority cost=%v want 0.0375", priority.TotalCost)
	}
	if priority.Model != "gpt-5.4" || priority.ServiceTier != "priority" {
		t.Errorf("priority model/tier mismatch: %+v", priority)
	}

	mini, ok := alice["gpt-5.4-mini"]
	if !ok {
		t.Fatalf("gpt-5.4-mini missing")
	}
	if mini.Requests != 1 || !costEq(mini.TotalCost, 0.001) {
		t.Errorf("mini: %+v", mini)
	}
}

func TestRecorderTracksUnpriced(t *testing.T) {
	rec, q, s := newTestRecorder(t)
	defer s.Close()
	defer q.Close()

	rec.RecordRequest(ingress.UsageRecord{
		Account: "alice@x.com", Model: "gpt-future-9", InputTokens: 100, Success: true,
		CostUnpriced: true,
	})
	day := rec.Today()
	if day.Unpriced != 1 {
		t.Errorf("unpriced count=%d want 1", day.Unpriced)
	}
	by := rec.TodayByAccountBilling()
	row := by["alice@x.com"]["gpt-future-9"]
	if row.Unpriced != 1 {
		t.Errorf("per-billing unpriced=%d want 1", row.Unpriced)
	}
}

func TestRecorderCostPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rec.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	q := NewQueue(st, WithBatchSize(1), WithMaxWait(time.Millisecond))
	pool := broker.NewPool(broker.PoolConfig{})
	rec1 := NewRecorder(st, q, pool)
	rec1.RecordRequest(ingress.UsageRecord{
		KeyID: "sk_a", Model: "gpt-5", ServiceTier: "priority",
		InputTokens: 1_000_000, OutputTokens: 100_000,
		Cost: 4.50, Success: true,
	})
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

	by := rec2.TodayByKeyBilling()
	row, ok := by["sk_a"]["gpt-5@priority"]
	if !ok {
		t.Fatalf("billing data lost across restart: %+v", by)
	}
	if !costEq(row.TotalCost, 4.50) {
		t.Errorf("cost=%v want 4.50", row.TotalCost)
	}
	if row.InputTokens != 1_000_000 {
		t.Errorf("input=%d want 1_000_000", row.InputTokens)
	}
}

func TestRecorderEmptyBillingKeyDoesNotPersist(t *testing.T) {
	rec, q, s := newTestRecorder(t)
	defer s.Close()
	defer q.Close()

	rec.RecordRequest(ingress.UsageRecord{
		KeyID: "sk_a", Model: "", InputTokens: 100, Success: true,
	})
	rec.RecordRequest(ingress.UsageRecord{
		KeyID: "sk_a", Model: "unknown", InputTokens: 100, Success: true,
	})
	by := rec.TodayByKeyBilling()
	if bucket, ok := by["sk_a"]; ok && len(bucket) > 0 {
		t.Errorf("empty/unknown model should not aggregate billing rows: %+v", bucket)
	}
}

func TestRecorderTierNormalizationConsistent(t *testing.T) {
	rec, q, s := newTestRecorder(t)
	defer s.Close()
	defer q.Close()

	for _, tier := range []string{"priority", "PRIORITY", " Priority ", "  PrioRity"} {
		rec.RecordRequest(ingress.UsageRecord{
			KeyID: "sk_a", Model: "gpt-5.4", ServiceTier: tier,
			InputTokens: 100, OutputTokens: 50, Cost: 0.001, Success: true,
		})
	}

	by := rec.TodayByKeyBilling()
	bucket := by["sk_a"]
	if len(bucket) != 1 {
		t.Errorf("variant tier casings must collapse to one bucket, got %d: %+v", len(bucket), bucket)
	}
	row, ok := bucket["gpt-5.4@priority"]
	if !ok {
		t.Fatalf("normalized billing key gpt-5.4@priority missing: %+v", bucket)
	}
	if row.Requests != 4 {
		t.Errorf("requests=%d want 4", row.Requests)
	}
}

func TestRecorderCostReadFromBoltMatchesWritten(t *testing.T) {
	rec, q, s := newTestRecorder(t)
	defer s.Close()
	defer q.Close()

	rec.RecordRequest(ingress.UsageRecord{
		Account: "alice@x.com", Model: "gpt-5.5", ServiceTier: "priority",
		InputTokens: 100_000, OutputTokens: 10_000,
		Cost: 1.6, Success: true,
	})
	q.Flush()

	raw, err := s.Get(store.BucketUsage, "day:"+time.Now().Format("2006-01-02")+":acc:alice@x.com:bk:gpt-5.5@priority")
	if err != nil {
		t.Fatalf("bbolt read: %v", err)
	}
	var d BillingDailyUsage
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("json: %v", err)
	}
	if d.Model != "gpt-5.5" || d.ServiceTier != "priority" {
		t.Errorf("model/tier: %+v", d)
	}
	if !costEq(d.TotalCost, 1.6) {
		t.Errorf("cost=%v want 1.6", d.TotalCost)
	}
}
