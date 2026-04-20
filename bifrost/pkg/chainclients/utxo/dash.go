package utxo

import (
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcjson"
	btcchaincfg "github.com/btcsuite/btcd/chaincfg"
	btcwire "github.com/btcsuite/btcd/wire"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/dashutil"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	btctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"
	"gitlab.com/thorchain/thornode/v3/common"
)

// payToPubKeyHashDashMemo builds a P2PKH-shaped scriptPubKey where the
// 20-byte "hash160" field carries arbitrary memo bytes (used by MemoToScripts
// to split memos >80 bytes into dust-funded overflow outputs). On Dash (no
// SegWit), this shape is always standard under IsStandardTx — unlike the
// OP_0 <hash> "P2WPKH" shape used by DOGE/BCH, which resolves to
// TxoutType::NONSTANDARD on dashd 23 (Dash's TxoutType enum has no
// WITNESS_V0_KEYHASH variant). The output is unspendable in practice because
// no one has a private key whose hash160 equals random memo bytes, so the
// attached dust is effectively burned — same end state as the DOGE/BCH flow.
func payToPubKeyHashDashMemo(hash []byte) ([]byte, error) {
	return btctxscript.NewScriptBuilder().
		AddOp(btctxscript.OP_DUP).
		AddOp(btctxscript.OP_HASH160).
		AddData(hash).
		AddOp(btctxscript.OP_EQUALVERIFY).
		AddOp(btctxscript.OP_CHECKSIG).
		Script()
}

// getChainCfgDASH returns the Dash chaincfg.Params for the current network.
// Dash uses Bitcoin-compatible single-byte base58check addresses, so the
// standard btcd/chaincfg.Params struct is sufficient — only the prefix bytes,
// magic, and BIP32 ids need to be Dash-specific. See the dashutil package for
// the param definitions.
func (c *Client) getChainCfgDASH() *btcchaincfg.Params {
	switch common.CurrentChainNetwork {
	case common.MockNet:
		return &dashutil.RegressionNetParams
	case common.MainNet, common.StageNet, common.ChainNet:
		return &dashutil.MainNetParams
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
		c.log.Err(err).Msg("fail to hex decode script pub key")
		return nil
	}
	_, extracted, _, err := btctxscript.ExtractPkScriptAddrs(buf, c.getChainCfgDASH())
	if err != nil {
		c.log.Err(err).Msg("fail to extract addresses from script pub key")
		return nil
	}
	addresses := make([]string, 0, len(extracted))
	for _, a := range extracted {
		addresses = append(addresses, a.String())
	}
	return addresses
}

// signUTXODASH signs a single input of the given redeem transaction. Dash is
// a non-SegWit Bitcoin fork, so the signing flow mirrors DOGE/BCH: build a
// legacy ECDSA signature with SigHashAll and place sig+pubkey in the
// SignatureScript. Verification uses btctxscript.StandardVerifyFlags;
// witness-related flags in that set are no-ops for P2PKH inputs (no witness
// stack means no evaluation path), and BIP16 P2SH support matches Dash.
func (c *Client) signUTXODASH(redeemTx *btcwire.MsgTx, tx stypes.TxOutItem, amount int64, sourceScript []byte, idx int) error {
	var signable btctxscript.Signable
	if tx.VaultPubKey.Equals(c.nodePubKey) {
		signable = btctxscript.NewPrivateKeySignable(c.nodePrivKey)
	} else {
		signable = newTssSignableBTC(tx.VaultPubKey, c.tssKeySigner, c.log)
	}

	sig, err := btctxscript.RawTxInSignature(redeemTx, idx, sourceScript, btctxscript.SigHashAll, signable)
	if err != nil {
		return fmt.Errorf("fail to get signature: %w", err)
	}

	pkData := signable.GetPubKey().SerializeCompressed()
	sigscript, err := btctxscript.NewScriptBuilder().AddData(sig).AddData(pkData).Script()
	if err != nil {
		return fmt.Errorf("fail to build signature script: %w", err)
	}
	redeemTx.TxIn[idx].SignatureScript = sigscript

	flag := btctxscript.StandardVerifyFlags
	engine, err := btctxscript.NewEngine(sourceScript, redeemTx, idx, flag, nil, nil, amount)
	if err != nil {
		return fmt.Errorf("fail to create engine: %w", err)
	}
	if err = engine.Execute(); err != nil {
		// NULLFAIL failsafe: see signUTXOBTC for rationale.
		if btctxscript.IsErrorCode(err, btctxscript.ErrNullFail) {
			c.log.Error().
				Err(err).
				Int("input_idx", idx).
				Msg("NULLFAIL FAILSAFE TRIGGERED - This should not happen! Investigate immediately!")
			return nil
		}
		return fmt.Errorf("fail to execute the script: %w", err)
	}
	return nil
}
