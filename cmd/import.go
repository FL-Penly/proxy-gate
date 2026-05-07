package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/codeking-ai/cligate-v2/broker"
)

type v1Account struct {
	Email        string  `json:"email"`
	AccountID    string  `json:"accountId"`
	PlanType     string  `json:"planType"`
	AccessToken  string  `json:"accessToken"`
	RefreshToken string  `json:"refreshToken"`
	IDToken      string  `json:"idToken"`
	ExpiresAt    *int64  `json:"expiresAt"`
}

type v1AccountsFile struct {
	Accounts      []v1Account `json:"accounts"`
	ActiveAccount string      `json:"activeAccount"`
	Version       int         `json:"version"`
}

func ImportV1Accounts(srcPath, destDir string) (int, error) {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", srcPath, err)
	}
	var f v1AccountsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, fmt.Errorf("parse %s: %w", srcPath, err)
	}
	count := 0
	for _, src := range f.Accounts {
		if src.Email == "" || src.AccessToken == "" {
			continue
		}
		acc := &broker.Account{
			Email:        src.Email,
			AccountID:    src.AccountID,
			PlanType:     broker.PlanType(strings.ToLower(src.PlanType)),
			AccessToken:  src.AccessToken,
			RefreshToken: src.RefreshToken,
			IDToken:      src.IDToken,
			CreatedAt:    time.Now().UTC(),
		}
		if src.ExpiresAt != nil {
			acc.ExpiresAt = time.UnixMilli(*src.ExpiresAt).UTC()
		}
		if _, err := broker.SaveAccountFile(destDir, acc); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

type v1APIKey struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	APIKey  string `json:"apiKey"`
	BaseURL string `json:"baseUrl"`
}

type v1APIKeysFile struct {
	Keys []v1APIKey `json:"keys"`
}

func ImportV1APIKeys(srcPath, destDir string) (int, error) {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", srcPath, err)
	}
	var f v1APIKeysFile
	if err := json.Unmarshal(raw, &f); err != nil {
		var arr []v1APIKey
		if err2 := json.Unmarshal(raw, &arr); err2 != nil {
			return 0, fmt.Errorf("parse %s: %w", srcPath, errors.Join(err, err2))
		}
		f.Keys = arr
	}
	count := 0
	for _, src := range f.Keys {
		if src.APIKey == "" {
			continue
		}
		key := &broker.APIKey{
			ID:      src.ID,
			Name:    src.Name,
			Type:    broker.APIKeyType(src.Type),
			APIKey:  src.APIKey,
			BaseURL: src.BaseURL,
		}
		if key.ID == "" {
			key.ID = strings.ToLower(strings.TrimPrefix(src.Name, " "))
			if key.ID == "" {
				key.ID = fmt.Sprintf("imported-%d", count+1)
			}
		}
		if key.Type == "" {
			key.Type = broker.KeyTypeOpenAI
		}
		path := destDir + "/" + sanitizeID(key.ID) + ".json"
		out, err := json.MarshalIndent(key, "", "  ")
		if err != nil {
			return count, err
		}
		if err := os.WriteFile(path, out, 0o600); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func sanitizeID(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return strings.Trim(string(out), "_")
}
