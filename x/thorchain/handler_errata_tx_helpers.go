package thorchain

import (
	"context"
	"fmt"
	"strings"

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

	terminalOutcome := er.Chain.RequiresTerminalErrataOutcome()
	newSigner := voter.Sign(attester)
	if !newSigner {
		// Direct duplicate messages retain their historical extra-message slash.
		if shouldSlashForDuplicate {
			slasher.IncSlashPoints(slashCtx, observeSlashPoints, attester)
		}
		if shouldSlashForDuplicate || !terminalOutcome ||
			voter.Outcome != common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED {
			return nil
		}
	}

	// Evaluate once per call, against current membership. Duplicate-only quorum
	// retries can become sufficient after churn, but must not charge a vote twice.
	// For XMR, BlockHeight also closes the observe-charge window: a new signer
	// is not charged if churn has removed current quorum after settlement.
	// Non-XMR retains its historical charge in that case.
	if !voter.HasConsensus(active) {
		if newSigner && (!terminalOutcome || voter.BlockHeight == 0) {
			slasher.IncSlashPoints(slashCtx, observeSlashPoints, attester)
		}
		return nil
	}

	if voter.BlockHeight > 0 {
		if newSigner && (voter.BlockHeight+observeFlex) >= ctx.BlockHeight() {
			slasher.DecSlashPoints(slashCtx, lackOfObservationPenalty, attester)
		}
		if !terminalOutcome || voter.Outcome != common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED {
			return nil
		}
	} else {
		// BlockHeight settles observation accounting once, independently of a
		// pending observed transaction. Subsequent economic retries preserve it.
		voter.BlockHeight = ctx.BlockHeight()
		// Count the initial quorum settlement, not later retries while the
		// observed transaction is still pending economic completion.
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "errata", "consensus"},
			1,
			[]metrics.Label{telemetry.NewLabel("chain", telemChain(er.Chain))},
		)
		if newSigner {
			slasher.IncSlashPoints(slashCtx, observeSlashPoints, attester)
		}
		signers := voter.GetSigners()
		nonSigners := getNonSigners(active, signers)
		slasher.DecSlashPoints(slashCtx, observeSlashPoints, signers...)
		slasher.IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)
		queueObservationTally(ctx, k, observationTallyKindErrata, voter.Chain, voter.TxID.String())
	}

	outcome, err := processErrataTx(ctx, mgr, er)
	if err == nil && terminalOutcome {
		voter.Outcome = outcome
	}
	return err
}

// processErrataTx applies economic effects inside the caller's transaction
// cache. Errors must roll back voter, slash and balance writes together.
func processErrataTx(ctx cosmos.Context, mgr Manager, er *common.ErrataTx) (common.ErrataTxOutcome, error) {
	k := mgr.Keeper()
	eventMgr := mgr.EventMgr()

	observedVoter, err := k.GetObservedTxInVoter(ctx, er.Id)
	if err != nil {
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, err
	}

	if len(observedVoter.Txs) == 0 {
		return processErrataOutboundTx(ctx, k, eventMgr, er)
	}
	if observedVoter.Tx.IsEmpty() {
		// Known observations may still reach quorum. Preserve pending state;
		// the observer bounds automatic retries with its outbox retry horizon.
		ctx.Logger().Info("tx has not reach consensus yet, so nothing need to be done", "tx_id", er.Id)
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, nil
	}

	tx := observedVoter.Tx.Tx
	if !tx.Chain.Equals(er.Chain) {
		// does not match chain
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION, nil
	}

	// Already reverted: errata reversal is idempotent for every chain — a second
	// revert must not debit the vault or pending receipt again (private's
	// unconditional guard). For terminal-errata chains the inbound backstop can
	// still win between retries without restoring an already-observed outbound, so
	// finish that side too; its own Reverted guard prevents a second vault credit.
	if observedVoter.Reverted {
		if er.Chain.RequiresTerminalErrataOutcome() {
			_, outboundErr := processErrataOutboundTx(ctx, k, eventMgr, er)
			if outboundErr != nil {
				return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, outboundErr
			}
		}
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
	}
	if er.Chain.RequiresTerminalErrataOutcome() {
		// TxID-wide reversal still requires a valid canonical observation;
		// absent/malformed location must never be converted to a terminal no-op.
		if _, heightErr := observedVoter.Tx.CanonicalMinedHeight(); heightErr != nil {
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, fmt.Errorf("resolve inbound errata mined height: %w", heightErr)
		}
	}

	// Policy: confirmed custody loss is independent of whether an ADD's LP can
	// be reversed exactly. Debit the full observed coins once here; liquidity
	// accounting below removes exact pending claims or socializes through pool
	// balances. A pool cap must not cap this custody correction as well.
	// Set the observed Tx to reverted only after chain validation passes.
	// Preserve the successful-execution status for the scoped ADD penalty:
	// SetReverted overwrites it; absence of outbounds alone does not prove success.
	inboundBeforeErrata := observedVoter
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
	// The inbound path marks a self-send as updated without crediting it.
	if observedVoter.UpdatedVault && (!er.Chain.RequiresTerminalErrataOutcome() || !tx.FromAddress.Equals(tx.ToAddress)) {
		vaultPubKey := observedVoter.Tx.ObservedPubKey
		if !vaultPubKey.IsEmpty() {
			// try to deduct the asset from asgard
			var vault Vault
			vault, err = k.GetVault(ctx, vaultPubKey)
			if err != nil {
				return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, fmt.Errorf("fail to get active asgard vaults: %w", err)
			}
			debitVault(ctx, mgr, &vault, observedVoter.Tx, tx.Coins, vaultDeficitReasonInboundErrataReversal)
			if err = k.SetVault(ctx, vault); err != nil {
				return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, fmt.Errorf("fail to save vault, err: %w", err)
			}
		}
	}

	// A swap inbound may still be queued as a limit or streaming swap. Its coins
	// never arrived, so drop the queued swap without settling it: no outbound for
	// the filled output and no refund of the unswapped remainder. Other inbounds
	// can queue swaps funded from existing claims; those are left to settle.
	var reverted []MsgSwap
	if isSwapInboundMemo(tx.Memo) {
		reverted, err = removeRevertedAdvSwaps(ctx, mgr, er.Id)
		if err != nil {
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, err
		}
	}

	if !observedVoter.Tx.IsFinal() {
		ctx.Logger().Info("tx is not finalised, so nothing need to be done", "tx_id", er.Id)
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
	}

	// The purged items already identify the inbound as a swap; do not depend on
	// re-parsing its memo, which can fail once e.g. an affiliate THORName expired.
	if len(reverted) > 0 {
		if err = unwindRevertedAdvSwaps(ctx, mgr, er, tx.Coins, reverted); err != nil {
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, err
		}
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
	}

	// Ownership receipts identify an executed ADD without re-resolving historical
	// names. Do not switch this to ParseMemo/ParseMemoWithTHORNames: it resolves
	// affiliate THORNames at errata time, so an expired name fails the parse, the
	// memo type becomes unknown, and we would return without debiting the pool
	// after the vault was already debited above. The raw prefix sniff cannot fail
	// that way (same reason the swap path uses isSwapInboundMemo). Match the
	// parser's suffix handling even for malformed ADDs such as "ADD|payload".
	baseMemo, _, _ := strings.Cut(tx.Memo, "|")
	memoType, _ := StringToTxType(strings.SplitN(baseMemo, ":", 2)[0])
	if observedVoter.PendingReceipt != nil || memoType == TxAdd {
		// A finalized inbound that never credited custody (e.g. a compromised-vault
		// no-op) never executed its ADD either; there is no loss for the pool to fund.
		if !inboundBeforeErrata.UpdatedVault {
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
		}
		if err = processErrataLiquidity(ctx, mgr, inboundBeforeErrata); err != nil {
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, err
		}
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
	}

	memo, _ := ParseMemoWithTHORNames(ctx, k, tx.Memo)
	// if the tx is a migration , from old valut to new vault , then the inbound tx must have a related outbound tx as well
	if memo.IsInternal() {
		_, outboundErr := processErrataOutboundTx(ctx, k, eventMgr, er)
		if outboundErr != nil {
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, outboundErr
		}
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
	}

	if !memo.IsType(TxSwap) {
		// ADDs were handled above; other transaction types do not debit a pool here.
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
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
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, err
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

	if err := k.SetPool(ctx, pool); err != nil {
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, fmt.Errorf("fail to save pool: %w", err)
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
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, ErrInternal(err, "fail to emit errata event")
	}
	return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
}

// processErrataOutboundTx handles an errata for an outbound tx that was sent but later reorged out.
// It re-credits funds to the vault so they are not abandoned.
// The tx is marked as reverted rather than rescheduled.
func processErrataOutboundTx(ctx cosmos.Context, k keeper.Keeper, eventMgr EventManager, er *common.ErrataTx) (common.ErrataTxOutcome, error) {
	txOutVoter, err := k.GetObservedTxOutVoter(ctx, er.Id)
	if err != nil {
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, fmt.Errorf("fail to get observed tx out voter for tx (%s) : %w", er.Id, err)
	}
	if len(txOutVoter.Txs) == 0 {
		if er.Chain.RequiresTerminalErrataOutcome() {
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION, nil
		}
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, fmt.Errorf("cannot find tx: %s", er.Id)
	}
	// The first outbound quorum debits the vault even before finalization.
	// Restore that selected consensus transaction without waiting for a final
	// observation, which an orphaned outbound may never receive.
	if txOutVoter.Tx.IsEmpty() || (er.Chain.RequiresTerminalErrataOutcome() && txOutVoter.Tx.BlockHeight <= 0) {
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, fmt.Errorf("tx out voter is not finalised")
	}
	tx := txOutVoter.Tx.Tx
	if !tx.Chain.Equals(er.Chain) || tx.Coins.IsEmpty() {
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION, nil
	}
	if er.Chain.RequiresTerminalErrataOutcome() {
		if txOutVoter.Reverted {
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
		}
		// Sign-time XMR outbounds are final before mining, with mined height
		// zero. GetTx retains that selected final tx even after a mined quorum.
		// TxID-wide principal restoration needs no height; validate one if set.
		if txOutVoter.Tx.XmrMinedHeight != 0 {
			if _, err := txOutVoter.Tx.CanonicalMinedHeight(); err != nil {
				return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, fmt.Errorf("resolve outbound errata mined height: %w", err)
			}
		}
	}
	if !er.Chain.RequiresTerminalErrataOutcome() {
		// The memo is unused by vault restoration. A consensus XMR outbound
		// must re-credit its vault even with an unusual memo. Preserve the
		// historical memo gate for other chains.
		m, err := ParseMemoWithTHORNames(ctx, k, tx.Memo)
		if err != nil {
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, fmt.Errorf("fail to parse memo(%s): %w", tx.Memo, err)
		}
		if !m.IsOutbound() && !m.IsInternal() {
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, fmt.Errorf("%s is not outbound or internal tx", m)
		}
	}
	vaultPubKey := txOutVoter.Tx.ObservedPubKey
	if er.Chain.RequiresTerminalErrataOutcome() && vaultPubKey.IsEmpty() {
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, fmt.Errorf("outbound errata has no observed vault")
	}
	if !vaultPubKey.IsEmpty() {
		v, err := k.GetVault(ctx, vaultPubKey)
		if err != nil {
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, fmt.Errorf("fail to get vault with pubkey %s: %w", vaultPubKey, err)
		}
		// Credit funds back to the vault so they are not lost.
		// Keep a valid outbound slash and its pool compensation after a reorg.
		// Any resulting surplus follows the normal over-solvency policy; gas
		// remains charged.
		// Note: We intentionally do NOT change InactiveVault back to RetiringVault
		// as this could cause side effects (blocking churns, affecting node unbonding).
		// Recovery from InactiveVaults with funds should be handled via migration.
		// For XMR outbound voters, UpdatedVault records a persisted principal
		// debit. This requires activation before economic XMR observations:
		// older voters have no debit record and receive no principal credit.
		if !er.Chain.RequiresTerminalErrataOutcome() || txOutVoter.UpdatedVault {
			v.AddFunds(tx.Coins)
		}
		if v.Status == InactiveVault {
			ctx.Logger().Info("Errata credited funds to inactive vault - recovery via migration needed", "vault pub key", v.PubKey)
		}

		if !v.IsEmpty() {
			if err := k.SetVault(ctx, v); err != nil {
				return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, fmt.Errorf("fail to save vault: %w", err)
			}
		}
	}

	// emit security event
	event := NewEventSecurity(tx, "outbound errata")
	if err := eventMgr.EmitEvent(ctx, event); err != nil {
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, ErrInternal(err, "fail to emit security event")
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
	return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
}

// revertedInboundCloseReason is the limit swap close reason of a swap whose
// inbound was reverted.
const revertedInboundCloseReason = "inbound reverted"

// isSwapInboundMemo reports whether an inbound memo is a market or limit swap.
// Only the memo prefix is read, so it does not depend on resolving THORNames
// that may have changed since the swap was queued.
func isSwapInboundMemo(memo string) bool {
	prefix, _, _ := strings.Cut(memo, ":")
	txType, err := StringToTxType(prefix)
	return err == nil && (txType == TxSwap || txType == TxLimitSwap)
}

// removeRevertedAdvSwaps removes every advanced swap queue item of a reverted
// inbound without settling it, and returns the removed items.
func removeRevertedAdvSwaps(ctx cosmos.Context, mgr Manager, txID common.TxID) ([]MsgSwap, error) {
	k := mgr.Keeper()
	// Affiliate sub-swaps share the inbound's tx id at non-contiguous indices,
	// so enumerate every stored index rather than scanning until a gap.
	indices, err := k.GetAdvSwapQueueItemIndices(ctx, txID)
	if err != nil {
		return nil, fmt.Errorf("fail to list adv swap queue items (%s): %w", txID, err)
	}
	var removed []MsgSwap
	for _, index := range indices {
		var msg MsgSwap
		msg, err = k.GetAdvSwapQueueItem(ctx, txID, index)
		if err != nil {
			return nil, fmt.Errorf("fail to get adv swap queue item (%s-%d): %w", txID, index, err)
		}
		if err = k.RemoveAdvSwapQueueItem(ctx, txID, index); err != nil {
			return nil, fmt.Errorf("fail to remove adv swap queue item (%s-%d): %w", txID, index, err)
		}
		if msg.State != nil && len(msg.Tx.Coins) > 0 && msg.IsStreaming() {
			// Close the stream with no output: the filled output is never paid,
			// so indexers must not record it. The unwind still reads State.Out.
			closed := *msg.State
			closed.Out = cosmos.ZeroUint()
			evtMsg := msg
			evtMsg.State = &closed
			emitStreamingSwapEvent(ctx, mgr, evtMsg)
		}
		if msg.IsLimitSwap() {
			evt := NewEventLimitSwapClose(txID, revertedInboundCloseReason, ctx.BlockHeight())
			if err = mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
				ctx.Logger().Error("fail to emit limit swap close event", "error", err)
			}
		}
		removed = append(removed, msg)
	}
	return removed, nil
}

// unwindRevertedAdvSwaps reverses the pool effect of reverted queued swaps.
// A queued swap is funded either by the inbound (its source is an inbound coin)
// or by the output of a sibling swap that already settled (an affiliate fee swap
// takes its deposit from the main swap's outbound).
//
// Of the inbound, only the unfilled remainder (State.Deposit - State.In) of the
// still-queued inbound-funded swaps never entered the source pool; everything
// else did, either through a queued swap's fills or through a sibling sub-swap
// that already settled. So the source pool is debited the inbound less that
// remainder.
//
// The unfilled remainder of an output-funded swap left its pool when the sibling
// settled and is returned to it the same way as an output below, unless it is a
// trade or secured asset still held in the recipient's account.
//
// The filled output (State.Out) of a queued swap left its pool but was never
// paid out, so it is credited back:
//   - a RUNE output returns to the source pool's RUNE depth;
//   - an L1, trade, secured or native non-RUNE output returns to its pool's
//     asset depth (trade and secured outputs are only credited to accounts at
//     settlement);
//   - a synth output was minted into Asgard during the swap, so it is burned;
//   - a derived asset output is left as is (derived assets are protocol-only).
//
// The executed sub-swaps of a double swap moved RUNE (State.IntermediateRune)
// from the source pool to the target pool; it is moved back. The liquidity fees
// of both legs already left Asgard for the Reserve and stay there.
//
// If the synth output of a double swap cannot be burned, the synth keeps
// existing, so the intermediate RUNE that minted it stays in the target pool to
// back it and the source pool is not credited. A security event names the synth
// left in Asgard. The errata itself never fails: rolling it back would put the
// reverted swap back in the queue and let it pay out.
func unwindRevertedAdvSwaps(ctx cosmos.Context, mgr Manager, er *common.ErrataTx, inbound common.Coins, reverted []MsgSwap) error {
	k := mgr.Keeper()
	type poolDelta struct {
		runeIn, assetIn, assetOut cosmos.Uint
		synthBurned               bool
	}
	// runeMove is intermediate RUNE to move from one pool back to another.
	type runeMove struct {
		from, to common.Asset
		amount   cosmos.Uint
	}
	var moves []runeMove
	deltas := map[common.Asset]*poolDelta{}
	var order []common.Asset
	delta := func(asset common.Asset) *poolDelta {
		if d, ok := deltas[asset]; ok {
			return d
		}
		d := &poolDelta{runeIn: cosmos.ZeroUint(), assetIn: cosmos.ZeroUint(), assetOut: cosmos.ZeroUint()}
		deltas[asset] = d
		order = append(order, asset)
		return d
	}

	// credit returns a never-paid coin to the pool it left, and reports whether
	// it did.
	credit := func(msg MsgSwap, coin common.Coin) bool {
		switch {
		case coin.Asset.IsRune():
			d := delta(msg.Tx.Coins[0].Asset.GetLayer1Asset())
			d.runeIn = d.runeIn.Add(coin.Amount)
		case coin.Asset.IsSyntheticAsset():
			if err := burnRevertedSynthOutput(ctx, mgr, coin); err != nil {
				// Failing the errata here would roll back the purge and let the
				// reverted swap pay out, so keep the revert and flag the synth
				// left in Asgard for operators instead.
				ctx.Logger().Error("fail to burn reverted synth output", "tx_id", msg.Tx.ID, "error", err)
				stranded := msg.Tx
				stranded.Coins = common.NewCoins(coin)
				evt := NewEventSecurity(stranded, fmt.Sprintf("errata: fail to burn reverted synth output: %s", err))
				if err = mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
					ctx.Logger().Error("fail to emit security event", "error", err)
				}
				return false
			}
			delta(coin.Asset.GetLayer1Asset()).synthBurned = true
		case !coin.Asset.IsDerivedAsset():
			// L1, trade, secured and native non-RUNE (e.g. THOR.TCY) coins
			// all left their pool's asset depth.
			d := delta(coin.Asset.GetLayer1Asset())
			d.assetIn = d.assetIn.Add(coin.Amount)
		}
		return true
	}

	unfilled := map[common.Asset]cosmos.Uint{}
	for _, msg := range reverted {
		if msg.State == nil || len(msg.Tx.Coins) == 0 {
			continue
		}
		source := msg.Tx.Coins[0].Asset
		remainder := common.SafeSub(msg.State.Deposit, msg.State.In)
		switch {
		case inbound.GetCoin(source).IsEmpty():
			// Output-funded. Only affiliate fee swaps take this shape today, and
			// RUNE affiliate fees are paid directly, never queued under the
			// inbound's tx id. A trade or secured output is credited in full to
			// the recipient, and the affiliate swap withdraws its deposit from
			// that account only when it executes, so the unswapped remainder is
			// still the recipient's and stays out of the pool.
			if !remainder.IsZero() && !source.IsRune() && !source.IsTradeAsset() && !source.IsSecuredAsset() {
				credit(msg, common.NewCoin(source, remainder))
			}
		case source.IsExternalL1Asset():
			if _, ok := unfilled[source]; !ok {
				unfilled[source] = cosmos.ZeroUint()
			}
			unfilled[source] = unfilled[source].Add(remainder)
		default:
			continue
		}
		outCredited := true
		if !msg.State.Out.IsZero() {
			outCredited = credit(msg, common.NewCoin(msg.TargetAsset, msg.State.Out))
		}
		from, to := msg.TargetAsset.GetLayer1Asset(), source.GetLayer1Asset()
		// a double swap through a single pool (e.g. BTC to BTC/BTC) moved no RUNE
		// between pools
		if ir := msg.State.GetIntermediateRuneOrZero(); !ir.IsZero() && outCredited &&
			!from.Equals(to) && !from.IsDerivedAsset() && !to.IsDerivedAsset() {
			// register both pools so they are loaded and saved below
			delta(from)
			delta(to)
			moves = append(moves, runeMove{from: from, to: to, amount: ir})
		}
	}

	for _, coin := range inbound {
		if !coin.Asset.IsExternalL1Asset() {
			continue
		}
		remainder, ok := unfilled[coin.Asset]
		if !ok {
			remainder = cosmos.ZeroUint()
		}
		d := delta(coin.Asset)
		d.assetOut = d.assetOut.Add(common.SafeSub(coin.Amount, remainder))
	}

	pools := make(map[common.Asset]*Pool, len(order))
	for _, asset := range order {
		d := deltas[asset]
		pool, err := k.GetPool(ctx, asset)
		if err != nil {
			return fmt.Errorf("fail to get pool for errata tx: %w", err)
		}
		if pool.IsEmpty() {
			continue
		}
		// cap the debit at the available depth so the event matches the state
		available := pool.BalanceAsset.Add(d.assetIn)
		if d.assetOut.GT(available) {
			d.assetOut = available
		}
		pool.BalanceAsset = common.SafeSub(available, d.assetOut)
		pool.BalanceRune = pool.BalanceRune.Add(d.runeIn)
		pools[asset] = &pool
	}

	// Move the intermediate RUNE back, capped at the RUNE the target pool still
	// holds so the source pool is never credited more than is taken.
	runeOut := map[common.Asset]cosmos.Uint{}
	for _, m := range moves {
		from, to := pools[m.from], pools[m.to]
		if from == nil || to == nil {
			continue
		}
		amount := cosmos.MinUint(m.amount, from.BalanceRune)
		from.BalanceRune = common.SafeSub(from.BalanceRune, amount)
		to.BalanceRune = to.BalanceRune.Add(amount)
		if _, ok := runeOut[m.from]; !ok {
			runeOut[m.from] = cosmos.ZeroUint()
		}
		runeOut[m.from] = runeOut[m.from].Add(amount)
		deltas[m.to].runeIn = deltas[m.to].runeIn.Add(amount)
	}

	var mods PoolMods
	for _, asset := range order {
		pool, ok := pools[asset]
		if !ok {
			continue
		}
		d := deltas[asset]
		if d.synthBurned {
			pool.CalcUnits(k.GetTotalSupply(ctx, asset.GetSyntheticAsset()))
		}
		if err := k.SetPool(ctx, *pool); err != nil {
			return fmt.Errorf("fail to save pool: %w", err)
		}
		out, ok := runeOut[asset]
		if !ok {
			out = cosmos.ZeroUint()
		}
		if !d.assetIn.IsZero() || !d.runeIn.IsZero() {
			mods = append(mods, NewPoolMod(asset, d.runeIn, true, d.assetIn, true))
		}
		if !d.assetOut.IsZero() || !out.IsZero() {
			mods = append(mods, NewPoolMod(asset, out, false, d.assetOut, false))
		}
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "errata", "applied"},
			1,
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(er.Chain)),
				telemetry.NewLabel("asset", pool.Asset.String()),
			},
		)
	}

	if err := mgr.EventMgr().EmitEvent(ctx, NewEventErrata(er.Id, mods)); err != nil {
		return ErrInternal(err, "fail to emit errata event")
	}
	return nil
}

// burnRevertedSynthOutput burns the synth output of a reverted swap, which was
// minted into Asgard by the swap and never paid out.
func burnRevertedSynthOutput(ctx cosmos.Context, mgr Manager, coin common.Coin) error {
	cacheCtx, commit := ctx.CacheContext()
	if err := mgr.Keeper().SendFromModuleToModule(cacheCtx, AsgardName, ModuleName, common.NewCoins(coin)); err != nil {
		return err
	}
	if err := mgr.Keeper().BurnFromModule(cacheCtx, ModuleName, coin); err != nil {
		return err
	}
	commit()
	burnEvt := NewEventMintBurn(BurnSupplyType, coin.Asset.Native(), coin.Amount, "errata")
	if err := mgr.EventMgr().EmitEvent(ctx, burnEvt); err != nil {
		ctx.Logger().Error("fail to emit burn event", "error", err)
	}
	return nil
}
