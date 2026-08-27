package substrate

import (
	"errors"
	"testing"
)

type memoryStateStore struct {
	values    map[string][]byte
	getErr    error
	putErr    error
	deleteErr error
}

func (s *memoryStateStore) Get(key string) ([]byte, bool, error) {
	if s.getErr != nil {
		return nil, false, s.getErr
	}
	value, found := s.values[key]
	return append([]byte(nil), value...), found, nil
}

func (s *memoryStateStore) Put(key string, value []byte) error {
	if s.putErr != nil {
		return s.putErr
	}
	if s.values == nil {
		s.values = make(map[string][]byte)
	}
	s.values[key] = append([]byte(nil), value...)
	return nil
}

func (s *memoryStateStore) Delete(key string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.values, key)
	return nil
}

func TestStateStoreExtrinsicRoundTrip(t *testing.T) {
	state := &memoryStateStore{values: make(map[string][]byte)}
	store, err := newStateStore(state)
	if err != nil {
		t.Fatal(err)
	}
	record := signedExtrinsicRecord{
		TxHash:        "0x1234",
		VaultAddress:  "vault",
		Nonce:         7,
		RecheckHeight: 100,
	}
	err = store.PutExtrinsic(record)
	if err != nil {
		t.Fatal(err)
	}
	_, found := state.values[signedExtrinsicPrefix+record.TxHash]
	if !found {
		t.Fatal("extrinsic was not stored under its record-family prefix")
	}
	got, found, err := store.GetExtrinsic(record.TxHash)
	if err != nil || !found || got != record {
		t.Fatalf("unexpected record: got=%+v found=%v err=%v", got, found, err)
	}
	err = store.DeleteExtrinsic(record.TxHash)
	if err != nil {
		t.Fatal(err)
	}
	_, found, err = store.GetExtrinsic(record.TxHash)
	if err != nil || found {
		t.Fatalf("record was not deleted: found=%v err=%v", found, err)
	}
}

func TestStateStoreRejectsInvalidAndCorruptExtrinsics(t *testing.T) {
	state := &memoryStateStore{values: make(map[string][]byte)}
	store, err := newStateStore(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []signedExtrinsicRecord{
		{VaultAddress: "vault"},
		{TxHash: "hash"},
		{TxHash: "hash", VaultAddress: "vault", RecheckHeight: -1},
		{TxHash: "hash", VaultAddress: "vault", EraBirthHeight: -1},
		{TxHash: "hash", VaultAddress: "vault", EraBirthHeight: 2, RecheckHeight: 1},
	} {
		err = store.PutExtrinsic(record)
		if err == nil {
			t.Fatalf("expected invalid record to fail: %+v", record)
		}
	}
	key := signedExtrinsicPrefix + "hash"
	state.values[key] = []byte("not-json")
	_, _, err = store.GetExtrinsic("hash")
	if err == nil {
		t.Fatal("expected corrupt record to fail")
	}
	state.values[key] = []byte(`{"transaction_hash":"other","vault_address":"vault","nonce":1,"recheck_height":1}`)
	_, _, err = store.GetExtrinsic("hash")
	if err == nil {
		t.Fatal("expected mismatched record hash to fail")
	}
}

func TestStateStorePropagatesExtrinsicErrors(t *testing.T) {
	stateErr := errors.New("leveldb unavailable")
	state := &memoryStateStore{
		values:    make(map[string][]byte),
		getErr:    stateErr,
		putErr:    stateErr,
		deleteErr: stateErr,
	}
	store, err := newStateStore(state)
	if err != nil {
		t.Fatal(err)
	}
	record := signedExtrinsicRecord{TxHash: "hash", VaultAddress: "vault"}
	err = store.PutExtrinsic(record)
	if !errors.Is(err, stateErr) {
		t.Fatalf("put did not propagate state error: %v", err)
	}
	_, _, err = store.GetExtrinsic("hash")
	if !errors.Is(err, stateErr) {
		t.Fatalf("get did not propagate state error: %v", err)
	}
	err = store.DeleteExtrinsic("hash")
	if !errors.Is(err, stateErr) {
		t.Fatalf("delete did not propagate state error: %v", err)
	}
}
