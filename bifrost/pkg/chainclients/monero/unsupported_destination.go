package monero

import (
	"errors"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

// CanSkipUnsupportedXMRDestination permits omission of a fresh, unsigned local
// attempt only. It neither completes the on-chain obligation nor deletes any
// signing evidence. The signer must preserve an existing queue entry on replay.
// Admission uses a fixed number of indexed reads, without a sidecar/RPC call or
// a scan of earlier unsupported payouts.
func (c *Client) CanSkipUnsupportedXMRDestination(tx stypes.TxOutItem) (bool, error) {
	if !tx.Chain.Equals(common.XMRChain) || len(tx.Checkpoint) != 0 ||
		!tx.ToAddress.IsChain(common.XMRChain) || common.IsSupportedXMRAddress(tx.ToAddress.String()) ||
		isConsolidationTx(tx) {
		return false, nil
	}
	if c.sidecar == nil || c.tssServer == nil || c.signedTxPayloadStore == nil || c.signedTxPayloadStore.db == nil || c.signerCacheManager == nil {
		return false, errors.New("XMR admission recovery stores are unavailable")
	}
	identity, err := xmrSigningCacheIdentity(tx)
	if err != nil {
		return false, err
	}
	if c.signerCacheManager.HasSigned(identity.CacheHash) {
		return false, nil
	}
	store := c.signedTxPayloadStore
	store.mu.Lock()
	defer store.mu.Unlock()
	keys := []string{
		signedTxPayloadKey(txCacheVault(tx, common.XMRChain), identity.CacheHash),
		signedTxPayloadClaimKey(identity.LogicalHash),
		signedTxCommittedLogicalKey(identity.LogicalHash),
		signedTxObservedLogicalKey(identity.LogicalHash),
		signedTxQuarantinedLogicalKey(identity.LogicalHash),
		signedTxBroadcastAttemptKey(identity.LogicalHash),
	}
	for _, alias := range identity.ObserveKeys {
		keys = append(keys, signedTxPayloadObserveKey(alias), signedTxCommittedAliasKey(alias),
			signedTxQuarantinedAliasKey(alias), signedTxObserveEpochKey(alias))
	}
	for _, key := range keys {
		found, readErr := store.db.Has([]byte(key), nil)
		if readErr != nil || found {
			return false, readErr
		}
	}
	return true, nil
}
