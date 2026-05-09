package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

type ClaudeAccount struct {
	Email            string    `json:"email"`
	AccountID        string    `json:"account_id,omitempty"`
	SubscriptionType string    `json:"subscription_type,omitempty"`
	RateLimitTier    string    `json:"rate_limit_tier,omitempty"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	ExpiresAt        time.Time `json:"expires_at,omitzero"`
	CreatedAt        time.Time `json:"created_at,omitzero"`

	state atomic.Pointer[claudeAccountState]
}

type claudeAccountState struct {
	TotalRequests    int64
	TotalInputTkn    int64
	TotalOutputTkn   int64
	TotalCost        float64
	LastUsed         time.Time
	Disabled         bool
	Dead             bool
	DeadReason       string
	CooldownUntil    time.Time
	SourcePath       string
	LastRefreshAt    time.Time
	LastRefreshErrAt time.Time
	LastRefreshErr   string
	RefreshFailCount int
	PrimaryUsedPct   float64
	SecondaryUsedPct float64
	PrimaryResetAt   time.Time
	SecondaryResetAt time.Time
	LastUsageAt      time.Time
	LastUsageErrAt   time.Time
	LastUsageErr     string
	UsageFailCount   int
}

type ClaudeAccountStats struct {
	TotalRequests    int64     `json:"total_requests"`
	TotalInputTkn    int64     `json:"total_input_tokens"`
	TotalOutputTkn   int64     `json:"total_output_tokens"`
	TotalCost        float64   `json:"total_cost"`
	LastUsed         time.Time `json:"last_used,omitzero"`
	Disabled         bool      `json:"disabled,omitempty"`
	Dead             bool      `json:"dead,omitempty"`
	DeadReason       string    `json:"dead_reason,omitempty"`
	CooldownUntil    time.Time `json:"cooldown_until,omitzero"`
	LastRefreshAt    time.Time `json:"last_refresh_at,omitzero"`
	LastRefreshErrAt time.Time `json:"last_refresh_err_at,omitzero"`
	LastRefreshErr   string    `json:"last_refresh_err,omitempty"`
	RefreshFailCount int       `json:"refresh_fail_count,omitempty"`
	PrimaryUsedPct   float64   `json:"primary_used_pct,omitempty"`
	SecondaryUsedPct float64   `json:"secondary_used_pct,omitempty"`
	PrimaryResetAt   time.Time `json:"primary_reset_at,omitzero"`
	SecondaryResetAt time.Time `json:"secondary_reset_at,omitzero"`
	LastUsageAt      time.Time `json:"last_usage_at,omitzero"`
	LastUsageErrAt   time.Time `json:"last_usage_err_at,omitzero"`
	LastUsageErr     string    `json:"last_usage_err,omitempty"`
	UsageFailCount   int       `json:"usage_fail_count,omitempty"`
}

func (a *ClaudeAccount) Stats() ClaudeAccountStats {
	st := a.state.Load()
	if st == nil {
		return ClaudeAccountStats{}
	}
	return ClaudeAccountStats{
		TotalRequests:    st.TotalRequests,
		TotalInputTkn:    st.TotalInputTkn,
		TotalOutputTkn:   st.TotalOutputTkn,
		TotalCost:        st.TotalCost,
		LastUsed:         st.LastUsed,
		Disabled:         st.Disabled,
		Dead:             st.Dead,
		DeadReason:       st.DeadReason,
		CooldownUntil:    st.CooldownUntil,
		LastRefreshAt:    st.LastRefreshAt,
		LastRefreshErrAt: st.LastRefreshErrAt,
		LastRefreshErr:   st.LastRefreshErr,
		RefreshFailCount: st.RefreshFailCount,
		PrimaryUsedPct:   st.PrimaryUsedPct,
		SecondaryUsedPct: st.SecondaryUsedPct,
		PrimaryResetAt:   st.PrimaryResetAt,
		SecondaryResetAt: st.SecondaryResetAt,
		LastUsageAt:      st.LastUsageAt,
		LastUsageErrAt:   st.LastUsageErrAt,
		LastUsageErr:     st.LastUsageErr,
		UsageFailCount:   st.UsageFailCount,
	}
}

func (a *ClaudeAccount) updateState(fn func(s *claudeAccountState)) {
	for {
		old := a.state.Load()
		var next claudeAccountState
		if old != nil {
			next = *old
		}
		fn(&next)
		if a.state.CompareAndSwap(old, &next) {
			return
		}
	}
}

func (a *ClaudeAccount) ApplyStats(s ClaudeAccountStats) {
	a.updateState(func(st *claudeAccountState) {
		st.TotalRequests = s.TotalRequests
		st.TotalInputTkn = s.TotalInputTkn
		st.TotalOutputTkn = s.TotalOutputTkn
		st.TotalCost = s.TotalCost
		st.LastUsed = s.LastUsed
		st.Disabled = s.Disabled
		st.Dead = s.Dead
		st.DeadReason = s.DeadReason
		st.CooldownUntil = s.CooldownUntil
		st.LastRefreshAt = s.LastRefreshAt
		st.LastRefreshErrAt = s.LastRefreshErrAt
		st.LastRefreshErr = s.LastRefreshErr
		st.RefreshFailCount = s.RefreshFailCount
		st.PrimaryUsedPct = s.PrimaryUsedPct
		st.SecondaryUsedPct = s.SecondaryUsedPct
		st.PrimaryResetAt = s.PrimaryResetAt
		st.SecondaryResetAt = s.SecondaryResetAt
		st.LastUsageAt = s.LastUsageAt
		st.LastUsageErrAt = s.LastUsageErrAt
		st.LastUsageErr = s.LastUsageErr
		st.UsageFailCount = s.UsageFailCount
	})
}

func (a *ClaudeAccount) IsAvailable(now time.Time) bool {
	st := a.state.Load()
	if st == nil {
		return true
	}
	if st.Disabled || st.Dead {
		return false
	}
	return st.CooldownUntil.IsZero() || !st.CooldownUntil.After(now)
}

func (a *ClaudeAccount) RecordSuccess(input, output int64, cost float64) {
	a.updateState(func(st *claudeAccountState) {
		st.TotalRequests++
		st.TotalInputTkn += input
		st.TotalOutputTkn += output
		st.TotalCost += cost
		st.LastUsed = time.Now()
	})
}

func (a *ClaudeAccount) MarkCooldown(until time.Time) {
	a.updateState(func(st *claudeAccountState) { st.CooldownUntil = until })
}

func (a *ClaudeAccount) MarkDead(reason string) {
	a.updateState(func(st *claudeAccountState) {
		st.Dead = true
		st.DeadReason = reason
	})
}

func (a *ClaudeAccount) SetDisabled(v bool) {
	a.updateState(func(st *claudeAccountState) { st.Disabled = v })
}

func (a *ClaudeAccount) MarkRefreshFailed(at time.Time, errMsg string) {
	a.updateState(func(st *claudeAccountState) {
		st.RefreshFailCount++
		st.LastRefreshErrAt = at
		st.LastRefreshErr = errMsg
	})
}

func (a *ClaudeAccount) MarkRefreshed(at time.Time) {
	a.updateState(func(st *claudeAccountState) {
		st.LastRefreshAt = at
		st.RefreshFailCount = 0
		st.LastRefreshErrAt = time.Time{}
		st.LastRefreshErr = ""
	})
}

type ClaudeUsageSnapshot struct {
	PrimaryUsedPct   float64
	SecondaryUsedPct float64
	PrimaryResetAt   time.Time
	SecondaryResetAt time.Time
	At               time.Time
}

func (a *ClaudeAccount) ApplyUsage(s ClaudeUsageSnapshot) {
	a.updateState(func(st *claudeAccountState) {
		st.PrimaryUsedPct = s.PrimaryUsedPct
		st.SecondaryUsedPct = s.SecondaryUsedPct
		st.PrimaryResetAt = s.PrimaryResetAt
		st.SecondaryResetAt = s.SecondaryResetAt
		st.LastUsageAt = s.At
		st.LastUsageErrAt = time.Time{}
		st.LastUsageErr = ""
		st.UsageFailCount = 0
	})
}

func (a *ClaudeAccount) MarkUsageFailed(at time.Time, errMsg string) {
	a.updateState(func(st *claudeAccountState) {
		st.UsageFailCount++
		st.LastUsageErrAt = at
		st.LastUsageErr = errMsg
	})
}

func (a *ClaudeAccount) UpdateTokens(access, refresh string, expires time.Time) {
	a.AccessToken = access
	if refresh != "" {
		a.RefreshToken = refresh
	}
	a.ExpiresAt = expires
}

func (a *ClaudeAccount) SourcePath() string {
	st := a.state.Load()
	if st == nil {
		return ""
	}
	return st.SourcePath
}

func LoadClaudeAccountFile(path string) (*ClaudeAccount, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var a ClaudeAccount
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if a.Email == "" {
		return nil, fmt.Errorf("%s: missing email", path)
	}
	if a.AccessToken == "" {
		return nil, fmt.Errorf("%s: missing access_token", path)
	}
	a.state.Store(&claudeAccountState{SourcePath: path})
	return &a, nil
}

func SaveClaudeAccountFile(dir string, a *ClaudeAccount) (string, error) {
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
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	a.updateState(func(st *claudeAccountState) { st.SourcePath = path })
	return path, nil
}

type ClaudePool struct {
	mu       sync.RWMutex
	accounts map[string]*claudeAccountEntry
	dir      string
}

type claudeAccountEntry struct {
	account  *ClaudeAccount
	inflight atomic.Int64
}

func NewClaudePool() *ClaudePool {
	return &ClaudePool{accounts: make(map[string]*claudeAccountEntry)}
}

func (p *ClaudePool) Add(a *ClaudeAccount) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing := p.accounts[a.Email]; existing != nil {
		a.ApplyStats(existing.account.Stats())
	}
	p.accounts[a.Email] = &claudeAccountEntry{account: a}
}

func (p *ClaudePool) Remove(email string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.accounts, email)
}

func (p *ClaudePool) Get(email string) (*ClaudeAccount, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e, ok := p.accounts[email]
	if !ok {
		return nil, false
	}
	return e.account, true
}

func (p *ClaudePool) List() []*ClaudeAccount {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*ClaudeAccount, 0, len(p.accounts))
	for _, e := range p.accounts {
		out = append(out, e.account)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	return out
}

func (p *ClaudePool) Dir() string { return p.dir }

func (p *ClaudePool) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.accounts)
}

func (p *ClaudePool) LoadDir(dir string) error {
	p.dir = dir
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read claude pool dir %s: %w", dir, err)
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		acc, err := LoadClaudeAccountFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			return err
		}
		p.Add(acc)
	}
	return nil
}

type ClaudeLease struct {
	Account *ClaudeAccount
	pool    *ClaudePool
}

func (l *ClaudeLease) Release() {
	if l == nil || l.pool == nil {
		return
	}
	p := l.pool
	p.mu.RLock()
	e := p.accounts[l.Account.Email]
	p.mu.RUnlock()
	if e != nil {
		e.inflight.Add(-1)
	}
	l.pool = nil
}

func (p *ClaudePool) Lease(_ context.Context) (*ClaudeLease, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.accounts) == 0 {
		return nil, ErrNoAccounts
	}
	now := time.Now()
	candidates := make([]*claudeAccountEntry, 0, len(p.accounts))
	for _, e := range p.accounts {
		if e.account.IsAvailable(now) {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		return nil, ErrAllExhausted
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		ii := claudeTokenLoad(candidates[i].account)
		jj := claudeTokenLoad(candidates[j].account)
		if ii != jj {
			return ii < jj
		}
		inI := candidates[i].inflight.Load()
		inJ := candidates[j].inflight.Load()
		if inI != inJ {
			return inI < inJ
		}
		return candidates[i].account.Email < candidates[j].account.Email
	})
	chosen := candidates[0]
	chosen.inflight.Add(1)
	return &ClaudeLease{Account: chosen.account, pool: p}, nil
}

func claudeTokenLoad(a *ClaudeAccount) int64 {
	st := a.state.Load()
	if st == nil {
		return 0
	}
	return st.TotalInputTkn + st.TotalOutputTkn
}

func (p *ClaudePool) NearestCooldown(now time.Time) time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var best time.Duration = -1
	for _, e := range p.accounts {
		st := e.account.state.Load()
		if st == nil || st.Disabled || st.Dead {
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

func (p *ClaudePool) WatchDir(ctx context.Context, dir string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return err
	}
	go p.runWatcher(ctx, w, dir, logger)
	return nil
}

func (p *ClaudePool) runWatcher(ctx context.Context, w *fsnotify.Watcher, dir string, logger *slog.Logger) {
	defer w.Close()
	debounce := make(map[string]*time.Timer)
	const wait = 200 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			for _, t := range debounce {
				t.Stop()
			}
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(ev.Name, ".json") {
				continue
			}
			path := ev.Name
			if t, ok := debounce[path]; ok {
				t.Stop()
			}
			debounce[path] = time.AfterFunc(wait, func() { p.handleFileEvent(ev.Op, path, logger) })
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			logger.Warn("claude watcher error", "err", err, "dir", dir)
		}
	}
}

func (p *ClaudePool) handleFileEvent(op fsnotify.Op, path string, logger *slog.Logger) {
	if op&fsnotify.Remove != 0 || op&fsnotify.Rename != 0 {
		if email := claudeEmailBySource(p, path); email != "" {
			p.Remove(email)
			logger.Info("claude account removed", "email", email, "path", path)
		}
		return
	}
	acc, err := LoadClaudeAccountFile(path)
	if err != nil {
		logger.Warn("claude account file invalid", "err", err, "path", path)
		return
	}
	p.Add(acc)
	logger.Info("claude account loaded", "email", acc.Email, "path", path)
}

func claudeEmailBySource(p *ClaudePool, path string) string {
	for _, a := range p.List() {
		if filepath.Clean(a.SourcePath()) == filepath.Clean(path) {
			return a.Email
		}
	}
	return ""
}
