package control

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/FL-Penly/proxy-gate/store"
)

func newTestPinStore(t *testing.T) (*PinStore, func()) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "pin.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	q := NewQueue(s, WithMaxWait(20*time.Millisecond))
	return NewPinStore(s, q), func() { q.Close(); s.Close() }
}

func TestPinStorePutLookup(t *testing.T) {
	p, cleanup := newTestPinStore(t)
	defer cleanup()

	if err := p.Put("resp_1", "a@x.com", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("put: %v", err)
	}
	p.queue.Flush()

	email, ok := p.Lookup("resp_1")
	if !ok {
		t.Fatalf("lookup miss")
	}
	if email != "a@x.com" {
		t.Errorf("got %q, want a@x.com", email)
	}
}

func TestPinStoreExpired(t *testing.T) {
	p, cleanup := newTestPinStore(t)
	defer cleanup()

	if err := p.Put("resp_old", "a@x.com", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("put: %v", err)
	}
	p.queue.Flush()

	if _, ok := p.Lookup("resp_old"); ok {
		t.Errorf("expired pin should not be returned")
	}
}

func TestPinStoreUnknown(t *testing.T) {
	p, cleanup := newTestPinStore(t)
	defer cleanup()

	if _, ok := p.Lookup("never-existed"); ok {
		t.Errorf("unknown id should return ok=false")
	}
}
