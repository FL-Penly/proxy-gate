package pricing

import (
	"maps"
	"strings"
	"sync"
	"time"
)

var codexAliases = map[string]string{
	"gpt-5-codex":   "gpt-5",
	"gpt-5.3-codex": "gpt-5.2-codex",
}

var modelAliases = map[string]string{
	"claude-haiku-4-5-20251001": "claude-haiku-4-5",
}

var fallbackPrices = map[string]CompactPrice{
	"claude-haiku-4-5":  {InputCost: 1e-6, OutputCost: 5e-6, CacheReadCost: 1e-7},
	"claude-sonnet-4-6": {InputCost: 3e-6, OutputCost: 15e-6, CacheReadCost: 3e-7},
	"claude-opus-4-7":   {InputCost: 15e-6, OutputCost: 75e-6, CacheReadCost: 1.5e-6},
}

var providerPrefixes = []string{
	"openai/",
	"azure/",
	"openrouter/openai/",
	"anthropic/",
	"openrouter/anthropic/",
}

type Source struct {
	mu       sync.RWMutex
	snapshot *Snapshot

	missMu sync.Mutex
	misses map[string]int64
}

func NewSource(initial *Snapshot) *Source {
	if initial == nil {
		initial = &Snapshot{Models: map[string]CompactPrice{}, Origin: OriginEmbedded}
	}
	return &Source{
		snapshot: initial,
		misses:   make(map[string]int64),
	}
}

func (s *Source) Snapshot() *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func (s *Source) Replace(snap *Snapshot) {
	if snap == nil || len(snap.Models) == 0 {
		return
	}
	if snap.FetchedAt.IsZero() {
		snap.FetchedAt = time.Now().UTC()
	}
	s.mu.Lock()
	s.snapshot = snap
	s.mu.Unlock()
}

func (s *Source) Lookup(model string) (CompactPrice, bool) {
	if model == "" || model == "unknown" {
		return CompactPrice{}, false
	}
	snap := s.Snapshot()
	if snap == nil {
		return CompactPrice{}, false
	}
	if p, ok := lookupIn(snap.Models, model); ok {
		return p, true
	}
	if alias, ok := codexAliases[model]; ok {
		if p, ok := lookupIn(snap.Models, alias); ok {
			return p, true
		}
	}
	if alias, ok := modelAliases[model]; ok {
		if p, ok := lookupIn(snap.Models, alias); ok {
			return p, true
		}
		if p, ok := fallbackPrices[alias]; ok && p.HasPricing() {
			return p, true
		}
	}
	if p, ok := fallbackPrices[model]; ok && p.HasPricing() {
		return p, true
	}
	return CompactPrice{}, false
}

func (s *Source) RecordMiss(billingKey string) {
	if billingKey == "" {
		return
	}
	s.missMu.Lock()
	s.misses[billingKey]++
	s.missMu.Unlock()
}

func (s *Source) Misses() map[string]int64 {
	s.missMu.Lock()
	defer s.missMu.Unlock()
	out := make(map[string]int64, len(s.misses))
	maps.Copy(out, s.misses)
	return out
}

func lookupIn(models map[string]CompactPrice, model string) (CompactPrice, bool) {
	if p, ok := models[model]; ok && p.HasPricing() {
		return p, true
	}
	for _, prefix := range providerPrefixes {
		if p, ok := models[prefix+model]; ok && p.HasPricing() {
			return p, true
		}
	}
	for _, prefix := range providerPrefixes {
		if stripped, ok := strings.CutPrefix(model, prefix); ok {
			if p, ok := models[stripped]; ok && p.HasPricing() {
				return p, true
			}
		}
	}
	return CompactPrice{}, false
}

func BillingKey(model, tier string) string {
	if model == "" || model == "unknown" {
		return ""
	}
	t := strings.ToLower(strings.TrimSpace(tier))
	if t == "" || t == "default" || t == "auto" {
		return model
	}
	return model + "@" + t
}
