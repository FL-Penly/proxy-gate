package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Claims struct {
	AccountID string
	PlanType  string
	UserID    string
	Email     string
	ExpiresAt time.Time
	Raw       map[string]any
}

func DecodeJWT(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("jwt: invalid format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		alt, err2 := base64.URLEncoding.DecodeString(parts[1])
		if err2 != nil {
			return nil, fmt.Errorf("jwt: decode payload: %w", err)
		}
		payload = alt
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("jwt: parse payload: %w", err)
	}
	return raw, nil
}

func ExtractAccountClaims(accessToken string) (Claims, error) {
	raw, err := DecodeJWT(accessToken)
	if err != nil {
		return Claims{}, err
	}
	c := Claims{Raw: raw}

	authObj, _ := raw["https://api.openai.com/auth"].(map[string]any)
	profileObj, _ := raw["https://api.openai.com/profile"].(map[string]any)

	c.AccountID = pickAccountID(raw, authObj)
	if v := strFromAny(authObj["chatgpt_plan_type"]); v != "" {
		c.PlanType = v
	} else {
		c.PlanType = "free"
	}
	c.UserID = strFromAny(authObj["chatgpt_user_id"])
	if c.UserID == "" {
		c.UserID = strFromAny(raw["sub"])
	}
	c.Email = strFromAny(profileObj["email"])
	if c.Email == "" {
		c.Email = strFromAny(raw["email"])
	}
	if exp, ok := raw["exp"].(float64); ok {
		c.ExpiresAt = time.Unix(int64(exp), 0)
	}
	return c, nil
}

func pickAccountID(raw, authObj map[string]any) string {
	if id := strFromAny(authObj["chatgpt_account_id"]); id != "" {
		return id
	}
	if id := strFromAny(raw["chatgpt_account_id"]); id != "" {
		return id
	}
	if orgs, ok := raw["organizations"].([]any); ok && len(orgs) > 0 {
		if first, ok := orgs[0].(map[string]any); ok {
			if id := strFromAny(first["id"]); id != "" {
				return id
			}
		}
	}
	return ""
}

func strFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
