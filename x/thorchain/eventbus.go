package thorchain

import (
	"sync"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type EventBus struct {
	mu                   sync.RWMutex
	txAppliedSubscribers map[chan *types.TxAppliedNotification]struct{}
}

func NewEventBus() *EventBus {
	return &EventBus{
		txAppliedSubscribers: make(map[chan *types.TxAppliedNotification]struct{}),
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
