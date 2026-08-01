package utxo

import (
	"fmt"

	"github.com/btcsuite/btcd/btcec"
	btcjson "github.com/btcsuite/btcd/btcjson"
	"github.com/cometbft/cometbft/crypto/secp256k1"

	"gitlab.com/thorchain/thornode/v3/common"
)

func bech32AccountPubKey(key *btcec.PrivateKey) (common.PubKey, error) {
	buf := key.PubKey().SerializeCompressed()
	pk := secp256k1.PubKey(buf)
	return common.NewPubKeyFromCrypto(pk)
}

func (c *Client) sumVoutBaseUnits(tx *btcjson.TxRawResult) (uint64, error) {
	var totalBaseUnits uint64
	for _, vout := range tx.Vout {
		baseUnits, err := c.rpc.ToBaseUnits(vout.Value)
		if err != nil {
			return 0, err
		}
		amount, err := toUint64(baseUnits)
		if err != nil {
			return 0, err
		}
		if ^uint64(0)-totalBaseUnits < amount {
			return 0, fmt.Errorf("sum of transaction outputs overflows uint64")
		}
		totalBaseUnits += amount
	}
	return totalBaseUnits, nil
}
