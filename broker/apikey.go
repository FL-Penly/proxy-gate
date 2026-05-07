package broker

import (
	"context"
	"encoding/json"
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

type APIKeyType string

const (
	KeyTypeOpenAI      APIKeyType = "openai"
	KeyTypeAzureOpenAI APIKeyType = "azure-openai"
	KeyTypeGemini      APIKeyType = "gemini"
	KeyTypeVertex      APIKeyType = "vertex-ai"
	KeyTypeAnthropic   APIKeyType = "anthropic"
	KeyTypeMoonshot    APIKeyType = "moonshot"
	KeyTypeMinimax     APIKeyType = "minimax"
	KeyTypeZhipu       APIKeyType = "zhipu"
)

type APIKey struct {
	ID       string     `json:"id"`
	Name     string     `json:"name,omitempty"`
	Type     APIKeyType `json:"type"`
	APIKey   string     `json:"api_key"`
	BaseURL  string     `json:"base_url,omitempty"`

	state atomic.Pointer[apiKeyState]
}

type apiKeyState struct {
	TotalRequests   int64
	TotalInputTkn   int64
	TotalOutputTkn  int64
	TotalCost       float64
	Disabled        bool
	Dead            bool
	DeadReason      string
	CooldownUntil   time.Time
	SourcePath      string
}

type APIKeyStats struct {
	TotalRequests  int64     `json:"total_requests"`
	TotalInputTkn  int64     `json:"total_input_tokens"`
	TotalOutputTkn int64     `json:"total_output_tokens"`
	TotalCost      float64   `json:"total_cost"`
	Disabled       bool      `json:"disabled,omitempty"`
	Dead           bool      `json:"dead,omitempty"`
	DeadReason     string    `json:"dead_reason,omitempty"`
	CooldownUntil  time.Time `json:"cooldown_until,omitempty"`
}

func (k *APIKey) Stats() APIKeyStats {
	st := k.state.Load()
	if st == nil {
		return APIKeyStats{}
	}
	return APIKeyStats{
		TotalRequests:  st.TotalRequests,
		TotalInputTkn:  st.TotalInputTkn,
		TotalOutputTkn: st.TotalOutputTkn,
		TotalCost:      st.TotalCost,
		Disabled:       st.Disabled,
		Dead:           st.Dead,
		DeadReason:     st.DeadReason,
		CooldownUntil:  st.CooldownUntil,
	}
}

func (k *APIKey) updateState(fn func(s *apiKeyState)) {
	for {
		old := k.state.Load()
		var next apiKeyState
		if old != nil {
			next = *old
		}
		fn(&next)
		if k.state.CompareAndSwap(old, &next) {
			return
		}
	}
}

func (k *APIKey) ApplyStats(s APIKeyStats) {
	k.updateState(func(st *apiKeyState) {
		st.TotalRequests = s.TotalRequests
		st.TotalInputTkn = s.TotalInputTkn
		st.TotalOutputTkn = s.TotalOutputTkn
		st.TotalCost = s.TotalCost
		st.Disabled = s.Disabled
		st.Dead = s.Dead
		st.DeadReason = s.DeadReason
		st.CooldownUntil = s.CooldownUntil
	})
}

func (k *APIKey) RecordSuccess(input, output int64, cost float64) {
	k.updateState(func(st *apiKeyState) {
		st.TotalRequests++
		st.TotalInputTkn += input
		st.TotalOutputTkn += output
		st.TotalCost += cost
	})
}

func (k *APIKey) MarkCooldown(until time.Time) {
	k.updateState(func(st *apiKeyState) {
		st.CooldownUntil = until
	})
}

func (k *APIKey) MarkDead(reason string) {
	k.updateState(func(st *apiKeyState) {
		st.Dead = true
		st.DeadReason = reason
	})
}

func (k *APIKey) SetDisabled(v bool) {
	k.updateState(func(st *apiKeyState) {
		st.Disabled = v
	})
}

func (k *APIKey) SourcePath() string {
	st := k.state.Load()
	if st == nil {
		return ""
	}
	return st.SourcePath
}

func (k *APIKey) IsAvailable(now time.Time) bool {
	st := k.state.Load()
	if st == nil {
		return true
	}
	if st.Disabled || st.Dead {
		return false
	}
	if !st.CooldownUntil.IsZero() && st.CooldownUntil.After(now) {
		return false
	}
	return true
}

type APIKeyPool struct {
	mu   sync.RWMutex
	keys map[string]*apiKeyEntry
}

type apiKeyEntry struct {
	key      *APIKey
	inflight atomic.Int64
}

func NewAPIKeyPool() *APIKeyPool {
	return &APIKeyPool{keys: make(map[string]*apiKeyEntry)}
}

func (p *APIKeyPool) Add(k *APIKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys[k.ID] = &apiKeyEntry{key: k}
}

func (p *APIKeyPool) Remove(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.keys, id)
}

func (p *APIKeyPool) Get(id string) (*APIKey, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e, ok := p.keys[id]
	if !ok {
		return nil, false
	}
	return e.key, true
}

func (p *APIKeyPool) List() []*APIKey {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*APIKey, 0, len(p.keys))
	for _, e := range p.keys {
		out = append(out, e.key)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (p *APIKeyPool) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.keys)
}

func LoadAPIKeyFile(path string) (*APIKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var k APIKey
	if err := json.Unmarshal(raw, &k); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if k.APIKey == "" {
		return nil, fmt.Errorf("%s: missing api_key", path)
	}
	if k.Type == "" {
		k.Type = KeyTypeOpenAI
	}
	if k.ID == "" {
		k.ID = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	if k.Name == "" {
		k.Name = k.ID
	}
	k.state.Store(&apiKeyState{SourcePath: path})
	return &k, nil
}

func (p *APIKeyPool) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		k, err := LoadAPIKeyFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			return err
		}
		p.Add(k)
	}
	return nil
}

type APIKeyLease struct {
	Key  *APIKey
	pool *APIKeyPool
}

func (l *APIKeyLease) Release() {
	if l == nil || l.pool == nil {
		return
	}
	p := l.pool
	p.mu.RLock()
	e := p.keys[l.Key.ID]
	p.mu.RUnlock()
	if e != nil {
		e.inflight.Add(-1)
	}
	l.pool = nil
}

var ErrNoAPIKeys = errors.New("apikey: no keys configured")

func (p *APIKeyPool) Lease(_ context.Context, types []APIKeyType) (*APIKeyLease, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.keys) == 0 {
		return nil, ErrNoAPIKeys
	}
	now := time.Now()
	allow := func(t APIKeyType) bool {
		if len(types) == 0 {
			return true
		}
		for _, want := range types {
			if want == t {
				return true
			}
		}
		return false
	}
	candidates := make([]*apiKeyEntry, 0, len(p.keys))
	for _, e := range p.keys {
		if !allow(e.key.Type) {
			continue
		}
		if !e.key.IsAvailable(now) {
			continue
		}
		candidates = append(candidates, e)
	}
	if len(candidates) == 0 {
		return nil, ErrAllExhausted
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		ii := tokenLoad(candidates[i].key)
		jj := tokenLoad(candidates[j].key)
		if ii != jj {
			return ii < jj
		}
		return candidates[i].key.ID < candidates[j].key.ID
	})
	chosen := candidates[0]
	chosen.inflight.Add(1)
	return &APIKeyLease{Key: chosen.key, pool: p}, nil
}

func tokenLoad(k *APIKey) int64 {
	st := k.state.Load()
	if st == nil {
		return 0
	}
	return st.TotalInputTkn + st.TotalOutputTkn
}
