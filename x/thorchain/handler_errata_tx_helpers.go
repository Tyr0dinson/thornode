package thorchain

import (
	"context"
	"fmt"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

func processErrataTxAttestation(
	ctx cosmos.Context,
	mgr Manager,
	voter *keeper.ErrataTxVoter,
	attester cosmos.AccAddress,
	active NodeAccounts,
	er *common.ErrataTx,
	shouldSlashForDuplicate bool,
) error {
	k := mgr.Keeper()

	observeSlashPoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	lackOfObservationPenalty := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observeFlex := k.GetConfigInt64(ctx, constants.ObservationDelayFlexibility)

	slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{ // nolint
		telemetry.NewLabel("reason", "failed_observe_errata"),
		telemetry.NewLabel("chain", telemChain(er.Chain)),
	}))

	slasher := mgr.Slasher()

	if !voter.Sign(attester) {
		// Slash for the network having to handle the extra message/s.
		if shouldSlashForDuplicate {
			slasher.IncSlashPoints(slashCtx, observeSlashPoints, attester)
		}
		ctx.Logger().Info("signer already signed MsgErrataTx", "signer", attester.String(), "txid", er.Id)
		return nil
	}

	// doesn't have consensus yet
	if !voter.HasConsensus(active) {
		// Before consensus, slash until consensus.
		slasher.IncSlashPoints(slashCtx, observeSlashPoints, attester)
		ctx.Logger().Info("not having consensus yet, return")
		return nil
	}

	if voter.BlockHeight > 0 {
		// After consensus, only decrement slash points if within the ObservationDelayFlexibility period.
		if (voter.BlockHeight + observeFlex) >= ctx.BlockHeight() {
			slasher.DecSlashPoints(slashCtx, lackOfObservationPenalty, attester)
		}
		// errata tx already processed
		return nil
	}

	voter.BlockHeight = ctx.BlockHeight()

	// An errata reaching consensus means a tx the network already acted on was
	// reorged out of the source chain. Every path above returns early (duplicate
	// signer, pre-consensus, already-processed), so this fires once per errata.
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "errata", "consensus"},
		1,
		[]metrics.Label{telemetry.NewLabel("chain", telemChain(er.Chain))},
	)

	// This signer brings the voter to consensus; increment the signer's slash points like the before-consensus signers,
	// then decrement all the signers' slash points and increment the non-signers' slash points.
	slasher.IncSlashPoints(slashCtx, observeSlashPoints, attester)
	signers := voter.GetSigners()
	nonSigners := getNonSigners(active, signers)
	slasher.DecSlashPoints(slashCtx, observeSlashPoints, signers...)
	slasher.IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)

	observedVoter, err := k.GetObservedTxInVoter(ctx, er.Id)
	if err != nil {
		return err
	}

	if len(observedVoter.Txs) == 0 {
		return processErrataOutboundTx(ctx, mgr, er)
	}
	if observedVoter.Tx.IsEmpty() {
		ctx.Logger().Info("tx has not reach consensus yet, so nothing need to be done", "tx_id", er.Id)
		return nil
	}

	tx := observedVoter.Tx.Tx
	if !tx.Chain.Equals(er.Chain) {
		// does not match chain
		return nil
	}

	// set the observed Tx to reverted only after chain validation passes
	observedVoter.SetReverted()
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "errata", "reverted"},
		1,
		[]metrics.Label{
			telemetry.NewLabel("chain", telemChain(er.Chain)),
			telemetry.NewLabel("direction", "in"),
		},
	)
	k.SetObservedTxInVoter(ctx, observedVoter)
	if observedVoter.UpdatedVault {
		vaultPubKey := observedVoter.Tx.ObservedPubKey
		if !vaultPubKey.IsEmpty() {
			// try to deduct the asset from asgard
			var vault Vault
			vault, err = k.GetVault(ctx, vaultPubKey)
			if err != nil {
				return fmt.Errorf("fail to get active asgard vaults: %w", err)
			}
			debitVault(ctx, mgr, &vault, observedVoter.Tx, tx.Coins, vaultDeficitReasonInboundErrataReversal)
			if err = k.SetVault(ctx, vault); err != nil {
				return fmt.Errorf("fail to save vault, err: %w", err)
			}
		}
	}

	if !observedVoter.Tx.IsFinal() {
		ctx.Logger().Info("tx is not finalised, so nothing need to be done", "tx_id", er.Id)
		return nil
	}

	memo, _ := ParseMemoWithTHORNames(ctx, k, tx.Memo)
	// if the tx is a migration , from old valut to new vault , then the inbound tx must have a related outbound tx as well
	if memo.IsInternal() {
		return processErrataOutboundTx(ctx, mgr, er)
	}

	if !memo.IsType(TxSwap) && !memo.IsType(TxAdd) {
		// must be a swap or add transaction
		return nil
	}

	runeCoin := common.NoCoin
	assetCoin := common.NoCoin
	for _, coin := range tx.Coins {
		if coin.IsRune() {
			runeCoin = coin
		} else {
			assetCoin = coin
		}
	}

	// fetch pool from memo
	pool, err := k.GetPool(ctx, assetCoin.Asset)
	if err != nil {
		ctx.Logger().Error("fail to get pool for errata tx", "error", err)
		return err
	}

	// subtract amounts from pool balances
	if runeCoin.Amount.GT(pool.BalanceRune) {
		runeCoin.Amount = pool.BalanceRune
	}
	if assetCoin.Amount.GT(pool.BalanceAsset) {
		assetCoin.Amount = pool.BalanceAsset
	}
	pool.BalanceRune = common.SafeSub(pool.BalanceRune, runeCoin.Amount)
	pool.BalanceAsset = common.SafeSub(pool.BalanceAsset, assetCoin.Amount)
	if memo.IsType(TxAdd) {
		lp, err := k.GetLiquidityProvider(ctx, pool.Asset, tx.FromAddress)
		if err != nil {
			return fmt.Errorf("fail to get liquidity provider: %w", err)
		}

		// since this address is being malicious, zero their liquidity provider units
		pool.LPUnits = common.SafeSub(pool.LPUnits, lp.Units)
		lp.Units = cosmos.ZeroUint()
		lp.LastAddHeight = ctx.BlockHeight()

		k.SetLiquidityProvider(ctx, lp)
	}

	if err := k.SetPool(ctx, pool); err != nil {
		return fmt.Errorf("fail to save pool: %w", err)
	}

	// The errata has been applied: pool balances (and possibly LP units) were
	// unwound. SetPool above rejects an empty asset, so pool.Asset is a real
	// stored pool here and the label stays bounded by the pool set.
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "errata", "applied"},
		1,
		[]metrics.Label{
			telemetry.NewLabel("chain", telemChain(er.Chain)),
			telemetry.NewLabel("asset", pool.Asset.String()),
		},
	)

	// send errata event
	mods := PoolMods{
		NewPoolMod(pool.Asset, runeCoin.Amount, false, assetCoin.Amount, false),
	}

	eventErrata := NewEventErrata(er.Id, mods)
	if err := mgr.EventMgr().EmitEvent(ctx, eventErrata); err != nil {
		return ErrInternal(err, "fail to emit errata event")
	}
	return nil
}

// processErrataOutboundTx handles an errata for an outbound tx that was sent but
// later invalidated (reorged out, or double-spent by a conflicting mined tx).
// It re-credits funds to the vault so they are not abandoned.
// For UTXO chains the outbound is also re-queued so the payee is still paid:
// a UTXO outbound errata can only come from a hard invalidation (a conflicting
// transaction mined, so the recorded out tx can never land - the reorg errata
// path only covers inbound/customer transactions), which makes re-queue safe.
func processErrataOutboundTx(ctx cosmos.Context, mgr Manager, er *common.ErrataTx) error {
	k := mgr.Keeper()
	eventMgr := mgr.EventMgr()
	txOutVoter, err := k.GetObservedTxOutVoter(ctx, er.Id)
	if err != nil {
		return fmt.Errorf("fail to get observed tx out voter for tx (%s) : %w", er.Id, err)
	}
	if len(txOutVoter.Txs) == 0 {
		return fmt.Errorf("cannot find tx: %s", er.Id)
	}
	if txOutVoter.Tx.IsEmpty() {
		return fmt.Errorf("tx out voter is not finalised")
	}
	tx := txOutVoter.Tx.Tx
	if !tx.Chain.Equals(er.Chain) || tx.Coins.IsEmpty() {
		return nil
	}
	// parse the outbound tx memo, so we can figure out which inbound tx triggered the outbound
	m, err := ParseMemoWithTHORNames(ctx, k, tx.Memo)
	if err != nil {
		return fmt.Errorf("fail to parse memo(%s): %w", tx.Memo, err)
	}
	if !m.IsOutbound() && !m.IsInternal() {
		return fmt.Errorf("%s is not outbound or internal tx", m)
	}
	vaultPubKey := txOutVoter.Tx.ObservedPubKey
	if !vaultPubKey.IsEmpty() {
		v, err := k.GetVault(ctx, vaultPubKey)
		if err != nil {
			return fmt.Errorf("fail to get vault with pubkey %s: %w", vaultPubKey, err)
		}
		// Credit funds back to the vault so they are not lost.
		// Note: We intentionally do NOT change InactiveVault back to RetiringVault
		// as this could cause side effects (blocking churns, affecting node unbonding).
		// Recovery from InactiveVaults with funds should be handled via migration.
		v.AddFunds(tx.Coins)
		if v.Status == InactiveVault {
			ctx.Logger().Info("Errata credited funds to inactive vault - recovery via migration needed", "vault pub key", v.PubKey)
		}

		if !v.IsEmpty() {
			if err := k.SetVault(ctx, v); err != nil {
				return fmt.Errorf("fail to save vault: %w", err)
			}
		}
	}

	// emit security event
	event := NewEventSecurity(tx, "outbound errata")
	if err := eventMgr.EmitEvent(ctx, event); err != nil {
		return ErrInternal(err, "fail to emit security event")
	}
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "security", "event"},
		1,
		[]metrics.Label{
			telemetry.NewLabel("reason", "outbound_errata"),
			telemetry.NewLabel("chain", telemChain(er.Chain)),
		},
	)

	txOutVoter.SetReverted()
	// An outbound was reorged out and its funds re-credited to the vault above;
	// if that vault is already inactive the funds need a manual migration.
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "errata", "reverted"},
		1,
		[]metrics.Label{
			telemetry.NewLabel("chain", telemChain(er.Chain)),
			telemetry.NewLabel("direction", "out"),
		},
	)
	k.SetObservedTxOutVoter(ctx, txOutVoter)

	requeueInvalidatedOutbound(ctx, mgr, er, txOutVoter.Tx)
	return nil
}

// requeueInvalidatedOutbound schedules a fresh TxOutItem for a UTXO outbound
// whose recorded out tx was invalidated before it could mine, so the payee is
// still paid. Without this the outbound was finalised at mempool stage and
// would remain recorded as paid while never landing on-chain.
//
// The vault has already been re-credited for the invalidated tx, so the item
// is assigned back to that vault; its inputs are unspent again (or spent by a
// conflicting vault tx accounted under its own item) and it can pay from its
// current UTXO set. The original item keeps its OutHash as the historical
// record, and the inbound voter keeps the invalidated tx in OutTxs so
// LackSigning does not also reschedule the action.
func requeueInvalidatedOutbound(ctx cosmos.Context, mgr Manager, er *common.ErrataTx, observed common.ObservedTx) {
	k := mgr.Keeper()
	if !er.Chain.IsUTXO() {
		return
	}
	memo, err := ParseMemoWithTHORNames(ctx, k, observed.Tx.Memo)
	if err != nil || !memo.IsOutbound() {
		return
	}
	inHash := memo.GetTxID()
	if inHash.IsEmpty() || inHash.IsBlank() {
		return
	}

	item, ok := findTxOutItemByOutHash(ctx, k, inHash, er.Id)
	if !ok {
		ctx.Logger().Error("outbound errata: tx out item not found, not re-queuing",
			"in_hash", inHash, "out_hash", er.Id, "chain", er.Chain)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "outbound", "errata_requeue_failed"},
			1,
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(er.Chain)),
				telemetry.NewLabel("reason", "item_not_found"),
			},
		)
		return
	}

	// The invalidating (conflicting) transaction may itself be a vault payment
	// for this same outbound, e.g. a re-sign that reused the inputs. It was then
	// observed and recorded on the inbound voter; re-queuing would pay the payee
	// twice. The match is deliberately coarse (recipient and asset, not amount):
	// a skipped re-queue only falls back to the re-credit-only behavior, while
	// a wrong re-queue is a double payment.
	if paidBy, paid := outboundPaidByOtherTx(ctx, k, inHash, er.Id, item); paid {
		ctx.Logger().Info("outbound errata: payee already paid by another tx, not re-queuing",
			"in_hash", inHash, "out_hash", er.Id, "paid_by", paidBy, "chain", er.Chain)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "outbound", "errata_requeue_failed"},
			1,
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(er.Chain)),
				telemetry.NewLabel("reason", "paid_by_other_tx"),
			},
		)
		return
	}

	vault, err := k.GetVault(ctx, observed.ObservedPubKey)
	if err != nil || vault.PubKey.IsEmpty() {
		ctx.Logger().Error("outbound errata: fail to get observed vault, not re-queuing",
			"in_hash", inHash, "out_hash", er.Id, "error", err)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "outbound", "errata_requeue_failed"},
			1,
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(er.Chain)),
				telemetry.NewLabel("reason", "no_vault"),
			},
		)
		return
	}

	newToi := item
	newToi.OutHash = common.TxID("")
	// clout spent on this item was already reclaimed when the invalidated tx
	// was observed; the replacement must not reclaim it a second time
	newToi.CloutSpent = nil

	// refresh max gas and gas rate like a LackSigning reschedule, keeping the
	// in-voter actions in sync so future reschedules still match. The helpers
	// match actions by the item's original fields, so they run before the vault
	// and gas mutations below.
	if maxGasCoin, gasErr := mgr.GasMgr().GetMaxGas(ctx, newToi.Chain); gasErr != nil {
		ctx.Logger().Error("outbound errata: fail to get max gas", "error", gasErr)
	} else if !maxGasCoin.IsEmpty() {
		if err = updateTxOutGas(ctx, k, newToi, common.Gas{maxGasCoin}); err != nil {
			ctx.Logger().Error("outbound errata: fail to update MaxGas of action in ObservedTxVoter", "hash", newToi.InHash, "error", err)
		} else {
			newToi.MaxGas = common.Gas{maxGasCoin}
		}
	}
	gasRate := int64(mgr.GasMgr().GetGasRate(ctx, newToi.Chain).Uint64())
	if err = updateTxOutGasRate(ctx, k, newToi, gasRate); err != nil {
		ctx.Logger().Error("outbound errata: fail to update GasRate of action in ObservedTxVoter", "hash", newToi.InHash, "error", err)
	} else {
		newToi.GasRate = gasRate
	}

	newToi.VaultPubKey = vault.PubKey
	newToi.VaultPubKeyEddsa = txOutVaultPubKeyEddsa(newToi.Chain, vault.PubKeyEddsa)
	newToi.VaultPubKeyFrost = vault.PubKeyFrost

	// Apply memoless outbound logic before rescheduling (mirrors LackSigning)
	enableMemolessOutbound := k.GetConfigInt64(ctx, constants.EnableMemolessOutbound)
	applyMemolessOutboundLogic(mgr.GetVersion(), &newToi, enableMemolessOutbound)

	// round up to nearest multiple of RescheduleCoalesceBlocks
	rescheduleHeight := ctx.BlockHeight()
	if rescheduleCoalesceBlocks := k.GetConfigInt64(ctx, constants.RescheduleCoalesceBlocks); rescheduleCoalesceBlocks > 1 {
		if overBlocks := rescheduleHeight % rescheduleCoalesceBlocks; overBlocks != 0 {
			rescheduleHeight += rescheduleCoalesceBlocks - overBlocks
		}
	}

	if err = mgr.TxOutStore().UnSafeAddTxOutItem(ctx, mgr, newToi, rescheduleHeight); err != nil {
		ctx.Logger().Error("outbound errata: fail to re-queue outbound", "in_hash", inHash, "out_hash", er.Id, "error", err)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "outbound", "errata_requeue_failed"},
			1,
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(er.Chain)),
				telemetry.NewLabel("reason", "queue_add_failed"),
			},
		)
		return
	}

	ctx.Logger().Info("outbound errata: re-queued outbound",
		"in_hash", inHash,
		"out_hash", er.Id,
		"chain", er.Chain,
		"coin", newToi.Coin.String(),
		"to", newToi.ToAddress.String(),
		"vault", vault.PubKey.String(),
		"height", rescheduleHeight)
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "outbound", "errata_requeued"},
		1,
		[]metrics.Label{
			telemetry.NewLabel("chain", telemChain(er.Chain)),
			telemetry.NewLabel("asset", newToi.Coin.Asset.String()),
		},
	)
}

// findTxOutItemByOutHash locates the TxOutItem that recorded the given out tx
// hash for the given inbound. The item sits on the block it was scheduled at,
// which must precede the outbound observation; the search walks back from the
// observation's consensus height, bounded by one reschedule window per allowed
// outbound attempt plus slack.
func findTxOutItemByOutHash(ctx cosmos.Context, k keeper.Keeper, inHash, outHash common.TxID) (TxOutItem, bool) {
	txOutVoter, err := k.GetObservedTxOutVoter(ctx, outHash)
	if err != nil {
		ctx.Logger().Error("outbound errata: fail to get tx out voter", "out_hash", outHash, "error", err)
		return TxOutItem{}, false
	}
	startHeight := txOutVoter.Height
	if startHeight <= 0 || startHeight > ctx.BlockHeight() {
		startHeight = ctx.BlockHeight()
	}

	inVoter, err := k.GetObservedTxInVoter(ctx, inHash)
	if err != nil {
		ctx.Logger().Error("outbound errata: fail to get tx in voter", "in_hash", inHash, "error", err)
		return TxOutItem{}, false
	}
	lowerBound := inVoter.FinalisedHeight
	if lowerBound <= 0 {
		lowerBound = inVoter.Height
	}

	signingTransPeriod := k.GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	maxOutboundAttempts := k.GetConfigInt64(ctx, constants.MaxOutboundAttempts)
	if signingTransPeriod <= 0 {
		signingTransPeriod = 1
	}
	if maxOutboundAttempts <= 0 {
		maxOutboundAttempts = 1
	}
	if window := startHeight - (maxOutboundAttempts+2)*signingTransPeriod; window > lowerBound {
		lowerBound = window
	}
	if lowerBound < 1 {
		lowerBound = 1
	}

	for height := startHeight; height >= lowerBound; height-- {
		txOut, err := k.GetTxOut(ctx, height)
		if err != nil {
			ctx.Logger().Error("outbound errata: fail to get tx out", "height", height, "error", err)
			return TxOutItem{}, false
		}
		for _, item := range txOut.TxArray {
			if item.InHash.Equals(inHash) && item.OutHash.Equals(outHash) {
				return item, true
			}
		}
	}
	return TxOutItem{}, false
}

// outboundPaidByOtherTx reports whether the inbound voter records an observed
// outbound, other than the invalidated one, that pays the item's recipient in
// the item's asset and has not itself been reverted by an errata.
func outboundPaidByOtherTx(ctx cosmos.Context, k keeper.Keeper, inHash, invalidated common.TxID, item TxOutItem) (common.TxID, bool) {
	inVoter, err := k.GetObservedTxInVoter(ctx, inHash)
	if err != nil {
		// cannot rule out a prior payment: treat as paid, falling back to the
		// re-credit-only behavior
		ctx.Logger().Error("outbound errata: fail to get tx in voter", "in_hash", inHash, "error", err)
		return common.BlankTxID, true
	}
	for _, out := range inVoter.OutTxs {
		if out.ID.IsEmpty() || out.ID.Equals(invalidated) || !out.Chain.Equals(item.Chain) {
			continue
		}
		if !common.EquivalentAddresses(item.Chain, out.ToAddress, item.ToAddress) {
			continue
		}
		if out.Coins.GetCoin(item.Coin.Asset).IsEmpty() {
			continue
		}
		outVoter, err := k.GetObservedTxOutVoter(ctx, out.ID)
		if err != nil {
			ctx.Logger().Error("outbound errata: fail to get tx out voter", "out_hash", out.ID, "error", err)
			return out.ID, true
		}
		if outVoter.Reverted {
			continue
		}
		return out.ID, true
	}
	return common.BlankTxID, false
}
