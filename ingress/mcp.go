package ingress

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/FL-Penly/proxy-gate/broker"
)

// MCPHandler proxies MCP (Model Context Protocol) Streamable HTTP requests
// to the ChatGPT backend's MCP endpoint. It handles path rewriting (Codex
// sends to /api/codex/apps, ChatGPT expects /backend-api/wham/apps),
// session stickiness via Mcp-Session-Id, and SSE streaming with no timeout.
type MCPHandler struct {
	Pool        *broker.Pool
	Refresher   TokenRefresher
	UpstreamURL string // e.g. "https://chatgpt.com/backend-api/wham/apps"
	Logger      *slog.Logger
}

// mcpClient uses zero timeout — MCP SSE streams are long-lived.
var mcpClient = &http.Client{
	Timeout: 0,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

var mcpPassthroughHeaders = []string{
	"Content-Type",
	"Accept",
	"Accept-Language",
	"User-Agent",
	"Mcp-Session-Id",
	"Last-Event-Id",
	"X-Client-Request-Id",
}

func (h *MCPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("Mcp-Session-Id")

	hint := broker.LeaseHint{SessionKey: sessionPinKey(r.Header)}
	if sessionID != "" {
		hint.PreviousResponseID = "mcp:" + sessionID
	}

	lease, err := h.Pool.Lease(r.Context(), hint)
	if err != nil {
		h.logger().Warn("mcp: no accounts", "err", err)
		writeJSONError(w, http.StatusServiceUnavailable, "no accounts available")
		return
	}
	defer lease.Release()

	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	}

	resp, err := h.forward(r.Context(), r, lease, body)
	if err != nil {
		h.logger().Warn("mcp: upstream error", "err", err, "method", r.Method)
		writeJSONError(w, http.StatusBadGateway, "upstream request failed")
		return
	}

	if resp.StatusCode == http.StatusUnauthorized && h.Refresher != nil {
		resp.Body.Close()
		if rerr := h.Refresher.RefreshToken(r.Context(), lease.Account); rerr == nil {
			resp, err = h.forward(r.Context(), r, lease, body)
			if err != nil {
				writeJSONError(w, http.StatusBadGateway, "upstream request failed after refresh")
				return
			}
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		lease.Account.RecordSuccess(0, 0, 0)
		if sk := hint.SessionKey; sk != "" {
			lease.PinResponse(sk)
		}
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			lease.PinResponse("mcp:" + sid)
		}
	}

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		h.relaySSE(w, resp.Body)
	} else {
		_, _ = io.Copy(w, resp.Body)
	}
}

func (h *MCPHandler) forward(ctx context.Context, orig *http.Request, lease *broker.Lease, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, orig.Method, h.UpstreamURL, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+lease.Account.AccessToken)
	if lease.Account.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", lease.Account.AccountID)
	}

	for _, name := range mcpPassthroughHeaders {
		if v := orig.Header.Get(name); v != "" {
			req.Header.Set(name, v)
		}
	}
	if len(body) > 0 {
		req.ContentLength = int64(len(body))
	}

	return mcpClient.Do(req)
}

func (h *MCPHandler) relaySSE(w http.ResponseWriter, src io.Reader) {
	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func (h *MCPHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}
