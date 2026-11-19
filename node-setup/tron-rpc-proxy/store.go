package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

var errStore = errors.New("journal unavailable")

type Entry struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
	Body        []byte `json:"body"`
	Expiration  int64  `json:"expiration"`
	Attempts    int    `json:"attempts"`
	Accepted    bool   `json:"accepted"`
}

type Store struct{ db *bolt.DB }

func openStore(filename, genesis string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, errStore
	}
	db, err := bolt.Open(filename, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, errStore
	}
	// Kubernetes fsGroup may widen an existing file's mode when remounting a PVC.
	if err := os.Chmod(filename, 0600); err != nil {
		db.Close()
		return nil, errStore
	}
	s := &Store{db: db}
	err = db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists([]byte("meta"))
		if err != nil {
			return err
		}
		if v := meta.Get([]byte("schema")); v != nil && string(v) != "1" {
			return errStore
		}
		if v := meta.Get([]byte("genesis")); v != nil && string(v) != genesis {
			return errStore
		}
		for key, value := range map[string]string{"schema": "1", "genesis": genesis} {
			if err := meta.Put([]byte(key), []byte(value)); err != nil {
				return err
			}
		}
		if meta.Get([]byte("mode")) == nil {
			if err := meta.Put([]byte("mode"), []byte("external")); err != nil {
				return err
			}
		}
		_, err = tx.CreateBucketIfNotExists([]byte("broadcasts"))
		return err
	})
	if err != nil {
		db.Close()
		return nil, errStore
	}
	return s, nil
}

func (s *Store) mode() (string, error) {
	var mode string
	err := s.db.View(func(tx *bolt.Tx) error { mode = string(tx.Bucket([]byte("meta")).Get([]byte("mode"))); return nil })
	if err != nil || (mode != "local" && mode != "external") {
		return "", errStore
	}
	return mode, nil
}

func (s *Store) setMode(mode string) error {
	if mode != "local" && mode != "external" {
		return errStore
	}
	if err := s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("meta")).Put([]byte("mode"), []byte(mode)) }); err != nil {
		return errStore
	}
	return nil
}

func (s *Store) get(id string) (*Entry, error) {
	var entry *Entry
	err := s.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket([]byte("broadcasts")).Get([]byte(id))
		if value == nil {
			return nil
		}
		entry = &Entry{}
		return json.Unmarshal(value, entry)
	})
	if err != nil {
		return nil, errStore
	}
	return entry, nil
}

func (s *Store) save(entry *Entry) error {
	value, err := json.Marshal(entry)
	if err != nil {
		return errStore
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("broadcasts"))
		if bucket.Get([]byte(entry.ID)) == nil && bucket.Stats().KeyN >= 50000 {
			return errStore
		}
		return bucket.Put([]byte(entry.ID), value)
	})
	if err != nil {
		return errStore
	}
	return nil
}
