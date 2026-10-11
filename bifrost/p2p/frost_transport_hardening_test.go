package p2p

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
		FrostMaxSessionPayload,
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

func encodedFrostBoundaryMessages(t *testing.T, msgType messages.THORChainTSSMessageType, limit int) (*messages.BroadcastMsgChan, *messages.BroadcastMsgChan) {
	t.Helper()
	for msgIDBytes := 2; msgIDBytes < maxTSSFrameMsgIDBytes; msgIDBytes++ {
		msgID := string(bytes.Repeat([]byte{'a'}, msgIDBytes))
		empty := messages.WrappedMessage{MessageType: msgType, MsgID: msgID, Payload: []byte{}}
		base, err := json.Marshal(empty)
		if err != nil {
			t.Fatal(err)
		}
		encodedPayloadBytes := limit - len(base)
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
		if len(encoded) != limit {
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
	for _, limit := range []int{FrostKeygenControlPayload, FrostMaxSessionPayload} {
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
			t.Run(fmt.Sprintf("%s/%d", msgType, limit), func(t *testing.T) {
				atLimit, above := encodedFrostBoundaryMessages(t, msgType, limit)
				expected, err := json.Marshal(atLimit.WrappedMessage)
				if err != nil {
					t.Fatal(err)
				}
				if len(expected) != limit {
					t.Fatalf("encoded frame bytes=%d, want %d", len(expected), limit)
				}
				comm := &Communication{
					BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 1),
					stopChan:         make(chan struct{}),
				}
				below := *atLimit
				below.WrappedMessage.MsgID = atLimit.WrappedMessage.MsgID[:len(atLimit.WrappedMessage.MsgID)-1]
				belowEncoded, err := json.Marshal(below.WrappedMessage)
				if err != nil || len(belowEncoded) != limit-1 {
					t.Fatal("invalid limit-1 fixture")
				}
				if err = comm.QueueFrostBroadcast(below.PeersID, below.WrappedMessage.Payload, below.WrappedMessage.MsgID, msgType, limit); err != nil {
					t.Fatal(err)
				}
				<-comm.BroadcastMsgChan
				if err = comm.QueueFrostBroadcast(atLimit.PeersID, atLimit.WrappedMessage.Payload, atLimit.WrappedMessage.MsgID, msgType, limit); err != nil {
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
				if len(aboveEncoded) != limit+1 {
					t.Fatalf("limit+1 fixture bytes=%d, want %d", len(aboveEncoded), limit+1)
				}
				if err = comm.QueueFrostBroadcast(above.PeersID, above.WrappedMessage.Payload, above.WrappedMessage.MsgID, msgType, limit); err == nil {
					t.Fatal("queue admitted encoded frame at limit+1")
				}
				if len(comm.BroadcastMsgChan) != 0 {
					t.Fatal("rejected encoded frame changed the broadcast queue")
				}
			})
		}
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

func TestQueueFrostBroadcastRejectsInvalidFrameLimit(t *testing.T) {
	comm := &Communication{BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 1), stopChan: make(chan struct{})}
	for _, limit := range []int{-1, 0, FrostMaxSessionPayload + 1} {
		if err := comm.QueueFrostBroadcast(nil, nil, "invalid-limit", messages.TSSFrostVerifyMsg, limit); err == nil {
			t.Fatalf("accepted invalid limit %d", limit)
		}
	}
	if len(comm.BroadcastMsgChan) != 0 {
		t.Fatal("invalid limit admitted a frame")
	}
}

func TestQueueFrostBroadcastRejectsOversizedFrameBeforeBackpressure(t *testing.T) {
	for _, limit := range []int{FrostKeygenControlPayload, FrostMaxSessionPayload} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			_, oversized := encodedFrostBoundaryMessages(t, messages.TSSFrostVerifyMsg, limit)
			comm := &Communication{
				BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 1),
				stopChan:         make(chan struct{}),
			}
			sentinel := &messages.BroadcastMsgChan{}
			comm.BroadcastMsgChan <- sentinel
			done := make(chan error, 1)
			finished := make(chan struct{})
			t.Cleanup(func() {
				close(comm.stopChan)
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("FROST producer did not exit after shutdown")
				}
			})
			go func() {
				defer close(finished)
				done <- comm.QueueFrostBroadcast(oversized.PeersID, oversized.WrappedMessage.Payload,
					oversized.WrappedMessage.MsgID, oversized.WrappedMessage.MessageType, limit)
			}()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "encoded FROST frame") {
					t.Fatalf("oversized frame error = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("oversized frame waited for queue capacity")
			}
			if len(comm.BroadcastMsgChan) != 1 || <-comm.BroadcastMsgChan != sentinel {
				t.Fatal("oversized frame changed the full queue")
			}
		})
	}
}

func TestQueueFrostBroadcastMessageIDBoundary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		msgID string
		valid bool
	}{
		{name: "maximum printable ID", msgID: strings.Repeat("~", maxTSSFrameMsgIDBytes), valid: true},
		{name: "limit plus one", msgID: strings.Repeat("a", maxTSSFrameMsgIDBytes+1)},
		{name: "space", msgID: "session with-space"},
		{name: "control byte", msgID: "session\nforged-line"},
		{name: "DEL byte", msgID: "session\x7f"},
		{name: "non-ASCII", msgID: "session\u00e9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			comm := &Communication{BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 1)}
			err := comm.QueueFrostBroadcast(nil, []byte("payload"), tc.msgID, messages.TSSFrostShareMsg, FrostMaxSessionPayload)
			if !tc.valid {
				if err == nil || len(comm.BroadcastMsgChan) != 0 {
					t.Fatalf("invalid ID admission: error=%v queued=%d", err, len(comm.BroadcastMsgChan))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			queued := <-comm.BroadcastMsgChan
			header, err := decodeTSSFrameHeader(queued.Encoded)
			if err != nil || header.MsgID != tc.msgID {
				t.Fatalf("receiver rejected maximum valid ID: header=%+v error=%v", header, err)
			}
		})
	}
}

func TestFrostEncodedFrameReceiverBoundary(t *testing.T) {
	for _, limit := range []int{FrostKeygenControlPayload, FrostMaxSessionPayload} {
		for _, topic := range []messages.THORChainTSSMessageType{
			messages.TSSFrostPreprocessMsg, messages.TSSFrostShareMsg,
			messages.TSSFrostVerifyMsg, messages.TSSFrostResultMsg,
			messages.TSSFrostDKGRound1Msg, messages.TSSFrostDKGRound2Msg,
			messages.TSSFrostDKGRound3Msg, messages.TSSFrostDKGResultMsg,
		} {
			t.Run(fmt.Sprintf("%s/%d", topic, limit), func(t *testing.T) {
				atLimit, above := encodedFrostBoundaryMessages(t, topic, limit)
				below := *atLimit
				below.WrappedMessage.MsgID = atLimit.WrappedMessage.MsgID[:len(atLimit.WrappedMessage.MsgID)-1]
				for delta, fixture := range []*messages.BroadcastMsgChan{&below, atLimit, above} {
					t.Run(fmt.Sprint(delta-1), func(t *testing.T) {
						frame, err := json.Marshal(fixture.WrappedMessage)
						if err != nil || len(frame) != limit+delta-1 {
							t.Fatal("invalid encoded boundary fixture")
						}
						remote := peer.ID("member-peer")
						msgID := fixture.WrappedMessage.MsgID
						comm := newHandlerCommunication(topic, msgID, []peer.ID{remote})
						comm.SetSubscribeWithPeers(topic, msgID, make(chan *Message, 1), []peer.ID{remote}, limit, 2)
						t.Cleanup(func() { comm.CancelSubscribe(topic, msgID) })
						sub := comm.getSubscriber(topic, msgID)
						stream := framedPreflightStream(t, uint32(len(frame)), frame)
						if delta == 2 {
							stream.maxRead = LengthHeader + TSSFramePreflightBytes
						}
						got, readErr := readStreamWithBufferPreflight(stream, TSSFramePreflightBytes, func(length uint32, prefix []byte) error {
							_, _, preflightErr := comm.preflightTSSFrame(remote, length, prefix)
							return preflightErr
						})
						if delta == 2 {
							if !errors.Is(readErr, ErrTSSFramePreflightRejected) || got != nil {
								t.Fatalf("receiver admitted oversized encoded frame: error=%v", readErr)
							}
							if stream.overread || stream.read != LengthHeader+TSSFramePreflightBytes {
								t.Fatal("oversized encoded frame was read past the preflight prefix")
							}
							if sub.sessionBytes != 0 || len(sub.peerBytes) != 0 || len(sub.peerCounts) != 0 {
								t.Fatal("oversized encoded frame consumed a transport allowance")
							}
							return
						}
						if readErr != nil || !bytes.Equal(got, frame) {
							t.Fatalf("receiver rejected valid encoded boundary frame: %v", readErr)
						}
						if sub.peerCounts[remote] != 1 || sub.peerBytes[remote] != uint64(len(frame)) || sub.sessionBytes != uint64(len(frame)) {
							t.Fatal("receiver did not charge the encoded frame bytes exactly once")
						}
					})
				}
			})
		}
	}
}
