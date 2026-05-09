package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicForwardAuthModes(t *testing.T) {
	var gotAPIKey, gotBearer, gotBeta, gotAccept, gotVersion, gotRequestID string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("x-api-key")
		gotBearer = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		gotAccept = r.Header.Get("Accept")
		gotVersion = r.Header.Get("anthropic-version")
		gotRequestID = r.Header.Get("x-request-id")
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1","usage":{"input_tokens":1,"output_tokens":2}}`))
	}))
	defer upstream.Close()

	client := &AnthropicClient{HTTPClient: upstream.Client(), BaseURL: upstream.URL}
	headers := http.Header{}
	headers.Set("anthropic-beta", "messages-2023-12-15")
	headers.Set("anthropic-version", "2023-99-99")
	headers.Set("Accept", "application/json")
	headers.Set("x-request-id", "req-client")
	_, err := client.Forward(context.Background(), AnthropicForwardRequest{Body: []byte(`{}`), APIKey: "sk-ant", Streaming: true, IncomingHeaders: headers})
	if err != nil {
		t.Fatalf("api key forward: %v", err)
	}
	if gotAPIKey != "sk-ant" || gotBearer != "" || gotAccept != "application/json" || gotBeta != "messages-2023-12-15" || gotVersion != "2023-99-99" || gotRequestID != "req-client" {
		t.Fatalf("api key headers got key=%q bearer=%q accept=%q beta=%q version=%q request_id=%q", gotAPIKey, gotBearer, gotAccept, gotBeta, gotVersion, gotRequestID)
	}

	headers = http.Header{}
	headers.Add("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	headers.Add("anthropic-beta", "interleaved-thinking-2025-05-14")
	_, err = client.Forward(context.Background(), AnthropicForwardRequest{Body: []byte(`{}`), AccessToken: "oauth-token", AuthMode: AnthropicAuthOAuth, IncomingHeaders: headers})
	if err != nil {
		t.Fatalf("oauth forward: %v", err)
	}
	wantBeta := "oauth-2025-04-20,claude-code-20250219,interleaved-thinking-2025-05-14"
	if gotAPIKey != "" || gotBearer != "Bearer oauth-token" || gotAccept != "application/json" || gotBeta != wantBeta {
		t.Fatalf("oauth headers got key=%q bearer=%q accept=%q beta=%q", gotAPIKey, gotBearer, gotAccept, gotBeta)
	}
}
