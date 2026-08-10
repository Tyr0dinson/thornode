package types

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"gitlab.com/thorchain/thornode/v3/common"
)

// ExpectedSecurityEventState is an immutable view of a registered security-event
// expectation. Actors can use it to wait until the watcher has observed all of the
// events they registered.
type ExpectedSecurityEventState struct {
	ID       string
	Message  string
	Expected int
	Observed int
	Pending  int
}

type expectedSecurityEventKey struct {
	id      string
	message string
}

type expectedSecurityEventCount struct {
	expected int
	observed int
}

// ExpectedSecurityEventRegistry tracks the exact security events that simulation
// actors intend to trigger. Its zero value is ready for use.
type ExpectedSecurityEventRegistry struct {
	mu     sync.RWMutex
	events map[expectedSecurityEventKey]expectedSecurityEventCount
}

// Register adds one exact expected security event. A count greater than one must
// be supplied up front; registering the same key twice is rejected so an actor
// cannot accidentally broaden an existing allowance.
func (r *ExpectedSecurityEventRegistry) Register(id, message string, count int) error {
	if r == nil {
		return fmt.Errorf("expected security event registry is nil")
	}
	if count <= 0 {
		return fmt.Errorf("expected security event count must be positive: %d", count)
	}

	key, err := newExpectedSecurityEventKey(id, message)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.events == nil {
		r.events = make(map[expectedSecurityEventKey]expectedSecurityEventCount)
	}
	if _, ok := r.events[key]; ok {
		return fmt.Errorf("security event expectation already registered for id %q and message %q", key.id, key.message)
	}
	r.events[key] = expectedSecurityEventCount{expected: count}
	return nil
}

// Consume records one matching event. It returns false for an unregistered event
// and after the registered count has already been consumed, keeping duplicate
// security events fatal in the watcher.
func (r *ExpectedSecurityEventRegistry) Consume(id, message string) bool {
	if r == nil {
		return false
	}
	key, err := newExpectedSecurityEventKey(id, message)
	if err != nil {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	count, ok := r.events[key]
	if !ok || count.observed >= count.expected {
		return false
	}
	count.observed++
	r.events[key] = count
	return true
}

// State returns the observed and pending counts for one exact expectation.
func (r *ExpectedSecurityEventRegistry) State(id, message string) (ExpectedSecurityEventState, bool) {
	if r == nil {
		return ExpectedSecurityEventState{}, false
	}
	key, err := newExpectedSecurityEventKey(id, message)
	if err != nil {
		return ExpectedSecurityEventState{}, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	count, ok := r.events[key]
	if !ok {
		return ExpectedSecurityEventState{}, false
	}
	return expectedSecurityEventState(key, count), true
}

// States returns a deterministic snapshot of all registered expectations.
func (r *ExpectedSecurityEventRegistry) States() []ExpectedSecurityEventState {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	states := make([]ExpectedSecurityEventState, 0, len(r.events))
	for key, count := range r.events {
		states = append(states, expectedSecurityEventState(key, count))
	}
	sort.Slice(states, func(i, j int) bool {
		if states[i].ID == states[j].ID {
			return states[i].Message < states[j].Message
		}
		return states[i].ID < states[j].ID
	})
	return states
}

// RegisterExpectedSecurityEvent registers an expectation on the shared simulation
// configuration. It is safe to call on a nil config and returns an error instead of
// panicking.
func (c *OpConfig) RegisterExpectedSecurityEvent(id, message string, count int) error {
	if c == nil {
		return fmt.Errorf("operation config is nil")
	}
	return c.ExpectedSecurityEvents.Register(id, message, count)
}

// ConsumeExpectedSecurityEvent consumes one expectation from the shared simulation
// configuration. A nil config never permits a security event.
func (c *OpConfig) ConsumeExpectedSecurityEvent(id, message string) bool {
	return c != nil && c.ExpectedSecurityEvents.Consume(id, message)
}

// ExpectedSecurityEventState returns one expectation's current state.
func (c *OpConfig) ExpectedSecurityEventState(id, message string) (ExpectedSecurityEventState, bool) {
	if c == nil {
		return ExpectedSecurityEventState{}, false
	}
	return c.ExpectedSecurityEvents.State(id, message)
}

// ExpectedSecurityEventStates returns a deterministic snapshot for actors that need
// to ensure all registered events have been observed.
func (c *OpConfig) ExpectedSecurityEventStates() []ExpectedSecurityEventState {
	if c == nil {
		return nil
	}
	return c.ExpectedSecurityEvents.States()
}

func newExpectedSecurityEventKey(id, message string) (expectedSecurityEventKey, error) {
	var err error
	id, err = normalizeSecurityEventID(id)
	if err != nil {
		return expectedSecurityEventKey{}, err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return expectedSecurityEventKey{}, fmt.Errorf("expected security event message is empty")
	}
	return expectedSecurityEventKey{id: id, message: message}, nil
}

func normalizeSecurityEventID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("expected security event id is empty")
	}
	txID, err := common.NewTxID(id)
	if err != nil {
		return "", fmt.Errorf("invalid expected security event id %q: %w", id, err)
	}
	normalized := txID.String()
	if len(normalized) == 66 && strings.EqualFold(normalized[:2], "0x") {
		normalized = normalized[2:]
	}
	// SOL transaction IDs are 87 or 88 character base58 strings and are
	// case-sensitive. Base58 excludes '-', so an indexed Cosmos transaction ID of
	// the same length remains distinguishable and is normalized to uppercase.
	if (len(normalized) == 87 || len(normalized) == 88) && !strings.Contains(normalized, "-") {
		return normalized, nil
	}
	return strings.ToUpper(normalized), nil
}

func expectedSecurityEventState(key expectedSecurityEventKey, count expectedSecurityEventCount) ExpectedSecurityEventState {
	return ExpectedSecurityEventState{
		ID:       key.id,
		Message:  key.message,
		Expected: count.expected,
		Observed: count.observed,
		Pending:  count.expected - count.observed,
	}
}
