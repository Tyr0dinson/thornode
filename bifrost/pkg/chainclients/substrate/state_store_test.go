package substrate

import (
	"errors"
	"sort"
	"strings"
	"testing"
)

type memoryStateStore struct {
	values    map[string][]byte
	getErr    error
	putErr    error
	deleteErr error
	iterErr   error
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

func (s *memoryStateStore) Iterate(
	prefix string,
	fn func(key string, value []byte) error,
) error {
	if s.iterErr != nil {
		return s.iterErr
	}
	keys := make([]string, 0, len(s.values))
	for key := range s.values {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	for _, key := range keys {
		if err := fn(key, append([]byte(nil), s.values[key]...)); err != nil {
			return err
		}
	}
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
	record, found, err := store.GetExtrinsic("hash")
	if err != nil || !found {
		t.Fatalf("expected record to load, found %v: %v", found, err)
	}
	if record.TxHash != "hash" {
		t.Fatalf("loaded hash %q, want the key's hash", record.TxHash)
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

func TestStateStoreSweepExpired(t *testing.T) {
	records := []signedExtrinsicRecord{
		{TxHash: "0xold", VaultAddress: "vault", Nonce: 1, EraBirthHeight: 10, RecheckHeight: 20},
		{TxHash: "0xdue", VaultAddress: "vault", Nonce: 2, EraBirthHeight: 90, RecheckHeight: 100},
		// Records without a recheck height are not swept.
		{TxHash: "0xnoheight", VaultAddress: "vault", Nonce: 3},
	}

	cases := []struct {
		name        string
		cutoff      int64
		wantDropped int
		wantGone    []string
	}{
		{name: "nothing past the cutoff", cutoff: 5},
		{
			name:        "only records the chain has passed",
			cutoff:      50,
			wantDropped: 1,
			wantGone:    []string{"0xold"},
		},
		{
			name:        "records with no recheck height always survive",
			cutoff:      1_000,
			wantDropped: 2,
			wantGone:    []string{"0xold", "0xdue"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &memoryStateStore{values: make(map[string][]byte)}
			store, err := newStateStore(state)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range records {
				if err = store.PutExtrinsic(record); err != nil {
					t.Fatal(err)
				}
			}

			dropped, err := store.SweepExpired(tc.cutoff)
			if err != nil {
				t.Fatal(err)
			}
			if dropped != tc.wantDropped {
				t.Fatalf("dropped %d records, want %d", dropped, tc.wantDropped)
			}
			for _, txHash := range tc.wantGone {
				if _, found, err := store.GetExtrinsic(txHash); err != nil || found {
					t.Fatalf("record %s should have been swept: found=%v err=%v", txHash, found, err)
				}
			}
			if _, found, err := store.GetExtrinsic("0xnoheight"); err != nil || !found {
				t.Fatalf("record without a recheck height was swept: found=%v err=%v", found, err)
			}
		})
	}
}

func TestStateStoreSweepExpiredReportsUnreadableRecords(t *testing.T) {
	state := &memoryStateStore{values: make(map[string][]byte)}
	store, err := newStateStore(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PutExtrinsic(signedExtrinsicRecord{
		TxHash: "0xold", VaultAddress: "vault", Nonce: 1, RecheckHeight: 20,
	}); err != nil {
		t.Fatal(err)
	}
	state.values[signedExtrinsicPrefix+"0xcorrupt"] = []byte("not json")

	dropped, err := store.SweepExpired(50)
	if err == nil || !strings.Contains(err.Error(), signedExtrinsicPrefix+"0xcorrupt") {
		t.Fatalf("expected an error naming the corrupt record, got %v", err)
	}
	if dropped != 1 {
		t.Fatalf("dropped %d records, want 1", dropped)
	}
	if _, found := state.values[signedExtrinsicPrefix+"0xcorrupt"]; !found {
		t.Fatal("the unreadable record should have been left in place")
	}
}

func TestStateStoreSweepExpiredReportsInvalidRecords(t *testing.T) {
	state := &memoryStateStore{values: make(map[string][]byte)}
	store, err := newStateStore(state)
	if err != nil {
		t.Fatal(err)
	}
	state.values[signedExtrinsicPrefix+"0xinvalid"] = []byte(`{"vault_address":"","nonce":1,"recheck_height":20}`)

	dropped, err := store.SweepExpired(50)
	if err == nil || !strings.Contains(err.Error(), signedExtrinsicPrefix+"0xinvalid") {
		t.Fatalf("expected an error naming the invalid record, got %v", err)
	}
	if dropped != 0 {
		t.Fatalf("dropped %d records, want 0", dropped)
	}
	if _, found := state.values[signedExtrinsicPrefix+"0xinvalid"]; !found {
		t.Fatal("the invalid record should have been left in place")
	}
}

func TestStateStoreSweepExpiredPropagatesIterateError(t *testing.T) {
	state := &memoryStateStore{
		values:  make(map[string][]byte),
		iterErr: errors.New("state unavailable"),
	}
	store, err := newStateStore(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SweepExpired(50); err == nil {
		t.Fatal("expected the iterate error to surface")
	}
}
