package generic

import (
	"errors"
	"sync"
	"testing"
)

type blockReferenceMemoryState struct {
	mu     sync.Mutex
	values map[string][]byte
	putErr error
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
	s.values[key] = append([]byte(nil), value...)
	return nil
}

func (s *blockReferenceMemoryState) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

func newBlockReferenceStore(t *testing.T, maxReferences int) (*BlockReferenceStore, *blockReferenceMemoryState) {
	t.Helper()
	state := &blockReferenceMemoryState{values: make(map[string][]byte)}
	store, err := NewBlockReferenceStore(state, maxReferences)
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

func TestBlockReferenceStorePersistsPrunesAndRewinds(t *testing.T) {
	store, state := newBlockReferenceStore(t, 3)
	for height := int64(1); height <= 5; height++ {
		if err := store.Set(BlockReference{
			Height: height, Hash: string(rune('0' + height)), Timestamp: height * 100,
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, found, err := state.Get("scanner/block-references")
	if err != nil || !found {
		t.Fatalf("references were not stored under the compatible key: found=%v err=%v", found, err)
	}

	restored, err := NewBlockReferenceStore(state, 3)
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
	if err = restored.Rewind(4); err != nil {
		t.Fatal(err)
	}
	if _, err = restored.Resolve(400); !errors.Is(err, ErrBlockReferenceUnavailable) {
		t.Fatalf("expected upper boundary to be removed, got %v", err)
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
	if _, err := NewBlockReferenceStore(nil, 2); err == nil {
		t.Fatal("expected nil state error")
	}
	if _, err := NewBlockReferenceStore(state, 1); err == nil {
		t.Fatal("expected capacity error")
	}
	store, err := NewBlockReferenceStore(state, 2)
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
