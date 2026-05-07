package pricing

import "strings"

type Tokens struct {
	Input        int64
	Cached       int64
	Output       int64
}

func (s *Source) CalculateTokens(input, cached, output int64, model, tier string) (cost float64, priced bool) {
	return s.Calculate(Tokens{Input: input, Cached: cached, Output: output}, model, tier)
}

func (s *Source) Calculate(t Tokens, model, tier string) (cost float64, priced bool) {
	price, ok := s.Lookup(model)
	if !ok {
		return 0, false
	}
	tierKey := normalizeTier(tier)
	inRate := pickRate(price.InputCost, price.InputCostPriority, price.InputCostFlex, tierKey)
	outRate := pickRate(price.OutputCost, price.OutputCostPriority, price.OutputCostFlex, tierKey)
	cacheRate := pickRate(price.CacheReadCost, price.CacheReadCostPriority, price.CacheReadCostFlex, tierKey)

	cached := min(t.Cached, t.Input)
	uncachedInput := t.Input - cached
	if cacheRate == 0 {
		cacheRate = inRate
	}
	cost = float64(uncachedInput)*inRate + float64(cached)*cacheRate + float64(t.Output)*outRate
	return cost, true
}

func normalizeTier(tier string) string {
	t := strings.ToLower(strings.TrimSpace(tier))
	switch t {
	case "priority":
		return "priority"
	case "flex":
		return "flex"
	default:
		return ""
	}
}

func pickRate(base, priority, flex float64, tier string) float64 {
	switch tier {
	case "priority":
		if priority > 0 {
			return priority
		}
	case "flex":
		if flex > 0 {
			return flex
		}
	}
	return base
}
