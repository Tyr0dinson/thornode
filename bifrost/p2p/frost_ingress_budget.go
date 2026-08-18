package p2p

import (
	"sync"

	"github.com/libp2p/go-libp2p-core/peer"
)

// frostIngressBudget is the node-wide FROST ingress accounting boundary.
// MessageSubscription still owns per-topic message counts, while this manager
// makes byte and in-flight limits apply once across every topic belonging to a
// ceremony and once across every concurrent ceremony on the node.
type frostIngressBudget struct {
	lock sync.Mutex

	maxNodeBytes          uint64
	maxCeremonyBytes      uint64
	maxBytesPerPeer       uint64
	maxConcurrentFrames   uint32
	maxFramesPerCeremony  uint32
	maxConcurrentSessions uint32

	nodeBytes      uint64
	inFlightFrames uint32
	activeSessions uint32
	sessions       map[string]*frostCeremonyBudget
}

// frostCeremonyBudget is shared by every type+msgID subscription for one
// FROST ceremony.
type frostCeremonyBudget struct {
	manager *frostIngressBudget
	msgID   string

	refs           uint32
	active         bool
	bytes          uint64
	inFlightFrames uint32
	peerBytes      map[peer.ID]uint64
}

func newFrostIngressBudget(
	maxNodeBytes,
	maxCeremonyBytes,
	maxBytesPerPeer uint64,
	maxConcurrentFrames,
	maxFramesPerCeremony,
	maxConcurrentSessions uint32,
) *frostIngressBudget {
	return &frostIngressBudget{
		maxNodeBytes:          maxNodeBytes,
		maxCeremonyBytes:      maxCeremonyBytes,
		maxBytesPerPeer:       maxBytesPerPeer,
		maxConcurrentFrames:   maxConcurrentFrames,
		maxFramesPerCeremony:  maxFramesPerCeremony,
		maxConcurrentSessions: maxConcurrentSessions,
		sessions:              make(map[string]*frostCeremonyBudget),
	}
}

func newDefaultFrostIngressBudget() *frostIngressBudget {
	return newFrostIngressBudget(
		FrostMaxNodeBytes,
		FrostMaxSessionBytes,
		FrostMaxBytesPerPeer,
		FrostMaxConcurrentFrames,
		FrostMaxConcurrentFramesPerSession,
		0,
	)
}

func (b *frostIngressBudget) attach(msgID string) *frostCeremonyBudget {
	if b == nil {
		return nil
	}
	b.lock.Lock()
	defer b.lock.Unlock()

	session := b.sessions[msgID]
	if session == nil {
		session = &frostCeremonyBudget{
			manager:   b,
			msgID:     msgID,
			peerBytes: make(map[peer.ID]uint64),
		}
		b.sessions[msgID] = session
	}
	session.refs++
	return session
}

func (b *frostIngressBudget) detach(session *frostCeremonyBudget) {
	if b == nil || session == nil {
		return
	}
	b.lock.Lock()
	defer b.lock.Unlock()

	current := b.sessions[session.msgID]
	if current != session {
		return
	}
	if session.refs > 0 {
		session.refs--
	}
	if session.refs != 0 {
		return
	}

	// Subscriptions normally refund their own retained accounting before
	// detaching. Keep this final subtraction as a fail-safe so replacing or
	// cancelling a subscription can never strand node-wide capacity.
	b.nodeBytes = saturatingSubtract(b.nodeBytes, session.bytes)
	b.inFlightFrames = saturatingSubtract32(b.inFlightFrames, session.inFlightFrames)
	if session.active {
		b.activeSessions = saturatingSubtract32(b.activeSessions, 1)
	}
	delete(b.sessions, session.msgID)
	session.active = false
	session.bytes = 0
	session.inFlightFrames = 0
	clear(session.peerBytes)
}

func (b *frostIngressBudget) reserve(session *frostCeremonyBudget, id peer.ID, payloadBytes uint64) bool {
	if b == nil || session == nil {
		return true
	}
	b.lock.Lock()
	defer b.lock.Unlock()

	if b.sessions[session.msgID] != session || session.refs == 0 {
		return false
	}
	if b.maxConcurrentFrames > 0 && b.inFlightFrames >= b.maxConcurrentFrames {
		return false
	}
	if b.maxFramesPerCeremony > 0 && session.inFlightFrames >= b.maxFramesPerCeremony {
		return false
	}
	if !session.active &&
		b.maxConcurrentSessions > 0 &&
		b.activeSessions >= b.maxConcurrentSessions {
		return false
	}
	if exceedsByteBudget(b.nodeBytes, payloadBytes, b.maxNodeBytes) ||
		exceedsByteBudget(session.bytes, payloadBytes, b.maxCeremonyBytes) ||
		exceedsByteBudget(session.peerBytes[id], payloadBytes, b.maxBytesPerPeer) {
		return false
	}

	if !session.active {
		session.active = true
		b.activeSessions++
	}
	b.nodeBytes += payloadBytes
	b.inFlightFrames++
	session.bytes += payloadBytes
	session.inFlightFrames++
	session.peerBytes[id] += payloadBytes
	return true
}

// commitFrame retains the cumulative byte charge for the life of the
// ceremony, but releases the in-flight slot once the coordinator accepts the
// fully read frame.
func (b *frostIngressBudget) commitFrame(session *frostCeremonyBudget) {
	if b == nil || session == nil {
		return
	}
	b.lock.Lock()
	defer b.lock.Unlock()
	if b.sessions[session.msgID] != session {
		return
	}
	b.inFlightFrames = saturatingSubtract32(b.inFlightFrames, 1)
	session.inFlightFrames = saturatingSubtract32(session.inFlightFrames, 1)
}

func (b *frostIngressBudget) refund(
	session *frostCeremonyBudget,
	id peer.ID,
	payloadBytes uint64,
	inFlight bool,
) {
	if b == nil || session == nil {
		return
	}
	b.lock.Lock()
	defer b.lock.Unlock()
	if b.sessions[session.msgID] != session {
		return
	}

	b.nodeBytes = saturatingSubtract(b.nodeBytes, payloadBytes)
	session.bytes = saturatingSubtract(session.bytes, payloadBytes)
	session.peerBytes[id] = saturatingSubtract(session.peerBytes[id], payloadBytes)
	if session.peerBytes[id] == 0 {
		delete(session.peerBytes, id)
	}
	if inFlight {
		b.inFlightFrames = saturatingSubtract32(b.inFlightFrames, 1)
		session.inFlightFrames = saturatingSubtract32(session.inFlightFrames, 1)
	}
	if session.active && session.bytes == 0 && session.inFlightFrames == 0 {
		session.active = false
		b.activeSessions = saturatingSubtract32(b.activeSessions, 1)
	}
}

// releaseSubscription removes both committed and in-flight accounting owned by
// a subscription. It is called exactly once by MessageSubscription.stop.
func (b *frostIngressBudget) releaseSubscription(
	session *frostCeremonyBudget,
	peerBytes map[peer.ID]uint64,
	totalBytes uint64,
	inFlightFrames uint32,
) {
	if b == nil || session == nil {
		return
	}
	b.lock.Lock()
	defer b.lock.Unlock()
	if b.sessions[session.msgID] != session {
		return
	}

	b.nodeBytes = saturatingSubtract(b.nodeBytes, totalBytes)
	session.bytes = saturatingSubtract(session.bytes, totalBytes)
	for id, amount := range peerBytes {
		session.peerBytes[id] = saturatingSubtract(session.peerBytes[id], amount)
		if session.peerBytes[id] == 0 {
			delete(session.peerBytes, id)
		}
	}
	b.inFlightFrames = saturatingSubtract32(b.inFlightFrames, inFlightFrames)
	session.inFlightFrames = saturatingSubtract32(session.inFlightFrames, inFlightFrames)
	if session.active && session.bytes == 0 && session.inFlightFrames == 0 {
		session.active = false
		b.activeSessions = saturatingSubtract32(b.activeSessions, 1)
	}
}

func saturatingSubtract(current, amount uint64) uint64 {
	if amount >= current {
		return 0
	}
	return current - amount
}

func saturatingSubtract32(current, amount uint32) uint32 {
	if amount >= current {
		return 0
	}
	return current - amount
}
