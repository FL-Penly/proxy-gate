package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/FL-Penly/proxy-gate/broker"
)

type claudeCredentialsFile struct {
	Email            string `json:"email"`
	AccountID        string `json:"account_id"`
	SubscriptionType string `json:"subscription_type"`
	RateLimitTier    string `json:"rate_limit_tier"`
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresAt        any    `json:"expires_at"`
	ClaudeAIOAuth    struct {
		AccessToken      string `json:"accessToken"`
		RefreshToken     string `json:"refreshToken"`
		ExpiresAt        any    `json:"expiresAt"`
		Email            string `json:"email"`
		AccountID        string `json:"accountId"`
		SubscriptionType string `json:"subscriptionType"`
	} `json:"claudeAiOauth"`
}

func ImportClaudeAccount(srcPath, destDir, email string) (*broker.ClaudeAccount, error) {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", srcPath, err)
	}
	var native broker.ClaudeAccount
	if err := json.Unmarshal(raw, &native); err == nil && native.Email != "" && native.AccessToken != "" {
		if _, err := broker.SaveClaudeAccountFile(destDir, &native); err != nil {
			return nil, err
		}
		return &native, nil
	}
	var f claudeCredentialsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", srcPath, err)
	}
	acc := &broker.ClaudeAccount{
		Email:            firstNonEmpty(email, f.Email, f.ClaudeAIOAuth.Email),
		AccountID:        firstNonEmpty(f.AccountID, f.ClaudeAIOAuth.AccountID),
		SubscriptionType: firstNonEmpty(f.SubscriptionType, f.ClaudeAIOAuth.SubscriptionType),
		RateLimitTier:    f.RateLimitTier,
		AccessToken:      firstNonEmpty(f.AccessToken, f.ClaudeAIOAuth.AccessToken),
		RefreshToken:     firstNonEmpty(f.RefreshToken, f.ClaudeAIOAuth.RefreshToken),
		CreatedAt:        time.Now().UTC(),
	}
	acc.ExpiresAt = parseClaudeExpires(firstNonNil(f.ExpiresAt, f.ClaudeAIOAuth.ExpiresAt))
	if acc.Email == "" {
		return nil, fmt.Errorf("claude import: missing email; pass --email=you@example.com")
	}
	if acc.AccessToken == "" {
		return nil, fmt.Errorf("claude import: missing access token")
	}
	if _, err := broker.SaveClaudeAccountFile(destDir, acc); err != nil {
		return nil, err
	}
	return acc, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstNonNil(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

func parseClaudeExpires(v any) time.Time {
	switch x := v.(type) {
	case float64:
		if x > 1e12 {
			return time.UnixMilli(int64(x)).UTC()
		}
		if x > 0 {
			return time.Unix(int64(x), 0).UTC()
		}
	case string:
		if t, err := time.Parse(time.RFC3339, x); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
