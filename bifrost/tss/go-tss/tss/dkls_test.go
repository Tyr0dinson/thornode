package tss

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	tmsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/libp2p/go-libp2p-core/peer"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keysign"
	"gitlab.com/thorchain/thornode/v3/common"
	dklssession "gitlab.com/thorchain/thornode/v3/dkls/go-wrappers/go-dkls/sessions"
)

func TestDKLSBlameFromIdentifiableAbort(t *testing.T) {
	participants := []string{"p1", "p2", "p3"}
	got := dklsBlameFromError(errors.New("Protocol abort and ban party 2"), participants, dklsKeysignAbortRound)
	if got.FailReason != "Protocol abort and ban party 2: dkls identifiable abort" {
		t.Fatalf("unexpected fail reason: %q", got.FailReason)
	}
	if got.Round != dklsKeysignAbortRound {
		t.Fatalf("unexpected round: %q", got.Round)
	}
	if len(got.BlameNodes) != 1 || got.BlameNodes[0].Pubkey != "p2" {
		t.Fatalf("unexpected blame nodes: %+v", got.BlameNodes)
	}
}

func TestDKLSBlameFromNonIdentifiableError(t *testing.T) {
	got := dklsBlameFromError(errors.New("network timeout"), []string{"p1"}, dklsKeygenAbortRound)
	if got.FailReason != "network timeout" {
		t.Fatalf("unexpected fail reason: %q", got.FailReason)
	}
	if got.Round != dklsKeygenAbortRound {
		t.Fatalf("unexpected round: %q", got.Round)
	}
	if len(got.BlameNodes) != 0 {
		t.Fatalf("expected no blame nodes: %+v", got.BlameNodes)
	}
}

func TestDKLSKeysignBlameDefaultsToLeader(t *testing.T) {
	got := dklsBlameFromErrorWithDefault(errors.New("timeout waiting for DKLS setup"), []string{"leader", "p2", "p3"}, dklsKeysignAbortRound, "leader", "dkls default leader abort")
	if got.Round != dklsKeysignAbortRound {
		t.Fatalf("unexpected round: %q", got.Round)
	}
	if got.FailReason != "timeout waiting for DKLS setup: dkls default leader abort" {
		t.Fatalf("unexpected fail reason: %q", got.FailReason)
	}
	if len(got.BlameNodes) != 1 || got.BlameNodes[0].Pubkey != "leader" {
		t.Fatalf("unexpected blame nodes: %+v", got.BlameNodes)
	}
}

func TestDKLSKeysignDefaultDoesNotOverrideAttributableBlame(t *testing.T) {
	err := dklsBlameError("invalid DKLS keysign result payload from p2", "p2")
	got := dklsBlameFromErrorWithDefault(err, []string{"leader", "p2", "p3"}, dklsKeysignAbortRound, "leader", "dkls default leader abort")
	if got.FailReason != err.Error()+": dkls attributable abort" {
		t.Fatalf("unexpected fail reason: %q", got.FailReason)
	}
	if len(got.BlameNodes) != 1 || got.BlameNodes[0].Pubkey != "p2" {
		t.Fatalf("unexpected blame nodes: %+v", got.BlameNodes)
	}
}

func TestDKLSBlameFromMissingParticipants(t *testing.T) {
	err := errors.New("DKLS session timeout: produced=1 sent=5 received=4 input=4 self_input=0 finished=false queued=0 froms=p1=1,p2=1 missing=p3")
	got := dklsBlameFromError(err, []string{"p1", "p2", "p3"}, dklsKeygenAbortRound)
	if got.Round != dklsKeygenAbortRound {
		t.Fatalf("unexpected round: %q", got.Round)
	}
	if got.FailReason != err.Error()+": dkls missing participant abort" {
		t.Fatalf("unexpected fail reason: %q", got.FailReason)
	}
	if len(got.BlameNodes) != 1 || got.BlameNodes[0].Pubkey != "p3" {
		t.Fatalf("unexpected blame nodes: %+v", got.BlameNodes)
	}
}

func TestDKLSBlameFromTimeoutSenderSkew(t *testing.T) {
	err := errors.New("DKLS session timeout: produced=1 sent=5 received=7 input=7 self_input=0 finished=false queued=0 froms=p1=1,p2=3,p3=1 missing=")
	got := dklsBlameFromError(err, []string{"p1", "p2", "p3"}, dklsKeygenAbortRound)
	if got.Round != dklsKeygenAbortRound {
		t.Fatalf("unexpected round: %q", got.Round)
	}
	if got.FailReason != err.Error()+": dkls timeout sender-skew abort" {
		t.Fatalf("unexpected fail reason: %q", got.FailReason)
	}
	if len(got.BlameNodes) != 1 || got.BlameNodes[0].Pubkey != "p2" {
		t.Fatalf("unexpected blame nodes: %+v", got.BlameNodes)
	}
}

func TestDKLSBlameFromTimeoutSenderSkewRequiresUniqueMax(t *testing.T) {
	err := errors.New("DKLS session timeout: produced=1 sent=5 received=7 input=7 self_input=0 finished=false queued=0 froms=p1=2,p2=2,p3=1 missing=")
	got := dklsBlameFromError(err, []string{"p1", "p2", "p3"}, dklsKeygenAbortRound)
	if len(got.BlameNodes) != 0 {
		t.Fatalf("expected no blame nodes: %+v", got.BlameNodes)
	}
}

func TestDKLSBlameFromAttributableError(t *testing.T) {
	err := dklsBlameError("invalid DKLS protocol message", "p2", "p2", "p1")
	got := dklsBlameFromError(err, []string{"p1", "p2", "p3"}, dklsKeysignAbortRound)
	if got.Round != dklsKeysignAbortRound {
		t.Fatalf("unexpected round: %q", got.Round)
	}
	if got.FailReason != err.Error()+": dkls attributable abort" {
		t.Fatalf("unexpected fail reason: %q", got.FailReason)
	}
	if len(got.BlameNodes) != 2 || got.BlameNodes[0].Pubkey != "p1" || got.BlameNodes[1].Pubkey != "p2" {
		t.Fatalf("unexpected blame nodes: %+v", got.BlameNodes)
	}
}

func TestDKLSAttributableFailureClasses(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		blamed string
	}{
		{
			name:   "corrupt",
			err:    dklsBlameError("DKLS corrupt protocol message from p2 caused native abort", "p2"),
			blamed: "p2",
		},
		{
			name:   "malformed",
			err:    dklsBlameError("invalid DKLS session wire from p2", "p2"),
			blamed: "p2",
		},
		{
			name:   "auth",
			err:    dklsBlameError("invalid DKLS session signature from p2", "p2"),
			blamed: "p2",
		},
		{
			name:   "result",
			err:    dklsBlameError("invalid DKLS keysign result signatures from p2", "p2"),
			blamed: "p2",
		},
		{
			name:   "native identifiable party",
			err:    errors.New("LIB_ABORT_PROTOCOL_AND_BAN_PARTY_2"),
			blamed: "p2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dklsBlameFromErrorWithDefault(tc.err, []string{"p1", "p2", "p3"}, dklsKeysignAbortRound, "leader", "dkls default leader abort")
			if got.Round != dklsKeysignAbortRound {
				t.Fatalf("unexpected round: %q", got.Round)
			}
			if len(got.BlameNodes) != 1 || got.BlameNodes[0].Pubkey != tc.blamed {
				t.Fatalf("unexpected blame nodes: %+v", got.BlameNodes)
			}
			if strings.Contains(got.FailReason, "default leader") {
				t.Fatalf("attributable blame was overwritten: %q", got.FailReason)
			}
		})
	}
}

func TestDKLSNodeSyncBlame(t *testing.T) {
	keys := make([]string, 3)
	for i := range keys {
		pubKey, err := common.NewPubKeyFromCrypto(tmsecp256k1.GenPrivKey().PubKey())
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = pubKey.String()
	}
	peerID, err := conversion.GetPeerIDFromPubKey(keys[0])
	if err != nil {
		t.Fatal(err)
	}
	got := dklsNodeSyncBlame(keys, []peer.ID{peerID}, dklsKeygenAbortRound, errors.New("join failed"))
	if got.Round != dklsKeygenAbortRound {
		t.Fatalf("unexpected round: %q", got.Round)
	}
	if len(got.BlameNodes) != 2 || got.BlameNodes[0].Pubkey != keys[1] || got.BlameNodes[1].Pubkey != keys[2] {
		t.Fatalf("unexpected blame nodes: %+v", got.BlameNodes)
	}
}

func TestDKLSMissingParticipantsExcludesLocalParty(t *testing.T) {
	got := dklsMissingParticipants([]string{"p1", "p2", "p3"}, "p2", map[string]int{"p1": 1})
	if got != "p3" {
		t.Fatalf("missing participants = %q, want p3", got)
	}
}

func TestValidateDKLSSignatures(t *testing.T) {
	priv, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		t.Fatal(err)
	}
	pubKey, err := dklsPubKeyFromCompressed(priv.PubKey().SerializeCompressed())
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("dkls message")
	digest := sha256.Sum256(msg)
	sig, err := priv.Sign(digest[:])
	if err != nil {
		t.Fatal(err)
	}
	encodedMsg := base64.StdEncoding.EncodeToString(msg)
	signatures := []keysign.Signature{keysign.NewSignature(
		encodedMsg,
		base64.StdEncoding.EncodeToString(sig.R.Bytes()),
		base64.StdEncoding.EncodeToString(sig.S.Bytes()),
		base64.StdEncoding.EncodeToString([]byte{0}),
	)}
	pk, err := common.NewPubKey(pubKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDKLSSignatures(pk, signatures, []string{encodedMsg}); err != nil {
		t.Fatalf("expected valid signature: %v", err)
	}
	duplicateSignatures := []keysign.Signature{signatures[0], signatures[0]}
	if err := validateDKLSSignatures(pk, duplicateSignatures, []string{encodedMsg, encodedMsg}); err != nil {
		t.Fatalf("expected duplicate messages to validate by count: %v", err)
	}
	signatures[0].S = base64.StdEncoding.EncodeToString([]byte{1})
	if err := validateDKLSSignatures(pk, signatures, []string{encodedMsg}); err == nil {
		t.Fatal("expected invalid signature error")
	}
}

func TestDKLSWireMessageAuth(t *testing.T) {
	priv := tmsecp256k1.GenPrivKey()
	from, err := common.NewPubKeyFromCrypto(priv.PubKey())
	if err != nil {
		t.Fatal(err)
	}
	msgID := "dkls-message-id"
	wire := dklsWireMessage{
		Kind:    "session",
		From:    from.String(),
		To:      "receiver",
		Message: []byte("payload"),
	}
	srv := &TssServer{privateKey: priv}
	if err := srv.signDKLSWireMessage(msgID, &wire); err != nil {
		t.Fatal(err)
	}
	if err := verifyDKLSWireMessage(msgID, wire); err != nil {
		t.Fatalf("expected valid DKLS wire signature: %v", err)
	}
	wire.Message = []byte("tampered")
	if err := verifyDKLSWireMessage(msgID, wire); err == nil {
		t.Fatal("expected tampered DKLS wire message to fail verification")
	}
	wire.Message = []byte("payload")
	if err := verifyDKLSWireMessage("other-message-id", wire); err == nil {
		t.Fatal("expected DKLS wire message signed for a different msgID to fail verification")
	}
}

func TestDKLSAbortModeMalformedWireFault(t *testing.T) {
	t.Setenv("BIFROST_DKLS_ABORT_MODE", "malformed")
	t.Setenv("BIFROST_DKLS_ABORT_PHASE", dklsAbortPhaseKeysign)

	srv := &TssServer{localNodePubKey: "sender"}
	got := srv.maybeFaultDKLSWireMessage(dklsAbortPhaseKeysign, "msg-id", "receiver", 1, 0, &dklsWireMessage{}, []byte(`{"ok":true}`))
	if string(got) != "{" {
		t.Fatalf("malformed wire fault = %q, want {", string(got))
	}
}

func TestDKLSAbortModeAuthWireFault(t *testing.T) {
	t.Setenv("BIFROST_DKLS_ABORT_MODE", "auth")
	t.Setenv("BIFROST_DKLS_ABORT_PHASE", dklsAbortPhaseKeysign)

	priv := tmsecp256k1.GenPrivKey()
	from, err := common.NewPubKeyFromCrypto(priv.PubKey())
	if err != nil {
		t.Fatal(err)
	}
	msgID := "msg-id"
	wire := dklsWireMessage{Kind: "session", From: from.String(), To: "receiver", Message: []byte("payload")}
	srv := &TssServer{privateKey: priv, localNodePubKey: from.String()}
	if err := srv.signDKLSWireMessage(msgID, &wire); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	faulted := srv.maybeFaultDKLSWireMessage(dklsAbortPhaseKeysign, msgID, "receiver", 1, 0, &wire, encoded)
	var got dklsWireMessage
	if err := json.Unmarshal(faulted, &got); err != nil {
		t.Fatal(err)
	}
	if err := verifyDKLSWireMessage(msgID, got); err == nil {
		t.Fatal("expected auth wire fault to fail verification")
	}
}

func TestDKLSAbortModeCorruptsProtocolPayload(t *testing.T) {
	t.Setenv("BIFROST_DKLS_ABORT_MODE", "corrupt")
	t.Setenv("BIFROST_DKLS_ABORT_PHASE", dklsAbortPhaseKeygen)

	msg := make([]byte, 64)
	copy(msg[:36], []byte("preserve-dkls-message-header-bytes"))
	srv := &TssServer{localNodePubKey: "sender"}
	got := srv.maybeCorruptDKLSTestMessage(dklsAbortPhaseKeygen, "msg-id", "receiver", 1, 0, msg)
	if !bytes.Equal(got[:36], msg[:36]) {
		t.Fatal("corrupt mode changed DKLS message header")
	}
	if got[36] == msg[36] {
		t.Fatal("corrupt mode did not change DKLS protocol payload")
	}
	if msg[36] != 0 {
		t.Fatal("corrupt mode mutated caller message")
	}
}

func TestDKLSAbortModeResultFault(t *testing.T) {
	t.Setenv("BIFROST_DKLS_ABORT_MODE", "result")
	t.Setenv("BIFROST_DKLS_ABORT_PHASE", dklsAbortPhaseKeysign)

	srv := &TssServer{localNodePubKey: "sender"}
	sigs := []keysign.Signature{keysign.NewSignature("msg", "r", "s", "recovery")}
	got, faulted := srv.maybeFaultDKLSSignResult(dklsAbortPhaseKeysign, "msg-id", sigs)
	if !faulted {
		t.Fatal("expected result fault to report faulted result")
	}
	if got[0].S == sigs[0].S {
		t.Fatal("expected result fault to corrupt signature")
	}
	if sigs[0].S != "s" {
		t.Fatal("result fault mutated caller signature slice")
	}
	blame := dklsBlameFromError(dklsBlameError("DKLS test bad keysign result broadcaster", "sender"), []string{"sender", "other"}, dklsKeysignAbortRound)
	if len(blame.BlameNodes) != 1 || blame.BlameNodes[0].Pubkey != "sender" {
		t.Fatalf("expected result broadcaster blame, got %+v", blame.BlameNodes)
	}
}

func TestDKLSDefaultAbortTargetsKeysignLeader(t *testing.T) {
	t.Setenv("BIFROST_DKLS_ABORT_PHASE", dklsAbortPhaseKeysign)

	leader := &TssServer{localNodePubKey: "leader"}
	if !leader.shouldTriggerDKLSTestFaultForTarget(dklsAbortPhaseKeysign, 1, 0, "leader") {
		t.Fatal("expected default keysign abort to trigger on leader")
	}
	nonLeader := &TssServer{localNodePubKey: "signer"}
	if nonLeader.shouldTriggerDKLSTestFaultForTarget(dklsAbortPhaseKeysign, 1, 0, "leader") {
		t.Fatal("expected default keysign abort not to trigger on non-leader")
	}
}

func TestDKLSExplicitAbortTargetOverridesDefaultLeader(t *testing.T) {
	t.Setenv("BIFROST_DKLS_ABORT_PHASE", dklsAbortPhaseKeysign)
	t.Setenv("BIFROST_DKLS_ABORT_LOCAL_PUBKEY", "signer")

	leader := &TssServer{localNodePubKey: "leader"}
	if leader.shouldTriggerDKLSTestFaultForTarget(dklsAbortPhaseKeysign, 1, 0, "leader") {
		t.Fatal("expected explicit abort target to override default leader")
	}
	signer := &TssServer{localNodePubKey: "signer"}
	if !signer.shouldTriggerDKLSTestFaultForTarget(dklsAbortPhaseKeysign, 1, 0, "leader") {
		t.Fatal("expected explicit abort target to trigger")
	}
}

func TestDKLSKeygenDefaultAbortUsesExplicitNewNodeTarget(t *testing.T) {
	t.Setenv("BIFROST_DKLS_ABORT_PHASE", dklsAbortPhaseKeygen)
	t.Setenv("BIFROST_DKLS_ABORT_LOCAL_PUBKEY", "new-node")

	newNode := &TssServer{localNodePubKey: "new-node"}
	if !newNode.shouldTriggerDKLSTestFaultForTarget(dklsAbortPhaseKeygen, 1, 0, "") {
		t.Fatal("expected keygen abort to trigger on explicit new node")
	}
	oldNode := &TssServer{localNodePubKey: "old-node"}
	if oldNode.shouldTriggerDKLSTestFaultForTarget(dklsAbortPhaseKeygen, 1, 0, "") {
		t.Fatal("expected keygen abort not to trigger on non-target node")
	}
}

func TestKeygenLocalStateEngineDefault(t *testing.T) {
	state := storage.KeygenLocalState{}
	if state.Engine() != storage.SigningEngineGG20 {
		t.Fatalf("empty engine = %q, want %q", state.Engine(), storage.SigningEngineGG20)
	}
	state.SigningEngine = storage.SigningEngineDKLS
	if state.Engine() != storage.SigningEngineDKLS {
		t.Fatalf("engine = %q, want %q", state.Engine(), storage.SigningEngineDKLS)
	}
}

func TestDKLSWrapperKeygenInMemory(t *testing.T) {
	testDKLSWrapperKeygenInMemory(t, false)
}

func TestDKLSWrapperKeygenInMemoryNetworkOrder(t *testing.T) {
	testDKLSWrapperKeygenInMemory(t, true)
}

func TestDKLSWrapperKeygenAbortMessage(t *testing.T) {
	ids, idsBytes := dklsIDs([]string{"p1", "p2", "p3"})
	threshold, err := dklsThreshold(len(ids))
	if err != nil {
		t.Fatal(err)
	}
	keyID := sha256.Sum256([]byte("test-dkls-keygen-abort"))
	setup, err := dklssession.DklsKeygenSetupMsgNew(threshold, keyID[:], idsBytes)
	if err != nil {
		t.Fatal(err)
	}
	sessions := make(map[string]dklssession.Handle, len(ids))
	defer func() {
		for _, handle := range sessions {
			_ = dklssession.DklsKeygenSessionFree(handle)
		}
	}()
	for _, id := range ids {
		handle, err := dklssession.DklsKeygenSessionFromSetup(setup, []byte(id))
		if err != nil {
			t.Fatal(err)
		}
		sessions[id] = handle
	}
	abortMsg, err := dklssession.DklsKeygenSessionAbortMessage(sessions["p2"])
	if err != nil {
		t.Fatal(err)
	}
	done, err := dklssession.DklsKeygenSessionInputMessage(sessions["p1"], abortMsg)
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("expected abort message to finish receiver session")
	}
	if _, err := dklssession.DklsKeygenSessionFinish(sessions["p1"]); err == nil || !strings.Contains(err.Error(), "Protocol abort by party 2") {
		t.Fatalf("expected keygen abort by party 2, got %v", err)
	}
}

func TestDKLSWrapperKeygenCorruptMessageCausesAbort(t *testing.T) {
	t.Setenv("BIFROST_DKLS_ABORT_MODE", "corrupt")
	t.Setenv("BIFROST_DKLS_ABORT_PHASE", dklsAbortPhaseKeygen)

	ids, idsBytes := dklsIDs([]string{"p1", "p2", "p3"})
	threshold, err := dklsThreshold(len(ids))
	if err != nil {
		t.Fatal(err)
	}
	keyID := sha256.Sum256([]byte("test-dkls-keygen-corrupt"))
	setup, err := dklssession.DklsKeygenSetupMsgNew(threshold, keyID[:], idsBytes)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := dklssession.DklsKeygenSessionFromSetup(setup, []byte("p1"))
	if err != nil {
		t.Fatal(err)
	}
	defer dklssession.DklsKeygenSessionFree(p1)
	p2, err := dklssession.DklsKeygenSessionFromSetup(setup, []byte("p2"))
	if err != nil {
		t.Fatal(err)
	}
	defer dklssession.DklsKeygenSessionFree(p2)

	out, err := dklssession.DklsKeygenSessionOutputMessage(p2)
	if err != nil {
		t.Fatal(err)
	}
	foundP1 := false
	for idx := 0; idx < len(ids); idx++ {
		receiver, err := dklssession.DklsKeygenSessionMessageReceiver(p2, out, idx)
		if err != nil {
			t.Fatal(err)
		}
		if receiver == "" {
			break
		}
		if receiver == "p1" {
			foundP1 = true
		}
	}
	if !foundP1 {
		t.Fatal("expected p2 output to target p1")
	}
	corrupt := (&TssServer{localNodePubKey: "p2"}).maybeCorruptDKLSTestMessage(dklsAbortPhaseKeygen, "msg-id", "p1", 1, 0, out)
	if bytes.Equal(corrupt, out) {
		t.Fatal("expected corrupt hook to change DKLS message")
	}
	done, err := dklssession.DklsKeygenSessionInputMessage(p1, corrupt)
	if err != nil {
		t.Fatal(err)
	}
	abortOut, abortErr := dklssession.DklsKeygenSessionOutputMessage(p1)
	if abortErr != nil {
		t.Fatal(abortErr)
	}
	if done {
		t.Fatal("corrupt message should cause an abort output before session finish")
	}
	if len(abortOut) == 0 {
		t.Fatal("expected corrupted DKLS message to cause native abort output")
	}
}

func TestDKLSWrapperConcurrentKeySignInMemory(t *testing.T) {
	ids := []string{"cthorpub1addwnpepqf40hmsl6443fhyy5z00dwg0wdttp2x5eascnx43dnqy4xtj09gr707pdt8", "cthorpub1addwnpepqf4tat5t69l8apydlam9lngr0hjj7c7pfcl44s0cpldj2rp264v72ks8trc", "cthorpub1addwnpepqfshsq2y6ejy2ysxmq4gj8n8mzuzyulk9wh4n946jv5w2vpwdn2yuwlsyz7", "cthorpub1addwnpepqtk2tngywwsly2wu90k30xjccafv5tsss3x8y0udsfv6yy8u0h7mjpw6l98", "cthorpub1addwnpepqwlkdxcyz8fnxk3xp0eqt89g6cxau2c4zp89yr2x2dy56s2zgu2jxml3fmh"}
	shares, pubKey := testDKLSWrapperKeygenShares(t, ids)
	_, idsBytes := dklsIDs(ids)
	firstShare, err := dklssession.DklsKeyshareFromBytes(shares[ids[0]])
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := dklssession.DklsKeyshareKeyID(firstShare)
	_ = dklssession.DklsKeyshareFree(firstShare)
	if err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 3)
	for _, msg := range [][]byte{[]byte("first dkls message"), []byte("second dkls message"), []byte("third dkls message")} {
		msg := msg
		go func() {
			digest := sha256.Sum256(msg)
			sig, err := testDKLSWrapperSign(t, ids, shares, keyID, idsBytes, digest[:])
			if err != nil {
				errCh <- err
				return
			}
			btcecSig := &btcec.Signature{
				R: sig.R,
				S: sig.S,
			}
			if !btcecSig.Verify(digest[:], pubKey) {
				errCh <- errors.New("DKLS signature verification failed")
				return
			}
			errCh <- nil
		}()
	}
	for i := 0; i < 3; i++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}

func TestDKLSWrapperSignAbortMessage(t *testing.T) {
	ids, idsBytes := dklsIDs([]string{"p1", "p2", "p3"})
	shares, _ := testDKLSWrapperKeygenShares(t, ids)
	firstShare, err := dklssession.DklsKeyshareFromBytes(shares[ids[0]])
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := dklssession.DklsKeyshareKeyID(firstShare)
	_ = dklssession.DklsKeyshareFree(firstShare)
	if err != nil {
		t.Fatal(err)
	}
	msgHash := sha256.Sum256([]byte("test-dkls-sign-abort-message"))
	setup, err := dklssession.DklsSignSetupMsgNew(keyID, nil, msgHash[:], idsBytes)
	if err != nil {
		t.Fatal(err)
	}
	type signState struct {
		session dklssession.Handle
		share   dklssession.Handle
	}
	sessions := make(map[string]signState, len(ids))
	defer func() {
		for _, state := range sessions {
			_ = dklssession.DklsKeyshareFree(state.share)
			_ = dklssession.DklsSignSessionFree(state.session)
		}
	}()
	for _, id := range ids {
		share, err := dklssession.DklsKeyshareFromBytes(shares[id])
		if err != nil {
			t.Fatal(err)
		}
		session, err := dklssession.DklsSignSessionFromSetup(setup, []byte(id), share)
		if err != nil {
			t.Fatal(err)
		}
		sessions[id] = signState{session: session, share: share}
	}
	abortMsg, err := dklssession.DklsSignSessionAbortMessage(sessions["p2"].session)
	if err != nil {
		t.Fatal(err)
	}
	done, err := dklssession.DklsSignSessionInputMessage(sessions["p1"].session, abortMsg)
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("expected abort message to finish receiver session")
	}
	if _, err := dklssession.DklsSignSessionFinish(sessions["p1"].session); err == nil || !strings.Contains(err.Error(), "Protocol abort by party 2") {
		t.Fatalf("expected sign abort by party 2, got %v", err)
	}
}

func testDKLSWrapperKeygenInMemory(t *testing.T, batchFirstRound bool) {
	ids, idsBytes := dklsIDs([]string{"cthorpub1addwnpepqf40hmsl6443fhyy5z00dwg0wdttp2x5eascnx43dnqy4xtj09gr707pdt8", "cthorpub1addwnpepqf4tat5t69l8apydlam9lngr0hjj7c7pfcl44s0cpldj2rp264v72ks8trc", "cthorpub1addwnpepqfshsq2y6ejy2ysxmq4gj8n8mzuzyulk9wh4n946jv5w2vpwdn2yuwlsyz7", "cthorpub1addwnpepqtk2tngywwsly2wu90k30xjccafv5tsss3x8y0udsfv6yy8u0h7mjpw6l98", "cthorpub1addwnpepqwlkdxcyz8fnxk3xp0eqt89g6cxau2c4zp89yr2x2dy56s2zgu2jxml3fmh"})
	threshold, err := dklsThreshold(len(ids))
	if err != nil {
		t.Fatal(err)
	}
	keyID := sha256.Sum256([]byte("test-dkls-keygen"))
	setup, err := dklssession.DklsKeygenSetupMsgNew(threshold, keyID[:], idsBytes)
	if err != nil {
		t.Fatal(err)
	}

	sessions := make(map[string]dklssession.Handle, len(ids))
	inputFinished := make(map[string]bool, len(ids))
	defer func() {
		for id, handle := range sessions {
			if !inputFinished[id] {
				_ = dklssession.DklsKeygenSessionFree(handle)
			}
		}
	}()
	for _, id := range ids {
		handle, err := dklssession.DklsKeygenSessionFromSetup(setup, []byte(id))
		if err != nil {
			t.Fatal(err)
		}
		sessions[id] = handle
	}
	if batchFirstRound {
		type delivery struct {
			receiver string
			msg      []byte
		}
		var deliveries []delivery
		for _, id := range ids {
			out, err := dklssession.DklsKeygenSessionOutputMessage(sessions[id])
			if err != nil {
				t.Fatal(err)
			}
			for idx := 0; idx < len(ids); idx++ {
				receiver, err := dklssession.DklsKeygenSessionMessageReceiver(sessions[id], out, idx)
				if err != nil {
					t.Fatal(err)
				}
				if receiver == "" {
					break
				}
				deliveries = append(deliveries, delivery{receiver: receiver, msg: out})
			}
		}
		for i := len(deliveries) - 1; i >= 0; i-- {
			done, err := dklssession.DklsKeygenSessionInputMessage(sessions[deliveries[i].receiver], deliveries[i].msg)
			if err != nil {
				t.Fatal(err)
			}
			if done {
				inputFinished[deliveries[i].receiver] = true
			}
		}
	}

	for round := 0; round < 10000; round++ {
		progress := false
		for _, handle := range sessions {
			out, err := dklssession.DklsKeygenSessionOutputMessage(handle)
			if err != nil {
				t.Fatal(err)
			}
			if len(out) == 0 {
				continue
			}
			progress = true
			for idx := 0; idx < len(ids); idx++ {
				receiver, err := dklssession.DklsKeygenSessionMessageReceiver(handle, out, idx)
				if err != nil {
					t.Fatal(err)
				}
				if receiver == "" {
					break
				}
				if inputFinished[receiver] {
					continue
				}
				target := sessions[receiver]
				done, err := dklssession.DklsKeygenSessionInputMessage(target, out)
				if err != nil {
					t.Fatal(err)
				}
				if done {
					inputFinished[receiver] = true
				}
			}
		}
		if len(inputFinished) == len(ids) {
			for id, handle := range sessions {
				share, err := dklssession.DklsKeygenSessionFinish(handle)
				if err != nil {
					t.Fatalf("%s finish: %v", id, err)
				}
				defer dklssession.DklsKeyshareFree(share)
				if _, err := dklssession.DklsKeyshareToBytes(share); err != nil {
					t.Fatalf("%s share bytes: %v", id, err)
				}
			}
			return
		}
		if !progress {
			t.Fatalf("dkls keygen stalled with %d/%d finished", len(inputFinished), len(ids))
		}
	}
	t.Fatalf("dkls keygen finished %d/%d parties", len(inputFinished), len(ids))
}

func testDKLSWrapperKeygenShares(t *testing.T, ids []string) (map[string][]byte, *btcec.PublicKey) {
	t.Helper()
	ids, idsBytes := dklsIDs(ids)
	threshold, err := dklsThreshold(len(ids))
	if err != nil {
		t.Fatal(err)
	}
	keyID := sha256.Sum256([]byte("test-dkls-concurrent-keysign-keygen"))
	setup, err := dklssession.DklsKeygenSetupMsgNew(threshold, keyID[:], idsBytes)
	if err != nil {
		t.Fatal(err)
	}
	sessions := make(map[string]dklssession.Handle, len(ids))
	finished := make(map[string]bool, len(ids))
	shares := make(map[string][]byte, len(ids))
	defer func() {
		for id, handle := range sessions {
			if !finished[id] {
				_ = dklssession.DklsKeygenSessionFree(handle)
			}
		}
	}()
	for _, id := range ids {
		handle, err := dklssession.DklsKeygenSessionFromSetup(setup, []byte(id))
		if err != nil {
			t.Fatal(err)
		}
		sessions[id] = handle
	}
	for step := 0; step < 10000 && len(finished) < len(ids); step++ {
		progressed := false
		for _, from := range ids {
			out, err := dklssession.DklsKeygenSessionOutputMessage(sessions[from])
			if err != nil {
				t.Fatal(err)
			}
			if len(out) == 0 {
				continue
			}
			progressed = true
			for idx := 0; idx < len(ids); idx++ {
				receiver, err := dklssession.DklsKeygenSessionMessageReceiver(sessions[from], out, idx)
				if err != nil {
					t.Fatal(err)
				}
				if receiver == "" {
					break
				}
				if finished[receiver] {
					continue
				}
				done, err := dklssession.DklsKeygenSessionInputMessage(sessions[receiver], out)
				if err != nil {
					t.Fatal(err)
				}
				if done {
					finished[receiver] = true
				}
			}
		}
		if len(finished) == len(ids) {
			for _, id := range ids {
				share, err := dklssession.DklsKeygenSessionFinish(sessions[id])
				if err != nil {
					t.Fatal(err)
				}
				shareBytes, err := dklssession.DklsKeyshareToBytes(share)
				if err != nil {
					t.Fatal(err)
				}
				shares[id] = shareBytes
				_ = dklssession.DklsKeyshareFree(share)
			}
			break
		}
		if !progressed {
			t.Fatalf("dkls keygen stalled with %d/%d finished", len(finished), len(ids))
		}
	}
	if len(shares) != len(ids) {
		t.Fatalf("dkls keygen finished %d/%d parties", len(shares), len(ids))
	}
	share, err := dklssession.DklsKeyshareFromBytes(shares[ids[0]])
	if err != nil {
		t.Fatal(err)
	}
	defer dklssession.DklsKeyshareFree(share)
	pkBytes, err := dklssession.DklsKeysharePublicKey(share)
	if err != nil {
		t.Fatal(err)
	}
	pubKey, err := btcec.ParsePubKey(pkBytes, btcec.S256())
	if err != nil {
		t.Fatal(err)
	}
	return shares, pubKey
}

func testDKLSWrapperSign(t *testing.T, ids []string, shares map[string][]byte, keyID []byte, idsBytes []byte, msgHash []byte) (*btcec.Signature, error) {
	t.Helper()
	setup, err := dklssession.DklsSignSetupMsgNew(keyID, nil, msgHash, idsBytes)
	if err != nil {
		return nil, err
	}
	type signState struct {
		session dklssession.Handle
		share   dklssession.Handle
	}
	sessions := make(map[string]signState, len(ids))
	finished := make(map[string]bool, len(ids))
	defer func() {
		for id, state := range sessions {
			_ = dklssession.DklsKeyshareFree(state.share)
			if !finished[id] {
				_ = dklssession.DklsSignSessionFree(state.session)
			}
		}
	}()
	for _, id := range ids {
		share, err := dklssession.DklsKeyshareFromBytes(shares[id])
		if err != nil {
			return nil, err
		}
		session, err := dklssession.DklsSignSessionFromSetup(setup, []byte(id), share)
		if err != nil {
			_ = dklssession.DklsKeyshareFree(share)
			return nil, err
		}
		sessions[id] = signState{session: session, share: share}
	}
	var signature []byte
	for step := 0; step < 10000 && len(finished) < len(ids); step++ {
		progressed := false
		for _, from := range ids {
			state := sessions[from]
			out, err := dklssession.DklsSignSessionOutputMessage(state.session)
			if err != nil {
				return nil, err
			}
			if len(out) == 0 {
				continue
			}
			progressed = true
			for idx := 0; idx < len(ids); idx++ {
				receiver, err := dklssession.DklsSignSessionMessageReceiver(state.session, out, idx)
				if err != nil {
					return nil, err
				}
				receiverID := string(receiver)
				if receiverID == "" {
					break
				}
				if finished[receiverID] {
					continue
				}
				receiverState := sessions[receiverID]
				done, err := dklssession.DklsSignSessionInputMessage(receiverState.session, out)
				if err != nil {
					return nil, err
				}
				if done {
					finished[receiverID] = true
				}
			}
		}
		if len(finished) == len(ids) {
			for _, id := range ids {
				state := sessions[id]
				sig, err := dklssession.DklsSignSessionFinish(state.session)
				if err != nil {
					return nil, err
				}
				if len(sig) < 64 {
					return nil, errors.New("invalid DKLS signature length")
				}
				if signature == nil {
					signature = sig
				}
			}
			break
		}
		if !progressed {
			return nil, fmt.Errorf("dkls sign stalled with %d/%d finished", len(finished), len(ids))
		}
	}
	if signature == nil {
		return nil, errors.New("dkls sign did not produce a signature")
	}
	return &btcec.Signature{
		R: new(big.Int).SetBytes(signature[:32]),
		S: new(big.Int).SetBytes(signature[32:64]),
	}, nil
}
