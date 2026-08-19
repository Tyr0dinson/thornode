package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

func frostStreamCount(net network.Network) int {
	count := 0
	for _, conn := range net.Conns() {
		for _, stream := range conn.GetStreams() {
			if stream.Protocol() == TSSProtocolID {
				count++
			}
		}
	}
	return count
}

func TestSuccessfulFrostStreamsFullyCloseOnBothEndpoints(t *testing.T) {
	senderHost, err := libp2p.New(
		context.Background(),
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	if err != nil {
		t.Fatalf("create sender host: %v", err)
	}
	t.Cleanup(func() { _ = senderHost.Close() })

	receiverHost, err := libp2p.New(
		context.Background(),
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	if err != nil {
		t.Fatalf("create receiver host: %v", err)
	}
	t.Cleanup(func() { _ = receiverHost.Close() })

	const frames = 8
	topic := messages.TSSFrostShareMsg
	msgID := "fully-close-successful-frost-streams"
	channel := make(chan *Message, frames)
	receiver := &Communication{
		host:             receiverHost,
		subscribers:      make(map[messages.THORChainTSSMessageType]*MessageIDSubscriber),
		subscriberLocker: &sync.Mutex{},
		streamMgr:        NewStreamMgr(),
		stopChan:         make(chan struct{}),
		frostBudget:      newDefaultFrostIngressBudget(),
		tssReaders:       newTSSReaderLimiter(TSSMaxConcurrentReaders, TSSMaxReadersPerPeer),
	}
	receiver.SetSubscribeWithPeers(
		topic,
		msgID,
		channel,
		[]peer.ID{senderHost.ID()},
		FrostMaxSessionPayload,
		frames,
	)
	receiverHost.SetStreamHandler(TSSProtocolID, receiver.handleStreamTss)
	t.Cleanup(func() {
		close(receiver.stopChan)
		receiver.CancelSubscribe(topic, msgID)
	})

	sender := &Communication{
		host:      senderHost,
		streamMgr: NewStreamMgr(),
		stopChan:  make(chan struct{}),
	}
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelConnect()
	if err = senderHost.Connect(connectCtx, peer.AddrInfo{
		ID:    receiverHost.ID(),
		Addrs: receiverHost.Addrs(),
	}); err != nil {
		t.Fatalf("connect hosts: %v", err)
	}

	frame, err := json.Marshal(messages.WrappedMessage{
		MessageType: topic,
		MsgID:       msgID,
		Payload:     []byte("successful-frost-frame"),
	})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}

	for i := range frames {
		sendResult := make(chan error, 1)
		go func() {
			sendResult <- sender.writeToStream(receiverHost.ID(), frame, msgID, true)
		}()

		var message *Message
		select {
		case message = <-channel:
		case <-time.After(5 * time.Second):
			t.Fatalf("frame %d was not delivered", i)
		}
		acceptResult := make(chan bool, 1)
		go func() { acceptResult <- message.Accept() }()
		select {
		case accepted := <-acceptResult:
			if !accepted {
				t.Fatalf("frame %d was rejected", i)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("frame %d receiver close did not finish", i)
		}
		select {
		case err := <-sendResult:
			if err != nil {
				t.Fatalf("frame %d sender close failed: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("frame %d sender close did not finish", i)
		}

		deadline := time.Now().Add(2 * time.Second)
		for frostStreamCount(senderHost.Network()) != 0 ||
			frostStreamCount(receiverHost.Network()) != 0 {
			if time.Now().After(deadline) {
				t.Fatalf(
					"frame %d leaked streams: sender=%d receiver=%d",
					i,
					frostStreamCount(senderHost.Network()),
					frostStreamCount(receiverHost.Network()),
				)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestFrostStreamTerminalCleanupOwnsStreamOnce(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "party-narrowing"
		if shutdown {
			name = "subscription-shutdown"
		}
		t.Run(name, func(t *testing.T) {
			for iteration := 0; iteration < 100; iteration++ {
				topic := messages.TSSFrostShareMsg
				msgID := fmt.Sprintf("stream-terminal-race-%s-%d", name, iteration)
				remote := peer.ID("terminal-race-peer")
				comm := newTestCommunication()
				comm.streamMgr = NewStreamMgr()
				comm.frostBudget = newDefaultFrostIngressBudget()
				comm.SetSubscribeWithPeers(
					topic,
					msgID,
					make(chan *Message, 1),
					[]peer.ID{remote},
					FrostMaxSessionPayload,
					1,
				)
				subscription := comm.getSubscriber(topic, msgID)
				reservation := subscription.reserve(remote, 1)
				if reservation == nil {
					t.Fatalf("iteration %d: reserve frame", iteration)
				}
				stream := rawHandlerStream(remote, nil)
				reservation.attachStream(stream, comm.streamMgr, msgID)
				if !reservation.parkStream() {
					t.Fatalf("iteration %d: park stream", iteration)
				}

				start := make(chan struct{})
				accepted := make(chan bool, 1)
				cleaned := make(chan bool, 1)
				go func() {
					<-start
					accepted <- reservation.accept()
				}()
				go func() {
					<-start
					if shutdown {
						comm.CancelSubscribe(topic, msgID)
						cleaned <- true
						return
					}
					cleaned <- comm.UpdateSubscribePeers(topic, msgID, nil)
				}()
				close(start)

				wasAccepted := <-accepted
				if !<-cleaned {
					t.Fatalf("iteration %d: terminal cleanup failed", iteration)
				}
				closed := stream.closeCalled.Load()
				reset := stream.resetCalled.Load()
				if closed == reset {
					t.Fatalf(
						"iteration %d: stream terminal actions close=%t reset=%t",
						iteration,
						closed,
						reset,
					)
				}
				if wasAccepted != closed {
					t.Fatalf(
						"iteration %d: accepted=%t but close=%t reset=%t",
						iteration,
						wasAccepted,
						closed,
						reset,
					)
				}
				if got := parkedStreamCount(comm.streamMgr); got != 0 {
					t.Fatalf("iteration %d: terminal stream remained parked: %d", iteration, got)
				}
				if state := snapshotFrostBudget(comm.frostBudget); state.inFlightFrames != 0 {
					t.Fatalf("iteration %d: terminal stream retained an in-flight frame", iteration)
				}

				if !shutdown {
					comm.CancelSubscribe(topic, msgID)
				}
				if state := snapshotFrostBudget(comm.frostBudget); state.nodeBytes != 0 ||
					state.inFlightFrames != 0 || state.activeSessions != 0 {
					t.Fatalf(
						"iteration %d: terminal cleanup leaked budget: %+v",
						iteration,
						state,
					)
				}
			}
		})
	}
}
