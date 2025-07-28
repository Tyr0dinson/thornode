package app

import (
	"io"
	"sync"

	storetypes "cosmossdk.io/store/types"
)

var _ storetypes.MultiStore = &QueryMultiStore{}

type QueryMultiStore struct {
	mu      sync.RWMutex
	root    storetypes.CommitMultiStore
	latest  storetypes.MultiStore
	applied int64
}

// NewQueryMultiStore creates a new QueryMultiStore.
func NewQueryMultiStore(root storetypes.CommitMultiStore) *QueryMultiStore {
	return &QueryMultiStore{
		root:    root,
		applied: 0,
	}
}

// SetLatestMultistore sets the multistore that queries are made against.
func (q *QueryMultiStore) SetLatestMultistore(latest storetypes.MultiStore) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.latest = latest
	q.applied++
}

// Commit resets the latest multistore.
func (q *QueryMultiStore) Commit() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.latest = nil
	q.applied = 0
}

// CacheMultiStore implements types.MultiStore.
func (q *QueryMultiStore) CacheMultiStore() storetypes.CacheMultiStore {
	q.mu.RLock()
	latest := q.latest
	q.mu.RUnlock()
	if latest != nil {
		return latest.CacheMultiStore()
	}
	return q.root.CacheMultiStore()
}

// CacheMultiStoreWithVersion implements types.MultiStore.
func (q *QueryMultiStore) CacheMultiStoreWithVersion(version int64) (storetypes.CacheMultiStore, error) {
	latestVersion := q.root.LatestVersion()
	if version == latestVersion {
		q.mu.RLock()
		latest := q.latest
		q.mu.RUnlock()

		if latest != nil {
			return latest.CacheMultiStore(), nil
		}
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
	q.mu.RLock()
	latest := q.latest
	q.mu.RUnlock()
	if latest != nil {
		return latest.GetKVStore(key)
	}
	return q.root.GetKVStore(key)
}

// GetStore implements types.MultiStore.
func (q *QueryMultiStore) GetStore(key storetypes.StoreKey) storetypes.Store {
	q.mu.RLock()
	latest := q.latest
	q.mu.RUnlock()
	if latest != nil {
		return latest.GetStore(key)
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
	q.mu.RLock()
	latest := q.latest
	q.mu.RUnlock()
	if latest != nil {
		latest.SetTracer(w)
	}
	q.root.SetTracer(w)
	return q
}

// SetTracingContext implements types.MultiStore.
func (q *QueryMultiStore) SetTracingContext(tc storetypes.TraceContext) storetypes.MultiStore {
	q.mu.RLock()
	latest := q.latest
	q.mu.RUnlock()
	if latest != nil {
		latest.SetTracingContext(tc)
	}
	q.root.SetTracingContext(tc)
	return q
}

// TracingEnabled implements types.MultiStore.
func (q *QueryMultiStore) TracingEnabled() bool {
	q.mu.RLock()
	latest := q.latest
	q.mu.RUnlock()
	if latest != nil {
		return latest.TracingEnabled()
	}
	return q.root.TracingEnabled()
}
