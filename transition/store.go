package transition

import (
	"fmt"
	"sort"
	"strings"

	storetypes "github.com/cosmos/cosmos-sdk/store/types"
)

// NewKVStoreKeys returns a map of new  pointers to KVStoreKey's.
// The function will panic if there is a potential conflict in names (see `assertNoPrefix`
// function for more details).
func NewKVStoreKeys(names ...string) map[string]*storetypes.KVStoreKey {
	assertNoCommonPrefix(names)
	keys := make(map[string]*storetypes.KVStoreKey, len(names))
	for _, n := range names {
		keys[n] = storetypes.NewKVStoreKey(n)
	}

	return keys
}

// NewTransientStoreKeys constructs a new map of TransientStoreKey's
// Must return pointers according to the ocap principle
// The function will panic if there is a potential conflict in names (see `assertNoPrefix`
// function for more details).
func NewTransientStoreKeys(names ...string) map[string]*storetypes.TransientStoreKey {
	assertNoCommonPrefix(names)
	keys := make(map[string]*storetypes.TransientStoreKey)
	for _, n := range names {
		keys[n] = storetypes.NewTransientStoreKey(n)
	}

	return keys
}

// NewMemoryStoreKeys constructs a new map matching store key names to their
// respective MemoryStoreKey references.
// The function will panic if there is a potential conflict in names (see `assertNoPrefix`
// function for more details).
func NewMemoryStoreKeys(names ...string) map[string]*storetypes.MemoryStoreKey {
	assertNoCommonPrefix(names)
	keys := make(map[string]*storetypes.MemoryStoreKey)
	for _, n := range names {
		keys[n] = storetypes.NewMemoryStoreKey(n)
	}

	return keys
}

// assertNoCommonPrefix will panic if there are two keys: k1 and k2 in keys, such that
// k1 is a prefix of k2
func assertNoCommonPrefix(keys []string) {
	sorted := make([]string, len(keys))
	copy(sorted, keys)
	sort.Strings(sorted)
	for i := 1; i < len(sorted); i++ {
		if strings.HasPrefix(sorted[i], sorted[i-1]) {
			panic(fmt.Sprint("Potential key collision between KVStores:", sorted[i], " - ", sorted[i-1]))
		}
	}
}
