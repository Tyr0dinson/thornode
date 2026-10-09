package txscript

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/gcash/bchd/bchec"
	"github.com/gcash/bchd/chaincfg"
	"github.com/gcash/bchd/chaincfg/chainhash"
	"github.com/gcash/bchd/wire"
	"github.com/gcash/bchlog"
	"github.com/gcash/bchutil"
)

// failingSignableCov is a Signable whose signing operations always fail.
type failingSignableCov struct {
	key *bchec.PrivateKey
}

func (f failingSignableCov) GetPubKey() *bchec.PublicKey { return f.key.PubKey() }

func (f failingSignableCov) SignECDSA(_ []byte) (*bchec.Signature, error) {
	return nil, errors.New("signer unavailable")
}

func (f failingSignableCov) SignSchnorr(_ []byte) (*bchec.Signature, error) {
	return nil, errors.New("signer unavailable")
}

func newKeyCov(t *testing.T) *bchec.PrivateKey {
	t.Helper()
	key, err := bchec.NewPrivateKey(bchec.S256())
	if err != nil {
		t.Fatalf("failed to make privKey: %v", err)
	}
	return key
}

func newSpendTxCov() *wire.MsgTx {
	tx := wire.NewMsgTx(wire.TxVersion)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: chainhash.Hash{1}, Index: 0},
		Sequence:         wire.MaxTxInSequenceNum,
	})
	tx.AddTxOut(wire.NewTxOut(1000, []byte{OP_TRUE}))
	return tx
}

func TestLogCov(t *testing.T) {
	UseLogger(bchlog.Disabled)
	if log != bchlog.Disabled {
		t.Fatal("UseLogger did not install the provided logger")
	}
	DisableLog()
	if log != bchlog.Disabled {
		t.Fatal("DisableLog did not reset the logger")
	}

	called := 0
	lc := newLogClosure(func() string {
		called++
		return "lazy"
	})
	if called != 0 {
		t.Fatal("closure must not be evaluated eagerly")
	}
	if lc.String() != "lazy" || called != 1 {
		t.Fatalf("unexpected closure evaluation: %q (%d)", lc.String(), called)
	}
}

func TestLegacySignatureScriptCov(t *testing.T) {
	for _, compress := range []bool{true, false} {
		key := newKeyCov(t)
		var pkBytes []byte
		if compress {
			pkBytes = key.PubKey().SerializeCompressed()
		} else {
			pkBytes = key.PubKey().SerializeUncompressed()
		}
		pkScript, err := payToPubKeyHashScript(bchutil.Hash160(pkBytes))
		if err != nil {
			t.Fatal(err)
		}

		tx := newSpendTxCov()
		sigScript, err := LegacySignatureScript(tx, 0, pkScript, SigHashAll,
			NewPrivateKeySignable(key), compress)
		if err != nil {
			t.Fatalf("LegacySignatureScript: %v", err)
		}
		pushes, err := PushedData(sigScript)
		if err != nil {
			t.Fatal(err)
		}
		if len(pushes) != 2 || !bytes.Equal(pushes[1], pkBytes) {
			t.Fatalf("unexpected pushes (compress=%v): %x", compress, pushes)
		}
		// Legacy signatures do not get the fork id added.
		if pushes[0][len(pushes[0])-1] != byte(SigHashAll) {
			t.Fatal("legacy signature must carry the plain hash type")
		}

		// The legacy (pre-UAHF) sighash must verify with the legacy engine.
		tx.TxIn[0].SignatureScript = sigScript
		vm, err := NewEngine(pkScript, tx, 0, ScriptBip16|ScriptVerifyDERSignatures,
			nil, nil, 0)
		if err != nil {
			t.Fatalf("NewEngine: %v", err)
		}
		if err := vm.Execute(); err != nil {
			t.Fatalf("legacy signature failed to verify: %v", err)
		}
	}
}

func TestLegacySignatureErrorsCov(t *testing.T) {
	key := newKeyCov(t)
	tx := newSpendTxCov()
	subScript, err := payToPubKeyHashScript(make([]byte, 20))
	if err != nil {
		t.Fatal(err)
	}

	// Index out of range.
	_, err = LegacySignatureScript(tx, 3, subScript, SigHashAll,
		NewPrivateKeySignable(key), true)
	if err == nil || !strings.Contains(err.Error(), "idx 3") {
		t.Fatalf("expected index error, got %v", err)
	}

	// Signer failure.
	_, err = LegacyTxInSignature(tx, 0, subScript, SigHashAll, failingSignableCov{key: key})
	if err == nil || !strings.Contains(err.Error(), "cannot sign tx input") {
		t.Fatalf("expected signer error, got %v", err)
	}
}

func TestRawSignatureErrorsCov(t *testing.T) {
	key := newKeyCov(t)
	tx := newSpendTxCov()
	subScript, err := payToPubKeyHashScript(make([]byte, 20))
	if err != nil {
		t.Fatal(err)
	}
	bad := []byte{OP_DATA_5, 0x01}

	_, err = RawTxInECDSASignature(tx, 0, bad, SigHashAll, NewPrivateKeySignable(key), 1)
	if err == nil || !strings.Contains(err.Error(), "cannot parse output script") {
		t.Fatalf("expected parse error, got %v", err)
	}
	_, err = RawTxInSchnorrSignature(tx, 0, bad, SigHashAll, NewPrivateKeySignable(key), 1)
	if err == nil || !strings.Contains(err.Error(), "cannot parse output script") {
		t.Fatalf("expected parse error, got %v", err)
	}
	_, err = RawTxInECDSASignature(tx, 0, subScript, SigHashAll, failingSignableCov{key: key}, 1)
	if err == nil || !strings.Contains(err.Error(), "cannot sign tx input") {
		t.Fatalf("expected signer error, got %v", err)
	}
	_, err = SignatureScript(tx, 0, 1, subScript, SigHashAll, failingSignableCov{key: key}, true)
	if err == nil || !strings.Contains(err.Error(), "cannot sign tx input") {
		t.Fatalf("expected signer error, got %v", err)
	}
	_, err = p2pkSignatureScript(tx, 0, 1, subScript, SigHashAll, failingSignableCov{key: key})
	if err == nil {
		t.Fatal("expected p2pkSignatureScript to propagate signer error")
	}

	// ECDSA signature with fork id already set keeps the hash type as is.
	sig, err := RawTxInECDSASignature(tx, 0, subScript, SigHashAll|SigHashForkID,
		NewPrivateKeySignable(key), 1)
	if err != nil {
		t.Fatal(err)
	}
	if sig[len(sig)-1] != byte(SigHashAll|SigHashForkID) {
		t.Fatalf("unexpected hash type byte %x", sig[len(sig)-1])
	}
}

func TestSignErrorPathsCov(t *testing.T) {
	params := &chaincfg.TestNet3Params
	key := newKeyCov(t)
	tx := newSpendTxCov()
	noKeys := KeyClosure(func(bchutil.Address) (*bchec.PrivateKey, bool, error) {
		return nil, false, errors.New("no key")
	})
	noScripts := ScriptClosure(func(bchutil.Address) ([]byte, error) {
		return nil, errors.New("no script")
	})

	pk, err := bchutil.NewAddressPubKey(key.PubKey().SerializeCompressed(), params)
	if err != nil {
		t.Fatal(err)
	}
	p2pk, err := PayToAddrScript(pk)
	if err != nil {
		t.Fatal(err)
	}
	p2pkh, err := PayToAddrScript(pk.AddressPubKeyHash())
	if err != nil {
		t.Fatal(err)
	}
	sh, err := bchutil.NewAddressScriptHash(p2pk, params)
	if err != nil {
		t.Fatal(err)
	}
	p2sh, err := PayToAddrScript(sh)
	if err != nil {
		t.Fatal(err)
	}
	nullData, err := NullDataScript([]byte("memo"))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		pkScript []byte
		errSub   string
	}{
		{"p2pk missing key", p2pk, "no key"},
		{"p2pkh missing key", p2pkh, "no key"},
		{"p2sh missing script", p2sh, "no script"},
		{"null data", nullData, "can't sign NULLDATA"},
		{"nonstandard", []byte{OP_TRUE}, "can't sign unknown"},
	}
	for _, tc := range tests {
		_, err := SignTxOutput(params, tx, 0, 1, tc.pkScript, SigHashAll, noKeys,
			noScripts, nil)
		if err == nil || !strings.Contains(err.Error(), tc.errSub) {
			t.Errorf("%s: expected error containing %q, got %v", tc.name,
				tc.errSub, err)
		}
	}

	// p2sh whose redeem script can't be signed propagates the inner error.
	scripts := ScriptClosure(func(bchutil.Address) ([]byte, error) {
		return p2pk, nil
	})
	_, err = SignTxOutput(params, tx, 0, 1, p2sh, SigHashAll, noKeys, scripts, nil)
	if err == nil || !strings.Contains(err.Error(), "no key") {
		t.Fatalf("expected inner p2sh sign error, got %v", err)
	}
}

func TestPayToWitnessScriptCov(t *testing.T) {
	data := bytes.Repeat([]byte{0xab}, P2WPKHSize)
	script, err := PayToWitnessScript(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Bitcoin Cash has no segwit: this variant emits a plain P2PKH script.
	want := append([]byte{OP_DUP, OP_HASH160, OP_DATA_20}, data...)
	want = append(want, OP_EQUALVERIFY, OP_CHECKSIG)
	if !bytes.Equal(script, want) {
		t.Fatalf("got %x want %x", script, want)
	}
	if GetScriptClass(script) != PubKeyHashTy {
		t.Fatalf("unexpected class %v", GetScriptClass(script))
	}

	_, err = PayToWitnessScript(data[:P2WPKHSize-1])
	if !IsErrorCode(err, ErrTooMuchNullData) {
		t.Fatalf("expected ErrTooMuchNullData, got %v", err)
	}
}

func TestPayToAddrScriptCov(t *testing.T) {
	params := &chaincfg.TestNet3Params
	hash := bytes.Repeat([]byte{0x42}, 20)

	legacyPKH, err := bchutil.NewLegacyAddressPubKeyHash(hash, params)
	if err != nil {
		t.Fatal(err)
	}
	script, err := PayToAddrScript(legacyPKH)
	if err != nil {
		t.Fatal(err)
	}
	if GetScriptClass(script) != PubKeyHashTy || !bytes.Contains(script, hash) {
		t.Fatalf("unexpected legacy p2pkh script %x", script)
	}

	legacySH, err := bchutil.NewLegacyAddressScriptHashFromHash(hash, params)
	if err != nil {
		t.Fatal(err)
	}
	script, err = PayToAddrScript(legacySH)
	if err != nil {
		t.Fatal(err)
	}
	if GetScriptClass(script) != ScriptHashTy || !bytes.Contains(script, hash) {
		t.Fatalf("unexpected legacy p2sh script %x", script)
	}

	addrs := []bchutil.Address{
		(*bchutil.LegacyAddressPubKeyHash)(nil),
		(*bchutil.AddressPubKeyHash)(nil),
		(*bchutil.LegacyAddressScriptHash)(nil),
		(*bchutil.AddressScriptHash)(nil),
		(*bchutil.AddressPubKey)(nil),
		nil,
	}
	for _, addr := range addrs {
		_, err := PayToAddrScript(addr)
		if !IsErrorCode(err, ErrUnsupportedAddress) {
			t.Errorf("%T: expected ErrUnsupportedAddress, got %v", addr, err)
		}
	}
}

func TestExpectedInputsCov(t *testing.T) {
	cases := map[ScriptClass]int{
		PubKeyTy:      1,
		PubKeyHashTy:  2,
		ScriptHashTy:  1,
		NullDataTy:    -1,
		NonStandardTy: -1,
	}
	for class, want := range cases {
		if got := expectedInputs(nil, class); got != want {
			t.Errorf("%v: got %d want %d", class, got, want)
		}
	}
}

func atomicSwapScriptCov(t *testing.T, secretSize, lockTime int64) []byte {
	t.Helper()
	b := NewScriptBuilder()
	b.AddOp(OP_IF)
	b.AddOp(OP_SIZE)
	b.AddInt64(secretSize)
	b.AddOp(OP_EQUALVERIFY)
	b.AddOp(OP_SHA256)
	b.AddData(bytes.Repeat([]byte{0x33}, 32))
	b.AddOp(OP_EQUALVERIFY)
	b.AddOp(OP_DUP)
	b.AddOp(OP_HASH160)
	b.AddData(bytes.Repeat([]byte{0x01}, 20))
	b.AddOp(OP_ELSE)
	b.AddInt64(lockTime)
	b.AddOp(OP_CHECKLOCKTIMEVERIFY)
	b.AddOp(OP_DROP)
	b.AddOp(OP_DUP)
	b.AddOp(OP_HASH160)
	b.AddData(bytes.Repeat([]byte{0x02}, 20))
	b.AddOp(OP_ENDIF)
	b.AddOp(OP_EQUALVERIFY)
	b.AddOp(OP_CHECKSIG)
	script, err := b.Script()
	if err != nil {
		t.Fatal(err)
	}
	return script
}

func TestExtractAtomicSwapDataPushesCov(t *testing.T) {
	pushes, err := ExtractAtomicSwapDataPushes(0, atomicSwapScriptCov(t, 32, 500000))
	if err != nil || pushes == nil {
		t.Fatalf("expected atomic swap pushes, got %v %v", pushes, err)
	}
	if pushes.SecretSize != 32 || pushes.LockTime != 500000 {
		t.Fatalf("unexpected sizes: %+v", pushes)
	}
	if pushes.RecipientHash160[0] != 0x01 || pushes.RefundHash160[0] != 0x02 ||
		pushes.SecretHash[0] != 0x33 {
		t.Fatalf("unexpected data: %+v", pushes)
	}

	pushes, err = ExtractAtomicSwapDataPushes(0, atomicSwapScriptCov(t, 16, 5))
	if err != nil || pushes == nil {
		t.Fatalf("expected atomic swap pushes, got %v %v", pushes, err)
	}
	if pushes.SecretSize != 16 || pushes.LockTime != 5 {
		t.Fatalf("unexpected small-int sizes: %+v", pushes)
	}

	pushes, err = ExtractAtomicSwapDataPushes(0, []byte{OP_TRUE})
	if err != nil || pushes != nil {
		t.Fatalf("expected nil,nil for short script, got %v %v", pushes, err)
	}

	script := atomicSwapScriptCov(t, 32, 500000)
	script[0] = OP_NOTIF
	pushes, err = ExtractAtomicSwapDataPushes(0, script)
	if err != nil || pushes != nil {
		t.Fatalf("expected nil,nil for non-swap script, got %v %v", pushes, err)
	}

	_, err = ExtractAtomicSwapDataPushes(0, []byte{OP_DATA_5})
	if err == nil {
		t.Fatal("expected parse error")
	}
}
