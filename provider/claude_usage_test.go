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

func TestParseClaudeUsage_UtilizationOnePercent(t *testing.T) {
	// Real API returns utilization in 0-100 range.
	// utilization=1.0 means 1%, NOT 100%.
	body := []byte(`{
		"five_hour":{"utilization":1.0,"resets_at":"2026-05-09T14:40:00Z"},
		"seven_day":{"utilization":6.0,"resets_at":"2026-05-11T09:00:01Z"}
	}`)
	u := parseClaudeUsage(body)
	if u.PrimaryUsedPct < 0.009 || u.PrimaryUsedPct > 0.011 {
		t.Fatalf("primary: want ~0.01 (1%%), got %v", u.PrimaryUsedPct)
	}
	if u.SecondaryUsedPct < 0.059 || u.SecondaryUsedPct > 0.061 {
		t.Fatalf("secondary: want ~0.06 (6%%), got %v", u.SecondaryUsedPct)
	}
}

func TestParseClaudeUsage_Hundred(t *testing.T) {
	// utilization=100.0 means 100%.
	body := []byte(`{"five_hour":{"utilization":100.0,"resets_at":"2026-05-09T12:00:00Z"}}`)
	u := parseClaudeUsage(body)
	if u.PrimaryUsedPct != 1.0 {
		t.Fatalf("primary: want 1.0 (100%%), got %v", u.PrimaryUsedPct)
	}
}
