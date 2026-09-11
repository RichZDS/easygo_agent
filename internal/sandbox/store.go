package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

var applicationBucket = []byte("applications-v1")

// BoltStore is the durable half of controller recovery. Docker labels are the
// second half and are reconciled through Manager.Reconcile.
type BoltStore struct {
	mu     sync.Mutex
	db     *bolt.DB
	closed bool
}

func OpenBoltStore(path string) (*BoltStore, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("bbolt state path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open bbolt state: %w", err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(applicationBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize bbolt state: %w", err)
	}
	return &BoltStore{db: db}, nil
}

func (store *BoltStore) List() ([]Application, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil, errors.New("bbolt state is closed")
	}
	var result []Application
	err := store.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(applicationBucket)
		return bucket.ForEach(func(_, value []byte) error {
			var application Application
			if err := json.Unmarshal(value, &application); err != nil {
				return fmt.Errorf("decode application state: %w", err)
			}
			result = append(result, application)
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("list bbolt state: %w", err)
	}
	return result, nil
}

func (store *BoltStore) Put(application Application) error {
	data, err := json.Marshal(application)
	if err != nil {
		return fmt.Errorf("encode application state: %w", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return errors.New("bbolt state is closed")
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(applicationBucket).Put([]byte(application.ID), data)
	}); err != nil {
		return fmt.Errorf("put bbolt state: %w", err)
	}
	return nil
}

func (store *BoltStore) Delete(applicationID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return errors.New("bbolt state is closed")
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(applicationBucket).Delete([]byte(applicationID))
	}); err != nil {
		return fmt.Errorf("delete bbolt state: %w", err)
	}
	return nil
}

func (store *BoltStore) Close() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	return store.db.Close()
}
