package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/FL-Penly/proxy-gate/auth"
	"github.com/FL-Penly/proxy-gate/broker"
	"github.com/FL-Penly/proxy-gate/provider"
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

func AddClaudeAccountNoBrowser(dataDir string) error {
	pkce, err := auth.NewPKCE()
	if err != nil {
		return err
	}
	state, err := auth.NewState()
	if err != nil {
		return err
	}
	redirectURI := fmt.Sprintf("http://localhost:%d%s", auth.ClaudeCallbackPorts[0], auth.ClaudeCallbackPath)
	authURL := auth.ClaudeAuthorizeURL(redirectURI, pkce.Challenge, state)

	pending := &PendingOAuth{
		Verifier:    pkce.Verifier,
		State:       state,
		RedirectURI: redirectURI,
		CreatedAt:   time.Now().UTC(),
	}
	if err := WritePending(dataDir, pending); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "[auth] Open this URL in a browser:")
	fmt.Fprintln(os.Stderr, authURL)
	fmt.Fprintln(os.Stderr, "[auth] After login, the browser will redirect to a localhost URL that fails to load.")
	fmt.Fprintln(os.Stderr, "[auth] COPY the full address-bar URL and run:")
	fmt.Fprintln(os.Stderr, "[auth]   proxy-gate add-claude-account --code='<paste URL>'")
	return nil
}

func AddClaudeAccountFromCode(ctx context.Context, dataDir, poolDir, codeOrURL, email string) (*broker.ClaudeAccount, error) {
	pending, err := ReadPending(dataDir)
	if err != nil {
		return nil, err
	}
	code, state, err := extractCodeFromInput(codeOrURL)
	if err != nil {
		return nil, err
	}
	if state != "" && pending.State != "" && state != pending.State {
		return nil, fmt.Errorf("OAuth state mismatch")
	}
	tok, err := provider.ExchangeClaudeCode(ctx, code, pending.Verifier, pending.RedirectURI, pending.State)
	if err != nil {
		return nil, fmt.Errorf("exchange: %w", err)
	}
	prof, err := provider.GetClaudeProfile(ctx, tok.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	if email == "" {
		email = prof.Email
	}
	if email == "" {
		return nil, fmt.Errorf("claude auth: email missing from profile; pass --email=you@example.com")
	}
	acc := &broker.ClaudeAccount{
		Email:            email,
		AccountID:        prof.AccountID,
		SubscriptionType: prof.SubscriptionType,
		RateLimitTier:    prof.RateLimitTier,
		AccessToken:      tok.AccessToken,
		RefreshToken:     tok.RefreshToken,
		ExpiresAt:        tok.ExpiresAt,
		CreatedAt:        time.Now().UTC(),
	}
	if _, err := broker.SaveClaudeAccountFile(poolDir, acc); err != nil {
		return nil, err
	}
	_ = DeletePending(dataDir)
	return acc, nil
}
