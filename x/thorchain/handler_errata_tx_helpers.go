package thorchain

import (
	"context"
	"errors"
	"fmt"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
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

	if er.Chain.RequiresTerminalErrataOutcome() {
		if observedVoter.Reverted {
			// The inbound backstop can win between errata retries without
			// restoring an already-observed outbound. Finish that side too;
			// its own Reverted guard prevents a second vault credit.
			_, outboundErr := processErrataOutboundTx(ctx, k, eventMgr, er)
			if outboundErr != nil {
				return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, outboundErr
			}
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
		}
		// TxID-wide reversal still requires a valid canonical observation;
		// absent/malformed location must never be converted to a terminal no-op.
		if _, heightErr := observedVoter.Tx.CanonicalMinedHeight(); heightErr != nil {
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, fmt.Errorf("resolve inbound errata mined height: %w", heightErr)
		}
	}

	// A speculative swap must stop and unwind as part of the errata decision.
	// This also covers a finalized voter whose atomic lifecycle completion has
	// not succeeded yet; falling through to ordinary errata there would deduct
	// the inbound asset without reversing the speculative swap output.
	// Run the happy path atomically with Reverted. If rollback itself fails,
	// persist Reverted plus ROLLBACK_PENDING as a fail-closed stop marker; the
	// bounded lifecycle recovery worker will retry without allowing another leg.
	record, found, lifecycleErr := k.GetMempoolSwap(ctx, observedVoter.TxID, 0)
	if lifecycleErr != nil {
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, fmt.Errorf("fail to get errata mempool swap lifecycle: %w", lifecycleErr)
	}
	if found {
		rolledBack, revertErr := revertMempoolSwap(ctx, mgr, observedVoter, record)
		if revertErr != nil {
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, revertErr
		}
		// Emit the same reverted counter the ordinary path below emits: both
		// outcomes of this branch persist Reverted, so without it a dashboard
		// built on errata.reverted undercounts once mempool-swap traffic exists.
		incrErrataRevertedIn(er.Chain)
		if rolledBack {
			incrMempoolSwapCounter("rolled_back_total", []metrics.Label{telemetry.NewLabel("reason", "errata")})
		}
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
	}

	// set the observed Tx to reverted only after chain validation passes
	observedVoter.SetReverted()
	incrErrataRevertedIn(er.Chain)

	memo, _ := ParseMemoWithTHORNames(ctx, k, tx.Memo)
	// A finalized swap or add credited the pool as well as the vault; both are
	// unwound by one pool correction.
	if observedVoter.Tx.IsFinal() && (memo.IsType(TxSwap) || memo.IsType(TxAdd)) {
		if err = correctOrDeferErrataPool(ctx, mgr, observedVoter, memo.GetType()); err != nil {
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, err
		}
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
	}

	k.SetObservedTxInVoter(ctx, observedVoter)
	if err = debitErrataVault(ctx, mgr, observedVoter); err != nil {
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, err
	}

	if !observedVoter.Tx.IsFinal() {
		ctx.Logger().Info("tx is not finalised, so nothing need to be done", "tx_id", er.Id)
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
	}

	// if the tx is a migration , from old valut to new vault , then the inbound tx must have a related outbound tx as well
	if memo.IsInternal() {
		_, outboundErr := processErrataOutboundTx(ctx, k, eventMgr, er)
		if outboundErr != nil {
			return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, outboundErr
		}
		return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
	}
	return common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, nil
}

// errErrataPoolCorrectionBlocked marks an errata pool correction which cannot
// run until the speculative owner of the pool is resolved.
var errErrataPoolCorrectionBlocked = errors.New("errata pool correction is blocked by an unresolved mempool swap pool owner")

// correctOrDeferErrataPool applies an errata pool correction atomically. When
// the pool's speculative owner cannot be resolved, the correction is recorded
// on the reverted voter instead of failing the attestation: an error here would
// discard the errata vote, slash accounting and the Reverted stop marker along
// with it. Pool gates then hold conflicting mutations back until a pool
// preemption or the LackSigning recovery pass applies the correction.
func correctOrDeferErrataPool(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter, txType mem.TxType) error {
	_, assetCoin := splitErrataPoolCoins(voter.Tx.Tx.Coins)
	correctionCtx, commit := ctx.CacheContext()
	// Settle corrections already pending on the pool first, preserving errata
	// order for the balance clamps.
	err := preemptMempoolSwapPoolOwnerCached(correctionCtx, mgr, assetCoin.Asset)
	if err != nil {
		err = fmt.Errorf("%w: %w", errErrataPoolCorrectionBlocked, err)
	} else {
		err = applyErrataPoolCorrectionCached(correctionCtx, mgr, voter, txType)
	}
	if err == nil {
		commit()
		return nil
	}
	if !errors.Is(err, errErrataPoolCorrectionBlocked) {
		return err
	}

	ctx.Logger().Error("deferring errata pool correction behind unresolved mempool swap owner", "tx_id", voter.TxID, "error", err)
	voter.ErrataPoolCorrection = txType.String()
	k := mgr.Keeper()
	k.SetObservedTxInVoter(ctx, voter)
	k.SetErrataPoolCorrection(ctx, assetCoin.Asset, voter.TxID)
	return nil
}

// settleErrataPoolCorrectionsCached applies every errata correction deferred
// on the asset's pool. The caller must already have resolved the pool owner.
func settleErrataPoolCorrectionsCached(ctx cosmos.Context, mgr Manager, asset common.Asset) error {
	k := mgr.Keeper()
	txIDs, err := k.GetErrataPoolCorrections(ctx, asset)
	if err != nil {
		return fmt.Errorf("fail to get errata pool corrections for %s: %w", asset.GetLayer1Asset(), err)
	}
	for _, txID := range txIDs {
		voter, err := k.GetObservedTxInVoter(ctx, txID)
		if err != nil {
			return fmt.Errorf("fail to get errata pool correction voter %s: %w", txID, err)
		}
		// Malformed entries can never apply. Drop them rather than holding
		// every later mutation of the pool behind them.
		if voter.ErrataPoolCorrection == "" {
			ctx.Logger().Error("dropping errata pool correction index without a pending voter", "tx_id", txID)
			k.DeleteErrataPoolCorrection(ctx, asset, txID)
			continue
		}
		txType, err := StringToTxType(voter.ErrataPoolCorrection)
		if err != nil || (txType != TxSwap && txType != TxAdd) {
			ctx.Logger().Error("dropping invalid errata pool correction", "tx_id", txID, "type", voter.ErrataPoolCorrection)
			k.DeleteErrataPoolCorrection(ctx, asset, txID)
			voter.ErrataPoolCorrection = ""
			k.SetObservedTxInVoter(ctx, voter)
			continue
		}
		if err = applyErrataPoolCorrectionCached(ctx, mgr, voter, txType); err != nil {
			return err
		}
	}
	return nil
}

// recoverErrataPoolCorrections retries deferred errata pool corrections once
// per pool. Each pool commits independently so one stuck owner cannot hold
// back another pool's correction.
func recoverErrataPoolCorrections(ctx cosmos.Context, mgr Manager) error {
	pools, err := mgr.Keeper().GetErrataPoolCorrectionPools(ctx)
	if err != nil {
		return fmt.Errorf("fail to list errata pool corrections: %w", err)
	}
	for _, asset := range pools {
		cacheCtx, commit := ctx.CacheContext()
		if err = preemptMempoolSwapPoolOwnerCached(cacheCtx, mgr, asset); err != nil {
			ctx.Logger().Error("fail to settle deferred errata pool correction", "pool", asset, "error", err)
			continue
		}
		commit()
	}
	return nil
}

func splitErrataPoolCoins(coins common.Coins) (runeCoin, assetCoin common.Coin) {
	runeCoin = common.NoCoin
	assetCoin = common.NoCoin
	for _, coin := range coins {
		if coin.IsRune() {
			runeCoin = coin
		} else {
			assetCoin = coin
		}
	}
	return runeCoin, assetCoin
}

// debitErrataVault removes a reverted inbound from the vault it credited.
func debitErrataVault(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter) error {
	tx := voter.Tx.Tx
	// The inbound path marks a self-send as updated without crediting it.
	if !voter.UpdatedVault || (tx.Chain.RequiresTerminalErrataOutcome() && tx.FromAddress.Equals(tx.ToAddress)) {
		return nil
	}
	vaultPubKey := voter.Tx.ObservedPubKey
	if vaultPubKey.IsEmpty() {
		return nil
	}
	// try to deduct the asset from asgard
	k := mgr.Keeper()
	vault, err := k.GetVault(ctx, vaultPubKey)
	if err != nil {
		return fmt.Errorf("fail to get active asgard vaults: %w", err)
	}
	debitVault(ctx, mgr, &vault, voter.Tx, voter.Tx.Tx.Coins, vaultDeficitReasonInboundErrataReversal)
	if err = k.SetVault(ctx, vault); err != nil {
		return fmt.Errorf("fail to save vault, err: %w", err)
	}
	return nil
}

// applyErrataPoolCorrectionCached unwinds a reverted swap or add from its vault
// and pool, clearing any pending marker in the same transaction so the
// correction applies exactly once.
func applyErrataPoolCorrectionCached(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter, txType mem.TxType) error {
	if txType != TxSwap && txType != TxAdd {
		return fmt.Errorf("invalid errata pool correction type %q for %s", txType, voter.TxID)
	}
	k := mgr.Keeper()
	tx := voter.Tx.Tx
	runeCoin, assetCoin := splitErrataPoolCoins(tx.Coins)

	// Ordinary errata is an irreversible pool mutation. Keep it atomic with
	// removal of any unrelated speculative owner so the debit cannot consume
	// balances still needed by a later lifecycle rollback.
	if err := resolveMempoolSwapPoolOwnerCached(ctx, mgr, assetCoin.Asset); err != nil {
		return fmt.Errorf("%w: %w", errErrataPoolCorrectionBlocked, err)
	}

	if voter.ErrataPoolCorrection != "" {
		k.DeleteErrataPoolCorrection(ctx, assetCoin.Asset, voter.TxID)
		voter.ErrataPoolCorrection = ""
	}
	k.SetObservedTxInVoter(ctx, voter)
	if err := debitErrataVault(ctx, mgr, voter); err != nil {
		return err
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
	if txType == TxAdd {
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
			telemetry.NewLabel("chain", telemChain(tx.Chain)),
			telemetry.NewLabel("asset", pool.Asset.String()),
		},
	)

	// send errata event
	mods := PoolMods{
		NewPoolMod(pool.Asset, runeCoin.Amount, false, assetCoin.Amount, false),
	}

	eventErrata := NewEventErrata(voter.TxID, mods)
	if err := mgr.EventMgr().EmitEvent(ctx, eventErrata); err != nil {
		return ErrInternal(err, "fail to emit errata event")
	}
	return nil
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

// incrErrataRevertedIn counts one reverted inbound. Both the mempool-swap
// lifecycle path and the ordinary errata path end at SetReverted, so both emit
// it; the counter is a global metrics side effect and is deliberately not tied
// to the errata CacheContext, because the voter is persisted Reverted whether
// or not the speculative rollback commits on this pass.
func incrErrataRevertedIn(chain common.Chain) {
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "errata", "reverted"},
		1,
		[]metrics.Label{
			telemetry.NewLabel("chain", telemChain(chain)),
			telemetry.NewLabel("direction", "in"),
		},
	)
}
