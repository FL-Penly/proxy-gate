package control

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/FL-Penly/proxy-gate/store"
)

func TestQueueBatching(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "q.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	q := NewQueue(s, WithBatchSize(50), WithMaxWait(20*time.Millisecond))
	defer q.Close()

	for i := 0; i < 200; i++ {
		k := []byte{byte(i), byte(i >> 8)}
		if err := q.Put(store.BucketUsage, string(k), []byte("x")); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	q.Flush()

	count := 0
	if err := s.ForEach(store.BucketUsage, func(_, _ []byte) error {
		count++
		return nil
	}); err != nil {
		t.Fatalf("foreach: %v", err)
	}
	if count != 200 {
		t.Fatalf("persisted %d, want 200", count)
	}
}

func TestQueueClose(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "q.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	q := NewQueue(s)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = q.Put(store.BucketUsage, string([]byte{byte(i)}), []byte("y"))
		}(i)
	}
	wg.Wait()
	q.Close()
	if err := q.Put(store.BucketUsage, "after-close", []byte("z")); err != ErrQueueClosed {
		t.Fatalf("put after close: want ErrQueueClosed, got %v", err)
	}
}
