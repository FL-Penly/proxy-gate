package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const LiteLLMURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

const (
	defaultRefreshInterval = 24 * time.Hour
	defaultStartupDelay    = 5 * time.Second
	defaultFetchTimeout    = 30 * time.Second
)

type FetcherConfig struct {
	URL             string
	HTTPClient      *http.Client
	RefreshInterval time.Duration
	StartupDelay    time.Duration
	FetchTimeout    time.Duration
	Logger          *slog.Logger
}

type Fetcher struct {
	cfg    FetcherConfig
	source *Source

	mu          sync.Mutex
	lastAttempt time.Time
	lastSuccess time.Time
	lastErr     error
}

func NewFetcher(source *Source, cfg FetcherConfig) *Fetcher {
	if cfg.URL == "" {
		cfg.URL = LiteLLMURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: defaultFetchTimeout}
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = defaultRefreshInterval
	}
	if cfg.StartupDelay <= 0 {
		cfg.StartupDelay = defaultStartupDelay
	}
	if cfg.FetchTimeout <= 0 {
		cfg.FetchTimeout = defaultFetchTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Fetcher{cfg: cfg, source: source}
}

func (f *Fetcher) Run(ctx context.Context) {
	timer := time.NewTimer(f.cfg.StartupDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		f.refreshOnce(ctx)
		timer.Reset(f.cfg.RefreshInterval)
	}
}

func (f *Fetcher) Refresh(ctx context.Context) error {
	return f.refreshOnce(ctx)
}

type fetcherStatus struct {
	URL         string    `json:"url"`
	LastAttempt time.Time `json:"last_attempt_at"`
	LastSuccess time.Time `json:"last_success_at"`
	LastError   string    `json:"last_error,omitempty"`
}

func (f *Fetcher) Status() fetcherStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := fetcherStatus{
		URL:         f.cfg.URL,
		LastAttempt: f.lastAttempt,
		LastSuccess: f.lastSuccess,
	}
	if f.lastErr != nil {
		st.LastError = f.lastErr.Error()
	}
	return st
}

func (f *Fetcher) refreshOnce(ctx context.Context) error {
	f.mu.Lock()
	f.lastAttempt = time.Now().UTC()
	f.mu.Unlock()

	fctx, cancel := context.WithTimeout(ctx, f.cfg.FetchTimeout)
	defer cancel()

	snap, err := fetchAndParse(fctx, f.cfg.HTTPClient, f.cfg.URL)
	if err != nil {
		f.cfg.Logger.Warn("pricing refresh failed", "err", err, "url", f.cfg.URL)
		f.mu.Lock()
		f.lastErr = err
		f.mu.Unlock()
		return err
	}

	f.source.Replace(snap)
	f.mu.Lock()
	f.lastSuccess = time.Now().UTC()
	f.lastErr = nil
	f.mu.Unlock()
	f.cfg.Logger.Info("pricing refreshed", "models", len(snap.Models), "url", f.cfg.URL)
	return nil
}

func fetchAndParse(ctx context.Context, client *http.Client, url string) (*Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("pricing: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pricing: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pricing: http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("pricing: read body: %w", err)
	}
	return parseLiteLLM(body)
}

func parseLiteLLM(body []byte) (*Snapshot, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("pricing: parse json: %w", err)
	}
	models := make(map[string]CompactPrice, 200)
	for name, val := range raw {
		var entry struct {
			Provider string `json:"litellm_provider"`
			Mode     string `json:"mode"`
			CompactPrice
		}
		if err := json.Unmarshal(val, &entry); err != nil {
			continue
		}
		if entry.Provider != "openai" {
			continue
		}
		if entry.Mode != "" && entry.Mode != "chat" && entry.Mode != "responses" {
			continue
		}
		if !entry.CompactPrice.HasPricing() {
			continue
		}
		models[name] = entry.CompactPrice
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("pricing: zero openai models in response")
	}
	return &Snapshot{
		Models:    models,
		FetchedAt: time.Now().UTC(),
		Origin:    OriginLiteLLM,
	}, nil
}
