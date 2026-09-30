package utxo

import (
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcjson"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

////////////////////////////////////////////////////////////////////////////////////////
// Pending Vault Transaction Watch
//
// Vault outbounds are finalised on THORChain at mempool stage. If such a
// transaction is invalidated before it mines (a conflicting transaction mines
// and spends one of its inputs), THORChain still records the outbound as paid
// even though it can never land. To recover, the chain client watches every
// vault-originated transaction it reported until the transaction mines: a
// watched transaction that is not mined while one of its inputs is spent
// on-chain can never be mined, so the client attests an errata for it (and for
// any watched descendants, whose inputs can likewise never exist) so THORChain
// can re-credit the vault and re-queue the outbound.
////////////////////////////////////////////////////////////////////////////////////////

// trackPendingVaultTx records a vault-originated transaction reported to
// THORChain so it can be watched until it mines. Transactions already mined
// need no watch. Tracking is idempotent.
func (c *Client) trackPendingVaultTx(txid string) {
	// skip already tracked transactions before hitting the daemon
	if tracked, err := c.temporalStorage.HasPendingVaultTx(txid); err != nil {
		c.log.Err(err).Str("txid", txid).Msg("fail to check pending vault tx tracking")
		return
	} else if tracked {
		return
	}

	raw, err := c.rpc.GetRawTransactionVerbose(txid)
	if err != nil {
		// the transaction may not be visible to the local daemon (e.g. it was
		// only ever in peer mempools); without its inputs no watch is possible
		c.log.Debug().Err(err).Str("txid", txid).Msg("unable to track pending vault tx")
		return
	}
	if raw.BlockHash != "" {
		// already mined, nothing to watch
		return
	}

	vins := make([]utxo.PendingVaultTxOutpoint, 0, len(raw.Vin))
	for _, vin := range raw.Vin {
		if vin.IsCoinBase() {
			continue
		}
		vins = append(vins, utxo.PendingVaultTxOutpoint{TxID: vin.Txid, Vout: vin.Vout})
	}
	if len(vins) == 0 {
		return
	}

	pending := utxo.PendingVaultTx{
		TxID:            txid,
		Vins:            vins,
		FirstSeenHeight: c.getCurrentBlockHeight(),
	}
	if err := c.temporalStorage.TrackPendingVaultTx(pending); err != nil {
		c.log.Err(err).Str("txid", txid).Msg("fail to track pending vault tx")
		return
	}
	c.log.Debug().Str("txid", txid).Msg("tracking pending vault tx")
}

// checkPendingVaultTxs resolves every watched vault transaction against the
// latest block: mined transactions are cleared, and transactions proven to be
// invalidated (not mined while an input is spent on-chain, or descended from
// one) are reported to the errata queue so THORChain can re-queue them.
// It must never fail block processing; all errors are logged and retried on
// the next block.
func (c *Client) checkPendingVaultTxs(block *btcjson.GetBlockVerboseTxResult) {
	pending, err := c.temporalStorage.GetPendingVaultTxs()
	if err != nil {
		c.log.Err(err).Msg("fail to get pending vault txs")
		return
	}
	if len(pending) == 0 {
		return
	}

	byID := make(map[string]utxo.PendingVaultTx, len(pending))
	for _, tx := range pending {
		byID[tx.TxID] = tx
	}

	// transactions mined in this block need no further watching
	inBlock := make(map[string]bool, len(block.Tx))
	for i := range block.Tx {
		inBlock[block.Tx[i].Txid] = true
	}
	for _, tx := range pending {
		if inBlock[tx.TxID] {
			c.untrackPendingVaultTx(tx.TxID)
			delete(byID, tx.TxID)
		}
	}
	if len(byID) == 0 {
		return
	}

	// classify the remaining transactions against the daemon: a transaction
	// still in the mempool is viable (the daemon evicts it when a conflicting
	// transaction mines), a mined one is cleared, and only one the daemon can
	// no longer find needs its inputs checked.
	unknown := make(map[string]utxo.PendingVaultTx, len(byID))
	for _, tx := range byID {
		raw, err := c.rpc.GetRawTransactionVerbose(tx.TxID)
		switch {
		case err == nil && raw.BlockHash != "":
			// mined while the scanner was not watching (e.g. downtime)
			c.untrackPendingVaultTx(tx.TxID)
		case err == nil:
			// still in the mempool, still viable
		case isRPCTxNotFound(err):
			unknown[tx.TxID] = tx
		default:
			// transient daemon errors must not be mistaken for invalidation
			c.log.Err(err).Str("txid", tx.TxID).Msg("fail to check pending vault tx")
		}
	}
	if len(unknown) == 0 {
		return
	}

	// A transaction the daemon can no longer find is invalidated when one of
	// its inputs is spent on-chain: the input's parent is mined and the output
	// is no longer in the UTXO set, so a conflicting transaction spent it and
	// this transaction can never land. Inputs of unconfirmed parents are left
	// alone here - those descendants are caught by the cascade below when the
	// parent is proven invalid.
	if c.pendingVaultTxCheckUnsupported.Load() {
		return
	}
	invalidated := make(map[string]bool)
	for _, tx := range unknown {
		bad, err := c.pendingVaultTxInvalidated(tx, byID)
		if err != nil {
			if isRPCMethodNotFound(err) {
				c.pendingVaultTxCheckUnsupported.Store(true)
				c.log.Warn().Msg("daemon does not support gettxout; pending vault tx invalidation detection disabled")
				return
			}
			c.log.Err(err).Str("txid", tx.TxID).Msg("fail to check pending vault tx inputs")
			continue
		}
		if bad {
			invalidated[tx.TxID] = true
		}
	}

	// cascade: a watched transaction spending an output of an invalidated
	// transaction can never be mined either, since its input will never exist
	if len(invalidated) > 0 {
		for {
			progress := false
			for txid, tx := range byID {
				if invalidated[txid] {
					continue
				}
				for _, vin := range tx.Vins {
					if invalidated[vin.TxID] {
						invalidated[txid] = true
						progress = true
						break
					}
				}
			}
			if !progress {
				break
			}
		}
	}

	for txid := range invalidated {
		c.log.Warn().Str("txid", txid).Msg("pending vault tx invalidated before mining, attesting errata")
		select {
		case c.globalErrataQueue <- types.ErrataBlock{
			Height: block.Height,
			Txs: []types.ErrataTx{{
				TxID:  common.TxID(txid),
				Chain: c.cfg.ChainID,
			}},
		}:
			c.untrackPendingVaultTx(txid)
		default:
			// queue full: keep watching and retry on the next block
			c.log.Error().Str("txid", txid).Msg("errata queue full, will retry pending vault tx invalidation")
		}
	}
}

// pendingVaultTxInvalidated reports whether the transaction is proven to be
// invalidated: not mined, while one of its inputs was spent by a mined
// (conflicting) transaction. Inputs whose parent is itself an unconfirmed
// watched transaction are skipped here and resolved by the caller's cascade.
func (c *Client) pendingVaultTxInvalidated(tx utxo.PendingVaultTx, watched map[string]utxo.PendingVaultTx) (bool, error) {
	for _, vin := range tx.Vins {
		out, err := c.rpc.GetTxOut(vin.TxID, vin.Vout, false)
		if err != nil {
			return false, fmt.Errorf("fail to get txout %s:%d: %w", vin.TxID, vin.Vout, err)
		}
		if out != nil {
			// confirmed and unspent
			continue
		}
		// The output is not in the confirmed UTXO set. If the parent is mined,
		// a conflicting transaction spent the input and this transaction is
		// invalidated. If the parent is unconfirmed, the input simply has not
		// appeared on-chain yet.
		parent, err := c.rpc.GetRawTransactionVerbose(vin.TxID)
		switch {
		case err == nil && parent.BlockHash != "":
			return true, nil
		case err == nil:
			// parent still in mempool, input pending
			continue
		case isRPCTxNotFound(err):
			// The parent is not mined and not in the local mempool. If it is a
			// watched vault transaction its fate is resolved by the caller's
			// cascade; otherwise assume the input can still appear.
			if _, ok := watched[vin.TxID]; ok {
				continue
			}
			c.log.Info().Str("txid", tx.TxID).Str("parent", vin.TxID).
				Msg("pending vault tx input parent not found and not tracked, skipping invalidation check")
		default:
			return false, fmt.Errorf("fail to get parent %s: %w", vin.TxID, err)
		}
	}
	return false, nil
}

func (c *Client) untrackPendingVaultTx(txid string) {
	if err := c.temporalStorage.UntrackPendingVaultTx(txid); err != nil {
		c.log.Err(err).Str("txid", txid).Msg("fail to untrack pending vault tx")
	}
}

// isRPCTxNotFound reports whether the error is the daemon's "no such
// transaction" response (-5), meaning the transaction is neither mined nor in
// the local mempool.
func isRPCTxNotFound(err error) bool {
	var rpcErr *btcjson.RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr.Code == btcjson.ErrRPCNoTxInfo
	}
	return false
}

// isRPCMethodNotFound reports whether the error is the daemon's "method not
// found" response (-32601), e.g. gettxout on Zebra.
func isRPCMethodNotFound(err error) bool {
	var rpcErr *btcjson.RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr.Code == btcjson.RPCErrorCode(-32601)
	}
	return false
}
