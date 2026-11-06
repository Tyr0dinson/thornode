package firoutil

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

func fixture(t *testing.T) (*wire.MsgTx, []*wire.TxOut, *btcec.PrivateKey) {
	t.Helper()
	key, _ := btcec.PrivKeyFromBytes(btcec.S256(), []byte{1})
	address, err := NewExchangeAddress(key.PubKey().SerializeCompressed(), RegTest)
	if err != nil {
		t.Fatal(err)
	}
	tx := wire.NewMsgTx(1)
	tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&chainhash.Hash{1}, 0), nil, nil))
	tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&chainhash.Hash{2}, 1), nil, nil))
	tx.AddTxOut(wire.NewTxOut(190000, address.Script()))
	return tx, []*wire.TxOut{wire.NewTxOut(100000, address.Script()), wire.NewTxOut(100000, address.Script())}, key
}

// Independent legacy SIGHASH_ALL preimage, without the shared script library.
func referenceHash(t *testing.T, tx *wire.MsgTx, previous []*wire.TxOut, index int) []byte {
	t.Helper()
	copyTx := tx.Copy()
	for i := range copyTx.TxIn {
		copyTx.TxIn[i].SignatureScript = nil
	}
	copyTx.TxIn[index].SignatureScript = previous[index].PkScript
	var preimage bytes.Buffer
	err := copyTx.SerializeNoWitness(&preimage)
	if err != nil {
		t.Fatal(err)
	}
	err = binary.Write(&preimage, binary.LittleEndian, uint32(1))
	if err != nil {
		t.Fatal(err)
	}
	first := sha256.Sum256(preimage.Bytes())
	second := sha256.Sum256(first[:])
	return second[:]
}

func TestExchangeSigning(t *testing.T) {
	tx, previous, key := fixture(t)
	hashes, err := SignatureHashes(tx, previous)
	if err != nil {
		t.Fatal(err)
	}
	for i, hash := range hashes {
		if !bytes.Equal(hash, referenceHash(t, tx, previous, i)) {
			t.Fatal("legacy preimage mismatch")
		}
		sig, signErr := key.Sign(hash)
		if signErr != nil {
			t.Fatal(signErr)
		}
		// Either S encoding is valid; script encoding must normalize it.
		raw := make([]byte, 64)
		sig.R.FillBytes(raw[:32])
		new(big.Int).Sub(btcec.S256().N, sig.S).FillBytes(raw[32:])
		parsedSig, signatureErr := ParseSignatureRS(raw)
		if signatureErr != nil {
			t.Fatal(signatureErr)
		}
		tx.TxIn[i].SignatureScript, err = SignatureScript(hash, parsedSig, key.PubKey())
		if err != nil {
			t.Fatal(err)
		}
	}
	err = VerifyTransaction(tx, previous)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*wire.MsgTx){
		"amount":       func(tx *wire.MsgTx) { tx.TxOut[0].Value++ },
		"sighash":      func(tx *wire.MsgTx) { s := tx.TxIn[0].SignatureScript; s[int(s[0])] = 2 },
		"nonminimal":   func(tx *wire.MsgTx) { tx.TxIn[0].SignatureScript = append([]byte{0x4c}, tx.TxIn[0].SignatureScript...) },
		"witness":      func(tx *wire.MsgTx) { tx.TxIn[0].Witness = wire.TxWitness{[]byte{1}} },
		"special type": func(tx *wire.MsgTx) { tx.Version = 0x10003 },
		"locktime":     func(tx *wire.MsgTx) { tx.LockTime = 1 },
		"duplicate":    func(tx *wire.MsgTx) { tx.TxIn[1].PreviousOutPoint = tx.TxIn[0].PreviousOutPoint },
		"sequence":     func(tx *wire.MsgTx) { tx.TxIn[0].Sequence-- },
	} {
		t.Run(name, func(t *testing.T) {
			changed := tx.Copy()
			mutate(changed)
			verifyErr := VerifyTransaction(changed, previous)
			if verifyErr == nil {
				t.Fatal("accepted mutation")
			}
		})
	}
	ordinary := []*wire.TxOut{wire.NewTxOut(100000, previous[0].PkScript[1:]), previous[1]}
	_, err = SignatureHashes(tx, ordinary)
	if err == nil {
		t.Fatal("accepted ordinary P2PKH vault input")
	}
	_, err = ParseSignatureRS(make([]byte, 64))
	if err == nil {
		t.Fatal("accepted zero scalars")
	}
	_, err = ParseSignatureRS(make([]byte, 65))
	if err == nil {
		t.Fatal("accepted malformed R || S signature")
	}
}
