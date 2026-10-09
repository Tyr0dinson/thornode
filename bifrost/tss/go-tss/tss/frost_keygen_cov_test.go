package tss

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

const (
	keygenCovAddress  = "48xmrkeygencovaddress"
	keygenCovMsgIDHex = "keygen-cov-msg"
)

// keygenRouterCov wires in-process FROST keygen coordinators together the way
// the p2p layer does: every broadcast lands in the addressed peers' per-topic
// inbox, tagged with the authenticated sender peer ID.
type keygenRouterCov struct {
	members  []string
	coords   []*FrostKeygenCoordinator
	sidecars []*mockFrostSidecarKeygen
	peerIDs  []peer.ID
	active   *activeSignerSet
	indexMap map[string]uint32
	done     chan struct{}
	wg       sync.WaitGroup
	mutate   func(from int, msg *messages.BroadcastMsgChan) *messages.BroadcastMsgChan
	// junk surrounds every real frame with benign garbage (nil, undecodable,
	// foreign session) and a duplicate of the real frame.
	junk bool
}

func newKeygenRouterCov(t *testing.T, members []string, timeout time.Duration) *keygenRouterCov {
	t.Helper()
	ensureBech32Prefix()
	_, indexMap, byNorm, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatal(err)
	}
	active, err := buildActiveSet(members, byNorm)
	if err != nil {
		t.Fatal(err)
	}
	r := &keygenRouterCov{members: members, active: active, indexMap: indexMap, done: make(chan struct{})}
	for i, member := range members {
		pid, err := conversion.GetPeerIDFromPubKey(member)
		if err != nil {
			t.Fatal(err)
		}
		r.peerIDs = append(r.peerIDs, pid)
		local := strconv.FormatUint(uint64(indexMap[normalizePubKey(member)]), 10)
		shares := map[string]string{}
		for _, other := range members {
			idx := strconv.FormatUint(uint64(indexMap[normalizePubKey(other)]), 10)
			if idx != local {
				shares[idx] = "share-" + local + "-to-" + idx
			}
		}
		sidecar := &mockFrostSidecarKeygen{
			round1Resp:   FrostSidecarDKGRound1Resp{Commitments: "commit-" + local},
			round2Resp:   FrostSidecarDKGRound2Resp{EncryptedShares: shares},
			round3Resp:   FrostSidecarDKGRound3Resp{Address: keygenCovAddress, GroupKey: strings.Repeat("cd", 32)},
			finalizeResp: FrostSidecarDKGFinalizeResp{Success: true, KeyBytesHex: "abcd0" + local},
			chainHeight:  int64(5000 + i),
		}
		r.sidecars = append(r.sidecars, sidecar)
		r.coords = append(r.coords, &FrostKeygenCoordinator{
			logger:           zerolog.Nop(),
			p2pCommunication: &p2p.Communication{BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 256)},
			sidecar:          sidecar,
			localPubKey:      member,
			msgID:            keygenCovMsgIDHex,
			inbox:            newFrostKeygenInbox(),
			timeout:          timeout,
		})
	}
	return r
}

func (r *keygenRouterCov) start() {
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
						if msg = r.mutate(from, msg); msg == nil {
							continue
						}
					}
					raw, err := json.Marshal(msg.WrappedMessage)
					if err != nil {
						continue
					}
					for _, target := range msg.PeersID {
						for j, pid := range r.peerIDs {
							if j == from || pid != target {
								continue
							}
							for _, sub := range r.coords[j].inbox.subscriptions() {
								if sub.msgType != msg.WrappedMessage.MessageType {
									continue
								}
								real := &p2p.Message{PeerID: r.peerIDs[from], Payload: raw}
								deliveries := []*p2p.Message{real}
								if r.junk {
									foreign := msg.WrappedMessage
									foreign.MsgID = "foreign-session"
									foreignRaw, _ := json.Marshal(foreign)
									deliveries = []*p2p.Message{
										nil,
										{PeerID: r.peerIDs[from], Payload: []byte("not json")},
										{PeerID: r.peerIDs[from], Payload: foreignRaw},
										real,
										real,
									}
								}
								for _, d := range deliveries {
									select {
									case sub.messageChan <- d:
									case <-r.done:
										return
									}
								}
							}
						}
					}
				}
			}
		}(i)
	}
}

func (r *keygenRouterCov) stop() {
	close(r.done)
	r.wg.Wait()
}

func (r *keygenRouterCov) executeAll(req FrostKeygenRequest) []FrostKeygenResponse {
	out := make([]FrostKeygenResponse, len(r.coords))
	var wg sync.WaitGroup
	for i := range r.coords {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i] = r.coords[i].Execute(req, r.active, r.peerIDs)
		}(i)
	}
	wg.Wait()
	return out
}

func keygenCovReq(members []string) FrostKeygenRequest {
	return FrostKeygenRequest{VaultPubKey: "thorpub1test-vault", SignerPubKeys: members, BlockHeight: 10}
}

// rewritePayloadCov decodes a wrapped payload, lets f edit it and re-encodes.
func rewritePayloadCov[T any](msg *messages.BroadcastMsgChan, f func(*T)) *messages.BroadcastMsgChan {
	var payload T
	if err := json.Unmarshal(msg.WrappedMessage.Payload, &payload); err != nil {
		return msg
	}
	f(&payload)
	raw, err := json.Marshal(payload)
	if err != nil {
		return msg
	}
	cp := *msg
	cp.WrappedMessage.Payload = raw
	return &cp
}

func TestFrostKeygenCovThreePartySuccess(t *testing.T) {
	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	r := newKeygenRouterCov(t, members, 5*time.Second)
	r.junk = true
	r.start()
	defer r.stop()

	results := r.executeAll(keygenCovReq(members))
	for i, resp := range results {
		if resp.Status != tcommon.Success {
			t.Fatalf("party %d failed: %+v", i, resp.Blame)
		}
		if resp.Address != keygenCovAddress || resp.GroupKey != strings.Repeat("cd", 32) {
			t.Fatalf("party %d result: %+v", i, resp)
		}
		// birthday is the lower median of observed heights minus the margin
		if resp.ObservedMoneroHeight != 5001-frostDKGReorgMargin {
			t.Fatalf("party %d birthday %d", i, resp.ObservedMoneroHeight)
		}
		sc := r.sidecars[i]
		if len(sc.round2Reqs) != 1 || len(sc.round2Reqs[0].AllCommitments) != 3 {
			t.Fatalf("party %d round2 commitments: %+v", i, sc.round2Reqs)
		}
		if len(sc.round3Reqs) != 1 || len(sc.round3Reqs[0].AllShares) != 2 {
			t.Fatalf("party %d round3 shares: %+v", i, sc.round3Reqs)
		}
		local := strconv.FormatUint(uint64(r.indexMap[normalizePubKey(members[i])]), 10)
		for sender, share := range sc.round3Reqs[0].AllShares {
			if share != "share-"+sender+"-to-"+local {
				t.Fatalf("party %d got share %q from %s", i, share, sender)
			}
		}
		if len(sc.finalizeReqs) != 1 || sc.finalizeReqs[0].Address != keygenCovAddress {
			t.Fatalf("party %d finalize: %+v", i, sc.finalizeReqs)
		}
		if sc.round1Reqs[0].Threshold != 2 || sc.round1Reqs[0].N != 3 {
			t.Fatalf("party %d threshold/n: %+v", i, sc.round1Reqs[0])
		}
	}
}

func TestFrostKeygenCovRound2TimeoutBlamesSilentPeer(t *testing.T) {
	t.Parallel()
	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	r := newKeygenRouterCov(t, members, 300*time.Millisecond)
	r.mutate = func(from int, msg *messages.BroadcastMsgChan) *messages.BroadcastMsgChan {
		if from == 2 && msg.WrappedMessage.MessageType == messages.TSSFrostDKGRound2Msg {
			return nil
		}
		return msg
	}
	r.start()
	defer r.stop()

	results := r.executeAll(keygenCovReq(members))
	for i := 0; i < 2; i++ {
		resp := results[i]
		if resp.Status != tcommon.Fail || resp.Blame.Round != frostDKGRound2 {
			t.Fatalf("party %d: %+v", i, resp)
		}
		blamed := false
		for _, n := range resp.Blame.BlameNodes {
			if n.Pubkey == members[2] {
				blamed = true
			}
		}
		if !blamed {
			t.Fatalf("party %d did not blame the silent peer: %+v", i, resp.Blame)
		}
		if len(r.sidecars[i].round3Reqs) != 0 {
			t.Fatalf("party %d continued to round3", i)
		}
	}
}

func TestFrostKeygenCovIncompleteRound2PayloadBlamesSender(t *testing.T) {
	t.Parallel()
	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	r := newKeygenRouterCov(t, members, 500*time.Millisecond)
	// party 2 omits the encrypted share addressed to party 0
	idx0 := strconv.FormatUint(uint64(r.indexMap[normalizePubKey(members[0])]), 10)
	delete(r.sidecars[2].round2Resp.EncryptedShares, idx0)
	r.start()
	defer r.stop()

	results := r.executeAll(keygenCovReq(members))
	for i := 0; i < 2; i++ {
		resp := results[i]
		if resp.Status != tcommon.Fail || resp.Blame.Round != frostDKGRound2 ||
			!strings.Contains(resp.Blame.FailReason, "missing encrypted share target in round2 payload") {
			t.Fatalf("party %d: %+v", i, resp)
		}
		if len(resp.Blame.BlameNodes) != 1 || resp.Blame.BlameNodes[0].Pubkey != members[2] {
			t.Fatalf("party %d must blame the incomplete sender: %+v", i, resp.Blame.BlameNodes)
		}
		if len(r.sidecars[i].round3Reqs) != 0 {
			t.Fatalf("party %d continued to round3", i)
		}
	}
}

func TestFrostKeygenCovRound3ResultMismatch(t *testing.T) {
	t.Parallel()
	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	r := newKeygenRouterCov(t, members, 300*time.Millisecond)
	r.sidecars[2].round3Resp.Address = "48xmrdifferentaddress"
	r.start()
	defer r.stop()

	results := r.executeAll(keygenCovReq(members))
	for i, resp := range results {
		if resp.Status != tcommon.Fail || resp.Blame.Round != frostDKGRound3 {
			t.Fatalf("party %d must fail round3 on divergent addresses: %+v", i, resp)
		}
		if len(r.sidecars[i].finalizeReqs) != 0 {
			t.Fatalf("party %d finalized a divergent result", i)
		}
	}
}

func TestFrostKeygenCovRejectsSpoofedRound1Sender(t *testing.T) {
	t.Parallel()
	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	r := newKeygenRouterCov(t, members, 300*time.Millisecond)
	// party 2 claims to be party 1 in its round1 payload
	r.mutate = func(from int, msg *messages.BroadcastMsgChan) *messages.BroadcastMsgChan {
		if from == 2 && msg.WrappedMessage.MessageType == messages.TSSFrostDKGRound1Msg {
			return rewritePayloadCov(msg, func(p *FrostDKGRound1Payload) { p.SenderPubKey = members[1] })
		}
		return msg
	}
	r.start()
	defer r.stop()

	results := r.executeAll(keygenCovReq(members))
	for i := 0; i < 2; i++ {
		resp := results[i]
		if resp.Status != tcommon.Fail || resp.Blame.Round != frostDKGRound1 {
			t.Fatalf("party %d: %+v", i, resp)
		}
		// the transport-authenticated sender is blamed, not the impersonated key
		if len(resp.Blame.BlameNodes) != 1 || resp.Blame.BlameNodes[0].Pubkey != members[2] {
			t.Fatalf("party %d blamed %+v", i, resp.Blame.BlameNodes)
		}
	}
}

func TestFrostKeygenCovExecuteValidation(t *testing.T) {
	ensureBech32Prefix()
	members := []string{testPubKeys[0], testPubKeys[1]}
	_, indexMap, byNorm, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatal(err)
	}
	active, err := buildActiveSet(members, byNorm)
	if err != nil {
		t.Fatal(err)
	}
	partial, err := buildActiveSet(members[:1], byNorm)
	if err != nil {
		t.Fatal(err)
	}
	_ = indexMap
	newCoord := func(local, msgID string, sidecar FrostSidecarKeygenClient) *FrostKeygenCoordinator {
		c := newTestFrostKeygenCoordinator(local, msgID, newFrostKeygenInbox(), time.Second)
		c.sidecar = sidecar
		return c
	}
	sc := &mockFrostSidecarKeygen{}
	cases := []struct {
		name   string
		coord  *FrostKeygenCoordinator
		req    FrostKeygenRequest
		active *activeSignerSet
		want   string
	}{
		{"nil sidecar", newCoord(members[0], "m", nil), keygenCovReq(members), active, "frost sidecar client is nil"},
		{"empty msgid", newCoord(members[0], "", sc), keygenCovReq(members), active, "frost keygen msgID is empty"},
		{"duplicate", newCoord(members[0], "m", sc), keygenCovReq([]string{members[0], strings.ToUpper(members[0])}), active, "duplicate signer pubkey"},
		{"not member", newCoord(testPubKeys[3], "m", sc), keygenCovReq(members), active, "local signer is not in participant set"},
		{"not active", newCoord(members[1], "m", sc), keygenCovReq(members), partial, "local signer is not in active signer set"},
		{"partial", newCoord(members[0], "m", sc), keygenCovReq(members), partial, "frost keygen requires all signers online"},
	}
	for _, tc := range cases {
		resp := tc.coord.Execute(tc.req, tc.active, nil)
		if resp.Status != tcommon.Fail || !strings.Contains(resp.Blame.FailReason, tc.want) {
			t.Fatalf("%s: %+v", tc.name, resp)
		}
	}
	if len(sc.round1Reqs) != 0 {
		t.Fatal("validation failures must not reach the sidecar")
	}
}

func TestFrostKeygenCovSingleSignerSidecarFailures(t *testing.T) {
	ensureBech32Prefix()
	members := []string{testPubKeys[0]}
	_, _, byNorm, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatal(err)
	}
	active, err := buildActiveSet(members, byNorm)
	if err != nil {
		t.Fatal(err)
	}
	ok := func() *mockFrostSidecarKeygen {
		return &mockFrostSidecarKeygen{
			round1Resp:   FrostSidecarDKGRound1Resp{Commitments: "c"},
			round2Resp:   FrostSidecarDKGRound2Resp{EncryptedShares: map[string]string{}},
			round3Resp:   FrostSidecarDKGRound3Resp{Address: keygenCovAddress, GroupKey: "ab"},
			finalizeResp: FrostSidecarDKGFinalizeResp{Success: true, KeyBytesHex: "ab"},
			chainHeight:  1000,
		}
	}
	run := func(sc *mockFrostSidecarKeygen, req FrostKeygenRequest) FrostKeygenResponse {
		c := newTestFrostKeygenCoordinator(members[0], "m", newFrostKeygenInbox(), time.Second)
		c.sidecar = sc
		return c.Execute(req, active, nil)
	}
	req := keygenCovReq(members)

	if resp := run(ok(), req); resp.Status != tcommon.Success || resp.ObservedMoneroHeight != 1000-frostDKGReorgMargin || resp.KeyBytesHex != "ab" {
		t.Fatalf("single signer success: %+v", resp)
	}

	sc := ok()
	sc.round2Err = errTestCov("r2 down")
	if resp := run(sc, req); resp.Status != tcommon.Fail || !strings.Contains(resp.Blame.FailReason, "frost dkg round2 failed: r2 down") {
		t.Fatalf("round2: %+v", resp)
	}
	sc = ok()
	sc.round3Err = errTestCov("r3 down")
	if resp := run(sc, req); resp.Status != tcommon.Fail || !strings.Contains(resp.Blame.FailReason, "frost dkg round3 failed: r3 down") {
		t.Fatalf("round3: %+v", resp)
	}
	sc = ok()
	sc.round3Resp.GroupKey = ""
	if resp := run(sc, req); resp.Status != tcommon.Fail || resp.Blame.FailReason != "empty frost keygen result" {
		t.Fatalf("empty result: %+v", resp)
	}
	sc = ok()
	sc.chainErr = errTestCov("no daemon")
	if resp := run(sc, req); resp.Status != tcommon.Fail || !strings.Contains(resp.Blame.FailReason, "no positive observed monero height") {
		t.Fatalf("chain height: %+v", resp)
	}
	expected := req
	expected.ExpectedAddr = "48xmrsomethingelse"
	if resp := run(ok(), expected); resp.Status != tcommon.Fail || !strings.Contains(resp.Blame.FailReason, "does not match expected address") {
		t.Fatalf("expected address: %+v", resp)
	}
	sc = ok()
	sc.finalizeErr = errTestCov("finalize down")
	if resp := run(sc, req); resp.Status != tcommon.Fail || !strings.Contains(resp.Blame.FailReason, "frost dkg finalize failed: finalize down") {
		t.Fatalf("finalize: %+v", resp)
	}
}

type errTestCov string

func (e errTestCov) Error() string { return string(e) }

func TestFrostKeygenCovServerValidationAndJoinFailure(t *testing.T) {
	t.Parallel()
	server := newFrostServerCov(t, 0, 200*time.Millisecond, time.Second)
	sc := &mockFrostSidecarKeygen{}
	if _, err := server.FrostKeygen(keygenCovReq([]string{testPubKeys[0]}), nil); err == nil ||
		!strings.Contains(err.Error(), "sidecar keygen client is nil") {
		t.Fatalf("nil sidecar: %v", err)
	}
	if _, err := server.FrostKeygen(keygenCovReq(nil), sc); err == nil || !strings.Contains(err.Error(), "signer pub keys are empty") {
		t.Fatalf("empty signers: %v", err)
	}
	if _, err := server.FrostKeygen(FrostKeygenRequest{SignerPubKeys: []string{testPubKeys[0]}}, sc); err == nil ||
		!strings.Contains(err.Error(), "fail to compute frost keygen msgID") {
		t.Fatalf("no vault: %v", err)
	}
	if _, err := server.FrostKeygen(keygenCovReq([]string{"bogus"}), sc); err == nil ||
		!strings.Contains(err.Error(), "fail to resolve frost keygen session peers") {
		t.Fatalf("bad peers: %v", err)
	}
	// an offline co-signer prevents the full-participation party from forming
	resp, err := server.FrostKeygen(keygenCovReq([]string{testPubKeys[0], testPubKeys[1]}), sc)
	if err != nil || resp.Status != tcommon.Fail || resp.Blame.FailReason == "" {
		t.Fatalf("join failure: resp=%+v err=%v", resp, err)
	}
	if len(sc.round1Reqs) != 0 {
		t.Fatal("sidecar must not run without a formed party")
	}
}

func TestFrostKeygenCovHelpers(t *testing.T) {
	ensureBech32Prefix()
	if _, err := FrostDKGContext(" "); err == nil {
		t.Fatal("empty dkg context accepted")
	}
	if _, err := FrostKeygenMsgID(FrostKeygenRequest{VaultPubKey: "v", SignerPubKeys: []string{"a", "A"}}); err == nil {
		t.Fatal("duplicate keygen members accepted")
	}
	if got := frostKeygenJoinThreshold(0); got != 0 {
		t.Fatalf("join threshold for 0: %d", got)
	}
	if got := frostKeygenJoinThreshold(4); got != 3 {
		t.Fatalf("join threshold for 4: %d", got)
	}
	if frostKeygenAdoptionQuorum(0) != 0 || frostKeygenAdoptionQuorum(3) != 2 || frostKeygenAdoptionQuorum(4) != 3 {
		t.Fatal("adoption quorum must be ceil(2n/3)")
	}
	if got := missingSignersForFullParticipation(nil, map[string]string{"b": "B", "a": "A"}); strings.Join(got, ",") != "A,B" {
		t.Fatalf("nil active set must report every signer missing: %v", got)
	}

	var nilState *frostKeygenRoundState
	if _, err := nilState.verifier(frostDKGRound1); err == nil {
		t.Fatal("nil round state accepted")
	}
	if _, err := (&frostKeygenRoundState{verifiers: map[string]*frostKeygenRoundVerifier{}}).verifier("bogus"); err == nil {
		t.Fatal("unknown round accepted")
	}

	timer := time.NewTimer(time.Hour)
	resetFrostKeygenRoundTimer(timer, time.Millisecond)
	<-timer.C
	// resetting an already-fired, drained timer must not block
	resetFrostKeygenRoundTimer(timer, time.Millisecond)
	<-timer.C

	c := NewFrostKeygenCoordinator(nil, nil, " k ", " m ", nil, time.Second)
	if c.timeout != minFrostKeygenTimeout || c.inbox == nil || c.localPubKey != "k" || c.msgID != "m" {
		t.Fatalf("coordinator defaults: %+v", c)
	}

	pid, err := conversion.GetPeerIDFromPubKey(testPubKeys[1])
	if err != nil {
		t.Fatal(err)
	}
	if resp := c.failForMalformed(pid, "bad", frostDKGRound1); len(resp.Blame.BlameNodes) != 1 || resp.Blame.BlameNodes[0].Pubkey != testPubKeys[1] {
		t.Fatalf("malformed blame: %+v", resp.Blame)
	}
	if resp := c.failForMalformed(peer.ID("x"), "bad", frostDKGRound1); len(resp.Blame.BlameNodes) != 0 {
		t.Fatalf("unresolvable peer blamed: %+v", resp.Blame)
	}
	if err := c.validateSenderPeerBinding(pid, "garbage"); err == nil {
		t.Fatal("bad sender accepted")
	}
	if err := c.validateSenderPeerBinding(pid, testPubKeys[2]); err == nil {
		t.Fatal("peer binding mismatch accepted")
	}

	for name, res := range map[string]FrostDKGResult{
		"status":  {Status: 9},
		"address": {Status: uint8(tcommon.Success), GroupKey: "ab", ObservedMoneroHeight: 1},
		"key":     {Status: uint8(tcommon.Success), Address: "a", ObservedMoneroHeight: 1},
		"height":  {Status: uint8(tcommon.Success), Address: "a", GroupKey: "ab"},
	} {
		if _, _, err := normalizeDKGResultPayload(res); err == nil {
			t.Fatalf("%s: invalid result accepted", name)
		}
	}
	norm, key, err := normalizeDKGResultPayload(FrostDKGResult{Status: uint8(tcommon.Success), Address: " a ", GroupKey: "AB", ObservedMoneroHeight: 3})
	if err != nil || norm.Address != "a" || norm.GroupKey != "ab" || key == "" {
		t.Fatalf("normalized success: %+v %q %v", norm, key, err)
	}
}

func TestFrostKeygenCovMalformedRoundPayloadsBlameSender(t *testing.T) {
	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	setField := func(field string, value interface{}) func(map[string]interface{}) {
		return func(m map[string]interface{}) { m[field] = value }
	}
	cases := []struct {
		name    string
		msgType messages.THORChainTSSMessageType
		round   string
		edit    func(map[string]interface{}) // nil means "replace payload with invalid JSON"
		reason  string
	}{
		{"r1 json", messages.TSSFrostDKGRound1Msg, frostDKGRound1, nil, "invalid round1 payload"},
		{"r1 index", messages.TSSFrostDKGRound1Msg, frostDKGRound1, setField("participant_index", 77), "participant index mismatch in round1 payload"},
		{"r1 commitments", messages.TSSFrostDKGRound1Msg, frostDKGRound1, setField("commitments", " "), "empty commitments in round1 payload"},
		{"r1 sender", messages.TSSFrostDKGRound1Msg, frostDKGRound1, setField("sender_pub_key", ""), "empty sender pubkey in round1 payload"},
		{"r2 json", messages.TSSFrostDKGRound2Msg, frostDKGRound2, nil, "invalid round2 payload"},
		{"r2 index", messages.TSSFrostDKGRound2Msg, frostDKGRound2, setField("participant_index", 77), "participant index mismatch in round2 payload"},
		{"r3 json", messages.TSSFrostDKGRound3Msg, frostDKGRound3, nil, "invalid round3 payload"},
		{"r3 index", messages.TSSFrostDKGRound3Msg, frostDKGRound3, setField("participant_index", 77), "participant index mismatch in round3 payload"},
		{"r3 address", messages.TSSFrostDKGRound3Msg, frostDKGRound3, setField("address", " "), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newKeygenRouterCov(t, members, 250*time.Millisecond)
			r.mutate = func(from int, msg *messages.BroadcastMsgChan) *messages.BroadcastMsgChan {
				if from != 2 || msg.WrappedMessage.MessageType != tc.msgType {
					return msg
				}
				cp := *msg
				if tc.edit == nil {
					cp.WrappedMessage.Payload = []byte("{")
					return &cp
				}
				var m map[string]interface{}
				if err := json.Unmarshal(msg.WrappedMessage.Payload, &m); err != nil {
					return msg
				}
				tc.edit(m)
				raw, err := json.Marshal(m)
				if err != nil {
					return msg
				}
				cp.WrappedMessage.Payload = raw
				return &cp
			}
			r.start()
			defer r.stop()
			results := r.executeAll(keygenCovReq(members))
			for i := 0; i < 2; i++ {
				resp := results[i]
				if resp.Status != tcommon.Fail || resp.Blame.Round != tc.round {
					t.Fatalf("party %d: %+v", i, resp)
				}
				if tc.reason != "" && !strings.Contains(resp.Blame.FailReason, tc.reason) {
					t.Fatalf("party %d reason %q want %q", i, resp.Blame.FailReason, tc.reason)
				}
				found := false
				for _, n := range resp.Blame.BlameNodes {
					found = found || n.Pubkey == members[2]
				}
				if !found {
					t.Fatalf("party %d did not blame the misbehaving sender: %+v", i, resp.Blame)
				}
			}
		})
	}
}

func TestFrostKeygenCovAdoptQuorumSuccess(t *testing.T) {
	ensureBech32Prefix()
	good := FrostKeygenResponse{Address: keygenCovAddress, GroupKey: "ab", ObservedMoneroHeight: 9, Status: tcommon.Success}
	newCoord := func(sc *mockFrostSidecarKeygen) *FrostKeygenCoordinator {
		c := newTestFrostKeygenCoordinator(testPubKeys[0], "m", newFrostKeygenInbox(), time.Second)
		c.sidecar = sc
		c.logger = zerolog.Nop()
		return c
	}
	okSidecar := func() *mockFrostSidecarKeygen {
		return &mockFrostSidecarKeygen{finalizeResp: FrostSidecarDKGFinalizeResp{Success: true, KeyBytesHex: "beef"}}
	}

	sc := okSidecar()
	adopted, ok := newCoord(sc).AdoptQuorumSuccess(keygenCovReq(nil), good)
	if !ok || adopted.KeyBytesHex != "beef" || adopted.ObservedMoneroHeight != 9 || adopted.Status != tcommon.Success {
		t.Fatalf("adoption: ok=%v %+v", ok, adopted)
	}
	if len(sc.finalizeReqs) != 1 || sc.finalizeReqs[0].Address != keygenCovAddress || sc.finalizeReqs[0].SessionID != "m" {
		t.Fatalf("finalize request: %+v", sc.finalizeReqs)
	}

	cases := map[string]struct {
		req  FrostKeygenRequest
		resp FrostKeygenResponse
		sc   *mockFrostSidecarKeygen
	}{
		"empty":    {keygenCovReq(nil), FrostKeygenResponse{GroupKey: "ab", ObservedMoneroHeight: 1}, okSidecar()},
		"expected": {FrostKeygenRequest{ExpectedAddr: "other"}, good, okSidecar()},
		"height":   {keygenCovReq(nil), FrostKeygenResponse{Address: "a", GroupKey: "ab"}, okSidecar()},
		"finalize": {keygenCovReq(nil), good, &mockFrostSidecarKeygen{finalizeErr: errTestCov("down")}},
		"proof":    {keygenCovReq(nil), good, &mockFrostSidecarKeygen{finalizeResp: FrostSidecarDKGFinalizeResp{Success: false}}},
	}
	for name, tc := range cases {
		if _, ok := newCoord(tc.sc).AdoptQuorumSuccess(tc.req, tc.resp); ok {
			t.Fatalf("%s: adopted a quorum result without proof", name)
		}
	}
	// no keyshare proof is attempted for results that fail the cheap checks
	if len(cases["empty"].sc.finalizeReqs) != 0 || len(cases["height"].sc.finalizeReqs) != 0 {
		t.Fatal("finalize called for an unusable quorum result")
	}
}

func TestFrostKeygenCovWaitForResultValidation(t *testing.T) {
	ensureBech32Prefix()
	c := newTestFrostKeygenCoordinator(testPubKeys[0], "m", newFrostKeygenInbox(), 20*time.Millisecond)
	if _, err := c.WaitForResult([]string{"a", "A"}, 1, time.Millisecond); err == nil {
		t.Fatal("duplicate participants accepted")
	}
	if _, err := c.WaitForResult([]string{testPubKeys[0]}, 3, time.Millisecond); err == nil ||
		!strings.Contains(err.Error(), "only 1 active participants") {
		t.Fatalf("quorum above participants: %v", err)
	}
	// quorum <= 0 defaults to the full set, timeout <= 0 to the coordinator's;
	// non-result traffic and noise are drained without ending the wait
	c.inbox.result <- nil
	c.inbox.result <- &p2p.Message{Payload: []byte("not json")}
	c.inbox.round1 <- &p2p.Message{}
	c.inbox.verify <- &p2p.Message{}
	if _, err := c.WaitForResult([]string{testPubKeys[0], testPubKeys[1]}, 0, 0); err == nil ||
		!strings.Contains(err.Error(), "timeout waiting for frost dkg result") {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestFrostKeygenCovBroadcastResult(t *testing.T) {
	ensureBech32Prefix()
	c := newTestFrostKeygenCoordinator(testPubKeys[0], "m", newFrostKeygenInbox(), time.Second)
	peers, err := conversion.GetPeerIDsFromPubKeys([]string{testPubKeys[1]})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.BroadcastResult(FrostKeygenResponse{Address: "a", GroupKey: "ab", ObservedMoneroHeight: 3, Status: tcommon.Success}, peers); err != nil {
		t.Fatal(err)
	}
	msg := <-c.p2pCommunication.BroadcastMsgChan
	var payload FrostDKGResultPayload
	if err := json.Unmarshal(msg.WrappedMessage.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if msg.WrappedMessage.MessageType != messages.TSSFrostDKGResultMsg || payload.SenderPubKey != testPubKeys[0] ||
		payload.Response.Address != "a" || payload.Response.ObservedMoneroHeight != 3 || len(msg.PeersID) != 1 {
		t.Fatalf("unexpected broadcast: %+v %+v", msg, payload)
	}
}
