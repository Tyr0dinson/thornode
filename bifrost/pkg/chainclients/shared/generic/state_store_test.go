package generic

import (
	"bytes"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"gitlab.com/thorchain/thornode/v3/common"
)

func newStateStoreTestDB(t *testing.T) *leveldb.DB {
	t.Helper()
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestStateStoreIsolationAndLifecycle(t *testing.T) {
	db := newStateStoreTestDB(t)
	dotA, err := newStateStore(db, common.DOTChain)
	if err != nil {
		t.Fatal(err)
	}
	dotB, err := newStateStore(db, common.DOTChain)
	if err != nil {
		t.Fatal(err)
	}
	tao, err := newStateStore(db, common.TAOChain)
	if err != nil {
		t.Fatal(err)
	}

	err = dotA.Put("hash", []byte("dot"))
	if err != nil {
		t.Fatal(err)
	}
	value, found, err := dotB.Get("hash")
	if err != nil || !found || !bytes.Equal(value, []byte("dot")) {
		t.Fatalf("same-chain store does not share state: value=%q found=%v err=%v", value, found, err)
	}
	_, found, err = tao.Get("hash")
	if err != nil || found {
		t.Fatalf("chain isolation failed: found=%v err=%v", found, err)
	}
	raw, err := db.Get([]byte("generic-state/DOT/hash"), nil)
	if err != nil || !bytes.Equal(raw, []byte("dot")) {
		t.Fatalf("unexpected physical key: value=%q err=%v", raw, err)
	}
	err = dotB.Delete("hash")
	if err != nil {
		t.Fatal(err)
	}
	err = dotA.Delete("hash")
	if err != nil {
		t.Fatalf("delete must be idempotent: %v", err)
	}
	_, found, err = dotA.Get("hash")
	if err != nil || found {
		t.Fatalf("deleted key remains: found=%v err=%v", found, err)
	}
}

func TestStateStoreValidationAndDBErrors(t *testing.T) {
	db := newStateStoreTestDB(t)
	store, err := newStateStore(db, common.DOTChain)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Put("", []byte("value"))
	if err == nil {
		t.Fatal("expected empty key to be rejected")
	}
	_, _, err = store.Get("")
	if err == nil {
		t.Fatal("expected empty key to be rejected")
	}

	err = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	err = store.Put("key", []byte("value"))
	if err == nil {
		t.Fatal("expected closed database error")
	}
	_, _, err = store.Get("key")
	if err == nil {
		t.Fatal("expected closed database error")
	}
	err = store.Delete("key")
	if err == nil {
		t.Fatal("expected closed database error")
	}
}

func TestStateStoreValidation(t *testing.T) {
	_, err := newStateStore(nil, common.DOTChain)
	if err == nil {
		t.Fatal("expected nil database error")
	}
	db := newStateStoreTestDB(t)
	_, err = newStateStore(db, common.EmptyChain)
	if err == nil {
		t.Fatal("expected empty chain error")
	}
}
