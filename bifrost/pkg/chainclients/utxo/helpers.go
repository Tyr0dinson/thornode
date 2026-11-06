package utxo

import (
	"fmt"

	"github.com/btcsuite/btcd/btcec"
	btcjson "github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcutil"
	"github.com/cometbft/cometbft/crypto/secp256k1"

	"gitlab.com/thorchain/thornode/v3/common"
)

func bech32AccountPubKey(key *btcec.PrivateKey) (common.PubKey, error) {
	buf := key.PubKey().SerializeCompressed()
	pk := secp256k1.PubKey(buf)
	return common.NewPubKeyFromCrypto(pk)
}

func sumVoutSats(tx *btcjson.TxRawResult) (uint64, error) {
	var sumVout uint64 = 0
	for _, vout := range tx.Vout {
		amount, err := btcutil.NewAmount(vout.Value)
		if err != nil {
			return 0, err
		}
		sumVout += uint64(amount.ToUnit(btcutil.AmountSatoshi))
	}
	return sumVout, nil
}

// isHash256Hex checks the encoding of a 32-byte hash, without verifying what it identifies.
func isHash256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := chainhash.NewHashFromStr(value)
	return err == nil
}

// amountToSats rounds an amount with 1e8 base units per coin and rejects
// negative values or values above btcutil.MaxSatoshi before and after rounding.
func amountToSats(value float64) (int64, error) {
	amount, err := btcutil.NewAmount(value)
	// Validate raw RPC bounds as well as the rounded integer amount.
	// analyze-ignore(float-comparison)
	if err != nil || value < 0 || value > float64(btcutil.MaxSatoshi)/1e8 || amount < 0 || amount > btcutil.MaxSatoshi {
		return 0, fmt.Errorf("invalid amount %v", value)
	}
	return int64(amount), nil
}
