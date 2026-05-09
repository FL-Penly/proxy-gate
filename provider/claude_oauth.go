package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const claudeProfileURL = "https://api.anthropic.com/api/oauth/profile"
const ClaudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

var claudeProfileURLOverride string
var ClaudeUsageURLOverride string

type ClaudeProfile struct {
	Email            string
	AccountID        string
	SubscriptionType string
	RateLimitTier    string
}

type ClaudeUsage struct {
	PrimaryUsedPct   float64
	SecondaryUsedPct float64
	PrimaryResetAt   time.Time
	SecondaryResetAt time.Time
}

func ExchangeClaudeCode(ctx context.Context, code, verifier, redirectURI, state string) (ClaudeToken, error) {
	body := map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"redirect_uri":  redirectURI,
		"client_id":     ClaudeClientID,
		"code_verifier": verifier,
		"state":         state,
	}
	return postClaudeOAuthToken(ctx, body)
}

func postClaudeOAuthToken(ctx context.Context, payload map[string]string) (ClaudeToken, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return ClaudeToken{}, err
	}
	url := ClaudeTokenURL
	if ClaudeTokenURLOverride != "" {
		url = ClaudeTokenURLOverride
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return ClaudeToken{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return ClaudeToken{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ClaudeToken{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ClaudeToken{}, &TokenError{Status: resp.StatusCode, Body: string(body)}
	}
	var rawTok claudeTokenResponse
	if err := json.Unmarshal(body, &rawTok); err != nil {
		return ClaudeToken{}, fmt.Errorf("claude oauth token: parse: %w", err)
	}
	if rawTok.AccessToken == "" {
		return ClaudeToken{}, fmt.Errorf("claude oauth token: empty access_token")
	}
	tok := ClaudeToken{AccessToken: rawTok.AccessToken, RefreshToken: rawTok.RefreshToken, ExpiresIn: rawTok.ExpiresIn}
	if rawTok.ExpiresIn > 0 {
		tok.ExpiresAt = time.Now().Add(time.Duration(rawTok.ExpiresIn) * time.Second)
	}
	return tok, nil
}

func GetClaudeProfile(ctx context.Context, accessToken string) (ClaudeProfile, error) {
	url := claudeProfileURL
	if claudeProfileURLOverride != "" {
		url = claudeProfileURLOverride
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ClaudeProfile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", ClaudeOAuthBetaHeader)
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return ClaudeProfile{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ClaudeProfile{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ClaudeProfile{}, &TokenError{Status: resp.StatusCode, Body: string(body)}
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ClaudeProfile{}, fmt.Errorf("claude profile: parse: %w", err)
	}
	account := childMap(m, "account")
	org := childMap(m, "organization")
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
			if v, ok := account[k].(string); ok && v != "" {
				return v
			}
			if v, ok := org[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	subscriptionType := get("subscription_type", "subscriptionType", "plan_type", "planType")
	if subscriptionType == "" {
		switch {
		case boolField(account, "has_claude_max"):
			subscriptionType = "claude_max"
		case boolField(account, "has_claude_pro"):
			subscriptionType = "claude_pro"
		}
	}
	return ClaudeProfile{
		Email:            get("email", "email_address", "emailAddress"),
		AccountID:        get("account_id", "accountId", "uuid", "id"),
		SubscriptionType: subscriptionType,
		RateLimitTier:    get("rate_limit_tier", "rateLimitTier"),
	}, nil
}

func GetClaudeUsage(ctx context.Context, accessToken string) (ClaudeUsage, error) {
	url := ClaudeUsageURL
	if ClaudeUsageURLOverride != "" {
		url = ClaudeUsageURLOverride
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ClaudeUsage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", ClaudeOAuthBetaHeader)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "claude-code/1.0")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return ClaudeUsage{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ClaudeUsage{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ClaudeUsage{}, &TokenError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	slog.Debug("claude usage raw response", "status", resp.StatusCode, "body", string(body))
	return parseClaudeUsage(body), nil
}

func parseClaudeUsage(body []byte) ClaudeUsage {
	root := gjson.ParseBytes(body)
	primary := firstExisting(root, "five_hour", "fiveHour", "rate_limits.five_hour")
	secondary := firstExisting(root, "seven_day", "sevenDay", "weekly", "rate_limits.seven_day")
	return ClaudeUsage{
		PrimaryUsedPct:   usagePct(primary),
		SecondaryUsedPct: usagePct(secondary),
		PrimaryResetAt:   claudeResetAt(primary),
		SecondaryResetAt: claudeResetAt(secondary),
	}
}

func firstExisting(root gjson.Result, paths ...string) gjson.Result {
	for _, p := range paths {
		v := root.Get(p)
		if v.Exists() {
			return v
		}
	}
	return gjson.Result{}
}

func usagePct(window gjson.Result) float64 {
	for _, path := range []string{"utilization", "used_percentage", "used_percent", "percentage", "usage_percent"} {
		if v := window.Get(path); v.Exists() {
			pct := v.Float()
			if pct >= 1 {
				pct /= 100
			}
			if pct < 0 {
				return 0
			}
			if pct > 1 {
				return 1
			}
			return pct
		}
	}
	return 0
}

func claudeResetAt(window gjson.Result) time.Time {
	for _, path := range []string{"resets_at", "reset_at", "resetAt"} {
		if v := window.Get(path); v.Exists() {
			if t := parseClaudeReset(v); !t.IsZero() {
				return t
			}
		}
	}
	for _, path := range []string{"reset_after_seconds", "resets_in_seconds"} {
		if after := window.Get(path).Int(); after > 0 {
			return time.Now().Add(time.Duration(after) * time.Second)
		}
	}
	return time.Time{}
}

func parseClaudeReset(v gjson.Result) time.Time {
	if s := v.String(); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	if epoch := v.Int(); epoch > 0 {
		if epoch > 1e12 {
			return time.UnixMilli(epoch)
		}
		return time.Unix(epoch, 0)
	}
	return time.Time{}
}

func childMap(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

func boolField(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}
