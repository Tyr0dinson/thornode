package ton

import (
	"sync"
	"time"
)

type State struct {
	RetryHeight int64
	Confirmed   bool
	DiscardAt   time.Time
	TxId        string
}

type RetryHandler struct {
	messages map[string]State
	mutex    sync.RWMutex
}

func NewRetryHandler() *RetryHandler {
	return &RetryHandler{
		messages: make(map[string]State),
		mutex:    sync.RWMutex{},
	}
}

func (h *RetryHandler) Add(hash string, height int64, timestamp time.Time) {
	h.mutex.Lock()
	h.messages[hash] = State{
		RetryHeight: height + 1,
		Confirmed:   false,
		DiscardAt:   timestamp,
	}
	h.mutex.Unlock()
}

func (h *RetryHandler) Get(hash string) (State, bool) {
	h.mutex.Lock()
	state, found := h.messages[hash]
	h.mutex.Unlock()
	return state, found
}

func (h *RetryHandler) Confirm(hash, txId string) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	_, found := h.messages[hash]
	if !found {
		return
	}
	h.messages[hash] = State{Confirmed: true, TxId: txId}
}

func (h *RetryHandler) Reschedule(hash string, height int64) {
	timestamp := time.Time{}
	state, found := h.messages[hash]
	if found {
		timestamp = state.DiscardAt
	}
	h.Add(hash, height, timestamp)
}

func (h *RetryHandler) Remove(hash string) {
	delete(h.messages, hash)
}
