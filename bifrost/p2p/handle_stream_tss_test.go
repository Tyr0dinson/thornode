package p2p

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

// handleStreamTss reads Conn().RemotePeer() and nothing else off the
// connection, so overriding that one method is enough. The embedded nil
// interface makes any other call panic loudly rather than silently pass.
type fakeTSSConn struct {
	network.Conn
	remote peer.ID
}

func (c fakeTSSConn) RemotePeer() peer.ID { return c.remote }

// handlerStream records whether the handler reset it, which is how every
// fail-closed branch in handleStreamTss is supposed to end.
type handlerStream struct {
	*MockNetworkStream
	conn        network.Conn
	resetCalled atomic.Bool
	closeCalled atomic.Bool
	closeErr    error
}

func (s *handlerStream) Reset() error {
	s.resetCalled.Store(true)
	return nil
}

func (s *handlerStream) Close() error {
	s.closeCalled.Store(true)
	return s.closeErr
}
func (s *handlerStream) Conn() network.Conn { return s.conn }

type stagedHandlerStream struct {
	*handlerStream
	initial     []byte
	remainder   []byte
	initialAt   int
	remainderAt int
	bodyGate    <-chan struct{}
	prefixReady chan<- struct{}
	prefixOnce  sync.Once
}

func (s *stagedHandlerStream) Read(p []byte) (int, error) {
	if s.initialAt < len(s.initial) {
		n := copy(p, s.initial[s.initialAt:])
		s.initialAt += n
		if s.initialAt == len(s.initial) {
			s.prefixOnce.Do(func() { s.prefixReady <- struct{}{} })
		}
		return n, nil
	}
	<-s.bodyGate
	if s.remainderAt >= len(s.remainder) {
		return 0, io.EOF
	}
	n := copy(p, s.remainder[s.remainderAt:])
	s.remainderAt += n
	return n, nil
}

func newStagedHandlerStream(
	remote peer.ID,
	frame []byte,
	bodyGate <-chan struct{},
	prefixReady chan<- struct{},
) *stagedHandlerStream {
	wire := make([]byte, LengthHeader+len(frame))
	binary.LittleEndian.PutUint32(wire[:LengthHeader], uint32(len(frame)))
	copy(wire[LengthHeader:], frame)
	initialSize := LengthHeader + TSSFramePreflightBytes
	if initialSize > len(wire) {
		initialSize = len(wire)
	}
	return &stagedHandlerStream{
		handlerStream: &handlerStream{
			MockNetworkStream: NewMockNetworkStream(),
			conn:              fakeTSSConn{remote: remote},
		},
		initial:     wire[:initialSize],
		remainder:   wire[initialSize:],
		bodyGate:    bodyGate,
		prefixReady: prefixReady,
	}
}

func newHandlerStream(remote peer.ID, frame []byte) *handlerStream {
	base := NewMockNetworkStream()
	header := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(header, uint32(len(frame)))
	_, _ = base.Write(header)
	_, _ = base.Write(frame)
	return &handlerStream{
		MockNetworkStream: base,
		conn:              fakeTSSConn{remote: remote},
	}
}

func newHandlerCommunication(
	topic messages.THORChainTSSMessageType,
	msgID string,
	allowed []peer.ID,
) *Communication {
	c := &Communication{
		subscribers:      make(map[messages.THORChainTSSMessageType]*MessageIDSubscriber),
		subscriberLocker: &sync.Mutex{},
		streamMgr:        NewStreamMgr(),
		stopChan:         make(chan struct{}),
	}
	c.SetSubscribeWithPeers(topic, msgID, make(chan *Message, 4), allowed, FrostMaxSessionPayload, 8)
	return c
}

func parkedStreamCount(sm *StreamMgr) int {
	sm.streamLocker.RLock()
	defer sm.streamLocker.RUnlock()
	total := 0
	for _, streams := range sm.unusedStreams {
		total += len(streams)
	}
	return total
}

// The frame's bounded PREFIX declares the session that preflight authorizes
// against, but the full body is what gets delivered. If the two are allowed to
// disagree, a peer authorized for session A can have its frame delivered into
// session B — it passes preflight under A's allow-list and budgets, then lands
// in B's channel. This is the single most security-load-bearing check in the
// handler, and the preflight tests cannot reach it because they never read a
// full body.
func TestHandleStreamTssRejectsFrameWhosePrefixAndBodyDisagree(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	authorized := messages.TSSFrostShareMsg
	msgID := "authorized-session"
	remote := peer.ID("member-peer")

	// Duplicate JSON keys are what let prefix and body disagree at all:
	// encoding/json takes the LAST occurrence of a repeated key, while the
	// preflight header parser only ever sees the FIRST — it reads a bounded
	// 4096-byte prefix. So a frame can pass preflight as the authorized session
	// (charged against its allow-list and budgets) and then unmarshal as a
	// different one. Pad the payload so the trailing override lands far outside
	// the preflight window.
	padding := strings.Repeat("A", 2*TSSFramePreflightBytes)

	cases := []struct {
		name     string
		override string
	}{
		{
			name:     "body switches msgID",
			override: `"message_id":"other-session"`,
		},
		{
			name:     "body switches message type",
			override: fmt.Sprintf(`"message_type":%d`, messages.TSSFrostResultMsg),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comm := newHandlerCommunication(authorized, msgID, []peer.ID{remote})
			sub := comm.getSubscriber(authorized, msgID)

			frame := []byte(fmt.Sprintf(
				`{"message_type":%d,"message_id":%q,"payload":%q,%s}`,
				authorized, msgID, padding, tc.override,
			))

			// Sanity: the frame really is the attack, i.e. preflight and body
			// genuinely disagree. Otherwise this test proves nothing.
			var decoded messages.WrappedMessage
			if err := json.Unmarshal(frame, &decoded); err != nil {
				t.Fatalf("attack frame must be valid JSON: %v", err)
			}
			if decoded.MessageType == authorized && decoded.MsgID == msgID {
				t.Fatal("frame body did not diverge from its prefix; the test is not exercising the check")
			}

			stream := newHandlerStream(remote, frame)
			comm.handleStreamTss(stream)

			// The divergent frame must never be delivered into the authorized
			// session's channel...
			select {
			case msg := <-sub.channel:
				t.Fatalf("transplanted frame was delivered into the authorized session: %q", msg.Payload)
			default:
			}
			// ...nor into whatever session its body named.
			if other := comm.getSubscriber(decoded.MessageType, decoded.MsgID); other != nil {
				select {
				case msg := <-other.channel:
					t.Fatalf("transplanted frame was delivered into the body's session: %q", msg.Payload)
				default:
				}
			}
			if !stream.resetCalled.Load() {
				t.Fatal("a frame whose body contradicts its preflight header must be reset")
			}
		})
	}
}

// A frame rejected in preflight must be RESET, not parked in streamMgr. Parking
// it would leak the stream: streamMgr only releases streams keyed by a msgID a
// ceremony later completes, and a rejected frame has no such ceremony.
func TestHandleStreamTssResetsPreflightRejectedStreamWithoutParkingIt(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	topic := messages.TSSFrostShareMsg
	msgID := "authorized-session"
	member := peer.ID("member-peer")
	outsider := peer.ID("outsider-peer")

	frame, err := json.Marshal(messages.WrappedMessage{
		MessageType: topic,
		MsgID:       msgID,
		Payload:     []byte("payload"),
	})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}

	comm := newHandlerCommunication(topic, msgID, []peer.ID{member})

	// The outsider clears the global connection gater but is not a member of
	// this ceremony, so preflight must reject it.
	stream := newHandlerStream(outsider, frame)
	comm.handleStreamTss(stream)

	if !stream.resetCalled.Load() {
		t.Fatal("a preflight-rejected stream must be reset")
	}
	if got := parkedStreamCount(comm.streamMgr); got != 0 {
		t.Fatalf("a preflight-rejected stream must not be parked in streamMgr, got %d parked", got)
	}

	sub := comm.getSubscriber(topic, msgID)
	select {
	case msg := <-sub.channel:
		t.Fatalf("unauthorized peer's frame reached the session channel: %q", msg.Payload)
	default:
	}
}

// The global MaxPayload gate runs BEFORE preflight(), so a peer that clears only
// the connection gater can trip it without belonging to any ceremony. It must be
// classified as a preflight rejection like every other pre-session reject, or the
// stream parks in StreamUnknown with no msgID a ceremony will ever release.
func TestHandleStreamTssResetsOversizedLengthHeaderWithoutParkingIt(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	topic := messages.TSSFrostShareMsg
	msgID := "authorized-session"
	member := peer.ID("member-peer")
	outsider := peer.ID("outsider-peer")

	comm := newHandlerCommunication(topic, msgID, []peer.ID{member})

	// Header only, no body: the size gate must reject on the declared length
	// alone. If it did not, the truncated prefix read below it would fail with
	// an ordinary error and the stream would be parked instead of reset.
	base := NewMockNetworkStream()
	header := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(header, MaxPayload+1)
	if _, err := base.Write(header); err != nil {
		t.Fatalf("write header: %v", err)
	}
	stream := &handlerStream{MockNetworkStream: base, conn: fakeTSSConn{remote: outsider}}

	comm.handleStreamTss(stream)

	if !stream.resetCalled.Load() {
		t.Fatal("a stream declaring an oversized payload length must be reset")
	}
	if got := parkedStreamCount(comm.streamMgr); got != 0 {
		t.Fatalf("an oversized-length stream must not be parked in streamMgr, got %d parked", got)
	}
}

// The authorized path must still work, or the tests above would pass for the
// wrong reason (a handler that drops everything).
func TestHandleStreamTssDeliversAuthorizedFrame(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	topic := messages.TSSFrostShareMsg
	msgID := "authorized-session"
	member := peer.ID("member-peer")

	sent := messages.WrappedMessage{
		MessageType: topic,
		MsgID:       msgID,
		Payload:     []byte("a-real-ceremony-frame"),
	}
	frame, err := json.Marshal(sent)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}

	comm := newHandlerCommunication(topic, msgID, []peer.ID{member})
	stream := newHandlerStream(member, frame)
	comm.handleStreamTss(stream)

	sub := comm.getSubscriber(topic, msgID)
	select {
	case msg := <-sub.channel:
		if !msg.Accept() {
			t.Fatal("authorized frame reservation was not accepted by the coordinator")
		}
		if msg.PeerID != member {
			t.Fatalf("delivered frame attributed to the wrong peer: %s", msg.PeerID)
		}
		var got messages.WrappedMessage
		if err = json.Unmarshal(msg.Payload, &got); err != nil {
			t.Fatalf("unmarshal delivered frame: %v", err)
		}
		if got.MsgID != msgID || got.MessageType != topic {
			t.Fatalf("delivered frame changed session: %+v", got)
		}
	default:
		t.Fatal("an authorized member's frame must be delivered to the session channel")
	}

	if stream.resetCalled.Load() {
		t.Fatal("an authorized frame's stream must not be reset")
	}
	if !stream.closeCalled.Load() {
		t.Fatal("an accepted FROST stream must be gracefully closed")
	}
	if got := parkedStreamCount(comm.streamMgr); got != 0 {
		t.Fatalf("an accepted FROST stream must not remain parked, got %d parked", got)
	}
}

func TestTSSReaderSlotReleasedBeforeCrossSessionDeliveryBackpressure(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	topic := messages.TSSFrostShareMsg
	peerA := peer.ID("session-a-peer")
	peerB := peer.ID("session-b-peer")
	channelA := make(chan *Message, 1)
	channelA <- &Message{}
	channelB := make(chan *Message, 1)
	comm := &Communication{
		subscribers:      make(map[messages.THORChainTSSMessageType]*MessageIDSubscriber),
		subscriberLocker: &sync.Mutex{},
		streamMgr:        NewStreamMgr(),
		stopChan:         make(chan struct{}),
		tssReaders:       newTSSReaderLimiter(1, 1),
	}
	comm.SetSubscribeWithPeers(topic, "session-a", channelA, []peer.ID{peerA}, FrostMaxSessionPayload, 8)
	comm.SetSubscribeWithPeers(topic, "session-b", channelB, []peer.ID{peerB}, FrostMaxSessionPayload, 8)

	frameA, err := json.Marshal(messages.WrappedMessage{
		MessageType: topic,
		MsgID:       "session-a",
		Payload:     []byte("a"),
	})
	if err != nil {
		t.Fatalf("marshal session A frame: %v", err)
	}
	frameB, err := json.Marshal(messages.WrappedMessage{
		MessageType: topic,
		MsgID:       "session-b",
		Payload:     []byte("b"),
	})
	if err != nil {
		t.Fatalf("marshal session B frame: %v", err)
	}

	streamA := newHandlerStream(peerA, frameA)
	doneA := make(chan struct{})
	go func() {
		comm.handleStreamTss(streamA)
		close(doneA)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		state := snapshotFrostBudget(comm.frostBudget)
		if state.inFlightFrames == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session A did not reach bounded delivery backpressure")
		}
		time.Sleep(time.Millisecond)
	}
	if total, peers := comm.tssReaders.snapshot(); total != 0 || len(peers) != 0 {
		t.Fatalf("authenticated session A retained preflight reader capacity: total=%d peers=%v", total, peers)
	}

	streamB := newHandlerStream(peerB, frameB)
	comm.handleStreamTss(streamB)
	select {
	case message := <-channelB:
		if !message.Accept() {
			t.Fatal("concurrent session B frame was not accepted")
		}
	default:
		t.Fatal("session A backpressure prevented session B delivery")
	}
	if streamB.resetCalled.Load() {
		t.Fatal("concurrent session B was reset by exhausted preflight readers")
	}

	<-channelA // release the deliberately full queue
	select {
	case <-doneA:
	case <-time.After(time.Second):
		t.Fatal("session A did not resume after coordinator backpressure cleared")
	}
	select {
	case message := <-channelA:
		if !message.Accept() {
			t.Fatal("session A frame was not accepted after backpressure cleared")
		}
	default:
		t.Fatal("session A frame was not delivered after backpressure cleared")
	}
	if total, peers := comm.tssReaders.snapshot(); total != 0 || len(peers) != 0 {
		t.Fatalf("reader slots leaked after both sessions completed: total=%d peers=%v", total, peers)
	}
}

func TestFrostAuthenticatedPrefixesDoNotExhaustPreflightReaders(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	const sessions = int(TSSMaxConcurrentReaders) + 32
	topic := messages.TSSFrostShareMsg
	bodyGate := make(chan struct{})
	prefixReady := make(chan struct{}, sessions)
	comm := &Communication{
		subscribers:      make(map[messages.THORChainTSSMessageType]*MessageIDSubscriber),
		subscriberLocker: &sync.Mutex{},
		streamMgr:        NewStreamMgr(),
		stopChan:         make(chan struct{}),
		tssReaders:       newTSSReaderLimiter(TSSMaxConcurrentReaders, TSSMaxReadersPerPeer),
	}
	channels := make([]chan *Message, sessions)
	streams := make([]*stagedHandlerStream, sessions)
	var wg sync.WaitGroup
	for i := 0; i < sessions; i++ {
		msgID := fmt.Sprintf("slow-valid-session-%03d", i)
		remote := peer.ID(fmt.Sprintf("slow-valid-peer-%03d", i))
		channels[i] = make(chan *Message, 1)
		comm.SetSubscribeWithPeers(
			topic,
			msgID,
			channels[i],
			[]peer.ID{remote},
			FrostMaxSessionPayload,
			8,
		)
		frame, err := json.Marshal(messages.WrappedMessage{
			MessageType: topic,
			MsgID:       msgID,
			Payload:     []byte(strings.Repeat("x", 8*1024)),
		})
		if err != nil {
			t.Fatalf("session %d: marshal frame: %v", i, err)
		}
		streams[i] = newStagedHandlerStream(remote, frame, bodyGate, prefixReady)
		wg.Add(1)
		go func(stream *stagedHandlerStream) {
			defer wg.Done()
			comm.handleStreamTss(stream)
		}(streams[i])
	}

	reached := 0
	deadline := time.After(3 * time.Second)
	for reached < sessions {
		select {
		case <-prefixReady:
			reached++
		case <-deadline:
			close(bodyGate)
			wg.Wait()
			t.Fatalf(
				"only %d/%d authenticated FROST prefixes progressed; reader cap is %d",
				reached,
				sessions,
				TSSMaxConcurrentReaders,
			)
		}
	}
	// prefixReady fires when the staged stream has returned the prefix bytes;
	// allow the handler callback which authenticates that prefix to run and
	// release its slot before inspecting the limiter.
	readerDeadline := time.Now().Add(time.Second)
	for {
		total, peers := comm.tssReaders.snapshot()
		if total == 0 && len(peers) == 0 {
			break
		}
		if time.Now().After(readerDeadline) {
			close(bodyGate)
			wg.Wait()
			t.Fatalf("authenticated FROST bodies retained preflight readers: total=%d peers=%v", total, peers)
		}
		time.Sleep(time.Millisecond)
	}
	if total, peers := comm.tssReaders.snapshot(); total != 0 || len(peers) != 0 {
		close(bodyGate)
		wg.Wait()
		t.Fatalf("authenticated FROST bodies retained preflight readers: total=%d peers=%v", total, peers)
	}

	close(bodyGate)
	wg.Wait()
	for i, channel := range channels {
		select {
		case message := <-channel:
			if !message.Accept() {
				t.Fatalf("session %d: authenticated frame was not accepted", i)
			}
		default:
			t.Fatalf("session %d: authenticated frame was not delivered", i)
		}
		if streams[i].resetCalled.Load() {
			t.Fatalf("session %d: valid FROST stream was reset", i)
		}
	}
}

func TestFrostOutboundStreamClosesWithoutParking(t *testing.T) {
	comm := &Communication{streamMgr: NewStreamMgr()}
	stream := &handlerStream{MockNetworkStream: NewMockNetworkStream()}

	if err := comm.finishWrittenTSSStream(stream, "frost-session", true); err != nil {
		t.Fatalf("finish FROST stream: %v", err)
	}
	if !stream.closeCalled.Load() {
		t.Fatal("successfully written FROST stream was not gracefully closed")
	}
	if stream.resetCalled.Load() {
		t.Fatal("successfully written FROST stream was reset")
	}
	if got := parkedStreamCount(comm.streamMgr); got != 0 {
		t.Fatalf("successfully written FROST stream remained parked: %d", got)
	}
}

func TestFrostOutboundCloseFailureResetsWithoutParking(t *testing.T) {
	comm := &Communication{streamMgr: NewStreamMgr()}
	stream := newHandlerStream(peer.ID("remote-peer"), nil)
	stream.closeErr = fmt.Errorf("close failed")

	if err := comm.finishWrittenTSSStream(stream, "frost-session", true); err == nil {
		t.Fatal("expected the graceful close error")
	}
	if !stream.closeCalled.Load() {
		t.Fatal("FROST outbound stream was not gracefully closed")
	}
	if !stream.resetCalled.Load() {
		t.Fatal("FROST outbound stream was not reset after graceful close failed")
	}
	if got := parkedStreamCount(comm.streamMgr); got != 0 {
		t.Fatalf("failed FROST outbound close parked %d streams", got)
	}
}

func TestLegacyOutboundStreamRemainsParked(t *testing.T) {
	comm := &Communication{streamMgr: NewStreamMgr()}
	stream := &handlerStream{MockNetworkStream: NewMockNetworkStream()}

	if err := comm.finishWrittenTSSStream(stream, "legacy-session", false); err != nil {
		t.Fatalf("finish legacy stream: %v", err)
	}
	if stream.closeCalled.Load() || stream.resetCalled.Load() {
		t.Fatal("legacy stream lifecycle changed")
	}
	if got := parkedStreamCount(comm.streamMgr); got != 1 {
		t.Fatalf("legacy stream should remain parked for session release, got %d", got)
	}
}
