package control

import (
	"testing"
	"time"
)

func TestFutureTimeOrEmptyHidesPast(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		in   time.Time
		want bool
	}{
		{"zero", time.Time{}, false},
		{"past", now.Add(-24 * time.Hour), false},
		{"now", now, false},
		{"future", now.Add(time.Hour), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := futureTimeOrEmpty(c.in, now)
			if (got != "") != c.want {
				t.Errorf("futureTimeOrEmpty(%v) = %q, want non-empty=%v", c.in, got, c.want)
			}
		})
	}
}
