package control

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/FL-Penly/proxy-gate/broker"
	"github.com/FL-Penly/proxy-gate/provider"
)

type WhamPoller struct {
	Pool      *broker.Pool
	Client    *provider.ChatGPTClient
	Refresher *TokenRefresher
	Interval  time.Duration
	Logger    *slog.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (p *WhamPoller) Start(parent context.Context) {
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

func (p *WhamPoller) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
}

func (p *WhamPoller) loop(ctx context.Context) {
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

func (p *WhamPoller) pollOnce(ctx context.Context) {
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

func (p *WhamPoller) pollAccount(ctx context.Context, acc *broker.Account) {
	usage, err := p.Client.FetchUsage(ctx, provider.Credential{
		AccessToken: acc.AccessToken,
		AccountID:   acc.AccountID,
	})
	if err == nil {
		acc.ApplyWham(snapshotFromUsage(usage))
		return
	}
	httpErr := classifyWhamError(err)
	switch httpErr {
	case 401:
		if p.Refresher != nil {
			if rerr := p.Refresher.RefreshToken(ctx, acc); rerr != nil {
				acc.MarkWhamFailed(time.Now(), "refresh: "+rerr.Error())
				p.Logger.Warn("wham: refresh failed", "account", acc.Email, "err", rerr)
				return
			}
			retry, rerr := p.Client.FetchUsage(ctx, provider.Credential{AccessToken: acc.AccessToken, AccountID: acc.AccountID})
			if rerr == nil {
				acc.ApplyWham(snapshotFromUsage(retry))
				return
			}
			acc.MarkWhamFailed(time.Now(), rerr.Error())
			p.Logger.Warn("wham: post-refresh fetch failed", "account", acc.Email, "err", rerr)
			return
		}
		acc.MarkWhamFailed(time.Now(), err.Error())
	case 429:
		acc.MarkWhamFailed(time.Now(), "429")
		p.Logger.Info("wham: rate limited (skip)", "account", acc.Email)
	default:
		acc.MarkWhamFailed(time.Now(), err.Error())
		p.Logger.Warn("wham: fetch failed", "account", acc.Email, "err", err)
	}
}

func snapshotFromUsage(u provider.WhamUsage) broker.WhamSnapshot {
	return broker.WhamSnapshot{
		PrimaryUsedPct:   u.PrimaryUsedPct,
		SecondaryUsedPct: u.SecondaryUsedPct,
		PrimaryResetAt:   u.PrimaryResetAt,
		SecondaryResetAt: u.SecondaryResetAt,
		LimitReached:     u.LimitReached,
		At:               time.Now(),
	}
}

func classifyWhamError(err error) int {
	var werr *provider.WhamError
	if errors.As(err, &werr) {
		return werr.Status
	}
	return 0
}
