package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/codeking-ai/cligate-v2/auth"
	"github.com/codeking-ai/cligate-v2/broker"
	"github.com/codeking-ai/cligate-v2/provider"
)

func AddAccountInteractive(ctx context.Context, poolDir string, openBrowser bool) (*broker.Account, error) {
	pkce, err := auth.NewPKCE()
	if err != nil {
		return nil, err
	}
	state, err := auth.NewState()
	if err != nil {
		return nil, err
	}

	cb, err := auth.StartOpenAICallback(ctx, state)
	if err != nil {
		return nil, err
	}
	defer cb.Close()

	authURL := auth.OpenAIAuthorizeURL(cb.RedirectURI(), pkce.Challenge, state)
	fmt.Fprintln(os.Stderr, "[auth] Open this URL in a browser to log in:")
	fmt.Fprintln(os.Stderr, authURL)
	if openBrowser {
		_ = auth.OpenBrowser(authURL)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	res, err := cb.Wait(waitCtx)
	if err != nil {
		return nil, err
	}

	tok, err := provider.ExchangeOpenAICode(ctx, res.Code, pkce.Verifier, cb.RedirectURI())
	if err != nil {
		return nil, fmt.Errorf("exchange: %w", err)
	}
	claims, err := auth.ExtractAccountClaims(tok.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("decode access_token: %w", err)
	}
	if claims.Email == "" {
		return nil, errors.New("auth: email missing from access token claims")
	}
	expires := tok.ExpiresAt
	if expires.IsZero() && !claims.ExpiresAt.IsZero() {
		expires = claims.ExpiresAt
	}
	acc := &broker.Account{
		Email:        claims.Email,
		AccountID:    claims.AccountID,
		PlanType:     broker.PlanType(strings.ToLower(claims.PlanType)),
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		IDToken:      tok.IDToken,
		ExpiresAt:    expires,
		CreatedAt:    time.Now().UTC(),
	}
	path, err := broker.SaveAccountFile(poolDir, acc)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "[auth] saved: %s (account_id=%s, plan=%s)\n", path, acc.AccountID, acc.PlanType)
	return acc, nil
}

func AddAccountFromCode(ctx context.Context, poolDir, codeOrURL string) (*broker.Account, error) {
	code, _, err := extractCodeFromInput(codeOrURL)
	if err != nil {
		return nil, err
	}
	pkce, err := auth.NewPKCE()
	if err != nil {
		return nil, err
	}
	cb, err := auth.StartOpenAICallback(ctx, "")
	if err != nil {
		return nil, err
	}
	cb.Close()
	tok, err := provider.ExchangeOpenAICode(ctx, code, pkce.Verifier, cb.RedirectURI())
	if err != nil {
		return nil, err
	}
	claims, err := auth.ExtractAccountClaims(tok.AccessToken)
	if err != nil {
		return nil, err
	}
	acc := &broker.Account{
		Email:        claims.Email,
		AccountID:    claims.AccountID,
		PlanType:     broker.PlanType(strings.ToLower(claims.PlanType)),
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		IDToken:      tok.IDToken,
		ExpiresAt:    tok.ExpiresAt,
		CreatedAt:    time.Now().UTC(),
	}
	if _, err := broker.SaveAccountFile(poolDir, acc); err != nil {
		return nil, err
	}
	return acc, nil
}

func extractCodeFromInput(input string) (code string, state string, err error) {
	trimmed := strings.TrimSpace(input)
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		idx := strings.IndexByte(trimmed, '?')
		if idx < 0 {
			return "", "", errors.New("URL has no query string")
		}
		for _, kv := range strings.Split(trimmed[idx+1:], "&") {
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) != 2 {
				continue
			}
			switch parts[0] {
			case "code":
				code = parts[1]
			case "state":
				state = parts[1]
			case "error":
				return "", "", fmt.Errorf("oauth error: %s", parts[1])
			}
		}
		if code == "" {
			return "", "", errors.New("no code in URL")
		}
		return code, state, nil
	}
	if len(trimmed) < 10 {
		return "", "", errors.New("input too short to be an authorization code")
	}
	return trimmed, "", nil
}

func ListAccounts(poolDir string) ([]string, error) {
	entries, err := os.ReadDir(poolDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	return out, nil
}
