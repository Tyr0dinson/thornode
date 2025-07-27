package app

import (
	"io"

	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/types"
)

var _ storetypes.MultiStore = &QueryMultiStore{}

type QueryMultiStore struct {
	root    storetypes.CommitMultiStore
	latest  storetypes.MultiStore
	applied []types.Tx
}

// NewQueryMultiStore creates a new QueryMultiStore.
func NewQueryMultiStore(root storetypes.CommitMultiStore) *QueryMultiStore {
	return &QueryMultiStore{
		root: root,
	}
}

// SetLatestMultistore sets the multistore that queries are made against.
func (q *QueryMultiStore) SetLatestMultistore(latest storetypes.MultiStore) {
	q.latest = latest
}

// Commit resets the latest multistore and applied txs.
func (q *QueryMultiStore) Commit() {
	q.latest = nil
	q.applied = nil
}

// CacheMultiStore implements types.MultiStore.
func (q *QueryMultiStore) CacheMultiStore() storetypes.CacheMultiStore {
	if q.latest != nil {
		return q.latest.CacheMultiStore()
	}
	return q.root.CacheMultiStore()
}

// CacheMultiStoreWithVersion implements types.MultiStore.
func (q *QueryMultiStore) CacheMultiStoreWithVersion(version int64) (storetypes.CacheMultiStore, error) {
	latestVersion := q.root.LatestVersion()
	if version == latestVersion && q.latest != nil {
		return q.latest.CacheMultiStore(), nil
	}
	return q.root.CacheMultiStoreWithVersion(version)
}

// CacheWrap implements types.MultiStore.
func (q *QueryMultiStore) CacheWrap() storetypes.CacheWrap { return q.CacheMultiStore() }

// CacheWrapWithTrace implements types.MultiStore.
func (q *QueryMultiStore) CacheWrapWithTrace(w io.Writer, tc storetypes.TraceContext) storetypes.CacheWrap {
	return q.CacheWrap()
}

// GetKVStore implements types.MultiStore.
func (q *QueryMultiStore) GetKVStore(key storetypes.StoreKey) storetypes.KVStore {
	if q.latest != nil {
		return q.latest.GetKVStore(key)
	}
	return q.root.GetKVStore(key)
}

// GetStore implements types.MultiStore.
func (q *QueryMultiStore) GetStore(key storetypes.StoreKey) storetypes.Store {
	if q.latest != nil {
		return q.latest.GetStore(key)
	}
	return q.root.GetStore(key)
}

// GetStoreType implements types.MultiStore.
func (q *QueryMultiStore) GetStoreType() storetypes.StoreType {
	return storetypes.StoreTypeMulti
}

// LatestVersion implements types.MultiStore.
func (q *QueryMultiStore) LatestVersion() int64 {
	return q.root.LatestVersion()
}

// SetTracer implements types.MultiStore.
func (q *QueryMultiStore) SetTracer(w io.Writer) storetypes.MultiStore {
	if q.latest != nil {
		q.latest.SetTracer(w)
	}
	q.root.SetTracer(w)
	return q
}

// SetTracingContext implements types.MultiStore.
func (q *QueryMultiStore) SetTracingContext(tc storetypes.TraceContext) storetypes.MultiStore {
	if q.latest != nil {
		q.latest.SetTracingContext(tc)
	}
	q.root.SetTracingContext(tc)
	return q
}

// TracingEnabled implements types.MultiStore.
func (q *QueryMultiStore) TracingEnabled() bool {
	if q.latest != nil {
		return q.latest.TracingEnabled()
	}
	return q.root.TracingEnabled()
}
