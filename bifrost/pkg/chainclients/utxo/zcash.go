package utxo

import (
	"encoding/hex"
	"fmt"

	btcjson "github.com/btcsuite/btcd/btcjson"
	btcchaincfg "github.com/btcsuite/btcd/chaincfg"
	btcwire "github.com/btcsuite/btcd/wire"
	btctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

// getChainCfgZEC returns the Zcash chain configuration for the current network.
// For transparent transactions, we can use Bitcoin's chain params as they share
// similar UTXO structure.
func (c *Client) getChainCfgZEC() *btcchaincfg.Params {
	switch common.CurrentChainNetwork {
	case common.MockNet:
		return &btcchaincfg.RegressionNetParams
	case common.TestNet:
		return &btcchaincfg.TestNet3Params
	case common.MainNet:
		// ZEC mainnet - for transparent addresses we can use BTC params
		// as the address format and UTXO model are compatible
		return &btcchaincfg.MainNetParams
	case common.StageNet:
		return &btcchaincfg.MainNetParams
	default:
		c.log.Fatal().Msg("unsupported network")
		return nil
	}
}

// signUTXOZEC signs a Zcash transparent transaction using witness signatures.
// Transparent ZEC transactions use P2WPKH similar to Bitcoin, so we can reuse
// the Bitcoin signing logic for now.
func (c *Client) signUTXOZEC(redeemTx *btcwire.MsgTx, tx stypes.TxOutItem, amount int64, sourceScript []byte, idx int) error {
	sigHashes := btctxscript.NewTxSigHashes(redeemTx)

	var signable btctxscript.Signable
	if tx.VaultPubKey.Equals(c.nodePubKey) {
		signable = btctxscript.NewPrivateKeySignable(c.nodePrivKey)
	} else {
		signable = newTssSignableZEC(tx.VaultPubKey, c.tssKeySigner, c.log)
	}

	witness, err := btctxscript.WitnessSignature(redeemTx, sigHashes, idx, amount, sourceScript, btctxscript.SigHashAll, signable, true)
	if err != nil {
		return fmt.Errorf("fail to get witness: %w", err)
	}

	redeemTx.TxIn[idx].Witness = witness
	flag := btctxscript.StandardVerifyFlags
	engine, err := btctxscript.NewEngine(sourceScript, redeemTx, idx, flag, nil, nil, amount)
	if err != nil {
		return fmt.Errorf("fail to create engine: %w", err)
	}
	if err = engine.Execute(); err != nil {
		return fmt.Errorf("fail to execute the script: %w", err)
	}
	return nil
}

// getAddressesFromScriptPubKeyZEC extracts Zcash transparent addresses from script public key.
// For transparent addresses, ZEC follows Bitcoin's address format.
func (c *Client) getAddressesFromScriptPubKeyZEC(scriptPubKey btcjson.ScriptPubKeyResult) []string {
	addresses := scriptPubKey.Addresses
	if len(addresses) > 0 {
		return addresses
	}

	if len(scriptPubKey.Hex) == 0 {
		return nil
	}
	buf, err := hex.DecodeString(scriptPubKey.Hex)
	if err != nil {
		c.log.Err(err).Msg("fail to hex decode script pub key")
		return nil
	}
	_, extractedAddresses, _, err := btctxscript.ExtractPkScriptAddrs(buf, c.getChainCfgZEC())
	if err != nil {
		c.log.Err(err).Msg("fail to extract addresses from script pub key")
		return nil
	}
	for _, item := range extractedAddresses {
		addresses = append(addresses, item.String())
	}
	return addresses
}
