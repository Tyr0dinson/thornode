package tss

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

// One member makes an honest receiver abort safely; others only observe its
// later absence. Missing participation must not become culpability.
func TestFrostKeygenSelectiveFailureDoesNotBlameMissingVictim(t *testing.T) {
	ensureBech32Prefix()
	members := append([]string(nil), testPubKeys...)
	active, indexes, peers := testFrostKeygenParticipants(t, members)
	payloads := make([]FrostDKGRound1Payload, len(members))
	entries := make([]frostKeygenTranscriptEntry, 0, len(members))
	for i, pk := range members {
		payloads[i] = FrostDKGRound1Payload{
			SenderPubKey: pk, ParticipantIndex: indexes[normalizePubKey(pk)], Commitments: "commitment",
		}
		hash, err := FrostTranscriptDKGRound1Hash("selective-payload", payloads[i])
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, frostKeygenTranscriptEntry{participantIndex: payloads[i].ParticipantIndex, payloadHash: hash})
	}
	transcriptHash := testFrostKeygenTranscriptHash(t, "selective-payload", frostDKGRound1, entries...)
	victimInbox := newFrostKeygenInbox()
	bad := payloads[3]
	bad.Commitments = ""
	enqueueRoundMessage(t, victimInbox, messages.TSSFrostDKGRound1Msg, "selective-payload", members[3], bad)
	victim := newTestFrostKeygenCoordinator(members[0], "selective-payload", victimInbox, time.Second)
	initTestFrostKeygenRoundState(t, victim, active, indexes)
	_, victimFailure := victim.exchangeRound1(active, indexes, peers, payloads[0])
	if victimFailure == nil || victimFailure.Status != tcommon.Fail ||
		len(victimFailure.Blame.BlameNodes) != 1 || victimFailure.Blame.BlameNodes[0].Pubkey != members[3] {
		t.Fatalf("victim must blame actual invalid-payload sender: %+v", victimFailure)
	}
	if len(victim.p2pCommunication.BroadcastMsgChan) != 1 {
		t.Fatal("victim must have broadcast its valid round1 payload before failing")
	}
	sharesFor := func(sender int) map[string]string {
		shares := make(map[string]string)
		for _, index := range indexes {
			if index != indexes[normalizePubKey(members[sender])] {
				shares[strconv.FormatUint(uint64(index), 10)] = "share"
			}
		}
		return shares
	}
	for _, receiver := range []int{1, 2} {
		inbox := newFrostKeygenInbox()
		var logs bytes.Buffer
		coord := newTestFrostKeygenCoordinator(members[receiver], "selective-payload", inbox, 250*time.Millisecond)
		coord.logger = zerolog.New(&logs)
		initTestFrostKeygenRoundState(t, coord, active, indexes)
		for i, payload := range payloads {
			if i != receiver {
				enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound1Msg, "selective-payload", members[i], payload)
			}
		}
		for _, signer := range []int{1, 2, 3} {
			if signer != receiver {
				enqueueVerifyMessage(t, inbox, "selective-payload", members[signer], frostDKGRound1, transcriptHash)
			}
		}
		if _, failure := coord.exchangeRound1(active, indexes, peers, payloads[receiver]); failure != nil {
			t.Fatalf("receiver %d must complete round1 without victim's vote: %+v", receiver, failure)
		}
		for _, sender := range []int{1, 2, 3} {
			if sender == receiver {
				continue
			}

			enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound2Msg, "selective-payload", members[sender], FrostDKGRound2Payload{
				SenderPubKey: members[sender], ParticipantIndex: indexes[normalizePubKey(members[sender])], EncryptedShares: sharesFor(sender),
			})
		}
		_, failure := coord.exchangeRound2(active, indexes, peers, FrostDKGRound2Payload{
			SenderPubKey: members[receiver], ParticipantIndex: indexes[normalizePubKey(members[receiver])],
			EncryptedShares: sharesFor(receiver),
		})
		if failure == nil || !strings.Contains(failure.Blame.FailReason, "timeout") ||
			len(failure.Blame.BlameNodes) != 0 {
			t.Fatalf("receiver %d must diagnose timeout without blaming missing victim: %+v", receiver, failure)
		}
		var diagnostic map[string]json.RawMessage
		if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &diagnostic); err != nil {
			t.Fatal(err)
		}
		var missing []string
		if err := json.Unmarshal(diagnostic["missing_payload_pubkeys"], &missing); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(missing, []string{members[0]}) {
			t.Fatalf("missing participant diagnostic = %v, want victim %s", missing, members[0])
		}
	}
	t.Log("invalid sender remains attributable; downstream missing victim is diagnostic only")
}

// A timeout can contain several kinds of evidence at once. Preserve their
// identities independently; only locally witnessed equivocation is attributed.
func TestFrostKeygenTimeoutSeparatesVerificationDiagnostics(t *testing.T) {
	ensureBech32Prefix()
	members := testPubKeys[:4]
	active, indexes, peers := testFrostKeygenParticipants(t, members)
	inbox := newFrostKeygenInbox()
	coord := newTestFrostKeygenCoordinator(members[0], "timeout-categories", inbox, 250*time.Millisecond)
	var logs bytes.Buffer
	coord.logger = zerolog.New(&logs)
	initTestFrostKeygenRoundState(t, coord, active, indexes)
	payloads := make([]FrostDKGRound3Payload, len(members))
	for i, member := range members {
		payloads[i] = FrostDKGRound3Payload{
			SenderPubKey: member, ParticipantIndex: indexes[normalizePubKey(member)],
			Address: "48addr", GroupKey: strings.Repeat("ab", 32), ObservedMoneroHeight: 456,
		}
		if i != 0 {
			enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound3Msg, coord.msgID, member, payloads[i])
		}
	}
	enqueueVerifyMessage(t, inbox, coord.msgID, members[2], frostDKGRound3, strings.Repeat("a", 64))
	enqueueVerifyMessage(t, inbox, coord.msgID, members[3], frostDKGRound3, strings.Repeat("b", 64))
	enqueueVerifyMessage(t, inbox, coord.msgID, members[3], frostDKGRound3, strings.Repeat("c", 64))
	_, _, _, failure := coord.exchangeRound3(active, indexes, peers, payloads[0])
	if failure == nil || len(failure.Blame.BlameNodes) != 1 || failure.Blame.BlameNodes[0].Pubkey != members[3] {
		t.Fatalf("only the equivocator may be attributed, got %+v", failure)
	}
	var diagnostic map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &diagnostic); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		field   string
		members []string
	}{
		{"missing_payload_pubkeys", nil},
		{"missing_verification_pubkeys", []string{members[1]}},
		{"conflicting_verification_pubkeys", []string{members[2]}},
		{"equivocating_verification_pubkeys", []string{members[3]}},
	} {
		var got []string
		if err := json.Unmarshal(diagnostic[want.field], &got); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want.members) {
			t.Fatalf("%s = %v, want %v", want.field, got, want.members)
		}
	}
}

// Failures without attributed nodes cannot enter result quorum buckets. Keep
// those local diagnostics off the wire while preserving usable result gossip.
func TestFrostKeygenBroadcastResultSkipsUnattributedFailures(t *testing.T) {
	ensureBech32Prefix()
	active, _, peers := testFrostKeygenParticipants(t, testPubKeys)
	for _, tc := range []struct {
		name     string
		response FrostKeygenResponse
		wantSend bool
	}{
		{
			name: "unattributed failure",
			response: FrostKeygenResponse{
				Status: tcommon.Fail,
				Blame:  buildBlame("timeout", frostDKGRound1, nil),
			},
		},
		{
			name: "attributed failure",
			response: FrostKeygenResponse{
				Status: tcommon.Fail,
				Blame:  buildBlame("invalid payload", frostDKGRound1, testPubKeys[1:2]),
			},
			wantSend: true,
		},
		{
			name: "success without blame",
			response: FrostKeygenResponse{
				Status: tcommon.Success, Address: "48addr",
				GroupKey: strings.Repeat("ab", 32), ObservedMoneroHeight: 456,
			},
			wantSend: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			coord := newTestFrostKeygenCoordinator(testPubKeys[0], "result-broadcast", newFrostKeygenInbox(), time.Second)
			if err := coord.BroadcastResult(tc.response, peers); err != nil {
				t.Fatal(err)
			}
			if !tc.wantSend {
				if got := len(coord.p2pCommunication.BroadcastMsgChan); got != 0 {
					t.Fatalf("unattributed failure queued %d broadcasts, want none", got)
				}
				return
			}
			if got := len(coord.p2pCommunication.BroadcastMsgChan); got != 1 {
				t.Fatalf("usable result queued %d broadcasts, want one", got)
			}
			msg := <-coord.p2pCommunication.BroadcastMsgChan
			if msg.WrappedMessage.MessageType != messages.TSSFrostDKGResultMsg ||
				msg.WrappedMessage.MsgID != coord.msgID || !slices.Equal(msg.PeersID, peers) {
				t.Fatalf("incorrect result routing: %+v", msg)
			}
			var payload FrostDKGResultPayload
			if err := json.Unmarshal(msg.WrappedMessage.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.SenderPubKey != coord.localPubKey {
				t.Fatalf("result sender = %s, want %s", payload.SenderPubKey, coord.localPubKey)
			}
			normalized, _, err := normalizeDKGResultPayload(payload.Response, active.originalByNormalized)
			if err != nil || normalized.Status != tc.response.Status {
				t.Fatalf("broadcast result must remain usable: %+v, %v", normalized, err)
			}
		})
	}
}
