package txscript

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/eager7/dogd/btcec"
	"github.com/eager7/dogd/chaincfg"
	"github.com/eager7/dogd/chaincfg/chainhash"
	"github.com/eager7/dogd/wire"
	"github.com/eager7/doglog"
	"github.com/eager7/dogutil"
)

// failingSignableCov is a Signable whose Sign always fails.
type failingSignableCov struct {
	key *btcec.PrivateKey
}

func (f failingSignableCov) GetPubKey() *btcec.PublicKey { return f.key.PubKey() }

func (f failingSignableCov) Sign(_ []byte) (*btcec.Signature, error) {
	return nil, errors.New("signer unavailable")
}

func newKeyCov(t *testing.T) *btcec.PrivateKey {
	t.Helper()
	key, err := btcec.NewPrivateKey(btcec.S256())
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
	UseLogger(doglog.Disabled)
	if log != doglog.Disabled {
		t.Fatal("UseLogger did not install the provided logger")
	}
	DisableLog()
	if log != doglog.Disabled {
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

func TestWitnessSignatureCov(t *testing.T) {
	const amt = int64(50000)
	for _, compress := range []bool{true, false} {
		key := newKeyCov(t)
		var pkBytes []byte
		if compress {
			pkBytes = key.PubKey().SerializeCompressed()
		} else {
			pkBytes = key.PubKey().SerializeUncompressed()
		}
		pkHash := dogutil.Hash160(pkBytes)
		pkScript, err := payToWitnessPubKeyHashScript(pkHash)
		if err != nil {
			t.Fatal(err)
		}
		// BIP143 says the script code of a p2wpkh output is the p2pkh script.
		subScript, err := payToPubKeyHashScript(pkHash)
		if err != nil {
			t.Fatal(err)
		}

		tx := newSpendTxCov()
		sigHashes := NewTxSigHashes(tx)
		witness, err := WitnessSignature(tx, sigHashes, 0, amt, subScript,
			SigHashAll, NewPrivateKeySignable(key), compress)
		if err != nil {
			t.Fatalf("WitnessSignature: %v", err)
		}
		if len(witness) != 2 {
			t.Fatalf("expected 2 witness items, got %d", len(witness))
		}
		if !bytes.Equal(witness[1], pkBytes) {
			t.Fatalf("witness pubkey mismatch (compress=%v)", compress)
		}
		if witness[0][len(witness[0])-1] != byte(SigHashAll) {
			t.Fatal("signature must end with the hash type")
		}

		if compress {
			// Only compressed keys are standard in segwit; verify
			// the produced witness actually spends the output.
			tx.TxIn[0].Witness = witness
			vm, err := NewEngine(pkScript, tx, 0, StandardVerifyFlags, nil,
				sigHashes, amt)
			if err != nil {
				t.Fatalf("NewEngine: %v", err)
			}
			if err := vm.Execute(); err != nil {
				t.Fatalf("witness failed to verify: %v", err)
			}

			// Wrong amount must not verify (BIP143 commits to it).
			vm, err = NewEngine(pkScript, tx, 0, StandardVerifyFlags, nil,
				sigHashes, amt+1)
			if err != nil {
				t.Fatalf("NewEngine: %v", err)
			}
			if err := vm.Execute(); err == nil {
				t.Fatal("witness verified against the wrong amount")
			}
		}
	}
}

func TestWitnessSignatureErrorsCov(t *testing.T) {
	key := newKeyCov(t)
	tx := newSpendTxCov()
	sigHashes := NewTxSigHashes(tx)
	subScript, err := payToPubKeyHashScript(make([]byte, 20))
	if err != nil {
		t.Fatal(err)
	}

	// Unparseable script: a data push that runs off the end.
	_, err = RawTxInWitnessSignature(tx, sigHashes, 0, 1, []byte{OP_DATA_5, 0x01},
		SigHashAll, NewPrivateKeySignable(key))
	if err == nil || !strings.Contains(err.Error(), "cannot parse output script") {
		t.Fatalf("expected parse error, got %v", err)
	}

	// Index out of range for the transaction.
	_, err = WitnessSignature(tx, sigHashes, 5, 1, subScript, SigHashAll,
		NewPrivateKeySignable(key), true)
	if err == nil {
		t.Fatal("expected error for out of range input index")
	}

	// Signer failure is wrapped.
	_, err = WitnessSignature(tx, sigHashes, 0, 1, subScript, SigHashAll,
		failingSignableCov{key: key}, true)
	if err == nil || !strings.Contains(err.Error(), "cannot sign tx input") {
		t.Fatalf("expected signer error, got %v", err)
	}

	// Legacy signature paths with a failing signer.
	_, err = RawTxInSignature(tx, 0, subScript, SigHashAll, failingSignableCov{key: key})
	if err == nil || !strings.Contains(err.Error(), "cannot sign tx input") {
		t.Fatalf("expected signer error, got %v", err)
	}
	_, err = SignatureScript(tx, 0, subScript, SigHashAll, failingSignableCov{key: key}, true)
	if err == nil {
		t.Fatal("expected SignatureScript to propagate signer error")
	}
	_, err = p2pkSignatureScript(tx, 0, subScript, SigHashAll, failingSignableCov{key: key})
	if err == nil {
		t.Fatal("expected p2pkSignatureScript to propagate signer error")
	}
}

func TestSignErrorPathsCov(t *testing.T) {
	params := &chaincfg.TestNet3Params
	key := newKeyCov(t)
	tx := newSpendTxCov()
	noKeys := KeyClosure(func(dogutil.Address) (*btcec.PrivateKey, bool, error) {
		return nil, false, errors.New("no key")
	})
	noScripts := ScriptClosure(func(dogutil.Address) ([]byte, error) {
		return nil, errors.New("no script")
	})

	pk, err := dogutil.NewAddressPubKey(key.PubKey().SerializeCompressed(), params)
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
	sh, err := dogutil.NewAddressScriptHash(p2pk, params)
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
		_, err := SignTxOutput(params, tx, 0, tc.pkScript, SigHashAll, noKeys,
			noScripts, nil)
		if err == nil || !strings.Contains(err.Error(), tc.errSub) {
			t.Errorf("%s: expected error containing %q, got %v", tc.name,
				tc.errSub, err)
		}
	}

	// p2sh whose redeem script can't be signed propagates the inner error.
	scripts := ScriptClosure(func(dogutil.Address) ([]byte, error) {
		return p2pk, nil
	})
	_, err = SignTxOutput(params, tx, 0, p2sh, SigHashAll, noKeys, scripts, nil)
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
	// Dogecoin has no segwit: this variant emits a plain P2PKH script.
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

func TestPayToAddrScriptNilCov(t *testing.T) {
	addrs := []dogutil.Address{
		(*dogutil.AddressPubKeyHash)(nil),
		(*dogutil.AddressScriptHash)(nil),
		(*dogutil.AddressPubKey)(nil),
		(*dogutil.AddressWitnessPubKeyHash)(nil),
		(*dogutil.AddressWitnessScriptHash)(nil),
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
		PubKeyTy:              1,
		PubKeyHashTy:          2,
		WitnessV0PubKeyHashTy: 2,
		ScriptHashTy:          1,
		WitnessV0ScriptHashTy: 1,
		NullDataTy:            -1,
		NonStandardTy:         -1,
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

	// Small-int encoded secret size and locktime.
	pushes, err = ExtractAtomicSwapDataPushes(0, atomicSwapScriptCov(t, 16, 5))
	if err != nil || pushes == nil {
		t.Fatalf("expected atomic swap pushes, got %v %v", pushes, err)
	}
	if pushes.SecretSize != 16 || pushes.LockTime != 5 {
		t.Fatalf("unexpected small-int sizes: %+v", pushes)
	}

	// Wrong number of opcodes.
	pushes, err = ExtractAtomicSwapDataPushes(0, []byte{OP_TRUE})
	if err != nil || pushes != nil {
		t.Fatalf("expected nil,nil for short script, got %v %v", pushes, err)
	}

	// Right length but wrong opcodes.
	script := atomicSwapScriptCov(t, 32, 500000)
	script[0] = OP_NOTIF
	pushes, err = ExtractAtomicSwapDataPushes(0, script)
	if err != nil || pushes != nil {
		t.Fatalf("expected nil,nil for non-swap script, got %v %v", pushes, err)
	}

	// Unparseable script.
	_, err = ExtractAtomicSwapDataPushes(0, []byte{OP_DATA_5})
	if err == nil {
		t.Fatal("expected parse error")
	}
}
