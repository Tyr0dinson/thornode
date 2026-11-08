package tss

import (
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

// A party member released its share, or is still in preprocess, and waits for
// transcript traffic while its peers combine. Their results are broadcast once
// and must remain queued for WaitForResult after the transcript round times out.
func TestFrostWaitForResultAdoptsResultsReceivedDuringTranscriptRounds(t *testing.T) {
	ensureBech32Prefix()
	party := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2], testPubKeys[3]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(party)
	if err != nil {
		t.Fatal(err)
	}
	activeSet, err := buildActiveSet(party, signerByNormalized)
	if err != nil {
		t.Fatal(err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(party)
	if err != nil {
		t.Fatal(err)
	}
	localIndex := participantIndexMap[normalizePubKey(party[0])]

	rounds := map[string]func(c *FrostSignCoordinator) *FrostKeySignResponse{
		"share round": func(c *FrostSignCoordinator) *FrostKeySignResponse {
			_, fail := c.exchangeShares(activeSet, participantIndexMap, peerIDs, FrostSharePayload{
				SenderPubKey: party[0], ParticipantIndex: localIndex, ShareHex: "cc",
			})
			return fail
		},
		"preprocess round": func(c *FrostSignCoordinator) *FrostKeySignResponse {
			_, fail := c.exchangePreprocess(activeSet, participantIndexMap, peerIDs, FrostPreprocessPayload{
				SenderPubKey: party[0], ParticipantIndex: localIndex, PreprocessHex: "aa",
			})
			return fail
		},
	}

	type adoption struct {
		coordinator func(t *testing.T, msgChan *frostSignInbox) *FrostSignCoordinator
		result      func(t *testing.T) FrostResult
		senders     []string
	}
	paths := map[string]adoption{
		// Two results spending the inputs this node prepared.
		"prepared inputs": {
			coordinator: func(t *testing.T, msgChan *frostSignInbox) *FrostSignCoordinator {
				req, prepared, _ := golden832c(t)
				c := newTestFrostSignCoordinator(party[0], "msg-id", msgChan, 30*time.Millisecond)
				c.logger = zerolog.Nop()
				c.sidecar = &knownTxSidecar{}
				c.preparedInputs = prepared
				c.preparedRequest = req
				c.verifyAdopted = func(req FrostKeySignRequest, prepared []FrostSpentOutputRef, resp FrostKeySignResponse) error {
					return verifyFrostTxForVault(req, prepared, resp, golden832cVaultAddress)
				}
				return c
			},
			result: func(t *testing.T) FrostResult {
				_, prepared, genuine := golden832c(t)
				return goldenResult(t, prepared, genuine)
			},
			senders: party[1:3],
		},
		// Threshold results matching the scheduled txout, from a member that
		// holds no prepared inputs.
		"scheduled txout": {
			coordinator: func(t *testing.T, msgChan *frostSignInbox) *FrostSignCoordinator {
				req, _ := golden7969(t)
				req.SignerPubKeys = append([]string{testNonPartyPubKey}, party...)
				c := newTestFrostSignCoordinator(party[0], "msg-id", msgChan, 30*time.Millisecond)
				c.logger = zerolog.Nop()
				c.sidecar = &vaultOutputsSidecar{outputs: golden7969Outputs(t)}
				c.verifyScheduled = func(req FrostKeySignRequest, resp FrostKeySignResponse, outputs []FrostVaultOutput) error {
					return verifyScheduledFrostTxForVault(req, resp, outputs, golden7969VaultAddress)
				}
				if !c.adoptScheduledTxOut(req) {
					t.Fatal("not a source-vault member")
				}
				return c
			},
			result: func(t *testing.T) FrostResult {
				_, genuine := golden7969(t)
				return goldenResult(t, genuine.SpentOutputRefs, genuine)
			},
			senders: party[1:4],
		},
	}

	for roundName, round := range rounds {
		for pathName, path := range paths {
			t.Run(roundName+", "+pathName, func(t *testing.T) {
				msgChan := newFrostSignInbox()
				coord := path.coordinator(t, msgChan)
				initTestFrostSignRoundState(t, coord, activeSet, participantIndexMap)
				result := path.result(t)
				for _, sender := range path.senders {
					enqueueResultMessage(t, msgChan, "msg-id", sender, sender, FrostResultPayload{
						SenderPubKey: sender,
						Response:     result,
					})
				}
				wantRound, wantReason := frostRoundPreprocess, "timeout waiting for preprocess payloads"
				if roundName == "share round" {
					wantRound, wantReason = frostRoundShare, "timeout waiting for share payloads"
				}
				fail := round(coord)
				if fail == nil || fail.Status != tcommon.Fail || fail.Blame.Round != wantRound ||
					!strings.HasPrefix(fail.Blame.FailReason, wantReason) {
					t.Fatalf("transcript round did not reach the expected timeout: %+v", fail)
				}
				if len(msgChan.result) != len(path.senders) {
					t.Fatalf("round retained %d results, want %d", len(msgChan.result), len(path.senders))
				}
				resp, err := coord.WaitForResult(party, 4, 150*time.Millisecond)
				if err != nil {
					t.Fatalf("results received during the round were lost: %v", err)
				}
				if resp.Status != tcommon.Success || resp.TxHash != result.TxHash {
					t.Fatalf("adopted %+v", resp)
				}
			})
		}
	}
}

// Results queued during transcript rounds still pass the result waiter
// authentication, session and equivocation checks.
func TestFrostTranscriptRoundRetainsResultsForValidation(t *testing.T) {
	ensureBech32Prefix()
	party := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(party)
	if err != nil {
		t.Fatal(err)
	}
	activeSet, err := buildActiveSet(party, signerByNormalized)
	if err != nil {
		t.Fatal(err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(party)
	if err != nil {
		t.Fatal(err)
	}
	msgChan := newFrostSignInbox()
	for i := 0; i < 5; i++ {
		enqueueResultMessage(t, msgChan, "msg-id", party[1], party[1], FrostResultPayload{
			Response: mustSuccessResult(t, "msg-id", testExpectedPlanHash, testHashOf(rune('a'+i)), "deadbeef", 10),
		})
	}
	// A result whose sender is not bound to the peer that sent it.
	enqueueResultMessage(t, msgChan, "msg-id", party[1], party[0], FrostResultPayload{})
	// A frame of another session.
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostResultMsg, "other-msg-id", party[1], FrostResultPayload{SenderPubKey: party[1]})

	coord := newTestFrostSignCoordinator(party[0], "msg-id", msgChan, 30*time.Millisecond)
	initTestFrostSignRoundState(t, coord, activeSet, participantIndexMap)
	if fail := func() *FrostKeySignResponse {
		_, fail := coord.exchangeShares(activeSet, participantIndexMap, peerIDs, FrostSharePayload{
			SenderPubKey: party[0], ParticipantIndex: participantIndexMap[normalizePubKey(party[0])], ShareHex: "cc",
		})
		return fail
	}(); fail == nil || fail.Status != tcommon.Fail || fail.Blame.Round != frostRoundShare ||
		!strings.HasPrefix(fail.Blame.FailReason, "timeout waiting for share payloads") {
		t.Fatalf("share round did not reach the expected timeout: %+v", fail)
	}
	if len(msgChan.result) != 7 {
		t.Fatalf("kept %d result frames, want 7", len(msgChan.result))
	}
	if _, err := coord.WaitForResult(party, 2, 30*time.Millisecond); err != errFrostResultTimeout {
		t.Fatalf("conflicting or unauthenticated early results were accepted: %v", err)
	}
}
