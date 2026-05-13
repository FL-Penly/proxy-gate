package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

type staticVertexToken string

func (s staticVertexToken) Token(context.Context) (string, error) { return string(s), nil }
func (s staticVertexToken) Method() string                        { return "static" }

func TestPrepareVertexAnthropicBody(t *testing.T) {
	out, err := PrepareVertexAnthropicBody([]byte(`{"model":"claude-sonnet-4-6","context_management":{"edits":true},"service_tier":"standard","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "model").Exists() {
		t.Fatalf("model should be removed: %s", out)
	}
	if gjson.GetBytes(out, "context_management").Exists() || gjson.GetBytes(out, "service_tier").Exists() {
		t.Fatalf("vertex-only body should remove unsupported fields: %s", out)
	}
	if got := gjson.GetBytes(out, "anthropic_version").String(); got != VertexAnthropicVersion {
		t.Fatalf("anthropic_version=%q", got)
	}
}

func TestAnthropicVertexClientForward(t *testing.T) {
	var gotPath, gotAuth, gotAccept string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","model":"claude-sonnet-4-6","usage":{"input_tokens":1,"output_tokens":2}}`))
	}))
	defer upstream.Close()

	client := &AnthropicVertexClient{
		HTTPClient: upstream.Client(),
		BaseURL:    upstream.URL,
		Config: VertexAnthropicConfig{
			ProjectID: "p",
			Location:  "us-east5",
		},
		TokenSource: staticVertexToken("tok"),
	}
	resp, err := client.Forward(context.Background(), AnthropicVertexForwardRequest{
		Body:      []byte(`{"model":"claude-opus-4-7","messages":[]}`),
		Model:     "claude-sonnet-4-6",
		Streaming: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !strings.Contains(gotPath, "/projects/p/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-6:streamRawPredict") {
		t.Fatalf("path=%q", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("auth=%q", gotAuth)
	}
	if gotAccept != "text/event-stream" {
		t.Fatalf("accept=%q", gotAccept)
	}
	if gjson.GetBytes(gotBody, "model").Exists() || gjson.GetBytes(gotBody, "anthropic_version").String() != VertexAnthropicVersion {
		t.Fatalf("body=%s", gotBody)
	}
}

func TestParseVertexAliases(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/.zshrc"
	raw := `alias g-opencode='export GOOGLE_CLOUD_PROJECT=pago-427611 && export VERTEX_LOCATION=us-east5 && opencode'`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	project, location := parseVertexAliases(path)
	if project != "pago-427611" || location != "us-east5" {
		t.Fatalf("project=%q location=%q", project, location)
	}
}

func TestParseGCloudConfig(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config_default"
	raw := "[core]\nproject = lucid-sonar-402610\n[compute]\nregion = us-east5\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	project, location := parseGCloudConfig(path)
	if project != "lucid-sonar-402610" || location != "us-east5" {
		t.Fatalf("project=%q location=%q", project, location)
	}
}
