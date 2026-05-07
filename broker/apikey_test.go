package broker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func osWrite(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

func mkKey(id string, totalInput, totalOutput int64) *APIKey {
	k := &APIKey{ID: id, Name: id, Type: KeyTypeOpenAI, APIKey: "sk-" + id}
	k.state.Store(&apiKeyState{
		TotalInputTkn:  totalInput,
		TotalOutputTkn: totalOutput,
		TotalRequests:  1000,
	})
	return k
}

func TestAPIKeyLoadBalancesByTokensNotRequests(t *testing.T) {
	pool := NewAPIKeyPool()

	heavy := mkKey("heavy", 10_000_000, 5_000_000)
	heavy.state.Store(&apiKeyState{
		TotalInputTkn: 10_000_000, TotalOutputTkn: 5_000_000, TotalRequests: 5,
	})

	light := mkKey("light", 1_000, 500)
	light.state.Store(&apiKeyState{
		TotalInputTkn: 1_000, TotalOutputTkn: 500, TotalRequests: 200,
	})

	pool.Add(heavy)
	pool.Add(light)

	lease, err := pool.Lease(context.Background(), nil)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	defer lease.Release()
	if lease.Key.ID != "light" {
		t.Errorf("chose %q, want light (less tokens) — fixes v1 api-key-manager.js:198-204 bug", lease.Key.ID)
	}
}

func TestAPIKeyTypeFilter(t *testing.T) {
	pool := NewAPIKeyPool()
	pool.Add(&APIKey{ID: "a", Type: KeyTypeOpenAI, APIKey: "sk-a"})
	pool.Add(&APIKey{ID: "b", Type: KeyTypeAnthropic, APIKey: "sk-ant-b"})
	pool.List()

	lease, err := pool.Lease(context.Background(), []APIKeyType{KeyTypeAnthropic})
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	defer lease.Release()
	if lease.Key.ID != "b" {
		t.Errorf("chose %q, want b (Anthropic-only filter)", lease.Key.ID)
	}
}

func TestAPIKeyLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "openai-1.json")
	json := `{"id":"openai-1","name":"first","type":"openai","api_key":"sk-test","base_url":"https://api.openai.com/v1"}`
	if err := writeFile(path, json); err != nil {
		t.Fatalf("write: %v", err)
	}
	k, err := LoadAPIKeyFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if k.ID != "openai-1" || k.Name != "first" || k.APIKey != "sk-test" || k.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("loaded incorrectly: %+v", k)
	}
	if k.SourcePath() != path {
		t.Errorf("SourcePath = %q", k.SourcePath())
	}
}

func writeFile(path, body string) error {
	return osWrite(path, []byte(body))
}
