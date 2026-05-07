package broker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchAddsNewFile(t *testing.T) {
	dir := t.TempDir()
	pool := NewPool(PoolConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.WatchDir(ctx, dir, nil); err != nil {
		t.Fatalf("WatchDir: %v", err)
	}

	json := `{"email":"new@example.com","account_id":"acc-new","plan_type":"plus","access_token":"t","refresh_token":"r","expires_at":"2099-12-31T00:00:00Z","created_at":"2026-04-26T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "new.json"), []byte(json), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := pool.Get("new@example.com"); ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("account not added after fsnotify create")
}

func TestWatchRemovesDeletedFile(t *testing.T) {
	dir := t.TempDir()
	json := `{"email":"gone@example.com","account_id":"acc","plan_type":"plus","access_token":"t","refresh_token":"r","expires_at":"2099-12-31T00:00:00Z","created_at":"2026-04-26T00:00:00Z"}`
	path := filepath.Join(dir, "gone.json")
	if err := os.WriteFile(path, []byte(json), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	pool := NewPool(PoolConfig{})
	if err := pool.LoadDir(dir); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if _, ok := pool.Get("gone@example.com"); !ok {
		t.Fatalf("setup: account not loaded")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.WatchDir(ctx, dir, nil); err != nil {
		t.Fatalf("WatchDir: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := pool.Get("gone@example.com"); !ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("account not removed after fsnotify delete")
}
