package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/sjson"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	VertexAnthropicVersion   = "vertex-2023-10-16"
	VertexCloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"
)

type VertexAnthropicConfig struct {
	ProjectID  string `json:"project_id"`
	Location   string `json:"location"`
	AuthMethod string `json:"auth_method,omitempty"`
}

type VertexAnthropicStatus struct {
	Available  bool   `json:"available"`
	ProjectID  string `json:"project_id,omitempty"`
	Location   string `json:"location,omitempty"`
	AuthMethod string `json:"auth_method,omitempty"`
	Error      string `json:"error,omitempty"`
}

type VertexTokenSource interface {
	Token(ctx context.Context) (string, error)
	Method() string
}

type AnthropicVertexClient struct {
	HTTPClient  *http.Client
	BaseURL     string
	Config      VertexAnthropicConfig
	TokenSource VertexTokenSource
}

type AnthropicVertexForwardRequest struct {
	Body            []byte
	Model           string
	Streaming       bool
	IncomingHeaders http.Header
}

func NewAnthropicVertexClient(ctx context.Context) *AnthropicVertexClient {
	cfg := DetectVertexAnthropicConfig()
	ts, method := DefaultVertexTokenSource(ctx)
	cfg.AuthMethod = method
	return &AnthropicVertexClient{
		HTTPClient:  &http.Client{Timeout: 0},
		BaseURL:     "https://LOCATION-aiplatform.googleapis.com",
		Config:      cfg,
		TokenSource: ts,
	}
}

func (c *AnthropicVertexClient) Status(ctx context.Context) VertexAnthropicStatus {
	cfg := c.Config
	if cfg.ProjectID == "" || cfg.Location == "" {
		return VertexAnthropicStatus{Available: false, ProjectID: cfg.ProjectID, Location: cfg.Location, AuthMethod: cfg.AuthMethod, Error: "missing project or location"}
	}
	if c.TokenSource == nil {
		return VertexAnthropicStatus{Available: false, ProjectID: cfg.ProjectID, Location: cfg.Location, AuthMethod: cfg.AuthMethod, Error: "missing token source"}
	}
	if _, err := c.TokenSource.Token(ctx); err != nil {
		return VertexAnthropicStatus{Available: false, ProjectID: cfg.ProjectID, Location: cfg.Location, AuthMethod: c.TokenSource.Method(), Error: err.Error()}
	}
	return VertexAnthropicStatus{Available: true, ProjectID: cfg.ProjectID, Location: cfg.Location, AuthMethod: c.TokenSource.Method()}
}

func (c *AnthropicVertexClient) Forward(ctx context.Context, req AnthropicVertexForwardRequest) (*http.Response, error) {
	if c == nil {
		return nil, errors.New("vertex anthropic: client not configured")
	}
	cfg := c.Config
	if cfg.ProjectID == "" || cfg.Location == "" {
		return nil, errors.New("vertex anthropic: missing project or location")
	}
	if req.Model == "" {
		return nil, errors.New("vertex anthropic: missing model")
	}
	if c.TokenSource == nil {
		return nil, errors.New("vertex anthropic: missing token source")
	}
	token, err := c.TokenSource.Token(ctx)
	if err != nil {
		return nil, err
	}
	body, err := PrepareVertexAnthropicBody(req.Body)
	if err != nil {
		return nil, err
	}
	target := c.endpoint(req.Model, req.Streaming)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.ContentLength = int64(len(body))
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	if req.Streaming {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	for _, name := range []string{"user-agent", "x-request-id", "x-client-request-id", "x-app", "x-claude-code-session-id"} {
		if v := req.IncomingHeaders.Get(name); v != "" {
			httpReq.Header.Set(name, v)
		}
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(httpReq)
}

func (c *AnthropicVertexClient) endpoint(model string, streaming bool) string {
	base := c.BaseURL
	if base == "" {
		base = "https://LOCATION-aiplatform.googleapis.com"
	}
	base = strings.ReplaceAll(base, "LOCATION", c.Config.Location)
	method := "rawPredict"
	if streaming {
		method = "streamRawPredict"
	}
	return strings.TrimRight(base, "/") + "/v1/projects/" + url.PathEscape(c.Config.ProjectID) +
		"/locations/" + url.PathEscape(c.Config.Location) +
		"/publishers/anthropic/models/" + url.PathEscape(model) + ":" + method
}

func PrepareVertexAnthropicBody(body []byte) ([]byte, error) {
	out, err := sjson.DeleteBytes(body, "model")
	if err != nil {
		return nil, err
	}
	for _, path := range []string{"context_management", "service_tier"} {
		out, err = sjson.DeleteBytes(out, path)
		if err != nil {
			return nil, err
		}
	}
	out, err = sjson.SetBytes(out, "anthropic_version", VertexAnthropicVersion)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func DetectVertexAnthropicConfig() VertexAnthropicConfig {
	project := firstNonEmpty(
		os.Getenv("GOOGLE_CLOUD_PROJECT"),
		os.Getenv("ANTHROPIC_VERTEX_PROJECT_ID"),
		os.Getenv("GCLOUD_PROJECT"),
	)
	location := firstNonEmpty(
		os.Getenv("VERTEX_LOCATION"),
		os.Getenv("CLOUD_ML_REGION"),
		os.Getenv("GOOGLE_CLOUD_LOCATION"),
	)
	if project == "" || location == "" {
		if home, err := os.UserHomeDir(); err == nil {
			zProject, zLocation := parseVertexAliases(filepath.Join(home, ".zshrc"))
			project = firstNonEmpty(project, zProject)
			location = firstNonEmpty(location, zLocation)
		}
	}
	if project == "" {
		project = strings.TrimSpace(runGCloudValue("project"))
	}
	return VertexAnthropicConfig{ProjectID: project, Location: location}
}

func parseVertexAliases(path string) (project, location string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.Contains(line, "g-opencode") && !strings.Contains(line, "g-claude") {
			continue
		}
		for _, part := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ' ' || r == '\'' || r == '"' || r == '&' || r == ';'
		}) {
			if v, ok := strings.CutPrefix(part, "GOOGLE_CLOUD_PROJECT="); ok {
				project = strings.TrimSpace(v)
			}
			if v, ok := strings.CutPrefix(part, "ANTHROPIC_VERTEX_PROJECT_ID="); ok && project == "" {
				project = strings.TrimSpace(v)
			}
			if v, ok := strings.CutPrefix(part, "VERTEX_LOCATION="); ok {
				location = strings.TrimSpace(v)
			}
			if v, ok := strings.CutPrefix(part, "CLOUD_ML_REGION="); ok && location == "" {
				location = strings.TrimSpace(v)
			}
		}
	}
	return project, location
}

func runGCloudValue(name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gcloud", "config", "get-value", name, "--quiet").Output()
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(out))
	if v == "(unset)" {
		return ""
	}
	return v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func DefaultVertexTokenSource(ctx context.Context) (VertexTokenSource, string) {
	if ts, err := newADCTokenSource(ctx); err == nil {
		return ts, ts.Method()
	}
	if _, err := exec.LookPath("gcloud"); err == nil {
		ts := &gcloudTokenSource{}
		return ts, ts.Method()
	}
	return nil, ""
}

type adcTokenSource struct {
	src oauth2.TokenSource
}

func newADCTokenSource(ctx context.Context) (*adcTokenSource, error) {
	creds, err := google.FindDefaultCredentials(ctx, VertexCloudPlatformScope)
	if err != nil {
		return nil, err
	}
	if creds.TokenSource == nil {
		return nil, errors.New("adc token source missing")
	}
	return &adcTokenSource{src: creds.TokenSource}, nil
}

func (s *adcTokenSource) Token(_ context.Context) (string, error) {
	tok, err := s.src.Token()
	if err != nil {
		return "", err
	}
	if tok == nil || tok.AccessToken == "" {
		return "", errors.New("adc returned empty token")
	}
	return tok.AccessToken, nil
}

func (s *adcTokenSource) Method() string { return "adc" }

type gcloudTokenSource struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func (s *gcloudTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Now().Before(s.expiresAt.Add(-5*time.Minute)) {
		return s.token, nil
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "gcloud", "auth", "print-access-token", "--quiet").Output()
	if err != nil {
		return "", fmt.Errorf("gcloud auth print-access-token: %w", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.New("gcloud returned empty access token")
	}
	s.token = token
	s.expiresAt = time.Now().Add(time.Hour)
	return token, nil
}

func (s *gcloudTokenSource) Method() string { return "gcloud" }
