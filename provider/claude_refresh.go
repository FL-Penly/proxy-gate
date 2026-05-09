package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	ClaudeTokenURL = "https://console.anthropic.com/v1/oauth/token"
	ClaudeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
)

var ClaudeTokenURLOverride string

type ClaudeToken struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	ExpiresAt    time.Time
}

type claudeTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

func RefreshClaudeToken(ctx context.Context, refreshToken string) (ClaudeToken, error) {
	if refreshToken == "" {
		return ClaudeToken{}, fmt.Errorf("claude oauth: missing refresh_token")
	}
	body := `{"grant_type":"refresh_token","refresh_token":"` + jsonEscape(refreshToken) + `","client_id":"` + ClaudeClientID + `"}`
	tok, err := postClaudeToken(ctx, body)
	if err != nil {
		return tok, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

func postClaudeToken(ctx context.Context, body string) (ClaudeToken, error) {
	target := ClaudeTokenURL
	if ClaudeTokenURLOverride != "" {
		target = ClaudeTokenURLOverride
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		return ClaudeToken{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ClaudeToken{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ClaudeToken{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ClaudeToken{}, &TokenError{Status: resp.StatusCode, Body: string(raw)}
	}
	var parsed claudeTokenResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ClaudeToken{}, fmt.Errorf("claude oauth token: parse: %w", err)
	}
	if parsed.AccessToken == "" {
		return ClaudeToken{}, fmt.Errorf("claude oauth token: empty access_token")
	}
	tok := ClaudeToken{AccessToken: parsed.AccessToken, RefreshToken: parsed.RefreshToken, ExpiresIn: parsed.ExpiresIn}
	if parsed.ExpiresIn > 0 {
		tok.ExpiresAt = time.Now().Add(time.Duration(parsed.ExpiresIn) * time.Second)
	}
	return tok, nil
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return strings.Trim(string(b), `"`)
}
