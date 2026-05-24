package ingress

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FL-Penly/proxy-gate/broker"
	"github.com/FL-Penly/proxy-gate/provider"
)

const opencodeRequestBody = `{"model":"gpt-5.4-mini","input":[{"type":"message","role":"developer","content":"You are concise."},{"type":"message","role":"user","content":"hi"}],"include":["reasoning.encrypted_content"],"reasoning":{"effort":"medium","summary":"auto"},"store":false,"stream":true,"text":{"verbosity":"medium"},"tool_choice":"auto","tools":[],"prompt_cache_key":"abc123"}`

func TestParityChatGPTBackendBodyMatchesV1Adapter(t *testing.T) {
	out, err := AdaptForChatGPTBackend([]byte(opencodeRequestBody))
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	body := string(out)

	if !strings.Contains(body, `"instructions":""`) {
		t.Errorf("expected instructions:\"\" (matches v1's body.instructions=''), got: %s", body)
	}
	if !strings.Contains(body, `"store":false`) {
		t.Errorf("client store=false must be preserved (not overwritten): %s", body)
	}
	if !strings.Contains(body, `"stream":true`) {
		t.Errorf("stream must be true for /v1/responses: %s", body)
	}
	if strings.Contains(body, "max_output_tokens") {
		t.Errorf("max_output_tokens must be stripped: %s", body)
	}
	if strings.Contains(body, "max_tokens") {
		t.Errorf("max_tokens must be stripped: %s", body)
	}
	if !strings.Contains(body, `"role":"developer"`) {
		t.Errorf("developer role must remain in input array (NOT moved to instructions): %s", body)
	}
	if !strings.Contains(body, `"include":["reasoning.encrypted_content"]`) {
		t.Errorf("include array must be preserved verbatim: %s", body)
	}
	if !strings.Contains(body, `"prompt_cache_key":"abc123"`) {
		t.Errorf("prompt_cache_key must be preserved: %s", body)
	}
	if !strings.Contains(body, `"reasoning":{"effort":"medium","summary":"auto"}`) {
		t.Errorf("reasoning options must pass through verbatim: %s", body)
	}
	if !strings.Contains(body, `"text":{"verbosity":"medium"}`) {
		t.Errorf("text options must pass through verbatim: %s", body)
	}
}

func TestParityAPIKeyPathSendsRawBody(t *testing.T) {
	var captured []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_x","model":"gpt-5","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	keyPool := broker.NewAPIKeyPool()
	key := &broker.APIKey{ID: "k1", Type: broker.KeyTypeOpenAI, APIKey: "sk-test"}
	key.ApplyStats(broker.APIKeyStats{})
	keyPool.Add(key)

	pool := broker.NewPool(broker.PoolConfig{})

	h := &ResponsesHandler{
		Pool:     pool,
		KeyPool:  keyPool,
		ChatGPT:  &provider.ChatGPTClient{HTTPClient: upstream.Client(), BaseURL: upstream.URL},
		OpenAI:   &provider.OpenAIClient{HTTPClient: upstream.Client(), BaseURL: upstream.URL + "/responses"},
		Recorder: &fakeRecorder{},
	}

	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(opencodeRequestBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if string(captured) != opencodeRequestBody {
		t.Errorf("API key path must forward raw body unmodified.\nsent: %s\ngot:  %s", opencodeRequestBody, string(captured))
	}
}
