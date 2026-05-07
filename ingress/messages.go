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

	"github.com/codeking-ai/cligate-v2/broker"
	"github.com/codeking-ai/cligate-v2/provider"
	"github.com/codeking-ai/cligate-v2/relay"
	"github.com/tidwall/gjson"
)

type MessagesHandler struct {
	KeyPool   *broker.APIKeyPool
	Anthropic *provider.AnthropicClient
	Recorder  RequestRecorder
	Logger    *slog.Logger
}

func (h *MessagesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	logger := h.logger()

	if h.KeyPool == nil || h.KeyPool.Len() == 0 {
		writeJSONError(w, http.StatusUnauthorized, "No Anthropic API keys configured.")
		return
	}

	body, _, err := ReadAndDecompress(r)
	if err != nil {
		logger.Warn("decompress failed", "err", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	model := gjson.GetBytes(body, "model").String()
	streaming := gjson.GetBytes(body, "stream").Bool()

	level5xx := 0
	for attempt := 0; attempt < maxAttempts; attempt++ {
		lease, err := h.KeyPool.Lease(r.Context(), []broker.APIKeyType{broker.KeyTypeAnthropic})
		if err != nil {
			if errors.Is(err, broker.ErrNoAPIKeys) || errors.Is(err, broker.ErrAllExhausted) {
				writeJSONError(w, http.StatusServiceUnavailable, "All Anthropic API keys exhausted.")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "key pool error")
			return
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
				return
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
				return
			}
			lease.Release()
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			ct := resp.Header.Get("Content-Type")
			if strings.HasPrefix(ct, "application/json") {
				w.Header().Set("Content-Type", "application/json")
			} else {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			}
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(b)
			lease.Release()
			return
		}

		rec := h.serveSuccess(w, resp, lease, model, streaming)
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
		return
	}
	writeJSONError(w, http.StatusServiceUnavailable, "All Anthropic API keys exhausted.")
}

func (h *MessagesHandler) serveSuccess(w http.ResponseWriter, resp *http.Response, lease *broker.APIKeyLease, model string, streaming bool) UsageRecord {
	defer resp.Body.Close()
	rec := UsageRecord{KeyID: lease.Key.ID, Status: resp.StatusCode, Success: true, Model: model}
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
