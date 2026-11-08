package thorchain

import (
	"context"
	"strings"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// RagnarokHandler process MsgRagnarok
type RagnarokHandler struct {
	mgr Manager
}

// NewRagnarokHandler create a new instance of RagnarokHandler
func NewRagnarokHandler(mgr Manager) RagnarokHandler {
	return RagnarokHandler{
		mgr: mgr,
	}
}

// Run is the main entry point of ragnarok handler
func (h RagnarokHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgRagnarok)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgRagnarok failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgRagnarok", "error", err)
	}
	return result, err
}

func (h RagnarokHandler) validate(ctx cosmos.Context, msg MsgRagnarok) error {
	return msg.ValidateBasic()
}

func (h RagnarokHandler) slash(ctx cosmos.Context, tx ObservedTx) error {
	toSlash := make(common.Coins, len(tx.Tx.Coins))
	copy(toSlash, tx.Tx.Coins)
	toSlash = toSlash.Add(tx.Tx.Gas.ToCoins()...)

	ctx = ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
		telemetry.NewLabel("reason", "failed_ragnarok"),
		telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
	}))

	return h.mgr.Slasher().SlashVault(ctx, tx.ObservedPubKey, toSlash, h.mgr)
}

func (h RagnarokHandler) handle(ctx cosmos.Context, msg MsgRagnarok) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgRagnarok", "request tx hash", msg.Tx.Tx.ID)

	// for ragnarok on thorchain ,
	if msg.Tx.Tx.Chain.Equals(common.THORChain) {
		return &cosmos.Result{}, nil
	}
	shouldSlash := true
	// follow the heights LackSigning reschedules the ragnarok outbound to: each
	// retry is due one signing period of wall-clock time after the previous one
	// (the extended period for XMR, EVM aggregator and EVM token outbounds), and
	// is queued at that block rounded up to RescheduleCoalesceBlocks, then moved
	// by any later block time change
	refMs := constants.ThorchainBlockTime.Milliseconds()
	// the period LackSigning reads, mimir included, in reference blocks so it
	// measures wall-clock time
	refPeriod := h.mgr.Keeper().GetConfigInt64Ref(ctx, constants.SigningTransactionPeriod)
	if refPeriod <= 0 {
		// LackSigning reschedules nothing then; a positive step keeps the walk
		// moving up to the current block
		// analyze-ignore(unscaled-config): a wall-clock period, in reference blocks
		refPeriod = h.mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	}
	periodMs := refPeriod * refMs * observedSigningPeriodMultiplier(msg.Tx)
	history := h.mgr.Keeper().GetBlockTimeHistory(ctx)
	// read once the walk passes the first height, as LackSigning reads it
	var coalesce int64
	coalesceRead := false
	nextRetry := func(height int64) (due, retry int64) {
		if !coalesceRead {
			coalesce = h.mgr.Keeper().GetConfigInt64Ref(ctx, constants.RescheduleCoalesceBlocks)
			coalesceRead = true
		}
		return history.RescheduleHeight(height, periodMs, coalesce, refMs)
	}
	// The computed retry height assumes the current RescheduleCoalesceBlocks and
	// LackSigning's schedule. A retry queued under an earlier coalesce interval,
	// or moved by a SOL durable nonce reissue, sits elsewhere. Every copy is
	// queued above the one it replaces, so when the computed height holds no copy
	// of this outbound, scan up from the last copy to the next one. After a scan
	// finds none, fall back to the computed heights alone.
	//
	// DiscoverOutbounds can split a payout across vaults into parts with the
	// same chain, destination, asset and memo, so isRagnarokCopy cannot tell
	// them apart, and each part is retried on its own. The copy at the computed
	// height may then be another part's, with this part's next copy below it.
	// Once a record shows more than one copy, visit every copy in height order.
	ragnarokMemo := NewRagnarokMemo(msg.BlockHeight).String()
	copies := func(txOut *TxOut) int {
		n := 0
		for _, tx := range txOut.TxArray {
			if isRagnarokCopy(tx, msg.Tx.Tx, ragnarokMemo) {
				n++
			}
		}
		return n
	}
	isCopy := func(txOut *TxOut) bool { return copies(txOut) > 0 }
	split := false
	scan := true
	decrementedPendingRagnarok := false
	// a retry is searched once it is due, though it can be queued ahead of the
	// current block: an observation of the outbound it replaces can still land
	height, due := msg.BlockHeight, msg.BlockHeight
	var next *TxOut
	for due <= ctx.BlockHeight() {
		txOut := next
		next = nil
		var err error
		if txOut == nil {
			if txOut, err = h.mgr.Keeper().GetTxOut(ctx, height); err != nil {
				return nil, ErrInternal(err, "unable to get txOut record")
			}
		}
		// update txOut record with our TxID that sent funds out of the pool
		for i, tx := range txOut.TxArray {
			// ragnarok is the memo used by thorchain to identify fund returns to
			// bonders, LPs, and reserve contributors.
			// it use ragnarok:{block height} to mark a tx out caused by the ragnarok protocol
			// this type of tx out is special, because it doesn't have relevant tx
			// in to trigger it, it is trigger by thorchain itself.

			var fromAddress common.Address
			sourcePubKey, sourceErr := txOutSourcePubKey(tx)
			if sourceErr != nil {
				err = sourceErr
			} else {
				fromAddress, err = sourcePubKey.GetAddress(tx.Chain)
			}
			if err != nil {
				ctx.Logger().Error(
					"fail to get from address from vault pubkey",
					"error", err,
					"vault_pubkey", tx.VaultPubKey,
					"vault_pubkey_eddsa", tx.VaultPubKeyEddsa,
					"chain", tx.Chain,
				)
				continue
			}

			if tx.InHash.Equals(common.BlankTxID) &&
				tx.OutHash.IsEmpty() &&
				common.EquivalentAddresses(tx.Chain, tx.ToAddress, msg.Tx.Tx.ToAddress) &&
				fromAddress.Equals(msg.Tx.Tx.FromAddress) {

				matchCoin := msg.Tx.Tx.Coins.EqualsEx(common.Coins{tx.Coin})
				// when outbound is gas asset
				if !matchCoin && tx.Coin.Asset.Equals(tx.Chain.GetGasAsset()) {
					asset := tx.Chain.GetGasAsset()
					intendToSpend := tx.Coin.Amount.Add(tx.MaxGas.ToCoins().GetCoin(asset).Amount)
					actualSpend := msg.Tx.Tx.Coins.GetCoin(asset).Amount.Add(msg.Tx.Tx.Gas.ToCoins().GetCoin(asset).Amount)
					if intendToSpend.Equal(actualSpend) {
						maxGasAmt := tx.MaxGas.ToCoins().GetCoin(asset).Amount
						realGasAmt := msg.Tx.Tx.Gas.ToCoins().GetCoin(asset).Amount
						if maxGasAmt.GTE(realGasAmt) {
							matchCoin = true
							ctx.Logger().Info("override match coin", "intend to spend", intendToSpend, "actual spend", actualSpend, "max gas", maxGasAmt, "actual gas", realGasAmt)
						}
						// the network didn't charge fee when it is ragnarok , thus it doesn't need to adjust gas
					}
				}
				if !matchCoin {
					continue
				}
				txOut.TxArray[i].OutHash = msg.Tx.Tx.ID
				shouldSlash = false
				if err := h.mgr.Keeper().SetTxOut(ctx, txOut); nil != err {
					return nil, ErrInternal(err, "fail to save tx out")
				}
				if !decrementedPendingRagnarok {
					pending, err := h.mgr.Keeper().GetRagnarokPending(ctx)
					if err != nil {
						ctx.Logger().Error("fail to get ragnarok pending", "error", err)
					} else {
						if pending > 0 {
							h.mgr.Keeper().SetRagnarokPending(ctx, pending-1)
						}
						ctx.Logger().Info("remaining ragnarok transaction", "count", pending-1)
					}
					decrementedPendingRagnarok = true
				}
				break

			}
		}
		// If the TxOutItem matching the observed outbound has been found,
		// do not check other block heights.
		if !shouldSlash {
			break
		}
		split = split || copies(txOut) > 1

		nextDue, retry := nextRetry(height)
		searchable := nextDue <= ctx.BlockHeight()
		if !scan {
			if !searchable {
				break
			}
			height, due = retry, nextDue
			continue
		}
		if !searchable && !msg.Tx.Tx.Chain.Equals(common.SOLChain) {
			// LackSigning queues a copy only once the outbound falls due; only a
			// SOL durable nonce reissue queues one earlier (reissueSolDurableAt)
			break
		}
		var retryOut *TxOut
		if searchable {
			out, getErr := h.mgr.Keeper().GetTxOut(ctx, retry)
			if getErr != nil {
				return nil, ErrInternal(getErr, "unable to get txOut record")
			}
			retryOut = out
			if !split && isCopy(retryOut) {
				height, due, next = retry, nextDue, retryOut
				continue
			}
		}
		// Scan up to the furthest height a copy can be queued at by now, and,
		// once the outbound is due again, to one signing period past that: the
		// copy LackSigning queued then lies at most one coalesce interval later,
		// assumed to be no longer than a signing period.
		limit := rescheduleReach(ctx, h.mgr.Keeper())
		if searchable {
			limit = max(limit, retry, history.HeightAfter(nextDue, periodMs, refMs)-1)
		}
		var found *TxOut
		foundAt := int64(0)
		for hgt := height + 1; hgt <= limit; hgt++ {
			candidate := retryOut
			if !searchable || hgt != retry {
				var err error
				if candidate, err = h.mgr.Keeper().GetTxOut(ctx, hgt); err != nil {
					return nil, ErrInternal(err, "unable to get txOut record")
				}
			}
			if isCopy(candidate) {
				found, foundAt = candidate, hgt
				break
			}
		}
		switch {
		case found != nil:
			// a persisted copy is searched whatever its due block
			height, due, next = foundAt, min(nextDue, ctx.BlockHeight()), found
		case searchable:
			// no copy where one can be: keep to the computed heights
			scan = false
			height, due, next = retry, nextDue, retryOut
		default:
			// not due again, and no copy queued since
			due = nextDue
		}
	}

	if shouldSlash {
		if err := h.slash(ctx, msg.Tx); err != nil {
			return nil, ErrInternal(err, "fail to slash account")
		}
	}

	if err := h.mgr.Keeper().SetLastSignedHeight(ctx, msg.BlockHeight); err != nil {
		ctx.Logger().Info("fail to update last signed height", "error", err)
	}

	return &cosmos.Result{}, nil
}

// isRagnarokCopy reports whether item is a copy, live or replaced, of the
// ragnarok outbound tx pays: LackSigning and SOL durable nonce reissue keep the
// chain, destination, coin asset and RAGNAROK memo of the item they replace,
// though they can change its vault.
func isRagnarokCopy(item TxOutItem, tx common.Tx, ragnarokMemo string) bool {
	return item.InHash.Equals(common.BlankTxID) &&
		item.Chain.Equals(tx.Chain) &&
		strings.EqualFold(item.GetMemo(), ragnarokMemo) &&
		common.EquivalentAddresses(item.Chain, item.ToAddress, tx.ToAddress) &&
		!tx.Coins.GetCoin(item.Coin.Asset).IsEmpty()
}
