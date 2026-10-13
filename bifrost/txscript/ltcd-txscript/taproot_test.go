package txscript

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/ltcsuite/ltcd/chaincfg"
	"github.com/ltcsuite/ltcd/chaincfg/chainhash"
	"github.com/ltcsuite/ltcd/wire"
)

// bip341Vectors mirrors the subset of bip-0341/wallet-test-vectors.json that
// key-path spending needs. See data/README-bip341-vectors.md for provenance.
type bip341Vectors struct {
	Version      int `json:"version"`
	ScriptPubKey []struct {
		Given struct {
			InternalPubkey string          `json:"internalPubkey"`
			ScriptTree     json.RawMessage `json:"scriptTree"`
		} `json:"given"`
		Intermediary struct {
			Tweak         string `json:"tweak"`
			TweakedPubkey string `json:"tweakedPubkey"`
		} `json:"intermediary"`
		Expected struct {
			ScriptPubKey  string `json:"scriptPubKey"`
			Bip350Address string `json:"bip350Address"`
		} `json:"expected"`
	} `json:"scriptPubKey"`
	KeyPathSpending []struct {
		Given struct {
			RawUnsignedTx string `json:"rawUnsignedTx"`
			UtxosSpent    []struct {
				ScriptPubKey string `json:"scriptPubKey"`
				AmountSats   int64  `json:"amountSats"`
			} `json:"utxosSpent"`
		} `json:"given"`
		Intermediary struct {
			HashAmounts       string `json:"hashAmounts"`
			HashOutputs       string `json:"hashOutputs"`
			HashPrevouts      string `json:"hashPrevouts"`
			HashScriptPubkeys string `json:"hashScriptPubkeys"`
			HashSequences     string `json:"hashSequences"`
		} `json:"intermediary"`
		InputSpending []struct {
			Given struct {
				TxinIndex       int     `json:"txinIndex"`
				InternalPrivkey string  `json:"internalPrivkey"`
				MerkleRoot      *string `json:"merkleRoot"`
				HashType        int     `json:"hashType"`
			} `json:"given"`
			Intermediary struct {
				InternalPubkey string `json:"internalPubkey"`
				Tweak          string `json:"tweak"`
				TweakedPrivkey string `json:"tweakedPrivkey"`
				SigMsg         string `json:"sigMsg"`
				SigHash        string `json:"sigHash"`
			} `json:"intermediary"`
			Expected struct {
				Witness []string `json:"witness"`
			} `json:"expected"`
		} `json:"inputSpending"`
	} `json:"keyPathSpending"`
}

func loadBIP341Vectors(t *testing.T) *bip341Vectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("data", "bip341-wallet-test-vectors.json"))
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v bip341Vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	if v.Version != 1 {
		t.Fatalf("unexpected vector version %d", v.Version)
	}
	return &v
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode hex %q: %v", s, err)
	}
	return b
}

func hashHex(h chainhash.Hash) string {
	// The vectors list digests in natural byte order; chainhash.Hash.String
	// reverses, so encode the raw bytes.
	return hex.EncodeToString(h[:])
}

// tweakTaprootPrivKey returns the private key for the BIP-341 output key of
// priv's public key with no script tree: d' = (d or n-d, whichever yields an
// even-y internal key) + H_TapTweak(x(P)).
func tweakTaprootPrivKey(priv *btcec.PrivateKey) (*btcec.PrivateKey, error) {
	pub := priv.PubKey()
	d := priv.Key
	if pub.SerializeCompressed()[0] == 0x03 {
		d.Negate()
	}
	tweak, err := TaprootTweak(schnorr.SerializePubKey(pub))
	if err != nil {
		return nil, err
	}
	var t btcec.ModNScalar
	if overflow := t.SetByteSlice(tweak[:]); overflow {
		return nil, errors.New("tweak overflows curve order")
	}
	d.Add(&t)
	if d.IsZero() {
		return nil, errors.New("tweaked private key is zero")
	}
	return btcec.PrivKeyFromScalar(&d), nil
}

// privKeySchnorrSignable is a SchnorrSignable over a local internal key,
// used only in tests. Production signing goes through the threshold signer.
type privKeySchnorrSignable struct {
	tweaked   *btcec.PrivateKey
	outputKey []byte
}

func newPrivKeySchnorrSignable(t *testing.T, internal *btcec.PrivateKey) *privKeySchnorrSignable {
	t.Helper()
	tweaked, err := tweakTaprootPrivKey(internal)
	if err != nil {
		t.Fatalf("tweak private key: %v", err)
	}
	return &privKeySchnorrSignable{
		tweaked:   tweaked,
		outputKey: schnorr.SerializePubKey(tweaked.PubKey()),
	}
}

func (s *privKeySchnorrSignable) TaprootOutputKey() []byte { return s.outputKey }

func (s *privKeySchnorrSignable) SignSchnorr(digest []byte) ([]byte, error) {
	sig, err := schnorr.Sign(s.tweaked, digest)
	if err != nil {
		return nil, err
	}
	return sig.Serialize(), nil
}

func TestTaprootTweakVectors(t *testing.T) {
	vectors := loadBIP341Vectors(t)
	ran := 0
	for i, v := range vectors.ScriptPubKey {
		if string(v.Given.ScriptTree) != "null" {
			continue // script-path vectors are out of scope for key-path only support
		}
		ran++
		internalXOnly := mustHex(t, v.Given.InternalPubkey)
		internal, err := schnorr.ParsePubKey(internalXOnly)
		if err != nil {
			t.Fatalf("vector %d: parse internal key: %v", i, err)
		}

		tweak, err := TaprootTweak(internalXOnly)
		if err != nil {
			t.Fatalf("vector %d: tweak: %v", i, err)
		}
		if got := hex.EncodeToString(tweak[:]); got != v.Intermediary.Tweak {
			t.Fatalf("vector %d: tweak = %s, want %s", i, got, v.Intermediary.Tweak)
		}

		outputKey, err := ComputeTaprootOutputKeyXOnlyFromInternalKey(internal)
		if err != nil {
			t.Fatalf("vector %d: output key: %v", i, err)
		}
		if got := hex.EncodeToString(outputKey); got != v.Intermediary.TweakedPubkey {
			t.Fatalf("vector %d: output key = %s, want %s", i, got, v.Intermediary.TweakedPubkey)
		}

		script, err := PayToTaprootScript(outputKey)
		if err != nil {
			t.Fatalf("vector %d: script: %v", i, err)
		}
		if got := hex.EncodeToString(script); got != v.Expected.ScriptPubKey {
			t.Fatalf("vector %d: scriptPubKey = %s, want %s", i, got, v.Expected.ScriptPubKey)
		}
		if class := GetScriptClass(script); class != WitnessV1TaprootTy {
			t.Fatalf("vector %d: script class = %s, want %s", i, class, WitnessV1TaprootTy)
		}

		// Same encoder as NewAddressTaproot, with Bitcoin's HRP to match the vector.
		addr, err := newAddressTaproot("bc", outputKey)
		if err != nil {
			t.Fatalf("vector %d: address: %v", i, err)
		}
		if got := addr.EncodeAddress(); got != v.Expected.Bip350Address {
			t.Fatalf("vector %d: address = %s, want %s", i, got, v.Expected.Bip350Address)
		}
	}
	if ran == 0 {
		t.Fatal("no key-only scriptPubKey vectors found")
	}
}

func TestTaprootSigHashVectors(t *testing.T) {
	vectors := loadBIP341Vectors(t)
	if len(vectors.KeyPathSpending) == 0 {
		t.Fatal("no keyPathSpending vectors")
	}
	keyOnlyInputs := 0
	for vi, v := range vectors.KeyPathSpending {
		var tx wire.MsgTx
		if err := tx.Deserialize(bytes.NewReader(mustHex(t, v.Given.RawUnsignedTx))); err != nil {
			t.Fatalf("vector %d: deserialize tx: %v", vi, err)
		}
		if len(tx.TxIn) != len(v.Given.UtxosSpent) {
			t.Fatalf("vector %d: %d inputs but %d utxos", vi, len(tx.TxIn), len(v.Given.UtxosSpent))
		}
		prevOuts := NewMultiPrevOutFetcher(nil)
		for i, utxo := range v.Given.UtxosSpent {
			prevOuts.AddPrevOut(tx.TxIn[i].PreviousOutPoint, wire.NewTxOut(utxo.AmountSats, mustHex(t, utxo.ScriptPubKey)))
		}

		sigHashes, err := NewTaprootSigHashes(&tx, prevOuts)
		if err != nil {
			t.Fatalf("vector %d: sighashes: %v", vi, err)
		}
		for name, pair := range map[string][2]string{
			"hashPrevouts":      {hashHex(sigHashes.HashPrevOutsV1), v.Intermediary.HashPrevouts},
			"hashAmounts":       {hashHex(sigHashes.HashInputAmountsV1), v.Intermediary.HashAmounts},
			"hashScriptPubkeys": {hashHex(sigHashes.HashInputScriptsV1), v.Intermediary.HashScriptPubkeys},
			"hashSequences":     {hashHex(sigHashes.HashSequenceV1), v.Intermediary.HashSequences},
			"hashOutputs":       {hashHex(sigHashes.HashOutputsV1), v.Intermediary.HashOutputs},
		} {
			if pair[0] != pair[1] {
				t.Fatalf("vector %d: %s = %s, want %s", vi, name, pair[0], pair[1])
			}
		}

		for _, in := range v.InputSpending {
			idx := in.Given.TxinIndex
			hashType := SigHashType(in.Given.HashType)
			utxo := v.Given.UtxosSpent[idx]
			spentScript := mustHex(t, utxo.ScriptPubKey)
			outputKey := spentScript[2:]

			msg, err := taprootSigMsg(sigHashes, hashType, &tx, idx, prevOuts)
			if err != nil {
				t.Fatalf("vector %d input %d: sigmsg: %v", vi, idx, err)
			}
			if got := hex.EncodeToString(msg); got != in.Intermediary.SigMsg {
				t.Fatalf("vector %d input %d (type 0x%02x): sigMsg = %s, want %s", vi, idx, hashType, got, in.Intermediary.SigMsg)
			}
			digest, err := CalcTaprootSignatureHash(sigHashes, hashType, &tx, idx, prevOuts)
			if err != nil {
				t.Fatalf("vector %d input %d: sighash: %v", vi, idx, err)
			}
			if got := hex.EncodeToString(digest); got != in.Intermediary.SigHash {
				t.Fatalf("vector %d input %d (type 0x%02x): sigHash = %s, want %s", vi, idx, hashType, got, in.Intermediary.SigHash)
			}

			// The vector's witness must parse to the same hash type and verify
			// against the spent output key.
			witness := wire.TxWitness{mustHex(t, in.Expected.Witness[0])}
			sig, parsedType, err := ParseTaprootKeyPathWitness(witness)
			if err != nil {
				t.Fatalf("vector %d input %d: parse witness: %v", vi, idx, err)
			}
			if parsedType != hashType {
				t.Fatalf("vector %d input %d: witness hash type 0x%02x, want 0x%02x", vi, idx, parsedType, hashType)
			}
			if err := VerifyTaprootKeyPathSignature(outputKey, digest, sig); err != nil {
				t.Fatalf("vector %d input %d: verify vector signature: %v", vi, idx, err)
			}

			// The remaining checks derive the output key from the internal key with
			// no script tree, so they only apply to inputs whose spent output has
			// no merkle root. Script-tree outputs are out of scope for vaults; the
			// signature hash and vector witness above are still verified for them.
			if in.Given.MerkleRoot != nil {
				continue
			}
			keyOnlyInputs++

			// Tweaking the vector's internal private key must give its tweaked
			// private key, whose public key is the spent output key.
			internalPriv, internalPub := btcec.PrivKeyFromBytes(mustHex(t, in.Given.InternalPrivkey))
			if got := hex.EncodeToString(schnorr.SerializePubKey(internalPub)); got != in.Intermediary.InternalPubkey {
				t.Fatalf("vector %d input %d: internal pubkey = %s, want %s", vi, idx, got, in.Intermediary.InternalPubkey)
			}
			tweaked, err := tweakTaprootPrivKey(internalPriv)
			if err != nil {
				t.Fatalf("vector %d input %d: tweak privkey: %v", vi, idx, err)
			}
			tweakedBytes := tweaked.Key.Bytes()
			if got := hex.EncodeToString(tweakedBytes[:]); got != in.Intermediary.TweakedPrivkey {
				t.Fatalf("vector %d input %d: tweaked privkey = %s, want %s", vi, idx, got, in.Intermediary.TweakedPrivkey)
			}
			if got := schnorr.SerializePubKey(tweaked.PubKey()); !bytes.Equal(got, outputKey) {
				t.Fatalf("vector %d input %d: tweaked pubkey %x, want %x", vi, idx, got, outputKey)
			}
			if got, err := ComputeTaprootOutputKeyXOnlyFromInternalKey(internalPub); err != nil || !bytes.Equal(got, outputKey) {
				t.Fatalf("vector %d input %d: ComputeTaprootOutputKeyXOnlyFromInternalKey = %x (%v), want %x", vi, idx, got, err, outputKey)
			}

			// Signing with our own tweaked key yields a witness of the right shape
			// that verifies, even though BIP-340 aux randomness differs from the vector.
			signable := newPrivKeySchnorrSignable(t, internalPriv)
			ownWitness, err := TaprootKeyPathSignature(&tx, sigHashes, idx, hashType, prevOuts, signable)
			if err != nil {
				t.Fatalf("vector %d input %d: own signature: %v", vi, idx, err)
			}
			ownSig, ownType, err := ParseTaprootKeyPathWitness(ownWitness)
			if err != nil || ownType != hashType {
				t.Fatalf("vector %d input %d: own witness parse: type 0x%02x err %v", vi, idx, ownType, err)
			}
			if err := VerifyTaprootKeyPathSignature(outputKey, digest, ownSig); err != nil {
				t.Fatalf("vector %d input %d: verify own signature: %v", vi, idx, err)
			}
		}
	}
	if keyOnlyInputs == 0 {
		t.Fatal("no key-only inputs exercised the tweak checks")
	}
}

func TestTaprootKeyPathWitness(t *testing.T) {
	sig := bytes.Repeat([]byte{0xab}, SchnorrSignatureSize)

	w, err := TaprootKeyPathWitness(sig, SigHashDefault)
	if err != nil || len(w) != 1 || len(w[0]) != SchnorrSignatureSize {
		t.Fatalf("default witness: %v %v", w, err)
	}
	w[0][0] = 0 // must be a copy
	if sig[0] != 0xab {
		t.Fatal("witness aliased the signature")
	}

	for _, hashType := range []SigHashType{SigHashAll, SigHashNone, SigHashSingle, SigHashAll | SigHashAnyOneCanPay, SigHashNone | SigHashAnyOneCanPay, SigHashSingle | SigHashAnyOneCanPay} {
		w, err := TaprootKeyPathWitness(sig, hashType)
		if err != nil || len(w) != 1 || len(w[0]) != SchnorrSignatureSize+1 || w[0][SchnorrSignatureSize] != byte(hashType) {
			t.Fatalf("witness for 0x%02x: %v %v", hashType, w, err)
		}
		gotSig, gotType, err := ParseTaprootKeyPathWitness(w)
		if err != nil || gotType != hashType || !bytes.Equal(gotSig, sig) {
			t.Fatalf("parse witness for 0x%02x: type 0x%02x err %v", hashType, gotType, err)
		}
	}

	for _, hashType := range []SigHashType{SigHashAnyOneCanPay, 0x04, 0x84, 0x1f, 0xff} {
		if _, err := TaprootKeyPathWitness(sig, hashType); err == nil {
			t.Fatalf("expected error for hash type 0x%02x", hashType)
		}
	}
	if _, err := TaprootKeyPathWitness(sig[:63], SigHashDefault); err == nil {
		t.Fatal("expected error for short signature")
	}

	// Parsing rejects malformed stacks.
	for name, witness := range map[string]wire.TxWitness{
		"two items":          {sig, sig},
		"empty":              {},
		"short":              {sig[:63]},
		"long":               {append(append([]byte(nil), sig...), 0x01, 0x01)},
		"explicit zero type": {append(append([]byte(nil), sig...), 0x00)},
		"invalid type":       {append(append([]byte(nil), sig...), 0x80)},
	} {
		if _, _, err := ParseTaprootKeyPathWitness(witness); err == nil {
			t.Fatalf("expected parse error for %s", name)
		}
	}
}

func TestTaprootSigHashErrors(t *testing.T) {
	internal, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	outputKey, err := ComputeTaprootOutputKeyXOnlyFromInternalKey(internal.PubKey())
	if err != nil {
		t.Fatal(err)
	}
	script, err := PayToTaprootScript(outputKey)
	if err != nil {
		t.Fatal(err)
	}

	tx := wire.NewMsgTx(2)
	op0 := wire.OutPoint{Hash: chainhash.Hash{1}, Index: 0}
	op1 := wire.OutPoint{Hash: chainhash.Hash{2}, Index: 3}
	tx.AddTxIn(wire.NewTxIn(&op0, nil, nil))
	tx.AddTxIn(wire.NewTxIn(&op1, nil, nil))
	tx.AddTxOut(wire.NewTxOut(50_000, script))

	complete := NewMultiPrevOutFetcher(map[wire.OutPoint]*wire.TxOut{
		op0: wire.NewTxOut(40_000, script),
		op1: wire.NewTxOut(20_000, script),
	})
	partial := NewMultiPrevOutFetcher(map[wire.OutPoint]*wire.TxOut{op0: wire.NewTxOut(40_000, script)})

	if _, err := NewTaprootSigHashes(tx, partial); err == nil {
		t.Fatal("expected error for missing previous output")
	}
	if _, err := NewTaprootSigHashes(nil, complete); err == nil {
		t.Fatal("expected error for nil tx")
	}
	if _, err := NewTaprootSigHashes(tx, nil); err == nil {
		t.Fatal("expected error for nil fetcher")
	}

	sigHashes, err := NewTaprootSigHashes(tx, complete)
	if err != nil {
		t.Fatal(err)
	}
	// SIGHASH_SINGLE with no matching output is invalid under BIP-341.
	if _, err := CalcTaprootSignatureHash(sigHashes, SigHashSingle, tx, 1, complete); err == nil {
		t.Fatal("expected error for sighash single without matching output")
	}
	if _, err := CalcTaprootSignatureHash(sigHashes, SigHashSingle, tx, 0, complete); err != nil {
		t.Fatalf("sighash single with matching output: %v", err)
	}
	// ANYONECANPAY needs the spent output for the signed input only.
	if _, err := CalcTaprootSignatureHash(sigHashes, SigHashAll|SigHashAnyOneCanPay, tx, 1, partial); err == nil {
		t.Fatal("expected error for anyonecanpay with missing previous output")
	}
	if _, err := CalcTaprootSignatureHash(sigHashes, SigHashAll|SigHashAnyOneCanPay, tx, 0, partial); err != nil {
		t.Fatalf("anyonecanpay with known previous output: %v", err)
	}
	for _, idx := range []int{-1, 2} {
		if _, err := CalcTaprootSignatureHash(sigHashes, SigHashDefault, tx, idx, complete); err == nil {
			t.Fatalf("expected error for input index %d", idx)
		}
	}
	if _, err := CalcTaprootSignatureHash(sigHashes, SigHashAnyOneCanPay, tx, 0, complete); err == nil {
		t.Fatal("expected error for bare ANYONECANPAY")
	}
	if _, err := CalcTaprootSignatureHash(nil, SigHashDefault, tx, 0, complete); err == nil {
		t.Fatal("expected error for nil sighashes")
	}

	// Each hash type commits to different data, so digests must all differ.
	seen := map[string]SigHashType{}
	for _, hashType := range []SigHashType{SigHashDefault, SigHashAll, SigHashNone, SigHashSingle, SigHashAll | SigHashAnyOneCanPay, SigHashNone | SigHashAnyOneCanPay, SigHashSingle | SigHashAnyOneCanPay} {
		digest, err := CalcTaprootSignatureHash(sigHashes, hashType, tx, 0, complete)
		if err != nil {
			t.Fatalf("hash type 0x%02x: %v", hashType, err)
		}
		if prev, dup := seen[string(digest)]; dup {
			t.Fatalf("hash types 0x%02x and 0x%02x produced the same digest", prev, hashType)
		}
		seen[string(digest)] = hashType
	}

	// The signable path verifies before returning a witness.
	signable := newPrivKeySchnorrSignable(t, internal)
	if !bytes.Equal(signable.TaprootOutputKey(), outputKey) {
		t.Fatal("signable output key does not match ComputeTaprootOutputKeyFromInternalKey")
	}
	for idx := range tx.TxIn {
		witness, err := TaprootKeyPathSignature(tx, sigHashes, idx, SigHashDefault, complete, signable)
		if err != nil {
			t.Fatalf("sign input %d: %v", idx, err)
		}
		digest, _ := CalcTaprootSignatureHash(sigHashes, SigHashDefault, tx, idx, complete)
		if err := VerifyTaprootKeyPathSignature(outputKey, digest, witness[0]); err != nil {
			t.Fatalf("verify input %d: %v", idx, err)
		}
		// A signature for one input must not verify for the other.
		other, _ := CalcTaprootSignatureHash(sigHashes, SigHashDefault, tx, 1-idx, complete)
		if err := VerifyTaprootKeyPathSignature(outputKey, other, witness[0]); err == nil {
			t.Fatalf("signature for input %d verified against input %d", idx, 1-idx)
		}
	}
	// A signer for a different key than the spent output is rejected before
	// any signature is requested, so no threshold round is wasted.
	otherKey, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	wrongVault := &countingSignable{inner: newPrivKeySchnorrSignable(t, otherKey)}
	if _, err := TaprootKeyPathSignature(tx, sigHashes, 0, SigHashDefault, complete, wrongVault); err == nil {
		t.Fatal("expected error for a signer whose key does not own the spent output")
	}
	if wrongVault.calls != 0 {
		t.Fatalf("signer was invoked %d times for a mismatched output", wrongVault.calls)
	}
	// A signer whose claimed output key does not match its real key is rejected
	// after signing, by verification.
	lying := newPrivKeySchnorrSignable(t, internal)
	lying.outputKey = schnorr.SerializePubKey(otherKey.PubKey())
	mismatched := NewMultiPrevOutFetcher(nil)
	otherScript, _ := PayToTaprootScript(lying.outputKey)
	mismatched.AddPrevOut(op0, wire.NewTxOut(40_000, otherScript))
	mismatched.AddPrevOut(op1, wire.NewTxOut(20_000, otherScript))
	if _, err := TaprootKeyPathSignature(tx, sigHashes, 0, SigHashDefault, mismatched, lying); err == nil {
		t.Fatal("expected error for a signature that does not verify against the claimed key")
	}
	if _, err := TaprootKeyPathSignature(tx, sigHashes, 0, SigHashDefault, complete, nil); err == nil {
		t.Fatal("expected error for nil signable")
	}
	if _, err := TaprootKeyPathSignature(tx, sigHashes, 0, SigHashDefault, partial, signable); err != nil {
		t.Fatalf("unexpected error for known previous output: %v", err)
	}
	if _, err := TaprootKeyPathSignature(tx, sigHashes, 1, SigHashDefault, partial, signable); err == nil {
		t.Fatal("expected error for unknown previous output")
	}
}

// countingSignable records how many signatures were requested.
type countingSignable struct {
	inner SchnorrSignable
	calls int
}

func (c *countingSignable) TaprootOutputKey() []byte { return c.inner.TaprootOutputKey() }

func (c *countingSignable) SignSchnorr(digest []byte) ([]byte, error) {
	c.calls++
	return c.inner.SignSchnorr(digest)
}

func TestMultiPrevOutFetcherZeroValue(t *testing.T) {
	var fetcher MultiPrevOutFetcher
	op := wire.OutPoint{Hash: chainhash.Hash{9}, Index: 1}
	if got := fetcher.FetchPrevOutput(op); got != nil {
		t.Fatalf("empty fetcher returned %v", got)
	}
	txOut := wire.NewTxOut(1, []byte{0x51})
	fetcher.AddPrevOut(op, txOut)
	if got := fetcher.FetchPrevOutput(op); got != txOut {
		t.Fatalf("fetcher returned %v, want %v", got, txOut)
	}
}

func TestComputeTaprootOutputKeyErrors(t *testing.T) {
	if _, err := ComputeTaprootOutputKeyFromInternalKey(nil); err == nil {
		t.Fatal("expected error for nil key")
	}
	if _, err := TaprootTweak(make([]byte, 31)); err == nil {
		t.Fatal("expected error for short internal key")
	}
	if _, err := PayToTaprootScript(make([]byte, 33)); err == nil {
		t.Fatal("expected error for long output key")
	}
	if err := VerifyTaprootKeyPathSignature(make([]byte, 32), make([]byte, 31), make([]byte, 64)); err == nil {
		t.Fatal("expected error for short digest")
	}
	if err := VerifyTaprootKeyPathSignature(make([]byte, 31), make([]byte, 32), make([]byte, 64)); err == nil {
		t.Fatal("expected error for short output key")
	}
	if err := VerifyTaprootKeyPathSignature(make([]byte, 32), make([]byte, 32), make([]byte, 65)); err == nil {
		t.Fatal("expected error for long signature")
	}
}

// TestTaprootAddressLitecoinCore pins the Litecoin bech32m encoding of the
// BIP-341 scriptPubKey vector 0 output key against Litecoin Core.
//
// The regtest string was checked on 2026-10-12 with Litecoin Core 0.21.4
// (registry.gitlab.com/thorchain/devops/node-launcher:litecoin-daemon-0.21.4):
// `validateaddress` returned isvalid=true, witness_version=1 and
// scriptPubKey=512053a1...a343, and `sendtoaddress` to it was accepted.
// The other HRPs are cross-checked against an independent bech32m encoder.
func TestTaprootAddressLitecoinCore(t *testing.T) {
	program := mustHex(t, "53a1f6e454df1aa2776a2814a721372d6258050de330b3c6d10ee8f4e0dda343")
	expectedScript := "512053a1f6e454df1aa2776a2814a721372d6258050de330b3c6d10ee8f4e0dda343"

	cases := []struct {
		net  *chaincfg.Params
		addr string
	}{
		{&chaincfg.RegressionNetParams, "rltc1p2wsldez5mud2yam29q22wgfh9439spgduvct83k3pm50fcxa5dpsxhprzv"},
		{&chaincfg.MainNetParams, "ltc1p2wsldez5mud2yam29q22wgfh9439spgduvct83k3pm50fcxa5dpshpe9c3"},
		{&chaincfg.TestNet4Params, "tltc1p2wsldez5mud2yam29q22wgfh9439spgduvct83k3pm50fcxa5dpsuwam8y"},
	}
	for _, tc := range cases {
		addr, err := NewAddressTaproot(program, tc.net)
		if err != nil {
			t.Fatalf("%s: new address: %v", tc.net.Name, err)
		}
		if got := addr.String(); got != tc.addr {
			t.Fatalf("%s: address = %s, want %s", tc.net.Name, got, tc.addr)
		}
		decoded, err := DecodeAddress(tc.addr, tc.net)
		if err != nil {
			t.Fatalf("%s: decode: %v", tc.net.Name, err)
		}
		tapAddr, ok := decoded.(*AddressTaproot)
		if !ok || !bytes.Equal(tapAddr.ScriptAddress(), program) {
			t.Fatalf("%s: decoded %T with program %x", tc.net.Name, decoded, tapAddr.ScriptAddress())
		}
		script, err := PayToAddrScript(decoded)
		if err != nil {
			t.Fatalf("%s: pay to addr script: %v", tc.net.Name, err)
		}
		if got := hex.EncodeToString(script); got != expectedScript {
			t.Fatalf("%s: script = %s, want %s", tc.net.Name, got, expectedScript)
		}
		direct, err := PayToTaprootScript(program)
		if err != nil || !bytes.Equal(direct, script) {
			t.Fatalf("%s: PayToTaprootScript mismatch: %x (%v)", tc.net.Name, direct, err)
		}
	}
	// Wrong network must not decode.
	if _, err := DecodeAddress(cases[0].addr, &chaincfg.MainNetParams); err == nil {
		t.Fatal("regtest address decoded as mainnet")
	}
}
