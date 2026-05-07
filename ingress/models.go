package ingress

import (
	"encoding/json"
	"net/http"
	"time"
)

type modelsResponse struct {
	Object string       `json:"object"`
	Data   []modelEntry `json:"data"`
	Models []modelEntry `json:"models"`
}

type modelEntry struct {
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

var defaultModels = []string{
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.4-mini",
	"o3",
	"o4-mini",
	"gpt-4.1",
	"gpt-4.1-mini",
	"gpt-4.1-nano",
	"gpt-4o",
	"gpt-4o-mini",
}

type ModelsHandler struct{}

func (h *ModelsHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().Unix()
	data := make([]modelEntry, len(defaultModels))
	for i, id := range defaultModels {
		data[i] = modelEntry{
			ID:      id,
			Slug:    id,
			Object:  "model",
			Created: now,
			OwnedBy: "system",
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(modelsResponse{
		Object: "list",
		Data:   data,
		Models: data,
	})
}
