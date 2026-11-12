package tron

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
)

// recordAcceptedBroadcast runs under the signer's LockTxOut boundary. Every
// production TRON signer-cache writer participates in that boundary. An older
// saved payload must not replace a newer payment record.
func (c *TronClient) recordAcceptedBroadcast(txOutItem types.TxOutItem, txID string) error {
	cacheHash := txOutItem.CacheHash()
	if c.signerCacheManager.HasSigned(cacheHash) {
		previous, ok := c.signerCacheManager.GetSignedTxHash(cacheHash)
		if !ok {
			return fmt.Errorf("TRON broadcast conflicts with a protected signer-cache record; reconciliation required")
		}
		if strings.EqualFold(previous, txID) {
			// Duplicate acceptance cannot extend a failed attempt's recovery gate.
			return nil
		}
		// SignTx may already have proved previous failed in this lock section.
		// Repeating the lookup adds nothing but a transient RPC error.
		if !c.isVerifiedFailed(cacheHash, previous) && c.verifyCachedTransaction(previous) != signercache.LookupFailed {
			// Neither acceptance nor an unavailable receipt orders these two
			// exposed transactions. Keep the existing identity and make the
			// cache non-expiring: otherwise its later failure could authorize a
			// third attempt while the untracked accepted transaction has paid.
			if err := c.signerCacheManager.ProtectSigned(cacheHash); err != nil {
				return fmt.Errorf("failed to protect conflicting TRON broadcasts: %w", err)
			}
			return fmt.Errorf("TRON accepted broadcast %s conflicts with cached %s; reconciliation required", txID, previous)
		}
	}
	return c.signerCacheManager.SetSignedWithExpiry(
		cacheHash,
		txOutItem.CacheVault(c.GetChain()),
		txID,
		time.Now().Add(c.getSignerCacheExpiry()).UnixMilli(),
	)
}

// isTronTxID reports whether sig has the form of a TRON transaction ID.
func isTronTxID(sig string) bool {
	hash, err := hex.DecodeString(sig)
	return err == nil && len(hash) == sha256.Size
}

// classifySolidReceipt classifies one solidity-API receipt lookup for the cached
// attempt sig. Only a receipt bound to sig at a positive block height proves
// anything: SUCCESS is Found and a recognized execution failure is Failed.
// Everything else is Ambiguous, including {} for a transaction that is pending,
// unknown or outside the node's history: an empty lookup never proves absence,
// so the result is never NotFound. readFailed reports that the lookup itself
// failed rather than answering without proof, so a caller that asks several
// endpoints can tell an unreachable endpoint from an unproven one.
func classifySolidReceipt(sig string, info api.TransactionInfo, err error) (_ signercache.LookupResult, readFailed bool) {
	if err != nil {
		return signercache.LookupAmbiguous, true
	}
	if !strings.EqualFold(info.ID, sig) || info.BlockNumber <= 0 {
		return signercache.LookupAmbiguous, false
	}
	if info.Receipt.Result == "SUCCESS" {
		return signercache.LookupFound, false
	}
	if info.Result != "FAILED" {
		return signercache.LookupAmbiguous, false
	}
	// Explicit failures only: absent receipts, DEFAULT, UNKNOWN and future
	// execution results are not proof that no payment was made.
	switch info.Receipt.Result {
	case "REVERT", "BAD_JUMP_DESTINATION", "OUT_OF_MEMORY", "PRECOMPILED_CONTRACT",
		"STACK_TOO_SMALL", "STACK_TOO_LARGE", "ILLEGAL_OPERATION", "STACK_OVERFLOW",
		"OUT_OF_ENERGY", "OUT_OF_TIME", "JVM_STACK_OVER_FLOW", "TRANSFER_FAILED", "INVALID_CODE":
		return signercache.LookupFailed, false
	default:
		return signercache.LookupAmbiguous, false
	}
}
