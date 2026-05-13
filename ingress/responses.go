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
)

const (
	maxAttempts          = 5
	exhaustedRetryWaitMs = 10_000
)

type RequestRecorder interface {
	RecordRequest(rec UsageRecord)
}

type Pricer interface {
	CalculateTokens(input, cached, output int64, model, tier string) (cost float64, priced bool)
	RecordMiss(billingKey string)
}

type UsageRecord struct {
	Account      string
	KeyID        string
	Provider     string
	Model        string
	ServiceTier  string
	Route        string
	InputTokens  int64
	CachedTokens int64
	OutputTokens int64
	ReasoningTkn int64
	TotalTokens  int64
	Cost         float64
	CostUnpriced bool
	DurationMs   int64
	Status       int
	Success      bool
	Error        string
	ResponseID   string
}

type TokenRefresher interface {
	RefreshToken(ctx context.Context, acc *broker.Account) error
}

type ResponsesHandler struct {
	Pool      *broker.Pool
	KeyPool   *broker.APIKeyPool
	ChatGPT   *provider.ChatGPTClient
	OpenAI    *provider.OpenAIClient
	Recorder  RequestRecorder
	Refresher TokenRefresher
	Pricer    Pricer
	Priority  string
	Logger    *slog.Logger
}

func (h *ResponsesHandler) priceRec(rec *UsageRecord) {
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

func tierSuffix(tier string) string {
	t := strings.ToLower(strings.TrimSpace(tier))
	if t == "" || t == "default" || t == "auto" {
		return ""
	}
	return "@" + t
}

type sourceKind int

const (
	sourceAccount sourceKind = iota
	sourceAPIKey
	sourceVertexAI
)

func (h *ResponsesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	logger := h.logger()
	isCompact := strings.HasSuffix(r.URL.Path, "/compact")

	body, _, err := ReadAndDecompress(r)
	if err != nil {
		logger.Warn("decompress failed", "err", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var adapted []byte
	if isCompact {
		adapted, err = AdaptForChatGPTCompact(body)
	} else {
		adapted, err = AdaptForChatGPTBackend(body)
	}
	if err != nil {
		logger.Warn("adapt body failed", "err", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	model := ExtractModel(adapted)
	tier := ExtractServiceTier(adapted)
	streaming := !isCompact
	hint := broker.LeaseHint{
		Model:              model,
		PreviousResponseID: ExtractPreviousResponseID(adapted),
	}

	order := h.sourceOrder()
	if len(order) == 0 || (h.totalConfigured() == 0) {
		writeJSONError(w, http.StatusUnauthorized, "No accounts configured.")
		return
	}
	for _, src := range order {
		switch src {
		case sourceAccount:
			outcome := h.serveViaAccountPool(w, r, adapted, model, tier, streaming, isCompact, hint, start)
			if outcome != outcomeSourceUnavailable {
				return
			}
		case sourceAPIKey:
			outcome := h.serveViaAPIKeyPool(w, r, body, model, tier, streaming, isCompact, start)
			if outcome != outcomeSourceUnavailable {
				return
			}
		}
	}
	writeJSONError(w, http.StatusServiceUnavailable, "All accounts and API keys exhausted. Try again later.")
}

func (h *ResponsesHandler) totalConfigured() int {
	n := 0
	if h.Pool != nil {
		n += h.Pool.Len()
	}
	if h.KeyPool != nil {
		n += h.KeyPool.Len()
	}
	return n
}

func (h *ResponsesHandler) sourceOrder() []sourceKind {
	if h.Pool == nil && h.KeyPool == nil {
		return nil
	}
	if h.Pool == nil {
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

type sourceOutcome int

const (
	outcomeServed sourceOutcome = iota
	outcomeRetry
	outcomeFailed
	outcomeSourceUnavailable
)

func (h *ResponsesHandler) serveViaAccountPool(
	w http.ResponseWriter, r *http.Request,
	adapted []byte, model, tier string, streaming, isCompact bool,
	hint broker.LeaseHint, start time.Time,
) sourceOutcome {
	if h.Pool == nil || h.Pool.Len() == 0 {
		return outcomeSourceUnavailable
	}
	logger := h.logger()
	bx := backoffRetry{logger: logger}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		lease, err := h.Pool.Lease(r.Context(), hint)
		if err != nil {
			if errors.Is(err, broker.ErrNoAccounts) {
				return outcomeSourceUnavailable
			}
			if errors.Is(err, broker.ErrAllExhausted) {
				if attempt+1 < maxAttempts {
					wait := minDuration(h.Pool.NearestCooldown(time.Now()), time.Duration(exhaustedRetryWaitMs)*time.Millisecond)
					if wait > 0 && sleepCtx(r.Context(), wait) {
						continue
					}
				}
				return outcomeSourceUnavailable
			}
			writePoolError(w, err)
			return outcomeFailed
		}

		decision := h.attemptForwardAccount(r.Context(), w, r, lease, adapted, model, tier, streaming, isCompact, start, &bx)
		switch decision.outcome {
		case outcomeServed:
			lease.Release()
			return outcomeServed
		case outcomeRetry:
			lease.Release()
			continue
		case outcomeFailed:
			lease.Release()
			h.recordFailure("chatgpt-pool", lease.Account.Email, "", model, time.Since(start), decision.status, decision.errMsg)
			return outcomeFailed
		}
	}
	return outcomeSourceUnavailable
}

func (h *ResponsesHandler) serveViaAPIKeyPool(
	w http.ResponseWriter, r *http.Request,
	rawBody []byte, model, tier string, streaming, isCompact bool,
	start time.Time,
) sourceOutcome {
	if h.KeyPool == nil || h.KeyPool.Len() == 0 {
		return outcomeSourceUnavailable
	}
	logger := h.logger()
	level5xx := 0
	for attempt := 0; attempt < maxAttempts; attempt++ {
		lease, err := h.KeyPool.Lease(r.Context(), []broker.APIKeyType{broker.KeyTypeOpenAI, broker.KeyTypeAzureOpenAI})
		if err != nil {
			if errors.Is(err, broker.ErrNoAPIKeys) {
				return outcomeSourceUnavailable
			}
			if errors.Is(err, broker.ErrAllExhausted) {
				return outcomeSourceUnavailable
			}
			writeJSONError(w, http.StatusInternalServerError, "key pool error")
			return outcomeFailed
		}

		resp, err := h.OpenAI.Forward(r.Context(), provider.OpenAIForwardRequest{
			Body:            rawBody,
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
			logger.Warn("openai forward error", "err", err, "key", lease.Key.ID)
			continue
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			_ = resp.Body.Close()
			lease.Key.MarkDead("auth_" + http.StatusText(resp.StatusCode))
			lease.Release()
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			cooldown := broker.ParseRetryAfter(resp.Header, body, time.Now())
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
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			ct := resp.Header.Get("Content-Type")
			if strings.HasPrefix(ct, "application/json") {
				w.Header().Set("Content-Type", "application/json")
			} else {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			}
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(body)
			h.recordFailure("openai-key", "", lease.Key.ID, model, time.Since(start), resp.StatusCode, truncate(string(body), 200))
			lease.Release()
			return outcomeFailed
		}

		rec := h.serveSuccessAPIKey(w, resp, lease, model, tier, streaming, isCompact)
		rec.KeyID = lease.Key.ID
		rec.Provider = "openai-key"
		rec.DurationMs = time.Since(start).Milliseconds()
		rec.Route = routeLabel(isCompact)
		if rec.Model == "" {
			rec.Model = model
		}
		h.priceRec(&rec)
		if rec.Success {
			lease.Key.RecordSuccess(rec.InputTokens, rec.OutputTokens, rec.Cost)
		}
		h.record(rec)
		lease.Release()
		return outcomeServed
	}
	return outcomeSourceUnavailable
}

type forwardOutcome int

const (
	fwdOutServed forwardOutcome = iota
	fwdOutRetry
	fwdOutFailed
)

type forwardDecision struct {
	outcome sourceOutcome
	status  int
	errMsg  string
}

type backoffRetry struct {
	logger      *slog.Logger
	level5xx    int
	authRetried map[string]bool
}

func (h *ResponsesHandler) attemptForwardAccount(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	lease *broker.Lease,
	adapted []byte,
	model, tier string,
	streaming, isCompact bool,
	start time.Time,
	bx *backoffRetry,
) forwardDecision {
	logger := h.logger()
	resp, err := h.ChatGPT.Forward(ctx, provider.ForwardRequest{
		Body:            adapted,
		ContentType:     "application/json",
		IncomingHeaders: r.Header,
		Credential:      provider.Credential{AccessToken: lease.Account.AccessToken, AccountID: lease.Account.AccountID},
		Streaming:       streaming,
		Compact:         isCompact,
	})
	if err != nil {
		if ctxCanceled(ctx) {
			return forwardDecision{outcome: outcomeFailed, errMsg: "client disconnected"}
		}
		logger.Warn("forward error", "err", err, "account", lease.Account.Email)
		return forwardDecision{outcome: outcomeRetry}
	}

	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		if h.Refresher == nil {
			lease.Account.MarkDead("auth_unauthorized")
			return forwardDecision{outcome: outcomeRetry}
		}
		if bx.authRetried == nil {
			bx.authRetried = make(map[string]bool)
		}
		if bx.authRetried[lease.Account.Email] {
			lease.Account.MarkDead("auth_refresh_failed")
			return forwardDecision{outcome: outcomeRetry}
		}
		bx.authRetried[lease.Account.Email] = true
		if err := h.Refresher.RefreshToken(ctx, lease.Account); err != nil {
			lease.Account.MarkDead("refresh: " + err.Error())
			logger.Warn("token refresh failed", "err", err, "account", lease.Account.Email)
			return forwardDecision{outcome: outcomeRetry}
		}
		retryResp, err := h.ChatGPT.Forward(ctx, provider.ForwardRequest{
			Body:            adapted,
			ContentType:     "application/json",
			IncomingHeaders: r.Header,
			Credential:      provider.Credential{AccessToken: lease.Account.AccessToken, AccountID: lease.Account.AccountID},
			Streaming:       streaming,
			Compact:         isCompact,
		})
		if err != nil {
			lease.Account.MarkDead("post_refresh: " + err.Error())
			return forwardDecision{outcome: outcomeRetry}
		}
		resp = retryResp
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		cooldown := broker.ParseRetryAfter(resp.Header, body, time.Now())
		lease.Account.MarkCooldown(time.Now().Add(cooldown))
		logger.Info("account rate limited", "account", lease.Account.Email, "cooldown", cooldown)
		return forwardDecision{outcome: outcomeRetry}
	}

	if resp.StatusCode >= 500 && resp.StatusCode < 600 {
		_ = resp.Body.Close()
		bx.level5xx++
		wait := broker.BackoffFor5xx(bx.level5xx - 1)
		if !sleepCtx(ctx, wait) {
			return forwardDecision{outcome: outcomeFailed, errMsg: "context canceled during 5xx backoff", status: resp.StatusCode}
		}
		logger.Warn("upstream 5xx", "status", resp.StatusCode, "account", lease.Account.Email, "level", bx.level5xx)
		return forwardDecision{outcome: outcomeRetry}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		ct := resp.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "application/json") {
			w.Header().Set("Content-Type", "application/json")
		} else {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		relay.CopyAllowedResponseHeaders(resp.Header, w.Header())
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return forwardDecision{outcome: outcomeFailed, status: resp.StatusCode, errMsg: truncate(string(body), 200)}
	}

	defer resp.Body.Close()
	relay.CopyAllowedResponseHeaders(resp.Header, w.Header())
	rec := h.serveSuccessAccount(w, resp, lease, model, tier, streaming, isCompact, start)
	rec.Account = lease.Account.Email
	rec.Provider = "chatgpt-pool"
	rec.DurationMs = time.Since(start).Milliseconds()
	rec.Route = routeLabel(isCompact)
	if rec.Model == "" {
		rec.Model = model
	}
	h.priceRec(&rec)
	if rec.Success {
		lease.Account.RecordSuccess(rec.InputTokens, rec.OutputTokens, rec.Cost)
		if rec.ResponseID != "" {
			lease.PinResponse(rec.ResponseID)
		}
	}
	h.record(rec)
	return forwardDecision{outcome: outcomeServed}
}

func (h *ResponsesHandler) serveSuccessAccount(
	w http.ResponseWriter,
	resp *http.Response,
	lease *broker.Lease,
	model, tier string,
	streaming, isCompact bool,
	_ time.Time,
) UsageRecord {
	rec := UsageRecord{
		Account:     lease.Account.Email,
		Status:      resp.StatusCode,
		Success:     true,
		Model:       model,
		ServiceTier: tier,
	}
	return h.relayResponse(w, resp, rec, streaming, isCompact)
}

func (h *ResponsesHandler) serveSuccessAPIKey(
	w http.ResponseWriter,
	resp *http.Response,
	lease *broker.APIKeyLease,
	model, tier string,
	streaming, isCompact bool,
) UsageRecord {
	rec := UsageRecord{
		KeyID:       lease.Key.ID,
		Status:      resp.StatusCode,
		Success:     true,
		Model:       model,
		ServiceTier: tier,
	}
	relay.CopyAllowedResponseHeaders(resp.Header, w.Header())
	defer resp.Body.Close()
	return h.relayResponse(w, resp, rec, streaming, isCompact)
}

func (h *ResponsesHandler) relayResponse(
	w http.ResponseWriter,
	resp *http.Response,
	rec UsageRecord,
	streaming, isCompact bool,
) UsageRecord {
	if streaming {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(resp.StatusCode)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		result := relay.RelaySSE(w, resp.Body)
		rec.InputTokens = result.Usage.InputTokens
		rec.CachedTokens = result.Usage.CachedInputTokens
		rec.OutputTokens = result.Usage.OutputTokens
		rec.ReasoningTkn = result.Usage.ReasoningTokens
		rec.TotalTokens = result.Usage.TotalTokens
		rec.ResponseID = result.Usage.ResponseID
		if result.Usage.Model != "" {
			rec.Model = result.Usage.Model
		}
		if result.Usage.ServiceTier != "" {
			rec.ServiceTier = result.Usage.ServiceTier
		}
		if result.WriteErr != nil {
			rec.Success = false
			rec.Error = result.WriteErr.Error()
		}
		if result.UpstrErr != nil {
			rec.Success = false
			rec.Error = result.UpstrErr.Error()
		}
		_ = isCompact
		return rec
	}
	w.Header().Set("Content-Type", contentTypeOrJSON(resp.Header.Get("Content-Type")))
	w.WriteHeader(resp.StatusCode)
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		rec.Success = false
		rec.Error = err.Error()
	}
	_, _ = w.Write(buf)
	extractNonStreamingUsage(&rec, buf)
	return rec
}

func (h *ResponsesHandler) record(rec UsageRecord) {
	if h.Recorder != nil {
		h.Recorder.RecordRequest(rec)
	}
}

func (h *ResponsesHandler) recordFailure(prov, account, key, model string, dur time.Duration, status int, errMsg string) {
	h.record(UsageRecord{
		Account:    account,
		KeyID:      key,
		Provider:   prov,
		Model:      model,
		Route:      "/responses",
		Status:     status,
		Success:    false,
		Error:      errMsg,
		DurationMs: dur.Milliseconds(),
	})
}

func (h *ResponsesHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func writePoolError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, broker.ErrNoAccounts):
		writeJSONError(w, http.StatusUnauthorized, "No accounts configured.")
	case errors.Is(err, broker.ErrAllExhausted):
		writeJSONError(w, http.StatusServiceUnavailable, "All accounts exhausted. Try again later.")
	default:
		writeJSONError(w, http.StatusInternalServerError, "broker unavailable")
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg}})
}

func contentTypeOrJSON(ct string) string {
	if ct == "" {
		return "application/json"
	}
	return ct
}

func extractNonStreamingUsage(rec *UsageRecord, body []byte) {
	if len(body) == 0 {
		return
	}
	var raw struct {
		ID          string `json:"id"`
		Model       string `json:"model"`
		ServiceTier string `json:"service_tier"`
		Usage       struct {
			InputTokens        int64 `json:"input_tokens"`
			InputTokensDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputTokens        int64 `json:"output_tokens"`
			OutputTokensDetails struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
			TotalTokens int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return
	}
	rec.InputTokens = raw.Usage.InputTokens
	rec.CachedTokens = raw.Usage.InputTokensDetails.CachedTokens
	rec.OutputTokens = raw.Usage.OutputTokens
	rec.ReasoningTkn = raw.Usage.OutputTokensDetails.ReasoningTokens
	rec.TotalTokens = raw.Usage.TotalTokens
	if rec.TotalTokens == 0 {
		rec.TotalTokens = rec.InputTokens + rec.OutputTokens
	}
	if raw.ID != "" {
		rec.ResponseID = raw.ID
	}
	if raw.Model != "" {
		rec.Model = raw.Model
	}
	if raw.ServiceTier != "" {
		rec.ServiceTier = raw.ServiceTier
	}
}

func routeLabel(isCompact bool) string {
	if isCompact {
		return "/responses/compact"
	}
	return "/responses"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func ctxCanceled(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
