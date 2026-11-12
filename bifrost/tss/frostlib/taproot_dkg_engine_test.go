//go:build stagenet || mocknet
// +build stagenet mocknet

package frostlib

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/ecies"
	"gitlab.com/thorchain/tss/thor-go-frost/gofrost"

	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	ltctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/ltcd-txscript"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// testDKGNode is one keygen member: its node key and its engine.
type testDKGNode struct {
	pubKey string
	key    *ecdsa.PrivateKey
	engine TaprootDKGEngine
}

func (n testDKGNode) session(t *testing.T, sessionID string) *taprootDKGSession {
	t.Helper()
	engine, ok := n.engine.(*taprootDKGEngine)
	if !ok {
		t.Fatalf("unexpected engine type %T", n.engine)
	}
	session, ok := engine.sessions[sessionID]
	if !ok {
		t.Fatalf("session %s not found", sessionID)
	}
	return session
}

// resealShare replaces the share sender broadcast for receiver with one the
// sender signs over content or a lock the test controls. change receives the
// label, the receiver's lock key and the honest plaintext, and returns the
// sealed bytes to broadcast instead.
func resealShare(t *testing.T, sessionID string, nodes []testDKGNode, shares map[string]map[string]string, sender, receiver int, change func(label shareLabel, lock *lockKey, plaintext []byte) []byte) {
	t.Helper()
	session := nodes[receiver].session(t, sessionID)
	lock := session.lockKeys[uint32(sender+1)]
	label, err := session.label(uint32(sender+1), uint32(receiver+1), lock.pub)
	if err != nil {
		t.Fatal(err)
	}
	senderKey, receiverKey := indexKey(uint32(sender+1)), indexKey(uint32(receiver+1))
	sealed, err := hex.DecodeString(shares[senderKey][receiverKey])
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := openSealedShare(label, lock, sealed)
	if err != nil {
		t.Fatal(err)
	}
	shares[senderKey][receiverKey] = hex.EncodeToString(change(label, lock, plaintext))
}

// newTestDKGNodes creates n members and returns them in the coordinator's
// canonical order (sorted pubkeys), so nodes[i] holds participant index i+1.
func newTestDKGNodes(t *testing.T, n int) ([]testDKGNode, []string) {
	t.Helper()
	nodes := make([]testDKGNode, 0, n)
	for i := 0; i < n; i++ {
		key, err := ethcrypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		bech, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, &secp256k1.PubKey{Key: ethcrypto.CompressPubkey(&key.PublicKey)})
		if err != nil {
			t.Fatal(err)
		}
		engine, err := NewTaprootDKGEngine(TaprootDKGConfig{
			Chain:       common.LTCChain,
			NodePubKey:  common.PubKey(bech),
			NodePrivKey: ethcrypto.FromECDSA(key),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(engine.Close)
		nodes = append(nodes, testDKGNode{pubKey: bech, key: key, engine: engine})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].pubKey < nodes[j].pubKey })
	participants := make([]string, n)
	for i, node := range nodes {
		participants[i] = node.pubKey
	}
	return nodes, participants
}

func testRound1Req(sessionID string, participants []string, index int, threshold uint32) tssp.FrostSidecarDKGRound1Req {
	return tssp.FrostSidecarDKGRound1Req{
		VaultPubKey:      "vault-label",
		Threshold:        threshold,
		N:                uint32(len(participants)),
		ParticipantIndex: uint32(index + 1),
		Participants:     participants,
		SessionID:        sessionID,
	}
}

// runTestRounds12 drives rounds 1 and 2 the way the keygen coordinator does and
// returns every node's round-1 message and locked shares:
// shares[sender index][receiver index].
func runTestRounds12(t *testing.T, sessionID string, nodes []testDKGNode, participants []string, threshold uint32) (map[string]string, map[string]map[string]string) {
	t.Helper()
	commitments := make(map[string]string, len(nodes))
	for i, node := range nodes {
		resp, err := node.engine.FrostDKGRound1(testRound1Req(sessionID, participants, i, threshold))
		if err != nil {
			t.Fatalf("round1 node %d: %v", i+1, err)
		}
		commitments[indexKey(uint32(i+1))] = resp.Commitments
	}
	shares := make(map[string]map[string]string, len(nodes))
	for i, node := range nodes {
		resp, err := node.engine.FrostDKGRound2(tssp.FrostSidecarDKGRound2Req{SessionID: sessionID, AllCommitments: commitments})
		if err != nil {
			t.Fatalf("round2 node %d: %v", i+1, err)
		}
		if len(resp.EncryptedShares) != len(nodes)-1 {
			t.Fatalf("node %d produced %d shares, want %d", i+1, len(resp.EncryptedShares), len(nodes)-1)
		}
		shares[indexKey(uint32(i+1))] = resp.EncryptedShares
	}
	return commitments, shares
}

// sharesFor collects the shares addressed to one receiver, keyed by sender, as
// the coordinator passes them to round 3.
func sharesFor(shares map[string]map[string]string, receiver int) map[string]string {
	receiverKey := indexKey(uint32(receiver + 1))
	out := make(map[string]string)
	for sender, bySender := range shares {
		if sender != receiverKey {
			out[sender] = bySender[receiverKey]
		}
	}
	return out
}

func TestTaprootDKGEngineFullKeygen(t *testing.T) {
	const sessionID = "engine-full-keygen"
	const threshold = 2
	nodes, participants := newTestDKGNodes(t, 3)
	_, shares := runTestRounds12(t, sessionID, nodes, participants, threshold)

	var address, groupKey string
	keyshares := make(map[string]gofrost.TaprootKeygenResult, len(nodes))
	for i, node := range nodes {
		round3, err := node.engine.FrostDKGRound3(tssp.FrostSidecarDKGRound3Req{SessionID: sessionID, AllShares: sharesFor(shares, i)})
		if err != nil {
			t.Fatalf("round3 node %d: %v", i+1, err)
		}
		if i == 0 {
			address, groupKey = round3.Address, round3.GroupKey
		} else if round3.Address != address || round3.GroupKey != groupKey {
			t.Fatalf("node %d derived %s/%s, want %s/%s", i+1, round3.Address, round3.GroupKey, address, groupKey)
		}

		// A result for another vault must not release the keyshare.
		if _, err = node.engine.FrostDKGFinalize(tssp.FrostSidecarDKGFinalizeReq{SessionID: sessionID, GroupKey: strings.Repeat("02", 33), Address: address}); err == nil {
			t.Fatalf("node %d finalized against a foreign group key", i+1)
		}
		finalize, err := node.engine.FrostDKGFinalize(tssp.FrostSidecarDKGFinalizeReq{SessionID: sessionID, GroupKey: strings.ToUpper(groupKey), Address: address})
		if err != nil || !finalize.Success {
			t.Fatalf("finalize node %d: %v", i+1, err)
		}
		keyshare, err := hex.DecodeString(finalize.KeyBytesHex)
		if err != nil {
			t.Fatal(err)
		}
		// The session is gone once the keyshare was handed out.
		if _, err = node.engine.FrostDKGFinalize(tssp.FrostSidecarDKGFinalizeReq{SessionID: sessionID, GroupKey: groupKey, Address: address}); err == nil {
			t.Fatalf("node %d finalized twice", i+1)
		}
		keyshares[node.pubKey] = gofrost.TaprootKeygenResult{Keyshare: keyshare}
	}

	// The address is the chain's P2TR address of the DKG output key.
	compressed, err := hex.DecodeString(groupKey)
	if err != nil || len(compressed) != 33 {
		t.Fatalf("group key %q is not a compressed key", groupKey)
	}
	decoded, err := ltctxscript.DecodeAddress(address, ltcNetParams(t))
	if err != nil {
		t.Fatalf("decode %s: %v", address, err)
	}
	taproot, ok := decoded.(*ltctxscript.AddressTaproot)
	if !ok {
		t.Fatalf("%s decoded as %T, want a taproot address", address, decoded)
	}
	program := taproot.ScriptAddress()
	if !bytes.Equal(program, compressed[1:]) {
		t.Fatalf("address program %x is not the group key %x", program, compressed[1:])
	}

	// Every keyshare belongs to its node and to this vault, and a threshold of
	// them signs for the vault key.
	for _, node := range nodes {
		if _, err = gofrost.ValidateKeyshareForTaproot(keyshares[node.pubKey].Keyshare, node.pubKey, program); err != nil {
			t.Fatalf("keyshare of %s: %v", node.pubKey, err)
		}
	}
	digest := sha256.Sum256([]byte("stand-in for a BIP-341 key-path sighash"))
	sig := runSign(t, participants[:threshold], keyshares, program, digest[:])
	if err = gofrost.VerifyTaproot(program, digest[:], sig); err != nil {
		t.Fatal(err)
	}
	if n := gofrost.SessionActiveCount(); n != 0 {
		t.Fatalf("%d go-frost sessions still active after the keygen", n)
	}
}

// A bad share ends round 3 with a dispute that every other member judges the
// same way from public material.
func TestTaprootDKGEngineDisputesBadShares(t *testing.T) {
	nodes, participants := newTestDKGNodes(t, 3)

	corrupt := func(sealedHex string) string {
		sealed, err := hex.DecodeString(sealedHex)
		if err != nil {
			t.Fatal(err)
		}
		sealed[len(sealed)-1] ^= 0x01
		return hex.EncodeToString(sealed)
	}
	// Each case changes the share node 2 broadcast for node 1.
	cases := map[string]func(shares map[string]map[string]string){
		"tampered share":                    func(shares map[string]map[string]string) { shares["2"]["1"] = corrupt(shares["2"]["1"]) },
		"share locked for another receiver": func(shares map[string]map[string]string) { shares["2"]["1"] = shares["2"]["3"] },
		"share copied from another sender":  func(shares map[string]map[string]string) { shares["2"]["1"] = shares["3"]["1"] },
		"not hex":                           func(shares map[string]map[string]string) { shares["2"]["1"] = "zz" },
		"missing":                           func(shares map[string]map[string]string) { delete(shares["2"], "1") },
	}
	for name, mutate := range cases {
		sessionID := "engine-bad-share-" + strings.ReplaceAll(name, " ", "-")
		round1, shares := runTestRounds12(t, sessionID, nodes, participants, 2)
		honest := shares["3"]["1"]
		mutate(shares)

		_, err := nodes[0].engine.FrostDKGRound3(tssp.FrostSidecarDKGRound3Req{SessionID: sessionID, AllShares: sharesFor(shares, 0)})
		{
			var dispute *ShareDisputeError
			if !errors.As(err, &dispute) || dispute.SenderIndex != 2 || dispute.Sender != participants[1] {
				t.Fatalf("%s: expected a dispute against participant 2, got %v", name, err)
			}
			// Each of these faults is visible in the broadcast itself, so the
			// receiver reveals no lock key for it.
			if dispute.RevealedLockKeyHex != "" {
				t.Fatalf("%s: a lock key was revealed for a fault everyone can see", name)
			}
			// Every member judges from what it received in the broadcasts.
			disputeCase := ShareDisputeCase{
				SessionID: sessionID, Threshold: 2, Participants: participants, Round1: round1,
				SenderIndex: 2, ReceiverIndex: 1,
				SealedShareHex: shares["2"]["1"], RevealedLockKeyHex: dispute.RevealedLockKeyHex,
			}
			verdict, _, judgeErr := JudgeShareDispute(disputeCase)
			if judgeErr != nil || verdict != ShareDisputeSenderAtFault {
				t.Fatalf("%s: verdict=%d err=%v, want the sender at fault", name, verdict, judgeErr)
			}
			// The revealed key opens only the lock for sender 2: the honest
			// share of sender 3 stays closed.
			disputeCase.SenderIndex = 3
			disputeCase.SealedShareHex = honest
			if verdict, share, _ := JudgeShareDispute(disputeCase); verdict == ShareDisputeShareOpened || share != nil {
				t.Fatalf("%s: the revealed key opened another sender's share", name)
			}
		}
		// The failed session is gone with its key material.
		if _, err = nodes[0].engine.FrostDKGFinalize(tssp.FrostSidecarDKGFinalizeReq{SessionID: sessionID}); err == nil {
			t.Fatalf("%s: session survived a failed round3", name)
		}
		for _, node := range nodes {
			node.engine.AbortSession(sessionID)
		}
		if n := gofrost.SessionActiveCount(); n != 0 {
			t.Fatalf("%s: %d go-frost sessions still active", name, n)
		}
	}
}

// A receiver that disputes a good share is exposed: everyone opens it. And a
// receiver cannot fake a dispute with a key it did not announce.
func TestJudgeShareDisputeOverGoodShare(t *testing.T) {
	const sessionID = "engine-good-share-dispute"
	nodes, participants := newTestDKGNodes(t, 3)
	round1, shares := runTestRounds12(t, sessionID, nodes, participants, 2)
	receiver, ok := nodes[0].engine.(*taprootDKGEngine)
	if !ok {
		t.Fatalf("unexpected engine type %T", nodes[0].engine)
	}
	lock := receiver.sessions[sessionID].lockKeys[2]
	disputeCase := ShareDisputeCase{
		SessionID: sessionID, Threshold: 2, Participants: participants, Round1: round1,
		SenderIndex: 2, ReceiverIndex: 1,
		SealedShareHex: shares["2"]["1"], RevealedLockKeyHex: hex.EncodeToString(lock.priv),
	}
	verdict, share, err := JudgeShareDispute(disputeCase)
	if err != nil || verdict != ShareDisputeShareOpened || len(share) == 0 {
		t.Fatalf("verdict=%d err=%v, want the share opened", verdict, err)
	}

	forged := disputeCase
	forged.RevealedLockKeyHex = hex.EncodeToString(receiver.sessions[sessionID].lockKeys[3].priv)
	if verdict, _, err = JudgeShareDispute(forged); err != nil || verdict != ShareDisputeReceiverAtFault {
		t.Fatalf("verdict=%d err=%v, want the receiver at fault for a key it did not announce for this sender", verdict, err)
	}

	// The case must be judged against the real transcript: a changed
	// threshold or a swapped round-1 message makes it unusable or shifts
	// nothing onto the honest sender.
	otherThreshold := disputeCase
	otherThreshold.Threshold = 3
	if verdict, _, err = JudgeShareDispute(otherThreshold); err == nil && verdict == ShareDisputeShareOpened {
		t.Fatal("a dispute judged under another threshold still opened the share")
	}
	missing := disputeCase
	missing.Round1 = map[string]string{"1": round1["1"], "2": round1["2"]}
	if _, _, err = JudgeShareDispute(missing); err == nil {
		t.Fatal("a dispute without every round1 message was judged")
	}
	for _, node := range nodes {
		node.engine.Close()
	}
	if n := gofrost.SessionActiveCount(); n != 0 {
		t.Fatalf("%d go-frost sessions still active", n)
	}
}

// Round 2 refuses a round-1 message that is not signed by its sender.
func TestTaprootDKGEngineRejectsForgedRound1(t *testing.T) {
	nodes, participants := newTestDKGNodes(t, 3)
	commitments := make(map[string]string, len(nodes))
	for i, node := range nodes {
		resp, err := node.engine.FrostDKGRound1(testRound1Req("engine-forged-round1", participants, i, 2))
		if err != nil {
			t.Fatal(err)
		}
		commitments[indexKey(uint32(i+1))] = resp.Commitments
	}
	// Node 3's message presented as node 2's.
	swapped := map[string]string{"1": commitments["1"], "2": commitments["3"], "3": commitments["3"]}
	if _, err := nodes[0].engine.FrostDKGRound2(tssp.FrostSidecarDKGRound2Req{SessionID: "engine-forged-round1", AllCommitments: swapped}); err == nil {
		t.Fatal("round2 accepted a round1 message under another member's name")
	}
	for _, node := range nodes {
		node.engine.Close()
	}
	if n := gofrost.SessionActiveCount(); n != 0 {
		t.Fatalf("%d go-frost sessions still active", n)
	}
}

// A share replayed from another keygen of the same members does not open.
func TestTaprootDKGEngineRejectsShareFromAnotherSession(t *testing.T) {
	nodes, participants := newTestDKGNodes(t, 2)
	_, first := runTestRounds12(t, "engine-replay-a", nodes, participants, 2)
	runTestRounds12(t, "engine-replay-b", nodes, participants, 2)
	if _, err := nodes[0].engine.FrostDKGRound3(tssp.FrostSidecarDKGRound3Req{SessionID: "engine-replay-b", AllShares: sharesFor(first, 0)}); err == nil {
		t.Fatal("round3 accepted a share sealed for another session")
	}
	for _, node := range nodes {
		node.engine.Close()
	}
	if n := gofrost.SessionActiveCount(); n != 0 {
		t.Fatalf("%d go-frost sessions still active", n)
	}
}

func TestTaprootDKGEngineRound1Validation(t *testing.T) {
	nodes, participants := newTestDKGNodes(t, 3)
	engine := nodes[0].engine
	valid := testRound1Req("engine-round1", participants, 0, 2)

	notLocal := valid
	notLocal.ParticipantIndex = 2
	short := valid
	short.Participants = participants[:1]
	short.N = 1
	wrongN := valid
	wrongN.N = 4
	zeroThreshold := valid
	zeroThreshold.Threshold = 0
	bigThreshold := valid
	bigThreshold.Threshold = 4
	duplicate := valid
	duplicate.Participants = []string{participants[0], participants[1], participants[1]}
	notAKey := valid
	notAKey.Participants = []string{participants[0], participants[1], "not-a-node-pubkey"}
	noSession := valid
	noSession.SessionID = " "
	for name, req := range map[string]tssp.FrostSidecarDKGRound1Req{
		"index of another node": notLocal,
		"single participant":    short,
		"n mismatch":            wrongN,
		"zero threshold":        zeroThreshold,
		"threshold above n":     bigThreshold,
		"duplicate participant": duplicate,
		"participant not a key": notAKey,
		"empty session":         noSession,
	} {
		if _, err := engine.FrostDKGRound1(req); err == nil {
			t.Fatalf("%s: round1 accepted it", name)
		}
	}

	if _, err := engine.FrostDKGRound1(valid); err != nil {
		t.Fatal(err)
	}
	// Starting the same session again is a retry: the earlier attempt is
	// released, not kept alongside.
	first := nodes[0].session(t, valid.SessionID)
	if _, err := engine.FrostDKGRound1(valid); err != nil {
		t.Fatalf("retry of the same session failed: %v", err)
	}
	if nodes[0].session(t, valid.SessionID) == first || first.lockKeys != nil || first.handleOpen {
		t.Fatal("the earlier attempt was not released by the retry")
	}
	if n := gofrost.SessionActiveCount(); n != 1 {
		t.Fatalf("%d go-frost sessions active after a retry, want 1", n)
	}
	// Steps out of order are refused.
	if _, err := engine.FrostDKGRound3(tssp.FrostSidecarDKGRound3Req{SessionID: valid.SessionID}); err == nil {
		t.Fatal("round3 ran before round2")
	}
	if _, err := engine.FrostDKGFinalize(tssp.FrostSidecarDKGFinalizeReq{SessionID: valid.SessionID}); err == nil {
		t.Fatal("finalize ran before round3")
	}
	engine.AbortSession(valid.SessionID)
	if n := gofrost.SessionActiveCount(); n != 0 {
		t.Fatalf("%d go-frost sessions still active", n)
	}
}

func TestTaprootDKGEngineExpiresStaleSessions(t *testing.T) {
	nodes, participants := newTestDKGNodes(t, 2)
	engine, ok := nodes[0].engine.(*taprootDKGEngine)
	if !ok {
		t.Fatalf("unexpected engine type %T", nodes[0].engine)
	}
	now := time.Now()
	engine.now = func() time.Time { return now }
	if _, err := engine.FrostDKGRound1(testRound1Req("engine-stale", participants, 0, 2)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(DefaultTaprootDKGSessionTTL + time.Second)
	// Any later call releases it, not only the start of a new keygen.
	if _, err := engine.FrostDKGFinalize(tssp.FrostSidecarDKGFinalizeReq{SessionID: "unrelated"}); err == nil {
		t.Fatal("finalize of an unknown session succeeded")
	}
	if n := gofrost.SessionActiveCount(); n != 0 {
		t.Fatalf("%d go-frost sessions active after expiry, want none", n)
	}
	if _, err := engine.FrostDKGRound2(tssp.FrostSidecarDKGRound2Req{SessionID: "engine-stale"}); err == nil {
		t.Fatal("the stale session survived")
	}
	engine.Close()
	if n := gofrost.SessionActiveCount(); n != 0 {
		t.Fatalf("%d go-frost sessions still active", n)
	}
}

func TestNewTaprootDKGEngineValidation(t *testing.T) {
	key, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	other, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	bech, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, &secp256k1.PubKey{Key: ethcrypto.CompressPubkey(&key.PublicKey)})
	if err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]TaprootDKGConfig{
		"chain without taproot vaults": {Chain: common.BTCChain, NodePubKey: common.PubKey(bech), NodePrivKey: ethcrypto.FromECDSA(key)},
		"key of another node":          {Chain: common.LTCChain, NodePubKey: common.PubKey(bech), NodePrivKey: ethcrypto.FromECDSA(other)},
		"malformed private key":        {Chain: common.LTCChain, NodePubKey: common.PubKey(bech), NodePrivKey: []byte{1, 2, 3}},
		"malformed pubkey":             {Chain: common.LTCChain, NodePubKey: common.PubKey("nope"), NodePrivKey: ethcrypto.FromECDSA(key)},
	} {
		if _, err = NewTaprootDKGEngine(cfg); err == nil {
			t.Fatalf("%s: engine was created", name)
		}
	}
}

// A share with a good signature whose content is bad: the receiver must dispute
// the sender that produced it, whatever its position, and reveal only the lock
// key announced for that sender.
func TestTaprootDKGEngineDisputesTheRealCulprit(t *testing.T) {
	for _, tc := range []struct{ receiver, culprit, honest int }{
		{receiver: 2, culprit: 0, honest: 1}, // culprit is fed first, honest sender last
		{receiver: 2, culprit: 1, honest: 0}, // culprit is fed last
		{receiver: 0, culprit: 1, honest: 2},
	} {
		sessionID := "engine-culprit-" + indexKey(uint32(tc.receiver)) + "-" + indexKey(uint32(tc.culprit))
		nodes, participants := newTestDKGNodes(t, 3)
		round1, shares := runTestRounds12(t, sessionID, nodes, participants, 2)
		resealShare(t, sessionID, nodes, shares, tc.culprit, tc.receiver, func(label shareLabel, _ *lockKey, plaintext []byte) []byte {
			bad := append([]byte(nil), plaintext...)
			bad[len(bad)-1] ^= 0x01
			sealed, err := sealShare(nodes[tc.culprit].key, label, bad)
			if err != nil {
				t.Fatal(err)
			}
			return sealed
		})
		honestLock := append([]byte(nil), nodes[tc.receiver].session(t, sessionID).lockKeys[uint32(tc.honest+1)].priv...)

		_, err := nodes[tc.receiver].engine.FrostDKGRound3(tssp.FrostSidecarDKGRound3Req{SessionID: sessionID, AllShares: sharesFor(shares, tc.receiver)})
		var dispute *ShareDisputeError
		if !errors.As(err, &dispute) {
			t.Fatalf("receiver %d: expected a dispute, got %v", tc.receiver+1, err)
		}
		if dispute.SenderIndex != uint32(tc.culprit+1) || dispute.Sender != participants[tc.culprit] {
			t.Fatalf("receiver %d: disputed participant %d, want %d", tc.receiver+1, dispute.SenderIndex, tc.culprit+1)
		}
		if dispute.RevealedLockKeyHex == "" || dispute.RevealedLockKeyHex == hex.EncodeToString(honestLock) {
			t.Fatalf("receiver %d: revealed the wrong lock key", tc.receiver+1)
		}
		if strings.Contains(dispute.Error(), dispute.RevealedLockKeyHex) {
			t.Fatal("the error text contains the revealed key")
		}
		disputeCase := ShareDisputeCase{
			SessionID: sessionID, Threshold: 2, Participants: participants, Round1: round1,
			SenderIndex: uint32(tc.culprit + 1), ReceiverIndex: uint32(tc.receiver + 1),
			SealedShareHex:     shares[indexKey(uint32(tc.culprit+1))][indexKey(uint32(tc.receiver+1))],
			RevealedLockKeyHex: dispute.RevealedLockKeyHex,
		}
		// Everyone can open the culprit's share and see its content.
		if verdict, share, judgeErr := JudgeShareDispute(disputeCase); judgeErr != nil || verdict != ShareDisputeShareOpened || len(share) == 0 {
			t.Fatalf("receiver %d: verdict=%d err=%v, want the culprit's share opened", tc.receiver+1, verdict, judgeErr)
		}
		// The honest sender's share stays closed.
		disputeCase.SenderIndex = uint32(tc.honest + 1)
		disputeCase.SealedShareHex = shares[indexKey(uint32(tc.honest+1))][indexKey(uint32(tc.receiver+1))]
		if verdict, share, _ := JudgeShareDispute(disputeCase); verdict == ShareDisputeShareOpened || share != nil {
			t.Fatalf("receiver %d: the dispute opened the honest sender's share", tc.receiver+1)
		}
		for _, node := range nodes {
			node.engine.Close()
		}
		if n := gofrost.SessionActiveCount(); n != 0 {
			t.Fatalf("%d go-frost sessions still active", n)
		}
	}
}

// A validly signed share whose lock does not open can only be shown to the
// others with the lock key, so that is the one case a signature-level fault
// reveals it.
func TestTaprootDKGEngineRevealsKeyForLockThatDoesNotOpen(t *testing.T) {
	const sessionID = "engine-lock-does-not-open"
	nodes, participants := newTestDKGNodes(t, 3)
	round1, shares := runTestRounds12(t, sessionID, nodes, participants, 2)
	resealShare(t, sessionID, nodes, shares, 1, 0, func(label shareLabel, lock *lockKey, plaintext []byte) []byte {
		other := label
		other.receiverIndex = 3
		locked, err := ecies.Encrypt(rand.Reader, ecies.ImportECDSAPublic(lock.pub), plaintext, nil, other.bytes())
		if err != nil {
			t.Fatal(err)
		}
		return signLocked(t, nodes[1].key, label, locked)
	})
	_, err := nodes[0].engine.FrostDKGRound3(tssp.FrostSidecarDKGRound3Req{SessionID: sessionID, AllShares: sharesFor(shares, 0)})
	var dispute *ShareDisputeError
	if !errors.As(err, &dispute) || dispute.SenderIndex != 2 || dispute.RevealedLockKeyHex == "" {
		t.Fatalf("expected a dispute against participant 2 with a revealed key, got %v", err)
	}
	verdict, _, judgeErr := JudgeShareDispute(ShareDisputeCase{
		SessionID: sessionID, Threshold: 2, Participants: participants, Round1: round1,
		SenderIndex: 2, ReceiverIndex: 1,
		SealedShareHex: shares["2"]["1"], RevealedLockKeyHex: dispute.RevealedLockKeyHex,
	})
	if judgeErr != nil || verdict != ShareDisputeSenderAtFault {
		t.Fatalf("verdict=%d err=%v, want the sender at fault", verdict, judgeErr)
	}
	for _, node := range nodes {
		node.engine.Close()
	}
}

// Lock keys are wiped as soon as they are no longer needed, and an idle engine
// still releases an unfinished session.
func TestTaprootDKGEngineWipesLockKeys(t *testing.T) {
	const sessionID = "engine-wipe"
	nodes, participants := newTestDKGNodes(t, 2)
	_, shares := runTestRounds12(t, sessionID, nodes, participants, 2)
	session := nodes[0].session(t, sessionID)
	lock := session.lockKeys[2]
	if _, err := nodes[0].engine.FrostDKGRound3(tssp.FrostSidecarDKGRound3Req{SessionID: sessionID, AllShares: sharesFor(shares, 0)}); err != nil {
		t.Fatal(err)
	}
	if session.lockKeys != nil || !bytes.Equal(lock.priv, make([]byte, len(lock.priv))) {
		t.Fatal("lock keys survived a completed round 3")
	}
	aborted := nodes[1].session(t, sessionID)
	abortedLock := aborted.lockKeys[1]
	nodes[1].engine.AbortSession(sessionID)
	if aborted.lockKeys != nil || !bytes.Equal(abortedLock.priv, make([]byte, len(abortedLock.priv))) {
		t.Fatal("lock keys survived an abort")
	}
	for _, node := range nodes {
		node.engine.Close()
	}

	engine, err := NewTaprootDKGEngine(TaprootDKGConfig{
		Chain: common.LTCChain, NodePubKey: common.PubKey(nodes[0].pubKey), NodePrivKey: ethcrypto.FromECDSA(nodes[0].key),
		SessionTTL: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.FrostDKGRound1(testRound1Req("engine-idle", participants, 0, 2)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for gofrost.SessionActiveCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("an idle engine never released its unfinished session")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestJudgeShareDisputeValidatesTheCase(t *testing.T) {
	const sessionID = "engine-judge-validation"
	nodes, participants := newTestDKGNodes(t, 3)
	round1, shares := runTestRounds12(t, sessionID, nodes, participants, 2)
	valid := ShareDisputeCase{
		SessionID: sessionID, Threshold: 2, Participants: participants, Round1: round1,
		SenderIndex: 2, ReceiverIndex: 1, SealedShareHex: shares["2"]["1"],
	}
	untrimmed, duplicate, zeroThreshold, bigThreshold, sameIndex, noSession := valid, valid, valid, valid, valid, valid
	untrimmed.Participants = []string{participants[0], " " + participants[1], participants[2]}
	duplicate.Participants = []string{participants[0], participants[1], participants[1]}
	zeroThreshold.Threshold = 0
	bigThreshold.Threshold = 4
	sameIndex.SenderIndex = 1
	noSession.SessionID = " "
	for name, c := range map[string]ShareDisputeCase{
		"untrimmed participant": untrimmed, "duplicate participant": duplicate, "zero threshold": zeroThreshold,
		"threshold above n": bigThreshold, "sender is the receiver": sameIndex, "empty session": noSession,
	} {
		if _, _, err := JudgeShareDispute(c); err == nil {
			t.Fatalf("%s: the case was judged", name)
		}
	}
	// A valid share with no revealed key: the receiver has shown nothing.
	if verdict, _, err := JudgeShareDispute(valid); err != nil || verdict != ShareDisputeReceiverAtFault {
		t.Fatalf("verdict=%d err=%v, want the receiver at fault for a dispute without a key", verdict, err)
	}
	for _, node := range nodes {
		node.engine.Close()
	}
}
