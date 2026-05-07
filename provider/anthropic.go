package provider

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
)

const (
	AnthropicMessagesURL    = "https://api.anthropic.com/v1/messages"
	AnthropicVersionHeader  = "2023-06-01"
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
	BaseURL         string
	Streaming       bool
	IncomingHeaders http.Header
	BetaHeader      string
}

func (c *AnthropicClient) Forward(ctx context.Context, req AnthropicForwardRequest) (*http.Response, error) {
	if req.APIKey == "" {
		return nil, errors.New("anthropic: missing api key")
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
	httpReq.Header.Set("x-api-key", req.APIKey)
	httpReq.Header.Set("anthropic-version", AnthropicVersionHeader)
	httpReq.Header.Set("Content-Type", "application/json")
	if req.Streaming {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	if req.BetaHeader != "" {
		httpReq.Header.Set("anthropic-beta", req.BetaHeader)
	}
	if v := req.IncomingHeaders.Get("anthropic-beta"); v != "" && req.BetaHeader == "" {
		httpReq.Header.Set("anthropic-beta", v)
	}
	for _, name := range passthroughRequestHeaders {
		if v := req.IncomingHeaders.Get(name); v != "" {
			httpReq.Header.Set(name, v)
		}
	}
	return c.HTTPClient.Do(httpReq)
}
