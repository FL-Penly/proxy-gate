package ingress

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/FL-Penly/proxy-gate/broker"
	"github.com/FL-Penly/proxy-gate/provider"
	"github.com/FL-Penly/proxy-gate/relay"
	"github.com/tidwall/gjson"
)

type MessagesHandler struct {
	ClaudePool      *broker.ClaudePool
	KeyPool         *broker.APIKeyPool
	Anthropic       *provider.AnthropicClient
	Vertex          *provider.AnthropicVertexClient
	Recorder        RequestRecorder
	ClaudeRefresher ClaudeTokenRefresher
	Pricer          Pricer
	Priority        string
	FallbackPolicy  ClaudeFallbackPolicy
	FallbackRuntime *ClaudeFallbackRuntime
	Logger          *slog.Logger
}

type ClaudeTokenRefresher interface {
	RefreshClaudeToken(ctx context.Context, acc *broker.ClaudeAccount) error
}

func (h *MessagesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	logger := h.logger()

	body, _, err := ReadAndDecompress(r)
	if err != nil {
		logger.Warn("decompress failed", "err", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	model := gjson.GetBytes(body, "model").String()
	streaming := gjson.GetBytes(body, "stream").Bool()

	if h.totalConfigured() == 0 {
		writeJSONError(w, http.StatusUnauthorized, "No Claude accounts or Anthropic API keys configured.")
		return
	}
	for _, src := range h.sourceOrder() {
		switch src {
		case sourceAccount:
			outcome := h.serveViaClaudePool(w, r, body, model, streaming, start)
			if outcome != outcomeSourceUnavailable {
				return
			}
		case sourceVertexAI:
			outcome := h.serveViaVertexFallback(w, r, body, streaming, start)
			if outcome != outcomeSourceUnavailable {
				return
			}
		case sourceAPIKey:
			outcome := h.serveViaAnthropicKeyPool(w, r, body, model, streaming, start)
			if outcome != outcomeSourceUnavailable {
				return
			}
		}
	}
	if h.fallbackEnabled() {
		writeJSONError(w, http.StatusServiceUnavailable, "All Claude accounts exhausted and no Vertex AI fallback matched or succeeded.")
		return
	}
	writeJSONError(w, http.StatusServiceUnavailable, "All Claude accounts and Anthropic API keys exhausted.")
}

func (h *MessagesHandler) totalConfigured() int {
	n := 0
	if h.ClaudePool != nil {
		n += h.ClaudePool.Len()
	}
	if h.fallbackEnabled() && h.Vertex != nil {
		n++
	}
	if h.KeyPool != nil {
		for _, k := range h.KeyPool.List() {
			if k.Type == broker.KeyTypeAnthropic {
				n++
			}
		}
	}
	return n
}

func (h *MessagesHandler) sourceOrder() []sourceKind {
	if h.fallbackEnabled() {
		return []sourceKind{sourceAccount, sourceVertexAI}
	}
	if h.ClaudePool == nil && h.KeyPool == nil {
		return nil
	}
	if h.ClaudePool == nil {
		return []sourceKind{sourceAPIKey}
	}
	if h.KeyPool == nil {
		return []sourceKind{sourceAccount}
	}
	if h.Priority == "apikey-first" {
		return []sourceKind{sourceAPIKey, sourceAccount}
	}
	return []sourceKind{sourceAccount, sourceAPIKey}
}

func (h *MessagesHandler) fallbackEnabled() bool {
	if h.FallbackRuntime != nil {
		return h.FallbackRuntime.Enabled()
	}
	return h.FallbackPolicy.Enabled
}

func (h *MessagesHandler) fallbackMatch(body []byte, headers http.Header) (ClaudeFallbackMatch, bool) {
	if h.FallbackRuntime != nil {
		return h.FallbackRuntime.Match(body, headers)
	}
	return h.FallbackPolicy.Match(body, headers)
}

func (h *MessagesHandler) serveViaClaudePool(w http.ResponseWriter, r *http.Request, body []byte, model string, streaming bool, start time.Time) sourceOutcome {
	if h.ClaudePool == nil || h.ClaudePool.Len() == 0 {
		return outcomeSourceUnavailable
	}
	logger := h.logger()
	level5xx := 0
	refreshed := make(map[string]bool)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		lease, err := h.ClaudePool.Lease(r.Context())
		if err != nil {
			if errors.Is(err, broker.ErrNoAccounts) {
				return outcomeSourceUnavailable
			}
			if errors.Is(err, broker.ErrAllExhausted) {
				if attempt+1 < maxAttempts {
					wait := minDuration(h.ClaudePool.NearestCooldown(time.Now()), time.Duration(exhaustedRetryWaitMs)*time.Millisecond)
					if wait > 0 && sleepCtx(r.Context(), wait) {
						continue
					}
				}
				return outcomeSourceUnavailable
			}
			writeJSONError(w, http.StatusInternalServerError, "claude pool error")
			return outcomeFailed
		}

		resp, err := h.Anthropic.Forward(r.Context(), provider.AnthropicForwardRequest{
			Body:            body,
			AccessToken:     lease.Account.AccessToken,
			AuthMode:        provider.AnthropicAuthOAuth,
			Streaming:       streaming,
			IncomingHeaders: r.Header,
		})
		if err != nil {
			lease.Release()
			if ctxCanceled(r.Context()) {
				return outcomeFailed
			}
			logger.Warn("claude forward error", "err", err, "account", lease.Account.Email)
			continue
		}

		if resp.StatusCode == http.StatusUnauthorized {
			_ = resp.Body.Close()
			if h.ClaudeRefresher != nil && lease.Account.RefreshToken != "" && !refreshed[lease.Account.Email] {
				refreshed[lease.Account.Email] = true
				if err := h.ClaudeRefresher.RefreshClaudeToken(r.Context(), lease.Account); err != nil {
					lease.Account.MarkDead("refresh: " + err.Error())
					logger.Warn("claude token refresh failed", "err", err, "account", lease.Account.Email)
					lease.Release()
					continue
				}
				lease.Release()
				attempt--
				continue
			}
			lease.Account.MarkDead("auth_unauthorized")
			lease.Release()
			continue
		}
		if resp.StatusCode == http.StatusForbidden {
			_ = resp.Body.Close()
			lease.Account.MarkDead("auth_forbidden")
			lease.Release()
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			cooldown := broker.ParseRetryAfter(resp.Header, b, time.Now())
			lease.Account.MarkCooldown(time.Now().Add(cooldown))
			lease.Release()
			continue
		}
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			_ = resp.Body.Close()
			level5xx++
			if !sleepCtx(r.Context(), broker.BackoffFor5xx(level5xx-1)) {
				lease.Release()
				return outcomeFailed
			}
			lease.Release()
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			writeUpstreamError(w, resp, body)
			lease.Release()
			return outcomeFailed
		}

		rec := h.serveSuccess(w, resp, UsageRecord{Account: lease.Account.Email, Status: resp.StatusCode, Success: true, Model: model}, streaming)
		rec.Account = lease.Account.Email
		rec.Provider = "claude-pool"
		rec.Route = "/v1/messages"
		rec.DurationMs = time.Since(start).Milliseconds()
		if rec.Model == "" {
			rec.Model = model
		}
		h.priceRec(&rec)
		if rec.Success {
			lease.Account.RecordSuccess(rec.InputTokens, rec.OutputTokens, rec.Cost)
		}
		h.record(rec)
		lease.Release()
		return outcomeServed
	}
	return outcomeSourceUnavailable
}

func (h *MessagesHandler) serveViaAnthropicKeyPool(w http.ResponseWriter, r *http.Request, body []byte, model string, streaming bool, start time.Time) sourceOutcome {
	if h.KeyPool == nil || h.KeyPool.Len() == 0 {
		return outcomeSourceUnavailable
	}
	logger := h.logger()

	level5xx := 0
	for attempt := 0; attempt < maxAttempts; attempt++ {
		lease, err := h.KeyPool.Lease(r.Context(), []broker.APIKeyType{broker.KeyTypeAnthropic})
		if err != nil {
			if errors.Is(err, broker.ErrNoAPIKeys) || errors.Is(err, broker.ErrAllExhausted) {
				return outcomeSourceUnavailable
			}
			writeJSONError(w, http.StatusInternalServerError, "key pool error")
			return outcomeFailed
		}

		resp, err := h.Anthropic.Forward(r.Context(), provider.AnthropicForwardRequest{
			Body:            body,
			APIKey:          lease.Key.APIKey,
			BaseURL:         lease.Key.BaseURL,
			Streaming:       streaming,
			IncomingHeaders: r.Header,
		})
		if err != nil {
			lease.Release()
			if ctxCanceled(r.Context()) {
				return outcomeFailed
			}
			logger.Warn("anthropic forward error", "err", err, "key", lease.Key.ID)
			continue
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			_ = resp.Body.Close()
			lease.Key.MarkDead("auth_" + http.StatusText(resp.StatusCode))
			lease.Release()
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			cooldown := broker.ParseRetryAfter(resp.Header, b, time.Now())
			lease.Key.MarkCooldown(time.Now().Add(cooldown))
			lease.Release()
			continue
		}
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			_ = resp.Body.Close()
			level5xx++
			if !sleepCtx(r.Context(), broker.BackoffFor5xx(level5xx-1)) {
				lease.Release()
				return outcomeFailed
			}
			lease.Release()
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			writeUpstreamError(w, resp, b)
			lease.Release()
			return outcomeFailed
		}

		rec := h.serveSuccess(w, resp, UsageRecord{KeyID: lease.Key.ID, Status: resp.StatusCode, Success: true, Model: model}, streaming)
		if rec.Success {
			lease.Key.RecordSuccess(rec.InputTokens, rec.OutputTokens, 0)
		}
		rec.KeyID = lease.Key.ID
		rec.Provider = "anthropic-key"
		rec.Route = "/v1/messages"
		rec.DurationMs = time.Since(start).Milliseconds()
		if rec.Model == "" {
			rec.Model = model
		}
		h.record(rec)
		lease.Release()
		return outcomeServed
	}
	return outcomeSourceUnavailable
}

func (h *MessagesHandler) serveViaVertexFallback(w http.ResponseWriter, r *http.Request, body []byte, streaming bool, start time.Time) sourceOutcome {
	if h.Vertex == nil || !h.fallbackEnabled() {
		return outcomeSourceUnavailable
	}
	match, ok := h.fallbackMatch(body, r.Header)
	if !ok {
		return outcomeSourceUnavailable
	}
	targetBody, err := ApplyClaudeTarget(body, match.ToModel, match.ToVariant)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid claude fallback variant: "+err.Error())
		return outcomeFailed
	}
	logger := h.logger()
	level5xx := 0
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err := h.Vertex.Forward(r.Context(), provider.AnthropicVertexForwardRequest{
			Body:            targetBody,
			Model:           match.ToModel,
			Streaming:       streaming,
			IncomingHeaders: r.Header,
		})
		if err != nil {
			if ctxCanceled(r.Context()) {
				return outcomeFailed
			}
			logger.Warn("vertex anthropic forward error", "err", err, "from_model", match.FromModel, "to_model", match.ToModel)
			if attempt+1 < maxAttempts && sleepCtx(r.Context(), broker.BackoffFor5xx(level5xx)) {
				level5xx++
				continue
			}
			writeJSONError(w, http.StatusBadGateway, "vertex anthropic forward error")
			return outcomeFailed
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			if attempt+1 < maxAttempts {
				wait := broker.ParseRetryAfter(resp.Header, b, time.Now())
				if wait > 0 && sleepCtx(r.Context(), minDuration(wait, time.Duration(exhaustedRetryWaitMs)*time.Millisecond)) {
					continue
				}
			}
			writeJSONError(w, http.StatusTooManyRequests, "vertex anthropic rate limited")
			return outcomeFailed
		}
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			_ = resp.Body.Close()
			level5xx++
			if attempt+1 < maxAttempts && sleepCtx(r.Context(), broker.BackoffFor5xx(level5xx-1)) {
				continue
			}
			writeJSONError(w, http.StatusBadGateway, "vertex anthropic upstream unavailable")
			return outcomeFailed
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			writeUpstreamError(w, resp, b)
			return outcomeFailed
		}

		rec := h.serveSuccess(w, resp, UsageRecord{Status: resp.StatusCode, Success: true, Model: match.ToModel}, streaming)
		rec.KeyID = "vertex-ai"
		rec.Provider = "vertex-ai"
		rec.Route = "/v1/messages"
		rec.DurationMs = time.Since(start).Milliseconds()
		if rec.Model == "" || NormalizeClaudeModel(rec.Model) == match.FromModel {
			rec.Model = match.ToModel
		}
		h.priceRec(&rec)
		h.record(rec)
		return outcomeServed
	}
	return outcomeSourceUnavailable
}

func (h *MessagesHandler) serveSuccess(w http.ResponseWriter, resp *http.Response, rec UsageRecord, streaming bool) UsageRecord {
	defer resp.Body.Close()
	copyUpstreamResponseHeaders(w.Header(), resp.Header)
	if streaming {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(resp.StatusCode)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		extractor := relay.NewAnthropicUsageExtractor()
		buf := make([]byte, 16<<10)
		var writeErr, upstrErr error
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				_, _ = extractor.Write(chunk)
				if _, werr := w.Write(chunk); werr != nil {
					writeErr = werr
					break
				}
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			if rerr != nil {
				if rerr != io.EOF {
					upstrErr = rerr
				}
				break
			}
		}
		u := extractor.Done()
		rec.InputTokens = u.InputTokens
		rec.CachedTokens = u.CachedInputTokens
		rec.OutputTokens = u.OutputTokens
		rec.TotalTokens = u.TotalTokens
		if u.Model != "" {
			rec.Model = u.Model
		}
		if u.ResponseID != "" {
			rec.ResponseID = u.ResponseID
		}
		if writeErr != nil {
			rec.Success = false
			rec.Error = writeErr.Error()
		}
		if upstrErr != nil {
			rec.Success = false
			rec.Error = upstrErr.Error()
		}
		return rec
	}
	w.Header().Set("Content-Type", contentTypeOrJSON(resp.Header.Get("Content-Type")))
	w.WriteHeader(resp.StatusCode)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		rec.Success = false
		rec.Error = err.Error()
	}
	_, _ = w.Write(body)
	extractAnthropicUsage(&rec, body)
	return rec
}

func (h *MessagesHandler) priceRec(rec *UsageRecord) {
	if h.Pricer == nil || !rec.Success {
		return
	}
	cost, priced := h.Pricer.CalculateTokens(rec.InputTokens, rec.CachedTokens, rec.OutputTokens, rec.Model, rec.ServiceTier)
	if priced {
		rec.Cost = cost
		return
	}
	if rec.Model == "" || rec.Model == "unknown" {
		return
	}
	rec.CostUnpriced = true
	h.Pricer.RecordMiss(rec.Model + tierSuffix(rec.ServiceTier))
}

func writeUpstreamError(w http.ResponseWriter, resp *http.Response, body []byte) {
	copyUpstreamResponseHeaders(w.Header(), resp.Header)
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		w.Header().Set("Content-Type", "application/json")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func copyUpstreamResponseHeaders(dst, src http.Header) {
	for name, vals := range src {
		if isHopByHopHeader(name) || strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "x-api-key") {
			continue
		}
		dst.Del(name)
		for _, v := range vals {
			dst.Add(name, v)
		}
	}
}

func isHopByHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func (h *MessagesHandler) record(rec UsageRecord) {
	if h.Recorder != nil {
		h.Recorder.RecordRequest(rec)
	}
}

func (h *MessagesHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func extractAnthropicUsage(rec *UsageRecord, body []byte) {
	if len(body) == 0 {
		return
	}
	var raw struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens         int64 `json:"input_tokens"`
			OutputTokens        int64 `json:"output_tokens"`
			CacheReadInputToken int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return
	}
	rec.InputTokens = raw.Usage.InputTokens
	rec.CachedTokens = raw.Usage.CacheReadInputToken
	rec.OutputTokens = raw.Usage.OutputTokens
	rec.TotalTokens = rec.InputTokens + rec.OutputTokens
	if raw.ID != "" {
		rec.ResponseID = raw.ID
	}
	if raw.Model != "" {
		rec.Model = raw.Model
	}
}

var _ context.Context = nil
