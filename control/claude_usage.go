package control

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/FL-Penly/proxy-gate/broker"
	"github.com/FL-Penly/proxy-gate/provider"
	"github.com/FL-Penly/proxy-gate/store"
)

type ClaudeUsagePoller struct {
	Pool      *broker.ClaudePool
	Refresher *ClaudeTokenRefresher
	Queue     *Queue
	Interval  time.Duration
	Logger    *slog.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (p *ClaudeUsagePoller) Start(parent context.Context) {
	if p.Pool == nil {
		return
	}
	if p.Interval <= 0 {
		p.Interval = 5 * time.Minute
	}
	if p.Logger == nil {
		p.Logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	p.wg.Add(1)
	go p.loop(ctx)
}

func (p *ClaudeUsagePoller) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
}

func (p *ClaudeUsagePoller) loop(ctx context.Context) {
	defer p.wg.Done()
	t := time.NewTicker(p.Interval)
	defer t.Stop()
	p.pollOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.pollOnce(ctx)
		}
	}
}

func (p *ClaudeUsagePoller) pollOnce(ctx context.Context) {
	for _, acc := range p.Pool.List() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if stats := acc.Stats(); stats.Disabled || stats.Dead {
			continue
		}
		p.pollAccount(ctx, acc)
	}
}

func (p *ClaudeUsagePoller) pollAccount(ctx context.Context, acc *broker.ClaudeAccount) {
	usage, err := provider.GetClaudeUsage(ctx, acc.AccessToken)
	if err == nil {
		acc.ApplyUsage(claudeUsageSnapshot(usage))
		p.persist(acc)
		return
	}
	status := classifyClaudeUsageError(err)
	if status == 401 && p.Refresher != nil {
		if rerr := p.Refresher.RefreshClaudeToken(ctx, acc); rerr != nil {
			acc.MarkUsageFailed(time.Now(), "refresh: "+rerr.Error())
			p.persist(acc)
			p.Logger.Warn("claude usage: refresh failed", "account", acc.Email, "err", rerr)
			return
		}
		if retry, rerr := provider.GetClaudeUsage(ctx, acc.AccessToken); rerr == nil {
			acc.ApplyUsage(claudeUsageSnapshot(retry))
			p.persist(acc)
			return
		} else {
			err = rerr
		}
	}
	acc.MarkUsageFailed(time.Now(), err.Error())
	p.persist(acc)
	if status == 429 {
		p.Logger.Info("claude usage: rate limited", "account", acc.Email)
		return
	}
	p.Logger.Warn("claude usage: fetch failed", "account", acc.Email, "err", err)
}

func (p *ClaudeUsagePoller) persist(acc *broker.ClaudeAccount) {
	if p.Queue == nil {
		return
	}
	data, err := json.Marshal(acc.Stats())
	if err == nil {
		_ = p.Queue.Put(store.BucketClaudeAccounts, "stats:"+acc.Email, data)
	}
}

func claudeUsageSnapshot(u provider.ClaudeUsage) broker.ClaudeUsageSnapshot {
	return broker.ClaudeUsageSnapshot{
		PrimaryUsedPct:   u.PrimaryUsedPct,
		SecondaryUsedPct: u.SecondaryUsedPct,
		PrimaryResetAt:   u.PrimaryResetAt,
		SecondaryResetAt: u.SecondaryResetAt,
		At:               time.Now(),
	}
}

func classifyClaudeUsageError(err error) int {
	var werr *provider.TokenError
	if errors.As(err, &werr) {
		return werr.Status
	}
	return 0
}
