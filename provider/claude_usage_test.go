package provider

import (
	"testing"
	"time"
)

func TestParseClaudeUsage(t *testing.T) {
	body := []byte(`{
		"five_hour":{"utilization":0.82,"resets_at":"2026-05-09T12:00:00Z"},
		"seven_day":{"utilization":79,"resets_at":1778328000}
	}`)
	u := parseClaudeUsage(body)
	if u.PrimaryUsedPct != 0.82 {
		t.Fatalf("primary=%v", u.PrimaryUsedPct)
	}
	if u.SecondaryUsedPct != 0.79 {
		t.Fatalf("secondary=%v", u.SecondaryUsedPct)
	}
	if !u.PrimaryResetAt.Equal(time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("primary reset=%s", u.PrimaryResetAt)
	}
	if u.SecondaryResetAt.IsZero() {
		t.Fatal("secondary reset missing")
	}
}

func TestParseClaudeUsageStatuslineShape(t *testing.T) {
	body := []byte(`{"rate_limits":{"five_hour":{"used_percentage":95,"resets_at":"2026-05-09T10:00:00Z"},"seven_day":{"used_percentage":0.77,"resets_at":"2026-05-12T10:00:00Z"}}}`)
	u := parseClaudeUsage(body)
	if u.PrimaryUsedPct != 0.95 {
		t.Fatalf("primary=%v", u.PrimaryUsedPct)
	}
	if u.SecondaryUsedPct != 0.77 {
		t.Fatalf("secondary=%v", u.SecondaryUsedPct)
	}
}
