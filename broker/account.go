package broker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

type PlanType string

const (
	PlanFree    PlanType = "free"
	PlanPlus    PlanType = "plus"
	PlanPro     PlanType = "pro"
	PlanTeam    PlanType = "team"
	PlanEnterprise PlanType = "enterprise"
)

type Account struct {
	Email        string    `json:"email"`
	AccountID    string    `json:"account_id"`
	PlanType     PlanType  `json:"plan_type"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	CreatedAt    time.Time `json:"created_at"`

	state atomic.Pointer[accountState]
}

type accountState struct {
	TotalRequests   int64
	TotalInputTkn   int64
	TotalOutputTkn  int64
	TotalCost       float64
	LastUsed        time.Time
	Disabled        bool
	Dead            bool
	DeadReason      string
	PrimaryUsedPct  float64
	SecondaryUsedPct float64
	PrimaryResetAt  time.Time
	SecondaryResetAt time.Time
	CooldownUntil   time.Time
	SourcePath      string

	LastWhamAt       time.Time
	LastWhamErrAt    time.Time
	LastWhamErr      string
	WhamFailCount    int
	WhamLimitReached bool
}

type AccountStats struct {
	TotalRequests    int64     `json:"total_requests"`
	TotalInputTkn    int64     `json:"total_input_tokens"`
	TotalOutputTkn   int64     `json:"total_output_tokens"`
	TotalCost        float64   `json:"total_cost"`
	LastUsed         time.Time `json:"last_used,omitzero"`
	Disabled         bool      `json:"disabled,omitempty"`
	Dead             bool      `json:"dead,omitempty"`
	DeadReason       string    `json:"dead_reason,omitempty"`
	PrimaryUsedPct   float64   `json:"primary_used_pct,omitempty"`
	SecondaryUsedPct float64   `json:"secondary_used_pct,omitempty"`
	PrimaryResetAt   time.Time `json:"primary_reset_at,omitzero"`
	SecondaryResetAt time.Time `json:"secondary_reset_at,omitzero"`
	CooldownUntil    time.Time `json:"cooldown_until,omitzero"`
	LastWhamAt       time.Time `json:"last_wham_at,omitzero"`
	LastWhamErrAt    time.Time `json:"last_wham_err_at,omitzero"`
	LastWhamErr      string    `json:"last_wham_err,omitempty"`
	WhamFailCount    int       `json:"wham_fail_count,omitempty"`
	WhamLimitReached bool      `json:"wham_limit_reached,omitempty"`
}

type WhamSnapshot struct {
	PrimaryUsedPct   float64
	SecondaryUsedPct float64
	PrimaryResetAt   time.Time
	SecondaryResetAt time.Time
	LimitReached     bool
	At               time.Time
}

func (a *Account) Stats() AccountStats {
	st := a.state.Load()
	if st == nil {
		return AccountStats{}
	}
	return AccountStats{
		TotalRequests:    st.TotalRequests,
		TotalInputTkn:    st.TotalInputTkn,
		TotalOutputTkn:   st.TotalOutputTkn,
		TotalCost:        st.TotalCost,
		LastUsed:         st.LastUsed,
		Disabled:         st.Disabled,
		Dead:             st.Dead,
		DeadReason:       st.DeadReason,
		PrimaryUsedPct:   st.PrimaryUsedPct,
		SecondaryUsedPct: st.SecondaryUsedPct,
		PrimaryResetAt:   st.PrimaryResetAt,
		SecondaryResetAt: st.SecondaryResetAt,
		CooldownUntil:    st.CooldownUntil,
		LastWhamAt:       st.LastWhamAt,
		LastWhamErrAt:    st.LastWhamErrAt,
		LastWhamErr:      st.LastWhamErr,
		WhamFailCount:    st.WhamFailCount,
		WhamLimitReached: st.WhamLimitReached,
	}
}

func (a *Account) updateState(fn func(s *accountState)) accountState {
	for {
		old := a.state.Load()
		var next accountState
		if old != nil {
			next = *old
		}
		fn(&next)
		if a.state.CompareAndSwap(old, &next) {
			return next
		}
	}
}

func (a *Account) ApplyStats(s AccountStats) {
	a.updateState(func(st *accountState) {
		st.TotalRequests = s.TotalRequests
		st.TotalInputTkn = s.TotalInputTkn
		st.TotalOutputTkn = s.TotalOutputTkn
		st.TotalCost = s.TotalCost
		st.LastUsed = s.LastUsed
		st.Disabled = s.Disabled
		st.Dead = s.Dead
		st.DeadReason = s.DeadReason
		st.PrimaryUsedPct = s.PrimaryUsedPct
		st.SecondaryUsedPct = s.SecondaryUsedPct
		st.PrimaryResetAt = s.PrimaryResetAt
		st.SecondaryResetAt = s.SecondaryResetAt
		st.CooldownUntil = s.CooldownUntil
		st.LastWhamAt = s.LastWhamAt
		st.LastWhamErrAt = s.LastWhamErrAt
		st.LastWhamErr = s.LastWhamErr
		st.WhamFailCount = s.WhamFailCount
		st.WhamLimitReached = s.WhamLimitReached
	})
}

func (a *Account) IsAvailable(now time.Time, primaryMax, secondaryMax float64) bool {
	st := a.state.Load()
	if st == nil {
		return true
	}
	if st.Disabled || st.Dead {
		return false
	}
	if !st.CooldownUntil.IsZero() && st.CooldownUntil.After(now) {
		return false
	}
	if primaryMax > 0 && st.PrimaryUsedPct >= primaryMax {
		return false
	}
	if secondaryMax > 0 && st.SecondaryUsedPct >= secondaryMax {
		return false
	}
	return true
}

const usageEstimatePerCall = 0.025

func (a *Account) RecordSuccess(input, output int64, cost float64) {
	a.updateState(func(st *accountState) {
		st.TotalRequests++
		st.TotalInputTkn += input
		st.TotalOutputTkn += output
		st.TotalCost += cost
		st.LastUsed = time.Now()
		st.PrimaryUsedPct += usageEstimatePerCall
		if st.PrimaryUsedPct > 1.0 {
			st.PrimaryUsedPct = 1.0
		}
	})
}

func (a *Account) MarkCooldown(until time.Time) {
	a.updateState(func(st *accountState) {
		st.CooldownUntil = until
	})
}

func (a *Account) MarkDead(reason string) {
	a.updateState(func(st *accountState) {
		st.Dead = true
		st.DeadReason = reason
	})
}

func (a *Account) SetDisabled(v bool) {
	a.updateState(func(st *accountState) {
		st.Disabled = v
	})
}

func (a *Account) ApplyWham(snap WhamSnapshot) {
	a.updateState(func(st *accountState) {
		wasWhamCooldown := !st.CooldownUntil.IsZero() && st.CooldownUntil.Equal(st.SecondaryResetAt)
		st.PrimaryUsedPct = snap.PrimaryUsedPct
		st.SecondaryUsedPct = snap.SecondaryUsedPct
		st.PrimaryResetAt = snap.PrimaryResetAt
		st.SecondaryResetAt = snap.SecondaryResetAt
		st.LastWhamAt = snap.At
		st.WhamFailCount = 0
		st.LastWhamErr = ""
		st.WhamLimitReached = snap.LimitReached
		if snap.LimitReached && !snap.SecondaryResetAt.IsZero() && snap.SecondaryResetAt.After(snap.At) {
			st.CooldownUntil = snap.SecondaryResetAt
		} else if !snap.LimitReached && wasWhamCooldown {
			st.CooldownUntil = time.Time{}
		}
	})
}

func (a *Account) MarkWhamFailed(at time.Time, errMsg string) {
	a.updateState(func(st *accountState) {
		st.WhamFailCount++
		st.LastWhamErrAt = at
		st.LastWhamErr = errMsg
	})
}

func (a *Account) UpdateTokens(access, refresh, id string, expires time.Time) {
	a.AccessToken = access
	if refresh != "" {
		a.RefreshToken = refresh
	}
	if id != "" {
		a.IDToken = id
	}
	a.ExpiresAt = expires
}

func LoadAccountFile(path string) (*Account, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var a Account
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if a.Email == "" {
		return nil, fmt.Errorf("%s: missing email", path)
	}
	if a.AccessToken == "" {
		return nil, fmt.Errorf("%s: missing access_token", path)
	}
	a.state.Store(&accountState{SourcePath: path})
	return &a, nil
}

func (a *Account) SourcePath() string {
	st := a.state.Load()
	if st == nil {
		return ""
	}
	return st.SourcePath
}

func SaveAccountFile(dir string, a *Account) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, sanitizeFilename(a.Email)+".json")
	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func sanitizeFilename(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return strings.Trim(string(out), "_")
}
