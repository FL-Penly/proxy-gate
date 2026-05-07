package pricing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const fakeLiteLLM = `{
  "sample_spec": {"key": "drop me"},
  "gpt-5": {
    "litellm_provider": "openai",
    "mode": "chat",
    "input_cost_per_token": 1.25e-6,
    "input_cost_per_token_priority": 2.5e-6,
    "output_cost_per_token": 1e-5,
    "output_cost_per_token_priority": 2e-5,
    "cache_read_input_token_cost": 1.25e-7
  },
  "gpt-5.5": {
    "litellm_provider": "openai",
    "mode": "responses",
    "input_cost_per_token": 5e-6,
    "output_cost_per_token": 3e-5
  },
  "claude-future-9": {
    "litellm_provider": "anthropic",
    "input_cost_per_token": 1e-5,
    "output_cost_per_token": 5e-5
  },
  "whisper-1": {
    "litellm_provider": "openai",
    "mode": "audio_transcription",
    "input_cost_per_token": 1e-6
  },
  "chatgpt/gpt-5.4": {
    "litellm_provider": "chatgpt",
    "mode": "responses"
  }
}`

func TestParseLiteLLMFiltersOpenAI(t *testing.T) {
	snap, err := parseLiteLLM([]byte(fakeLiteLLM))
	if err != nil {
		t.Fatalf("parseLiteLLM: %v", err)
	}
	if _, ok := snap.Models["gpt-5"]; !ok {
		t.Error("gpt-5 missing")
	}
	if _, ok := snap.Models["gpt-5.5"]; !ok {
		t.Error("gpt-5.5 missing")
	}
	if _, ok := snap.Models["claude-future-9"]; ok {
		t.Error("anthropic entry should be filtered out")
	}
	if _, ok := snap.Models["whisper-1"]; ok {
		t.Error("non-chat/responses mode should be filtered out")
	}
	if _, ok := snap.Models["chatgpt/gpt-5.4"]; ok {
		t.Error("chatgpt provider entries should be filtered (no openai pricing)")
	}
	if snap.Origin != OriginLiteLLM {
		t.Errorf("origin=%q", snap.Origin)
	}
	if snap.FetchedAt.IsZero() {
		t.Error("FetchedAt should be set after parse")
	}
}

func TestParseLiteLLMRejectsZeroOpenAIModels(t *testing.T) {
	body := `{"claude-x": {"litellm_provider": "anthropic", "input_cost_per_token": 1e-6}}`
	if _, err := parseLiteLLM([]byte(body)); err == nil {
		t.Error("expected error when no openai models in body")
	}
}

func TestFetcherEndToEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeLiteLLM))
	}))
	defer server.Close()

	src := NewSource(&Snapshot{Models: map[string]CompactPrice{}, Origin: OriginEmbedded})
	f := NewFetcher(src, FetcherConfig{
		URL:             server.URL,
		HTTPClient:      server.Client(),
		RefreshInterval: time.Hour,
	})
	if err := f.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	snap := src.Snapshot()
	if snap.Origin != OriginLiteLLM {
		t.Errorf("origin=%q want %q", snap.Origin, OriginLiteLLM)
	}
	if _, ok := src.Lookup("gpt-5"); !ok {
		t.Error("gpt-5 not in source after refresh")
	}
	st := f.Status()
	if st.LastSuccess.IsZero() {
		t.Error("LastSuccess not recorded")
	}
	if st.LastError != "" {
		t.Errorf("LastError unexpectedly set: %q", st.LastError)
	}
}

func TestFetcherSilentlyKeepsOldSnapshotOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	embed, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	src := NewSource(embed)
	beforeOrigin := src.Snapshot().Origin
	beforeCount := len(src.Snapshot().Models)

	f := NewFetcher(src, FetcherConfig{URL: server.URL, HTTPClient: server.Client()})
	if err := f.Refresh(context.Background()); err == nil {
		t.Error("expected fetch error from 500 response")
	}
	if src.Snapshot().Origin != beforeOrigin {
		t.Error("failed fetch must not replace origin")
	}
	if len(src.Snapshot().Models) != beforeCount {
		t.Error("failed fetch must not replace models")
	}
	st := f.Status()
	if st.LastError == "" {
		t.Error("status should record fetch error")
	}
	if !st.LastSuccess.IsZero() {
		t.Error("failed fetch must not bump LastSuccess")
	}
}

func TestFetcherRunRespectsContextCancel(t *testing.T) {
	calls := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case calls <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeLiteLLM))
	}))
	defer server.Close()

	src := NewSource(&Snapshot{Models: map[string]CompactPrice{}, Origin: OriginEmbedded})
	f := NewFetcher(src, FetcherConfig{
		URL:             server.URL,
		HTTPClient:      server.Client(),
		StartupDelay:    10 * time.Millisecond,
		RefreshInterval: 10 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.Run(ctx)
		close(done)
	}()

	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("expected at least one fetch within 1s")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after context cancel")
	}
}
