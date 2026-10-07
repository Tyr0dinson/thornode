package keeperv1

import (
	"bytes"
	"sync"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// nodeAccountMemoMaxEntries bounds the memo (mainnet has ~200 node accounts).
// An entry holds the record's stored bytes plus its decoded fields, ~10 KiB
// for a node in ~100 vaults, so the memo stays under ~20 MiB.
const nodeAccountMemoMaxEntries = 2048

type nodeAccountMemoEntry struct {
	raw []byte // stored bytes na was decoded from; never written
	na  NodeAccount
}

// nodeAccountMemo maps a node account store key to the last bytes decoded for
// it and their decoding. It is a pure cache: an entry is used only when the
// bytes read from the store are equal to raw, and decoding is a function of
// the bytes alone, so the result is the same whichever store, height or
// context the bytes came from. Store access is unchanged, since the bytes are
// still read to compare them.
var nodeAccountMemo = struct {
	sync.RWMutex
	m map[string]*nodeAccountMemoEntry
}{m: make(map[string]*nodeAccountMemoEntry)}

// memoDecodeNodeAccount sets *na to the decoding of bz, the value stored under
// key, and returns true. A copy of the memoized value is returned, which the
// caller may modify freely. It returns false, leaving *na untouched, if bz
// does not decode; the caller then decodes bz itself to get the error.
func (k KVStore) memoDecodeNodeAccount(key, bz []byte, na *NodeAccount) bool {
	nodeAccountMemo.RLock()
	e := nodeAccountMemo.m[string(key)]
	nodeAccountMemo.RUnlock()
	if e != nil && bytes.Equal(e.raw, bz) {
		*na = keeper.CloneNodeAccount(e.na)
		return true
	}

	e = &nodeAccountMemoEntry{raw: bytes.Clone(bz)}
	if err := k.decodeNodeAccount(e.raw, &e.na, true); err != nil {
		return false
	}
	nodeAccountMemo.Lock()
	if _, ok := nodeAccountMemo.m[string(key)]; !ok && len(nodeAccountMemo.m) >= nodeAccountMemoMaxEntries {
		clear(nodeAccountMemo.m)
	}
	nodeAccountMemo.m[string(key)] = e
	nodeAccountMemo.Unlock()
	*na = keeper.CloneNodeAccount(e.na)
	return true
}
