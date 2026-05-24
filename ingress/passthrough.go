package ingress

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/FL-Penly/proxy-gate/broker"
)

// PassthroughHandler proxies arbitrary requests to the ChatGPT backend
// using a pooled account for authentication. It is used for non-responses
// endpoints such as /backend-api/codex/models and plugin APIs.
type PassthroughHandler struct {
	Pool      *broker.Pool
	Refresher TokenRefresher
	BaseURL   string // upstream base, e.g. "https://chatgpt.com"
	Logger    *slog.Logger
}

func (h *PassthroughHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	lease, err := h.Pool.Lease(r.Context(), broker.LeaseHint{})
	if err != nil {
		h.logger().Warn("passthrough: no accounts", "err", err)
		writeJSONError(w, http.StatusServiceUnavailable, "no accounts available")
		return
	}
	defer lease.Release()

	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	}

	upstream := h.BaseURL + r.URL.Path
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}

	resp, err := h.forward(r.Context(), r, lease, body, upstream)
	if err != nil {
		h.logger().Warn("passthrough: upstream error", "err", err, "path", r.URL.Path)
		writeJSONError(w, http.StatusBadGateway, "upstream request failed")
		return
	}
	if resp.StatusCode == http.StatusUnauthorized && h.Refresher != nil {
		resp.Body.Close()
		if rerr := h.Refresher.RefreshToken(r.Context(), lease.Account); rerr == nil {
			resp, err = h.forward(r.Context(), r, lease, body, upstream)
			if err != nil {
				writeJSONError(w, http.StatusBadGateway, "upstream request failed after refresh")
				return
			}
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		lease.Account.RecordSuccess(0, 0, 0)
	}

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

var passthroughClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func (h *PassthroughHandler) forward(ctx context.Context, orig *http.Request, lease *broker.Lease, body []byte, upstream string) (*http.Response, error) {
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, orig.Method, upstream, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+lease.Account.AccessToken)
	if lease.Account.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", lease.Account.AccountID)
	}

	for _, name := range []string{
		"Content-Type", "Accept", "Accept-Language",
		"User-Agent", "X-Client-Request-Id",
	} {
		if v := orig.Header.Get(name); v != "" {
			req.Header.Set(name, v)
		}
	}
	if len(body) > 0 {
		req.ContentLength = int64(len(body))
	}

	return passthroughClient.Do(req)
}

func (h *PassthroughHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}
