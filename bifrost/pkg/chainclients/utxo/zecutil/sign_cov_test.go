package zecutil

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
)

func newZecTxCov() *MsgTx {
	tx := wire.NewMsgTx(versionSapling)
	tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&chainhash.Hash{7}, 0), nil, nil))
	tx.AddTxOut(wire.NewTxOut(1000, []byte{txscript.OP_TRUE}))
	return &MsgTx{MsgTx: tx, ExpiryHeight: 2_000_000}
}

func newKeyCovZec(t *testing.T) *btcec.PrivateKey {
	t.Helper()
	key, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestBlake2bHashPersonTooLongCov(t *testing.T) {
	// blake2b personalization is limited to 16 bytes.
	if _, err := blake2bHash([]byte("data"), bytes.Repeat([]byte("x"), 17)); err == nil {
		t.Fatal("expected an error for an oversized personalization")
	}
	h1, err := blake2bHash([]byte("data"), []byte("ZcashSigHash"))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := blake2bHash([]byte("data"), []byte("ZcashOtherHash"))
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("personalization must change the digest")
	}
}

func TestSignatureErrorsCov(t *testing.T) {
	key := newKeyCovZec(t)
	tx := newZecTxCov()

	if _, err := RawTxInSignature(tx, 3, nil, txscript.SigHashAll, key, 1); err == nil ||
		!strings.Contains(err.Error(), "idx 3 but 1 txins") {
		t.Fatalf("expected index error, got %v", err)
	}
	if _, err := SignatureScript(tx, 3, nil, txscript.SigHashAll, key, true, 1); err == nil {
		t.Fatal("expected SignatureScript to propagate the sighash error")
	}

	// Uncompressed key variant pushes the 65 byte pubkey.
	script, err := SignatureScript(tx, 0, []byte{txscript.OP_TRUE}, txscript.SigHashAll, key, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	pushes, err := txscript.PushedData(script)
	if err != nil {
		t.Fatal(err)
	}
	if len(pushes) != 2 || len(pushes[1]) != 65 {
		t.Fatalf("unexpected pushes %x", pushes)
	}
}

func TestSigHashSingleAndAnyoneCanPayCov(t *testing.T) {
	tx := newZecTxCov()
	cache, err := NewTxSigHashes(tx)
	if err != nil {
		t.Fatal(err)
	}
	all, err := Blake2bSignatureHash(nil, cache, txscript.SigHashAll, tx, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	single, err := Blake2bSignatureHash(nil, cache, txscript.SigHashSingle, tx, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	acp, err := Blake2bSignatureHash(nil, cache, txscript.SigHashAll|txscript.SigHashAnyOneCanPay, tx, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(all, single) || bytes.Equal(all, acp) || bytes.Equal(single, acp) {
		t.Fatal("each sighash mode must commit to a different preimage")
	}
}

func TestSignTxOutputScriptHashCov(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	key := newKeyCovZec(t)
	pkh, err := btcutil.NewAddressPubKeyHash(btcutil.Hash160(key.PubKey().SerializeCompressed()), params)
	if err != nil {
		t.Fatal(err)
	}
	redeem, err := txscript.PayToAddrScript(pkh)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := btcutil.NewAddressScriptHash(redeem, params)
	if err != nil {
		t.Fatal(err)
	}
	p2sh, err := txscript.PayToAddrScript(sh)
	if err != nil {
		t.Fatal(err)
	}

	keys := txscript.KeyClosure(func(btcutil.Address) (*btcec.PrivateKey, bool, error) {
		return key, true, nil
	})
	noKeys := txscript.KeyClosure(func(btcutil.Address) (*btcec.PrivateKey, bool, error) {
		return nil, false, errors.New("no key")
	})
	scripts := txscript.ScriptClosure(func(btcutil.Address) ([]byte, error) { return redeem, nil })
	noScripts := txscript.ScriptClosure(func(btcutil.Address) ([]byte, error) { return nil, errors.New("no script") })

	sigScript, err := SignTxOutput(params, newZecTxCov(), 0, p2sh, txscript.SigHashAll, keys, scripts, nil, 1000)
	if err != nil {
		t.Fatal(err)
	}
	pushes, err := txscript.PushedData(sigScript)
	if err != nil {
		t.Fatal(err)
	}
	// <sig> <pubkey> <redeem script>
	if len(pushes) != 3 || !bytes.Equal(pushes[2], redeem) {
		t.Fatalf("unexpected p2sh signature script %x", sigScript)
	}

	// A longer previous script wins the merge.
	prev := bytes.Repeat([]byte{txscript.OP_TRUE}, len(sigScript)+1)
	merged, err := SignTxOutput(params, newZecTxCov(), 0, p2sh, txscript.SigHashAll, keys, scripts, prev, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(merged, prev) {
		t.Fatal("expected the longer previous script to be kept")
	}

	if _, err = SignTxOutput(params, newZecTxCov(), 0, p2sh, txscript.SigHashAll, keys, noScripts, nil, 1000); err == nil ||
		err.Error() != "no script" {
		t.Fatalf("expected script lookup error, got %v", err)
	}
	if _, err = SignTxOutput(params, newZecTxCov(), 0, p2sh, txscript.SigHashAll, noKeys, scripts, nil, 1000); err == nil ||
		err.Error() != "no key" {
		t.Fatalf("expected inner key lookup error, got %v", err)
	}

	// p2pkh with an out-of-range input fails while hashing.
	if _, err = SignTxOutput(params, newZecTxCov(), 4, redeem, txscript.SigHashAll, keys, scripts, nil, 1000); err == nil {
		t.Fatal("expected sighash error")
	}
	// Unparseable and non-standard scripts.
	if _, err = SignTxOutput(params, newZecTxCov(), 0, []byte{txscript.OP_DATA_5}, txscript.SigHashAll, keys, scripts, nil, 1); err == nil {
		t.Fatal("expected parse error")
	}
	if _, err = SignTxOutput(params, newZecTxCov(), 0, []byte{txscript.OP_TRUE}, txscript.SigHashAll, keys, scripts, nil, 1); err == nil ||
		err.Error() != "can't sign unknown transactions" {
		t.Fatalf("expected unknown script error, got %v", err)
	}
}

func TestSignTxOutputMultiSigCov(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	key1 := newKeyCovZec(t)
	key2 := newKeyCovZec(t)
	pk1, err := btcutil.NewAddressPubKey(key1.PubKey().SerializeCompressed(), params)
	if err != nil {
		t.Fatal(err)
	}
	pk2, err := btcutil.NewAddressPubKey(key2.PubKey().SerializeCompressed(), params)
	if err != nil {
		t.Fatal(err)
	}
	multisig, err := txscript.MultiSigScript([]*btcutil.AddressPubKey{pk1, pk2}, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Only the second key is available: one signature satisfies 1-of-2.
	keys := txscript.KeyClosure(func(addr btcutil.Address) (*btcec.PrivateKey, bool, error) {
		if addr.EncodeAddress() == pk2.EncodeAddress() {
			return key2, true, nil
		}
		return nil, false, errors.New("unknown key")
	})
	script, err := SignTxOutput(params, newZecTxCov(), 0, multisig, txscript.SigHashAll, keys, nil, nil, 1000)
	if err != nil {
		t.Fatal(err)
	}
	pushes, err := txscript.PushedData(script)
	if err != nil {
		t.Fatal(err)
	}
	// OP_FALSE (nil push) followed by exactly one signature
	if len(pushes) != 2 || pushes[0] != nil || len(pushes[1]) < 64 {
		t.Fatalf("unexpected multisig script %x", script)
	}

	// Signing fails for an out-of-range input: no signatures are added.
	script, err = SignTxOutput(params, newZecTxCov(), 5, multisig, txscript.SigHashAll, keys, nil, nil, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(script, []byte{txscript.OP_FALSE}) {
		t.Fatalf("expected only OP_FALSE, got %x", script)
	}
}
