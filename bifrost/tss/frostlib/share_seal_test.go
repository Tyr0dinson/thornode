//go:build stagenet || mocknet
// +build stagenet mocknet

package frostlib

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"math/big"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/ecies"
)

func newTestNodeKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func newTestLockKey(t *testing.T) *lockKey {
	t.Helper()
	key, err := newLockKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func testLabel(sender, receiver *ecdsa.PrivateKey, lock *lockKey) shareLabel {
	transcript := sha256.Sum256([]byte("transcript"))
	return shareLabel{
		sessionID:     "session-1",
		transcript:    transcript[:],
		senderIndex:   1,
		receiverIndex: 2,
		sender:        &sender.PublicKey,
		receiver:      &receiver.PublicKey,
		lockKey:       lock.pub,
	}
}

// signLocked signs arbitrary locked bytes as the sender, to build shares a
// dishonest sender would produce.
func signLocked(t *testing.T, sender *ecdsa.PrivateKey, label shareLabel, locked []byte) []byte {
	t.Helper()
	sig, err := signWithNodeKey(sender, label.digest(locked))
	if err != nil {
		t.Fatal(err)
	}
	return append(append([]byte{sealedShareVersion}, sig...), locked...)
}

func lockedPart(sealed []byte) []byte { return sealed[1+nodeSigLen:] }

func TestSealedShareRoundTrip(t *testing.T) {
	sender, receiver, lock := newTestNodeKey(t), newTestNodeKey(t), newTestLockKey(t)
	label := testLabel(sender, receiver, lock)
	share := []byte("round-2 secret share")

	sealed, err := sealShare(sender, label, share)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, share) {
		t.Fatal("sealed share contains the plaintext")
	}
	if _, err = verifySealedShare(label, sealed); err != nil {
		t.Fatalf("a third party must be able to verify the signature: %v", err)
	}
	opened, err := openSealedShare(label, lock, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, share) {
		t.Fatalf("opened %q, want %q", opened, share)
	}
	again, err := sealShare(sender, label, share)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(again, sealed) {
		t.Fatal("sealing the same share twice produced identical bytes")
	}
}

// Assumption: only the receiver can read a share. The node keys open nothing;
// only the throwaway lock key does.
func TestSealedShareOpensOnlyWithItsLockKey(t *testing.T) {
	sender, receiver, lock, otherLock := newTestNodeKey(t), newTestNodeKey(t), newTestLockKey(t), newTestLockKey(t)
	label := testLabel(sender, receiver, lock)
	sealed, err := sealShare(sender, label, []byte("share"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = openSealedShare(label, otherLock, sealed); err == nil {
		t.Fatal("another lock key opened the share")
	}
	for name, nodeKey := range map[string]*ecdsa.PrivateKey{"sender": sender, "receiver": receiver} {
		asLock := &lockKey{priv: ethcrypto.FromECDSA(nodeKey), pub: &nodeKey.PublicKey}
		if _, err = openSealedShare(label, asLock, sealed); err == nil {
			t.Fatalf("the %s node key opened the share", name)
		}
		if _, err = ecies.ImportECDSA(nodeKey).Decrypt(lockedPart(sealed), nil, label.bytes()); err == nil {
			t.Fatalf("the %s node key decrypted the lock directly", name)
		}
	}
}

// Assumption: a share is accepted only from the node that signed it.
func TestSealedShareRejectsWrongSenderAndTampering(t *testing.T) {
	sender, receiver, other, lock := newTestNodeKey(t), newTestNodeKey(t), newTestNodeKey(t), newTestLockKey(t)
	label := testLabel(sender, receiver, lock)
	sealed, err := sealShare(sender, label, []byte("share"))
	if err != nil {
		t.Fatal(err)
	}

	asOther := label
	asOther.sender = &other.PublicKey
	if _, err = openSealedShare(asOther, lock, sealed); !errors.Is(err, errSealedShare) {
		t.Fatalf("expected a sealed share error for a wrong sender, got %v", err)
	}
	for _, offset := range []int{0, 1, nodeSigLen, 1 + nodeSigLen, 1 + nodeSigLen + lockedSharePointLen, len(sealed) - 1} {
		tampered := append([]byte(nil), sealed...)
		tampered[offset] ^= 0x01
		if _, err = openSealedShare(label, lock, tampered); !errors.Is(err, errSealedShare) {
			t.Fatalf("expected a sealed share error after flipping byte %d, got %v", offset, err)
		}
	}
	for _, short := range [][]byte{nil, {sealedShareVersion}, sealed[:1+nodeSigLen]} {
		if _, err = openSealedShare(label, lock, short); !errors.Is(err, errSealedShare) {
			t.Fatalf("expected a sealed share error for %d bytes, got %v", len(short), err)
		}
	}
	if _, err = sealShare(other, label, []byte("share")); err == nil {
		t.Fatal("a node sealed a share under another node's sender label")
	}
}

// Assumption: a share is valid in one keygen, from one sender, to one receiver.
func TestSealedShareIsBoundToItsLabel(t *testing.T) {
	sender, receiver, lock := newTestNodeKey(t), newTestNodeKey(t), newTestLockKey(t)
	label := testLabel(sender, receiver, lock)
	sealed, err := sealShare(sender, label, []byte("share"))
	if err != nil {
		t.Fatal(err)
	}
	otherSession, otherTranscript, otherSender, otherReceiver := label, label, label, label
	otherSession.sessionID = "session-2"
	changed := sha256.Sum256([]byte("another transcript"))
	otherTranscript.transcript = changed[:]
	otherSender.senderIndex = 3
	otherReceiver.receiverIndex = 3
	for name, l := range map[string]shareLabel{
		"session": otherSession, "transcript": otherTranscript, "sender index": otherSender, "receiver index": otherReceiver,
	} {
		if _, err = openSealedShare(l, lock, sealed); !errors.Is(err, errSealedShare) {
			t.Fatalf("expected a sealed share error for another %s, got %v", name, err)
		}
	}
	a, b := label, label
	a.sessionID, b.sessionID = "ab", "a"
	if bytes.Equal(a.bytes(), b.bytes()) {
		t.Fatal("different labels share an encoding")
	}
}

// A member copies another member's locked share and signs it as its own. The
// receiver cannot open it and disputes. The key it reveals must open neither
// the copy nor the original: the original share stays secret.
func TestDisputeOverCopiedLockRevealsNothing(t *testing.T) {
	honest, receiver, thief := newTestNodeKey(t), newTestNodeKey(t), newTestNodeKey(t)
	lockForHonest, lockForThief := newTestLockKey(t), newTestLockKey(t)
	original := testLabel(honest, receiver, lockForHonest)
	secret := []byte("the honest sender's share")
	sealed, err := sealShare(honest, original, secret)
	if err != nil {
		t.Fatal(err)
	}

	copied := testLabel(thief, receiver, lockForThief)
	copied.senderIndex = 3
	resigned := signLocked(t, thief, copied, lockedPart(sealed))
	if _, err = openSealedShare(copied, lockForThief, resigned); !errors.Is(err, errSealedShare) {
		t.Fatalf("the receiver opened a copied lock: %v", err)
	}

	// The receiver disputes by revealing the key it announced for the thief.
	verdict, share, err := judgeSealedShare(copied, resigned, lockForThief.priv)
	if err != nil || verdict != ShareDisputeSenderAtFault || share != nil {
		t.Fatalf("expected the thief at fault with nothing revealed, got verdict=%d share=%q err=%v", verdict, share, err)
	}
	// With that revealed key, nobody can open the honest sender's lock.
	if _, err = unlockShare(original, lockForThief.priv, lockedPart(sealed)); err == nil {
		t.Fatal("the revealed key opened the original share")
	}
	revealed, err := ethcrypto.ToECDSA(lockForThief.priv)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range [][]byte{original.bytes(), copied.bytes(), nil} {
		if plain, decErr := ecies.ImportECDSA(revealed).Decrypt(lockedPart(sealed), nil, info); decErr == nil {
			t.Fatalf("the revealed key decrypted the original lock to %q", plain)
		}
	}
}

func TestJudgeSealedShareVerdicts(t *testing.T) {
	sender, receiver, lock, otherLock := newTestNodeKey(t), newTestNodeKey(t), newTestLockKey(t), newTestLockKey(t)
	label := testLabel(sender, receiver, lock)
	share := []byte("share")
	sealed, err := sealShare(sender, label, share)
	if err != nil {
		t.Fatal(err)
	}
	expect := func(name string, sealedShare, key []byte, want ShareDisputeVerdict) {
		t.Helper()
		verdict, opened, judgeErr := judgeSealedShare(label, sealedShare, key)
		if judgeErr != nil || verdict != want {
			t.Fatalf("%s: verdict=%d err=%v, want %d", name, verdict, judgeErr, want)
		}
		if (want == ShareDisputeShareOpened) != bytes.Equal(opened, share) {
			t.Fatalf("%s: unexpected share %q", name, opened)
		}
	}

	// A good share is opened for everyone, exposing a baseless dispute.
	expect("good share", sealed, lock.priv, ShareDisputeShareOpened)
	// A receiver cannot make a good lock look broken with another key.
	expect("wrong revealed key", sealed, otherLock.priv, ShareDisputeReceiverAtFault)
	expect("node key instead of lock key", sealed, ethcrypto.FromECDSA(receiver), ShareDisputeReceiverAtFault)
	expect("malformed revealed key", sealed, []byte{1, 2, 3}, ShareDisputeReceiverAtFault)
	expect("no revealed key", sealed, nil, ShareDisputeReceiverAtFault)

	// Shares a dishonest sender signed.
	badSig := append([]byte(nil), sealed...)
	badSig[1] ^= 0x01
	expect("bad signature", badSig, lock.priv, ShareDisputeSenderAtFault)
	brokenTag := append([]byte(nil), lockedPart(sealed)...)
	brokenTag[len(brokenTag)-1] ^= 0x01
	expect("broken tag", signLocked(t, sender, label, brokenTag), lock.priv, ShareDisputeSenderAtFault)
	otherLabel := label
	otherLabel.receiverIndex = 3
	mislabelled, err := ecies.Encrypt(rand.Reader, ecies.ImportECDSAPublic(lock.pub), share, nil, otherLabel.bytes())
	if err != nil {
		t.Fatal(err)
	}
	expect("locked under another label", signLocked(t, sender, label, mislabelled), lock.priv, ShareDisputeSenderAtFault)
	toOtherKey, err := ecies.Encrypt(rand.Reader, ecies.ImportECDSAPublic(otherLock.pub), share, nil, label.bytes())
	if err != nil {
		t.Fatal(err)
	}
	expect("locked to another key", signLocked(t, sender, label, toOtherKey), lock.priv, ShareDisputeSenderAtFault)
}

// Locks the ECIES library must never be handed: too short, a one-time key off
// the curve, or one encoded with a coordinate above the field prime. Each is
// refused cleanly on the receiver path and judged against the sender.
func TestMalformedLocksAreRefusedWithoutPanic(t *testing.T) {
	sender, receiver, lock := newTestNodeKey(t), newTestNodeKey(t), newTestLockKey(t)
	label := testLabel(sender, receiver, lock)
	sealed, err := sealShare(sender, label, []byte("share"))
	if err != nil {
		t.Fatal(err)
	}
	good := lockedPart(sealed)

	offCurve := append([]byte(nil), good...)
	offCurve[lockedSharePointLen-1] ^= 0x01

	// x = 1 + p with y^2 = 1 + 7: a point whose x is not reduced.
	p := ethcrypto.S256().Params().P
	y := new(big.Int).ModSqrt(big.NewInt(8), p)
	if y == nil {
		t.Fatal("test setup: 8 is not a square mod p")
	}
	nonCanonical := append([]byte(nil), good...)
	nonCanonical[0] = 4
	new(big.Int).Add(p, big.NewInt(1)).FillBytes(nonCanonical[1:33])
	y.FillBytes(nonCanonical[33:65])

	// Correctly keyed and tagged, but with no room for the IV.
	short := make([]byte, lockedShareMinLen-1)
	copy(short, good[:lockedSharePointLen])

	for name, locked := range map[string][]byte{
		"off-curve one-time key":     offCurve,
		"non-canonical one-time key": nonCanonical,
		"too short":                  short,
		"empty":                      nil,
	} {
		candidate := signLocked(t, sender, label, locked)
		if _, err = openSealedShare(label, lock, candidate); !errors.Is(err, errSealedShare) {
			t.Fatalf("%s: expected a sealed share error, got %v", name, err)
		}
		verdict, _, judgeErr := judgeSealedShare(label, candidate, lock.priv)
		if judgeErr != nil || verdict != ShareDisputeSenderAtFault {
			t.Fatalf("%s: verdict=%d err=%v, want the sender at fault", name, verdict, judgeErr)
		}
	}
}

func testRound1Context(n int, senderIndex uint32) round1Context {
	participants := make([]string, n)
	for i := range participants {
		participants[i] = string(rune('a' + i))
	}
	return round1Context{sessionID: "session-1", threshold: 2, participants: participants, senderIndex: senderIndex}
}

func TestRound1MessageRoundTripAndBinding(t *testing.T) {
	node, other := newTestNodeKey(t), newTestNodeKey(t)
	ctx := testRound1Context(3, 2)
	keys := []*ecdsa.PublicKey{newTestLockKey(t).pub, newTestLockKey(t).pub}
	raw, err := encodeRound1Message(node, ctx, []byte("frost package"), keys)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := decodeRound1Message(ctx, &node.PublicKey, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(msg.frost, []byte("frost package")) || len(msg.lockKeys) != 2 {
		t.Fatalf("unexpected message %+v", msg)
	}
	// Keys are announced in ascending index order, skipping the announcer (2).
	for sender, want := range map[uint32]*ecdsa.PublicKey{1: keys[0], 3: keys[1]} {
		got, keyErr := msg.lockKeyFor(2, sender)
		if keyErr != nil || !got.Equal(want) {
			t.Fatalf("lock key for sender %d: %v", sender, keyErr)
		}
	}
	for _, sender := range []uint32{0, 2, 4} {
		if _, keyErr := msg.lockKeyFor(2, sender); keyErr == nil {
			t.Fatalf("got a lock key for sender %d", sender)
		}
	}

	otherSession, otherThreshold, otherIndex, otherMembers := ctx, ctx, ctx, testRound1Context(3, 2)
	otherSession.sessionID = "session-2"
	otherThreshold.threshold = 3
	otherIndex.senderIndex = 1
	otherMembers.participants[2] = "z"
	for name, c := range map[string]round1Context{
		"session": otherSession, "threshold": otherThreshold, "sender index": otherIndex, "participants": otherMembers,
	} {
		if _, err = decodeRound1Message(c, &node.PublicKey, raw); !errors.Is(err, errRound1Message) {
			t.Fatalf("message verified under another %s: %v", name, err)
		}
	}
	if _, err = decodeRound1Message(ctx, &other.PublicKey, raw); !errors.Is(err, errRound1Message) {
		t.Fatalf("message verified for another signer: %v", err)
	}
	for offset := range raw {
		damaged := append([]byte(nil), raw...)
		damaged[offset] ^= 0x01
		if _, err = decodeRound1Message(ctx, &node.PublicKey, damaged); !errors.Is(err, errRound1Message) {
			t.Fatalf("message with byte %d flipped was accepted: %v", offset, err)
		}
	}
	for length := 0; length < len(raw); length++ {
		if _, err = decodeRound1Message(ctx, &node.PublicKey, raw[:length]); !errors.Is(err, errRound1Message) {
			t.Fatalf("message truncated to %d bytes was accepted: %v", length, err)
		}
	}

	if _, err = encodeRound1Message(node, ctx, []byte("frost package"), keys[:1]); err == nil {
		t.Fatal("encoded a message with too few lock keys")
	}
	duplicate, err := encodeRound1Message(node, ctx, []byte("frost package"), []*ecdsa.PublicKey{keys[0], keys[0]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeRound1Message(ctx, &node.PublicKey, duplicate); !errors.Is(err, errRound1Message) {
		t.Fatalf("a message announcing one lock key twice was accepted: %v", err)
	}
}

func TestTranscriptHashBindsEveryInput(t *testing.T) {
	base := transcriptHash("s", 2, []string{"a", "b"}, [][]byte{[]byte("m1"), []byte("m2")})
	for name, other := range map[string][]byte{
		"session":        transcriptHash("t", 2, []string{"a", "b"}, [][]byte{[]byte("m1"), []byte("m2")}),
		"threshold":      transcriptHash("s", 3, []string{"a", "b"}, [][]byte{[]byte("m1"), []byte("m2")}),
		"participants":   transcriptHash("s", 2, []string{"a", "c"}, [][]byte{[]byte("m1"), []byte("m2")}),
		"round1 message": transcriptHash("s", 2, []string{"a", "b"}, [][]byte{[]byte("m1"), []byte("mX")}),
		"field boundary": transcriptHash("s", 2, []string{"ab"}, [][]byte{[]byte("m1"), []byte("m2")}),
	} {
		if bytes.Equal(base, other) {
			t.Fatalf("transcript hash ignores the %s", name)
		}
	}
}

func TestLockKeyWipe(t *testing.T) {
	key := newTestLockKey(t)
	key.wipe()
	if !bytes.Equal(key.priv, make([]byte, len(key.priv))) {
		t.Fatal("lock key was not wiped")
	}
}
