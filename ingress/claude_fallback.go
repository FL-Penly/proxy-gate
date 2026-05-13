package ingress

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type ClaudeFallbackPolicy struct {
	Enabled bool                 `json:"enabled"`
	Rules   []ClaudeFallbackRule `json:"rules"`
}

type ClaudeFallbackRule struct {
	Enabled     bool   `json:"enabled"`
	FromModel   string `json:"from_model"`
	FromVariant string `json:"from_variant,omitempty"`
	ToModel     string `json:"to_model"`
	ToVariant   string `json:"to_variant,omitempty"`
}

type ClaudeFallbackMatch struct {
	Rule        ClaudeFallbackRule
	FromModel   string
	FromVariant string
	ToModel     string
	ToVariant   string
}

type ClaudeFallbackRuntime struct {
	mu     sync.RWMutex
	policy ClaudeFallbackPolicy
}

func NewClaudeFallbackRuntime(policy ClaudeFallbackPolicy) *ClaudeFallbackRuntime {
	return &ClaudeFallbackRuntime{policy: policy.WithDefaults()}
}

func (r *ClaudeFallbackRuntime) Policy() ClaudeFallbackPolicy {
	if r == nil {
		return ClaudeFallbackPolicy{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.policy
}

func (r *ClaudeFallbackRuntime) Set(policy ClaudeFallbackPolicy) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policy = policy.WithDefaults()
}

func (r *ClaudeFallbackRuntime) Enabled() bool {
	return r.Policy().Enabled
}

func (r *ClaudeFallbackRuntime) Match(body []byte, headers http.Header) (ClaudeFallbackMatch, bool) {
	return r.Policy().Match(body, headers)
}

type ClaudeVariantInfo struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

func DefaultClaudeFallbackPolicy() ClaudeFallbackPolicy {
	return ClaudeFallbackPolicy{
		Enabled: true,
		Rules: []ClaudeFallbackRule{
			{Enabled: true, FromModel: "claude-opus-4-7", ToModel: "claude-opus-4-6", ToVariant: "preserve"},
			{Enabled: true, FromModel: "claude-haiku-4-5", ToModel: "claude-sonnet-4-6", ToVariant: "preserve"},
			{Enabled: true, FromModel: "claude-sonnet-4-6", ToModel: "claude-sonnet-4-6", ToVariant: "preserve"},
		},
	}
}

func ClaudeVariantCatalog() []ClaudeVariantInfo {
	return []ClaudeVariantInfo{
		{ID: "preserve", Label: "Preserve", Description: "Keep incoming request thinking/options unchanged."},
		{ID: "default", Label: "Default", Description: "Remove explicit thinking options."},
		{ID: "none", Label: "None", Description: "Remove explicit thinking options."},
		{ID: "thinking-16k", Label: "Thinking 16k", Description: "Set Anthropic thinking budget to 16k tokens."},
		{ID: "thinking-32k", Label: "Thinking 32k", Description: "Set Anthropic thinking budget to 32k tokens."},
		{ID: "thinking-64k", Label: "Thinking 64k", Description: "Set Anthropic thinking budget to 64k tokens."},
		{ID: "long-context", Label: "Long Context 1M", Description: "No request patch for Claude 4.6+ 1M models; kept as an explicit routing label."},
	}
}

func (p ClaudeFallbackPolicy) WithDefaults() ClaudeFallbackPolicy {
	if len(p.Rules) == 0 {
		return DefaultClaudeFallbackPolicy()
	}
	for i := range p.Rules {
		p.Rules[i].FromModel = NormalizeClaudeModel(p.Rules[i].FromModel)
		p.Rules[i].ToModel = NormalizeClaudeModel(p.Rules[i].ToModel)
		p.Rules[i].FromVariant = normalizeVariant(p.Rules[i].FromVariant)
		p.Rules[i].ToVariant = normalizeVariant(p.Rules[i].ToVariant)
		if p.Rules[i].ToVariant == "" {
			p.Rules[i].ToVariant = "preserve"
		}
	}
	return p
}

func (p ClaudeFallbackPolicy) Match(body []byte, headers http.Header) (ClaudeFallbackMatch, bool) {
	if !p.Enabled {
		return ClaudeFallbackMatch{}, false
	}
	p = p.WithDefaults()
	model := NormalizeClaudeModel(gjson.GetBytes(body, "model").String())
	variant := DetectClaudeVariant(body, headers)
	for _, rule := range p.Rules {
		if !rule.Enabled {
			continue
		}
		from := NormalizeClaudeModel(rule.FromModel)
		if from == "" || from != model {
			continue
		}
		rv := normalizeVariant(rule.FromVariant)
		if rv != "" && rv != variant {
			continue
		}
		to := NormalizeClaudeModel(rule.ToModel)
		if to == "" {
			continue
		}
		tv := normalizeVariant(rule.ToVariant)
		if tv == "" {
			tv = "preserve"
		}
		return ClaudeFallbackMatch{Rule: rule, FromModel: model, FromVariant: variant, ToModel: to, ToVariant: tv}, true
	}
	return ClaudeFallbackMatch{}, false
}

func NormalizeClaudeModel(model string) string {
	s := strings.ToLower(strings.TrimSpace(model))
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, ".", "-")
	s = regexp.MustCompile(`-+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	suffix := ""
	if idx := strings.IndexByte(s, '@'); idx >= 0 {
		suffix = s[idx:]
		s = s[:idx]
	}
	re := regexp.MustCompile(`(?:^|-)claude-?(opus|sonnet|haiku)-?([0-9]+)-?([0-9]+)(?:-|$)`)
	if m := re.FindStringSubmatch(s); len(m) == 4 {
		return fmt.Sprintf("claude-%s-%s-%s%s", m[1], m[2], m[3], suffix)
	}
	re = regexp.MustCompile(`(?:^|-)(opus|sonnet|haiku)-?([0-9]+)-?([0-9]+)(?:-|$)`)
	if m := re.FindStringSubmatch(s); len(m) == 4 {
		return fmt.Sprintf("claude-%s-%s-%s%s", m[1], m[2], m[3], suffix)
	}
	return s + suffix
}

func DetectClaudeVariant(body []byte, headers http.Header) string {
	if headers != nil {
		if v := headers.Get("x-proxygate-variant"); v != "" {
			return normalizeVariant(v)
		}
	}
	if v := gjson.GetBytes(body, "metadata.proxygate_variant").String(); v != "" {
		return normalizeVariant(v)
	}
	typ := strings.ToLower(gjson.GetBytes(body, "thinking.type").String())
	switch typ {
	case "enabled":
		budget := gjson.GetBytes(body, "thinking.budget_tokens").Int()
		if budget > 0 {
			return "thinking-" + strconv.FormatInt((budget+999)/1000, 10) + "k"
		}
		return "thinking"
	case "disabled":
		return "none"
	}
	return "default"
}

func ApplyClaudeTarget(body []byte, model, variant string) ([]byte, error) {
	out, err := sjson.SetBytes(body, "model", model)
	if err != nil {
		return nil, err
	}
	return ApplyClaudeVariant(out, variant)
}

func ApplyClaudeVariant(body []byte, variant string) ([]byte, error) {
	v := normalizeVariant(variant)
	switch v {
	case "", "preserve", "long-context":
		return body, nil
	case "default", "none":
		return sjson.DeleteBytes(body, "thinking")
	}
	if strings.HasPrefix(v, "thinking-") && strings.HasSuffix(v, "k") {
		num := strings.TrimSuffix(strings.TrimPrefix(v, "thinking-"), "k")
		k, err := strconv.Atoi(num)
		if err != nil || k <= 0 {
			return nil, fmt.Errorf("invalid claude variant %q", variant)
		}
		out, err := sjson.SetBytes(body, "thinking.type", "enabled")
		if err != nil {
			return nil, err
		}
		out, err = sjson.SetBytes(out, "thinking.budget_tokens", k*1000)
		if err != nil {
			return nil, err
		}
		if max := gjson.GetBytes(out, "max_tokens").Int(); max > 0 && max <= int64(k*1000) {
			out, err = sjson.SetBytes(out, "max_tokens", k*1000+1024)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported claude variant %q", variant)
}

func normalizeVariant(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "_", "-")
	v = strings.ReplaceAll(v, " ", "-")
	return strings.Trim(v, "-")
}

func ParseClaudeFallbackPolicy(raw []byte) (ClaudeFallbackPolicy, error) {
	var p ClaudeFallbackPolicy
	if len(raw) == 0 {
		return DefaultClaudeFallbackPolicy(), nil
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ClaudeFallbackPolicy{}, err
	}
	return p.WithDefaults(), nil
}
