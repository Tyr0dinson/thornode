package p2p

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
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

func waitForTestSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

type lifecycleHandlerStream struct {
	*terminalCountingStream
	conn network.Conn
}

func (s *lifecycleHandlerStream) Conn() network.Conn { return s.conn }

func newLifecycleHandlerStream(remote peer.ID, frame []byte) *lifecycleHandlerStream {
	stream := newTerminalCountingStream()
	header := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(header, uint32(len(frame)))
	_, _ = stream.Buffer.Write(header)
	_, _ = stream.Buffer.Write(frame)
	return &lifecycleHandlerStream{
		terminalCountingStream: stream,
		conn:                   fakeTSSConn{remote: remote},
	}
}

func frostTestFrame(t *testing.T, topic messages.THORChainTSSMessageType, msgID string) []byte {
	t.Helper()
	frame, err := json.Marshal(messages.WrappedMessage{
		MessageType: topic,
		MsgID:       msgID,
		Payload:     []byte("frost-lifecycle-payload"),
	})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	return frame
}

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
	senderHost, err := libp2p.New(context.Background(), libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("create sender host: %v", err)
	}
	t.Cleanup(func() { _ = senderHost.Close() })
	receiverHost, err := libp2p.New(context.Background(), libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("create receiver host: %v", err)
	}
	t.Cleanup(func() { _ = receiverHost.Close() })

	const frames = 4
	topic := messages.TSSFrostShareMsg
	msgID := "fully-close-successful-frost-streams"
	channel := make(chan *Message, frames)
	receiver := &Communication{
		host:             receiverHost,
		subscribers:      make(map[messages.THORChainTSSMessageType]*MessageIDSubscriber),
		subscriberLocker: &sync.Mutex{},
		streamMgr:        NewStreamMgr(),
		stopChan:         make(chan struct{}),
	}
	receiver.SetSubscribeWithPeers(topic, msgID, channel, []peer.ID{senderHost.ID()}, FrostMaxSessionPayload, frames)
	receiverHost.SetStreamHandler(TSSProtocolID, receiver.handleStreamTss)
	t.Cleanup(func() { receiver.CancelSubscribe(topic, msgID) })

	sender := &Communication{host: senderHost, streamMgr: NewStreamMgr(), stopChan: make(chan struct{})}
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelConnect()
	if err = senderHost.Connect(connectCtx, peer.AddrInfo{ID: receiverHost.ID(), Addrs: receiverHost.Addrs()}); err != nil {
		t.Fatalf("connect hosts: %v", err)
	}
	frame := frostTestFrame(t, topic, msgID)

	for i := 0; i < frames; i++ {
		sendResult := make(chan error, 1)
		go func() { sendResult <- sender.writeToStream(receiverHost.ID(), frame, msgID, true) }()

		var message *Message
		select {
		case message = <-channel:
		case <-time.After(5 * time.Second):
			t.Fatalf("frame %d was not delivered", i)
		}
		select {
		case err := <-sendResult:
			if err != nil {
				t.Fatalf("frame %d sender close failed: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("frame %d sender waited for receiver acceptance", i)
		}
		if message.PeerID != senderHost.ID() {
			t.Fatalf("frame %d delivered from %s", i, message.PeerID)
		}
		deadline := time.Now().Add(2 * time.Second)
		for frostStreamCount(senderHost.Network()) != 0 || frostStreamCount(receiverHost.Network()) != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("frame %d leaked streams: sender=%d receiver=%d", i, frostStreamCount(senderHost.Network()), frostStreamCount(receiverHost.Network()))
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
