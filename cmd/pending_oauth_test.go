package cmd

import (
	"strings"
	"testing"
	"time"
)

func TestPendingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := &PendingOAuth{
		Verifier:    "verifier-abc",
		State:       "state-xyz",
		RedirectURI: "http://localhost:1461/callback",
		CreatedAt:   time.Now(),
	}
	if err := WritePending(dir, p); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPending(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verifier != p.Verifier || got.State != p.State || got.RedirectURI != p.RedirectURI {
		t.Fatalf("fields mismatch: got %+v", got)
	}
}

func TestPendingExpired(t *testing.T) {
	dir := t.TempDir()
	p := &PendingOAuth{
		Verifier:  "v",
		State:     "s",
		CreatedAt: time.Now().Add(-11 * time.Minute),
	}
	if err := WritePending(dir, p); err != nil {
		t.Fatal(err)
	}
	_, err := ReadPending(dir)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired error, got %v", err)
	}
}

func TestPendingNotExpired(t *testing.T) {
	dir := t.TempDir()
	p := &PendingOAuth{
		Verifier:  "v",
		State:     "s",
		CreatedAt: time.Now().Add(-5 * time.Minute),
	}
	if err := WritePending(dir, p); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPending(dir); err != nil {
		t.Fatal(err)
	}
}

func TestReadPendingMissing(t *testing.T) {
	_, err := ReadPending(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "no pending") {
		t.Fatalf("expected no pending error, got %v", err)
	}
}

func TestDeletePending(t *testing.T) {
	dir := t.TempDir()
	p := &PendingOAuth{Verifier: "v", State: "s", CreatedAt: time.Now()}
	if err := WritePending(dir, p); err != nil {
		t.Fatal(err)
	}
	if err := DeletePending(dir); err != nil {
		t.Fatal(err)
	}
	_, err := ReadPending(dir)
	if err == nil || !strings.Contains(err.Error(), "no pending") {
		t.Fatalf("expected no pending error after delete, got %v", err)
	}
}

func TestDeletePendingMissing(t *testing.T) {
	if err := DeletePending(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestWritePendingOverwrite(t *testing.T) {
	dir := t.TempDir()
	first := &PendingOAuth{Verifier: "first", State: "s1", CreatedAt: time.Now()}
	second := &PendingOAuth{Verifier: "second", State: "s2", CreatedAt: time.Now()}
	if err := WritePending(dir, first); err != nil {
		t.Fatal(err)
	}
	if err := WritePending(dir, second); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPending(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verifier != "second" || got.State != "s2" {
		t.Fatalf("expected second write, got %+v", got)
	}
}
