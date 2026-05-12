package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const pendingFileName = ".pending-claude-oauth.json"
const pendingTTL = 10 * time.Minute

// PendingOAuth holds PKCE state between --no-browser and --code= calls.
type PendingOAuth struct {
	Verifier    string    `json:"verifier"`
	State       string    `json:"state"`
	RedirectURI string    `json:"redirect_uri"`
	CreatedAt   time.Time `json:"created_at"`
}

func WritePending(dataDir string, p *PendingOAuth) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("pending oauth: mkdir: %w", err)
	}
	path := filepath.Join(dataDir, pendingFileName)
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func ReadPending(dataDir string) (*PendingOAuth, error) {
	path := filepath.Join(dataDir, pendingFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no pending OAuth flow — run add-claude-account --no-browser first")
		}
		return nil, fmt.Errorf("read pending oauth: %w", err)
	}
	var p PendingOAuth
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parse pending oauth: %w", err)
	}
	if time.Since(p.CreatedAt) > pendingTTL {
		return nil, fmt.Errorf("pending OAuth expired (created %s ago) — run add-claude-account --no-browser again",
			time.Since(p.CreatedAt).Truncate(time.Second))
	}
	return &p, nil
}

func DeletePending(dataDir string) error {
	path := filepath.Join(dataDir, pendingFileName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
