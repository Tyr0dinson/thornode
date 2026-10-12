package tss

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/common"
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

func assertFrostEncodedFrameFits(t *testing.T, encodedBytes, limit int) {
	t.Helper()
	if encodedBytes <= 0 || encodedBytes > limit {
		t.Fatalf("encoded frame bytes=%d, want 1..%d", encodedBytes, limit)
	}
	t.Logf("encoded_bytes=%d limit=%d remaining_bytes=%d headroom=%.2fx", encodedBytes, limit, limit-encodedBytes, float64(limit)/float64(encodedBytes))
}

func TestFrostResultFrameHeadroom(t *testing.T) {
	ensureBech32Prefix()
	const partySize = 128
	_, localPubKey := newTestFrostSigner(t)
	remotePubKeys := make([]string, partySize-1)
	for i := range remotePubKeys {
		_, remotePubKeys[i] = newTestFrostSigner(t)
	}
	msgID := testHashOf('f')
	// buildBlame is the local failure producer: public keys only, no evidence
	// blobs. '<' exercises JSON's largest escaping expansion at the reason cap.
	reason := strings.Repeat("<", frostMaxResultReasonBytes)
	keygen := newTestFrostKeygenCoordinator(localPubKey, msgID, newFrostKeygenInbox(), 0)
	keysign := newTestFrostSignCoordinator(localPubKey, msgID, newFrostSignInbox(), 0)

	t.Run("dkg_success", func(t *testing.T) {
		response := FrostKeygenResponse{
			Address:              "48cNbRvGyrb43yGQYLWKxshBnnq8nRkFfPcUwfqpihwJWiSwHb7gy6p6oAXAxDJXbPbGME2xWBgFdSUzrxhiKLiqGbm9KqB",
			GroupKey:             testHashOf('a'),
			KeyBytesHex:          "local-only-key-share",
			ObservedMoneroHeight: 3_000_000,
			Status:               tcommon.Success,
		}
		if err := keygen.BroadcastResult(response, nil); err != nil {
			t.Fatal(err)
		}
		queued := <-keygen.p2pCommunication.BroadcastMsgChan
		assertFrostEncodedFrameFits(t, len(queued.Encoded), p2p.FrostKeygenControlPayload)
		payload := queuedFrostPayload(t, queued)
		if strings.Contains(string(payload), response.KeyBytesHex) || strings.Contains(string(payload), "key_bytes_hex") {
			t.Fatal("DKG result gossiped the local key share")
		}
		var result FrostDKGResultPayload
		if err := json.Unmarshal(payload, &result); err != nil {
			t.Fatal(err)
		}
		normalized, _, err := normalizeDKGResultPayload(result.Response)
		if err != nil || normalized.Address != response.Address || normalized.GroupKey != response.GroupKey || normalized.ObservedMoneroHeight != response.ObservedMoneroHeight {
			t.Fatalf("DKG success fields lost or rejected: %+v, %v", normalized, err)
		}
	})

	t.Run("dkg_full_party_failure", func(t *testing.T) {
		response := keygen.failForPubKeys(reason, frostDKGRound3, remotePubKeys)
		if err := keygen.BroadcastResult(response, nil); err != nil {
			t.Fatal(err)
		}
		queued := <-keygen.p2pCommunication.BroadcastMsgChan
		assertFrostEncodedFrameFits(t, len(queued.Encoded), p2p.FrostKeygenControlPayload)
		var result FrostDKGResultPayload
		if err := json.Unmarshal(queuedFrostPayload(t, queued), &result); err != nil {
			t.Fatal(err)
		}
		normalized, _, err := normalizeDKGResultPayload(result.Response)
		if err != nil || normalized.Blame.FailReason != reason || len(normalized.Blame.BlameNodes) != len(remotePubKeys) {
			t.Fatalf("full-party DKG failure lost or rejected: nodes=%d, %v", len(normalized.Blame.BlameNodes), err)
		}
	})

	t.Run("keysign_success_upper_size_model", func(t *testing.T) {
		// The signer's pinned monero-wallet (devops/serai 6309206) rejects
		// transaction weight >= (300000/2)-600; serialized bytes <= weight.
		// This synthetic hex models that conservative upper size, not a valid
		// signed transaction. Bifrost maxInputsFromMimir clamps inputs to 32,
		// unlike the result decoder's larger independent defensive field caps.
		const maxSignedTxBytes = (300_000 / 2) - 600 - 1
		result := mustSuccessResult(t, msgID, testExpectedPlanHash, testHashOf('e'), strings.Repeat("ab", maxSignedTxBytes), 10)
		result.SpentOutputRefs = make([]FrostSpentOutputRef, common.MaxXMRSpentOutputRefs)
		for i := range result.SpentOutputRefs {
			result.SpentOutputRefs[i] = FrostSpentOutputRef{
				TxHash:      fmt.Sprintf("%064x", i+1),
				OutputIndex: 1,
				KeyImage:    fmt.Sprintf("%064x", i+100),
				SpendTxHash: result.TxHash,
			}
		}
		response := FrostKeySignResponse{
			MsgID: result.MsgID, TxHash: result.TxHash, TxHex: result.TxHex,
			TxKey: result.TxKey, FeeAtomic: result.FeeAtomic, Eventuality: result.Eventuality,
			SpentOutputRefs: result.SpentOutputRefs, Status: tcommon.Success,
		}
		if err := keysign.BroadcastResult(response, nil); err != nil {
			t.Fatal(err)
		}
		queued := <-keysign.p2pCommunication.BroadcastMsgChan
		assertFrostEncodedFrameFits(t, len(queued.Encoded), p2p.FrostMaxSessionPayload)
		var payload FrostResultPayload
		if err := json.Unmarshal(queuedFrostPayload(t, queued), &payload); err != nil {
			t.Fatal(err)
		}
		normalized, _, err := normalizeResultPayload(payload.Response, msgID, testExpectedPlanHash)
		if err != nil || normalized.TxHex != response.TxHex || !reflect.DeepEqual(normalized.SpentOutputRefs, response.SpentOutputRefs) {
			t.Fatalf("keysign success data lost or rejected: tx_hex_chars=%d refs=%d, %v", len(normalized.TxHex), len(normalized.SpentOutputRefs), err)
		}
	})

	t.Run("keysign_full_party_failure", func(t *testing.T) {
		response := keysign.failForPubKeys(reason, frostRoundPreprocess, remotePubKeys)
		if err := keysign.BroadcastResult(response, nil); err != nil {
			t.Fatal(err)
		}
		queued := <-keysign.p2pCommunication.BroadcastMsgChan
		assertFrostEncodedFrameFits(t, len(queued.Encoded), p2p.FrostMaxSessionPayload)
		var result FrostResultPayload
		if err := json.Unmarshal(queuedFrostPayload(t, queued), &result); err != nil {
			t.Fatal(err)
		}
		normalized, _, err := normalizeResultPayload(result.Response, msgID, testExpectedPlanHash)
		if err != nil || normalized.Blame.FailReason != reason || len(normalized.Blame.BlameNodes) != len(remotePubKeys) {
			t.Fatalf("full-party keysign failure lost or rejected: nodes=%d, %v", len(normalized.Blame.BlameNodes), err)
		}
	})

	t.Run("reason_limit_plus_one_is_semantically_invalid", func(t *testing.T) {
		oversizedBlame := buildBlame(reason+"<", frostDKGRound3, remotePubKeys)
		if _, _, err := normalizeDKGResultPayload(FrostDKGResult{Status: uint8(tcommon.Fail), Blame: oversizedBlame}); err == nil {
			t.Fatal("DKG accepted a reason beyond the semantic cap")
		}
		if _, _, err := normalizeResultPayload(FrostResult{MsgID: msgID, Status: uint8(tcommon.Fail), Blame: oversizedBlame}, msgID, testExpectedPlanHash); err == nil {
			t.Fatal("keysign accepted a reason beyond the semantic cap")
		}
	})
}
