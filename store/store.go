package store

import (
	"errors"
	"fmt"
	"time"

	"go.etcd.io/bbolt"
)

const (
	BucketAccounts = "accounts"
	BucketAPIKeys  = "apikeys"
	BucketUsage    = "usage"
	BucketSettings = "settings"
	BucketPins     = "pins"
)

var allBuckets = []string{BucketAccounts, BucketAPIKeys, BucketUsage, BucketSettings, BucketPins}

var ErrNotFound = errors.New("store: not found")

type Store struct {
	db *bbolt.DB
}

func Open(path string) (*Store, error) {
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		for _, b := range allBuckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(b)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create buckets: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *bbolt.DB {
	return s.db
}

func (s *Store) Get(bucket, key string) ([]byte, error) {
	var out []byte
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("bucket %s missing", bucket)
		}
		v := b.Get([]byte(key))
		if v == nil {
			return ErrNotFound
		}
		out = make([]byte, len(v))
		copy(out, v)
		return nil
	})
	return out, err
}

func (s *Store) Put(bucket, key string, value []byte) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("bucket %s missing", bucket)
		}
		return b.Put([]byte(key), value)
	})
}

func (s *Store) Delete(bucket, key string) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("bucket %s missing", bucket)
		}
		return b.Delete([]byte(key))
	})
}

func (s *Store) ForEach(bucket string, fn func(key, value []byte) error) error {
	return s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("bucket %s missing", bucket)
		}
		return b.ForEach(fn)
	})
}
