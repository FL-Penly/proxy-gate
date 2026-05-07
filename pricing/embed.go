package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"time"
)

//go:embed embedded_prices.json
var embeddedJSON []byte

func LoadEmbedded() (*Snapshot, error) {
	models := make(map[string]CompactPrice)
	if err := json.Unmarshal(embeddedJSON, &models); err != nil {
		return nil, fmt.Errorf("pricing: parse embedded snapshot: %w", err)
	}
	return &Snapshot{
		Models:    models,
		FetchedAt: time.Time{},
		Origin:    OriginEmbedded,
	}, nil
}
