package tss

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	cometsecp "github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

// Both standby paths return the waiter response directly. A four-member vault
// needs three signers; its standby member can receive every minimal-party vote.
func TestFrostResultDiagnosticsStandbyMinimalParty(t *testing.T) {
	ensureBech32Prefix()
	party := testPubKeys[:3]
	threshold, err := conversion.GetThreshold(4)
	if err != nil {
		t.Fatal(err)
	}
	if threshold+1 != len(party) {
		t.Fatal("fixture is not a minimal signing party")
	}
	for _, tc := range []struct {
		name   string
		reason string
		round  string
	}{
		{"preprocess timeout", transcriptTimeoutReason("preprocess payloads", []string{party[0]}, []string{party[1]}), frostRoundPreprocess},
		{"MaxGas", FrostFailReasonXMRMaxGasExceeded + ": fee exceeds scheduled cap", frostRoundResult},
		{"local equivocation", frostEquivocationFailReason, frostRoundShare},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgChan := newFrostSignInbox()
			coord := newTestFrostSignCoordinator(testPubKeys[3], "msg-id", msgChan, time.Second)
			coord.logger = zerolog.Nop()
			var failure FrostKeySignResponse
			if tc.name == "local equivocation" {
				failure = detectedFrostResultEquivocation(t, coord)
			} else {
				failure = coord.failForPubKeys(tc.reason, tc.round, nil)
			}
			result := FrostResult{MsgID: coord.msgID, Status: uint8(failure.Status), Blame: failure.Blame, FeeAtomic: 101}
			for _, sender := range party {
				enqueueResultMessage(t, msgChan, coord.msgID, sender, sender, FrostResultPayload{Response: result})
			}
			resp, waitErr := coord.WaitForResult(party, threshold+1, time.Second)
			if waitErr != nil {
				t.Fatal(waitErr)
			}
			if resp.Status != tcommon.Fail || resp.Blame.FailReason != tc.reason || resp.Blame.Round != tc.round || resp.FeeAtomic != 101 {
				t.Fatalf("standby lost the original terminal diagnostic: %+v", resp)
			}
			if len(resp.Blame.BlameNodes) != 0 || resp.releaseSigningClaim {
				t.Fatalf("standby manufactured reporter blame or released a claim: %+v", resp)
			}
		})
	}
}

// Use two verified signed statements and the same detector used by transcript
// rounds, so the no-node failure comes from local equivocation detection.
func detectedFrostResultEquivocation(t *testing.T, coord *FrostSignCoordinator) FrostKeySignResponse {
	t.Helper()
	privateKey := cometsecp.PrivKey([]byte(strings.Repeat("x", 32)))
	id, err := conversion.GetPeerIDFromSecp256PubKey(privateKey.PubKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	pubKey, err := conversion.GetPubKeyFromPeerID(id.String())
	if err != nil {
		t.Fatal(err)
	}
	_, indexes, members, err := buildParticipantIndex([]string{pubKey})
	if err != nil {
		t.Fatal(err)
	}
	active, err := buildActiveSet([]string{pubKey}, members)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := newFrostTranscriptVerifier(frostRoundShare, active, indexes, coord.vaultPubKey, coord.network)
	if err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{testHashOf('a'), testHashOf('b')} {
		envelope, signErr := newFrostSignedEnvelope(privateKey, testHashOf('c'), coord.vaultPubKey, coord.network, frostRoundShare, pubKey, indexes[normalizePubKey(pubKey)], hash)
		if signErr != nil {
			t.Fatal(signErr)
		}
		if verifyErr := verifyFrostSignedEnvelope(envelope); verifyErr != nil {
			t.Fatal(verifyErr)
		}
		evidence, recordErr := verifier.recordSignedEnvelope(envelope)
		if recordErr != nil {
			t.Fatal(recordErr)
		}
		if evidence != nil {
			return coord.failForEquivocation(*evidence)
		}
	}
	t.Fatal("conflicting signed statements did not produce equivocation evidence")
	return FrostKeySignResponse{}
}

func TestFrostResultDiagnosticsBoundsAndCanonicalEmptyBlame(t *testing.T) {
	for _, reason := range []string{"local durability failure", strings.Repeat("a", frostMaxResultReasonBytes)} {
		first := FrostResult{Status: uint8(tcommon.Fail), Blame: buildBlame(" "+reason+" ", " "+frostRoundShare+" ", nil)}
		first.Blame.IsUnicast = true
		resp, key, err := normalizeResultPayload(first, "msg-id", testExpectedPlanHash)
		if err != nil {
			t.Fatal(err)
		}
		second := first
		second.Blame.BlameNodes = []blame.Node{}
		_, secondKey, err := normalizeResultPayload(second, "msg-id", testExpectedPlanHash)
		if err != nil || key != secondKey {
			t.Fatalf("nil and empty blame differ: %v", err)
		}
		if resp.Blame.FailReason != reason || resp.Blame.Round != frostRoundShare || !resp.Blame.IsUnicast || len(resp.Blame.BlameNodes) != 0 {
			t.Fatalf("diagnostic normalization changed semantics: %+v", resp)
		}
	}
}

// Malformed quorums still terminate promptly, but authentication of a reporter
// is not evidence that it caused the underlying signing failure.
func TestFrostResultDiagnosticsMalformedQuorumHasNoReporterBlame(t *testing.T) {
	ensureBech32Prefix()
	valid := FrostResult{MsgID: "msg-id", Status: uint8(tcommon.Fail), Blame: buildBlame("local failure", frostRoundShare, nil)}
	for _, tc := range []struct {
		name   string
		mutate func(*FrostResult)
		reason string
	}{
		{"empty reason", func(r *FrostResult) { r.Blame.FailReason = " " }, "missing blame reason"},
		{"oversized reason", func(r *FrostResult) { r.Blame.FailReason = strings.Repeat("a", frostMaxResultReasonBytes+1) }, "reason exceeds limit"},
		{"invalid status", func(r *FrostResult) { r.Status = 255 }, "invalid frost result status"},
		{"wrong session", func(r *FrostResult) { r.MsgID = "other" }, "msg_id mismatch"},
		{"empty culprit", func(r *FrostResult) { r.Blame.BlameNodes = []blame.Node{{}} }, "empty blame node pubkey"},
		{"too many culprits", func(r *FrostResult) { r.Blame.BlameNodes = make([]blame.Node, frostMaxResultBlameNodes+1) }, "blame nodes exceeds limit"},
		{"oversized evidence", func(r *FrostResult) {
			r.Blame.BlameNodes = []blame.Node{blame.NewNode(testPubKeys[3], make([]byte, frostMaxResultEvidenceBytes+1), nil)}
		}, "evidence exceeds limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := valid
			tc.mutate(&result)
			msgChan := newFrostSignInbox()
			coord := newTestFrostSignCoordinator(testPubKeys[3], "msg-id", msgChan, time.Second)
			coord.logger = zerolog.Nop()
			for _, sender := range testPubKeys[:3] {
				enqueueResultMessage(t, msgChan, coord.msgID, sender, sender, FrostResultPayload{Response: result})
			}
			resp, err := coord.WaitForResult(testPubKeys[:3], 3, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Status != tcommon.Fail || resp.Blame.Round != frostRoundResult ||
				!strings.Contains(resp.Blame.FailReason, tc.reason) || len(resp.Blame.FailReason) > frostMaxResultReasonBytes {
				t.Fatalf("malformed quorum did not terminate with its bounded diagnostic: %+v", resp)
			}
			if len(resp.Blame.BlameNodes) != 0 {
				t.Fatalf("malformed transport reporters became culprits: %+v", resp.Blame.BlameNodes)
			}
		})
	}
}

func TestFrostResultDiagnosticsActionableQuorumUnchanged(t *testing.T) {
	ensureBech32Prefix()
	b := buildBlame("peer failure", frostRoundShare, []string{testPubKeys[3]})
	b.IsUnicast = true
	b.BlameNodes[0].BlameData = []byte{1, 2}
	b.BlameNodes[0].BlameSignature = []byte{3, 4}
	result := FrostResult{MsgID: "msg-id", Status: uint8(tcommon.Fail), Blame: b}
	msgChan := newFrostSignInbox()
	coord := newTestFrostSignCoordinator(testPubKeys[3], "msg-id", msgChan, time.Second)
	for _, sender := range testPubKeys[:3] {
		enqueueResultMessage(t, msgChan, coord.msgID, sender, sender, FrostResultPayload{Response: result})
	}
	resp, err := coord.WaitForResult(testPubKeys[:3], 3, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != tcommon.Fail || !reflect.DeepEqual(resp.Blame, b) {
		t.Fatalf("actionable blame changed: %+v", resp)
	}
}

func TestFrostResultDiagnosticsVotesRemainExclusive(t *testing.T) {
	ensureBech32Prefix()
	first := FrostResult{Status: uint8(tcommon.Fail), Blame: buildBlame("local failure", frostRoundShare, nil)}
	conflict := first
	conflict.Blame.FailReason = "different failure"
	malformed := first
	malformed.Blame.FailReason = ""
	otherMalformed := first
	otherMalformed.Status = 255
	type vote struct {
		sender int
		result FrostResult
	}
	for _, tc := range []struct {
		name  string
		votes []vote
	}{
		{"identical duplicate", []vote{{0, first}, {0, first}, {1, first}}},
		{"conflicting sender disqualified", []vote{{0, first}, {0, conflict}, {0, first}, {1, first}, {2, first}}},
		{"different diagnostics do not match", []vote{{0, first}, {1, conflict}, {2, first}}},
		{"malformed duplicate", []vote{{0, malformed}, {0, malformed}, {1, malformed}}},
		{"malformed replaced by valid", []vote{{0, malformed}, {0, first}, {1, malformed}, {2, malformed}}},
		{"different malformed reasons do not match", []vote{{0, malformed}, {1, otherMalformed}, {2, malformed}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgChan := newFrostSignInbox()
			coord := newTestFrostSignCoordinator(testPubKeys[3], "msg-id", msgChan, time.Second)
			coord.logger = zerolog.Nop()
			// !50's below-quorum paths apply only to verified successes.
			coord.preparedInputs = []FrostSpentOutputRef{{TxHash: testHashOf('a')}}
			coord.scheduledTxOut = true
			coord.verifyAdopted = func(FrostKeySignRequest, []FrostSpentOutputRef, FrostKeySignResponse) error {
				t.Fatal("failure invoked success verification")
				return nil
			}
			for _, v := range tc.votes {
				sender := testPubKeys[v.sender]
				enqueueResultMessage(t, msgChan, coord.msgID, sender, sender, FrostResultPayload{Response: v.result})
			}
			_, err := coord.WaitForResult(testPubKeys[:3], 3, 100*time.Millisecond)
			if !errors.Is(err, errFrostResultTimeout) {
				t.Fatalf("votes manufactured a quorum: %v", err)
			}
			if len(msgChan.result) != 0 {
				t.Fatalf("wait timed out before consuming %d queued reports", len(msgChan.result))
			}
		})
	}
}

// Execute releases a local share in a four-member party (one spare member).
// Three peers' results remain in their dedicated inbox during exchangeShares.
// Exercise result preservation and the actual failure-adoption gate:
// the old code imported false blame at frost_result and replaced frost_share.
func TestFrostResultDiagnosticsActiveSparePartyKeepsLocalExposure(t *testing.T) {
	ensureBech32Prefix()
	party := testPubKeys[:4]
	threshold, err := conversion.GetThreshold(len(party))
	if err != nil {
		t.Fatal(err)
	}
	_, indexes, _, err := buildParticipantIndex(party)
	if err != nil {
		t.Fatal(err)
	}
	peers, err := conversion.GetPeerIDsFromPubKeys(party)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		reason string
	}{
		{"MaxGas", FrostFailReasonXMRMaxGasExceeded},
		{"local equivocation", frostEquivocationFailReason},
		{"malformed", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgID := testHashOf('a')
			msgChan := newFrostSignInbox()
			coord := newTestFrostSignCoordinator(party[0], msgID, msgChan, 100*time.Millisecond)
			coord.logger = zerolog.Nop()
			coord.sidecar = &mockFrostSidecarSign{
				prepareResp: FrostSidecarPrepareResp{SessionID: "session", Preprocess: "aa"},
				partialResp: FrostSidecarPartialResp{SignatureShare: "cc"},
			}
			// Complete preprocess, including every remote transcript attestation.
			for i, sender := range party {
				payload := FrostPreprocessPayload{SenderPubKey: sender, ParticipantIndex: indexes[normalizePubKey(sender)], PreprocessHex: "aa"}
				hash, hashErr := FrostTranscriptPreprocessHash(msgID, payload)
				if hashErr != nil {
					t.Fatal(hashErr)
				}
				if i != 0 {
					enqueueKeySignMessage(t, msgChan, messages.TSSFrostPreprocessMsg, msgID, sender, payload)
				}
				for _, attester := range party[1:] {
					enqueueKeySignMessage(t, msgChan, messages.TSSFrostVerifyMsg, msgID, attester, FrostVerifyPayload{
						SenderPubKey: attester, Round: frostRoundPreprocess, TargetSenderPubKey: sender,
						TargetParticipantIndex: payload.ParticipantIndex, PayloadHash: hash,
					})
				}
			}
			result := FrostResult{MsgID: msgID, Status: uint8(tcommon.Fail), Blame: buildBlame(tc.reason, frostRoundShare, nil)}
			for _, sender := range []string{party[1], party[1], party[2], party[3]} {
				enqueueResultMessage(t, msgChan, msgID, sender, sender, FrostResultPayload{Response: result})
			}
			req := FrostKeySignRequest{VaultPubKey: "thorpub1test-vault", ToAddress: "48abc", AmountAtomic: 1, SignerPubKeys: party}
			local := coord.Execute(req, party, peers)
			if local.Status != tcommon.Fail || local.Blame.Round != frostRoundShare || local.releaseSigningClaim {
				t.Fatalf("local share exposure or sticky claim lost: %+v", local)
			}
			if len(msgChan.result) != 4 {
				t.Fatalf("same-ceremony result inbox lost messages: queued=%d", len(msgChan.result))
			}
			candidate, waitErr := coord.WaitForResult(party, threshold+1, time.Second)
			if waitErr != nil {
				t.Fatal(waitErr)
			}
			if candidate.Status != tcommon.Fail || len(candidate.Blame.BlameNodes) != 0 || shouldAdoptFrostFailureResult(local, candidate) {
				t.Fatalf("late waiter replaced local exposure with reporter blame: local=%+v candidate=%+v", local, candidate)
			}
			got := preserveFrostExposureRound(local, candidate)
			if !reflect.DeepEqual(got, local) || got.releaseSigningClaim {
				t.Fatalf("terminal diagnostic or sticky claim changed: %+v", got)
			}
			if len(msgChan.result) != 0 {
				t.Fatal("queued results were not consumed")
			}
			if _, waitErr = coord.WaitForResult(party, threshold+1, 10*time.Millisecond); !errors.Is(waitErr, errFrostResultTimeout) {
				t.Fatalf("retained results counted twice: %v", waitErr)
			}
		})
	}
}
