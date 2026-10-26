package thorchain

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// A solvency-gated chain (SolvencyGateInbound-<CHAIN> > 0) does not execute a
// finalised inbound when it is observed. The inbound credits its vault as usual,
// then is parked until a solvency consensus, reached in a later THORChain block,
// attests the vault's wallet balance at or above the inbound's external height
// and finds the vault solvent. An inbound credited for funds that never reached
// the vault (a spoofed observation) leaves the vault insolvent, so the solvency
// checker halts the chain while the inbound is still parked, before its swap or
// refund pays anything out.

const (
	// solvencyGateMaxReleasesPerBlock bounds the inbounds executed by EndBlock.
	solvencyGateMaxReleasesPerBlock = 100
	// solvencyGateMaxScanPerChain bounds the parked inbounds read per chain per
	// block, so a chain whose vault stays unconfirmed cannot make EndBlock walk an
	// unbounded queue. Parked inbounds are read in external height order.
	solvencyGateMaxScanPerChain = 200
)

// isSolvencyGated reports whether inbounds on the chain wait for solvency
// confirmation. It fails closed: an unreadable Mimir gates the chain.
func isSolvencyGated(ctx cosmos.Context, k keeper.Keeper, chain common.Chain) bool {
	key := fmt.Sprintf(constants.MimirTemplateSolvencyGateInbound, chain)
	value, err := k.GetMimir(ctx, key)
	if err != nil {
		ctx.Logger().Error("fail to get mimir, gating inbounds", "key", key, "error", err)
		return true
	}
	return value > 0
}

// parkSolvencyGatedInbound parks a finalised, vault-credited inbound if its chain
// is solvency-gated, and reports whether it did.
func parkSolvencyGatedInbound(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter) (bool, error) {
	k := mgr.Keeper()
	tx := voter.Tx
	if !isSolvencyGated(ctx, k, tx.Tx.Chain) {
		return false, nil
	}
	in := types.SolvencyGatedInbound{
		Chain:    tx.Tx.Chain,
		PubKey:   tx.ObservedPubKey,
		Height:   tx.BlockHeight,
		TxID:     tx.Tx.ID,
		ParkedAt: ctx.BlockHeight(),
	}
	if err := k.SetSolvencyGatedInbound(ctx, in); err != nil {
		return false, fmt.Errorf("fail to park solvency gated inbound: %w", err)
	}
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "solvency_gate", "parked"}, 1,
		[]metrics.Label{telemetry.NewLabel("chain", telemChain(in.Chain))},
	)
	ctx.Logger().Info("inbound parked until vault solvency is confirmed", "chain", in.Chain, "id", in.TxID, "height", in.Height, "vault", in.PubKey)
	return true, nil
}

// confirmSolvencyGate records a solvent consensus for the vault on a gated chain
// at the given external height. Inbounds parked in an earlier THORChain block at
// or below that height are released by the next EndBlock.
func confirmSolvencyGate(ctx cosmos.Context, k keeper.Keeper, chain common.Chain, pubKey common.PubKey, height int64) {
	if !isSolvencyGated(ctx, k, chain) {
		return
	}
	if err := k.SetSolvencyConfirmation(ctx, chain, pubKey, height); err != nil {
		ctx.Logger().Error("fail to set solvency confirmation", "chain", chain, "vault", pubKey, "error", err)
	}
}

// releaseSolvencyGatedInbounds executes parked inbounds whose vault has been
// confirmed solvent at or above their external height, by a consensus reached
// after they were parked. A consensus reached in the block that parked an inbound,
// or earlier, was checked against a vault balance that did not yet include it.
// Inbounds on a chain whose gate has been switched off are released unconditionally.
func releaseSolvencyGatedInbounds(ctx cosmos.Context, mgr Manager) error {
	k := mgr.Keeper()
	handler := NewInternalHandler(mgr)
	released := 0
	for _, chain := range common.AllChains {
		if released >= solvencyGateMaxReleasesPerBlock {
			return nil
		}
		inbounds, err := k.GetSolvencyGatedInbounds(ctx, chain, solvencyGateMaxScanPerChain)
		if err != nil {
			ctx.Logger().Error("fail to get solvency gated inbounds", "chain", chain, "error", err)
		}
		if len(inbounds) == 0 {
			continue
		}
		gated := isSolvencyGated(ctx, k, chain)
		for _, in := range inbounds {
			if released >= solvencyGateMaxReleasesPerBlock {
				return nil
			}
			if gated {
				confirmedHeight, confirmedAt, err := k.GetSolvencyConfirmation(ctx, chain, in.PubKey)
				if err != nil {
					ctx.Logger().Error("fail to get solvency confirmation", "chain", chain, "vault", in.PubKey, "error", err)
					continue
				}
				if confirmedHeight < in.Height || confirmedAt <= in.ParkedAt {
					continue
				}
			}
			released++
			releaseSolvencyGatedInbound(ctx, mgr, handler, in)
		}
	}
	return nil
}

// releaseSolvencyGatedInbound unparks and executes one inbound. Its state changes
// commit only if execution succeeds; the inbound is unparked either way, as a
// failed execution would fail again every block.
func releaseSolvencyGatedInbound(ctx cosmos.Context, mgr Manager, handler cosmos.Handler, in types.SolvencyGatedInbound) {
	k := mgr.Keeper()
	k.DeleteSolvencyGatedInbound(ctx, in)

	result := "released"
	defer func() {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "solvency_gate", "unparked"}, 1,
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(in.Chain)),
				telemetry.NewLabel("result", result),
			},
		)
	}()

	voter, err := k.GetObservedTxInVoter(ctx, in.TxID)
	if err != nil {
		result = "error"
		ctx.Logger().Error("fail to get observed tx in voter for solvency gated inbound", "id", in.TxID, "error", err)
		return
	}
	// An inbound reverted while parked (for example by errata) must not execute.
	if voter.Reverted || voter.Tx.IsEmpty() {
		result = "skipped"
		ctx.Logger().Info("solvency gated inbound no longer executable", "id", in.TxID, "reverted", voter.Reverted)
		return
	}
	vault, err := k.GetVault(ctx, in.PubKey)
	if err != nil {
		result = "error"
		ctx.Logger().Error("fail to get vault for solvency gated inbound", "id", in.TxID, "vault", in.PubKey, "error", err)
		return
	}

	// The consensus tx stands in for the observation that finalised it. Its memo
	// is the voter's: a generated r:<ref> memo for a memoless inbound, which the
	// processing below resolves exactly as it would have at finalisation.
	tx := voter.Tx
	memo, _ := ParseMemoWithTHORNames(ctx, k, tx.Tx.Memo)
	signers := tx.GetSigners()
	if len(signers) == 0 {
		result = "error"
		ctx.Logger().Error("solvency gated inbound has no signers", "id", in.TxID)
		return
	}

	cacheCtx, commit := ctx.CacheContext()
	if err = processFinalisedObservedTxIn(cacheCtx, mgr, signers[0], handler, tx, voter, vault, memo); err != nil {
		result = "error"
		ctx.Logger().Error("fail to process solvency gated inbound", "id", in.TxID, "error", err)
		return
	}
	commit()
	ctx.Logger().Info("solvency gated inbound released", "chain", in.Chain, "id", in.TxID, "height", in.Height)
}
