package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	ChatGPTResponsesURL        = "https://chatgpt.com/backend-api/codex/responses"
	ChatGPTResponsesCompactURL = "https://chatgpt.com/backend-api/codex/responses/compact"
	ChatGPTUsageURL            = "https://chatgpt.com/backend-api/wham/usage"
	ChatGPTAccountCheckURL     = "https://chatgpt.com/backend-api/wham/accounts/check"
)

var passthroughRequestHeaders = []string{
	"x-client-request-id",
	"x-openai-subagent",
	"x-codex-turn-state",
	"originator",
	"user-agent",
	"session_id",
	"x-session-affinity",
	"openai-beta",
	"openai-organization",
	"openai-project",
}

type Credential struct {
	AccessToken string
	AccountID   string
}

type ChatGPTClient struct {
	HTTPClient     *http.Client
	WhamHTTPClient *http.Client
	BaseURL        string
	CompactURL     string
	UsageURL       string
}

func NewChatGPTClient() *ChatGPTClient {
	return &ChatGPTClient{
		HTTPClient:     &http.Client{Timeout: 0},
		WhamHTTPClient: newWhamHTTPClient(),
		BaseURL:        ChatGPTResponsesURL,
		CompactURL:     ChatGPTResponsesCompactURL,
		UsageURL:       ChatGPTUsageURL,
	}
}

func newWhamHTTPClient() *http.Client {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       120 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: 20 * time.Second}
}

func (c *ChatGPTClient) usageURL() string {
	if c.UsageURL != "" {
		return c.UsageURL
	}
	return ChatGPTUsageURL
}

func (c *ChatGPTClient) compactURL() string {
	if c.CompactURL != "" {
		return c.CompactURL
	}
	return c.BaseURL + "/compact"
}

type ForwardRequest struct {
	Body            []byte
	ContentType     string
	ContentEncoding string
	IncomingHeaders http.Header
	Credential      Credential
	Streaming       bool
	Compact         bool
}

func (c *ChatGPTClient) Forward(ctx context.Context, req ForwardRequest) (*http.Response, error) {
	if req.Credential.AccessToken == "" {
		return nil, errors.New("chatgpt: missing access token")
	}
	target := c.BaseURL
	if req.Compact {
		target = c.compactURL()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(req.Body))
	if err != nil {
		return nil, err
	}
	httpReq.ContentLength = int64(len(req.Body))
	httpReq.Header.Set("Authorization", "Bearer "+req.Credential.AccessToken)
	if req.Credential.AccountID != "" {
		httpReq.Header.Set("ChatGPT-Account-Id", req.Credential.AccountID)
	}
	contentType := req.ContentType
	if contentType == "" {
		contentType = "application/json"
	}
	httpReq.Header.Set("Content-Type", contentType)
	if req.Streaming {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	httpReq.Header.Set("Accept-Encoding", "identity")
	if req.ContentEncoding != "" {
		httpReq.Header.Set("Content-Encoding", req.ContentEncoding)
	}
	for _, name := range passthroughRequestHeaders {
		if v := req.IncomingHeaders.Get(name); v != "" {
			httpReq.Header.Set(name, v)
		}
	}

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

type WhamUsage struct {
	PrimaryUsedPct      float64
	SecondaryUsedPct    float64
	PrimaryResetAt      time.Time
	SecondaryResetAt    time.Time
	LimitReached        bool
	PlanType            string
}

type WhamError struct {
	Status int
	Body   string
}

func (e *WhamError) Error() string {
	return fmt.Sprintf("wham/usage: %d %s", e.Status, e.Body)
}

func (c *ChatGPTClient) FetchUsage(ctx context.Context, cred Credential) (WhamUsage, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.usageURL(), nil)
	if err != nil {
		return WhamUsage{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	httpReq.Header.Set("ChatGPT-Account-Id", cred.AccountID)
	httpReq.Header.Set("Accept", "application/json")

	client := c.WhamHTTPClient
	if client == nil {
		client = c.HTTPClient
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return WhamUsage{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return WhamUsage{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return WhamUsage{}, &WhamError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	return parseWhamUsage(body)
}
