package tss

import (
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

func newVerifierCov(t *testing.T, members []string, round string, expected ...string) *frostTranscriptVerifier {
	t.Helper()
	_, indexMap, byNorm, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatal(err)
	}
	active, err := buildActiveSet(members, byNorm)
	if err != nil {
		t.Fatal(err)
	}
	v, err := newFrostTranscriptVerifier(round, active, indexMap, expected...)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestFrostSignedTranscriptCovErrors(t *testing.T) {
	ensureBech32Prefix()
	priv := frostPrivKeyCov(t, 0)
	hash := testHashOf('a')

	sess := testHashOf('f')
	if _, err := newFrostSignedEnvelope(nil, sess, "v", "mainnet", frostRoundShare, testPubKeys[0], 1, hash); err == nil ||
		!strings.Contains(err.Error(), "signing key is unavailable") {
		t.Fatalf("nil key: %v", err)
	}
	if _, err := newFrostSignedEnvelope(priv, "", "v", "mainnet", frostRoundShare, testPubKeys[0], 1, hash); err == nil {
		t.Fatal("envelope without session accepted")
	}
	env, err := newFrostSignedEnvelope(priv, " "+sess+" ", "v", "mainnet", frostRoundShare, testPubKeys[0], 1, strings.ToUpper(hash))
	if err != nil {
		t.Fatal(err)
	}
	if env.SessionID != sess || env.PayloadHash != hash {
		t.Fatalf("envelope not normalized: %+v", env)
	}
	if err := verifyFrostSignedEnvelope(env); err != nil {
		t.Fatalf("valid envelope rejected: %v", err)
	}

	unsigned := env
	unsigned.Signature = nil
	if err := verifyFrostSignedEnvelope(unsigned); err == nil || !strings.Contains(err.Error(), "signature is missing") {
		t.Fatalf("missing signature: %v", err)
	}
	oversized := env
	oversized.Signature = make([]byte, frostMaxNodeSignatureBytes+1)
	if err := verifyFrostSignedEnvelope(oversized); err == nil || !strings.Contains(err.Error(), "signature is missing") {
		t.Fatalf("oversized signature: %v", err)
	}
	badSender := env
	badSender.SenderPubKey = "not-bech32"
	if err := verifyFrostSignedEnvelope(badSender); err == nil {
		t.Fatal("bad sender accepted")
	}
	// signature by key 0 does not verify as key 1
	impostor := env
	impostor.SenderPubKey = testPubKeys[1]
	if err := verifyFrostSignedEnvelope(impostor); err == nil || !strings.Contains(err.Error(), "invalid FROST transcript envelope signature") {
		t.Fatalf("impostor: %v", err)
	}
	broken := env
	broken.SessionID = ""
	if err := verifyFrostSignedEnvelope(broken); err == nil {
		t.Fatal("invalid envelope accepted")
	}

	// echoes
	attester := frostPrivKeyCov(t, 1)
	if _, err := newFrostSignedEcho(nil, testPubKeys[1], env); err == nil {
		t.Fatal("nil echo key accepted")
	}
	if _, err := newFrostSignedEcho(attester, "", env); err == nil {
		t.Fatal("echo without attester accepted")
	}
	echo, err := newFrostSignedEcho(attester, testPubKeys[1], env)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyFrostSignedEcho(echo); err != nil {
		t.Fatalf("valid echo rejected: %v", err)
	}
	badTarget := echo
	badTarget.Target = unsigned
	if err := verifyFrostSignedEcho(badTarget); err == nil {
		t.Fatal("echo of unsigned target accepted")
	}
	noSig := echo
	noSig.Signature = nil
	if err := verifyFrostSignedEcho(noSig); err == nil || !strings.Contains(err.Error(), "echo signature is missing") {
		t.Fatalf("echo without signature: %v", err)
	}
	badAttester := echo
	badAttester.AttesterPubKey = "garbage"
	if err := verifyFrostSignedEcho(badAttester); err == nil {
		t.Fatal("bad attester accepted")
	}
	wrongAttester := echo
	wrongAttester.AttesterPubKey = testPubKeys[2]
	if err := verifyFrostSignedEcho(wrongAttester); err == nil || !strings.Contains(err.Error(), "invalid FROST transcript echo signature") {
		t.Fatalf("wrong attester: %v", err)
	}

	// the wire form of an echo round-trips through frostEchoFromVerify
	wire := FrostVerifyPayload{
		ProtocolVersion:        echo.ProtocolVersion,
		SenderPubKey:           echo.AttesterPubKey,
		Round:                  env.Round,
		TargetSenderPubKey:     env.SenderPubKey,
		TargetParticipantIndex: env.ParticipantIndex,
		PayloadHash:            env.PayloadHash,
		TargetSignature:        env.Signature,
		AttesterSignature:      echo.Signature,
	}
	if err := verifyFrostSignedEcho(frostEchoFromVerify(env.SessionID, env.VaultPubKey, env.Network, wire)); err != nil {
		t.Fatalf("round-tripped echo rejected: %v", err)
	}
}

func TestFrostTranscriptVerifierCovConstruction(t *testing.T) {
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
	if _, err := newFrostTranscriptVerifier(frostRoundShare, nil, indexMap); err == nil {
		t.Fatal("nil active set accepted")
	}
	if _, err := newFrostTranscriptVerifier(frostRoundShare, &activeSignerSet{}, indexMap); err == nil {
		t.Fatal("empty active set accepted")
	}
	if _, err := newFrostTranscriptVerifier(frostRoundShare, active, nil); err == nil {
		t.Fatal("empty index map accepted")
	}
	if _, err := newFrostTranscriptVerifier(" ", active, indexMap); err == nil {
		t.Fatal("empty round accepted")
	}
	v, err := newFrostTranscriptVerifier(frostRoundShare, active, indexMap, " vault ", " net ")
	if err != nil {
		t.Fatal(err)
	}
	// two members need both confirmations
	if v.expectedVaultPubKey != "vault" || v.expectedNetwork != "net" || v.confirmationQuorum != 2 || v.totalParticipants != 2 {
		t.Fatalf("unexpected verifier: %+v", v)
	}
	if firstTrimmed(nil) != "" || secondTrimmed([]string{"a"}) != "" {
		t.Fatal("missing expected values must be empty")
	}
}

func TestFrostTranscriptVerifierCovRecordSignedEnvelope(t *testing.T) {
	ensureBech32Prefix()
	members := []string{testPubKeys[0], testPubKeys[1]}
	v := newVerifierCov(t, members, frostRoundShare, "vault", "mainnet")
	idx := v.participantIndexMap[normalizePubKey(testPubKeys[0])]
	priv := frostPrivKeyCov(t, 0)
	sign := func(vault, network, round, sender string, index uint32, hash string) messages.FrostSignedEnvelope {
		env, err := newFrostSignedEnvelope(priv, testHashOf('f'), vault, network, round, sender, index, hash)
		if err != nil {
			t.Fatal(err)
		}
		return env
	}

	cases := []struct {
		env  messages.FrostSignedEnvelope
		want string
	}{
		{sign("other", "mainnet", frostRoundShare, testPubKeys[0], idx, testHashOf('a')), "vault pubkey mismatch"},
		{sign("vault", "stagenet", frostRoundShare, testPubKeys[0], idx, testHashOf('a')), "network mismatch"},
		{sign("vault", "mainnet", frostRoundShare, testPubKeys[3], idx, testHashOf('a')), "not in active participant set"},
		{sign("vault", "mainnet", frostRoundShare, testPubKeys[0], idx+7, testHashOf('a')), "participant index mismatch"},
		{sign("vault", "mainnet", frostRoundPreprocess, testPubKeys[0], idx, testHashOf('a')), "round mismatch"},
	}
	for _, tc := range cases {
		if _, err := v.recordSignedEnvelope(tc.env); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("want %q got %v", tc.want, err)
		}
	}
	badHash := sign("vault", "mainnet", frostRoundShare, testPubKeys[0], idx, testHashOf('a'))
	badHash.PayloadHash = "xyz"
	if _, err := v.recordSignedEnvelope(badHash); err == nil {
		t.Fatal("bad hash accepted")
	}

	first := sign("vault", "mainnet", frostRoundShare, testPubKeys[0], idx, testHashOf('a'))
	if ev, err := v.recordSignedEnvelope(first); ev != nil || err != nil {
		t.Fatalf("first envelope: ev=%v err=%v", ev, err)
	}
	if ev, err := v.recordSignedEnvelope(first); ev != nil || err != nil {
		t.Fatalf("duplicate envelope must be idempotent: ev=%v err=%v", ev, err)
	}
	second := sign("vault", "mainnet", frostRoundShare, testPubKeys[0], idx, testHashOf('b'))
	ev, err := v.recordSignedEnvelope(second)
	if err != nil || ev == nil || ev.First.PayloadHash != testHashOf('a') || ev.Second.PayloadHash != testHashOf('b') {
		t.Fatalf("expected equivocation evidence: ev=%+v err=%v", ev, err)
	}
	// unsigned conflicting envelope cannot form valid portable evidence
	unsigned := sign("vault", "mainnet", frostRoundShare, testPubKeys[0], idx, testHashOf('c'))
	unsigned.Signature = nil
	if ev, err := v.recordSignedEnvelope(unsigned); ev != nil || err == nil {
		t.Fatalf("invalid evidence must error: ev=%v err=%v", ev, err)
	}
}

func TestFrostTranscriptVerifierCovRecordPayloadAndVerification(t *testing.T) {
	ensureBech32Prefix()
	members := []string{testPubKeys[0], testPubKeys[1]}
	v := newVerifierCov(t, members, frostRoundShare)
	i0 := v.participantIndexMap[normalizePubKey(testPubKeys[0])]
	i1 := v.participantIndexMap[normalizePubKey(testPubKeys[1])]

	if _, failure := v.recordPayload(testPubKeys[0], i0, "bad"); failure == nil ||
		failure.Error() != "invalid transcript payload hash" || len(failure.blamePubKey) != 1 {
		t.Fatalf("bad payload hash: %+v", failure)
	}
	if isNew, failure := v.recordPayload(testPubKeys[0], i0, testHashOf('a')); !isNew || failure != nil {
		t.Fatalf("first payload: %v %+v", isNew, failure)
	}
	if isNew, failure := v.recordPayload(testPubKeys[0], i0, strings.ToUpper(testHashOf('a'))); isNew || failure != nil {
		t.Fatalf("duplicate payload: %v %+v", isNew, failure)
	}
	if _, failure := v.recordPayload(testPubKeys[0], i0, testHashOf('b')); failure == nil ||
		!strings.Contains(failure.reason, "conflicting round payloads") || len(failure.blamePubKey) != 0 {
		t.Fatalf("conflict must abort without blame: %+v", failure)
	}

	if _, failure := v.recordVerification("", testPubKeys[0], i0, testHashOf('a')); failure == nil ||
		!strings.Contains(failure.reason, "attester is not in active participant set") {
		t.Fatalf("unknown attester: %+v", failure)
	}
	if _, failure := v.recordVerification(testPubKeys[1], testPubKeys[3], i0, testHashOf('a')); failure == nil ||
		!strings.Contains(failure.reason, "verification target is not in active participant set") ||
		failure.blamePubKey[0] != testPubKeys[1] {
		t.Fatalf("unknown target blames the attester: %+v", failure)
	}
	if _, failure := v.recordVerification(testPubKeys[1], testPubKeys[0], i0, "bad"); failure == nil ||
		!strings.Contains(failure.reason, "verification payload hash is invalid") {
		t.Fatalf("bad vote hash: %+v", failure)
	}

	// attester 1 votes, then equivocates: its vote is dropped and it is
	// disqualified from voting again for any target
	if counted, failure := v.recordVerification(testPubKeys[1], testPubKeys[0], i0, testHashOf('a')); !counted || failure != nil {
		t.Fatalf("first vote: %v %+v", counted, failure)
	}
	if counted, _ := v.recordVerification(testPubKeys[1], testPubKeys[0], i0, testHashOf('a')); counted {
		t.Fatal("duplicate vote counted twice")
	}
	if counted, failure := v.recordVerification(testPubKeys[1], testPubKeys[0], i0, testHashOf('c')); counted || failure != nil {
		t.Fatalf("conflicting vote: %v %+v", counted, failure)
	}
	if _, ok := v.equivocatingVoters[normalizePubKey(testPubKeys[1])]; !ok {
		t.Fatal("equivocating voter not disqualified")
	}
	if counted, _ := v.recordVerification(testPubKeys[1], testPubKeys[1], i1, testHashOf('d')); counted {
		t.Fatal("disqualified voter counted")
	}

	// target 0 has a payload but is unverified; both members still owe a vote
	if got := v.missingVerifierPubKeys(); len(got) != 2 {
		t.Fatalf("missing verifiers: %v", got)
	}
	if got := v.missingPayloadPubKeys(); len(got) != 1 || got[0] != testPubKeys[1] {
		t.Fatalf("missing payloads: %v", got)
	}
	if v.isComplete() {
		t.Fatal("incomplete transcript reported complete")
	}
}

func TestTranscriptTimeoutReasonCov(t *testing.T) {
	cases := []struct {
		payloads, verifiers []string
		want                string
	}{
		{[]string{"a"}, []string{"b"}, "timeout waiting for x and transcript verifications"},
		{[]string{"a"}, nil, "timeout waiting for x"},
		{nil, []string{"b"}, "timeout waiting for x transcript verifications"},
		{nil, nil, "timeout waiting for transcript completion"},
	}
	for _, tc := range cases {
		if got := transcriptTimeoutReason("x", tc.payloads, tc.verifiers); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
	if got := mergePubKeyLists([]string{" b ", "a", ""}, []string{"a", "c"}); strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("merge: %v", got)
	}
	if f := newFrostTranscriptFailure("  why  ", []string{"k", "k"}); f.Error() != "why" || len(f.blamePubKey) != 1 {
		t.Fatalf("failure: %+v", f)
	}
}

func TestFrostTranscriptHashesCovErrors(t *testing.T) {
	ensureBech32Prefix()
	sender := testPubKeys[0]
	if _, err := FrostTranscriptPreprocessHash("m", FrostPreprocessPayload{SenderPubKey: sender, ParticipantIndex: 1}); err == nil {
		t.Fatal("empty preprocess accepted")
	}
	if _, err := FrostTranscriptShareHash("m", FrostSharePayload{SenderPubKey: sender, ParticipantIndex: 1, ShareHex: " "}); err == nil {
		t.Fatal("empty share accepted")
	}
	if _, err := FrostTranscriptDKGRound1Hash("m", FrostDKGRound1Payload{SenderPubKey: sender, ParticipantIndex: 1}); err == nil {
		t.Fatal("empty commitments accepted")
	}
	for name, p := range map[string]FrostDKGRound3Payload{
		"address":  {SenderPubKey: sender, ParticipantIndex: 1, GroupKey: "ab"},
		"groupkey": {SenderPubKey: sender, ParticipantIndex: 1, Address: "x"},
		"height":   {SenderPubKey: sender, ParticipantIndex: 1, Address: "x", GroupKey: "ab", ObservedMoneroHeight: -1},
	} {
		if _, err := FrostTranscriptDKGRound3Hash("m", p); err == nil {
			t.Fatalf("%s: invalid round3 accepted", name)
		}
	}
	h1, err := FrostTranscriptDKGRound3Hash("m", FrostDKGRound3Payload{SenderPubKey: sender, ParticipantIndex: 1, Address: "x", GroupKey: "AB", ObservedMoneroHeight: 5})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := FrostTranscriptDKGRound3Hash("m", FrostDKGRound3Payload{SenderPubKey: sender, ParticipantIndex: 1, Address: "x", GroupKey: "ab", ObservedMoneroHeight: 5})
	if err != nil || h1 != h2 {
		t.Fatalf("group key case must not change the hash: %s %s %v", h1, h2, err)
	}
	if _, err := FrostTranscriptDKGRound2Hash("m", FrostDKGRound2Payload{SenderPubKey: sender, ParticipantIndex: 1, EncryptedShares: map[string]string{" ": "x"}}); err == nil {
		t.Fatal("empty round2 target accepted")
	}

	for name, args := range map[string]struct {
		round, msgID, sender string
		index                uint32
	}{
		"msgid":  {"r", " ", sender, 1},
		"round":  {" ", "m", sender, 1},
		"sender": {"r", "m", " ", 1},
		"index":  {"r", "m", sender, 0},
	} {
		if _, err := frostTranscriptHash(args.round, args.msgID, args.sender, args.index, nil); err == nil {
			t.Fatalf("%s: invalid transcript hash input accepted", name)
		}
	}
}
