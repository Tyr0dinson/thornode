package firoutil

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"
)

// ValidateOutboundFormat checks the transaction format supported for outbound
// signing, including version, input sequence, witness and amount restrictions.
func ValidateOutboundFormat(tx *wire.MsgTx) error {
	if tx == nil || tx.Version != 1 || tx.LockTime != 0 || len(tx.TxIn) == 0 || len(tx.TxOut) == 0 {
		return fmt.Errorf("unsupported FIRO outbound transaction")
	}
	seen := make(map[wire.OutPoint]bool)
	for _, in := range tx.TxIn {
		if in == nil || len(in.Witness) != 0 || in.Sequence != wire.MaxTxInSequenceNum || seen[in.PreviousOutPoint] {
			return fmt.Errorf("invalid FIRO outbound input")
		}
		seen[in.PreviousOutPoint] = true
	}
	var total int64
	for _, out := range tx.TxOut {
		if out == nil || out.Value < 0 || out.Value > btcutil.MaxSatoshi-total {
			return fmt.Errorf("invalid FIRO outbound amount")
		}
		total += out.Value
	}
	return nil
}

func SignatureHashes(tx *wire.MsgTx, previous []*wire.TxOut) ([][]byte, error) {
	err := ValidateOutboundFormat(tx)
	if err != nil {
		return nil, err
	}
	if len(previous) != len(tx.TxIn) {
		return nil, fmt.Errorf("FIRO signing requires every previous output")
	}
	hashes := make([][]byte, len(previous))
	for i, out := range previous {
		if out == nil || out.Value <= 0 || out.Value > btcutil.MaxSatoshi {
			return nil, fmt.Errorf("invalid FIRO previous output %d", i)
		}
		address, err := AddressFromScript(out.PkScript, MainNet)
		if err != nil || address.Kind() != Exchange {
			return nil, fmt.Errorf("FIRO vault input %d must use the exact exchange script", i)
		}
		// OP_EXCHANGEADDR is part of scriptCode. The Bitcoin hash function
		// preserves unknown opcodes; only its execution engine rejects them.
		hashes[i], err = txscript.CalcSignatureHash(out.PkScript, txscript.SigHashAll, tx, i)
		if err != nil {
			return nil, err
		}
	}
	return hashes, nil
}

// ParseSignatureRS parses two 32-byte, big-endian ECDSA scalars in R || S order.
// It validates the scalar ranges; it does not verify the signature against a message.
func ParseSignatureRS(raw []byte) (*btcec.Signature, error) {
	if len(raw) != 64 {
		return nil, fmt.Errorf("signature R || S: got %d bytes, want 64", len(raw))
	}
	r, s := new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:])
	if r.Sign() <= 0 || s.Sign() <= 0 || r.Cmp(btcec.S256().N) >= 0 || s.Cmp(btcec.S256().N) >= 0 {
		return nil, fmt.Errorf("invalid signature scalar")
	}
	return &btcec.Signature{R: r, S: s}, nil
}

func SignatureScript(digest []byte, signature *btcec.Signature, key *btcec.PublicKey) ([]byte, error) {
	if len(digest) != 32 || signature == nil || key == nil || !signature.Verify(digest, key) {
		return nil, fmt.Errorf("invalid FIRO signature")
	}
	return txscript.NewScriptBuilder().
		AddData(append(signature.Serialize(), byte(txscript.SigHashAll))).
		AddData(key.SerializeCompressed()).Script()
}

// VerifyTransaction enforces precisely the scripts we generate, including
// canonical pushes, compressed keys and low-S DER. No script errors are ignored.
func VerifyTransaction(tx *wire.MsgTx, previous []*wire.TxOut) error {
	hashes, err := SignatureHashes(tx, previous)
	if err != nil {
		return err
	}
	var inputTotal, outputTotal int64
	for i, in := range tx.TxIn {
		if previous[i].Value > btcutil.MaxSatoshi-inputTotal {
			return fmt.Errorf("FIRO input amount overflow")
		}
		inputTotal += previous[i].Value
		script := in.SignatureScript
		if len(script) < 2 || int(script[0])+35 != len(script) || script[0] < 9 || script[0] > 73 {
			return fmt.Errorf("invalid FIRO signature script %d", i)
		}
		n := int(script[0])
		if script[n] != byte(txscript.SigHashAll) || script[n+1] != 33 {
			return fmt.Errorf("invalid FIRO signature pushes %d", i)
		}
		key, err := btcec.ParsePubKey(script[n+2:], btcec.S256())
		if err != nil || !bytes.Equal(btcutil.Hash160(script[n+2:]), previous[i].PkScript[4:24]) {
			return fmt.Errorf("FIRO input %d does not match vault key", i)
		}
		sig, err := btcec.ParseDERSignature(script[1:n], btcec.S256())
		if err != nil || !bytes.Equal(sig.Serialize(), script[1:n]) || !sig.Verify(hashes[i], key) {
			return fmt.Errorf("FIRO input %d has an invalid or noncanonical signature", i)
		}
	}
	for _, out := range tx.TxOut {
		outputTotal += out.Value
	}
	if outputTotal > inputTotal {
		return fmt.Errorf("FIRO outputs exceed inputs")
	}
	return nil
}
