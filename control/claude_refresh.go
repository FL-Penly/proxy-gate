package control

import (
	"context"
	"encoding/json"
	"time"

	"github.com/FL-Penly/proxy-gate/broker"
	"github.com/FL-Penly/proxy-gate/provider"
	"github.com/FL-Penly/proxy-gate/store"
)

type ClaudeTokenRefresher struct {
	PoolDir string
	Queue   *Queue
}

func (t *ClaudeTokenRefresher) RefreshClaudeToken(ctx context.Context, acc *broker.ClaudeAccount) error {
	tok, err := provider.RefreshClaudeToken(ctx, acc.RefreshToken)
	if err != nil {
		acc.MarkRefreshFailed(time.Now(), err.Error())
		t.persistStats(acc)
		return err
	}
	expires := tok.ExpiresAt
	if expires.IsZero() {
		expires = time.Now().Add(time.Hour)
	}
	acc.UpdateTokens(tok.AccessToken, tok.RefreshToken, expires)
	acc.MarkRefreshed(time.Now())
	if t.PoolDir != "" {
		if _, err := broker.SaveClaudeAccountFile(t.PoolDir, acc); err != nil {
			return err
		}
	}
	t.persistStats(acc)
	return nil
}

func (t *ClaudeTokenRefresher) persistStats(acc *broker.ClaudeAccount) {
	if t.Queue == nil {
		return
	}
	data, err := json.Marshal(acc.Stats())
	if err != nil {
		return
	}
	_ = t.Queue.Put(store.BucketClaudeAccounts, "stats:"+acc.Email, data)
}
