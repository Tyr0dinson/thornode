package substrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const signedExtrinsicPrefix = "signed-extrinsic/"

type signedExtrinsicRecord struct {
	// TxHash is stored in the key.
	TxHash         string `json:"-"`
	VaultAddress   string `json:"vault_address"`
	Nonce          uint32 `json:"nonce"`
	EraBirthHeight int64  `json:"era_birth_height,omitempty"`
	RecheckHeight  int64  `json:"recheck_height"`
}

type byteStateStore interface {
	Get(key string) (value []byte, found bool, err error)
	Put(key string, value []byte) error
	Delete(key string) error
	Iterate(prefix string, fn func(key string, value []byte) error) error
}

// stateStore persists Substrate-specific records.
type stateStore struct {
	state byteStateStore
}

// newStateStore creates a Substrate state store.
func newStateStore(state byteStateStore) (*stateStore, error) {
	if state == nil {
		return nil, fmt.Errorf("state store is nil")
	}
	return &stateStore{state: state}, nil
}

// Validate checks fields required for safe recovery decisions.
func (record signedExtrinsicRecord) Validate() error {
	if record.TxHash == "" {
		return fmt.Errorf("transaction hash is empty")
	}
	if record.VaultAddress == "" {
		return fmt.Errorf("vault address is empty")
	}
	if record.RecheckHeight < 0 {
		return fmt.Errorf("recheck height is negative")
	}
	if record.EraBirthHeight < 0 {
		return fmt.Errorf("era birth height is negative")
	}
	if record.EraBirthHeight > record.RecheckHeight {
		return fmt.Errorf("era birth height is after recheck height")
	}
	return nil
}

// signedExtrinsicKey validates and prefixes a transaction hash.
func signedExtrinsicKey(txHash string) (string, error) {
	if txHash == "" {
		return "", fmt.Errorf("transaction hash is empty")
	}
	return signedExtrinsicPrefix + txHash, nil
}

// PutExtrinsic validates and persists a signed-extrinsic recovery record.
func (s *stateStore) PutExtrinsic(record signedExtrinsicRecord) error {
	err := record.Validate()
	if err != nil {
		return fmt.Errorf("invalid signed extrinsic record: %w", err)
	}
	key, err := signedExtrinsicKey(record.TxHash)
	if err != nil {
		return err
	}
	value, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to marshal signed extrinsic record: %w", err)
	}
	err = s.state.Put(key, value)
	if err != nil {
		return fmt.Errorf("failed to persist signed extrinsic record: %w", err)
	}
	return nil
}

// GetExtrinsic loads and validates a signed-extrinsic recovery record.
func (s *stateStore) GetExtrinsic(txHash string) (signedExtrinsicRecord, bool, error) {
	key, err := signedExtrinsicKey(txHash)
	if err != nil {
		return signedExtrinsicRecord{}, false, err
	}
	value, found, err := s.state.Get(key)
	if err != nil {
		return signedExtrinsicRecord{}, false, fmt.Errorf("failed to load signed extrinsic record: %w", err)
	}
	if !found {
		return signedExtrinsicRecord{}, false, nil
	}
	var record signedExtrinsicRecord
	err = json.Unmarshal(value, &record)
	if err != nil {
		return signedExtrinsicRecord{}, false, fmt.Errorf("failed to unmarshal signed extrinsic record: %w", err)
	}
	record.TxHash = txHash
	err = record.Validate()
	if err != nil {
		return signedExtrinsicRecord{}, false, fmt.Errorf("invalid signed extrinsic record: %w", err)
	}
	return record, true, nil
}

// DeleteExtrinsic removes a signed-extrinsic recovery record.
func (s *stateStore) DeleteExtrinsic(txHash string) error {
	key, err := signedExtrinsicKey(txHash)
	if err != nil {
		return err
	}
	err = s.state.Delete(key)
	if err != nil {
		return fmt.Errorf("failed to delete signed extrinsic record: %w", err)
	}
	return nil
}

// SweepExpired removes records with recheck heights below cutoff.
func (s *stateStore) SweepExpired(cutoff int64) (int, error) {
	var expired []string
	var recordErrors []error

	err := s.state.Iterate(signedExtrinsicPrefix, func(key string, value []byte) error {
		var record signedExtrinsicRecord
		if err := json.Unmarshal(value, &record); err != nil {
			recordErrors = append(recordErrors, fmt.Errorf("failed to decode %s: %w", key, err))
			return nil
		}
		record.TxHash = strings.TrimPrefix(key, signedExtrinsicPrefix)
		if err := record.Validate(); err != nil {
			recordErrors = append(recordErrors, fmt.Errorf("invalid record %s: %w", key, err))
			return nil
		}
		if record.RecheckHeight > 0 && record.RecheckHeight < cutoff {
			expired = append(expired, key)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("failed to scan signed extrinsic records: %w", err)
	}

	deleted := 0
	for _, key := range expired {
		if err = s.state.Delete(key); err != nil {
			return deleted, errors.Join(
				fmt.Errorf("failed to delete expired signed extrinsic record: %w", err),
				errors.Join(recordErrors...),
			)
		}
		deleted++
	}

	return deleted, errors.Join(recordErrors...)
}
