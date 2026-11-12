package tss

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

// fakeFrostDKGEngine is an in-process DKG engine without a chain-height
// provider: the shape of a FROST library engine for a chain whose vault
// records no scanner birthday.
type fakeFrostDKGEngine struct {
	round1Reqs   []FrostSidecarDKGRound1Req
	round2Reqs   []FrostSidecarDKGRound2Req
	round3Reqs   []FrostSidecarDKGRound3Req
	finalizeReqs []FrostSidecarDKGFinalizeReq

	round1Resp   FrostSidecarDKGRound1Resp
	round2Resp   FrostSidecarDKGRound2Resp
	round3Resp   FrostSidecarDKGRound3Resp
	finalizeResp FrostSidecarDKGFinalizeResp
}

var (
	_ FrostDKGEngine              = (*fakeFrostDKGEngine)(nil)
	_ FrostDKGEngine              = (*mockFrostSidecarKeygen)(nil)
	_ FrostDKGChainHeightProvider = (*mockFrostSidecarKeygen)(nil)
)

func (f *fakeFrostDKGEngine) FrostDKGRound1(req FrostSidecarDKGRound1Req) (FrostSidecarDKGRound1Resp, error) {
	f.round1Reqs = append(f.round1Reqs, req)
	return f.round1Resp, nil
}

func (f *fakeFrostDKGEngine) FrostDKGRound2(req FrostSidecarDKGRound2Req) (FrostSidecarDKGRound2Resp, error) {
	f.round2Reqs = append(f.round2Reqs, req)
	return f.round2Resp, nil
}

func (f *fakeFrostDKGEngine) FrostDKGRound3(req FrostSidecarDKGRound3Req) (FrostSidecarDKGRound3Resp, error) {
	f.round3Reqs = append(f.round3Reqs, req)
	return f.round3Resp, nil
}

func (f *fakeFrostDKGEngine) FrostDKGFinalize(req FrostSidecarDKGFinalizeReq) (FrostSidecarDKGFinalizeResp, error) {
	f.finalizeReqs = append(f.finalizeReqs, req)
	return f.finalizeResp, nil
}

func TestFrostKeygenCoordinatorDetectsChainBirthdayMode(t *testing.T) {
	comm := &p2p.Communication{BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 1)}
	if _, ok := FrostDKGEngine(&fakeFrostDKGEngine{}).(FrostDKGChainHeightProvider); ok {
		t.Fatal("fake engine must not expose a chain height provider")
	}

	withoutHeight := NewFrostKeygenCoordinator(comm, &fakeFrostDKGEngine{}, testPubKeys[0], "msg-id", nil, time.Second)
	if !withoutHeight.noChainBirthday {
		t.Fatal("engine without a chain height provider must disable the chain birthday")
	}
	withHeight := NewFrostKeygenCoordinator(comm, &mockFrostSidecarKeygen{}, testPubKeys[0], "msg-id", nil, time.Second)
	if withHeight.noChainBirthday {
		t.Fatal("engine with a chain height provider must keep the chain birthday")
	}
	nilEngine := NewFrostKeygenCoordinator(comm, nil, testPubKeys[0], "msg-id", nil, time.Second)
	if nilEngine.noChainBirthday {
		t.Fatal("nil engine must keep the default birthday path")
	}
}

func TestFrostDKGParticipantsCanonicalOrder(t *testing.T) {
	members := []string{testPubKeys[2], testPubKeys[0], testPubKeys[1]}
	sorted, indexMap, originalByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	participants := frostDKGParticipants(sorted, originalByNormalized)
	if len(participants) != len(members) {
		t.Fatalf("expected %d participants, got %v", len(members), participants)
	}
	for _, member := range members {
		idx := indexMap[normalizePubKey(member)]
		if idx == 0 || participants[idx-1] != member {
			t.Fatalf("participant index %d maps to %q, want %q", idx, participants[idx-1], member)
		}
	}
	for i := 1; i < len(participants); i++ {
		if normalizePubKey(participants[i-1]) >= normalizePubKey(participants[i]) {
			t.Fatalf("participants are not in canonical order: %v", participants)
		}
	}
}

func TestFrostKeygenExecuteSingleSignerWithoutChainBirthday(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0]}
	activeSet, participantIndexMap, activePeerIDs := testFrostKeygenParticipants(t, members)
	localIndex := participantIndexMap[normalizePubKey(testPubKeys[0])]
	groupAddress := "ltc1pvaultaddress"
	groupKey := strings.Repeat("ab", 33)

	engine := &fakeFrostDKGEngine{
		round1Resp:   FrostSidecarDKGRound1Resp{Commitments: "single-commitment"},
		round2Resp:   FrostSidecarDKGRound2Resp{EncryptedShares: map[string]string{}},
		round3Resp:   FrostSidecarDKGRound3Resp{Address: groupAddress, GroupKey: groupKey},
		finalizeResp: FrostSidecarDKGFinalizeResp{Success: true, KeyBytesHex: "abcd"},
	}
	coord := NewFrostKeygenCoordinator(
		&p2p.Communication{BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 64)},
		engine,
		testPubKeys[0],
		"msg-id-engine",
		newFrostKeygenInbox(),
		time.Second,
	)
	resp := coord.Execute(FrostKeygenRequest{
		VaultPubKey:   "thorpub-engine",
		SignerPubKeys: members,
		ExpectedAddr:  groupAddress,
	}, activeSet, activePeerIDs)
	if resp.Status != tcommon.Success {
		t.Fatalf("expected success, got %d with blame %+v", resp.Status, resp.Blame)
	}
	if resp.Address != groupAddress || resp.GroupKey != groupKey || resp.KeyBytesHex != "abcd" {
		t.Fatalf("unexpected keygen response: %+v", resp)
	}
	if resp.ObservedMoneroHeight != 0 {
		t.Fatalf("engine without a chain height provider must report height 0, got %d", resp.ObservedMoneroHeight)
	}
	if len(engine.round1Reqs) != 1 {
		t.Fatalf("expected one round1 call, got %d", len(engine.round1Reqs))
	}
	round1 := engine.round1Reqs[0]
	if round1.ParticipantIndex != localIndex || round1.N != 1 || round1.Threshold != 1 {
		t.Fatalf("unexpected single-signer round1 request: %+v", round1)
	}
	if len(round1.Participants) != 1 || round1.Participants[0] != testPubKeys[0] {
		t.Fatalf("expected the local node as the only participant, got %v", round1.Participants)
	}
	if round1.SessionID != "msg-id-engine" || round1.Context == "" {
		t.Fatalf("round1 request must carry the session id and dkg context: %+v", round1)
	}
	if len(engine.finalizeReqs) != 1 || engine.finalizeReqs[0].GroupKey != groupKey || engine.finalizeReqs[0].Address != groupAddress {
		t.Fatalf("unexpected finalize requests: %+v", engine.finalizeReqs)
	}
}

func TestFrostKeygenRound3WithoutChainBirthdayReturnsZeroHeight(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	activeSet, participantIndexMap, peerIDs := testFrostKeygenParticipants(t, members)
	localIndex := participantIndexMap[normalizePubKey(testPubKeys[0])]
	remoteIndex := participantIndexMap[normalizePubKey(testPubKeys[1])]
	groupAddress := "ltc1pvaultaddress"
	groupKey := strings.Repeat("ab", 33)

	localRound3 := FrostDKGRound3Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: localIndex,
		Address:          groupAddress,
		GroupKey:         groupKey,
	}
	remoteRound3 := FrostDKGRound3Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: remoteIndex,
		Address:          groupAddress,
		GroupKey:         groupKey,
	}
	localHash, err := FrostTranscriptDKGRound3Hash("msg-id", localRound3)
	if err != nil {
		t.Fatalf("local round3 hash: %v", err)
	}
	remoteHash, err := FrostTranscriptDKGRound3Hash("msg-id", remoteRound3)
	if err != nil {
		t.Fatalf("remote round3 hash: %v", err)
	}
	transcript := testFrostKeygenTranscriptHash(t, "msg-id", frostDKGRound3,
		frostKeygenTranscriptEntry{participantIndex: localIndex, payloadHash: localHash},
		frostKeygenTranscriptEntry{participantIndex: remoteIndex, payloadHash: remoteHash},
	)

	run := func(noChainBirthday bool) (int64, *FrostKeygenResponse) {
		inbox := newFrostKeygenInbox()
		enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound3Msg, "msg-id", testPubKeys[1], remoteRound3)
		enqueueVerifyMessage(t, inbox, "msg-id", testPubKeys[1], frostDKGRound3, transcript)
		coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", inbox, time.Second)
		coord.noChainBirthday = noChainBirthday
		initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
		address, key, birthday, failure := coord.exchangeRound3(activeSet, participantIndexMap, peerIDs, localRound3)
		if failure == nil && (address != groupAddress || key != groupKey) {
			t.Fatalf("unexpected round3 result address=%q key=%q", address, key)
		}
		return birthday, failure
	}

	birthday, failure := run(true)
	if failure != nil {
		t.Fatalf("round3 without a chain birthday failed: %+v", failure)
	}
	if birthday != 0 {
		t.Fatalf("expected birthday 0 without a chain height provider, got %d", birthday)
	}

	// The same all-zero transcript on a chain that records a birthday has no
	// positive heights to derive one from and must still fail.
	if _, failure := run(false); failure == nil || !strings.Contains(failure.Blame.FailReason, "birthday") {
		t.Fatalf("expected a birthday derivation failure with zero heights, got %+v", failure)
	}
}

func TestFrostKeygenRound3RejectsChainHeightWithoutChainBirthday(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	activeSet, participantIndexMap, peerIDs := testFrostKeygenParticipants(t, members)
	localIndex := participantIndexMap[normalizePubKey(testPubKeys[0])]
	remoteIndex := participantIndexMap[normalizePubKey(testPubKeys[1])]
	groupAddress := "ltc1pvaultaddress"
	groupKey := strings.Repeat("ab", 33)

	inbox := newFrostKeygenInbox()
	enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound3Msg, "msg-id", testPubKeys[1], FrostDKGRound3Payload{
		SenderPubKey:         testPubKeys[1],
		ParticipantIndex:     remoteIndex,
		Address:              groupAddress,
		GroupKey:             groupKey,
		ObservedMoneroHeight: 455,
	})
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", inbox, time.Second)
	coord.noChainBirthday = true
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
	_, _, _, failure := coord.exchangeRound3(activeSet, participantIndexMap, peerIDs, FrostDKGRound3Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: localIndex,
		Address:          groupAddress,
		GroupKey:         groupKey,
	})
	if failure == nil || failure.Status != tcommon.Fail {
		t.Fatalf("expected a failure for a peer height without a chain birthday, got %+v", failure)
	}
	if len(failure.Blame.BlameNodes) != 1 || failure.Blame.BlameNodes[0].Pubkey != testPubKeys[1] {
		t.Fatalf("expected blame on %s, got %+v", testPubKeys[1], failure.Blame.BlameNodes)
	}

	// A local payload carrying a height is a programming error in the same mode.
	coord = newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", newFrostKeygenInbox(), time.Second)
	coord.noChainBirthday = true
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
	_, _, _, failure = coord.exchangeRound3(activeSet, participantIndexMap, peerIDs, FrostDKGRound3Payload{
		SenderPubKey:         testPubKeys[0],
		ParticipantIndex:     localIndex,
		Address:              groupAddress,
		GroupKey:             groupKey,
		ObservedMoneroHeight: 1,
	})
	if failure == nil || failure.Status != tcommon.Fail {
		t.Fatalf("expected a failure for a local height without a chain birthday, got %+v", failure)
	}
}

func TestNormalizeDKGResultPayloadChainBirthdayModes(t *testing.T) {
	success := func(height int64) FrostDKGResult {
		return FrostDKGResult{
			Address:              "ltc1pvaultaddress",
			GroupKey:             strings.Repeat("AB", 33),
			ObservedMoneroHeight: height,
			Status:               uint8(tcommon.Success),
		}
	}
	cases := []struct {
		name            string
		result          FrostDKGResult
		noChainBirthday bool
		wantErr         bool
	}{
		{name: "birthday chain positive height", result: success(456)},
		{name: "birthday chain zero height", result: success(0), wantErr: true},
		{name: "no birthday zero height", result: success(0), noChainBirthday: true},
		{name: "no birthday positive height", result: success(456), noChainBirthday: true, wantErr: true},
	}
	for _, tc := range cases {
		normalized, key, err := normalizeDKGResultPayload(tc.result, tc.noChainBirthday)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected an error, got %+v", tc.name, normalized)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.name, err)
		}
		if normalized.ObservedMoneroHeight != tc.result.ObservedMoneroHeight {
			t.Fatalf("%s: height %d, want %d", tc.name, normalized.ObservedMoneroHeight, tc.result.ObservedMoneroHeight)
		}
		if normalized.GroupKey != strings.ToLower(tc.result.GroupKey) {
			t.Fatalf("%s: group key %q was not lower-cased", tc.name, normalized.GroupKey)
		}
		wantHeight := `"observed_monero_height":` + strconv.FormatInt(tc.result.ObservedMoneroHeight, 10)
		if !strings.Contains(key, wantHeight) {
			t.Fatalf("%s: comparison key %q does not pin the height", tc.name, key)
		}
	}
}

func TestAdoptQuorumSuccessWithoutChainBirthday(t *testing.T) {
	engine := &fakeFrostDKGEngine{finalizeResp: FrostSidecarDKGFinalizeResp{Success: true, KeyBytesHex: "abcd"}}
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", newFrostKeygenInbox(), time.Second)
	coord.sidecar = engine
	coord.noChainBirthday = true

	req := FrostKeygenRequest{VaultPubKey: "thorpub-engine", SignerPubKeys: []string{testPubKeys[0], testPubKeys[1]}}
	quorum := FrostKeygenResponse{
		Address:  "ltc1pvaultaddress",
		GroupKey: strings.Repeat("ab", 33),
		Status:   tcommon.Success,
	}
	adopted, ok := coord.AdoptQuorumSuccess(req, quorum)
	if !ok {
		t.Fatal("quorum success with height 0 must be adoptable without a chain birthday")
	}
	if adopted.ObservedMoneroHeight != 0 || adopted.KeyBytesHex != "abcd" || adopted.Address != quorum.Address {
		t.Fatalf("unexpected adopted result: %+v", adopted)
	}
	if len(engine.finalizeReqs) != 1 || engine.finalizeReqs[0].GroupKey != quorum.GroupKey {
		t.Fatalf("adoption must finalize against the quorum group key, got %+v", engine.finalizeReqs)
	}

	withHeight := quorum
	withHeight.ObservedMoneroHeight = 456
	if _, ok := coord.AdoptQuorumSuccess(req, withHeight); ok {
		t.Fatal("quorum result with a chain height must not be adopted for a vault without a chain birthday")
	}
	if len(engine.finalizeReqs) != 1 {
		t.Fatal("a rejected adoption must not touch the engine")
	}
}
