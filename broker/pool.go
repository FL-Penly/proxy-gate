package broker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrNoAccounts        = errors.New("broker: no accounts configured")
	ErrAllExhausted      = errors.New("broker: all accounts exhausted")
	ErrAccountNotFound   = errors.New("broker: account not found")
)

type PoolConfig struct {
	PrimaryUsedPctMax   float64
	SecondaryUsedPctMax float64
	Weights             ScoreWeights
}

type Pool struct {
	cfg PoolConfig

	mu        sync.RWMutex
	accounts  map[string]*accountEntry
	pinStore  PinStore
	pinTTL    time.Duration
}

type PinStore interface {
	Put(prevResponseID, accountEmail string, expires time.Time) error
	Lookup(prevResponseID string) (string, bool)
}

func (p *Pool) SetPinStore(s PinStore, ttl time.Duration) {
	p.pinStore = s
	p.pinTTL = ttl
}

type accountEntry struct {
	account  *Account
	inflight atomic.Int64
}

func NewPool(cfg PoolConfig) *Pool {
	return &Pool{
		cfg:      cfg,
		accounts: make(map[string]*accountEntry),
	}
}

func (p *Pool) Add(a *Account) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accounts[a.Email] = &accountEntry{account: a}
}

func (p *Pool) Remove(email string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.accounts, email)
}

func (p *Pool) Get(email string) (*Account, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e, ok := p.accounts[email]
	if !ok {
		return nil, false
	}
	return e.account, true
}

func (p *Pool) List() []*Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Account, 0, len(p.accounts))
	for _, e := range p.accounts {
		out = append(out, e.account)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	return out
}

func (p *Pool) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.accounts)
}

func (p *Pool) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read pool dir %s: %w", dir, err)
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		acc, err := LoadAccountFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			return err
		}
		p.Add(acc)
	}
	return nil
}

type Lease struct {
	Account *Account
	pool    *Pool
	hint    LeaseHint
}

func (l *Lease) Release() {
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

func (l *Lease) PinResponse(responseID string) {
	if l == nil || l.pool == nil || l.pool.pinStore == nil || responseID == "" || l.pool.pinTTL <= 0 {
		return
	}
	_ = l.pool.pinStore.Put(responseID, l.Account.Email, time.Now().Add(l.pool.pinTTL))
}

type LeaseHint struct {
	Model              string
	PreviousResponseID string
}

func (p *Pool) Lease(_ context.Context, hint LeaseHint) (*Lease, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.accounts) == 0 {
		return nil, ErrNoAccounts
	}
	now := time.Now()

	if hint.PreviousResponseID != "" && p.pinStore != nil {
		if email, ok := p.pinStore.Lookup(hint.PreviousResponseID); ok {
			if e, exists := p.accounts[email]; exists && e.account.IsAvailable(now, p.cfg.PrimaryUsedPctMax, p.cfg.SecondaryUsedPctMax) {
				e.inflight.Add(1)
				return &Lease{Account: e.account, pool: p, hint: hint}, nil
			}
		}
	}

	ranks := rankCandidates(now, p, p.weights(), nil)
	if len(ranks) == 0 {
		return nil, ErrAllExhausted
	}
	chosen := ranks[0].entry
	chosen.inflight.Add(1)
	return &Lease{Account: chosen.account, pool: p, hint: hint}, nil
}

func (p *Pool) weights() ScoreWeights {
	w := p.cfg.Weights
	if w == (ScoreWeights{}) {
		return DefaultScoreWeights()
	}
	return w
}
