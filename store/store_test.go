package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.Put(BucketSettings, "k", []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.Get(BucketSettings, "k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("got %q want %q", got, "v")
	}
	if err := s.Delete(BucketSettings, "k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(BucketSettings, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("post-delete get: want ErrNotFound, got %v", err)
	}
}

func TestStoreForEach(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	for _, k := range []string{"a", "b", "c"} {
		if err := s.Put(BucketAccounts, k, []byte(k)); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	var seen []string
	err = s.ForEach(BucketAccounts, func(key, _ []byte) error {
		seen = append(seen, string(key))
		return nil
	})
	if err != nil {
		t.Fatalf("foreach: %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("got %d keys, want 3", len(seen))
	}
}
