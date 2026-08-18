package p2p

import (
	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

type frostBroadcastTask struct {
	peers    []peer.ID
	msg      []byte
	msgID    string
	msgType  messages.THORChainTSSMessageType
	terminal bool
}

// frostBroadcastScheduler keeps a bounded, round-robin queue by ceremony.
// Active counts are owned by ProcessBroadcast's single goroutine, so no lock is
// required and a busy ceremony cannot sit ahead of another ceremony forever.
type frostBroadcastScheduler struct {
	maxActive           uint32
	maxActivePerSession uint32
	maxPending          int

	active                uint32
	activeByMsgID         map[string]uint32
	activeOrdinaryByMsgID map[string]uint32
	pending               int
	pendingByID           map[string][]frostBroadcastTask
	ready                 []string
	readySet              map[string]struct{}
}

func newFrostBroadcastScheduler(maxActive, maxActivePerSession uint32, maxPending int) *frostBroadcastScheduler {
	return &frostBroadcastScheduler{
		maxActive:             maxActive,
		maxActivePerSession:   maxActivePerSession,
		maxPending:            maxPending,
		activeByMsgID:         make(map[string]uint32),
		activeOrdinaryByMsgID: make(map[string]uint32),
		pendingByID:           make(map[string][]frostBroadcastTask),
		readySet:              make(map[string]struct{}),
	}
}

func (s *frostBroadcastScheduler) enqueue(task frostBroadcastTask) bool {
	if s == nil || task.msgID == "" || (s.maxPending > 0 && s.pending >= s.maxPending) {
		return false
	}
	queue := s.pendingByID[task.msgID]
	if task.terminal {
		// Final result propagation must not sit behind hundreds of obsolete
		// round messages from the same ceremony. Preserve FIFO among terminal
		// results while placing them ahead of ordinary round traffic.
		insertAt := 0
		for insertAt < len(queue) && queue[insertAt].terminal {
			insertAt++
		}
		queue = append(queue, frostBroadcastTask{})
		copy(queue[insertAt+1:], queue[insertAt:])
		queue[insertAt] = task
	} else {
		queue = append(queue, task)
	}
	s.pendingByID[task.msgID] = queue
	s.pending++
	if _, exists := s.readySet[task.msgID]; !exists {
		s.ready = append(s.ready, task.msgID)
		s.readySet[task.msgID] = struct{}{}
	}
	return true
}

func (s *frostBroadcastScheduler) next() (frostBroadcastTask, bool) {
	if s == nil ||
		(s.maxActive > 0 && s.active >= s.maxActive) ||
		len(s.ready) == 0 {
		return frostBroadcastTask{}, false
	}

	// Scan every ready ceremony once. Entries at their per-session ceiling stay
	// ready for the completion event that will make them eligible again.
	readyCount := len(s.ready)
	for range readyCount {
		msgID := s.ready[0]
		s.ready = s.ready[1:]
		queue := s.pendingByID[msgID]
		if len(queue) == 0 {
			delete(s.readySet, msgID)
			delete(s.pendingByID, msgID)
			continue
		}
		if s.maxActivePerSession > 0 &&
			s.activeByMsgID[msgID] >= s.maxActivePerSession &&
			!queue[0].terminal {
			s.ready = append(s.ready, msgID)
			continue
		}
		task := queue[0]
		// Drop references held by the backing array as soon as a task becomes
		// active; a long session queue must not retain already-sent payloads.
		queue[0] = frostBroadcastTask{}
		queue = queue[1:]
		s.pending--
		s.active++
		s.activeByMsgID[msgID]++
		if !task.terminal {
			s.activeOrdinaryByMsgID[msgID]++
		}
		if len(queue) == 0 {
			delete(s.pendingByID, msgID)
			delete(s.readySet, msgID)
		} else {
			s.pendingByID[msgID] = queue
			s.ready = append(s.ready, msgID)
		}
		return task, true
	}
	return frostBroadcastTask{}, false
}

func (s *frostBroadcastScheduler) complete(msgID string, terminal bool) {
	if s == nil || msgID == "" || s.activeByMsgID[msgID] == 0 {
		return
	}
	s.activeByMsgID[msgID]--
	s.active = saturatingSubtract32(s.active, 1)
	if s.activeByMsgID[msgID] == 0 {
		delete(s.activeByMsgID, msgID)
	}
	if !terminal && s.activeOrdinaryByMsgID[msgID] > 0 {
		s.activeOrdinaryByMsgID[msgID]--
		if s.activeOrdinaryByMsgID[msgID] == 0 {
			delete(s.activeOrdinaryByMsgID, msgID)
		}
	}
}

func (s *frostBroadcastScheduler) activeOrdinary(msgID string) uint32 {
	if s == nil {
		return 0
	}
	return s.activeOrdinaryByMsgID[msgID]
}

// cancel removes obsolete round traffic for a completed ceremony while
// preserving final result propagation. Result messages are the recovery path
// for keyless/standby members and are intentionally allowed to outlive the
// local coordinator.
func (s *frostBroadcastScheduler) cancel(msgID string) int {
	if s == nil || msgID == "" {
		return 0
	}
	queue := s.pendingByID[msgID]
	if len(queue) == 0 {
		return 0
	}
	dropped := 0
	for _, task := range queue {
		if !task.terminal {
			dropped++
		}
	}
	// Do not touch an all-terminal queue. Clearing it before discovering that
	// there is no ordinary work to drop corrupts the live pending tasks while
	// leaving the scheduler counters unchanged.
	if dropped == 0 {
		return 0
	}

	kept := make([]frostBroadcastTask, 0, len(queue)-dropped)
	for i := range queue {
		if queue[i].terminal {
			kept = append(kept, queue[i])
		}
		queue[i] = frostBroadcastTask{}
	}
	s.pending -= dropped
	if len(kept) == 0 {
		delete(s.pendingByID, msgID)
		delete(s.readySet, msgID)
		filtered := s.ready[:0]
		for _, readyID := range s.ready {
			if readyID != msgID {
				filtered = append(filtered, readyID)
			}
		}
		s.ready = filtered
	} else {
		s.pendingByID[msgID] = kept
	}
	return dropped
}
