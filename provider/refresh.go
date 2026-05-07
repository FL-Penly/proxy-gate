package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	openAITokenURL = "https://auth.openai.com/oauth/token"
	openAIClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
)

var openAITokenURLOverride string

func tokenURL() string {
	if openAITokenURLOverride != "" {
		return openAITokenURLOverride
	}
	return openAITokenURL
}

type Token struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	ExpiresIn    int
	ExpiresAt    time.Time
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

type TokenError struct {
	Status int
	Body   string
}

func (e *TokenError) Error() string {
	return fmt.Sprintf("oauth token request failed: %d %s", e.Status, e.Body)
}

func ExchangeOpenAICode(ctx context.Context, code, verifier, redirectURI string) (Token, error) {
	form := url.Values{
		"grant_type":    []string{"authorization_code"},
		"code":          []string{code},
		"redirect_uri":  []string{redirectURI},
		"client_id":     []string{openAIClientID},
		"code_verifier": []string{verifier},
	}
	return postOpenAIToken(ctx, form)
}

func RefreshOpenAIToken(ctx context.Context, refreshToken string) (Token, error) {
	form := url.Values{
		"grant_type":    []string{"refresh_token"},
		"refresh_token": []string{refreshToken},
		"client_id":     []string{openAIClientID},
	}
	tok, err := postOpenAIToken(ctx, form)
	if err != nil {
		return tok, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

func postOpenAIToken(ctx context.Context, form url.Values) (Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Token{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Token{}, &TokenError{Status: resp.StatusCode, Body: string(body)}
	}
	var raw tokenResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return Token{}, fmt.Errorf("oauth token: parse: %w", err)
	}
	if raw.AccessToken == "" {
		return Token{}, fmt.Errorf("oauth token: empty access_token")
	}
	tok := Token{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		IDToken:      raw.IDToken,
		ExpiresIn:    raw.ExpiresIn,
	}
	if raw.ExpiresIn > 0 {
		tok.ExpiresAt = time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second)
	}
	return tok, nil
}
