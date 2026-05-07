package pricing

import (
	"math"
	"testing"
)

const epsilon = 1e-9

func nearlyEqual(a, b float64) bool {
	return math.Abs(a-b) < epsilon
}

func TestCalculateGPT5Default(t *testing.T) {
	src := newSourceWithFixtures()
	cost, ok := src.Calculate(Tokens{Input: 1_000_000, Output: 0}, "gpt-5", "")
	if !ok {
		t.Fatal("gpt-5 should be priced")
	}
	if !nearlyEqual(cost, 1.25) {
		t.Errorf("1M input gpt-5 = $1.25, got $%.6f", cost)
	}
}

func TestCalculatePriorityTier(t *testing.T) {
	src := newSourceWithFixtures()
	cost, ok := src.Calculate(Tokens{Input: 1_000_000, Output: 0}, "gpt-5", "priority")
	if !ok {
		t.Fatal("not priced")
	}
	if !nearlyEqual(cost, 2.50) {
		t.Errorf("1M input gpt-5 priority = $2.50, got $%.6f", cost)
	}
}

func TestCalculateFlexTier(t *testing.T) {
	src := newSourceWithFixtures()
	cost, ok := src.Calculate(Tokens{Input: 1_000_000, Output: 0}, "gpt-5", "flex")
	if !ok {
		t.Fatal("not priced")
	}
	if !nearlyEqual(cost, 0.625) {
		t.Errorf("1M input gpt-5 flex = $0.625, got $%.6f", cost)
	}
}

func TestCalculateTierFallsBackToBaseWhenMissing(t *testing.T) {
	models := map[string]CompactPrice{
		"gpt-5.1-codex-max": {
			InputCost:     1.25e-6,
			OutputCost:    1e-5,
			CacheReadCost: 1.25e-7,
		},
	}
	src := NewSource(&Snapshot{Models: models, Origin: OriginEmbedded})
	cost, ok := src.Calculate(Tokens{Input: 1_000_000}, "gpt-5.1-codex-max", "priority")
	if !ok {
		t.Fatal("not priced")
	}
	if !nearlyEqual(cost, 1.25) {
		t.Errorf("missing _priority must fall back to base ($1.25), got $%.6f", cost)
	}
}

func TestCalculateCachedTokensUseCacheRate(t *testing.T) {
	src := newSourceWithFixtures()
	cost, ok := src.Calculate(Tokens{Input: 1_000_000, Cached: 1_000_000}, "gpt-5", "")
	if !ok {
		t.Fatal("not priced")
	}
	want := 1.25e-7 * 1_000_000
	if !nearlyEqual(cost, want) {
		t.Errorf("all-cached input should use cache rate ($%.6f), got $%.6f", want, cost)
	}
}

func TestCalculateMixedCachedUncached(t *testing.T) {
	src := newSourceWithFixtures()
	cost, ok := src.Calculate(Tokens{Input: 1_000_000, Cached: 200_000, Output: 500_000}, "gpt-5", "")
	if !ok {
		t.Fatal("not priced")
	}
	want := 800_000*1.25e-6 + 200_000*1.25e-7 + 500_000*1e-5
	if !nearlyEqual(cost, want) {
		t.Errorf("mixed cost: want $%.6f got $%.6f", want, cost)
	}
}

func TestCalculateUnknownModelReturnsZero(t *testing.T) {
	src := newSourceWithFixtures()
	cost, ok := src.Calculate(Tokens{Input: 1000, Output: 500}, "claude-future-9000", "")
	if ok {
		t.Error("unknown model should return ok=false")
	}
	if cost != 0 {
		t.Errorf("unknown model cost must be 0, got %v", cost)
	}
}

func TestCalculateClampsCachedToInput(t *testing.T) {
	src := newSourceWithFixtures()
	cost, ok := src.Calculate(Tokens{Input: 100, Cached: 999, Output: 0}, "gpt-5", "")
	if !ok {
		t.Fatal("not priced")
	}
	if cost < 0 {
		t.Errorf("cost must not be negative, got %v", cost)
	}
	want := float64(100) * 1.25e-7
	if !nearlyEqual(cost, want) {
		t.Errorf("cached>input must clamp to input, billing 100 cached tokens at cache rate: want $%.9f got $%.9f", want, cost)
	}
}

func TestCalculateCacheRateFallsBackThroughTierThenBaseThenInput(t *testing.T) {
	models := map[string]CompactPrice{
		"only-input": {InputCost: 1e-5, OutputCost: 5e-5},
		"flex-input-base-cache": {
			InputCost:        1.5e-5,
			InputCostFlex:    7e-6,
			CacheReadCost:    3e-6,
			OutputCost:       3e-5,
			OutputCostFlex:   1.5e-5,
		},
	}
	src := NewSource(&Snapshot{Models: models, Origin: OriginEmbedded})

	cost, ok := src.Calculate(Tokens{Input: 1000, Cached: 200, Output: 0}, "only-input", "")
	if !ok {
		t.Fatal("not priced")
	}
	want := float64(800)*1e-5 + float64(200)*1e-5
	if !nearlyEqual(cost, want) {
		t.Errorf("missing cache rate falls back to input rate: want $%.9f got $%.9f", want, cost)
	}

	cost, ok = src.Calculate(Tokens{Input: 1000, Cached: 200, Output: 100}, "flex-input-base-cache", "flex")
	if !ok {
		t.Fatal("not priced")
	}
	want = float64(800)*7e-6 + float64(200)*3e-6 + float64(100)*1.5e-5
	if !nearlyEqual(cost, want) {
		t.Errorf("missing flex cache: 800*flex_in + 200*base_cache + 100*flex_out: want $%.9f got $%.9f", want, cost)
	}
}

func TestCalculateGPT54Priority(t *testing.T) {
	src := newSourceWithFixtures()
	cost, ok := src.Calculate(Tokens{Input: 1_000_000, Output: 0}, "gpt-5.4", "priority")
	if !ok {
		t.Fatal("not priced")
	}
	if !nearlyEqual(cost, 5.0) {
		t.Errorf("1M input gpt-5.4 priority = $5.00, got $%.6f", cost)
	}
}

func TestCalculateGPT55Default(t *testing.T) {
	src := newSourceWithFixtures()
	cost, ok := src.Calculate(Tokens{Input: 1_000_000, Output: 0}, "gpt-5.5", "")
	if !ok {
		t.Fatal("not priced")
	}
	if !nearlyEqual(cost, 5.0) {
		t.Errorf("1M input gpt-5.5 = $5.00, got $%.6f", cost)
	}
}

func TestCalculateGPT55Priority(t *testing.T) {
	src := newSourceWithFixtures()
	cost, ok := src.Calculate(Tokens{Input: 1_000_000, Output: 0}, "gpt-5.5", "priority")
	if !ok {
		t.Fatal("not priced")
	}
	if !nearlyEqual(cost, 10.0) {
		t.Errorf("1M input gpt-5.5 priority = $10.00, got $%.6f", cost)
	}
}

func TestCalculateGPT54MiniSmallReq(t *testing.T) {
	src := newSourceWithFixtures()
	cost, ok := src.Calculate(Tokens{Input: 1000, Output: 500}, "gpt-5.4-mini", "")
	if !ok {
		t.Fatal("not priced")
	}
	want := 1000*7.5e-7 + 500*4.5e-6
	if !nearlyEqual(cost, want) {
		t.Errorf("gpt-5.4-mini: want $%.9f got $%.9f", want, cost)
	}
}
