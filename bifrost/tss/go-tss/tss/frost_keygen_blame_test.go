package tss

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	chaincommon "gitlab.com/thorchain/thornode/v3/common"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestFrostKeygenSemanticFailuresUseCanonicalBlame(t *testing.T) {
	ensureBech32Prefix()
	members := append([]string(nil), testPubKeys...)
	active, indexes, peers := testFrostKeygenParticipants(t, members)
	localIndex := indexes[normalizePubKey(members[0])]
	senderIndex := indexes[normalizePubKey(members[3])]
	sharesFor := func(sender uint32) map[string]string {
		shares := make(map[string]string)
		for _, index := range indexes {
			if index != sender {
				shares[strconv.FormatUint(uint64(index), 10)] = "share"
			}
		}
		return shares
	}
	invalidTargetShares := sharesFor(senderIndex)
	invalidTargetShares["invalid-index"] = "share"
	duplicateTargetShares := sharesFor(senderIndex)
	duplicateTargetShares["0"+strconv.FormatUint(uint64(localIndex), 10)] = "share"
	emptyExtraShare := sharesFor(senderIndex)
	emptyExtraShare["0"] = ""
	groupKey := strings.Repeat("ab", 32)
	round3 := func(index uint32, address, key string, height int64) FrostDKGRound3Payload {
		return FrostDKGRound3Payload{ParticipantIndex: index, Address: address, GroupKey: key, ObservedMoneroHeight: height}
	}
	cases := []struct {
		name    string
		round   string
		reason  string
		payload interface{}
	}{
		{"round1/index", frostDKGRound1, "participant index mismatch", FrostDKGRound1Payload{Commitments: "commitment"}},
		{"round1/commitments", frostDKGRound1, "empty commitments", FrostDKGRound1Payload{ParticipantIndex: senderIndex}},
		{"round2/index", frostDKGRound2, "participant index mismatch", FrostDKGRound2Payload{EncryptedShares: sharesFor(senderIndex)}},
		{"round2/shares", frostDKGRound2, "empty encrypted shares", FrostDKGRound2Payload{ParticipantIndex: senderIndex}},
		{"round2/missing-target", frostDKGRound2, "missing encrypted share target", FrostDKGRound2Payload{ParticipantIndex: senderIndex, EncryptedShares: map[string]string{"0": "share"}}},
		{"round2/invalid-target", frostDKGRound2, "failed to hash round2 payload", FrostDKGRound2Payload{ParticipantIndex: senderIndex, EncryptedShares: invalidTargetShares}},
		{"round2/duplicate-target", frostDKGRound2, "failed to hash round2 payload", FrostDKGRound2Payload{ParticipantIndex: senderIndex, EncryptedShares: duplicateTargetShares}},
		{"round2/empty-extra-share", frostDKGRound2, "failed to hash round2 payload", FrostDKGRound2Payload{ParticipantIndex: senderIndex, EncryptedShares: emptyExtraShare}},
		{"round3/index", frostDKGRound3, "participant index mismatch", round3(0, "48addr", groupKey, 100)},
		{"round3/empty-address", frostDKGRound3, "empty address/group key", round3(senderIndex, "", groupKey, 100)},
		{"round3/empty-group-key", frostDKGRound3, "empty address/group key", round3(senderIndex, "48addr", "", 100)},
		{"round3/address-mismatch", frostDKGRound3, "DKG address mismatch", round3(senderIndex, "other-address", groupKey, 100)},
		{"round3/group-key-mismatch", frostDKGRound3, "DKG group key mismatch", round3(senderIndex, "48addr", strings.Repeat("cd", 32), 100)},
		{"round3/negative-height", frostDKGRound3, "failed to hash round3 payload", round3(senderIndex, "48addr", groupKey, -1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canonicalReportID := frostKeygenBlameReportID(t, members, buildBlame(tc.reason, tc.round, []string{members[3]}))
			for _, encoding := range []string{"canonical", "uppercase"} {
				t.Run(encoding, func(t *testing.T) {
					claim := members[3]
					if encoding == "uppercase" {
						claim = strings.ToUpper(claim)
					}
					inbox := newFrostKeygenInbox()
					coord := newTestFrostKeygenCoordinator(members[0], "canonical-blame", inbox, time.Second)
					initTestFrostKeygenRoundState(t, coord, active, indexes)
					if err := coord.validateSenderPeerBinding(peers[3], claim); err != nil {
						t.Fatalf("own-key claim must pass peer binding: %v", err)
					}
					var failure *FrostKeygenResponse
					switch remote := tc.payload.(type) {
					case FrostDKGRound1Payload:
						remote.SenderPubKey = claim
						enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound1Msg, coord.msgID, members[3], remote)
						_, failure = coord.exchangeRound1(active, indexes, peers, FrostDKGRound1Payload{
							SenderPubKey: members[0], ParticipantIndex: localIndex, Commitments: "local",
						})
					case FrostDKGRound2Payload:
						remote.SenderPubKey = claim
						enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound2Msg, coord.msgID, members[3], remote)
						_, failure = coord.exchangeRound2(active, indexes, peers, FrostDKGRound2Payload{
							SenderPubKey: members[0], ParticipantIndex: localIndex, EncryptedShares: sharesFor(localIndex),
						})
					case FrostDKGRound3Payload:
						remote.SenderPubKey = claim
						enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound3Msg, coord.msgID, members[3], remote)
						_, _, _, failure = coord.exchangeRound3(active, indexes, peers, FrostDKGRound3Payload{
							SenderPubKey: members[0], ParticipantIndex: localIndex, Address: "48addr", GroupKey: groupKey,
							ObservedMoneroHeight: 100,
						})
					default:
						t.Fatalf("unsupported payload %T", tc.payload)
					}
					if failure == nil || failure.Status != tcommon.Fail {
						t.Fatalf("invalid DKG payload must fail: %+v", failure)
					}
					if failure.Blame.Round != tc.round || !strings.Contains(failure.Blame.FailReason, tc.reason) {
						t.Fatalf("wrong semantic failure: %+v", failure.Blame)
					}
					if len(failure.Blame.BlameNodes) != 1 || failure.Blame.BlameNodes[0].Pubkey != members[3] {
						t.Fatalf("expected exact ceremony member in blame, got %+v", failure.Blame.BlameNodes)
					}
					reportID := frostKeygenBlameReportID(t, members, failure.Blame)
					if reportID != canonicalReportID {
						t.Fatalf("sender encoding changed report ID: %s != %s", reportID, canonicalReportID)
					}
				})
			}
		})
	}
}

// Exercise the actual message constructor: it hashes raw blamed-key strings
// before ValidateBasic checks exact membership and rejects duplicate blame.
func frostKeygenBlameReportID(t *testing.T, members []string, failure blame.Blame) string {
	t.Helper()
	signerKey, err := chaincommon.NewPubKey(members[0])
	if err != nil {
		t.Fatal(err)
	}
	signer, err := signerKey.GetThorAddress()
	if err != nil {
		t.Fatal(err)
	}
	nodes := make([]thorchaintypes.Node, 0, len(failure.BlameNodes))
	for _, node := range failure.BlameNodes {
		nodes = append(nodes, thorchaintypes.Node{Pubkey: node.Pubkey, BlameData: node.BlameData, BlameSignature: node.BlameSignature})
	}
	report, err := thorchaintypes.NewMsgTssPoolV3(
		append([]string(nil), members...), chaincommon.EmptyPubKey, nil, nil,
		thorchaintypes.KeygenType_XMRMonovaultKeygen, 100,
		[]thorchaintypes.Blame{{FailReason: failure.FailReason, Round: failure.Round, IsUnicast: failure.IsUnicast, BlameNodes: nodes}},
		[]string{chaincommon.THORChain.String(), chaincommon.XMRChain.String()}, signer, 1,
		chaincommon.EmptyPubKey, nil, nil, chaincommon.EmptyPubKey, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := report.ValidateBasic(); err != nil {
		t.Fatalf("canonical blame must be a valid TSS pool report: %v", err)
	}
	return report.ID
}

func TestFrostKeygenResultBlameUsesCanonicalMembers(t *testing.T) {
	ensureBech32Prefix()
	members := append([]string(nil), testPubKeys...)
	canonical := buildBlame("invalid payload", frostDKGRound2, []string{members[3]})
	expectedID := frostKeygenBlameReportID(t, members, canonical)
	// Different encodings and duplicate representations must form one result
	// bucket, regardless of which report supplies the adopted response.
	for _, uppercaseFirst := range []bool{false, true} {
		t.Run(strconv.FormatBool(uppercaseFirst), func(t *testing.T) {
			inbox := newFrostKeygenInbox()
			for i := 0; i < 3; i++ {
				reported := canonical
				if (i == 0) == uppercaseFirst {
					reported = buildBlame("invalid payload", frostDKGRound2, []string{strings.ToUpper(members[3]), members[3]})
				}
				enqueueRoundMessage(t, inbox, messages.TSSFrostDKGResultMsg, "result-blame", members[i], FrostDKGResultPayload{
					SenderPubKey: members[i], Response: FrostDKGResult{Status: uint8(tcommon.Fail), Blame: reported},
				})
			}
			coord := newTestFrostKeygenCoordinator(members[0], "result-blame", inbox, 100*time.Millisecond)
			response, err := coord.WaitForResult(members, 3, 100*time.Millisecond)
			if err != nil {
				t.Fatalf("equivalent blame must reach the unchanged quorum: %v", err)
			}
			if response.Status != tcommon.Fail || len(response.Blame.BlameNodes) != 1 || response.Blame.BlameNodes[0].Pubkey != members[3] {
				t.Fatalf("adopted blame must use the exact ceremony member once: %+v", response)
			}
			if id := frostKeygenBlameReportID(t, members, response.Blame); id != expectedID {
				t.Fatalf("adopted blame changed report ID: %s != %s", id, expectedID)
			}
		})
	}
}

func TestFrostKeygenBlameResolutionUsesMemberRepresentationAndDeduplicates(t *testing.T) {
	ensureBech32Prefix()
	for _, member := range []string{testPubKeys[3], strings.ToUpper(testPubKeys[3])} {
		t.Run(member, func(t *testing.T) {
			members := append([]string(nil), testPubKeys...)
			members[3] = member
			active, _, _ := testFrostKeygenParticipants(t, members)
			coord := newTestFrostKeygenCoordinator(members[0], "blame-resolution", newFrostKeygenInbox(), time.Second)
			keys := []string{strings.ToUpper(testPubKeys[3]), testPubKeys[3], " " + testPubKeys[3] + " "}
			response := coord.failForPubKeys(active, "invalid payload", frostDKGRound1, keys)
			if len(response.Blame.BlameNodes) != 1 || response.Blame.BlameNodes[0].Pubkey != member {
				t.Fatalf("resolve to the exact member before deduplication: %+v", response.Blame)
			}
			if keys[0] != strings.ToUpper(testPubKeys[3]) || keys[1] != testPubKeys[3] {
				t.Fatal("blame resolution mutated the caller's keys")
			}
			expected := buildBlame("invalid payload", frostDKGRound1, []string{member})
			if frostKeygenBlameReportID(t, members, response.Blame) != frostKeygenBlameReportID(t, members, expected) {
				t.Fatal("duplicate member encodings changed the report ID")
			}
			// Peer decoding normally returns lowercase. Even binding failures
			// must use the authoritative member representation.
			_, _, peers := testFrostKeygenParticipants(t, members)
			malformed := coord.failForMalformed(active, peers[3], "invalid payload", frostDKGRound1)
			if len(malformed.Blame.BlameNodes) != 1 || malformed.Blame.BlameNodes[0].Pubkey != member {
				t.Fatalf("transport attribution lost the member representation: %+v", malformed.Blame)
			}
		})
	}
}

func TestFrostKeygenResultBlameNormalizationPreservesEvidenceAndInput(t *testing.T) {
	ensureBech32Prefix()
	members := append([]string(nil), testPubKeys...)
	members[3] = strings.ToUpper(members[3])
	active, _, _ := testFrostKeygenParticipants(t, members)
	node := blame.Node{Pubkey: testPubKeys[3], BlameData: []byte("evidence"), BlameSignature: []byte("signature")}
	duplicate := node
	duplicate.Pubkey = members[3]
	input := FrostDKGResult{
		Status: uint8(tcommon.Fail),
		Blame: blame.Blame{
			FailReason: "invalid payload", Round: frostDKGRound2, IsUnicast: true,
			BlameNodes: []blame.Node{node, duplicate},
		},
	}
	response, key, err := normalizeDKGResultPayload(input, active.originalByNormalized)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Blame.BlameNodes) != 1 || response.Blame.BlameNodes[0].Pubkey != members[3] {
		t.Fatalf("incorrect resolved blame: %+v", response.Blame)
	}
	got := response.Blame.BlameNodes[0]
	if string(got.BlameData) != "evidence" || string(got.BlameSignature) != "signature" || !response.Blame.IsUnicast || response.Blame.Round != frostDKGRound2 {
		t.Fatalf("resolution changed blame evidence or metadata: %+v", response.Blame)
	}
	if input.Blame.BlameNodes[0].Pubkey != testPubKeys[3] || len(input.Blame.BlameNodes) != 2 {
		t.Fatal("normalization mutated the original result")
	}
	canonicalInput := FrostDKGResult{Status: uint8(tcommon.Fail), Blame: response.Blame}
	_, canonicalKey, err := normalizeDKGResultPayload(canonicalInput, active.originalByNormalized)
	if err != nil || key != canonicalKey {
		t.Fatalf("equivalent result comparison keys differ: err=%v", err)
	}
	frostKeygenBlameReportID(t, members, response.Blame)
	input.Blame.BlameNodes = make([]blame.Node, frostMaxResultBlameNodes+1)
	if _, _, err := normalizeDKGResultPayload(input, active.originalByNormalized); err == nil {
		t.Fatal("oversized blame must remain invalid")
	}
}

// Keysign diagnostic acceptance must not relax DKG's actionable-only failure
// gossip. A no-node quorum is skipped before the subsequent attributed quorum.
func TestFrostKeygenResultRejectsNoNodeFailures(t *testing.T) {
	ensureBech32Prefix()
	members := testPubKeys[:4]
	active, _, _ := testFrostKeygenParticipants(t, members)
	for _, tc := range []struct {
		name  string
		nodes []blame.Node
	}{
		{"nil", nil},
		{"empty", []blame.Node{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := FrostDKGResult{Status: uint8(tcommon.Fail), Blame: buildBlame("local DKG timeout", frostDKGRound1, nil)}
			local.Blame.BlameNodes = tc.nodes
			if _, _, err := normalizeDKGResultPayload(local, active.originalByNormalized); err == nil ||
				!strings.Contains(err.Error(), "missing blame nodes") {
				t.Errorf("DKG accepted a keysign-only non-actionable diagnostic: %v", err)
			}
			inbox := newFrostKeygenInbox()
			coord := newTestFrostKeygenCoordinator(members[0], "no-node-result", inbox, time.Second)
			attributed := FrostDKGResult{Status: uint8(tcommon.Fail), Blame: buildBlame("invalid payload", frostDKGRound2, []string{members[3]})}
			for _, result := range []FrostDKGResult{local, attributed} {
				for _, sender := range members[:3] {
					enqueueRoundMessage(t, inbox, messages.TSSFrostDKGResultMsg, coord.msgID, sender, FrostDKGResultPayload{
						SenderPubKey: sender, Response: result,
					})
				}
			}
			response, err := coord.WaitForResult(members, 3, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if response.Status != tcommon.Fail || response.Blame.FailReason != attributed.Blame.FailReason ||
				response.Blame.Round != frostDKGRound2 || len(response.Blame.BlameNodes) != 1 ||
				response.Blame.BlameNodes[0].Pubkey != members[3] {
				t.Fatalf("no-node votes displaced the attributed DKG quorum: %+v", response)
			}
			if len(inbox.result) != 0 {
				t.Fatal("DKG waiter did not consume the invalid and attributed quorums")
			}
		})
	}
}
