package p2p

import (
	"sync"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
)

// MessageSubscription is the security boundary between an authenticated
// libp2p stream and a protocol session.  A node may be allowed by the global
// connection gater without being a member of this particular ceremony, so a
// subscription can additionally restrict delivery to an exact peer set.
//
// The per-peer counter is intentionally kept on the subscription rather than
// globally: it is bounded by the allow-list and is discarded with the session.
type MessageSubscription struct {
	channel            chan *Message
	allowedPeers       map[peer.ID]struct{}
	maxPayloadBytes    int
	maxMessagesPerPeer uint32
	maxBytesPerPeer    uint64
	maxSessionBytes    uint64
	done               chan struct{}
	doneOnce           sync.Once
	authEpoch          uint64
	sessionBudget      *frostCeremonyBudget
	stopped            bool
	deliveryLock       sync.Mutex

	lock         sync.Mutex
	peerCounts   map[peer.ID]uint32
	peerBytes    map[peer.ID]uint64
	sessionBytes uint64
	reservations map[*messageReservation]struct{}
}

func newMessageSubscription(
	channel chan *Message,
	allowedPeers []peer.ID,
	maxPayloadBytes int,
	maxMessagesPerPeer uint32,
	maxBytesPerPeer uint64,
	maxSessionBytes uint64,
	sessionBudget *frostCeremonyBudget,
) *MessageSubscription {
	var allowed map[peer.ID]struct{}
	if allowedPeers != nil {
		allowed = make(map[peer.ID]struct{}, len(allowedPeers))
		for _, id := range allowedPeers {
			allowed[id] = struct{}{}
		}
	}
	return &MessageSubscription{
		channel:            channel,
		allowedPeers:       allowed,
		maxPayloadBytes:    maxPayloadBytes,
		maxMessagesPerPeer: maxMessagesPerPeer,
		maxBytesPerPeer:    maxBytesPerPeer,
		maxSessionBytes:    maxSessionBytes,
		done:               make(chan struct{}),
		sessionBudget:      sessionBudget,
		peerCounts:         make(map[peer.ID]uint32, len(allowed)),
		peerBytes:          make(map[peer.ID]uint64, len(allowed)),
		reservations:       make(map[*messageReservation]struct{}),
	}
}

// messageReservation follows one frame from preflight through coordinator
// acceptance. Failed reads, peer revocation, and cancelled delivery refund it;
// accepted frames retain their cumulative byte charge until session teardown.
type messageReservation struct {
	subscription *MessageSubscription
	peerID       peer.ID
	payloadBytes uint64
	authEpoch    uint64
	revoked      chan struct{}

	settled   bool
	stream    network.Stream
	streamMgr *StreamMgr
	msgID     string
}

// reserve authorizes a frame and atomically reserves exactly one per-peer
// message allowance plus its cumulative byte allowances.
func (s *MessageSubscription) reserve(id peer.ID, payloadBytes int) *messageReservation {
	if s == nil || s.channel == nil || payloadBytes < 0 {
		return nil
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.stopped || !s.authorizedLocked(id, payloadBytes) {
		return nil
	}
	if s.maxMessagesPerPeer > 0 && s.peerCounts[id] >= s.maxMessagesPerPeer {
		return nil
	}
	if !s.reserveBytesLocked(id, uint64(payloadBytes)) {
		return nil
	}
	if s.maxMessagesPerPeer > 0 {
		s.peerCounts[id]++
	}
	reservation := &messageReservation{
		subscription: s,
		peerID:       id,
		payloadBytes: uint64(payloadBytes),
		authEpoch:    s.authEpoch,
		revoked:      make(chan struct{}),
	}
	s.reservations[reservation] = struct{}{}
	return reservation
}

// accepts preserves the test/helper API and models an immediately consumed
// frame. Production preflight keeps the returned reservation until the
// coordinator accepts or rejects it.
func (s *MessageSubscription) accepts(id peer.ID, payloadBytes int) bool {
	reservation := s.reserve(id, payloadBytes)
	return reservation != nil && reservation.accept()
}

// reserveBytesLocked atomically applies both fairness and total-session
// limits. Failed reservations consume neither allowance. The subtraction form
// avoids integer overflow if these limits are changed in the future.
func (s *MessageSubscription) reserveBytesLocked(id peer.ID, payloadBytes uint64) bool {
	peerBytes := s.peerBytes[id]
	if s.sessionBudget == nil {
		if exceedsByteBudget(peerBytes, payloadBytes, s.maxBytesPerPeer) ||
			exceedsByteBudget(s.sessionBytes, payloadBytes, s.maxSessionBytes) {
			return false
		}
	} else if !s.sessionBudget.manager.reserve(s.sessionBudget, id, payloadBytes) {
		return false
	}
	if s.sessionBudget != nil || s.maxBytesPerPeer > 0 {
		s.peerBytes[id] = peerBytes + payloadBytes
	}
	if s.sessionBudget != nil || s.maxSessionBytes > 0 {
		s.sessionBytes += payloadBytes
	}
	return true
}

func exceedsByteBudget(current, additional, maximum uint64) bool {
	return maximum > 0 && (current > maximum || additional > maximum-current)
}

// authorized rechecks mutable peer/frame constraints after the frame read
// without consuming a second message or byte allowance.
func (s *MessageSubscription) authorized(id peer.ID, payloadBytes int) bool {
	if s == nil || s.channel == nil || payloadBytes < 0 {
		return false
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.authorizedLocked(id, payloadBytes)
}

func (s *MessageSubscription) authorizedLocked(id peer.ID, payloadBytes int) bool {
	if s.maxPayloadBytes > 0 && payloadBytes > s.maxPayloadBytes {
		return false
	}
	if s.allowedPeers != nil {
		if _, ok := s.allowedPeers[id]; !ok {
			return false
		}
	}
	return true
}

func (s *MessageSubscription) setAllowedPeers(allowedPeers []peer.ID) bool {
	if s == nil {
		return false
	}
	allowed := make(map[peer.ID]struct{}, len(allowedPeers))
	for _, id := range allowedPeers {
		allowed[id] = struct{}{}
	}
	s.lock.Lock()
	if s.stopped {
		s.lock.Unlock()
		return false
	}
	if peerSetsEqual(s.allowedPeers, allowed) {
		s.lock.Unlock()
		return true
	}
	s.allowedPeers = allowed
	s.authEpoch++
	revoked := make([]*messageReservation, 0, len(s.reservations))
	for reservation := range s.reservations {
		if _, retained := allowed[reservation.peerID]; retained {
			// A frame from a peer which survived party formation remains the
			// same authenticated FROST message. Move its reservation to the new
			// epoch so one-shot protocol frames queued by a faster peer are not
			// discarded merely because the final party is a strict subset.
			reservation.authEpoch = s.authEpoch
			continue
		}
		revoked = append(revoked, reservation)
	}
	s.lock.Unlock()

	// Removed peers are revoked and refunded; retained peers keep their exact
	// reservation, accounting, and queued message under the new epoch.
	for _, reservation := range revoked {
		reservation.reject()
	}
	s.purgeRevokedQueuedMessages()
	return true
}

func (s *MessageSubscription) Channel() chan *Message {
	if s == nil {
		return nil
	}
	return s.channel
}

// deliver applies backpressure when a bounded protocol queue is full. Dropping
// an authenticated FROST frame here can make an otherwise healthy ceremony
// fail merely because a valid burst exceeded the local buffer. Session byte
// and message budgets bound the number of blocked handlers, and stop unblocks
// them during shutdown.
func (s *MessageSubscription) deliver(message *Message, stop <-chan struct{}) bool {
	if s == nil || s.channel == nil || message == nil {
		return false
	}
	if message.reservation == nil {
		select {
		case s.channel <- message:
			return true
		case <-s.done:
			return false
		case <-stop:
			return false
		}
	}
	session := s.sessionBudget
	deliveryLock := &s.deliveryLock
	if session != nil {
		deliveryLock = &session.deliveryLock
	}
	deliveryLock.Lock()
	defer deliveryLock.Unlock()
	if !message.reservation.current() {
		return false
	}
	select {
	case s.channel <- message:
		if session == nil {
			// Legacy TSS subscriptions do not narrow parties or share a FROST
			// budget, so enqueue is their final authorization boundary.
			return message.reservation.accept()
		}
		return true
	case <-message.reservation.revoked:
		return false
	case <-s.done:
		return false
	case <-stop:
		return false
	}
}

func (s *MessageSubscription) stop() {
	if s == nil {
		return
	}
	s.doneOnce.Do(func() {
		close(s.done)

		s.lock.Lock()
		s.stopped = true
		s.authEpoch++
		stale := make([]*messageReservation, 0, len(s.reservations))
		for reservation := range s.reservations {
			reservation.settled = true
			close(reservation.revoked)
			stale = append(stale, reservation)
		}
		clear(s.reservations)
		peerBytes := make(map[peer.ID]uint64, len(s.peerBytes))
		for id, amount := range s.peerBytes {
			peerBytes[id] = amount
		}
		totalBytes := s.sessionBytes
		inFlightFrames := uint32(len(stale))
		clear(s.peerCounts)
		clear(s.peerBytes)
		s.sessionBytes = 0
		s.lock.Unlock()

		if s.sessionBudget != nil {
			s.sessionBudget.manager.releaseSubscription(
				s.sessionBudget,
				peerBytes,
				totalBytes,
				inFlightFrames,
			)
			s.sessionBudget.manager.detach(s.sessionBudget)
		}
		for _, reservation := range stale {
			reservation.resetStream()
		}
	})
}

func (s *MessageSubscription) purgeRevokedQueuedMessages() {
	if s == nil || s.channel == nil {
		return
	}
	session := s.sessionBudget
	deliveryLock := &s.deliveryLock
	if session != nil {
		deliveryLock = &session.deliveryLock
	}
	deliveryLock.Lock()
	defer deliveryLock.Unlock()

	kept := make([]*Message, 0, len(s.channel))
	for {
		select {
		case message := <-s.channel:
			if message != nil &&
				message.reservation != nil &&
				message.reservation.subscription == s &&
				!message.reservation.current() {
				message.Reject()
				continue
			}
			kept = append(kept, message)
		default:
			for _, message := range kept {
				s.channel <- message
			}
			return
		}
	}
}

func peerSetsEqual(left, right map[peer.ID]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for id := range left {
		if _, ok := right[id]; !ok {
			return false
		}
	}
	return true
}

func (r *messageReservation) attachStream(stream network.Stream, streamMgr *StreamMgr, msgID string) {
	if r == nil || r.subscription == nil || stream == nil {
		return
	}
	s := r.subscription
	s.lock.Lock()
	if r.settled || s.stopped {
		s.lock.Unlock()
		_ = stream.Reset()
		return
	}
	r.stream = stream
	r.streamMgr = streamMgr
	r.msgID = msgID
	s.lock.Unlock()
}

// parkStream atomically crosses the last gap between authorization and stream
// retention. Party narrowing either settles the reservation before this lock,
// in which case the dead stream is never parked, or runs afterwards and can
// remove the already parked stream through resetStream.
func (r *messageReservation) parkStream() bool {
	if r == nil || r.subscription == nil {
		return false
	}
	s := r.subscription
	s.lock.Lock()
	defer s.lock.Unlock()
	if r.settled ||
		s.stopped ||
		r.stream == nil ||
		r.streamMgr == nil ||
		r.authEpoch != s.authEpoch ||
		!s.authorizedLocked(r.peerID, int(r.payloadBytes)) {
		return false
	}
	r.streamMgr.AddStream(r.msgID, r.stream)
	return true
}

func (r *messageReservation) current() bool {
	if r == nil || r.subscription == nil {
		return false
	}
	s := r.subscription
	s.lock.Lock()
	defer s.lock.Unlock()
	return !r.settled &&
		!s.stopped &&
		r.authEpoch == s.authEpoch &&
		s.authorizedLocked(r.peerID, int(r.payloadBytes))
}

func (r *messageReservation) accept() bool {
	if r == nil || r.subscription == nil {
		return false
	}
	s := r.subscription
	session := s.sessionBudget
	if session != nil {
		session.deliveryLock.Lock()
	}
	s.lock.Lock()
	if r.settled ||
		s.stopped ||
		r.authEpoch != s.authEpoch ||
		!s.authorizedLocked(r.peerID, int(r.payloadBytes)) {
		s.lock.Unlock()
		if session != nil {
			session.deliveryLock.Unlock()
		}
		r.reject()
		return false
	}
	r.settled = true
	delete(s.reservations, r)
	s.lock.Unlock()
	if session != nil {
		session.manager.commitFrame(session)
		session.deliveryLock.Unlock()
		r.closeStream()
	}
	return true
}

func (r *messageReservation) reject() {
	if r == nil || r.subscription == nil {
		return
	}
	s := r.subscription
	s.lock.Lock()
	if r.settled {
		s.lock.Unlock()
		return
	}
	r.settled = true
	close(r.revoked)
	delete(s.reservations, r)
	if s.maxMessagesPerPeer > 0 && s.peerCounts[r.peerID] > 0 {
		s.peerCounts[r.peerID]--
		if s.peerCounts[r.peerID] == 0 {
			delete(s.peerCounts, r.peerID)
		}
	}
	s.peerBytes[r.peerID] = saturatingSubtract(s.peerBytes[r.peerID], r.payloadBytes)
	if s.peerBytes[r.peerID] == 0 {
		delete(s.peerBytes, r.peerID)
	}
	s.sessionBytes = saturatingSubtract(s.sessionBytes, r.payloadBytes)
	session := s.sessionBudget
	s.lock.Unlock()

	if session != nil {
		session.manager.refund(session, r.peerID, r.payloadBytes, true)
	}
	r.resetStream()
}

func (r *messageReservation) resetStream() {
	if r == nil || r.subscription == nil {
		return
	}
	s := r.subscription
	s.lock.Lock()
	stream, streamMgr, msgID := r.takeStreamLocked()
	s.lock.Unlock()
	if stream == nil {
		return
	}
	if streamMgr != nil {
		streamMgr.RemoveStream(msgID, stream)
	}
	_ = stream.Reset()
}

// closeStream gracefully closes a fully read FROST stream and removes the
// handler's retained reference. FROST senders also close their write side after
// a successful frame write, completing the libp2p half-close handshake without
// aborting bytes that the receiver has not yet consumed.
func (r *messageReservation) closeStream() {
	if r == nil || r.subscription == nil {
		return
	}
	s := r.subscription
	s.lock.Lock()
	stream, streamMgr, msgID := r.takeStreamLocked()
	s.lock.Unlock()
	if stream == nil {
		return
	}
	if streamMgr != nil {
		streamMgr.RemoveStream(msgID, stream)
	}
	if err := stream.Close(); err != nil {
		_ = stream.Reset()
	}
}

// takeStreamLocked transfers ownership of the reservation's stream reference
// to the caller. Every stream-field read and clear is serialized by the
// subscription lock; network Close/Reset happens only after that lock releases.
func (r *messageReservation) takeStreamLocked() (network.Stream, *StreamMgr, string) {
	stream := r.stream
	streamMgr := r.streamMgr
	msgID := r.msgID
	r.stream = nil
	r.streamMgr = nil
	r.msgID = ""
	return stream, streamMgr, msgID
}

// MessageIDSubscriber
type MessageIDSubscriber struct {
	lock        *sync.Mutex
	subscribers map[string]*MessageSubscription
}

// NewMessageIDSubscriber create a new instance of MessageIDSubscriber
func NewMessageIDSubscriber() *MessageIDSubscriber {
	return &MessageIDSubscriber{
		lock:        &sync.Mutex{},
		subscribers: make(map[string]*MessageSubscription),
	}
}

// Subscribe a message id
func (ms *MessageIDSubscriber) Subscribe(msgID string, channel chan *Message) {
	ms.lock.Lock()
	defer ms.lock.Unlock()
	if previous := ms.subscribers[msgID]; previous != nil {
		previous.stop()
	}
	ms.subscribers[msgID] = newMessageSubscription(channel, nil, 0, 0, 0, 0, nil)
}

// SubscribeRestricted registers a session-scoped authenticated peer allow-list
// and resource bounds. A nil allow-list permits every authenticated peer; an
// empty non-nil list accepts no peers. Delivery uses bounded backpressure so a
// valid burst is not silently discarded.
func (ms *MessageIDSubscriber) SubscribeRestricted(
	msgID string,
	channel chan *Message,
	allowedPeers []peer.ID,
	maxPayloadBytes int,
	maxMessagesPerPeer uint32,
) {
	ms.SubscribeRestrictedWithBudgets(
		msgID,
		channel,
		allowedPeers,
		maxPayloadBytes,
		maxMessagesPerPeer,
		FrostMaxBytesPerPeer,
		FrostMaxSessionBytes,
	)
}

// SubscribeRestrictedWithBudgets registers the full set of resource bounds.
// It is separate from SubscribeRestricted to preserve compatibility for
// callers while ensuring the production FROST path always supplies explicit
// cumulative byte limits.
func (ms *MessageIDSubscriber) SubscribeRestrictedWithBudgets(
	msgID string,
	channel chan *Message,
	allowedPeers []peer.ID,
	maxPayloadBytes int,
	maxMessagesPerPeer uint32,
	maxBytesPerPeer uint64,
	maxSessionBytes uint64,
) {
	ms.subscribeRestrictedWithBudgets(
		msgID,
		channel,
		allowedPeers,
		maxPayloadBytes,
		maxMessagesPerPeer,
		maxBytesPerPeer,
		maxSessionBytes,
		nil,
	)
}

func (ms *MessageIDSubscriber) subscribeRestrictedWithBudgets(
	msgID string,
	channel chan *Message,
	allowedPeers []peer.ID,
	maxPayloadBytes int,
	maxMessagesPerPeer uint32,
	maxBytesPerPeer uint64,
	maxSessionBytes uint64,
	sessionBudget *frostCeremonyBudget,
) {
	ms.lock.Lock()
	defer ms.lock.Unlock()
	if previous := ms.subscribers[msgID]; previous != nil {
		previous.stop()
	}
	ms.subscribers[msgID] = newMessageSubscription(
		channel,
		allowedPeers,
		maxPayloadBytes,
		maxMessagesPerPeer,
		maxBytesPerPeer,
		maxSessionBytes,
		sessionBudget,
	)
}

// UnSubscribe a messageid
func (ms *MessageIDSubscriber) UnSubscribe(msgID string) {
	ms.lock.Lock()
	defer ms.lock.Unlock()
	if subscription := ms.subscribers[msgID]; subscription != nil {
		subscription.stop()
	}
	delete(ms.subscribers, msgID)
}

func (ms *MessageIDSubscriber) UpdateAllowedPeers(msgID string, allowedPeers []peer.ID) bool {
	ms.lock.Lock()
	subscription, ok := ms.subscribers[msgID]
	ms.lock.Unlock()
	if !ok || subscription == nil {
		return false
	}
	return subscription.setAllowedPeers(allowedPeers)
}

// GetSubscribers return a subscriber of given message id
func (ms *MessageIDSubscriber) GetSubscriber(msgID string) *MessageSubscription {
	ms.lock.Lock()
	defer ms.lock.Unlock()
	c, ok := ms.subscribers[msgID]
	if !ok {
		return nil
	}
	return c
}

// IsEmpty check whether there is subscribers
func (ms *MessageIDSubscriber) IsEmpty() bool {
	ms.lock.Lock()
	defer ms.lock.Unlock()
	return len(ms.subscribers) == 0
}
