package utxo

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/dashpay/dashd-go/btcec/v2"
	"github.com/dashpay/dashd-go/btcec/v2/ecdsa"
	"github.com/dashpay/dashd-go/btcutil"
	"github.com/dashpay/dashd-go/chaincfg"
	"github.com/dashpay/dashd-go/txscript"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/dashutil"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
)

// payToPubKeyHashMemoScript builds a P2PKH-shaped scriptPubKey where the
// 20-byte "hash160" field carries arbitrary memo bytes (used by MemoToScripts
// to split memos >80 bytes into dust-funded overflow outputs). On Dash (no
// SegWit), this shape is always standard under IsStandardTx — unlike the
// OP_0 <hash> "P2WPKH" shape used by DOGE/BCH, which resolves to
// TxoutType::NONSTANDARD on dashd 23 (Dash's TxoutType enum has no
// WITNESS_V0_KEYHASH variant). The output is unspendable in practice because
// no one has a private key whose hash160 equals random memo bytes, so the
// attached dust is effectively burned — same end state as the DOGE/BCH flow.
func payToPubKeyHashMemoScript(hash []byte) ([]byte, error) {
	address, err := btcutil.NewAddressPubKeyHash(hash, &chaincfg.MainNetParams)
	if err != nil {
		return nil, err
	}
	return txscript.PayToAddrScript(address)
}

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
		signature, err = dashutil.TSSSignature(raw)
		if err != nil {
			return nil, err
		}
	}
	return dashutil.SignatureScript(digest, signature, pubKey)
}

// Dash signing must stop well before a prolonged ChainLock stall can lead to
// outbound reassignment. Allow one block of lag and one expected block interval
// of height-data age, rather than the shared scanner's longer MaxHealthyLag.
const dashMaxHealthyBlockLag int64 = 1

// chainLockHeightHealth is immutable once published; the scanner and signer read it
// concurrently through Client.chainLockHeightHealth.
type chainLockHeightHealth struct {
	blockHeight     int64
	chainLockHeight int64
	checkedAt       time.Time
}

// isHealthy reports whether height data is fresh and both ChainLock and scanner
// lag are within the Dash signing limit.
func (h *chainLockHeightHealth) isHealthy(now time.Time, scannedHeight int64) bool {
	maxAge := time.Duration(dashMaxHealthyBlockLag*common.DASHChain.ApproximateBlockMilliseconds()) * time.Millisecond
	if h == nil || h.checkedAt.IsZero() || now.Sub(h.checkedAt) >= maxAge {
		return false
	}
	// Check finality even when the scanner is caught up to a frozen ChainLock.
	// Also bound the unscanned backlog so a new ChainLock cannot immediately
	// restore signing using the scanner's old healthy flag before catch-up.
	return h.blockHeight >= h.chainLockHeight &&
		h.blockHeight-h.chainLockHeight <= dashMaxHealthyBlockLag &&
		h.blockHeight-scannedHeight <= dashMaxHealthyBlockLag
}

// isTransactionQuorumLocked checks daemon lock status for balance reporting.
// The cache belongs to one operation, so a previously unlocked tx is refreshed
// on the next call and multiple outputs of one tx need only one RPC lookup.
func (c *Client) isTransactionQuorumLocked(txID string, cache map[string]bool) (bool, error) {
	key := strings.ToLower(txID)
	if final, found := cache[key]; found {
		return final, nil
	}
	tx, err := c.rpc.GetRawTransactionVerbose(txID)
	if err != nil {
		return false, fmt.Errorf("fail to get transaction locks for %s: %w", txID, err)
	}
	if !strings.EqualFold(tx.Txid, txID) {
		return false, fmt.Errorf("finality txid mismatch: got %q, want %q", tx.Txid, txID)
	}
	cache[key] = tx.QuorumLocked()
	return cache[key], nil
}
