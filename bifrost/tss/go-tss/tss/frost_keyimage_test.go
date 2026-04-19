package tss

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

type mockFrostSidecarKeyImage struct {
	prepareReqs  []FrostSidecarKeyImagePrepareReq
	partialReqs  []FrostSidecarKeyImagePartialReq
	combineReqs  []FrostSidecarKeyImageCombineReq
	validateReqs []FrostSidecarKeyImageValidateReq

	prepareResp  FrostSidecarKeyImagePrepareResp
	partialResp  FrostSidecarKeyImagePartialResp
	combineResp  FrostSidecarKeyImageCombineResp
	validateResp FrostSidecarKeyImageValidateResp

	prepareErr  error
	partialErr  error
	combineErr  error
	validateErr error
}

func (m *mockFrostSidecarKeyImage) FrostSignPrepare(FrostSidecarPrepareReq) (FrostSidecarPrepareResp, error) {
	return FrostSidecarPrepareResp{}, nil
}

func (m *mockFrostSidecarKeyImage) FrostSignPartial(FrostSidecarPartialReq) (FrostSidecarPartialResp, error) {
	return FrostSidecarPartialResp{}, nil
}

func (m *mockFrostSidecarKeyImage) FrostSignCombine(FrostSidecarCombineReq) (FrostSidecarCombineResp, error) {
	return FrostSidecarCombineResp{}, nil
}

func (m *mockFrostSidecarKeyImage) FrostKeyImagePrepare(req FrostSidecarKeyImagePrepareReq) (FrostSidecarKeyImagePrepareResp, error) {
	m.prepareReqs = append(m.prepareReqs, req)
	if m.prepareErr != nil {
		return FrostSidecarKeyImagePrepareResp{}, m.prepareErr
	}
	return m.prepareResp, nil
}

func (m *mockFrostSidecarKeyImage) FrostKeyImagePartial(req FrostSidecarKeyImagePartialReq) (FrostSidecarKeyImagePartialResp, error) {
	m.partialReqs = append(m.partialReqs, req)
	if m.partialErr != nil {
		return FrostSidecarKeyImagePartialResp{}, m.partialErr
	}
	return m.partialResp, nil
}

func (m *mockFrostSidecarKeyImage) FrostKeyImageCombine(req FrostSidecarKeyImageCombineReq) (FrostSidecarKeyImageCombineResp, error) {
	m.combineReqs = append(m.combineReqs, req)
	if m.combineErr != nil {
		return FrostSidecarKeyImageCombineResp{}, m.combineErr
	}
	return m.combineResp, nil
}

func (m *mockFrostSidecarKeyImage) FrostKeyImageValidate(req FrostSidecarKeyImageValidateReq) (FrostSidecarKeyImageValidateResp, error) {
	m.validateReqs = append(m.validateReqs, req)
	if m.validateErr != nil {
		return FrostSidecarKeyImageValidateResp{}, m.validateErr
	}
	return m.validateResp, nil
}

func newTestFrostKeyImageCoordinator(localPubKey, msgID string, msgChan chan *p2p.Message, timeout time.Duration) *FrostKeyImageCoordinator {
	return &FrostKeyImageCoordinator{
		p2pCommunication: &p2p.Communication{BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 64)},
		localPubKey:      localPubKey,
		msgID:            msgID,
		msgChan:          msgChan,
		timeout:          timeout,
	}
}

func TestFrostKeyImageMsgIDDeterministic(t *testing.T) {
	base := FrostKeyImageRequest{
		VaultPubKey:   "thorpub-vault-1",
		TxHash:        testHashOf('a'),
		OutputIndex:   7,
		SignerPubKeys: []string{testPubKeys[2], testPubKeys[0], testPubKeys[1]},
	}

	id1, err := FrostKeyImageMsgID(base)
	if err != nil {
		t.Fatalf("msg id 1: %v", err)
	}
	id2, err := FrostKeyImageMsgID(base)
	if err != nil {
		t.Fatalf("msg id 2: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected deterministic msg id, got %q != %q", id1, id2)
	}

	reordered := base
	reordered.SignerPubKeys = []string{testPubKeys[1], testPubKeys[2], testPubKeys[0]}
	id3, err := FrostKeyImageMsgID(reordered)
	if err != nil {
		t.Fatalf("msg id reordered: %v", err)
	}
	if id1 != id3 {
		t.Fatalf("expected same msg id with reordered signers, got %q != %q", id1, id3)
	}

	otherOutput := base
	otherOutput.OutputIndex = 8
	id4, err := FrostKeyImageMsgID(otherOutput)
	if err != nil {
		t.Fatalf("msg id other output: %v", err)
	}
	if id1 == id4 {
		t.Fatalf("expected different msg id when output index changes")
	}
}

func TestFrostKeyImageCoordinationHeightUsesProvidedBlockHeight(t *testing.T) {
	base := FrostKeyImageRequest{
		VaultPubKey:   "thorpub-vault-1",
		TxHash:        testHashOf('a'),
		OutputIndex:   7,
		SignerPubKeys: []string{testPubKeys[2], testPubKeys[0], testPubKeys[1]},
		BlockHeight:   101,
	}

	if base.BlockHeight <= 0 {
		t.Fatalf("expected positive block height, got %d", base.BlockHeight)
	}
	if base.BlockHeight != 101 {
		t.Fatalf("expected request block height to drive coordination, got %d", base.BlockHeight)
	}

	zeroHeight := base
	zeroHeight.BlockHeight = 0
	if zeroHeight.BlockHeight != 0 {
		t.Fatalf("expected zero-height request fixture")
	}
}

func TestFrostKeyImageExchangePreprocessAcceptsTranscriptVerification(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	localPayload := FrostPreprocessPayload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		PreprocessHex:    "aa",
	}
	remotePayload := FrostPreprocessPayload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[1])],
		PreprocessHex:    "bb",
	}
	localHash, err := FrostTranscriptPreprocessHash("msg-id", localPayload)
	if err != nil {
		t.Fatalf("local hash: %v", err)
	}
	remoteHash, err := FrostTranscriptPreprocessHash("msg-id", remotePayload)
	if err != nil {
		t.Fatalf("remote hash: %v", err)
	}

	msgChan := make(chan *p2p.Message, 8)
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostPreprocessMsg, "msg-id", testPubKeys[1], remotePayload)
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostVerifyMsg, "msg-id", testPubKeys[1], FrostVerifyPayload{
		SenderPubKey:           testPubKeys[1],
		Round:                  frostRoundPreprocess,
		TargetSenderPubKey:     testPubKeys[1],
		TargetParticipantIndex: remotePayload.ParticipantIndex,
		PayloadHash:            remoteHash,
	})
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostVerifyMsg, "msg-id", testPubKeys[1], FrostVerifyPayload{
		SenderPubKey:           testPubKeys[1],
		Round:                  frostRoundPreprocess,
		TargetSenderPubKey:     testPubKeys[0],
		TargetParticipantIndex: localPayload.ParticipantIndex,
		PayloadHash:            localHash,
	})

	coord := newTestFrostKeyImageCoordinator(testPubKeys[0], "msg-id", msgChan, time.Second)
	values, failResp := coord.exchangePreprocess(activeSet, participantIndexMap, peerIDs, localPayload)
	if failResp != nil {
		t.Fatalf("expected success, got fail response: %+v", failResp)
	}
	if len(values) != 2 {
		t.Fatalf("expected two preprocess payloads, got %d", len(values))
	}
}

func TestFrostKeyImageExchangePreprocessRejectsSenderBindingMismatch(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	msgChan := make(chan *p2p.Message, 2)
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostPreprocessMsg, "msg-id", testPubKeys[2], FrostPreprocessPayload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[1])],
		PreprocessHex:    "remote-preprocess",
	})
	coord := newTestFrostKeyImageCoordinator(testPubKeys[0], "msg-id", msgChan, time.Second)

	_, failResp := coord.exchangePreprocess(activeSet, participantIndexMap, peerIDs, FrostPreprocessPayload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		PreprocessHex:    "local-preprocess",
	})
	if failResp == nil {
		t.Fatalf("expected sender-binding failure response")
	}
	if failResp.Status != tcommon.Fail {
		t.Fatalf("expected fail status, got %d", failResp.Status)
	}
	if len(failResp.Blame.BlameNodes) != 1 || failResp.Blame.BlameNodes[0].Pubkey != testPubKeys[1] {
		t.Fatalf("expected sender binding blame for %s, got %+v", testPubKeys[1], failResp.Blame.BlameNodes)
	}
}

func TestFrostKeyImageExchangePreprocessTimeoutMissingVerifications(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	msgChan := make(chan *p2p.Message, 4)
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostPreprocessMsg, "msg-id", testPubKeys[1], FrostPreprocessPayload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[1])],
		PreprocessHex:    "bb",
	})

	coord := newTestFrostKeyImageCoordinator(testPubKeys[0], "msg-id", msgChan, 30*time.Millisecond)
	_, failResp := coord.exchangePreprocess(activeSet, participantIndexMap, peerIDs, FrostPreprocessPayload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		PreprocessHex:    "aa",
	})
	if failResp == nil {
		t.Fatalf("expected timeout failure")
	}
	if failResp.Status != tcommon.Fail {
		t.Fatalf("expected fail status, got %d", failResp.Status)
	}
	if len(failResp.Blame.BlameNodes) != 1 || failResp.Blame.BlameNodes[0].Pubkey != testPubKeys[1] {
		t.Fatalf("expected missing verifier blame for %s, got %+v", testPubKeys[1], failResp.Blame.BlameNodes)
	}
}

func TestFrostKeyImageExchangeSharesAcceptsTranscriptVerification(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	localPayload := FrostSharePayload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		ShareHex:         "11",
	}
	remotePayload := FrostSharePayload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[1])],
		ShareHex:         "22",
	}
	localHash, err := FrostTranscriptShareHash("msg-id", localPayload)
	if err != nil {
		t.Fatalf("local hash: %v", err)
	}
	remoteHash, err := FrostTranscriptShareHash("msg-id", remotePayload)
	if err != nil {
		t.Fatalf("remote hash: %v", err)
	}

	msgChan := make(chan *p2p.Message, 8)
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostShareMsg, "msg-id", testPubKeys[1], remotePayload)
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostVerifyMsg, "msg-id", testPubKeys[1], FrostVerifyPayload{
		SenderPubKey:           testPubKeys[1],
		Round:                  frostRoundShare,
		TargetSenderPubKey:     testPubKeys[1],
		TargetParticipantIndex: remotePayload.ParticipantIndex,
		PayloadHash:            remoteHash,
	})
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostVerifyMsg, "msg-id", testPubKeys[1], FrostVerifyPayload{
		SenderPubKey:           testPubKeys[1],
		Round:                  frostRoundShare,
		TargetSenderPubKey:     testPubKeys[0],
		TargetParticipantIndex: localPayload.ParticipantIndex,
		PayloadHash:            localHash,
	})

	coord := newTestFrostKeyImageCoordinator(testPubKeys[0], "msg-id", msgChan, time.Second)
	values, failResp := coord.exchangeShares(activeSet, participantIndexMap, peerIDs, localPayload)
	if failResp != nil {
		t.Fatalf("expected success, got fail response: %+v", failResp)
	}
	if len(values) != 2 {
		t.Fatalf("expected two share payloads, got %d", len(values))
	}
}

func TestFrostKeyImageExecuteSuccess(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, _, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activePeerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	localIndex := participantIndexMap[normalizePubKey(testPubKeys[0])]
	remoteIndex := participantIndexMap[normalizePubKey(testPubKeys[1])]
	localIndexKey := strconv.FormatUint(uint64(localIndex), 10)
	remoteIndexKey := strconv.FormatUint(uint64(remoteIndex), 10)

	localPreprocess := FrostPreprocessPayload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: localIndex,
		PreprocessHex:    "local-preprocess",
	}
	remotePreprocess := FrostPreprocessPayload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: remoteIndex,
		PreprocessHex:    "remote-preprocess",
	}
	localPreprocessHash, err := FrostTranscriptPreprocessHash("msg-id", localPreprocess)
	if err != nil {
		t.Fatalf("local preprocess hash: %v", err)
	}
	remotePreprocessHash, err := FrostTranscriptPreprocessHash("msg-id", remotePreprocess)
	if err != nil {
		t.Fatalf("remote preprocess hash: %v", err)
	}

	localShare := FrostSharePayload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: localIndex,
		ShareHex:         "local-share",
	}
	remoteShare := FrostSharePayload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: remoteIndex,
		ShareHex:         "remote-share",
	}
	localShareHash, err := FrostTranscriptShareHash("msg-id", localShare)
	if err != nil {
		t.Fatalf("local share hash: %v", err)
	}
	remoteShareHash, err := FrostTranscriptShareHash("msg-id", remoteShare)
	if err != nil {
		t.Fatalf("remote share hash: %v", err)
	}

	msgChan := make(chan *p2p.Message, 12)
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostPreprocessMsg, "msg-id", testPubKeys[1], remotePreprocess)
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostVerifyMsg, "msg-id", testPubKeys[1], FrostVerifyPayload{
		SenderPubKey:           testPubKeys[1],
		Round:                  frostRoundPreprocess,
		TargetSenderPubKey:     testPubKeys[1],
		TargetParticipantIndex: remoteIndex,
		PayloadHash:            remotePreprocessHash,
	})
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostVerifyMsg, "msg-id", testPubKeys[1], FrostVerifyPayload{
		SenderPubKey:           testPubKeys[1],
		Round:                  frostRoundPreprocess,
		TargetSenderPubKey:     testPubKeys[0],
		TargetParticipantIndex: localIndex,
		PayloadHash:            localPreprocessHash,
	})
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostShareMsg, "msg-id", testPubKeys[1], remoteShare)
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostVerifyMsg, "msg-id", testPubKeys[1], FrostVerifyPayload{
		SenderPubKey:           testPubKeys[1],
		Round:                  frostRoundShare,
		TargetSenderPubKey:     testPubKeys[1],
		TargetParticipantIndex: remoteIndex,
		PayloadHash:            remoteShareHash,
	})
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostVerifyMsg, "msg-id", testPubKeys[1], FrostVerifyPayload{
		SenderPubKey:           testPubKeys[1],
		Round:                  frostRoundShare,
		TargetSenderPubKey:     testPubKeys[0],
		TargetParticipantIndex: localIndex,
		PayloadHash:            localShareHash,
	})

	sidecar := &mockFrostSidecarKeyImage{
		prepareResp:  FrostSidecarKeyImagePrepareResp{SessionID: "session-1", Preprocess: "local-preprocess"},
		partialResp:  FrostSidecarKeyImagePartialResp{SignatureShare: "local-share"},
		combineResp:  FrostSidecarKeyImageCombineResp{TxHash: strings.ToUpper(testHashOf('c')), OutputIndex: 7, KeyImage: "ABCD", ProofC: "CDEF", ProofS: "0123"},
		validateResp: FrostSidecarKeyImageValidateResp{Valid: true},
	}

	coord := newTestFrostKeyImageCoordinator(testPubKeys[0], "msg-id", msgChan, time.Second)
	coord.sidecar = sidecar

	resp := coord.Execute(FrostKeyImageRequest{
		VaultPubKey:   "thorpub-vault",
		TxHash:        testHashOf('c'),
		OutputIndex:   7,
		SignerPubKeys: members,
	}, members, activePeerIDs)
	if resp.Status != tcommon.Success {
		t.Fatalf("expected success status, got %d with blame %+v", resp.Status, resp.Blame)
	}
	if resp.MsgID != "msg-id" || resp.TxHash != testHashOf('c') {
		t.Fatalf("unexpected response identity: %+v", resp)
	}
	if resp.KeyImage != "abcd" || resp.ProofC != "cdef" || resp.ProofS != "0123" {
		t.Fatalf("expected lowercase normalized proof payload, got %+v", resp)
	}

	if len(sidecar.prepareReqs) != 1 {
		t.Fatalf("expected one prepare call, got %d", len(sidecar.prepareReqs))
	}
	if sidecar.prepareReqs[0].VaultPubKey != "thorpub-vault" || sidecar.prepareReqs[0].TxHash != testHashOf('c') {
		t.Fatalf("unexpected prepare request: %+v", sidecar.prepareReqs[0])
	}
	if len(sidecar.partialReqs) != 1 {
		t.Fatalf("expected one partial call, got %d", len(sidecar.partialReqs))
	}
	if sidecar.partialReqs[0].SessionID != "session-1" {
		t.Fatalf("unexpected partial session id: %+v", sidecar.partialReqs[0])
	}
	if len(sidecar.partialReqs[0].AllPreprocesses) != 2 || sidecar.partialReqs[0].AllPreprocesses[remoteIndexKey] != "remote-preprocess" {
		t.Fatalf("unexpected preprocess set: %+v", sidecar.partialReqs[0].AllPreprocesses)
	}
	if sidecar.partialReqs[0].AllPreprocesses[localIndexKey] != "local-preprocess" {
		t.Fatalf("missing local preprocess: %+v", sidecar.partialReqs[0].AllPreprocesses)
	}
	if len(sidecar.combineReqs) != 1 {
		t.Fatalf("expected one combine call, got %d", len(sidecar.combineReqs))
	}
	if len(sidecar.combineReqs[0].AllShares) != 2 || sidecar.combineReqs[0].AllShares[remoteIndexKey] != "remote-share" {
		t.Fatalf("unexpected share set: %+v", sidecar.combineReqs[0].AllShares)
	}
	if sidecar.combineReqs[0].AllShares[localIndexKey] != "local-share" {
		t.Fatalf("missing local share: %+v", sidecar.combineReqs[0].AllShares)
	}
	if len(sidecar.validateReqs) != 1 {
		t.Fatalf("expected one validate call, got %d", len(sidecar.validateReqs))
	}
	if sidecar.validateReqs[0].KeyImage != "ABCD" || sidecar.validateReqs[0].ProofC != "CDEF" || sidecar.validateReqs[0].ProofS != "0123" {
		t.Fatalf("unexpected validate request: %+v", sidecar.validateReqs[0])
	}
}

func TestFrostKeyImageExecuteFailsWhenPrepareFails(t *testing.T) {
	sidecar := &mockFrostSidecarKeyImage{prepareErr: errTestKeyImagePrepare}

	coord := newTestFrostKeyImageCoordinator(testPubKeys[0], "msg-id", make(chan *p2p.Message, 4), time.Second)
	coord.sidecar = sidecar

	resp := coord.Execute(FrostKeyImageRequest{
		VaultPubKey:   "thorpub-vault",
		TxHash:        testHashOf('d'),
		OutputIndex:   1,
		SignerPubKeys: []string{testPubKeys[0]},
	}, []string{testPubKeys[0]}, nil)

	if resp.Status != tcommon.Fail {
		t.Fatalf("expected fail status, got %d", resp.Status)
	}
	if !strings.Contains(resp.Blame.FailReason, "prepare failed") {
		t.Fatalf("expected prepare failure blame, got %+v", resp.Blame)
	}
}

func TestFrostKeyImageExecuteFailsWhenValidateReturnsInvalid(t *testing.T) {
	sidecar := &mockFrostSidecarKeyImage{
		prepareResp:  FrostSidecarKeyImagePrepareResp{SessionID: "session-1", Preprocess: "local-preprocess"},
		partialResp:  FrostSidecarKeyImagePartialResp{SignatureShare: "local-share"},
		combineResp:  FrostSidecarKeyImageCombineResp{TxHash: testHashOf('e'), OutputIndex: 2, KeyImage: "abcd", ProofC: "cdef", ProofS: "0123"},
		validateResp: FrostSidecarKeyImageValidateResp{Valid: false},
	}

	coord := newTestFrostKeyImageCoordinator(testPubKeys[0], "msg-id", make(chan *p2p.Message, 4), time.Second)
	coord.sidecar = sidecar

	resp := coord.Execute(FrostKeyImageRequest{
		VaultPubKey:   "thorpub-vault",
		TxHash:        testHashOf('e'),
		OutputIndex:   2,
		SignerPubKeys: []string{testPubKeys[0]},
	}, []string{testPubKeys[0]}, nil)

	if resp.Status != tcommon.Fail {
		t.Fatalf("expected fail status, got %d", resp.Status)
	}
	if resp.Blame.FailReason != "frost key image validate returned invalid=false" {
		t.Fatalf("unexpected validate failure blame: %+v", resp.Blame)
	}
}

var errTestKeyImagePrepare = json.Unmarshal([]byte("!"), &struct{}{})
