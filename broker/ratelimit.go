package broker

import (
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/tidwall/gjson"
)

const (
	defaultCooldown = 60 * time.Second
	maxCooldown     = 30 * time.Minute
	min5xxBackoff   = time.Second
	max5xxBackoff   = 30 * time.Minute
)

func ParseRetryAfter(h http.Header, body []byte, now time.Time) time.Duration {
	if v := h.Get("retry-after-ms"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return clampCooldown(time.Duration(ms) * time.Millisecond)
		}
	}
	if v := h.Get("retry-after"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return clampCooldown(time.Duration(secs) * time.Second)
		}
		if t, err := http.ParseTime(v); err == nil {
			delta := t.Sub(now)
			if delta > 0 {
				return clampCooldown(delta)
			}
		}
	}
	if v := h.Get("x-ratelimit-reset"); v != "" {
		if epoch, err := strconv.ParseInt(v, 10, 64); err == nil {
			delta := time.Unix(epoch, 0).Sub(now)
			if delta > 0 {
				return clampCooldown(delta)
			}
		}
	}
	if d := parseFromBody(body); d > 0 {
		return clampCooldown(d)
	}
	return defaultCooldown
}

var (
	quotaResetDelayRE = regexp.MustCompile(`(?i)quotaResetDelay[:\s"]+(\d+(?:\.\d+)?)\s*(ms|s)`)
	retryInRE         = regexp.MustCompile(`(?i)retry\s+(?:after\s+)?(\d+)\s*(?:sec|s\b)`)
)

func parseFromBody(body []byte) time.Duration {
	if len(body) == 0 {
		return 0
	}
	if msg := gjson.GetBytes(body, "error.message").String(); msg != "" {
		if d := parseFromString(msg); d > 0 {
			return d
		}
	}
	return parseFromString(string(body))
}

func parseFromString(s string) time.Duration {
	if m := quotaResetDelayRE.FindStringSubmatch(s); len(m) == 3 {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			if m[2] == "s" {
				return time.Duration(v * float64(time.Second))
			}
			return time.Duration(v * float64(time.Millisecond))
		}
	}
	if m := retryInRE.FindStringSubmatch(s); len(m) == 2 {
		if v, err := strconv.Atoi(m[1]); err == nil && v > 0 {
			return time.Duration(v) * time.Second
		}
	}
	return 0
}

func clampCooldown(d time.Duration) time.Duration {
	if d < 0 {
		return defaultCooldown
	}
	if d > maxCooldown {
		return maxCooldown
	}
	return d
}

func BackoffFor5xx(level int) time.Duration {
	d := min5xxBackoff << level
	if d <= 0 || d > max5xxBackoff {
		return max5xxBackoff
	}
	return d
}

func (p *Pool) NearestCooldown(now time.Time) time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var best time.Duration = -1
	for _, e := range p.accounts {
		st := e.account.state.Load()
		if st == nil {
			continue
		}
		if st.Disabled || st.Dead {
			continue
		}
		if st.CooldownUntil.IsZero() || !st.CooldownUntil.After(now) {
			continue
		}
		d := st.CooldownUntil.Sub(now)
		if best < 0 || d < best {
			best = d
		}
	}
	if best < 0 {
		return 0
	}
	return best
}
