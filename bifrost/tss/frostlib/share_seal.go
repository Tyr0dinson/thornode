//go:build stagenet || mocknet
// +build stagenet mocknet

package frostlib

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/ecies"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// A DKG round-2 share travels inside a payload that every keygen member
// receives, so each share is locked to its one receiver and signed by its
// sender.
//
// Lock keys are throwaway keys. In its round-1 message every member announces
// one fresh public key per other member, signed with its node key. A share from
// sender S to receiver R is locked (ECIES) to the key R announced for S, and
// signed by S's node key over a label naming the keygen session, its round-1
// transcript, the sender, the receiver and that lock key. The receiver rebuilds
// the label from what it already knows and never reads it from the message.
//
// Because a lock key is used for exactly one share, a receiver can settle a
// dispute by publishing that key's private half: every member then opens the
// lock with the same library call and reaches the same verdict, and the
// published key opens nothing else. No node key is ever used to lock or open,
// and this package implements no cryptography of its own.
//
// NOTE: the lock is the ready-made ECIES of go-ethereum (AES-128-CTR with
// HMAC-SHA256 for secp256k1). It is an old construction whose own README says
// it was never formally reviewed. Move to HPKE (RFC 9180) once a standard KEM
// for secp256k1 is available in a maintained library; the version bytes below
// exist so both formats can be told apart during that change.

const (
	sealedShareVersion   byte = 1
	round1MessageVersion byte = 1
	sealedShareDomain         = "thorchain/frost/dkg-round2-share"
	round1MessageDomain       = "thorchain/frost/dkg-round1"
	transcriptDomain          = "thorchain/frost/dkg-transcript"

	// nodeSigLen is the length of a compact secp256k1 signature (r || s).
	nodeSigLen       = 64
	compressedKeyLen = 33
	// Layout of the locked bytes produced by the go-ethereum ECIES for
	// secp256k1: one-time public key (65 bytes, uncompressed) || AES-CTR IV (16)
	// || encrypted share (at least 1) || HMAC-SHA256 tag (32).
	lockedSharePointLen = 65
	lockedShareIVLen    = 16
	lockedShareTagLen   = 32
	lockedShareMinLen   = lockedSharePointLen + lockedShareIVLen + 1 + lockedShareTagLen
)

var (
	errSealedShare   = errors.New("invalid sealed dkg share")
	errRound1Message = errors.New("invalid dkg round1 message")
)

// lengthPrefixed concatenates fields, each behind its 4-byte length, so no two
// different field lists encode to the same bytes.
func lengthPrefixed(fields ...[]byte) []byte {
	var out []byte
	for _, field := range fields {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(field)))
		out = append(out, length[:]...)
		out = append(out, field...)
	}
	return out
}

func uint32Bytes(v uint32) []byte {
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], v)
	return out[:]
}

func signWithNodeKey(key *ecdsa.PrivateKey, digest []byte) ([]byte, error) {
	sig, err := ethcrypto.Sign(digest, key)
	if err != nil {
		return nil, err
	}
	return sig[:nodeSigLen], nil
}

// verifyNodeSig rejects malleable (high-s) signatures.
func verifyNodeSig(key *ecdsa.PublicKey, digest, sig []byte) bool {
	return len(sig) == nodeSigLen && ethcrypto.VerifySignature(ethcrypto.CompressPubkey(key), digest, sig)
}

// lockKey is a throwaway key pair one share is locked to.
type lockKey struct {
	priv []byte
	pub  *ecdsa.PublicKey
}

func newLockKey() (*lockKey, error) {
	key, err := ethcrypto.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("generate lock key: %w", err)
	}
	// Keep only a byte copy of the private half and an independent copy of the
	// public half, so the generated key object can be wiped.
	out := &lockKey{
		priv: ethcrypto.FromECDSA(key),
		pub:  &ecdsa.PublicKey{Curve: key.Curve, X: new(big.Int).Set(key.X), Y: new(big.Int).Set(key.Y)},
	}
	wipeScalar(key.D)
	return out, nil
}

// wipeScalar zeroes the words backing a private scalar. Setting a big.Int to
// zero only changes its length and leaves the value in memory.
func wipeScalar(d *big.Int) {
	if d == nil {
		return
	}
	words := d.Bits()
	for i := range words {
		words[i] = 0
	}
	d.SetInt64(0)
}

func (k *lockKey) wipe() {
	for i := range k.priv {
		k.priv[i] = 0
	}
}

// round1Context is what a round-1 message is signed for.
type round1Context struct {
	sessionID    string
	threshold    uint32
	participants []string // canonical order
	senderIndex  uint32
}

func (c round1Context) digest(body []byte) []byte {
	participants := make([][]byte, len(c.participants))
	for i, participant := range c.participants {
		participants[i] = []byte(participant)
	}
	sum := sha256.Sum256(lengthPrefixed(
		[]byte(round1MessageDomain),
		[]byte{round1MessageVersion},
		[]byte(c.sessionID),
		uint32Bytes(c.threshold),
		lengthPrefixed(participants...),
		uint32Bytes(c.senderIndex),
		body,
	))
	return sum[:]
}

// round1Message is a member's round-1 broadcast: its FROST commitment package
// and the lock keys it announces, one per other member in ascending
// participant index.
type round1Message struct {
	frost    []byte
	lockKeys []*ecdsa.PublicKey
}

// lockKeyFor returns the key the announcer (index announcer) published for
// shares coming from sender.
func (m *round1Message) lockKeyFor(announcer, sender uint32) (*ecdsa.PublicKey, error) {
	if sender == 0 || sender == announcer {
		return nil, errors.New("no lock key for this sender")
	}
	position := sender - 1
	if sender > announcer {
		position--
	}
	if int(position) >= len(m.lockKeys) {
		return nil, errors.New("no lock key for this sender")
	}
	return m.lockKeys[position], nil
}

// encodeRound1Message builds version || len || frost package || count || lock
// keys || node signature.
func encodeRound1Message(nodeKey *ecdsa.PrivateKey, ctx round1Context, frost []byte, lockKeys []*ecdsa.PublicKey) ([]byte, error) {
	if len(frost) == 0 || len(lockKeys) != len(ctx.participants)-1 {
		return nil, errors.New("round1 message needs a frost package and one lock key per other participant")
	}
	body := []byte{round1MessageVersion}
	body = append(body, uint32Bytes(uint32(len(frost)))...)
	body = append(body, frost...)
	body = append(body, uint32Bytes(uint32(len(lockKeys)))...)
	for _, key := range lockKeys {
		body = append(body, ethcrypto.CompressPubkey(key)...)
	}
	sig, err := signWithNodeKey(nodeKey, ctx.digest(body))
	if err != nil {
		return nil, fmt.Errorf("sign round1 message: %w", err)
	}
	return append(body, sig...), nil
}

// decodeRound1Message checks the framing and the sender's node signature.
func decodeRound1Message(ctx round1Context, sender *ecdsa.PublicKey, raw []byte) (*round1Message, error) {
	fail := func(reason string) (*round1Message, error) {
		return nil, fmt.Errorf("%w: %s", errRound1Message, reason)
	}
	if len(raw) < 1+4+1+4+nodeSigLen {
		return fail("too short")
	}
	if raw[0] != round1MessageVersion {
		return fail("unsupported version")
	}
	body, sig := raw[:len(raw)-nodeSigLen], raw[len(raw)-nodeSigLen:]
	rest := body[1:]
	frostLen := binary.BigEndian.Uint32(rest[:4])
	rest = rest[4:]
	if frostLen == 0 || uint64(frostLen)+4 > uint64(len(rest)) {
		return fail("bad frost package length")
	}
	frost := rest[:frostLen]
	rest = rest[frostLen:]
	count := binary.BigEndian.Uint32(rest[:4])
	rest = rest[4:]
	if int(count) != len(ctx.participants)-1 || uint64(len(rest)) != uint64(count)*compressedKeyLen {
		return fail("wrong number of lock keys")
	}
	if !verifyNodeSig(sender, ctx.digest(body), sig) {
		return fail("signature does not match the sender")
	}
	msg := &round1Message{frost: append([]byte(nil), frost...), lockKeys: make([]*ecdsa.PublicKey, count)}
	seen := make(map[string]struct{}, count)
	for i := range msg.lockKeys {
		encoded := rest[i*compressedKeyLen : (i+1)*compressedKeyLen]
		// DecompressPubkey accepts only canonical encodings of curve points.
		key, err := ethcrypto.DecompressPubkey(encoded)
		if err != nil {
			return fail("lock key is not a curve point")
		}
		if _, dup := seen[string(encoded)]; dup {
			return fail("lock key is announced twice")
		}
		seen[string(encoded)] = struct{}{}
		msg.lockKeys[i] = key
	}
	return msg, nil
}

// transcriptHash binds a keygen's parameters and every member's round-1
// message. round1 holds the raw messages in participant order.
func transcriptHash(sessionID string, threshold uint32, participants []string, round1 [][]byte) []byte {
	names := make([][]byte, len(participants))
	for i, participant := range participants {
		names[i] = []byte(participant)
	}
	sum := sha256.Sum256(lengthPrefixed(
		[]byte(transcriptDomain),
		[]byte(sessionID),
		uint32Bytes(threshold),
		lengthPrefixed(names...),
		lengthPrefixed(round1...),
	))
	return sum[:]
}

// shareLabel is the context a sealed share is valid in.
type shareLabel struct {
	sessionID     string
	transcript    []byte
	senderIndex   uint32
	receiverIndex uint32
	sender        *ecdsa.PublicKey // sender's node key
	receiver      *ecdsa.PublicKey // receiver's node key
	lockKey       *ecdsa.PublicKey // the key the receiver announced for this sender
}

func (l shareLabel) bytes() []byte {
	return lengthPrefixed(
		[]byte(sealedShareDomain),
		[]byte{sealedShareVersion},
		[]byte(l.sessionID),
		l.transcript,
		uint32Bytes(l.senderIndex),
		uint32Bytes(l.receiverIndex),
		ethcrypto.CompressPubkey(l.sender),
		ethcrypto.CompressPubkey(l.receiver),
		ethcrypto.CompressPubkey(l.lockKey),
	)
}

func (l shareLabel) validate() error {
	if l.sessionID == "" || len(l.transcript) != sha256.Size {
		return errors.New("sealed share label has no session id or transcript")
	}
	if l.senderIndex == 0 || l.receiverIndex == 0 || l.senderIndex == l.receiverIndex {
		return errors.New("sealed share label has invalid participant indexes")
	}
	if l.sender == nil || l.receiver == nil || l.lockKey == nil {
		return errors.New("sealed share label is missing a key")
	}
	return nil
}

func (l shareLabel) digest(locked []byte) []byte {
	sum := sha256.Sum256(lengthPrefixed(l.bytes(), locked))
	return sum[:]
}

// sealShare locks plaintext to label.lockKey and signs it with the sender's
// node key. The result is version || signature || locked bytes.
func sealShare(nodeKey *ecdsa.PrivateKey, label shareLabel, plaintext []byte) ([]byte, error) {
	if err := label.validate(); err != nil {
		return nil, err
	}
	if !label.sender.Equal(&nodeKey.PublicKey) {
		return nil, errors.New("sealed share sender is not this node")
	}
	if len(plaintext) == 0 {
		return nil, errors.New("sealed share plaintext is empty")
	}
	// The label is the ECIES "shared info 2": it is covered by the lock's own
	// integrity tag, so the locked bytes themselves name their sender.
	locked, err := ecies.Encrypt(rand.Reader, ecies.ImportECDSAPublic(label.lockKey), plaintext, nil, label.bytes())
	if err != nil {
		return nil, fmt.Errorf("lock dkg share: %w", err)
	}
	sig, err := signWithNodeKey(nodeKey, label.digest(locked))
	if err != nil {
		return nil, fmt.Errorf("sign dkg share: %w", err)
	}
	out := make([]byte, 0, 1+nodeSigLen+len(locked))
	out = append(out, sealedShareVersion)
	out = append(out, sig...)
	return append(out, locked...), nil
}

// verifySealedShare checks the framing and the sender's signature for label and
// returns the locked bytes. Every keygen member can run it.
func verifySealedShare(label shareLabel, sealed []byte) ([]byte, error) {
	if err := label.validate(); err != nil {
		return nil, err
	}
	if len(sealed) <= 1+nodeSigLen {
		return nil, fmt.Errorf("%w: too short", errSealedShare)
	}
	if sealed[0] != sealedShareVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", errSealedShare, sealed[0])
	}
	sig, locked := sealed[1:1+nodeSigLen], sealed[1+nodeSigLen:]
	if !verifyNodeSig(label.sender, label.digest(locked), sig) {
		return nil, fmt.Errorf("%w: signature does not match sender and label", errSealedShare)
	}
	return locked, nil
}

// checkLockedShare refuses locked bytes the ECIES library must not be handed.
// The library slices the IV off without a length check, so a correctly tagged
// short lock would crash it; and it does not check that the one-time key is a
// canonical point on the curve before multiplying it with the private key.
func checkLockedShare(locked []byte) error {
	if len(locked) < lockedShareMinLen {
		return fmt.Errorf("%w: locked share is too short", errSealedShare)
	}
	point, err := ethcrypto.UnmarshalPubkey(locked[:lockedSharePointLen])
	if err != nil {
		return fmt.Errorf("%w: one-time key is not a curve point", errSealedShare)
	}
	// The on-curve check works modulo p, so a coordinate encoded as its value
	// plus p still parses and comes back unreduced. Only the canonical
	// encoding is accepted.
	p := ethcrypto.S256().Params().P
	if point.X.Cmp(p) >= 0 || point.Y.Cmp(p) >= 0 {
		return fmt.Errorf("%w: one-time key is not canonically encoded", errSealedShare)
	}
	return nil
}

// unlockShare opens locked bytes with a lock key's private half.
func unlockShare(label shareLabel, lockPriv, locked []byte) ([]byte, error) {
	if err := checkLockedShare(locked); err != nil {
		return nil, err
	}
	key, err := ethcrypto.ToECDSA(lockPriv)
	if err != nil {
		return nil, errors.New("lock key does not match the announced key")
	}
	defer wipeScalar(key.D)
	if !key.PublicKey.Equal(label.lockKey) {
		return nil, errors.New("lock key does not match the announced key")
	}
	// ImportECDSA shares the scalar with key, so the wipe above covers it.
	plaintext, err := ecies.ImportECDSA(key).Decrypt(locked, nil, label.bytes())
	if err != nil {
		return nil, fmt.Errorf("%w: lock does not open", errSealedShare)
	}
	return plaintext, nil
}

// openSealedShare is the receiver's path: verify the sender's signature, then
// open with the lock key announced for that sender. The caller wipes the
// returned plaintext.
func openSealedShare(label shareLabel, key *lockKey, sealed []byte) ([]byte, error) {
	locked, err := verifySealedShare(label, sealed)
	if err != nil {
		return nil, err
	}
	return unlockShare(label, key.priv, locked)
}

// judgeSealedShare is run by every keygen member on a disputed share, with the
// sealed bytes that member itself received in the broadcast and the lock key
// the receiver revealed. It needs no private key of its own.
//
//   - ShareDisputeSenderAtFault: the sealed share is not validly signed (this
//     needs no revealed key and is decided before the key is looked at), or its
//     lock does not open with the genuine lock key.
//   - ShareDisputeReceiverAtFault: the revealed key is not the lock key the
//     receiver announced.
//   - ShareDisputeShareOpened: the lock opened; share is its content, which the
//     caller must check against the sender's round-1 package. A share that
//     passes puts the receiver at fault, one that fails the sender.
func judgeSealedShare(label shareLabel, sealed, revealedLockKey []byte) (ShareDisputeVerdict, []byte, error) {
	if err := label.validate(); err != nil {
		return 0, nil, err
	}
	locked, err := verifySealedShare(label, sealed)
	if err != nil {
		return ShareDisputeSenderAtFault, nil, nil //nolint:nilerr // an invalid sealed share is a verdict, not a failure to judge
	}
	key, err := ethcrypto.ToECDSA(revealedLockKey)
	if err != nil {
		return ShareDisputeReceiverAtFault, nil, nil //nolint:nilerr // as above
	}
	matches := key.PublicKey.Equal(label.lockKey)
	wipeScalar(key.D)
	if !matches {
		return ShareDisputeReceiverAtFault, nil, nil
	}
	share, err := unlockShare(label, revealedLockKey, locked)
	if err != nil {
		return ShareDisputeSenderAtFault, nil, nil //nolint:nilerr // as above
	}
	return ShareDisputeShareOpened, share, nil
}

// nodePublicKey parses a THORChain node pubkey (bech32) into its secp256k1
// public key.
func nodePublicKey(pubKey string) (*ecdsa.PublicKey, error) {
	pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeAccPub, pubKey)
	if err != nil {
		return nil, fmt.Errorf("invalid node pubkey %q: %w", pubKey, err)
	}
	key, err := ethcrypto.DecompressPubkey(pk.Bytes())
	if err != nil {
		return nil, fmt.Errorf("node pubkey %q is not a secp256k1 key: %w", pubKey, err)
	}
	return key, nil
}
