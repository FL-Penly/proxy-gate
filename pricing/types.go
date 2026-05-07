package pricing

import "time"

type CompactPrice struct {
	InputCost             float64 `json:"input_cost_per_token,omitempty"`
	InputCostPriority     float64 `json:"input_cost_per_token_priority,omitempty"`
	InputCostFlex         float64 `json:"input_cost_per_token_flex,omitempty"`
	OutputCost            float64 `json:"output_cost_per_token,omitempty"`
	OutputCostPriority    float64 `json:"output_cost_per_token_priority,omitempty"`
	OutputCostFlex        float64 `json:"output_cost_per_token_flex,omitempty"`
	CacheReadCost         float64 `json:"cache_read_input_token_cost,omitempty"`
	CacheReadCostPriority float64 `json:"cache_read_input_token_cost_priority,omitempty"`
	CacheReadCostFlex     float64 `json:"cache_read_input_token_cost_flex,omitempty"`
}

func (p CompactPrice) HasPricing() bool {
	return p.InputCost > 0 || p.OutputCost > 0 || p.CacheReadCost > 0
}

const (
	OriginEmbedded = "embedded"
	OriginLiteLLM  = "litellm"
	OriginBolt     = "bbolt"
)

type Snapshot struct {
	Models    map[string]CompactPrice
	FetchedAt time.Time
	Origin    string
}
