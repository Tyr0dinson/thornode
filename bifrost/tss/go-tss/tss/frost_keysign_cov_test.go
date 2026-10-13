package tss

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tcrypto "github.com/cometbft/cometbft/crypto"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/common"
)

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

func frostPrivKeyCov(t *testing.T, idx int) tcrypto.PrivKey {
	t.Helper()
	priv, err := conversion.GetPriKey(testPriKeyArr[idx])
	if err != nil {
		t.Fatalf("private key %d: %v", idx, err)
	}
	return priv
}

// newFrostServerCov starts a real (loopback, ephemeral port) p2p host for the
// given test identity and wraps it in a minimal TssServer.
func newFrostServerCov(t *testing.T, idx int, partyTimeout, keysignTimeout time.Duration) *TssServer {
	t.Helper()
	ensureBech32Prefix()
	priv := frostPrivKeyCov(t, idx)
	raw, err := conversion.GetPriKeyRawBytes(priv)
	if err != nil {
		t.Fatal(err)
	}
	comm, err := p2p.NewCommunication(&p2p.Config{Port: 0, RendezvousString: "frost-cov-test"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := comm.Start(raw); err != nil {
		t.Fatal(err)
	}
	party := p2p.NewPartyCoordinator(comm.GetHost(), partyTimeout)
	t.Cleanup(func() {
		party.Stop()
		_ = comm.Stop()
	})
	return &TssServer{
		p2pCommunication: comm,
		partyCoordinator: party,
		localNodePubKey:  testPubKeys[idx],
		privateKey:       priv,
		conf:             tcommon.TssConfig{KeySignTimeout: keysignTimeout, PartyTimeout: partyTimeout},
		logger:           zerolog.Nop(),
	}
}

// frostCombineSidecarCov is a deterministic FROST sidecar whose combine output
// binds to the selected inputs it prepared.
type frostCombineSidecarCov struct {
	mu          sync.Mutex
	preprocess  string
	share       string
	txHash      string
	txHex       string
	fee         uint64
	prepareReqs []FrostSidecarPrepareReq
	partialReqs []FrostSidecarPartialReq
	combineReqs []FrostSidecarCombineReq
}

func newFrostCombineSidecarCov(preprocess, share string) *frostCombineSidecarCov {
	return &frostCombineSidecarCov{
		preprocess: preprocess,
		share:      share,
		txHash:     testHashOf('e'),
		txHex:      "deadbeef",
		fee:        42,
	}
}

func (m *frostCombineSidecarCov) selected() []FrostSpentOutputRef {
	return []FrostSpentOutputRef{{TxHash: testHashOf('c'), OutputIndex: 1, KeyImage: testHashOf('d')}}
}

func (m *frostCombineSidecarCov) FrostSignPrepare(req FrostSidecarPrepareReq) (FrostSidecarPrepareResp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prepareReqs = append(m.prepareReqs, req)
	return FrostSidecarPrepareResp{SessionID: "session", Preprocess: m.preprocess, SelectedInputs: m.selected()}, nil
}

func (m *frostCombineSidecarCov) FrostSignPartial(req FrostSidecarPartialReq) (FrostSidecarPartialResp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.partialReqs = append(m.partialReqs, req)
	return FrostSidecarPartialResp{SignatureShare: m.share}, nil
}

func (m *frostCombineSidecarCov) FrostSignCombine(req FrostSidecarCombineReq) (FrostSidecarCombineResp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.combineReqs = append(m.combineReqs, req)
	refs := m.selected()
	refs[0].SpendTxHash = m.txHash
	return FrostSidecarCombineResp{
		TxHash:          m.txHash,
		TxHex:           m.txHex,
		TxKey:           testTxKey,
		FeeAtomic:       m.fee,
		SpentOutputRefs: refs,
	}, nil
}

// frostRouterCov wires several in-process coordinators together: everything a
// coordinator broadcasts is delivered to the addressed peers' msgChan tagged
// with the sender's peer ID, exactly like the p2p layer does.
type frostRouterCov struct {
	coords  []*FrostSignCoordinator
	peerIDs []peer.ID
	done    chan struct{}
	wg      sync.WaitGroup
	// mutate, when non-nil, may rewrite or drop (return nil) a delivery.
	mutate func(from int, msg *messages.BroadcastMsgChan) *messages.BroadcastMsgChan
	// inject, when non-nil, returns extra frames delivered to `to` ahead of
	// each real frame from `from`.
	inject func(from, to int, msg *messages.BroadcastMsgChan) []*p2p.Message
}

func newFrostRouterCov(t *testing.T, members []string, msgID, planHash string, strict bool, timeout time.Duration) *frostRouterCov {
	t.Helper()
	ensureBech32Prefix()
	r := &frostRouterCov{done: make(chan struct{})}
	for i, member := range members {
		pid, err := conversion.GetPeerIDFromPubKey(member)
		if err != nil {
			t.Fatal(err)
		}
		r.peerIDs = append(r.peerIDs, pid)
		var priv tcrypto.PrivKey
		if strict {
			priv = frostPrivKeyCov(t, i)
		}
		comm := &p2p.Communication{BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 256)}
		r.coords = append(r.coords, NewFrostSignCoordinator(comm, nil, member, msgID, planHash,
			newFrostSignInbox(), timeout, priv))
	}
	return r
}

func (r *frostRouterCov) start() {
	for i := range r.coords {
		r.wg.Add(1)
		go func(from int) {
			defer r.wg.Done()
			for {
				select {
				case <-r.done:
					return
				case msg := <-r.coords[from].p2pCommunication.BroadcastMsgChan:
					if r.mutate != nil {
						msg = r.mutate(from, msg)
						if msg == nil {
							continue
						}
					}
					raw, err := json.Marshal(msg.WrappedMessage)
					if err != nil {
						continue
					}
					// injected junk keeps the real frame's type, so it rides the same topic
					topic := msg.WrappedMessage.MessageType
					for _, target := range msg.PeersID {
						for j, pid := range r.peerIDs {
							if j == from || pid != target {
								continue
							}
							topicChan, ok := r.coords[j].inbox.messageChannel(topic)
							if !ok {
								continue
							}
							deliveries := []*p2p.Message{}
							if r.inject != nil {
								deliveries = append(deliveries, r.inject(from, j, msg)...)
							}
							deliveries = append(deliveries, &p2p.Message{PeerID: r.peerIDs[from], Payload: raw})
							for _, d := range deliveries {
								select {
								case topicChan <- d:
								case <-r.done:
									return
								}
							}
						}
					}
				}
			}
		}(i)
	}
}

func (r *frostRouterCov) stop() {
	close(r.done)
	r.wg.Wait()
}

func (r *frostRouterCov) executeAll(req FrostKeySignRequest, active []string) []FrostKeySignResponse {
	out := make([]FrostKeySignResponse, len(r.coords))
	var wg sync.WaitGroup
	for i := range r.coords {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i] = r.coords[i].Execute(req, active, r.peerIDs)
		}(i)
	}
	wg.Wait()
	return out
}

func frostThreeMemberReqCov(members []string) FrostKeySignRequest {
	return FrostKeySignRequest{
		VaultPubKey:   "thorpub1test-vault",
		Network:       "mainnet",
		ToAddress:     "48abc",
		AmountAtomic:  1000,
		SignerPubKeys: members,
	}
}

// ---------------------------------------------------------------------------
// coordinator ceremonies (in-process, no network)
// ---------------------------------------------------------------------------

func TestFrostExecuteCovStrictThreePartySuccess(t *testing.T) {
	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	msgID := testHashOf('1')
	r := newFrostRouterCov(t, members, msgID, testExpectedPlanHash, true, 5*time.Second)
	sidecars := make([]*frostCombineSidecarCov, len(members))
	for i, coord := range r.coords {
		sidecars[i] = newFrostCombineSidecarCov("aa0"+string(rune('0'+i)), "cc0"+string(rune('0'+i)))
		coord.sidecar = sidecars[i]
		if !coord.strictTranscript {
			t.Fatal("coordinator with private key must use strict transcripts")
		}
	}
	r.start()
	defer r.stop()

	results := r.executeAll(frostThreeMemberReqCov(members), members)
	for i, resp := range results {
		if resp.Status != tcommon.Success {
			t.Fatalf("party %d failed: %+v", i, resp.Blame)
		}
		if resp.MsgID != msgID || resp.TxHash != testHashOf('e') || resp.TxKey != testTxKey || resp.FeeAtomic != 42 {
			t.Fatalf("party %d unexpected response: %+v", i, resp)
		}
		if resp.Eventuality.PlanHash != testExpectedPlanHash || resp.Eventuality.EventualityID == "" {
			t.Fatalf("party %d eventuality not bound to plan: %+v", i, resp.Eventuality)
		}
		// every party combined over all three preprocesses and shares
		if len(sidecars[i].partialReqs) != 1 || len(sidecars[i].partialReqs[0].AllPreprocesses) != 3 {
			t.Fatalf("party %d partial input: %+v", i, sidecars[i].partialReqs)
		}
		if len(sidecars[i].combineReqs) != 1 || len(sidecars[i].combineReqs[0].AllShares) != 3 {
			t.Fatalf("party %d combine input: %+v", i, sidecars[i].combineReqs)
		}
		// decoy context is derived from the msgID, identically on every party
		wantDecoy, err := FrostDecoyContext(msgID)
		if err != nil || sidecars[i].prepareReqs[0].DecoyContext != wantDecoy {
			t.Fatalf("party %d decoy context %q want %q (%v)", i, sidecars[i].prepareReqs[0].DecoyContext, wantDecoy, err)
		}
	}
	if results[0].Eventuality != results[1].Eventuality || results[1].Eventuality != results[2].Eventuality {
		t.Fatal("parties derived different eventualities")
	}
}

func TestFrostExecuteCovStrictRejectsUnsignedPeerTraffic(t *testing.T) {
	t.Parallel()
	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	msgID := testHashOf('2')
	r := newFrostRouterCov(t, members, msgID, testExpectedPlanHash, true, 300*time.Millisecond)
	for i, coord := range r.coords {
		coord.sidecar = newFrostCombineSidecarCov("aa0"+string(rune('0'+i)), "cc0"+string(rune('0'+i)))
	}
	// party 2 strips the signatures from everything it sends, so the strict
	// verifiers on parties 0 and 1 must ignore its payloads and echoes
	r.mutate = func(from int, msg *messages.BroadcastMsgChan) *messages.BroadcastMsgChan {
		if from != 2 {
			return msg
		}
		var generic map[string]json.RawMessage
		if err := json.Unmarshal(msg.WrappedMessage.Payload, &generic); err != nil {
			return msg
		}
		delete(generic, "signature")
		delete(generic, "target_signature")
		delete(generic, "attester_signature")
		stripped, err := json.Marshal(generic)
		if err != nil {
			return msg
		}
		cp := *msg
		cp.WrappedMessage.Payload = stripped
		return &cp
	}
	r.start()
	defer r.stop()

	results := r.executeAll(frostThreeMemberReqCov(members), members)
	for i := 0; i < 2; i++ {
		resp := results[i]
		if resp.Status != tcommon.Fail {
			t.Fatalf("party %d accepted unsigned traffic: %+v", i, resp)
		}
		if resp.Blame.Round != messages.FROSTKEYSIGNPREPROCESS {
			t.Fatalf("party %d failed at %q, want preprocess timeout", i, resp.Blame.Round)
		}
		if !strings.Contains(resp.Blame.FailReason, "preprocess payloads") {
			t.Fatalf("party %d reason %q", i, resp.Blame.FailReason)
		}
		// the timeout is not attributable, nothing is blamed
		if len(resp.Blame.BlameNodes) != 0 {
			t.Fatalf("party %d blamed nodes on an ambiguous timeout: %+v", i, resp.Blame.BlameNodes)
		}
		// three active members with threshold 1 leave a spare signer: another
		// threshold could have completed without this node, so the local
		// pre-share failure must not release its claim
		if resp.releaseSigningClaim {
			t.Fatalf("party %d released its claim despite a spare signer", i)
		}
	}
}

func TestFrostExecuteCovStrictShareTimeoutIsTerminal(t *testing.T) {
	t.Parallel()
	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	msgID := testHashOf('3')
	r := newFrostRouterCov(t, members, msgID, testExpectedPlanHash, true, 400*time.Millisecond)
	for i, coord := range r.coords {
		coord.sidecar = newFrostCombineSidecarCov("aa0"+string(rune('0'+i)), "cc0"+string(rune('0'+i)))
	}
	// party 2 never delivers its own share payload
	r.mutate = func(from int, msg *messages.BroadcastMsgChan) *messages.BroadcastMsgChan {
		if from == 2 && msg.WrappedMessage.MessageType == messages.TSSFrostShareMsg {
			return nil
		}
		return msg
	}
	r.start()
	defer r.stop()

	results := r.executeAll(frostThreeMemberReqCov(members), members)
	for i := 0; i < 2; i++ {
		resp := results[i]
		if resp.Status != tcommon.Fail || resp.Blame.Round != messages.FROSTKEYSIGNSHARE {
			t.Fatalf("party %d: want terminal share failure, got %+v", i, resp)
		}
		if !strings.Contains(resp.Blame.FailReason, "share payloads") {
			t.Fatalf("party %d reason %q", i, resp.Blame.FailReason)
		}
		if resp.releaseSigningClaim {
			t.Fatalf("party %d must keep its claim after share exposure", i)
		}
	}
}

func TestFrostExecuteCovNonStrictThreePartySuccess(t *testing.T) {
	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	msgID := testHashOf('4')
	r := newFrostRouterCov(t, members, msgID, testExpectedPlanHash, false, 5*time.Second)
	for i, coord := range r.coords {
		coord.sidecar = newFrostCombineSidecarCov("aa1"+string(rune('0'+i)), "cc1"+string(rune('0'+i)))
		if coord.strictTranscript {
			t.Fatal("coordinator without private key must not use strict transcripts")
		}
	}
	r.start()
	defer r.stop()
	for i, resp := range r.executeAll(frostThreeMemberReqCov(members), members) {
		if resp.Status != tcommon.Success || resp.TxHash != testHashOf('e') {
			t.Fatalf("party %d: %+v", i, resp)
		}
	}
}

func TestFrostExecuteCovEquivocatingPreprocessAborts(t *testing.T) {
	members := []string{testPubKeys[0], testPubKeys[1]}
	msgID := testHashOf('5')
	ensureBech32Prefix()
	_, indexMap, _, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatal(err)
	}
	remote := members[1]
	remotePriv := frostPrivKeyCov(t, 1)
	remoteIdx := indexMap[normalizePubKey(remote)]
	signedPreprocess := func(hexValue string) FrostPreprocessPayload {
		p := FrostPreprocessPayload{SenderPubKey: remote, ParticipantIndex: remoteIdx, PreprocessHex: hexValue}
		h, err := FrostTranscriptPreprocessHash(msgID, p)
		if err != nil {
			t.Fatal(err)
		}
		env, err := newFrostSignedEnvelope(remotePriv, msgID, "thorpub1test-vault", "mainnet", frostRoundPreprocess, remote, remoteIdx, h)
		if err != nil {
			t.Fatal(err)
		}
		p.ProtocolVersion = env.ProtocolVersion
		p.Signature = env.Signature
		return p
	}

	msgChan := newFrostSignInbox()
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatal(err)
	}
	coord := NewFrostSignCoordinator(&p2p.Communication{BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 64)},
		newFrostCombineSidecarCov("aa", "cc"), members[0], msgID, testExpectedPlanHash, msgChan, 2*time.Second, frostPrivKeyCov(t, 0))
	// two different validly signed preprocess payloads from the same sender
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostPreprocessMsg, msgID, remote, signedPreprocess("01"))
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostPreprocessMsg, msgID, remote, signedPreprocess("02"))

	resp := coord.Execute(frostThreeMemberReqCov(members), members, peerIDs)
	if resp.Status != tcommon.Fail || resp.Blame.FailReason != frostEquivocationFailReason {
		t.Fatalf("expected equivocation failure, got %+v", resp)
	}
	if resp.Blame.Round != frostRoundPreprocess || len(resp.Blame.BlameNodes) != 0 {
		t.Fatalf("equivocation must stay non-actionable at preprocess round: %+v", resp.Blame)
	}
}

func TestFrostExecuteCovValidationFailures(t *testing.T) {
	ensureBech32Prefix()
	members := []string{testPubKeys[0], testPubKeys[1]}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatal(err)
	}
	base := frostThreeMemberReqCov(members)
	newCoord := func(local, msgID string, sidecar FrostSidecarClient) *FrostSignCoordinator {
		return NewFrostSignCoordinator(&p2p.Communication{BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 64)},
			sidecar, local, msgID, testExpectedPlanHash, newFrostSignInbox(), time.Second, nil)
	}
	sidecar := newFrostCombineSidecarCov("aa", "cc")

	cases := []struct {
		name   string
		coord  *FrostSignCoordinator
		req    FrostKeySignRequest
		active []string
		reason string
	}{
		{"nil sidecar", newCoord(members[0], "m", nil), base, members, "frost sidecar client is nil"},
		{"empty msgid", newCoord(members[0], " ", sidecar), base, members, "frost msgID is empty"},
		{"empty vault", newCoord(members[0], "m", sidecar), func() FrostKeySignRequest { r := base; r.VaultPubKey = " "; return r }(), members, "frost vault pubkey is empty"},
		{"duplicate signer", newCoord(members[0], "m", sidecar), func() FrostKeySignRequest {
			r := base
			r.SignerPubKeys = []string{members[0], strings.ToUpper(members[0])}
			return r
		}(), members, "duplicate signer pubkey"},
		{"unknown active", newCoord(members[0], "m", sidecar), base, []string{testPubKeys[3]}, "is not in signer set"},
		{"local not member", newCoord(testPubKeys[3], "m", sidecar), base, members, "local signer is not in participant set"},
		{"local not active", newCoord(members[0], "m", sidecar), base, []string{members[1]}, "local signer is not in active signer set"},
	}
	for _, tc := range cases {
		resp := tc.coord.Execute(tc.req, tc.active, peerIDs)
		if resp.Status != tcommon.Fail || !strings.Contains(resp.Blame.FailReason, tc.reason) {
			t.Fatalf("%s: got %+v", tc.name, resp)
		}
	}
	if len(sidecar.prepareReqs) != 0 {
		t.Fatal("validation failures must not reach the sidecar")
	}
	// network defaults to the current chain network when omitted
	c := newCoord(members[0], "m", &mockFrostSidecarSign{prepareErr: errors.New("boom")})
	req := base
	req.Network = ""
	resp := c.Execute(req, members, peerIDs)
	if resp.Status != tcommon.Fail || !strings.Contains(resp.Blame.FailReason, "frost prepare failed: boom") {
		t.Fatalf("prepare failure: %+v", resp)
	}
	if c.network != common.ChainNetworkName(common.CurrentChainNetwork) {
		t.Fatalf("network not defaulted: %q", c.network)
	}
}

func TestFrostExecuteCovCombineResultValidation(t *testing.T) {
	ensureBech32Prefix()
	members := []string{testPubKeys[0]}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatal(err)
	}
	run := func(combine FrostSidecarCombineResp, selected []FrostSpentOutputRef, planHash string) FrostKeySignResponse {
		coord := NewFrostSignCoordinator(&p2p.Communication{BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 64)},
			&mockFrostSidecarSign{
				prepareResp: FrostSidecarPrepareResp{SessionID: "s", Preprocess: "aa", SelectedInputs: selected},
				partialResp: FrostSidecarPartialResp{SignatureShare: "cc"},
				combineResp: combine,
			}, members[0], testHashOf('6'), planHash, newFrostSignInbox(), time.Second, frostPrivKeyCov(t, 0))
		return coord.Execute(frostThreeMemberReqCov(members), members, peerIDs)
	}
	selected := []FrostSpentOutputRef{{TxHash: testHashOf('c'), OutputIndex: 1, KeyImage: testHashOf('d')}}
	good := FrostSidecarCombineResp{
		TxHash: testHashOf('e'), TxHex: "deadbeef", TxKey: testTxKey, FeeAtomic: 1,
		SpentOutputRefs: []FrostSpentOutputRef{{TxHash: testHashOf('c'), OutputIndex: 1, KeyImage: testHashOf('d'), SpendTxHash: testHashOf('e')}},
	}
	if resp := run(good, selected, testExpectedPlanHash); resp.Status != tcommon.Success {
		t.Fatalf("strict single signer should succeed: %+v", resp)
	}

	badBinding := good
	badBinding.SpentOutputRefs = nil
	resp := run(badBinding, selected, testExpectedPlanHash)
	if resp.Status != tcommon.Fail || !strings.Contains(resp.Blame.FailReason, "frost combine input binding failed") ||
		resp.Blame.Round != messages.FROSTKEYSIGNRESULT {
		t.Fatalf("input binding: %+v", resp)
	}

	badKey := good
	badKey.TxKey = strings.Repeat("0", 64)
	resp = run(badKey, selected, testExpectedPlanHash)
	if resp.Status != tcommon.Fail || !strings.Contains(resp.Blame.FailReason, "invalid frost combine tx_key") {
		t.Fatalf("tx key: %+v", resp)
	}

	badHex := good
	badHex.TxHex = "zz"
	resp = run(badHex, selected, testExpectedPlanHash)
	if resp.Status != tcommon.Fail || !strings.Contains(resp.Blame.FailReason, "failed to derive eventuality") {
		t.Fatalf("eventuality: %+v", resp)
	}
}

// ---------------------------------------------------------------------------
// server entry point
// ---------------------------------------------------------------------------

func frostServerReqCov(signers []string) FrostKeySignRequest {
	return FrostKeySignRequest{
		VaultPubKey:   "thorpub1test-vault",
		ToAddress:     "48abc",
		AmountAtomic:  1000,
		SignerPubKeys: signers,
		BlockHeight:   100,
		Version:       frostJoinRotationVersion,
	}
}

func TestFrostKeySignCovServerSingleSignerSuccess(t *testing.T) {
	server := newFrostServerCov(t, 0, time.Second, 5*time.Second)
	sidecar := newFrostCombineSidecarCov("aa", "cc")
	req := frostServerReqCov([]string{testPubKeys[0]})
	reserved := 0
	req.ReserveSigningClaim = func() error { reserved++; return nil }

	resp, err := server.FrostKeySign(req, sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != tcommon.Success || resp.TxHash != testHashOf('e') {
		t.Fatalf("single signer keysign failed: %+v", resp)
	}
	wantMsgID, err := FrostKeySignMsgID(func() FrostKeySignRequest {
		r := req
		r.Network = common.ChainNetworkName(common.CurrentChainNetwork)
		return r
	}())
	if err != nil || resp.MsgID != wantMsgID {
		t.Fatalf("msgID %q want %q (%v)", resp.MsgID, wantMsgID, err)
	}
	if reserved != 1 {
		t.Fatalf("claim reserved %d times", reserved)
	}
	// the success result is gossiped to the vault
	select {
	case msg := <-server.p2pCommunication.BroadcastMsgChan:
		if msg == nil {
			t.Fatal("nil broadcast")
		}
	default:
	}
}

func TestFrostKeySignCovServerLegacyJoinVersion(t *testing.T) {
	server := newFrostServerCov(t, 0, 200*time.Millisecond, 5*time.Second)
	req := frostServerReqCov([]string{testPubKeys[0]})
	// below the rotation version the legacy joinParty path is used; an
	// unparseable version fails party formation without blame
	req.Version = "not-a-version"
	resp, err := server.FrostKeySign(req, newFrostCombineSidecarCov("aa", "cc"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != tcommon.Fail || resp.Blame.FailReason != blame.TssSyncFail || len(resp.Blame.BlameNodes) != 0 {
		t.Fatalf("expected unattributed join failure, got %+v", resp)
	}
}

func TestFrostKeySignCovServerReserveClaimFailure(t *testing.T) {
	server := newFrostServerCov(t, 0, time.Second, 5*time.Second)
	sidecar := newFrostCombineSidecarCov("aa", "cc")
	req := frostServerReqCov([]string{testPubKeys[0]})
	req.ReserveSigningClaim = func() error { return errors.New("claim store down") }
	_, err := server.FrostKeySign(req, sidecar)
	if err == nil || !strings.Contains(err.Error(), "claim store down") {
		t.Fatalf("expected reserve failure, got %v", err)
	}
	if len(sidecar.prepareReqs) != 0 {
		t.Fatal("sidecar must not be called without a reserved claim")
	}
}

func TestFrostKeySignCovServerFailureReleasesClaim(t *testing.T) {
	t.Parallel()
	server := newFrostServerCov(t, 0, time.Second, 50*time.Millisecond)
	// a negative timeout keeps the post-failure result wait short
	server.conf.KeySignTimeout = -4900 * time.Millisecond
	req := frostServerReqCov([]string{testPubKeys[0]})
	released := 0
	req.ReleaseSigningClaim = func() error { released++; return nil }
	resp, err := server.FrostKeySign(req, &mockFrostSidecarSign{prepareErr: errors.New("prepare down")})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != tcommon.Fail || !strings.Contains(resp.Blame.FailReason, "frost prepare failed") {
		t.Fatalf("unexpected response %+v", resp)
	}
	if released != 1 {
		t.Fatalf("pre-share failure must release the claim once, released=%d", released)
	}

	req.ReleaseSigningClaim = func() error { return errors.New("release failed") }
	_, err = server.FrostKeySign(req, &mockFrostSidecarSign{prepareErr: errors.New("prepare down")})
	if err == nil || !strings.Contains(err.Error(), "release unused FROST signing claim") {
		t.Fatalf("expected release error, got %v", err)
	}
}

func TestFrostKeySignCovServerNonEligibleStandsDown(t *testing.T) {
	t.Parallel()
	server := newFrostServerCov(t, 0, time.Second, 0)
	server.conf.KeySignTimeout = -4900 * time.Millisecond
	req := frostServerReqCov([]string{testPubKeys[0], testPubKeys[1], testPubKeys[2]})
	// the local node is a vault member but not in the filtered active set
	req.ActiveSignerPubKeys = []string{testPubKeys[1], testPubKeys[2]}
	sidecar := newFrostCombineSidecarCov("aa", "cc")
	resp, err := server.FrostKeySign(req, sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != tcommon.NA || resp.MsgID == "" {
		t.Fatalf("non-eligible signer should stand down with NA, got %+v", resp)
	}
	if len(sidecar.prepareReqs) != 0 {
		t.Fatal("non-eligible signer must not call the sidecar")
	}
}

func TestFrostKeySignCovServerJoinFailureRotatesWithoutBlame(t *testing.T) {
	t.Parallel()
	server := newFrostServerCov(t, 0, 150*time.Millisecond, time.Second)
	req := frostServerReqCov([]string{testPubKeys[0], testPubKeys[1]})
	resp, err := server.FrostKeySign(req, newFrostCombineSidecarCov("aa", "cc"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != tcommon.Fail || resp.Blame.FailReason != blame.TssSyncFail || len(resp.Blame.BlameNodes) != 0 {
		t.Fatalf("offline peer join failure must be unattributed: %+v", resp)
	}
}

func TestFrostKeySignCovServerRequestValidation(t *testing.T) {
	server := newFrostServerCov(t, 0, time.Second, time.Second)
	sidecar := newFrostCombineSidecarCov("aa", "cc")
	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}

	if _, err := server.FrostKeySign(frostServerReqCov(members), nil); err == nil || !strings.Contains(err.Error(), "sidecar client is nil") {
		t.Fatalf("nil sidecar: %v", err)
	}
	if _, err := server.FrostKeySign(frostServerReqCov(nil), sidecar); err == nil || !strings.Contains(err.Error(), "signer pub keys are empty") {
		t.Fatalf("empty signers: %v", err)
	}
	req := frostServerReqCov(members)
	req.Network = "some-other-net"
	if _, err := server.FrostKeySign(req, sidecar); err == nil || !strings.Contains(err.Error(), "does not match current network") {
		t.Fatalf("network mismatch: %v", err)
	}
	req = frostServerReqCov(members)
	req.ToAddress = ""
	if _, err := server.FrostKeySign(req, sidecar); err == nil || !strings.Contains(err.Error(), "fail to compute frost msgID") {
		t.Fatalf("missing address: %v", err)
	}
	req = frostServerReqCov(members)
	req.SignerPubKeys = []string{testPubKeys[0], testPubKeys[0], testPubKeys[1]}
	if _, err := server.FrostKeySign(req, sidecar); err == nil || !strings.Contains(err.Error(), "fail to compute frost plan hash") {
		t.Fatalf("duplicate signer: %v", err)
	}
	req = frostServerReqCov(members)
	req.ActiveSignerPubKeys = []string{testPubKeys[0]}
	if _, err := server.FrostKeySign(req, sidecar); err == nil || !strings.Contains(err.Error(), "not enough eligible frost signers: need 2 of 3, got 1") {
		t.Fatalf("too few active: %v", err)
	}
	req = frostServerReqCov([]string{"bogus-a", "bogus-b"})
	if _, err := server.FrostKeySign(req, sidecar); err == nil || !strings.Contains(err.Error(), "fail to resolve FROST session peers") {
		t.Fatalf("bad pubkeys: %v", err)
	}
	if len(sidecar.prepareReqs) != 0 {
		t.Fatal("rejected requests must not reach the sidecar")
	}
}

// ---------------------------------------------------------------------------
// join helpers
// ---------------------------------------------------------------------------

func TestValidateFrostJoinPartyCov(t *testing.T) {
	ensureBech32Prefix()
	ids, err := conversion.GetPeerIDsFromPubKeys([]string{testPubKeys[0], testPubKeys[1], testPubKeys[2]})
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := conversion.GetPeerIDFromPubKey(testPubKeys[3])
	if err != nil {
		t.Fatal(err)
	}
	leader := ids[0].String()
	cases := []struct {
		online []peer.ID
		quorum int
		want   string
	}{
		{ids[:2], 0, "need 0"},
		{ids[:1], 2, "has 1 peers; need 2"},
		{[]peer.ID{ids[0], outsider}, 2, "ineligible peer"},
		{[]peer.ID{ids[0], ids[0]}, 2, "duplicate peer"},
		{[]peer.ID{ids[1], ids[2]}, 2, "does not contain its selected leader"},
	}
	for _, tc := range cases {
		err := validateFrostJoinParty(tc.online, ids, leader, tc.quorum)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("online=%v quorum=%d: want %q got %v", tc.online, tc.quorum, tc.want, err)
		}
	}
	if err := validateFrostJoinParty(ids[:2], ids, leader, 2); err != nil {
		t.Fatalf("valid party rejected: %v", err)
	}
}

func TestJoinFrostKeySignPartyCovErrors(t *testing.T) {
	server := newFrostServerCov(t, 0, 100*time.Millisecond, time.Second)
	if _, _, err := server.joinFrostKeySignParty("m", "bad", 1, []string{testPubKeys[0]}, 0, nil); err == nil ||
		!strings.Contains(err.Error(), "fail to parse FROST join rotation version") {
		t.Fatalf("bad version: %v", err)
	}
	if _, _, err := server.joinFrostKeySignParty("m", frostJoinRotationVersion, 1, []string{"bogus"}, 0, nil); err == nil ||
		!strings.Contains(err.Error(), "fail to resolve FROST join peers") {
		t.Fatalf("bad peers: %v", err)
	}
	online, leader, err := server.joinFrostKeySignParty(testHashOf('7'), frostJoinRotationVersion, 1, []string{testPubKeys[0]}, 0, nil)
	if err != nil || len(online) != 1 || leader != server.p2pCommunication.GetLocalPeerID() {
		t.Fatalf("single member join: online=%v leader=%s err=%v", online, leader, err)
	}
}

// ---------------------------------------------------------------------------
// pure helpers
// ---------------------------------------------------------------------------

func TestFrostEventualityCovValidation(t *testing.T) {
	ensureBech32Prefix()
	req := frostServerReqCov([]string{testPubKeys[0], testPubKeys[1]})
	msgID, err := FrostKeySignMsgID(req)
	if err != nil {
		t.Fatal(err)
	}
	planHash, err := FrostPlanHash(req, msgID)
	if err != nil {
		t.Fatal(err)
	}
	txHash := testHashOf('e')
	ev, err := BuildFrostEventuality(planHash, txHash, "deadbeef", testTxKey, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateFrostEventuality(req, msgID, txHash, "deadbeef", testTxKey, 7, ev); err != nil {
		t.Fatalf("valid eventuality rejected: %v", err)
	}
	mutate := func(f func(*FrostEventuality)) FrostEventuality {
		cp := ev
		f(&cp)
		return cp
	}
	cases := []struct {
		name  string
		msgID string
		txHex string
		txKey string
		fee   uint64
		ev    FrostEventuality
		want  string
	}{
		{"empty msg", " ", "deadbeef", testTxKey, 7, ev, "msg_id is required"},
		{"version", msgID, "deadbeef", testTxKey, 7, mutate(func(e *FrostEventuality) { e.Version = 9 }), "unexpected eventuality version 9"},
		{"plan", msgID, "deadbeef", testTxKey, 7, mutate(func(e *FrostEventuality) { e.PlanHash = testHashOf('0') }), "plan_hash mismatch"},
		{"bad hex", msgID, "zz", testTxKey, 7, ev, "fail to derive tx_hex_hash"},
		{"hex mismatch", msgID, "beef", testTxKey, 7, ev, "tx_hex_hash mismatch"},
		{"bad key", msgID, "deadbeef", "XX", 7, ev, "fail to derive tx_key_hash"},
		{"key mismatch", msgID, "deadbeef", strings.Repeat("ab", 32), 7, ev, "tx_key_hash mismatch"},
		{"fee", msgID, "deadbeef", testTxKey, 8, ev, "eventuality_id mismatch"},
	}
	for _, tc := range cases {
		err := ValidateFrostEventuality(req, tc.msgID, txHash, tc.txHex, tc.txKey, tc.fee, tc.ev)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want %q got %v", tc.name, tc.want, err)
		}
	}
	badReq := req
	badReq.VaultPubKey = ""
	if err := ValidateFrostEventuality(badReq, msgID, txHash, "deadbeef", testTxKey, 7, ev); err == nil ||
		!strings.Contains(err.Error(), "fail to derive plan hash") {
		t.Fatalf("bad request: %v", err)
	}
	if err := ValidateFrostEventuality(req, msgID, "short", "deadbeef", testTxKey, 7, ev); err == nil ||
		!strings.Contains(err.Error(), "fail to derive eventuality_id") {
		t.Fatalf("bad tx hash: %v", err)
	}
}

func TestFrostHashHelpersCovErrors(t *testing.T) {
	ensureBech32Prefix()
	if _, err := FrostKeySignMsgID(FrostKeySignRequest{ToAddress: "a"}); err == nil {
		t.Fatal("missing vault accepted")
	}
	if _, err := FrostKeySignMsgID(FrostKeySignRequest{VaultPubKey: "v"}); err == nil {
		t.Fatal("missing address accepted")
	}
	if _, err := FrostKeySignMsgID(FrostKeySignRequest{VaultPubKey: "v", ToAddress: "a", RequiredInputs: []FrostSpentOutputRef{{}}}); err == nil ||
		!strings.Contains(err.Error(), "fail to hash required inputs") {
		t.Fatalf("empty required input: %v", err)
	}
	// required inputs are normalized and order-independent
	a := []FrostSpentOutputRef{{TxHash: "AA", OutputIndex: 2}, {TxHash: "aa", OutputIndex: 1, KeyImage: "K2"}, {TxHash: "aa", OutputIndex: 1, KeyImage: "k1", SpendTxHash: "b"}, {TxHash: "aa", OutputIndex: 1, KeyImage: "k1", SpendTxHash: "a"}}
	b := []FrostSpentOutputRef{a[3], a[2], a[1], a[0]}
	ha, err := frostRequiredInputsHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := frostRequiredInputsHash(b)
	if err != nil || ha != hb {
		t.Fatalf("required inputs hash is order dependent: %s vs %s (%v)", ha, hb, err)
	}

	if _, err := FrostDecoyContext("  "); err == nil {
		t.Fatal("empty decoy context accepted")
	}
	if _, err := frostSignerSetHash([]string{"a", "A"}); err == nil {
		t.Fatal("duplicate signer set accepted")
	}

	base := FrostKeySignRequest{VaultPubKey: "v", ToAddress: "a", SignerPubKeys: []string{"x"}}
	for name, tc := range map[string]struct {
		req   FrostKeySignRequest
		msgID string
		want  string
	}{
		"msgid":    {base, "", "msgID is required"},
		"vault":    {FrostKeySignRequest{ToAddress: "a"}, "m", "vault pubkey is required"},
		"address":  {FrostKeySignRequest{VaultPubKey: "v"}, "m", "to address is required"},
		"signers":  {FrostKeySignRequest{VaultPubKey: "v", ToAddress: "a", SignerPubKeys: []string{"x", "X"}}, "m", "fail to hash signer membership"},
		"active":   {FrostKeySignRequest{VaultPubKey: "v", ToAddress: "a", SignerPubKeys: []string{"x"}, ActiveSignerPubKeys: []string{"y", "Y"}}, "m", "fail to hash active signer membership"},
		"required": {FrostKeySignRequest{VaultPubKey: "v", ToAddress: "a", SignerPubKeys: []string{"x"}, RequiredInputs: []FrostSpentOutputRef{{TxHash: " "}}}, "m", "fail to hash required inputs"},
	} {
		if _, err := FrostPlanHash(tc.req, tc.msgID); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want %q got %v", name, tc.want, err)
		}
	}

	if _, err := FrostTxHexHash(" "); err == nil {
		t.Fatal("empty tx hex accepted")
	}
	if _, err := FrostTxHexHash("xyz"); err == nil {
		t.Fatal("invalid tx hex accepted")
	}
	good := testHashOf('a')
	for i, args := range [][4]string{
		{"x", good, good, good},
		{good, "x", good, good},
		{good, good, "x", good},
		{good, good, good, strings.Repeat("z", 64)},
	} {
		if _, err := FrostEventualityID(args[0], args[1], args[2], args[3], 1); err == nil {
			t.Fatalf("case %d: invalid eventuality input accepted", i)
		}
	}
	if _, err := BuildFrostEventuality(good, good, "", testTxKey, 1); err == nil {
		t.Fatal("empty tx hex accepted")
	}
	if _, err := BuildFrostEventuality(good, good, "aa", "bad", 1); err == nil {
		t.Fatal("bad tx key accepted")
	}
	if _, err := BuildFrostEventuality("bad", good, "aa", testTxKey, 1); err == nil {
		t.Fatal("bad plan hash accepted")
	}
}

func TestFrostSmallHelpersCov(t *testing.T) {
	ensureBech32Prefix()
	if containsNormalizedPubKey([]string{"A"}, " ") {
		t.Fatal("empty target matched")
	}
	if !containsNormalizedPubKey([]string{" Abc "}, "aBC") {
		t.Fatal("normalized match missed")
	}
	if containsNormalizedPubKey([]string{"abc"}, "abd") {
		t.Fatal("unexpected match")
	}
	if releaseFrostSigningClaim(FrostKeySignRequest{}) != nil {
		t.Fatal("nil release hook must be a no-op")
	}
	calls := 0
	if err := releaseFrostSigningClaim(FrostKeySignRequest{ReleaseSigningClaim: func() error { calls++; return nil }}); err != nil || calls != 1 {
		t.Fatalf("release hook calls=%d err=%v", calls, err)
	}

	coord := NewFrostSignCoordinator(nil, nil, " local ", " msg ", " plan ", nil, 0, nil)
	if coord.timeout != 90*time.Second || coord.localPubKey != "local" || coord.msgID != "msg" ||
		coord.expectedPlanHash != "plan" || coord.strictTranscript {
		t.Fatalf("unexpected coordinator defaults: %+v", coord)
	}

	pid, err := conversion.GetPeerIDFromPubKey(testPubKeys[1])
	if err != nil {
		t.Fatal(err)
	}
	resp := coord.failForMalformed(pid, "bad payload", frostRoundShare)
	if resp.Status != tcommon.Fail || len(resp.Blame.BlameNodes) != 1 || resp.Blame.BlameNodes[0].Pubkey != testPubKeys[1] ||
		resp.Blame.Round != frostRoundShare {
		t.Fatalf("malformed blame: %+v", resp.Blame)
	}
	resp = coord.failForMalformed(peer.ID("not-a-real-peer"), "bad payload", frostRoundShare)
	if resp.Status != tcommon.Fail || len(resp.Blame.BlameNodes) != 0 || resp.Blame.FailReason != "bad payload" {
		t.Fatalf("unresolvable peer must not be blamed: %+v", resp.Blame)
	}

	resp = coord.failForEquivocation(messages.FrostEquivocationEvidence{
		First: messages.FrostSignedEnvelope{Round: "something-else"},
	})
	if resp.Blame.Round != "" || resp.Blame.FailReason != frostEquivocationFailReason {
		t.Fatalf("unknown equivocation round must be dropped: %+v", resp.Blame)
	}

	if got := truncateFrostResultReason(strings.Repeat("x", frostMaxResultReasonBytes+10)); len(got) != frostMaxResultReasonBytes {
		t.Fatalf("reason not truncated: %d", len(got))
	}
}

func TestFrostBroadcastResultCov(t *testing.T) {
	ensureBech32Prefix()
	comm := &p2p.Communication{BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 4)}
	coord := NewFrostSignCoordinator(comm, nil, testPubKeys[0], "msg-cov", testExpectedPlanHash, nil, time.Second, nil)
	peers, err := conversion.GetPeerIDsFromPubKeys([]string{testPubKeys[1], testPubKeys[2]})
	if err != nil {
		t.Fatal(err)
	}
	resp := FrostKeySignResponse{
		MsgID: "msg-cov", TxHash: testHashOf('e'), TxHex: "aa", TxKey: testTxKey, FeeAtomic: 3,
		Status: tcommon.Fail, Blame: buildBlame("why", frostRoundShare, []string{testPubKeys[1]}),
	}
	if err := coord.BroadcastResult(resp, peers); err != nil {
		t.Fatal(err)
	}
	msg := <-comm.BroadcastMsgChan
	if msg.WrappedMessage.MessageType != messages.TSSFrostResultMsg || msg.WrappedMessage.MsgID != "msg-cov" || len(msg.PeersID) != 2 {
		t.Fatalf("unexpected broadcast envelope: %+v", msg)
	}
	var payload FrostResultPayload
	if err := json.Unmarshal(msg.WrappedMessage.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SenderPubKey != testPubKeys[0] || payload.Response.TxHash != resp.TxHash ||
		payload.Response.Status != uint8(tcommon.Fail) || payload.Response.Blame.FailReason != "why" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

// junkFramesCov derives malformed, spoofed and unauthorized variants of a real
// round frame. Every variant must be ignored by the receiver.
func junkFramesCov(t *testing.T, members []string, peerIDs []peer.ID, from, to int, msg *messages.BroadcastMsgChan) []*p2p.Message {
	t.Helper()
	outsider, err := conversion.GetPeerIDFromPubKey(testPubKeys[3])
	if err != nil {
		t.Fatal(err)
	}
	wrap := func(w messages.WrappedMessage) []byte {
		raw, err := json.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	sender := peerIDs[from]
	real := wrap(msg.WrappedMessage)
	out := []*p2p.Message{
		{PeerID: outsider, Payload: real},             // not an active peer
		{PeerID: sender, Payload: []byte("not json")}, // undecodable frame
	}
	other := msg.WrappedMessage
	other.MsgID = "some-other-session"
	out = append(out, &p2p.Message{PeerID: sender, Payload: wrap(other)})
	broken := msg.WrappedMessage
	broken.Payload = []byte("{")
	out = append(out, &p2p.Message{PeerID: sender, Payload: wrap(broken)})

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(msg.WrappedMessage.Payload, &fields); err != nil {
		t.Fatal(err)
	}
	variant := func(edit func(map[string]json.RawMessage)) {
		cp := make(map[string]json.RawMessage, len(fields))
		for k, v := range fields {
			cp[k] = v
		}
		edit(cp)
		inner, err := json.Marshal(cp)
		if err != nil {
			t.Fatal(err)
		}
		w := msg.WrappedMessage
		w.Payload = inner
		out = append(out, &p2p.Message{PeerID: sender, Payload: wrap(w)})
	}
	str := func(v string) json.RawMessage {
		raw, _ := json.Marshal(v)
		return raw
	}
	variant(func(m map[string]json.RawMessage) { m["sender_pub_key"] = str("") })
	variant(func(m map[string]json.RawMessage) { m["sender_pub_key"] = str(testPubKeys[3]) })
	variant(func(m map[string]json.RawMessage) { m["sender_pub_key"] = str(members[to]) })
	switch msg.WrappedMessage.MessageType {
	case messages.TSSFrostPreprocessMsg, messages.TSSFrostShareMsg:
		variant(func(m map[string]json.RawMessage) { m["participant_index"] = json.RawMessage("4242") })
		variant(func(m map[string]json.RawMessage) {
			m["preprocess_hex"] = str(" ")
			m["share_hex"] = str(" ")
		})
		if _, signed := fields["signature"]; signed {
			variant(func(m map[string]json.RawMessage) { delete(m, "signature") })
		}
	case messages.TSSFrostVerifyMsg:
		variant(func(m map[string]json.RawMessage) { m["round"] = str("bogus-round") })
		variant(func(m map[string]json.RawMessage) { m["payload_hash"] = str("zz") })
		if _, signed := fields["attester_signature"]; signed {
			variant(func(m map[string]json.RawMessage) { delete(m, "attester_signature") })
		}
	}
	return out
}

func TestFrostExecuteCovIgnoresJunkFrames(t *testing.T) {
	for _, strict := range []bool{true, false} {
		members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
		msgID := testHashOf('8')
		if !strict {
			msgID = testHashOf('9')
		}
		r := newFrostRouterCov(t, members, msgID, testExpectedPlanHash, strict, 5*time.Second)
		for i, coord := range r.coords {
			coord.sidecar = newFrostCombineSidecarCov("ab0"+string(rune('0'+i)), "cd0"+string(rune('0'+i)))
		}
		r.inject = func(from, to int, msg *messages.BroadcastMsgChan) []*p2p.Message {
			return junkFramesCov(t, members, r.peerIDs, from, to, msg)
		}
		r.start()
		results := r.executeAll(frostThreeMemberReqCov(members), members)
		r.stop()
		for i, resp := range results {
			if resp.Status != tcommon.Success || resp.TxHash != testHashOf('e') {
				t.Fatalf("strict=%v party %d did not survive junk frames: %+v", strict, i, resp.Blame)
			}
		}
	}
}

func TestNormalizeResultPayloadCovRejections(t *testing.T) {
	ensureBech32Prefix()
	msgID := testHashOf('1')
	valid := mustSuccessResult(t, msgID, testExpectedPlanHash, testHashOf('e'), "deadbeef", 5)
	if _, key, err := normalizeResultPayload(valid, msgID, testExpectedPlanHash); err != nil || key == "" {
		t.Fatalf("valid success rejected: %v", err)
	}
	mut := func(f func(*FrostResult)) FrostResult {
		cp := valid
		cp.SpentOutputRefs = append([]FrostSpentOutputRef(nil), valid.SpentOutputRefs...)
		f(&cp)
		return cp
	}
	cases := map[string]struct {
		res  FrostResult
		plan string
		want string
	}{
		"status":         {mut(func(r *FrostResult) { r.Status = 7 }), testExpectedPlanHash, "invalid frost result status"},
		"txhex limit":    {mut(func(r *FrostResult) { r.TxHex = strings.Repeat("a", frostMaxResultTxHexChars+1) }), testExpectedPlanHash, "tx_hex exceeds limit"},
		"refs limit":     {mut(func(r *FrostResult) { r.SpentOutputRefs = make([]FrostSpentOutputRef, frostMaxResultSpentRefs+1) }), testExpectedPlanHash, "spent refs exceeds limit"},
		"empty msgid":    {mut(func(r *FrostResult) { r.MsgID = " " }), testExpectedPlanHash, "empty msg_id"},
		"msgid":          {mut(func(r *FrostResult) { r.MsgID = "other" }), testExpectedPlanHash, "msg_id mismatch"},
		"txhash":         {mut(func(r *FrostResult) { r.TxHash = "" }), testExpectedPlanHash, "empty tx_hash"},
		"txhex":          {mut(func(r *FrostResult) { r.TxHex = "" }), testExpectedPlanHash, "empty tx_hex"},
		"txkey":          {mut(func(r *FrostResult) { r.TxKey = "nope" }), testExpectedPlanHash, "invalid tx_key"},
		"hex":            {mut(func(r *FrostResult) { r.TxHex = "zz" }), testExpectedPlanHash, "tx_hex is not valid hex"},
		"short hash":     {mut(func(r *FrostResult) { r.TxHash = "abcd" }), testExpectedPlanHash, "tx_hash must be"},
		"no plan":        {valid, " ", "expected plan hash is empty"},
		"version":        {mut(func(r *FrostResult) { r.Eventuality.Version = 1 }), testExpectedPlanHash, "invalid frost eventuality version"},
		"plan":           {valid, testHashOf('0'), "plan_hash mismatch"},
		"txhexhash":      {mut(func(r *FrostResult) { r.Eventuality.TxHexHash = "x" }), testExpectedPlanHash, "tx_hex_hash must be"},
		"txkeyhash":      {mut(func(r *FrostResult) { r.Eventuality.TxKeyHash = testHashOf('0') }), testExpectedPlanHash, "tx_key_hash mismatch"},
		"eventuality":    {mut(func(r *FrostResult) { r.Eventuality.EventualityID = "x" }), testExpectedPlanHash, "eventuality_id must be"},
		"txhex mismatch": {mut(func(r *FrostResult) { r.TxHex = "beef" }), testExpectedPlanHash, "tx_hex_hash mismatch"},
		"fee":            {mut(func(r *FrostResult) { r.FeeAtomic = 6 }), testExpectedPlanHash, "eventuality_id mismatch"},
		"refs":           {mut(func(r *FrostResult) { r.SpentOutputRefs = nil }), testExpectedPlanHash, "has no spent refs"},
		"fail msgid": {
			FrostResult{MsgID: "other", Status: uint8(tcommon.Fail), Blame: buildBlame("r", "", []string{testPubKeys[0]})},
			testExpectedPlanHash, "fail result msg_id mismatch",
		},
		"fail nodes": {
			FrostResult{Status: uint8(tcommon.Fail), Blame: blame.Blame{FailReason: "r", BlameNodes: make([]blame.Node, frostMaxResultBlameNodes+1)}},
			testExpectedPlanHash, "blame nodes exceeds limit",
		},
		"fail blame": {FrostResult{Status: uint8(tcommon.Fail), Blame: blame.Blame{FailReason: "r"}}, testExpectedPlanHash, "missing blame nodes"},
	}
	for name, tc := range cases {
		if _, _, err := normalizeResultPayload(tc.res, msgID, tc.plan); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want %q got %v", name, tc.want, err)
		}
	}
}

func TestNormalizeBlameForResultCov(t *testing.T) {
	cases := map[string]struct {
		in   blame.Blame
		want string
	}{
		"reason":   {blame.Blame{}, "missing blame reason"},
		"long":     {blame.Blame{FailReason: strings.Repeat("r", frostMaxResultReasonBytes+1)}, "reason exceeds limit"},
		"nodes":    {blame.Blame{FailReason: "r", BlameNodes: make([]blame.Node, frostMaxResultBlameNodes+1)}, "blame nodes exceeds limit"},
		"none":     {blame.Blame{FailReason: "r"}, "missing blame nodes"},
		"evidence": {blame.Blame{FailReason: "r", BlameNodes: []blame.Node{{Pubkey: "a", BlameData: make([]byte, frostMaxResultEvidenceBytes+1)}}}, "evidence exceeds limit"},
		"empty":    {blame.Blame{FailReason: "r", BlameNodes: []blame.Node{{Pubkey: " "}}}, "empty blame node pubkey"},
	}
	for name, tc := range cases {
		if _, err := normalizeBlameForResult(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want %q got %v", name, tc.want, err)
		}
	}
	got, err := normalizeBlameForResult(blame.Blame{
		FailReason: " why ", Round: " r ", IsUnicast: true,
		BlameNodes: []blame.Node{{Pubkey: "b"}, {Pubkey: " a "}, {Pubkey: "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.FailReason != "why" || got.Round != "r" || !got.IsUnicast || len(got.BlameNodes) != 2 ||
		got.BlameNodes[0].Pubkey != "a" || got.BlameNodes[1].Pubkey != "b" {
		t.Fatalf("blame not normalized: %+v", got)
	}
}

func TestFrostWaitForResultCovQuorumValidation(t *testing.T) {
	ensureBech32Prefix()
	c := newTestFrostSignCoordinator(testPubKeys[0], "m", newFrostSignInbox(), 20*time.Millisecond)
	if _, err := c.WaitForResult([]string{"a", "A"}, 1, time.Millisecond); err == nil {
		t.Fatal("duplicate participants accepted")
	}
	if _, err := c.WaitForResult([]string{testPubKeys[0]}, 2, time.Millisecond); err == nil ||
		!strings.Contains(err.Error(), "only 1 active participants") {
		t.Fatalf("quorum above participants: %v", err)
	}
	if _, err := c.WaitForResult(nil, 0, time.Millisecond); err == nil || !strings.Contains(err.Error(), "signer pub keys are empty") {
		t.Fatalf("empty participants: %v", err)
	}
	// quorum <= 0 is derived from the threshold; timeout <= 0 uses the
	// coordinator default and still reports the timeout sentinel
	if _, err := c.WaitForResult([]string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}, 0, 0); !errors.Is(err, errFrostResultTimeout) {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestActiveSignerSetMissingPubKeysCov(t *testing.T) {
	set := &activeSignerSet{originalByNormalized: map[string]string{"a": "A", "b": "B", "c": "C"}}
	got := set.missingPubKeys(map[string]struct{}{"b": {}})
	if strings.Join(got, ",") != "A,C" {
		t.Fatalf("missing: %v", got)
	}
	if _, err := buildActiveSet([]string{" ", "a"}, map[string]string{"a": "A"}); err != nil {
		t.Fatalf("blank participants must be skipped: %v", err)
	}
	if b := buildBlame("r", "x", []string{" ", "k", "k"}); len(b.BlameNodes) != 1 {
		t.Fatalf("blame nodes not deduplicated: %+v", b.BlameNodes)
	}
	if _, ok := parseWrappedMessage([]byte("{")); ok {
		t.Fatal("bad wrapped message parsed")
	}
}
