package control

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/FL-Penly/proxy-gate/store"
)

type pinEntry struct {
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expires_at"`
}

type PinStore struct {
	store *store.Store
	queue *Queue
}

func NewPinStore(s *store.Store, q *Queue) *PinStore {
	return &PinStore{store: s, queue: q}
}

func (p *PinStore) Put(prevResponseID, email string, expires time.Time) error {
	if prevResponseID == "" || email == "" {
		return nil
	}
	data, err := json.Marshal(pinEntry{Email: email, ExpiresAt: expires})
	if err != nil {
		return err
	}
	return p.queue.Put(store.BucketPins, prevResponseID, data)
}

func (p *PinStore) Lookup(prevResponseID string) (string, bool) {
	if prevResponseID == "" {
		return "", false
	}
	raw, err := p.store.Get(store.BucketPins, prevResponseID)
	if err != nil {
		return "", false
	}
	var entry pinEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return "", false
	}
	if !entry.ExpiresAt.IsZero() && entry.ExpiresAt.Before(time.Now()) {
		_ = p.queue.Delete(store.BucketPins, prevResponseID)
		return "", false
	}
	return entry.Email, true
}

var ErrPinExpired = errors.New("pin: expired")
