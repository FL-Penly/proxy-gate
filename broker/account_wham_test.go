package broker

import (
	"testing"
	"time"
)

func TestApplyWhamWritesFields(t *testing.T) {
	a := &Account{Email: "u@x.com", AccessToken: "t"}
	a.ApplyStats(AccountStats{})

	now := time.Now()
	primaryReset := now.Add(5 * time.Hour)
	secondaryReset := now.Add(72 * time.Hour)

	a.ApplyWham(WhamSnapshot{
		PrimaryUsedPct:   0.42,
		SecondaryUsedPct: 0.99,
		PrimaryResetAt:   primaryReset,
		SecondaryResetAt: secondaryReset,
		LimitReached:     false,
		At:               now,
	})

	s := a.Stats()
	if s.PrimaryUsedPct != 0.42 || s.SecondaryUsedPct != 0.99 {
		t.Fatalf("pct: got primary=%v secondary=%v", s.PrimaryUsedPct, s.SecondaryUsedPct)
	}
	if !s.LastWhamAt.Equal(now) {
		t.Errorf("LastWhamAt = %v, want %v", s.LastWhamAt, now)
	}
	if s.WhamFailCount != 0 {
		t.Errorf("WhamFailCount = %d, want 0", s.WhamFailCount)
	}
	if !s.CooldownUntil.IsZero() {
		t.Errorf("CooldownUntil should be zero when limit_reached=false, got %v", s.CooldownUntil)
	}
}

func TestApplyWhamLimitReachedSetsCooldown(t *testing.T) {
	a := &Account{Email: "u@x.com", AccessToken: "t"}
	a.ApplyStats(AccountStats{})

	now := time.Now()
	secondaryReset := now.Add(48 * time.Hour)

	a.ApplyWham(WhamSnapshot{
		PrimaryUsedPct:   0,
		SecondaryUsedPct: 1.0,
		SecondaryResetAt: secondaryReset,
		LimitReached:     true,
		At:               now,
	})

	s := a.Stats()
	if !s.WhamLimitReached {
		t.Errorf("WhamLimitReached not set")
	}
	if !s.CooldownUntil.Equal(secondaryReset) {
		t.Errorf("CooldownUntil = %v, want %v (secondary_reset_at)", s.CooldownUntil, secondaryReset)
	}
	if a.IsAvailable(now, 0.95, 0.99) {
		t.Errorf("account should be unavailable when CooldownUntil in the future")
	}
	if !a.IsAvailable(secondaryReset.Add(time.Second), 0, 0) {
		t.Errorf("cooldown gate should clear after CooldownUntil passes")
	}
}

func TestApplyWhamClearsPreviousWhamCooldown(t *testing.T) {
	a := &Account{Email: "u@x.com", AccessToken: "t"}
	now := time.Now()
	secondaryReset := now.Add(48 * time.Hour)
	a.ApplyStats(AccountStats{
		CooldownUntil:   secondaryReset,
		SecondaryResetAt: secondaryReset,
	})

	a.ApplyWham(WhamSnapshot{
		SecondaryUsedPct: 0.15,
		SecondaryResetAt: secondaryReset,
		LimitReached:     false,
		At:               now,
	})

	if got := a.Stats().CooldownUntil; !got.IsZero() {
		t.Errorf("CooldownUntil should be cleared when WHAM limit recovers, got %v", got)
	}
}

func TestApplyWhamDoesNotClearNonWhamCooldown(t *testing.T) {
	a := &Account{Email: "u@x.com", AccessToken: "t"}
	now := time.Now()
	secondaryReset := now.Add(48 * time.Hour)
	retryCooldown := now.Add(time.Minute)
	a.ApplyStats(AccountStats{
		CooldownUntil:   retryCooldown,
		SecondaryResetAt: secondaryReset,
	})

	a.ApplyWham(WhamSnapshot{
		SecondaryUsedPct: 0.15,
		SecondaryResetAt: secondaryReset,
		LimitReached:     false,
		At:               now,
	})

	if got := a.Stats().CooldownUntil; !got.Equal(retryCooldown) {
		t.Errorf("CooldownUntil = %v, want retry cooldown %v", got, retryCooldown)
	}
}

func TestApplyWhamLimitReachedNoCooldownIfResetMissing(t *testing.T) {
	a := &Account{Email: "u@x.com", AccessToken: "t"}
	a.ApplyStats(AccountStats{})

	a.ApplyWham(WhamSnapshot{
		LimitReached: true,
		At:           time.Now(),
	})
	if !a.Stats().CooldownUntil.IsZero() {
		t.Errorf("CooldownUntil must stay zero if SecondaryResetAt is missing")
	}
}

func TestApplyWhamResetsFailCountAndErr(t *testing.T) {
	a := &Account{Email: "u@x.com", AccessToken: "t"}
	a.ApplyStats(AccountStats{})

	a.MarkWhamFailed(time.Now(), "timeout")
	a.MarkWhamFailed(time.Now(), "timeout")
	if s := a.Stats(); s.WhamFailCount != 2 || s.LastWhamErr != "timeout" {
		t.Fatalf("after 2 fails: got %+v", s)
	}

	a.ApplyWham(WhamSnapshot{At: time.Now()})

	s := a.Stats()
	if s.WhamFailCount != 0 {
		t.Errorf("WhamFailCount = %d after success, want 0", s.WhamFailCount)
	}
	if s.LastWhamErr != "" {
		t.Errorf("LastWhamErr = %q after success, want empty", s.LastWhamErr)
	}
}

func TestMarkWhamFailedIncrements(t *testing.T) {
	a := &Account{Email: "u@x.com", AccessToken: "t"}
	a.ApplyStats(AccountStats{})

	t1 := time.Now()
	a.MarkWhamFailed(t1, "first")
	t2 := t1.Add(time.Second)
	a.MarkWhamFailed(t2, "second")

	s := a.Stats()
	if s.WhamFailCount != 2 {
		t.Errorf("WhamFailCount = %d, want 2", s.WhamFailCount)
	}
	if !s.LastWhamErrAt.Equal(t2) {
		t.Errorf("LastWhamErrAt = %v, want %v", s.LastWhamErrAt, t2)
	}
	if s.LastWhamErr != "second" {
		t.Errorf("LastWhamErr = %q, want %q", s.LastWhamErr, "second")
	}
}

func TestApplyStatsRoundTripsWhamFields(t *testing.T) {
	a := &Account{Email: "u@x.com", AccessToken: "t"}
	now := time.Now().UTC().Round(time.Second)
	in := AccountStats{
		LastWhamAt:       now,
		LastWhamErrAt:    now.Add(time.Minute),
		LastWhamErr:      "boom",
		WhamFailCount:    7,
		WhamLimitReached: true,
	}
	a.ApplyStats(in)

	got := a.Stats()
	if !got.LastWhamAt.Equal(in.LastWhamAt) {
		t.Errorf("LastWhamAt: got %v want %v", got.LastWhamAt, in.LastWhamAt)
	}
	if !got.LastWhamErrAt.Equal(in.LastWhamErrAt) {
		t.Errorf("LastWhamErrAt: got %v want %v", got.LastWhamErrAt, in.LastWhamErrAt)
	}
	if got.LastWhamErr != in.LastWhamErr || got.WhamFailCount != in.WhamFailCount || got.WhamLimitReached != in.WhamLimitReached {
		t.Errorf("round-trip mismatch: %+v vs %+v", got, in)
	}
}
