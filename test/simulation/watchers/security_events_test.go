package watchers

import (
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestSecurityEventWatcherConsumesExactRegisteredEvent(t *testing.T) {
	config := &types.OpConfig{}
	id := strings.Repeat("ab", 32)
	const message = "mempool swap rolled back"
	if err := config.RegisterExpectedSecurityEvent(id, message, 1); err != nil {
		t.Fatalf("register expectation: %v", err)
	}

	event := map[string]string{
		"type": "security",
		"id":   strings.ToUpper(id),
		"msg":  message,
	}
	if err := checkSecurityEvent(config, 12, event); err != nil {
		t.Fatalf("registered event should be permitted: %v", err)
	}
	state, ok := config.ExpectedSecurityEventState(id, message)
	if !ok || state.Observed != 1 || state.Pending != 0 {
		t.Fatalf("unexpected observed state: %+v, found=%v", state, ok)
	}
}

func TestSecurityEventWatcherRejectsUnmatchedAndPreservesExpectation(t *testing.T) {
	config := &types.OpConfig{}
	id := strings.Repeat("ab", 32)
	const message = "mempool swap rolled back"
	if err := config.RegisterExpectedSecurityEvent(id, message, 1); err != nil {
		t.Fatalf("register expectation: %v", err)
	}

	event := map[string]string{
		"type": "security",
		"id":   id,
		"msg":  "different security event",
	}
	err := checkSecurityEvent(config, 13, event)
	if err == nil || !strings.Contains(err.Error(), "security event detected at height 13") {
		t.Fatalf("unmatched event should remain fatal, got %v", err)
	}
	state, ok := config.ExpectedSecurityEventState(id, message)
	if !ok || state.Observed != 0 || state.Pending != 1 {
		t.Fatalf("unmatched event changed expectation: %+v, found=%v", state, ok)
	}
}

func TestSecurityEventWatcherRejectsDuplicateAfterRegisteredCount(t *testing.T) {
	config := &types.OpConfig{}
	id := strings.Repeat("ab", 32)
	const message = "mempool swap rolled back"
	if err := config.RegisterExpectedSecurityEvent(id, message, 1); err != nil {
		t.Fatalf("register expectation: %v", err)
	}
	event := map[string]string{"type": "security", "id": id, "msg": message}
	if err := checkSecurityEvent(config, 14, event); err != nil {
		t.Fatalf("first event should be permitted: %v", err)
	}
	if err := checkSecurityEvent(config, 15, event); err == nil {
		t.Fatal("duplicate event should remain fatal")
	}
}
