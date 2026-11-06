package utxo

import (
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/dashpay/dashd-go/btcec/v2"
	"github.com/dashpay/dashd-go/btcec/v2/ecdsa"
	"github.com/dashpay/dashd-go/chaincfg"
	"github.com/dashpay/dashd-go/txscript"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/dashutil"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
)

// getChainCfgDASH returns the Dash chaincfg.Params for the current network.
func (c *Client) getChainCfgDASH() *chaincfg.Params {
	switch common.CurrentChainNetwork {
	case common.MockNet:
		return &chaincfg.RegressionNetParams
	case common.MainNet, common.StageNet, common.ChainNet:
		return &chaincfg.MainNetParams
	default:
		c.log.Fatal().Msg("unsupported network")
		return nil
	}
}

// getAddressesFromScriptPubKeyDASH extracts output addresses for a Dash vout.
// Bitcoin Core 22/23 (the base for dashd 23) removed the scriptPubKey.addresses
// array from raw-tx JSON; the btcjson.ScriptPubKeyResult we deserialize into
// therefore has an empty Addresses slice. Parse the hex-encoded script locally
// against Dash's chain params instead — same fallback the BTC client uses in
// getAddressesFromScriptPubKeyBTC.
func (c *Client) getAddressesFromScriptPubKeyDASH(scriptPubKey btcjson.ScriptPubKeyResult) []string {
	if len(scriptPubKey.Addresses) > 0 {
		return scriptPubKey.Addresses
	}
	if len(scriptPubKey.Hex) == 0 {
		return nil
	}
	buf, err := hex.DecodeString(scriptPubKey.Hex)
	if err != nil {
		c.log.Err(err).Msg("fail to decode output script hex")
		return nil
	}
	_, extracted, _, err := txscript.ExtractPkScriptAddrs(buf, c.getChainCfgDASH())
	if err != nil {
		c.log.Err(err).Msg("fail to extract output addresses")
		return nil
	}
	addresses := make([]string, 0, len(extracted))
	for _, a := range extracted {
		addresses = append(addresses, a.String())
	}
	return addresses
}

// signUTXODASH signs a prepared digest without reading or mutating the transaction.
func (c *Client) signUTXODASH(digest []byte, tx stypes.TxOutItem, thorchainHeight int64) ([]byte, error) {
	if len(digest) != 32 {
		return nil, fmt.Errorf("Dash signing digest: got %d bytes, want 32", len(digest))
	}
	var signature *ecdsa.Signature
	var pubKey *btcec.PublicKey
	if tx.VaultPubKey.Equals(c.nodePubKey) {
		key, public := btcec.PrivKeyFromBytes(c.nodePrivKey.Serialize())
		pubKey = public
		signature = ecdsa.Sign(key, digest)
	} else {
		key, err := tx.VaultPubKey.Decode()
		if err != nil {
			return nil, fmt.Errorf("fail to decode Dash vault key: %w", err)
		}
		pubKey, err = btcec.ParsePubKey(key.Bytes())
		if err != nil {
			return nil, fmt.Errorf("fail to parse Dash vault key: %w", err)
		}
		raw, _, err := c.tssKeySigner.RemoteSign(digest, common.SigningAlgoSecp256k1, tx.VaultPubKey.String(), thorchainHeight)
		if err != nil {
			return nil, err
		}
		if raw == nil {
			return nil, tss.ErrNotSelectedForKeysign
		}
		signature, err = dashutil.ParseSignatureRS(raw)
		if err != nil {
			return nil, err
		}
	}
	return dashutil.SignatureScript(digest, signature, pubKey)
}
