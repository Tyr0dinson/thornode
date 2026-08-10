package types

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestExpectedSecurityEventRegistryConsumesOnlyRegisteredCount(t *testing.T) {
	var registry ExpectedSecurityEventRegistry
	id := strings.Repeat("ab", 32)
	const message = "mempool swap rolled back"

	if err := registry.Register("  "+id+"  ", "  "+message+"  ", 2); err != nil {
		t.Fatalf("register expectation: %v", err)
	}
	if registry.Consume(id, message+" unexpectedly") {
		t.Fatal("mismatched message must not consume expectation")
	}
	if registry.Consume(strings.Repeat("cd", 32), message) {
		t.Fatal("mismatched id must not consume expectation")
	}
	if !registry.Consume(strings.ToUpper(id), message) {
		t.Fatal("first normalized event should be consumed")
	}

	state, ok := registry.State(id, message)
	if !ok {
		t.Fatal("expected registered state")
	}
	if state.Expected != 2 || state.Observed != 1 || state.Pending != 1 {
		t.Fatalf("unexpected state after first event: %+v", state)
	}

	if !registry.Consume(id, message) {
		t.Fatal("second event should be consumed")
	}
	if registry.Consume(id, message) {
		t.Fatal("event beyond registered count must be rejected")
	}

	state, ok = registry.State(id, message)
	if !ok || state.Observed != 2 || state.Pending != 0 {
		t.Fatalf("unexpected completed state: %+v, found=%v", state, ok)
	}
}

func TestExpectedSecurityEventRegistryRejectsInvalidAndDuplicateRegistrations(t *testing.T) {
	var registry ExpectedSecurityEventRegistry
	if err := registry.Register("", "message", 1); err == nil {
		t.Fatal("empty id should be rejected")
	}
	id := strings.Repeat("ab", 32)
	if err := registry.Register(id, "", 1); err == nil {
		t.Fatal("empty message should be rejected")
	}
	if err := registry.Register(id, "message", 0); err == nil {
		t.Fatal("non-positive count should be rejected")
	}
	if err := registry.Register("not-a-transaction-id", "message", 1); err == nil {
		t.Fatal("invalid id should be rejected")
	}
	if err := registry.Register(id, "message", 1); err != nil {
		t.Fatalf("register expectation: %v", err)
	}
	if err := registry.Register(" 0x"+strings.ToUpper(id)+" ", " message ", 1); err == nil {
		t.Fatal("normalized duplicate registration should be rejected")
	}
}

func TestExpectedSecurityEventRegistryIsConcurrentAndCountBounded(t *testing.T) {
	var registry ExpectedSecurityEventRegistry
	const expected = 64
	id := strings.Repeat("ab", 32)
	if err := registry.Register(id, "message", expected); err != nil {
		t.Fatalf("register expectation: %v", err)
	}

	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < expected*2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if registry.Consume(id, "message") {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := accepted.Load(); got != expected {
		t.Fatalf("expected exactly %d accepted events, got %d", expected, got)
	}
	state, ok := registry.State(id, "message")
	if !ok || state.Observed != expected || state.Pending != 0 {
		t.Fatalf("unexpected final state: %+v, found=%v", state, ok)
	}
}
