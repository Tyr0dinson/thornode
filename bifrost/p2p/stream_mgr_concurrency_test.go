package p2p

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

type terminalCountingStream struct {
	*MockNetworkStream
	resetStarted chan struct{}
	resetGate    <-chan struct{}
	resetOnce    sync.Once
	resetCount   atomic.Int32
	closeCount   atomic.Int32
}

func newTerminalCountingStream() *terminalCountingStream {
	return &terminalCountingStream{MockNetworkStream: NewMockNetworkStream()}
}

func (s *terminalCountingStream) Reset() error {
	s.resetCount.Add(1)
	if s.resetStarted != nil {
		s.resetOnce.Do(func() { close(s.resetStarted) })
	}
	if s.resetGate != nil {
		<-s.resetGate
	}
	return nil
}

func (s *terminalCountingStream) Close() error {
	s.closeCount.Add(1)
	return nil
}

func streamMgrSnapshot(sm *StreamMgr, msgID string) []network.Stream {
	sm.streamLocker.RLock()
	defer sm.streamLocker.RUnlock()
	return append([]network.Stream(nil), sm.unusedStreams[msgID]...)
}

func waitForTestSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func TestStreamMgrReleaseDetachesBeforeReset(t *testing.T) {
	sm := NewStreamMgr()
	msgID := "release-detach-session"
	resetGate := make(chan struct{})
	var resetGateOnce sync.Once
	resetStarted := make(chan struct{})
	oldKnown := newTerminalCountingStream()
	oldKnown.resetStarted = resetStarted
	oldKnown.resetGate = resetGate
	oldUnknown := newTerminalCountingStream()
	newKnown := newTerminalCountingStream()
	newUnknown := newTerminalCountingStream()

	sm.AddStream(msgID, oldKnown)
	sm.AddStream(StreamUnknown, oldUnknown)
	releaseDone := make(chan struct{})
	go func() {
		sm.ReleaseStream(msgID)
		close(releaseDone)
	}()
	t.Cleanup(func() {
		resetGateOnce.Do(func() { close(resetGate) })
		select {
		case <-releaseDone:
		case <-time.After(2 * time.Second):
		}
	})

	waitForTestSignal(t, resetStarted, "detached stream reset")
	addDone := make(chan struct{})
	go func() {
		sm.AddStream(msgID, newKnown)
		sm.AddStream(StreamUnknown, newUnknown)
		close(addDone)
	}()
	waitForTestSignal(t, addDone, "new streams to be added while reset is blocked")

	resetGateOnce.Do(func() { close(resetGate) })
	waitForTestSignal(t, releaseDone, "stream release")

	known := streamMgrSnapshot(sm, msgID)
	if len(known) != 1 || known[0] != newKnown {
		t.Fatalf("new ceremony stream was not retained: %v", known)
	}
	unknown := streamMgrSnapshot(sm, StreamUnknown)
	if len(unknown) != 1 || unknown[0] != newUnknown {
		t.Fatalf("new unknown stream was not retained: %v", unknown)
	}
	if oldKnown.resetCount.Load() != 1 || oldUnknown.resetCount.Load() != 1 {
		t.Fatalf(
			"detached streams reset counts known=%d unknown=%d",
			oldKnown.resetCount.Load(),
			oldUnknown.resetCount.Load(),
		)
	}
	if newKnown.resetCount.Load() != 0 || newUnknown.resetCount.Load() != 0 {
		t.Fatal("streams added after detachment were reset by the old release")
	}

	sm.ReleaseStream(msgID)
	if newKnown.resetCount.Load() != 1 || newUnknown.resetCount.Load() != 1 {
		t.Fatal("new streams were not retained for the next release")
	}
}

func TestStreamMgrReleaseAndRemoveClaimTerminalActionOnce(t *testing.T) {
	for _, key := range []string{"terminal-claim-session", StreamUnknown} {
		t.Run(key, func(t *testing.T) {
			for iteration := 0; iteration < 200; iteration++ {
				sm := NewStreamMgr()
				stream := newTerminalCountingStream()
				sm.AddStream(key, stream)
				releaseID := key
				if key == StreamUnknown {
					releaseID = fmt.Sprintf("unknown-owner-%d", iteration)
				}

				start := make(chan struct{})
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					<-start
					sm.ReleaseStream(releaseID)
				}()
				go func() {
					defer wg.Done()
					<-start
					if sm.RemoveStream(key, stream) {
						_ = stream.Close()
					}
				}()
				close(start)
				wg.Wait()

				terminalActions := stream.resetCount.Load() + stream.closeCount.Load()
				if terminalActions != 1 {
					t.Fatalf(
						"iteration %d: terminal actions reset=%d close=%d",
						iteration,
						stream.resetCount.Load(),
						stream.closeCount.Load(),
					)
				}
				if sm.RemoveStream(key, stream) {
					t.Fatalf("iteration %d: second removal unexpectedly claimed stream", iteration)
				}
			}
		})
	}
}

func TestFrostReservationAndCeremonyReleaseOwnTerminalActionOnce(t *testing.T) {
	for iteration := 0; iteration < 200; iteration++ {
		remote := peer.ID("release-race-peer")
		msgID := fmt.Sprintf("reservation-release-race-%d", iteration)
		comm := newTestCommunication()
		comm.streamMgr = NewStreamMgr()
		comm.frostBudget = newDefaultFrostIngressBudget()
		comm.SetSubscribeWithPeers(
			messages.TSSFrostShareMsg,
			msgID,
			make(chan *Message, 1),
			[]peer.ID{remote},
			FrostMaxSessionPayload,
			1,
		)
		subscription := comm.getSubscriber(messages.TSSFrostShareMsg, msgID)
		reservation := subscription.reserve(remote, 1)
		if reservation == nil {
			t.Fatalf("iteration %d: reserve frame", iteration)
		}
		stream := newTerminalCountingStream()
		reservation.attachStream(stream, comm.streamMgr, msgID)
		if !reservation.parkStream() {
			t.Fatalf("iteration %d: park stream", iteration)
		}

		start := make(chan struct{})
		accepted := make(chan bool, 1)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			accepted <- reservation.accept()
		}()
		go func() {
			defer wg.Done()
			<-start
			comm.streamMgr.ReleaseStream(msgID)
		}()
		close(start)
		wg.Wait()

		if !<-accepted {
			t.Fatalf("iteration %d: valid reservation was not accepted", iteration)
		}
		terminalActions := stream.resetCount.Load() + stream.closeCount.Load()
		if terminalActions != 1 {
			t.Fatalf(
				"iteration %d: terminal actions reset=%d close=%d",
				iteration,
				stream.resetCount.Load(),
				stream.closeCount.Load(),
			)
		}
		if got := parkedStreamCount(comm.streamMgr); got != 0 {
			t.Fatalf("iteration %d: terminal stream remained parked: %d", iteration, got)
		}
		comm.CancelSubscribe(messages.TSSFrostShareMsg, msgID)
	}
}
