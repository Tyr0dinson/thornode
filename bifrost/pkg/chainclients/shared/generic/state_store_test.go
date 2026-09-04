package generic

import (
	"bytes"
	"errors"
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
	storeA, err := newStateStore(db, common.DOTChain)
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := newStateStore(db, common.DOTChain)
	if err != nil {
		t.Fatal(err)
	}
	tao, err := newStateStore(db, common.TAOChain)
	if err != nil {
		t.Fatal(err)
	}

	err = storeA.Put("hash", []byte("dot"))
	if err != nil {
		t.Fatal(err)
	}
	value, found, err := storeB.Get("hash")
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
	err = storeB.Delete("hash")
	if err != nil {
		t.Fatal(err)
	}
	err = storeA.Delete("hash")
	if err != nil {
		t.Fatalf("delete must be idempotent: %v", err)
	}
	_, found, err = storeA.Get("hash")
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

// seedIterateFixture creates records for prefix and chain-isolation tests.
func seedIterateFixture(t *testing.T, db *leveldb.DB) (dot, tao StateStore) {
	t.Helper()
	dot, err := newStateStore(db, common.DOTChain)
	if err != nil {
		t.Fatal(err)
	}
	tao, err = newStateStore(db, common.TAOChain)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"scanner/block-references/00000000000000000009": "nine",
		"scanner/block-references/00000000000000000010": "ten",
		"scanner/block-references/00000000000000000100": "hundred",
		"signed-extrinsic/abc":                          "record",
	} {
		if err = dot.Put(key, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tao.Put("scanner/block-references/00000000000000000001", []byte("tao-one")); err != nil {
		t.Fatal(err)
	}
	return dot, tao
}

func TestStateStoreIterate(t *testing.T) {
	cases := []struct {
		name     string
		useTAO   bool
		prefix   string
		wantKeys []string
	}{
		{
			name:   "numeric order across digit widths",
			prefix: "scanner/block-references/",
			wantKeys: []string{
				"scanner/block-references/00000000000000000009",
				"scanner/block-references/00000000000000000010",
				"scanner/block-references/00000000000000000100",
			},
		},
		{
			name:     "other chains are not visible",
			useTAO:   true,
			prefix:   "scanner/block-references/",
			wantKeys: []string{"scanner/block-references/00000000000000000001"},
		},
		{
			name:   "empty prefix walks this chain only",
			prefix: "",
			wantKeys: []string{
				"scanner/block-references/00000000000000000009",
				"scanner/block-references/00000000000000000010",
				"scanner/block-references/00000000000000000100",
				"signed-extrinsic/abc",
			},
		},
		{
			name:     "unmatched prefix visits nothing",
			prefix:   "no-such-prefix/",
			wantKeys: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dot, tao := seedIterateFixture(t, newStateStoreTestDB(t))
			store := dot
			if tc.useTAO {
				store = tao
			}

			var got []string
			err := store.Iterate(tc.prefix, func(key string, _ []byte) error {
				got = append(got, key)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.wantKeys) {
				t.Fatalf("got %v, want %v", got, tc.wantKeys)
			}
			for i, key := range tc.wantKeys {
				if got[i] != key {
					t.Fatalf("key %d is %q, want %q (full: %v)", i, got[i], key, got)
				}
			}
		})
	}
}

func TestStateStoreIterateControlFlow(t *testing.T) {
	sentinel := errors.New("stop iteration")

	t.Run("callback error propagates unwrapped", func(t *testing.T) {
		dot, _ := seedIterateFixture(t, newStateStoreTestDB(t))
		calls := 0
		err := dot.Iterate("scanner/", func(string, []byte) error {
			calls++
			if calls == 2 {
				return sentinel
			}
			return nil
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("got %v, want %v", err, sentinel)
		}
		if calls != 2 {
			t.Fatalf("callback ran %d times, want 2", calls)
		}
	})

	t.Run("values outlive the walk", func(t *testing.T) {
		dot, _ := seedIterateFixture(t, newStateStoreTestDB(t))
		var retained []byte
		if err := dot.Iterate("scanner/block-references/", func(_ string, value []byte) error {
			if retained == nil {
				retained = value
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(retained, []byte("nine")) {
			t.Fatalf("retained value is %q, want %q", retained, "nine")
		}
	})

	t.Run("nil callback is rejected", func(t *testing.T) {
		dot, _ := seedIterateFixture(t, newStateStoreTestDB(t))
		if err := dot.Iterate("scanner/", nil); err == nil {
			t.Fatal("expected a nil callback to be rejected")
		}
	})

	t.Run("database errors surface", func(t *testing.T) {
		db := newStateStoreTestDB(t)
		dot, _ := seedIterateFixture(t, db)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := dot.Iterate("scanner/", func(string, []byte) error {
			return nil
		}); err == nil {
			t.Fatal("expected an error from a closed database")
		}
	})
}
