package tss

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

func TestFrostSenderMatchesSubscriptionFrameLimits(t *testing.T) {
	check := func(t *testing.T, topic messages.THORChainTSSMessageType, limit int, keygen bool) {
		t.Helper()
		for _, delta := range []int{-1, 0, 1} {
			t.Run(fmt.Sprint(delta), func(t *testing.T) {
				wrapped := messages.WrappedMessage{MessageType: topic, MsgID: strings.Repeat("a", 16), Payload: []byte{}}
				empty, err := json.Marshal(wrapped)
				if err != nil {
					t.Fatal(err)
				}
				wrapped.Payload = bytes.Repeat([]byte{1}, 3*((limit-len(empty))/4))
				encoded, err := json.Marshal(wrapped)
				if err != nil {
					t.Fatal(err)
				}
				wrapped.MsgID = strings.Repeat("a", 16+limit-len(encoded)+delta)
				encoded, err = json.Marshal(wrapped)
				if err != nil || len(encoded) != limit+delta {
					t.Fatal("invalid frame fixture")
				}
				comm := &p2p.Communication{BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 1)}
				var broadcastErr error
				peers := []peer.ID{"receiver"}
				if keygen {
					coord := &FrostKeygenCoordinator{p2pCommunication: comm, msgID: wrapped.MsgID}
					broadcastErr = coord.broadcast(topic, wrapped.Payload, peers)
				} else {
					coord := &FrostSignCoordinator{p2pCommunication: comm, msgID: wrapped.MsgID}
					broadcastErr = coord.broadcast(topic, wrapped.Payload, peers)
				}
				if delta > 0 {
					if broadcastErr == nil || len(comm.BroadcastMsgChan) != 0 {
						t.Fatal("sender queued a frame its receiver rejects")
					}
				} else {
					if broadcastErr != nil {
						t.Fatal(broadcastErr)
					}
					if got := <-comm.BroadcastMsgChan; !bytes.Equal(got.Encoded, encoded) {
						t.Fatal("queued frame changed")
					}
				}
			})
		}
	}
	for _, subscription := range newFrostKeygenInbox().subscriptions() {
		t.Run("keygen/"+subscription.msgType.String(), func(t *testing.T) {
			check(t, subscription.msgType, subscription.maxPayloadBytes, true)
		})
	}
	for _, subscription := range newFrostSignInbox().subscriptions() {
		t.Run("keysign/"+subscription.msgType.String(), func(t *testing.T) {
			check(t, subscription.msgType, p2p.FrostMaxSessionPayload, false)
		})
	}
}
