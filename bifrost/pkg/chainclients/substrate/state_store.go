package substrate

import (
	"encoding/json"
	"fmt"
)

const signedExtrinsicPrefix = "signed-extrinsic/"

type signedExtrinsicRecord struct {
	TxHash        string `json:"transaction_hash"`
	VaultAddress  string `json:"vault_address"`
	Nonce         uint32 `json:"nonce"`
	RecheckHeight int64  `json:"recheck_height"`
}

type byteStateStore interface {
	Get(key string) (value []byte, found bool, err error)
	Put(key string, value []byte) error
	Delete(key string) error
}

// stateStore is the typed persistence boundary for the Substrate client.
// Record encoding and validation stay here; the underlying generic store only
// handles chain-isolated bytes.
type stateStore struct {
	state byteStateStore
}

// newStateStore creates the typed Substrate state boundary over a generic byte store.
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
	return nil
}

// signedExtrinsicKey validates a transaction hash and adds the record-family prefix.
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
	err = record.Validate()
	if err != nil {
		return signedExtrinsicRecord{}, false, fmt.Errorf("invalid signed extrinsic record: %w", err)
	}
	if record.TxHash != txHash {
		return signedExtrinsicRecord{}, false, fmt.Errorf("signed extrinsic record hash mismatch")
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
