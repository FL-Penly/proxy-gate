package broker

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfterMs(t *testing.T) {
	h := http.Header{"Retry-After-Ms": []string{"3000"}}
	d := ParseRetryAfter(h, nil, time.Now())
	if d != 3*time.Second {
		t.Errorf("got %v, want 3s", d)
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	h := http.Header{"Retry-After": []string{"45"}}
	d := ParseRetryAfter(h, nil, time.Now())
	if d != 45*time.Second {
		t.Errorf("got %v", d)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	h := http.Header{"Retry-After": []string{"Sun, 26 Apr 2026 12:01:30 GMT"}}
	d := ParseRetryAfter(h, nil, now)
	if d != 90*time.Second {
		t.Errorf("got %v, want 90s", d)
	}
}

func TestParseRetryAfterRatelimitReset(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := http.Header{"X-Ratelimit-Reset": []string{"1700000120"}}
	d := ParseRetryAfter(h, nil, now)
	if d != 120*time.Second {
		t.Errorf("got %v, want 120s", d)
	}
}

func TestParseRetryAfterBodyQuotaResetDelay(t *testing.T) {
	body := []byte(`{"error":{"message":"rate limited, quotaResetDelay: 5s"}}`)
	d := ParseRetryAfter(http.Header{}, body, time.Now())
	if d != 5*time.Second {
		t.Errorf("got %v, want 5s", d)
	}
}

func TestParseRetryAfterDefault(t *testing.T) {
	d := ParseRetryAfter(http.Header{}, nil, time.Now())
	if d != defaultCooldown {
		t.Errorf("got %v, want %v", d, defaultCooldown)
	}
}

func TestBackoffFor5xx(t *testing.T) {
	tests := []struct {
		level int
		want  time.Duration
	}{
		{0, time.Second},
		{1, 2 * time.Second},
		{4, 16 * time.Second},
		{40, max5xxBackoff},
	}
	for _, tc := range tests {
		got := BackoffFor5xx(tc.level)
		if got != tc.want {
			t.Errorf("BackoffFor5xx(%d) = %v, want %v", tc.level, got, tc.want)
		}
	}
}

func TestNearestCooldown(t *testing.T) {
	pool := NewPool(PoolConfig{})
	now := time.Now()
	a := mkAcc("a@x.com", PlanPro, 0, 0)
	a.MarkCooldown(now.Add(30 * time.Second))
	b := mkAcc("b@x.com", PlanPro, 0, 0)
	b.MarkCooldown(now.Add(10 * time.Second))
	pool.Add(a)
	pool.Add(b)

	d := pool.NearestCooldown(now)
	if d < 9*time.Second || d > 11*time.Second {
		t.Errorf("nearest cooldown = %v, want ~10s", d)
	}
}

func TestNearestCooldownNone(t *testing.T) {
	pool := NewPool(PoolConfig{})
	pool.Add(mkAcc("a@x.com", PlanPro, 0, 0))
	if d := pool.NearestCooldown(time.Now()); d != 0 {
		t.Errorf("got %v, want 0", d)
	}
}
