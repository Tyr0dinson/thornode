package generic

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// testRetention prevents time-based pruning in count-limit tests.
const testRetention = time.Hour

type blockReferenceMemoryState struct {
	mu        sync.Mutex
	values    map[string][]byte
	puts      map[string]int
	putErr    error
	deleteErr error
	iterErr   error
}

func (s *blockReferenceMemoryState) Get(key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.values[key]
	return append([]byte(nil), value...), found, nil
}

func (s *blockReferenceMemoryState) Put(key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putErr != nil {
		return s.putErr
	}
	if s.values == nil {
		s.values = make(map[string][]byte)
	}
	if s.puts == nil {
		s.puts = make(map[string]int)
	}
	s.values[key] = append([]byte(nil), value...)
	s.puts[key]++
	return nil
}

func (s *blockReferenceMemoryState) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.values, key)
	return nil
}

// Iterate invokes callbacks outside the lock.
func (s *blockReferenceMemoryState) Iterate(
	prefix string,
	fn func(key string, value []byte) error,
) error {
	s.mu.Lock()
	if s.iterErr != nil {
		s.mu.Unlock()
		return s.iterErr
	}
	keys := make([]string, 0, len(s.values))
	for key := range s.values {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	values := make([][]byte, len(keys))
	for i, key := range keys {
		values[i] = append([]byte(nil), s.values[key]...)
	}
	s.mu.Unlock()

	for i, key := range keys {
		if err := fn(key, values[i]); err != nil {
			return err
		}
	}
	return nil
}

func newBlockReferenceStore(t *testing.T, maxReferences int) (*BlockReferenceStore, *blockReferenceMemoryState) {
	t.Helper()
	state := &blockReferenceMemoryState{values: make(map[string][]byte)}
	store, err := NewBlockReferenceStore(state, maxReferences, testRetention)
	if err != nil {
		t.Fatal(err)
	}
	return store, state
}

func TestBlockReferenceStoreResolvesOutOfOrderReferences(t *testing.T) {
	store, _ := newBlockReferenceStore(t, 10)
	for _, reference := range []BlockReference{
		{Height: 12, Hash: "12", Timestamp: 1_200},
		{Height: 10, Hash: "10", Timestamp: 1_000},
		{Height: 11, Hash: "11", Timestamp: 1_100},
	} {
		if err := store.Set(reference); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.Resolve(1_100)
	if err != nil {
		t.Fatal(err)
	}
	if got.Height != 11 || got.Hash != "11" {
		t.Fatalf("unexpected reference: %+v", got)
	}
}

func TestBlockReferenceStoreRequiresConsecutiveUpperBoundary(t *testing.T) {
	store, _ := newBlockReferenceStore(t, 10)
	for _, reference := range []BlockReference{
		{Height: 10, Hash: "10", Timestamp: 1_000},
		{Height: 12, Hash: "12", Timestamp: 1_200},
	} {
		if err := store.Set(reference); err != nil {
			t.Fatal(err)
		}
	}
	for _, timestamp := range []int64{999, 1_050, 1_200} {
		_, err := store.Resolve(timestamp)
		if !errors.Is(err, ErrBlockReferenceUnavailable) {
			t.Fatalf("timestamp %d: expected unavailable, got %v", timestamp, err)
		}
	}
}

func TestBlockReferenceStoreSelectsHighestEqualTimestamp(t *testing.T) {
	store, _ := newBlockReferenceStore(t, 10)
	for _, reference := range []BlockReference{
		{Height: 10, Hash: "10", Timestamp: 1_000},
		{Height: 11, Hash: "11", Timestamp: 1_000},
		{Height: 12, Hash: "12", Timestamp: 1_100},
	} {
		if err := store.Set(reference); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Resolve(1_000)
	if err != nil {
		t.Fatal(err)
	}
	if got.Height != 11 {
		t.Fatalf("got height %d, want 11", got.Height)
	}
}

func TestBlockReferenceStoreRejectsConflictsAndDecreasingTimestamps(t *testing.T) {
	store, _ := newBlockReferenceStore(t, 10)
	reference := BlockReference{Height: 10, Hash: "10", Timestamp: 1_000}
	if err := store.Set(reference); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(reference); err != nil {
		t.Fatalf("identical reference must be idempotent: %v", err)
	}
	if err := store.Set(BlockReference{Height: 10, Hash: "fork", Timestamp: 1_000}); err == nil {
		t.Fatal("expected conflicting reference error")
	}
	if err := store.Set(BlockReference{Height: 11, Hash: "11", Timestamp: 999}); err == nil {
		t.Fatal("expected decreasing timestamp error")
	}
}

func TestBlockReferenceStorePersistsAndPrunes(t *testing.T) {
	store, state := newBlockReferenceStore(t, 3)
	for height := int64(1); height <= 5; height++ {
		if err := store.Set(BlockReference{
			Height: height, Hash: string(rune('0' + height)), Timestamp: height * 100,
		}); err != nil {
			t.Fatal(err)
		}
	}
	wantKeys := []string{
		blockReferenceKey(3), blockReferenceKey(4), blockReferenceKey(5),
	}
	if len(state.values) != len(wantKeys) {
		t.Fatalf("expected %d stored keys, got %d: %v", len(wantKeys), len(state.values), state.values)
	}
	for _, key := range wantKeys {
		if _, found := state.values[key]; !found {
			t.Fatalf("missing stored key %s: %v", key, state.values)
		}
	}

	restored, err := NewBlockReferenceStore(state, 3, testRetention)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restored.Resolve(250); !errors.Is(err, ErrBlockReferenceUnavailable) {
		t.Fatalf("expected pruned reference to be unavailable, got %v", err)
	}
	got, err := restored.Resolve(400)
	if err != nil || got.Height != 4 {
		t.Fatalf("unexpected restored reference: %+v err=%v", got, err)
	}
}

func TestBlockReferenceStoreDoesNotPublishFailedPersistence(t *testing.T) {
	store, state := newBlockReferenceStore(t, 3)
	state.putErr = errors.New("disk unavailable")
	if err := store.Set(BlockReference{Height: 1, Hash: "1", Timestamp: 100}); err == nil {
		t.Fatal("expected persistence error")
	}
	state.putErr = nil
	if err := store.Set(BlockReference{Height: 2, Hash: "2", Timestamp: 200}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(100); !errors.Is(err, ErrBlockReferenceUnavailable) {
		t.Fatalf("failed write was published in memory: %v", err)
	}
}

func TestBlockReferenceStoreConcurrentAdds(t *testing.T) {
	store, _ := newBlockReferenceStore(t, 200)
	errs := make(chan error, 100)
	var wg sync.WaitGroup
	for height := int64(1); height <= 100; height++ {
		wg.Add(1)
		go func(height int64) {
			defer wg.Done()
			errs <- store.Set(BlockReference{
				Height: height, Hash: string(rune(height)), Timestamp: height * 100,
			})
		}(height)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Resolve(9_950)
	if err != nil || got.Height != 99 {
		t.Fatalf("unexpected concurrent result: %+v err=%v", got, err)
	}
}

func TestBlockReferenceStoreValidation(t *testing.T) {
	state := &blockReferenceMemoryState{values: make(map[string][]byte)}
	if _, err := NewBlockReferenceStore(nil, 2, testRetention); err == nil {
		t.Fatal("expected nil state error")
	}
	if _, err := NewBlockReferenceStore(state, 1, testRetention); err == nil {
		t.Fatal("expected capacity error")
	}
	store, err := NewBlockReferenceStore(state, 2, testRetention)
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range []BlockReference{
		{Height: -1, Hash: "hash", Timestamp: 1},
		{Height: 1, Timestamp: 1},
		{Height: 1, Hash: "hash"},
	} {
		if err = store.Set(reference); err == nil {
			t.Fatalf("expected invalid reference error: %+v", reference)
		}
	}
}

func TestBlockReferenceValidation(t *testing.T) {
	valid := BlockReference{Height: 1, Hash: "hash", Timestamp: 1}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid reference failed validation: %v", err)
	}
	for _, reference := range []BlockReference{
		{Height: -1, Hash: "hash", Timestamp: 1},
		{Height: 1, Timestamp: 1},
		{Height: 1, Hash: "hash"},
	} {
		if err := reference.Validate(); err == nil {
			t.Fatalf("expected invalid reference error: %+v", reference)
		}
	}
}

func TestResolveWithinRetentionOnFastChain(t *testing.T) {
	const (
		blockInterval = 300 * time.Millisecond
		lookback      = time.Minute
		blocks        = 600
	)

	cases := []struct {
		name      string
		retention time.Duration
		resolves  bool
	}{
		{name: "retention covers lookback", retention: lookback * 2, resolves: true},
		{name: "retention shorter than lookback", retention: lookback / 2, resolves: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &blockReferenceMemoryState{values: make(map[string][]byte)}
			store, err := NewBlockReferenceStore(state, blocks, tc.retention)
			if err != nil {
				t.Fatal(err)
			}

			start := int64(1_700_000_000_000)
			step := blockInterval.Milliseconds()
			for i := int64(0); i < blocks; i++ {
				err = store.Set(BlockReference{
					Height:    i + 1,
					Hash:      fmt.Sprintf("hash-%d", i+1),
					Timestamp: start + i*step,
				})
				if err != nil {
					t.Fatalf("set height %d: %v", i+1, err)
				}
			}

			newest := start + (blocks-1)*step
			_, err = store.Resolve(newest - lookback.Milliseconds())
			switch {
			case tc.resolves && err != nil:
				t.Fatalf("expected the lookback anchor to resolve, got %v", err)
			case !tc.resolves && !errors.Is(err, ErrBlockReferenceUnavailable):
				t.Fatalf("expected %v, got %v", ErrBlockReferenceUnavailable, err)
			}
		})
	}
}

// seedBlockReferences writes persisted references directly.
func seedBlockReferences(t *testing.T, state *blockReferenceMemoryState, references ...BlockReference) {
	t.Helper()
	for _, reference := range references {
		value, err := json.Marshal(reference)
		if err != nil {
			t.Fatal(err)
		}
		if err = state.Put(blockReferenceKey(reference.Height), value); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBlockReferenceStoreWritesOnlyTheNewKey(t *testing.T) {
	store, state := newBlockReferenceStore(t, 100)
	const blocks = 20

	for height := int64(1); height <= blocks; height++ {
		if err := store.Set(BlockReference{
			Height: height, Hash: fmt.Sprintf("hash-%d", height), Timestamp: height * 100,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if len(state.puts) != blocks {
		t.Fatalf("expected %d keys written, got %d", blocks, len(state.puts))
	}
	for key, count := range state.puts {
		if count != 1 {
			t.Fatalf("key %s was written %d times, expected once", key, count)
		}
	}
}

func TestBlockReferenceStoreLoad(t *testing.T) {
	cases := []struct {
		name       string
		seed       []BlockReference
		corrupt    func(state *blockReferenceMemoryState)
		wantErr    bool
		resolve    int64
		wantHeight int64
	}{
		{
			name: "heights load in numeric order across digit widths",
			seed: []BlockReference{
				{Height: 9, Hash: "9", Timestamp: 900},
				{Height: 10, Hash: "10", Timestamp: 1_000},
				{Height: 100, Hash: "100", Timestamp: 1_100},
			},
			resolve:    950,
			wantHeight: 9,
		},
		{
			name: "height comes from the key, not from the value",
			seed: []BlockReference{
				{Height: 7, Hash: "7", Timestamp: 700},
				{Height: 8, Hash: "8", Timestamp: 800},
			},
			corrupt: func(state *blockReferenceMemoryState) {
				state.values[blockReferenceKey(8)] = []byte(`{"height":11,"hash":"8","timestamp":800}`)
			},
			resolve:    750,
			wantHeight: 7,
		},
		{
			name: "unreadable record is rejected",
			seed: []BlockReference{{Height: 1, Hash: "1", Timestamp: 100}},
			corrupt: func(state *blockReferenceMemoryState) {
				state.values[blockReferenceKey(1)] = []byte("not json")
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &blockReferenceMemoryState{values: make(map[string][]byte)}
			seedBlockReferences(t, state, tc.seed...)
			if tc.corrupt != nil {
				tc.corrupt(state)
			}

			store, err := NewBlockReferenceStore(state, 100, testRetention)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected construction to fail")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := store.Resolve(tc.resolve)
			if err != nil {
				t.Fatal(err)
			}
			if got.Height != tc.wantHeight {
				t.Fatalf("resolved height %d, want %d", got.Height, tc.wantHeight)
			}
		})
	}
}

func TestBlockReferenceStoreLoadAppliesRetention(t *testing.T) {
	state := &blockReferenceMemoryState{values: make(map[string][]byte)}
	const minute = int64(60_000)
	seedBlockReferences(t, state,
		BlockReference{Height: 1, Hash: "1", Timestamp: 1 * minute},
		BlockReference{Height: 2, Hash: "2", Timestamp: 2 * minute},
		BlockReference{Height: 3, Hash: "3", Timestamp: 9 * minute},
		BlockReference{Height: 4, Hash: "4", Timestamp: 10 * minute},
	)

	store, err := NewBlockReferenceStore(state, 100, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := state.values[blockReferenceKey(1)]; found {
		t.Fatal("stale reference was not dropped from state")
	}
	if _, err = store.Resolve(1 * minute); !errors.Is(err, ErrBlockReferenceUnavailable) {
		t.Fatalf("expected stale reference to be unavailable, got %v", err)
	}
	if got, err := store.Resolve(9 * minute); err != nil || got.Height != 3 {
		t.Fatalf("unexpected in-window reference: %+v err=%v", got, err)
	}
}

func TestBlockReferenceStoreToleratesPruneDeleteFailure(t *testing.T) {
	store, state := newBlockReferenceStore(t, 2)
	state.deleteErr = errors.New("delete unavailable")

	for height := int64(1); height <= 3; height++ {
		if err := store.Set(BlockReference{
			Height: height, Hash: fmt.Sprintf("hash-%d", height), Timestamp: height * 100,
		}); err != nil {
			t.Fatalf("set should survive a failed prune delete: %v", err)
		}
	}
	if _, found := state.values[blockReferenceKey(1)]; !found {
		t.Fatal("expected the undeletable reference to remain in state")
	}
	if _, err := store.Resolve(100); !errors.Is(err, ErrBlockReferenceUnavailable) {
		t.Fatalf("pruned reference should be gone from memory, got %v", err)
	}

	state.deleteErr = nil
	if _, err := NewBlockReferenceStore(state, 2, testRetention); err != nil {
		t.Fatal(err)
	}
	if _, found := state.values[blockReferenceKey(1)]; found {
		t.Fatal("reload did not drop the stale reference")
	}
}
