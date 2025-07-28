package thorchain

import (
	"sync"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type EventBus struct {
	mu                        sync.RWMutex
	txAppliedSubscribers      map[chan *types.TxAppliedNotification]struct{}
	blockFinalizedSubscribers map[chan *types.BlockFinalizedNotification]struct{}
}

func NewEventBus() *EventBus {
	return &EventBus{
		txAppliedSubscribers:      make(map[chan *types.TxAppliedNotification]struct{}),
		blockFinalizedSubscribers: make(map[chan *types.BlockFinalizedNotification]struct{}),
	}
}

func (m *EventBus) UnsubscribeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// analyze-ignore(map-iteration)
	for ch := range m.txAppliedSubscribers {
		close(ch)
	}

	clear(m.txAppliedSubscribers)
}

func (m *EventBus) SubscribeTxApplied() chan *types.TxAppliedNotification {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch := make(chan *types.TxAppliedNotification, 100)
	m.txAppliedSubscribers[ch] = struct{}{}
	return ch
}

func (m *EventBus) UnsubscribeTxApplied(ch chan *types.TxAppliedNotification) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.txAppliedSubscribers, ch)
	close(ch)
}

func (m *EventBus) BroadcastTxApplied(notification *types.TxAppliedNotification) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// analyze-ignore(map-iteration)
	for ch := range m.txAppliedSubscribers {
		select {
		case ch <- notification:
		default:
		}
	}
}

func (m *EventBus) SubscribeBlockFinalized() chan *types.BlockFinalizedNotification {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch := make(chan *types.BlockFinalizedNotification, 100)
	m.blockFinalizedSubscribers[ch] = struct{}{}
	return ch
}

func (m *EventBus) UnsubscribeBlockFinalized(ch chan *types.BlockFinalizedNotification) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.blockFinalizedSubscribers, ch)
	close(ch)
}

func (m *EventBus) BroadcastBlockFinalized(notification *types.BlockFinalizedNotification) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// analyze-ignore(map-iteration)
	for ch := range m.blockFinalizedSubscribers {
		select {
		case ch <- notification:
		default:
		}
	}
}
