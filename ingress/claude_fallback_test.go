package ingress

import (
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeClaudeModel(t *testing.T) {
	cases := map[string]string{
		"claude-opus4.7":             "claude-opus-4-7",
		"claude opus 4.7":            "claude-opus-4-7",
		"claude_sonnet4_6":           "claude-sonnet-4-6",
		"haiku-4.5":                  "claude-haiku-4-5",
		"claude-haiku-4-5@20251001":  "claude-haiku-4-5@20251001",
		"claude-sonnet-4-6-thinking": "claude-sonnet-4-6",
		"unknown-model":              "unknown-model",
	}
	for in, want := range cases {
		if got := NormalizeClaudeModel(in); got != want {
			t.Fatalf("NormalizeClaudeModel(%q)=%q want %q", in, got, want)
		}
	}
}

func TestClaudeFallbackPolicyMatch(t *testing.T) {
	p := ClaudeFallbackPolicy{
		Enabled: true,
		Rules: []ClaudeFallbackRule{
			{Enabled: true, FromModel: "claude-opus4.7", FromVariant: "thinking-32k", ToModel: "claude-opus-4-6", ToVariant: "preserve"},
			{Enabled: true, FromModel: "claude-haiku4.5", ToModel: "claude-sonnet4.6", ToVariant: "thinking-16k"},
		},
	}
	body := []byte(`{"model":"claude-opus-4-7","thinking":{"type":"enabled","budget_tokens":32000},"messages":[]}`)
	match, ok := p.Match(body, http.Header{})
	if !ok {
		t.Fatal("expected fallback match")
	}
	if match.ToModel != "claude-opus-4-6" || match.ToVariant != "preserve" {
		t.Fatalf("match=%+v", match)
	}

	body = []byte(`{"model":"claude-haiku-4-5","messages":[]}`)
	match, ok = p.Match(body, http.Header{})
	if !ok || match.ToModel != "claude-sonnet-4-6" || match.ToVariant != "thinking-16k" {
		t.Fatalf("match=%+v ok=%v", match, ok)
	}
}

func TestApplyClaudeTargetVariant(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","max_tokens":1000,"messages":[]}`)
	out, err := ApplyClaudeTarget(body, "claude-sonnet-4-6", "thinking-16k")
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(out, "model").String(); got != "claude-sonnet-4-6" {
		t.Fatalf("model=%q", got)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "enabled" {
		t.Fatalf("thinking.type=%q", got)
	}
	if got := gjson.GetBytes(out, "thinking.budget_tokens").Int(); got != 16000 {
		t.Fatalf("budget=%d", got)
	}
	if got := gjson.GetBytes(out, "max_tokens").Int(); got <= 16000 {
		t.Fatalf("max_tokens=%d, expected bumped above thinking budget", got)
	}
}
