package p2p

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

func testFrostBroadcastMessage() *messages.BroadcastMsgChan {
	return &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: messages.TSSFrostShareMsg,
			MsgID:       "queue-backpressure",
			Payload:     []byte("payload"),
		},
		PeersID: []peer.ID{"remote-peer"},
	}
}

func queueTestFrostBroadcast(comm *Communication, msg *messages.BroadcastMsgChan) error {
	return comm.QueueFrostBroadcast(
		msg.PeersID,
		msg.WrappedMessage.Payload,
		msg.WrappedMessage.MsgID,
		msg.WrappedMessage.MessageType,
	)
}

func assertFrostBroadcastBlocked(t *testing.T, done <-chan error) {
	t.Helper()
	// Yield repeatedly so the producer reaches the full channel. Unlike a
	// sleep-based elapsed-time assertion, this does not impose a latency bound.
	for range 100 {
		runtime.Gosched()
		select {
		case err := <-done:
			t.Fatalf("FROST broadcast returned while the queue was full: %v", err)
		default:
		}
	}
}

func encodedFrostBoundaryMessages(t *testing.T, msgType messages.THORChainTSSMessageType) (*messages.BroadcastMsgChan, *messages.BroadcastMsgChan) {
	t.Helper()
	for msgIDBytes := 1; msgIDBytes < maxTSSFrameMsgIDBytes; msgIDBytes++ {
		msgID := string(bytes.Repeat([]byte{'a'}, msgIDBytes))
		empty := messages.WrappedMessage{MessageType: msgType, MsgID: msgID, Payload: []byte{}}
		base, err := json.Marshal(empty)
		if err != nil {
			t.Fatal(err)
		}
		encodedPayloadBytes := FrostMaxSessionPayload - len(base)
		if encodedPayloadBytes < 0 || encodedPayloadBytes%4 != 0 {
			continue
		}
		atLimit := &messages.BroadcastMsgChan{
			WrappedMessage: messages.WrappedMessage{
				MessageType: msgType,
				MsgID:       msgID,
				Payload:     bytes.Repeat([]byte{0x42}, 3*encodedPayloadBytes/4),
			},
			PeersID: []peer.ID{"boundary-peer"},
		}
		encoded, err := json.Marshal(atLimit.WrappedMessage)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) != FrostMaxSessionPayload {
			continue
		}
		above := &messages.BroadcastMsgChan{
			WrappedMessage: atLimit.WrappedMessage,
			PeersID:        append([]peer.ID(nil), atLimit.PeersID...),
		}
		above.WrappedMessage.MsgID += "b"
		return atLimit, above
	}
	t.Fatal("could not construct exact encoded FROST frame boundary")
	return nil, nil
}

func TestQueueFrostBroadcastEncodedFrameBoundary(t *testing.T) {
	for _, msgType := range []messages.THORChainTSSMessageType{
		messages.TSSFrostPreprocessMsg,
		messages.TSSFrostShareMsg,
		messages.TSSFrostVerifyMsg,
		messages.TSSFrostResultMsg,
		messages.TSSFrostDKGRound1Msg,
		messages.TSSFrostDKGRound2Msg,
		messages.TSSFrostDKGRound3Msg,
		messages.TSSFrostDKGResultMsg,
	} {
		t.Run(msgType.String(), func(t *testing.T) {
			atLimit, above := encodedFrostBoundaryMessages(t, msgType)
			expected, err := json.Marshal(atLimit.WrappedMessage)
			if err != nil {
				t.Fatal(err)
			}
			if len(expected) != FrostMaxSessionPayload {
				t.Fatalf("encoded frame bytes=%d, want %d", len(expected), FrostMaxSessionPayload)
			}
			comm := &Communication{
				BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 1),
				stopChan:         make(chan struct{}),
			}
			if err = queueTestFrostBroadcast(comm, atLimit); err != nil {
				t.Fatalf("queue encoded frame at limit: %v", err)
			}
			queued := <-comm.BroadcastMsgChan
			if queued.Encoded == nil || !bytes.Equal(queued.Encoded, expected) {
				t.Fatal("queued frame does not contain the exact serialized bytes")
			}
			aboveEncoded, err := json.Marshal(above.WrappedMessage)
			if err != nil {
				t.Fatal(err)
			}
			if len(aboveEncoded) != FrostMaxSessionPayload+1 {
				t.Fatalf("limit+1 fixture bytes=%d, want %d", len(aboveEncoded), FrostMaxSessionPayload+1)
			}
			if err = queueTestFrostBroadcast(comm, above); err == nil {
				t.Fatal("queue admitted encoded frame at limit+1")
			}
			if len(comm.BroadcastMsgChan) != 0 {
				t.Fatal("rejected encoded frame changed the broadcast queue")
			}
		})
	}
}

func TestQueueFrostBroadcastOwnsPayloadAndPeers(t *testing.T) {
	comm := &Communication{
		BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 1),
		stopChan:         make(chan struct{}),
	}
	msg := &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: messages.TSSFrostShareMsg,
			MsgID:       "owned-queue-item",
			Payload:     []byte("immutable-payload"),
		},
		PeersID: []peer.ID{"first-peer", "second-peer"},
	}
	expected, err := json.Marshal(msg.WrappedMessage)
	if err != nil {
		t.Fatal(err)
	}
	if err := queueTestFrostBroadcast(comm, msg); err != nil {
		t.Fatalf("queue FROST broadcast: %v", err)
	}
	msg.WrappedMessage.Payload[0] ^= 0xff
	msg.PeersID[0] = "mutated-peer"

	queued := <-comm.BroadcastMsgChan
	if queued.Encoded == nil || !bytes.Equal(queued.Encoded, expected) {
		t.Fatal("queued FROST broadcast did not retain its serialized payload snapshot")
	}
	if len(queued.WrappedMessage.Payload) != 0 {
		t.Fatal("queued FROST broadcast retained a redundant raw payload")
	}
	var wrapped messages.WrappedMessage
	if err := json.Unmarshal(queued.Encoded, &wrapped); err != nil {
		t.Fatal(err)
	}
	if queued.PeersID[0] != "first-peer" || string(wrapped.Payload) != "immutable-payload" {
		t.Fatal("queued FROST broadcast retained caller-owned payload or peers")
	}
	if &queued.PeersID[0] == &msg.PeersID[0] {
		t.Fatal("queued FROST broadcast shares caller-owned peer backing storage")
	}
}

func TestQueueFrostBroadcastRejectsInvalidAdmission(t *testing.T) {
	comm := &Communication{
		BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 1),
		stopChan:         make(chan struct{}),
	}
	for _, msg := range []*messages.BroadcastMsgChan{
		{WrappedMessage: messages.WrappedMessage{MessageType: messages.TSSKeyGenMsg, MsgID: "not-frost"}},
		{WrappedMessage: messages.WrappedMessage{MessageType: messages.TSSFrostShareMsg}},
	} {
		if err := queueTestFrostBroadcast(comm, msg); err == nil {
			t.Fatalf("invalid FROST admission was accepted: %+v", msg.WrappedMessage)
		}
	}
	if len(comm.BroadcastMsgChan) != 0 {
		t.Fatal("invalid FROST admission changed the broadcast queue")
	}
}

func TestQueueFrostBroadcastRejectsMissingQueue(t *testing.T) {
	comm := &Communication{stopChan: make(chan struct{})}
	if err := queueTestFrostBroadcast(comm, testFrostBroadcastMessage()); err == nil {
		t.Fatal("FROST broadcast with no queue returned no error")
	}
}

func TestQueueFrostBroadcastBackpressuresUntilCapacity(t *testing.T) {
	comm := &Communication{
		BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 1),
		stopChan:         make(chan struct{}),
	}
	sentinel := &messages.BroadcastMsgChan{}
	comm.BroadcastMsgChan <- sentinel
	done := make(chan error, 1)
	go func() {
		done <- queueTestFrostBroadcast(comm, testFrostBroadcastMessage())
	}()
	assertFrostBroadcastBlocked(t, done)

	if got := <-comm.BroadcastMsgChan; got != sentinel {
		t.Fatal("queue head changed while the FROST producer was blocked")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("queue FROST broadcast after capacity became available: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FROST broadcast did not complete after capacity became available")
	}
	select {
	case queued := <-comm.BroadcastMsgChan:
		if queued.Encoded == nil {
			t.Fatal("unblocked FROST broadcast did not enqueue encoded wire bytes")
		}
	case <-time.After(time.Second):
		t.Fatal("completed FROST broadcast did not enqueue its work")
	}
}

func TestQueueFrostBroadcastStopReleasesBlockedProducer(t *testing.T) {
	comm := &Communication{
		BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 1),
		stopChan:         make(chan struct{}),
	}
	sentinel := &messages.BroadcastMsgChan{}
	comm.BroadcastMsgChan <- sentinel
	done := make(chan error, 1)
	go func() {
		done <- queueTestFrostBroadcast(comm, testFrostBroadcastMessage())
	}()
	assertFrostBroadcastBlocked(t, done)

	close(comm.stopChan)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "stopped") {
			t.Fatalf("blocked FROST broadcast stop error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stopping communication did not release the blocked FROST producer")
	}
	if got := <-comm.BroadcastMsgChan; got != sentinel {
		t.Fatal("stopped FROST producer changed the full queue")
	}
}
