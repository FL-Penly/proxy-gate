package broker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClaudePoolLoadSaveLease(t *testing.T) {
	dir := t.TempDir()
	acc := &ClaudeAccount{Email: "b@example.com", AccessToken: "tok-b", RefreshToken: "rt", CreatedAt: time.Now().UTC()}
	path, err := SaveClaudeAccountFile(dir, acc)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o want 600", info.Mode().Perm())
	}
	if err := os.WriteFile(filepath.Join(dir, "ignore.txt"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write ignore: %v", err)
	}

	pool := NewClaudePool()
	if err := pool.LoadDir(dir); err != nil {
		t.Fatalf("load: %v", err)
	}
	if pool.Len() != 1 {
		t.Fatalf("len=%d want 1", pool.Len())
	}
	lease, err := pool.Lease(context.Background())
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if lease.Account.Email != acc.Email {
		t.Fatalf("leased %q", lease.Account.Email)
	}
	lease.Release()
}

func TestSaveClaudeAccountFileTightensExistingPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a_example.com.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := SaveClaudeAccountFile(dir, &ClaudeAccount{Email: "a@example.com", AccessToken: "tok"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o want 600", info.Mode().Perm())
	}
}

func TestClaudePoolAddPreservesRuntimeStats(t *testing.T) {
	pool := NewClaudePool()
	old := &ClaudeAccount{Email: "a@example.com", AccessToken: "old"}
	old.ApplyStats(ClaudeAccountStats{Disabled: true, TotalInputTkn: 12})
	pool.Add(old)
	pool.Add(&ClaudeAccount{Email: "a@example.com", AccessToken: "new"})
	got, ok := pool.Get("a@example.com")
	if !ok {
		t.Fatalf("missing account")
	}
	if got.AccessToken != "new" || !got.Stats().Disabled || got.Stats().TotalInputTkn != 12 {
		t.Fatalf("account=%+v stats=%+v", got, got.Stats())
	}
}

func TestClaudePoolAvailabilityAndOrdering(t *testing.T) {
	pool := NewClaudePool()
	a := &ClaudeAccount{Email: "a@example.com", AccessToken: "tok-a"}
	a.ApplyStats(ClaudeAccountStats{TotalInputTkn: 100})
	b := &ClaudeAccount{Email: "b@example.com", AccessToken: "tok-b"}
	b.ApplyStats(ClaudeAccountStats{})
	c := &ClaudeAccount{Email: "c@example.com", AccessToken: "tok-c"}
	c.ApplyStats(ClaudeAccountStats{Disabled: true})
	pool.Add(a)
	pool.Add(b)
	pool.Add(c)

	lease, err := pool.Lease(context.Background())
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if lease.Account.Email != "b@example.com" {
		t.Fatalf("leased %q, want lowest token load b", lease.Account.Email)
	}
	lease.Release()

	b.MarkCooldown(time.Now().Add(time.Minute))
	lease, err = pool.Lease(context.Background())
	if err != nil {
		t.Fatalf("lease after cooldown: %v", err)
	}
	if lease.Account.Email != "a@example.com" {
		t.Fatalf("leased %q, want a", lease.Account.Email)
	}
	lease.Release()
	a.MarkDead("auth")
	if _, err := pool.Lease(context.Background()); err != ErrAllExhausted {
		t.Fatalf("want ErrAllExhausted, got %v", err)
	}
	if d := pool.NearestCooldown(time.Now()); d <= 0 {
		t.Fatalf("nearest cooldown = %v", d)
	}
}

func TestClaudePoolUsageAwarePrefersQuotaRemaining(t *testing.T) {
	pool := NewClaudePool()
	now := time.Now()
	a := &ClaudeAccount{Email: "a@example.com", AccessToken: "tok-a"}
	a.ApplyStats(ClaudeAccountStats{TotalInputTkn: 10_000, PrimaryUsedPct: 0.10, SecondaryUsedPct: 0.10, LastUsageAt: now})
	b := &ClaudeAccount{Email: "b@example.com", AccessToken: "tok-b"}
	b.ApplyStats(ClaudeAccountStats{PrimaryUsedPct: 0.80, SecondaryUsedPct: 0.20, LastUsageAt: now})
	pool.Add(a)
	pool.Add(b)

	lease, err := pool.Lease(context.Background())
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	defer lease.Release()
	if lease.Account.Email != "a@example.com" {
		t.Fatalf("leased %q, want account with more quota remaining", lease.Account.Email)
	}
}

func TestClaudePoolUsageAwarePenalizesInflight(t *testing.T) {
	pool := NewClaudePool()
	now := time.Now()
	a := &ClaudeAccount{Email: "a@example.com", AccessToken: "tok-a"}
	a.ApplyStats(ClaudeAccountStats{PrimaryUsedPct: 0.20, SecondaryUsedPct: 0.20, LastUsageAt: now})
	b := &ClaudeAccount{Email: "b@example.com", AccessToken: "tok-b"}
	b.ApplyStats(ClaudeAccountStats{PrimaryUsedPct: 0.20, SecondaryUsedPct: 0.20, LastUsageAt: now})
	pool.Add(a)
	pool.Add(b)

	first, err := pool.Lease(context.Background())
	if err != nil {
		t.Fatalf("first lease: %v", err)
	}
	defer first.Release()
	if first.Account.Email != "a@example.com" {
		t.Fatalf("first leased %q, want a by email tiebreak", first.Account.Email)
	}
	second, err := pool.Lease(context.Background())
	if err != nil {
		t.Fatalf("second lease: %v", err)
	}
	defer second.Release()
	if second.Account.Email != "b@example.com" {
		t.Fatalf("second leased %q, want lower inflight b", second.Account.Email)
	}
}

func TestClaudePoolProtectsNearlyExhaustedUsageWindows(t *testing.T) {
	pool := NewClaudePool()
	now := time.Now()
	a := &ClaudeAccount{Email: "a@example.com", AccessToken: "tok-a"}
	a.ApplyStats(ClaudeAccountStats{PrimaryUsedPct: 1.0, SecondaryUsedPct: 0.10, LastUsageAt: now})
	b := &ClaudeAccount{Email: "b@example.com", AccessToken: "tok-b"}
	b.ApplyStats(ClaudeAccountStats{PrimaryUsedPct: 0.60, SecondaryUsedPct: 0.60, LastUsageAt: now})
	pool.Add(a)
	pool.Add(b)

	lease, err := pool.Lease(context.Background())
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	defer lease.Release()
	if lease.Account.Email != "b@example.com" {
		t.Fatalf("leased %q, want account below protection threshold", lease.Account.Email)
	}
}

func TestClaudePoolFallsBackWhenAllUsageWindowsProtected(t *testing.T) {
	pool := NewClaudePool()
	now := time.Now()
	a := &ClaudeAccount{Email: "a@example.com", AccessToken: "tok-a"}
	a.ApplyStats(ClaudeAccountStats{PrimaryUsedPct: 1.0, SecondaryUsedPct: 0.80, LastUsageAt: now})
	b := &ClaudeAccount{Email: "b@example.com", AccessToken: "tok-b"}
	b.ApplyStats(ClaudeAccountStats{PrimaryUsedPct: 1.0, SecondaryUsedPct: 0.99, LastUsageAt: now})
	pool.Add(a)
	pool.Add(b)

	lease, err := pool.Lease(context.Background())
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	defer lease.Release()
	if lease.Account.Email != "a@example.com" {
		t.Fatalf("leased %q, want best scored account when all are protected", lease.Account.Email)
	}
}

func TestClaudePoolKnownHealthyUsageBeatsUnknownUsage(t *testing.T) {
	pool := NewClaudePool()
	now := time.Now()
	a := &ClaudeAccount{Email: "a@example.com", AccessToken: "tok-a"}
	a.ApplyStats(ClaudeAccountStats{TotalInputTkn: 10_000, PrimaryUsedPct: 0.60, SecondaryUsedPct: 0.60, LastUsageAt: now})
	b := &ClaudeAccount{Email: "b@example.com", AccessToken: "tok-b"}
	b.ApplyStats(ClaudeAccountStats{})
	pool.Add(a)
	pool.Add(b)

	lease, err := pool.Lease(context.Background())
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	defer lease.Release()
	if lease.Account.Email != "a@example.com" {
		t.Fatalf("leased %q, want known healthy usage account", lease.Account.Email)
	}
}

func TestClaudePoolUnknownUsageBeatsKnownProtectedUsage(t *testing.T) {
	pool := NewClaudePool()
	now := time.Now()
	a := &ClaudeAccount{Email: "a@example.com", AccessToken: "tok-a"}
	a.ApplyStats(ClaudeAccountStats{PrimaryUsedPct: 1.0, SecondaryUsedPct: 0.60, LastUsageAt: now})
	b := &ClaudeAccount{Email: "b@example.com", AccessToken: "tok-b"}
	b.ApplyStats(ClaudeAccountStats{})
	pool.Add(a)
	pool.Add(b)

	lease, err := pool.Lease(context.Background())
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	defer lease.Release()
	if lease.Account.Email != "b@example.com" {
		t.Fatalf("leased %q, want unknown account before protected usage account", lease.Account.Email)
	}
}

func TestClaudePoolStaleUsageFallsBackToTokenLoad(t *testing.T) {
	pool := NewClaudePool()
	stale := time.Now().Add(-claudeUsageFreshDuration - time.Minute)
	a := &ClaudeAccount{Email: "a@example.com", AccessToken: "tok-a"}
	a.ApplyStats(ClaudeAccountStats{TotalInputTkn: 10_000, PrimaryUsedPct: 0.10, SecondaryUsedPct: 0.10, LastUsageAt: stale})
	b := &ClaudeAccount{Email: "b@example.com", AccessToken: "tok-b"}
	b.ApplyStats(ClaudeAccountStats{TotalInputTkn: 1})
	pool.Add(a)
	pool.Add(b)

	lease, err := pool.Lease(context.Background())
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	defer lease.Release()
	if lease.Account.Email != "b@example.com" {
		t.Fatalf("leased %q, want token-load fallback for stale usage", lease.Account.Email)
	}
}

func TestClaudePoolReleaseAfterReloadDoesNotMakeInflightNegative(t *testing.T) {
	pool := NewClaudePool()
	acc := &ClaudeAccount{Email: "a@example.com", AccessToken: "tok-a"}
	pool.Add(acc)
	lease, err := pool.Lease(context.Background())
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	pool.Add(&ClaudeAccount{Email: "a@example.com", AccessToken: "tok-new"})
	pool.mu.RLock()
	inflightBeforeRelease := pool.accounts["a@example.com"].inflight.Load()
	pool.mu.RUnlock()
	if inflightBeforeRelease != 1 {
		t.Fatalf("inflight before release=%d, want active lease preserved", inflightBeforeRelease)
	}
	lease.Release()

	pool.mu.RLock()
	got := pool.accounts["a@example.com"].inflight.Load()
	pool.mu.RUnlock()
	if got != 0 {
		t.Fatalf("inflight=%d, want 0", got)
	}
}

func TestLoadClaudeAccountRejectsMissingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`{"email":"x@example.com"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadClaudeAccountFile(path); err == nil {
		t.Fatalf("expected missing access_token error")
	}
}
