package observer

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
)

type attestationStateAdmissionTestStream struct {
	MockStream
	reader          *bytes.Reader
	blockRead       bool
	blockAfterInput bool
	panicRead       bool
	reset           chan struct{}
	resetOnce       sync.Once
	blockedRead     chan struct{}
	blockedOnce     sync.Once
	readBytes       atomic.Int64
	resetCalls      atomic.Int32
	closeCalls      atomic.Int32
	written         atomic.Int64
}

func newAttestationStateAdmissionTestStream(
	remote peer.ID,
	input []byte,
) *attestationStateAdmissionTestStream {
	return &attestationStateAdmissionTestStream{
		MockStream:  MockStream{peer: remote, mu: new(sync.Mutex)},
		reader:      bytes.NewReader(input),
		reset:       make(chan struct{}),
		blockedRead: make(chan struct{}),
	}
}

func (s *attestationStateAdmissionTestStream) Read(p []byte) (int, error) {
	if s.panicRead {
		panic("injected state stream read panic")
	}
	if s.blockRead {
		<-s.reset
		return 0, io.ErrClosedPipe
	}
	if s.blockAfterInput && s.reader.Len() == 0 {
		s.blockedOnce.Do(func() { close(s.blockedRead) })
		<-s.reset
		return 0, io.ErrClosedPipe
	}
	n, err := s.reader.Read(p)
	s.readBytes.Add(int64(n))
	return n, err
}

func (s *attestationStateAdmissionTestStream) Write(p []byte) (int, error) {
	s.written.Add(int64(len(p)))
	return len(p), nil
}

func (s *attestationStateAdmissionTestStream) Reset() error {
	s.resetCalls.Add(1)
	s.resetOnce.Do(func() { close(s.reset) })
	return nil
}

func (s *attestationStateAdmissionTestStream) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func framedAttestationStateControl(payload []byte) []byte {
	frame := make([]byte, p2p.LengthHeader+len(payload))
	binary.LittleEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[p2p.LengthHeader:], payload)
	return frame
}

func declaredAttestationStateControl(length uint32) []byte {
	frame := make([]byte, p2p.LengthHeader)
	binary.LittleEndian.PutUint32(frame, length)
	return frame
}

func setAttestationStateActivePeers(ag *AttestationGossip, active []peer.ID) {
	ag.avMu.Lock()
	ag.activeVals = make(map[peer.ID]bool, len(active))
	for _, remote := range active {
		ag.activeVals[remote] = true
	}
	ag.avMu.Unlock()
}

func requireNoAttestationStateAdmissions(t *testing.T, ag *AttestationGossip) {
	t.Helper()
	ag.stateReceiveMu.Lock()
	defer ag.stateReceiveMu.Unlock()
	require.Zero(t, ag.stateReceiveActive)
	require.Empty(t, ag.stateReceiveByPeer)
}

func TestAttestationStateAdmissionRejectsUnauthorizedBeforeFrameRead(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	remote := peer.ID("unauthorized-state-peer")
	stream := newAttestationStateAdmissionTestStream(
		remote,
		declaredAttestationStateControl(p2p.MaxPayload),
	)

	ag.handleStreamAttestationState(stream)

	require.Zero(t, stream.readBytes.Load(), "unauthorized peers must be rejected before the length header or body")
	require.Equal(t, int32(1), stream.resetCalls.Load())
	requireNoAttestationStateAdmissions(t, ag)
}

func TestAttestationStateAdmissionRejectsBusyPeerWithoutConsumingAnotherSlot(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	ag.config.PeerTimeout = time.Second
	remote := peer.ID("busy-active-state-peer")
	setAttestationStateActivePeers(ag, []peer.ID{remote})

	heldStream := newAttestationStateAdmissionTestStream(
		remote,
		framedAttestationStateControl(prefixSendState),
	)
	heldStream.blockAfterInput = true
	heldDone := make(chan struct{})
	go func() {
		ag.handleStreamAttestationState(heldStream)
		close(heldDone)
	}()
	select {
	case <-heldStream.blockedRead:
	case <-time.After(time.Second):
		t.Fatal("first peer stream did not retain admission while serving state")
	}

	rejectedStream := newAttestationStateAdmissionTestStream(
		remote,
		framedAttestationStateControl(prefixSendState),
	)
	ag.handleStreamAttestationState(rejectedStream)

	require.Zero(t, rejectedStream.readBytes.Load(), "a concurrent stream from the same peer must be rejected before frame I/O")
	require.Equal(t, int32(1), rejectedStream.resetCalls.Load())
	ag.stateReceiveMu.Lock()
	require.Equal(t, 1, ag.stateReceiveActive)
	require.Equal(t, 1, ag.stateReceiveByPeer[remote])
	ag.stateReceiveMu.Unlock()

	require.NoError(t, heldStream.Reset())
	select {
	case <-heldDone:
	case <-time.After(time.Second):
		t.Fatal("first peer stream did not release admission after reset")
	}
	reused, ok := ag.acquireAttestationStateAdmission(context.Background(), remote)
	require.True(t, ok, "a rejected stream must not consume an admission slot")
	reused.release()
	requireNoAttestationStateAdmissions(t, ag)
}

func TestAttestationStateAdmissionEnforcesGlobalActivePeerLimit(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	peers := make([]peer.ID, maxConcurrentAttestationStateReceives+1)
	for i := range peers {
		peers[i] = peer.ID("active-state-peer-" + string(rune('a'+i)))
	}
	setAttestationStateActivePeers(ag, peers)

	held := make([]*attestationStateAdmission, 0, maxConcurrentAttestationStateReceives)
	for _, remote := range peers[:maxConcurrentAttestationStateReceives] {
		admission, ok := ag.acquireAttestationStateAdmission(context.Background(), remote)
		require.True(t, ok)
		held = append(held, admission)
	}
	overflowPeer := peers[len(peers)-1]
	stream := newAttestationStateAdmissionTestStream(
		overflowPeer,
		framedAttestationStateControl(append(prefixBatchBegin, stateBatchCount(0)...)),
	)
	ag.handleStreamAttestationState(stream)
	require.Zero(t, stream.readBytes.Load())
	require.Equal(t, int32(1), stream.resetCalls.Load())

	for _, admission := range held {
		admission.release()
	}
	reused, ok := ag.acquireAttestationStateAdmission(context.Background(), overflowPeer)
	require.True(t, ok)
	reused.release()
	requireNoAttestationStateAdmissions(t, ag)
}

func TestAttestationStateAdmissionReleasesMalformedPanickedAndTimedOutStreams(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		ag, _, _, _, _, _ := setupTestGossip(t)
		remote := peer.ID("malformed-state-peer")
		setAttestationStateActivePeers(ag, []peer.ID{remote})
		stream := newAttestationStateAdmissionTestStream(
			remote,
			append(declaredAttestationStateControl(maxAttestationStateControlBytes+1), bytes.Repeat([]byte{1}, 6)...),
		)

		ag.handleStreamAttestationState(stream)

		require.Equal(t, int64(p2p.LengthHeader), stream.readBytes.Load(), "oversized control bodies must not be read")
		require.Equal(t, int32(1), stream.resetCalls.Load())
		requireNoAttestationStateAdmissions(t, ag)
	})

	t.Run("panic", func(t *testing.T) {
		ag, _, _, _, _, _ := setupTestGossip(t)
		remote := peer.ID("panicked-state-peer")
		setAttestationStateActivePeers(ag, []peer.ID{remote})
		stream := newAttestationStateAdmissionTestStream(remote, nil)
		stream.panicRead = true

		require.NotPanics(t, func() { ag.handleStreamAttestationState(stream) })

		require.Equal(t, int32(1), stream.resetCalls.Load())
		requireNoAttestationStateAdmissions(t, ag)
	})

	t.Run("timeout", func(t *testing.T) {
		ag, _, _, _, _, _ := setupTestGossip(t)
		ag.config.PeerTimeout = 30 * time.Millisecond
		remote := peer.ID("timed-out-state-peer")
		setAttestationStateActivePeers(ag, []peer.ID{remote})
		stream := newAttestationStateAdmissionTestStream(remote, nil)
		stream.blockRead = true

		done := make(chan struct{})
		go func() {
			ag.handleStreamAttestationState(stream)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
			t.Fatal("timed-out initial state frame retained its admission slot")
		}

		require.Positive(t, stream.resetCalls.Load())
		requireNoAttestationStateAdmissions(t, ag)
	})

	t.Run("canceled admission", func(t *testing.T) {
		ag, _, _, _, _, _ := setupTestGossip(t)
		remote := peer.ID("canceled-state-peer")
		setAttestationStateActivePeers(ag, []peer.ID{remote})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, ok := ag.acquireAttestationStateAdmission(ctx, remote)
		require.False(t, ok)
		requireNoAttestationStateAdmissions(t, ag)
	})
}

func TestAttestationStateAdmissionHandlesControlFrames(t *testing.T) {
	t.Run("one-byte request", func(t *testing.T) {
		ag, _, _, _, _, _ := setupTestGossip(t)
		remote := peer.ID("active-state-requester")
		setAttestationStateActivePeers(ag, []peer.ID{remote})
		input := append(
			framedAttestationStateControl(prefixSendState),
			framedAttestationStateControl([]byte(p2p.StreamMsgDone))...,
		)
		stream := newAttestationStateAdmissionTestStream(remote, input)

		ag.handleStreamAttestationState(stream)

		require.Equal(t, int32(1), stream.closeCalls.Load())
		require.Zero(t, stream.resetCalls.Load())
		require.Positive(t, stream.written.Load())
		requireNoAttestationStateAdmissions(t, ag)
	})

	t.Run("unsolicited batch begin", func(t *testing.T) {
		ag, _, _, _, _, _ := setupTestGossip(t)
		remote := peer.ID("active-unsolicited-state-sender")
		setAttestationStateActivePeers(ag, []peer.ID{remote})
		stream := newAttestationStateAdmissionTestStream(
			remote,
			framedAttestationStateControl(append(prefixBatchBegin, stateBatchCount(0)...)),
		)

		ag.handleStreamAttestationState(stream)

		require.Zero(t, stream.closeCalls.Load())
		require.Equal(t, int32(1), stream.resetCalls.Load())
		require.Zero(t, stream.written.Load())
		require.Equal(t, int64(p2p.LengthHeader+maxAttestationStateControlBytes), stream.readBytes.Load())
		requireNoAttestationStateAdmissions(t, ag)
	})
}
