package ingress

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestAdaptMissingInstructionsBecomesEmptyString(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"developer","content":"You are a title generator."},{"type":"message","role":"user","content":"hi"}]}`)
	out, err := AdaptForChatGPTBackend(body)
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	instr := gjson.GetBytes(out, "instructions")
	if !instr.Exists() {
		t.Fatalf("instructions field missing — upstream rejects with 400")
	}
	if instr.String() != "" {
		t.Errorf("instructions = %q, want empty string (matches v1 codex-pool behavior)", instr.String())
	}
	input := gjson.GetBytes(out, "input").Array()
	if len(input) != 2 {
		t.Errorf("input length = %d, want 2 (developer message must NOT be moved into instructions)", len(input))
	}
	if input[0].Get("role").String() != "developer" {
		t.Errorf("developer message must remain in input array, got role=%q", input[0].Get("role").String())
	}
}

func TestAdaptKeepsExistingInstructions(t *testing.T) {
	body := []byte(`{"model":"x","instructions":"already set","input":[{"type":"message","role":"user","content":"hi"}]}`)
	out, err := AdaptForChatGPTBackend(body)
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	if gjson.GetBytes(out, "instructions").String() != "already set" {
		t.Errorf("existing instructions overwritten")
	}
}

func TestAdaptEmptyInstructionsKeptEmpty(t *testing.T) {
	body := []byte(`{"model":"x","instructions":"","input":[{"type":"message","role":"user","content":"hi"}]}`)
	out, err := AdaptForChatGPTBackend(body)
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	if got := gjson.GetBytes(out, "instructions").String(); got != "" {
		t.Errorf("empty instructions = %q, want empty (no default injection)", got)
	}
}

func TestAdaptForceStoreFalseAndStripsTokens(t *testing.T) {
	body := []byte(`{"model":"x","store":true,"max_output_tokens":2048,"max_tokens":1024,"input":[{"type":"message","role":"user","content":"hi"}]}`)
	out, err := AdaptForChatGPTBackend(body)
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	if gjson.GetBytes(out, "store").Bool() {
		t.Errorf("store should be false")
	}
	if gjson.GetBytes(out, "max_output_tokens").Exists() {
		t.Errorf("max_output_tokens should be stripped")
	}
	if gjson.GetBytes(out, "max_tokens").Exists() {
		t.Errorf("max_tokens should be stripped")
	}
}

func TestAdaptStreamForcedTrueForResponses(t *testing.T) {
	body := []byte(`{"model":"x","stream":false,"input":[{"type":"message","role":"user","content":"hi"}]}`)
	out, err := AdaptForChatGPTBackend(body)
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	if !gjson.GetBytes(out, "stream").Bool() {
		t.Errorf("stream must be true for /v1/responses (upstream rejects false)")
	}
}

func TestAdaptStreamForcedFalseForCompact(t *testing.T) {
	body := []byte(`{"model":"x","stream":true,"input":[{"type":"message","role":"user","content":"hi"}]}`)
	out, err := AdaptForChatGPTCompact(body)
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	if gjson.GetBytes(out, "stream").Bool() {
		t.Errorf("stream must be false for /v1/responses/compact")
	}
}
