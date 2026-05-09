package provider

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
)

const (
	AnthropicMessagesURL   = "https://api.anthropic.com/v1/messages"
	AnthropicVersionHeader = "2023-06-01"
	ClaudeOAuthBetaHeader  = "oauth-2025-04-20"
)

type AnthropicClient struct {
	HTTPClient *http.Client
	BaseURL    string
}

func NewAnthropicClient() *AnthropicClient {
	return &AnthropicClient{
		HTTPClient: &http.Client{Timeout: 0},
		BaseURL:    AnthropicMessagesURL,
	}
}

type AnthropicForwardRequest struct {
	Body            []byte
	APIKey          string
	AccessToken     string
	AuthMode        AnthropicAuthMode
	BaseURL         string
	Streaming       bool
	IncomingHeaders http.Header
	BetaHeader      string
}

type AnthropicAuthMode string

const (
	AnthropicAuthAPIKey AnthropicAuthMode = "api-key"
	AnthropicAuthOAuth  AnthropicAuthMode = "oauth"
)

var anthropicPassthroughHeaders = []string{
	"anthropic-version",
	"anthropic-beta",
	"user-agent",
	"x-request-id",
	"x-client-request-id",
	"x-stainless-arch",
	"x-stainless-lang",
	"x-stainless-os",
	"x-stainless-package-version",
	"x-stainless-retry-count",
	"x-stainless-runtime",
	"x-stainless-runtime-version",
	"anthropic-dangerous-direct-browser-access",
	"x-app",
	"x-claude-code-session-id",
	"x-session-affinity",
	"x-stainless-timeout",
}

func (c *AnthropicClient) Forward(ctx context.Context, req AnthropicForwardRequest) (*http.Response, error) {
	mode := req.AuthMode
	if mode == "" {
		mode = AnthropicAuthAPIKey
	}
	if mode == AnthropicAuthAPIKey && req.APIKey == "" {
		return nil, errors.New("anthropic: missing api key")
	}
	if mode == AnthropicAuthOAuth && req.AccessToken == "" {
		return nil, errors.New("anthropic: missing access token")
	}
	target := c.BaseURL
	if req.BaseURL != "" {
		target = strings.TrimRight(req.BaseURL, "/") + "/v1/messages"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(req.Body))
	if err != nil {
		return nil, err
	}
	httpReq.ContentLength = int64(len(req.Body))
	if mode == AnthropicAuthOAuth {
		httpReq.Header.Set("Authorization", "Bearer "+req.AccessToken)
	} else {
		httpReq.Header.Set("x-api-key", req.APIKey)
	}
	if v := req.IncomingHeaders.Get("anthropic-version"); v != "" {
		httpReq.Header.Set("anthropic-version", v)
	} else {
		httpReq.Header.Set("anthropic-version", AnthropicVersionHeader)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if v := req.IncomingHeaders.Get("Accept"); v != "" {
		httpReq.Header.Set("Accept", v)
	} else if req.Streaming {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	beta := req.BetaHeader
	if beta == "" {
		beta = strings.Join(req.IncomingHeaders.Values("anthropic-beta"), ",")
	}
	if mode == AnthropicAuthOAuth {
		beta = mergeAnthropicBeta(ClaudeOAuthBetaHeader, beta)
	}
	if beta != "" {
		httpReq.Header.Set("anthropic-beta", beta)
	}
	for _, name := range anthropicPassthroughHeaders {
		if strings.EqualFold(name, "anthropic-version") || strings.EqualFold(name, "anthropic-beta") {
			continue
		}
		if v := req.IncomingHeaders.Get(name); v != "" {
			httpReq.Header.Set(name, v)
		}
	}
	return c.HTTPClient.Do(httpReq)
}

func mergeAnthropicBeta(required, incoming string) string {
	seen := map[string]bool{}
	out := make([]string, 0, 4)
	add := func(s string) {
		for _, part := range strings.Split(s, ",") {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			out = append(out, part)
		}
	}
	add(required)
	add(incoming)
	return strings.Join(out, ",")
}
