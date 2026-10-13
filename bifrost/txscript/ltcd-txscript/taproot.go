package txscript

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/ltcsuite/ltcd/chaincfg/chainhash"
	"github.com/ltcsuite/ltcd/wire"
)

// This file adds BIP-341 key-path spending support for Litecoin Taproot
// (witness version 1) outputs: the tweak from an internal key to an output
// key, the BIP-341 signature hash, the key-path witness, and BIP-340
// verification of that witness.
//
// Only key-path spends with no script tree and no annex are supported. That is
// the only form a THORChain vault uses: the vault's FROST group key is the
// internal key, and the output key is the group key tweaked with
// H_TapTweak(x(P)), as required by BIP-341 for outputs without a script path.
//
// The script engine in this package predates Taproot and cannot verify a
// witness v1 spend, so callers verify key-path signatures with
// VerifyTaprootKeyPathSignature instead of executing the engine.

const (
	// SigHashDefault is the BIP-341 default signature hash type. It commits to
	// the same data as SigHashAll but is encoded as 0x00 and yields a 64-byte
	// witness signature with no hash type byte appended.
	SigHashDefault SigHashType = 0x00

	// SchnorrSignatureSize is the size in bytes of a BIP-340 signature.
	SchnorrSignatureSize = 64

	// TaprootOutputKeySize is the size in bytes of an x-only output key, which
	// is also the size of a witness v1 program.
	TaprootOutputKeySize = 32

	tagTapSighash = "TapSighash"
	tagTapTweak   = "TapTweak"

	// taprootSigHashOutputMask selects the output commitment mode from a hash
	// type per BIP-341 (hash_type & 3).
	taprootSigHashOutputMask SigHashType = 0x03
)

// taggedHash implements the BIP-340 tagged hash:
// SHA256(SHA256(tag) || SHA256(tag) || msg...).
func taggedHash(tag string, msgs ...[]byte) [sha256.Size]byte {
	tagHash := sha256.Sum256([]byte(tag))
	h := sha256.New()
	h.Write(tagHash[:])
	h.Write(tagHash[:])
	for _, msg := range msgs {
		h.Write(msg)
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// TaprootTweak returns t = H_TapTweak(x(P)) for an internal key P with no
// script tree.
func TaprootTweak(internalKeyXOnly []byte) ([sha256.Size]byte, error) {
	if len(internalKeyXOnly) != TaprootOutputKeySize {
		return [sha256.Size]byte{}, fmt.Errorf("internal key must be %d bytes, got %d", TaprootOutputKeySize, len(internalKeyXOnly))
	}
	return taggedHash(tagTapTweak, internalKeyXOnly), nil
}

// ComputeTaprootOutputKeyFromInternalKey returns the BIP-341 output key
// Q = P + t*G for an INTERNAL key P and no script tree, where
// t = H_TapTweak(x(P)). The internal key is first lifted to its even-y form as
// BIP-340 requires.
//
// WARNING: never call this with a key that is already an output key. The
// go-frost DKG result (TaprootOutputKeyXOnly, and the x-only key returned by
// FinishTaprootKeygenAndFree) is ALREADY tweaked: it is the output key and must
// be used directly as the witness program and address. Tweaking it again
// derives an address whose private key nobody holds, so funds sent there are
// unspendable. Vault address derivation and vault signing must not use this
// function; it exists for internal-key flows such as tests with local keys.
func ComputeTaprootOutputKeyFromInternalKey(internalKey *btcec.PublicKey) (*btcec.PublicKey, error) {
	if internalKey == nil {
		return nil, errors.New("nil internal key")
	}
	xOnly := schnorr.SerializePubKey(internalKey)
	liftedKey, err := schnorr.ParsePubKey(xOnly)
	if err != nil {
		return nil, fmt.Errorf("lift internal key: %w", err)
	}
	tweak, err := TaprootTweak(xOnly)
	if err != nil {
		return nil, err
	}

	var t btcec.ModNScalar
	if overflow := t.SetByteSlice(tweak[:]); overflow {
		// BIP-341: fail if t >= n. Probability ~2^-128; never happens for an
		// honestly generated key, but it must not be silently reduced.
		return nil, errors.New("taproot tweak is not less than the curve order")
	}

	var p, tG, q btcec.JacobianPoint
	liftedKey.AsJacobian(&p)
	btcec.ScalarBaseMultNonConst(&t, &tG)
	btcec.AddNonConst(&p, &tG, &q)
	if q.Z.IsZero() {
		return nil, errors.New("taproot output key is the point at infinity")
	}
	q.ToAffine()
	return btcec.NewPublicKey(&q.X, &q.Y), nil
}

// ComputeTaprootOutputKeyXOnlyFromInternalKey is
// ComputeTaprootOutputKeyFromInternalKey returning the 32-byte x-only output
// key, which is the witness v1 program. The same warning applies: the input
// must be an internal key, never an already-tweaked output key.
func ComputeTaprootOutputKeyXOnlyFromInternalKey(internalKey *btcec.PublicKey) ([]byte, error) {
	outputKey, err := ComputeTaprootOutputKeyFromInternalKey(internalKey)
	if err != nil {
		return nil, err
	}
	return schnorr.SerializePubKey(outputKey), nil
}

// PayToTaprootScript returns the witness v1 output script
// OP_1 <32-byte output key> for an x-only output key.
func PayToTaprootScript(outputKeyXOnly []byte) ([]byte, error) {
	if len(outputKeyXOnly) != TaprootOutputKeySize {
		return nil, fmt.Errorf("taproot output key must be %d bytes, got %d", TaprootOutputKeySize, len(outputKeyXOnly))
	}
	return payToWitnessTaprootScript(outputKeyXOnly)
}

// PrevOutputFetcher resolves the output spent by a transaction input. BIP-341
// signature hashes commit to the amount and script of every input's previous
// output, so a fetcher must know all of them.
type PrevOutputFetcher interface {
	// FetchPrevOutput returns the output spent by op, or nil if unknown.
	FetchPrevOutput(op wire.OutPoint) *wire.TxOut
}

// MultiPrevOutFetcher is a PrevOutputFetcher backed by a map. Its zero value
// is ready to use. It is not safe for concurrent use.
type MultiPrevOutFetcher struct {
	prevOuts map[wire.OutPoint]*wire.TxOut
}

// NewMultiPrevOutFetcher returns a fetcher seeded with prevOuts (may be nil).
func NewMultiPrevOutFetcher(prevOuts map[wire.OutPoint]*wire.TxOut) *MultiPrevOutFetcher {
	m := &MultiPrevOutFetcher{prevOuts: make(map[wire.OutPoint]*wire.TxOut, len(prevOuts))}
	for op, txOut := range prevOuts {
		m.AddPrevOut(op, txOut)
	}
	return m
}

// AddPrevOut records the output spent by op.
func (m *MultiPrevOutFetcher) AddPrevOut(op wire.OutPoint, txOut *wire.TxOut) {
	if m.prevOuts == nil {
		m.prevOuts = make(map[wire.OutPoint]*wire.TxOut)
	}
	m.prevOuts[op] = txOut
}

// FetchPrevOutput implements PrevOutputFetcher.
func (m *MultiPrevOutFetcher) FetchPrevOutput(op wire.OutPoint) *wire.TxOut {
	return m.prevOuts[op]
}

// TaprootSigHashes holds the BIP-341 intermediate hashes shared by every input
// of a transaction. Unlike the BIP-143 values in TxSigHashes these are single
// SHA256 digests.
type TaprootSigHashes struct {
	HashPrevOutsV1     chainhash.Hash
	HashInputAmountsV1 chainhash.Hash
	HashInputScriptsV1 chainhash.Hash
	HashSequenceV1     chainhash.Hash
	HashOutputsV1      chainhash.Hash
}

// NewTaprootSigHashes computes the BIP-341 intermediate hashes for tx. It
// fails if prevOuts cannot resolve every input.
func NewTaprootSigHashes(tx *wire.MsgTx, prevOuts PrevOutputFetcher) (*TaprootSigHashes, error) {
	if tx == nil {
		return nil, errors.New("nil transaction")
	}
	if prevOuts == nil {
		return nil, errors.New("nil previous output fetcher")
	}

	var prevOutsBuf, amountsBuf, scriptsBuf, sequencesBuf, outputsBuf bytes.Buffer
	for i, txIn := range tx.TxIn {
		prevOut := prevOuts.FetchPrevOutput(txIn.PreviousOutPoint)
		if prevOut == nil {
			return nil, fmt.Errorf("missing previous output for input %d (%s)", i, txIn.PreviousOutPoint)
		}
		writeOutPointV1(&prevOutsBuf, txIn.PreviousOutPoint)
		writeInt64LE(&amountsBuf, prevOut.Value)
		if err := wire.WriteVarBytes(&scriptsBuf, 0, prevOut.PkScript); err != nil {
			return nil, fmt.Errorf("serialize previous output script for input %d: %w", i, err)
		}
		writeUint32LE(&sequencesBuf, txIn.Sequence)
	}
	for i, txOut := range tx.TxOut {
		if err := wire.WriteTxOut(&outputsBuf, 0, 0, txOut); err != nil {
			return nil, fmt.Errorf("serialize output %d: %w", i, err)
		}
	}

	return &TaprootSigHashes{
		HashPrevOutsV1:     chainhash.Hash(sha256.Sum256(prevOutsBuf.Bytes())),
		HashInputAmountsV1: chainhash.Hash(sha256.Sum256(amountsBuf.Bytes())),
		HashInputScriptsV1: chainhash.Hash(sha256.Sum256(scriptsBuf.Bytes())),
		HashSequenceV1:     chainhash.Hash(sha256.Sum256(sequencesBuf.Bytes())),
		HashOutputsV1:      chainhash.Hash(sha256.Sum256(outputsBuf.Bytes())),
	}, nil
}

// validateTaprootSigHashType rejects every hash type BIP-341 does not allow:
// only 0x00, 0x01, 0x02, 0x03, 0x81, 0x82 and 0x83 are valid.
func validateTaprootSigHashType(hashType SigHashType) error {
	base := hashType & ^SigHashAnyOneCanPay
	switch base {
	case SigHashDefault:
		if hashType&SigHashAnyOneCanPay != 0 {
			return fmt.Errorf("invalid taproot sighash type 0x%02x: ANYONECANPAY requires ALL, NONE or SINGLE", byte(hashType))
		}
	case SigHashAll, SigHashNone, SigHashSingle:
	default:
		return fmt.Errorf("invalid taproot sighash type 0x%02x", byte(hashType))
	}
	return nil
}

// taprootSigMsg builds the BIP-341 signature hash preimage for a key-path spend
// of input idx with no annex: the epoch byte 0x00 followed by
// SigMsg(hash_type, ext_flag=0).
func taprootSigMsg(sigHashes *TaprootSigHashes, hashType SigHashType, tx *wire.MsgTx, idx int, prevOuts PrevOutputFetcher) ([]byte, error) {
	if sigHashes == nil {
		return nil, errors.New("nil taproot sighashes")
	}
	if tx == nil {
		return nil, errors.New("nil transaction")
	}
	if prevOuts == nil {
		return nil, errors.New("nil previous output fetcher")
	}
	if idx < 0 || idx >= len(tx.TxIn) {
		return nil, fmt.Errorf("input index %d out of range for %d inputs", idx, len(tx.TxIn))
	}
	if err := validateTaprootSigHashType(hashType); err != nil {
		return nil, err
	}
	anyoneCanPay := hashType&SigHashAnyOneCanPay != 0
	outputMode := hashType & taprootSigHashOutputMask

	var msg bytes.Buffer
	msg.WriteByte(0x00) // epoch
	msg.WriteByte(byte(hashType))
	writeUint32LE(&msg, uint32(tx.Version))
	writeUint32LE(&msg, tx.LockTime)
	if !anyoneCanPay {
		msg.Write(sigHashes.HashPrevOutsV1[:])
		msg.Write(sigHashes.HashInputAmountsV1[:])
		msg.Write(sigHashes.HashInputScriptsV1[:])
		msg.Write(sigHashes.HashSequenceV1[:])
	}
	if outputMode != SigHashNone && outputMode != SigHashSingle {
		msg.Write(sigHashes.HashOutputsV1[:])
	}

	// spend_type = 2*ext_flag + annex_present; key path without annex is 0.
	msg.WriteByte(0x00)

	txIn := tx.TxIn[idx]
	if anyoneCanPay {
		prevOut := prevOuts.FetchPrevOutput(txIn.PreviousOutPoint)
		if prevOut == nil {
			return nil, fmt.Errorf("missing previous output for input %d (%s)", idx, txIn.PreviousOutPoint)
		}
		writeOutPointV1(&msg, txIn.PreviousOutPoint)
		writeInt64LE(&msg, prevOut.Value)
		if err := wire.WriteVarBytes(&msg, 0, prevOut.PkScript); err != nil {
			return nil, fmt.Errorf("serialize previous output script: %w", err)
		}
		writeUint32LE(&msg, txIn.Sequence)
	} else {
		writeUint32LE(&msg, uint32(idx))
	}

	if outputMode == SigHashSingle {
		// BIP-341: SIGHASH_SINGLE with no matching output is invalid, unlike the
		// legacy "hash of 1" behaviour.
		if idx >= len(tx.TxOut) {
			return nil, fmt.Errorf("sighash single: input index %d has no matching output (%d outputs)", idx, len(tx.TxOut))
		}
		var out bytes.Buffer
		if err := wire.WriteTxOut(&out, 0, 0, tx.TxOut[idx]); err != nil {
			return nil, fmt.Errorf("serialize output %d: %w", idx, err)
		}
		single := sha256.Sum256(out.Bytes())
		msg.Write(single[:])
	}
	return msg.Bytes(), nil
}

// CalcTaprootSignatureHash returns the BIP-341 signature hash for a key-path
// spend (no script tree, no annex) of input idx of tx under hashType.
func CalcTaprootSignatureHash(sigHashes *TaprootSigHashes, hashType SigHashType, tx *wire.MsgTx, idx int, prevOuts PrevOutputFetcher) ([]byte, error) {
	msg, err := taprootSigMsg(sigHashes, hashType, tx, idx, prevOuts)
	if err != nil {
		return nil, err
	}
	digest := taggedHash(tagTapSighash, msg)
	return digest[:], nil
}

// TaprootKeyPathWitness returns the witness stack for a key-path spend: the
// 64-byte BIP-340 signature alone for SigHashDefault, otherwise the signature
// with the hash type byte appended.
func TaprootKeyPathWitness(sig []byte, hashType SigHashType) (wire.TxWitness, error) {
	if len(sig) != SchnorrSignatureSize {
		return nil, fmt.Errorf("schnorr signature must be %d bytes, got %d", SchnorrSignatureSize, len(sig))
	}
	if err := validateTaprootSigHashType(hashType); err != nil {
		return nil, err
	}
	if hashType == SigHashDefault {
		return wire.TxWitness{append([]byte(nil), sig...)}, nil
	}
	item := make([]byte, 0, SchnorrSignatureSize+1)
	item = append(item, sig...)
	item = append(item, byte(hashType))
	return wire.TxWitness{item}, nil
}

// ParseTaprootKeyPathWitness splits a key-path witness into its signature and
// hash type. A 64-byte item means SigHashDefault; a 65-byte item carries an
// explicit non-zero hash type, as BIP-341 requires.
func ParseTaprootKeyPathWitness(witness wire.TxWitness) ([]byte, SigHashType, error) {
	if len(witness) != 1 {
		return nil, 0, fmt.Errorf("key-path witness must have exactly one item, got %d", len(witness))
	}
	item := witness[0]
	switch len(item) {
	case SchnorrSignatureSize:
		return append([]byte(nil), item...), SigHashDefault, nil
	case SchnorrSignatureSize + 1:
		hashType := SigHashType(item[SchnorrSignatureSize])
		if hashType == SigHashDefault {
			return nil, 0, errors.New("65-byte key-path witness must not use hash type 0x00")
		}
		if err := validateTaprootSigHashType(hashType); err != nil {
			return nil, 0, err
		}
		return append([]byte(nil), item[:SchnorrSignatureSize]...), hashType, nil
	default:
		return nil, 0, fmt.Errorf("key-path witness item must be %d or %d bytes, got %d", SchnorrSignatureSize, SchnorrSignatureSize+1, len(item))
	}
}

// VerifyTaprootKeyPathSignature verifies a 64-byte BIP-340 signature over a
// 32-byte BIP-341 digest against the x-only output key of the spent output.
func VerifyTaprootKeyPathSignature(outputKeyXOnly, digest, sig []byte) error {
	if len(outputKeyXOnly) != TaprootOutputKeySize {
		return fmt.Errorf("taproot output key must be %d bytes, got %d", TaprootOutputKeySize, len(outputKeyXOnly))
	}
	if len(digest) != sha256.Size {
		return fmt.Errorf("taproot digest must be %d bytes, got %d", sha256.Size, len(digest))
	}
	if len(sig) != SchnorrSignatureSize {
		return fmt.Errorf("schnorr signature must be %d bytes, got %d", SchnorrSignatureSize, len(sig))
	}
	pubKey, err := schnorr.ParsePubKey(outputKeyXOnly)
	if err != nil {
		return fmt.Errorf("parse taproot output key: %w", err)
	}
	signature, err := schnorr.ParseSignature(sig)
	if err != nil {
		return fmt.Errorf("parse schnorr signature: %w", err)
	}
	if !signature.Verify(digest, pubKey) {
		return errors.New("schnorr signature does not verify against the taproot output key")
	}
	return nil
}

// SchnorrSignable produces BIP-340 signatures for one Taproot output key. The
// threshold signer implements it over the vault's FROST group key; tests use a
// local tweaked private key.
type SchnorrSignable interface {
	// TaprootOutputKey returns the 32-byte x-only output key (already tweaked)
	// that the signatures verify against.
	TaprootOutputKey() []byte
	// SignSchnorr returns a 64-byte BIP-340 signature over the 32-byte digest.
	SignSchnorr(digest []byte) ([]byte, error)
}

// TaprootKeyPathSignature computes the BIP-341 digest for input idx, signs it
// with signable, verifies the signature against signable's output key, and
// returns the witness stack to attach to the input.
//
// Before signing it checks that the output being spent pays to signable's
// output key, so a signer for the wrong vault fails here instead of at
// broadcast, after a threshold signing round has already been spent.
func TaprootKeyPathSignature(tx *wire.MsgTx, sigHashes *TaprootSigHashes, idx int, hashType SigHashType, prevOuts PrevOutputFetcher, signable SchnorrSignable) (wire.TxWitness, error) {
	if signable == nil {
		return nil, errors.New("nil schnorr signable")
	}
	if tx == nil {
		return nil, errors.New("nil transaction")
	}
	if prevOuts == nil {
		return nil, errors.New("nil previous output fetcher")
	}
	if idx < 0 || idx >= len(tx.TxIn) {
		return nil, fmt.Errorf("input index %d out of range for %d inputs", idx, len(tx.TxIn))
	}
	expectedScript, err := PayToTaprootScript(signable.TaprootOutputKey())
	if err != nil {
		return nil, fmt.Errorf("signer output key: %w", err)
	}
	prevOut := prevOuts.FetchPrevOutput(tx.TxIn[idx].PreviousOutPoint)
	if prevOut == nil {
		return nil, fmt.Errorf("missing previous output for input %d (%s)", idx, tx.TxIn[idx].PreviousOutPoint)
	}
	if !bytes.Equal(prevOut.PkScript, expectedScript) {
		return nil, fmt.Errorf("input %d spends script %x, which does not pay to the signer's taproot output key %x", idx, prevOut.PkScript, signable.TaprootOutputKey())
	}
	digest, err := CalcTaprootSignatureHash(sigHashes, hashType, tx, idx, prevOuts)
	if err != nil {
		return nil, err
	}
	sig, err := signable.SignSchnorr(digest)
	if err != nil {
		return nil, fmt.Errorf("sign taproot input %d: %w", idx, err)
	}
	if err := VerifyTaprootKeyPathSignature(signable.TaprootOutputKey(), digest, sig); err != nil {
		return nil, fmt.Errorf("verify taproot signature for input %d: %w", idx, err)
	}
	return TaprootKeyPathWitness(sig, hashType)
}

func writeOutPointV1(w *bytes.Buffer, op wire.OutPoint) {
	w.Write(op.Hash[:])
	writeUint32LE(w, op.Index)
}

func writeUint32LE(w *bytes.Buffer, v uint32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	w.Write(buf[:])
}

func writeInt64LE(w *bytes.Buffer, v int64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(v))
	w.Write(buf[:])
}
