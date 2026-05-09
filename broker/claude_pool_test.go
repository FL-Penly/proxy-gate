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
