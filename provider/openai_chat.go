package provider

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
)

const OpenAIChatURL = "https://api.openai.com/v1/chat/completions"

type ChatCompletionsClient struct {
	HTTPClient *http.Client
	BaseURL    string
}

func NewChatCompletionsClient() *ChatCompletionsClient {
	return &ChatCompletionsClient{
		HTTPClient: &http.Client{Timeout: 0},
		BaseURL:    OpenAIChatURL,
	}
}

type ChatForwardRequest struct {
	Body            []byte
	APIKey          string
	BaseURL         string
	Streaming       bool
	IncomingHeaders http.Header
}

func (c *ChatCompletionsClient) Forward(ctx context.Context, req ChatForwardRequest) (*http.Response, error) {
	if req.APIKey == "" {
		return nil, errors.New("chat: missing api key")
	}
	target := c.BaseURL
	if req.BaseURL != "" {
		target = strings.TrimRight(req.BaseURL, "/") + "/chat/completions"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(req.Body))
	if err != nil {
		return nil, err
	}
	httpReq.ContentLength = int64(len(req.Body))
	httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	if req.Streaming {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	for _, name := range passthroughRequestHeaders {
		if v := req.IncomingHeaders.Get(name); v != "" {
			httpReq.Header.Set(name, v)
		}
	}
	return c.HTTPClient.Do(httpReq)
}
