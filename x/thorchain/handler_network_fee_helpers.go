package thorchain

import (
	"context"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

const xmrNetworkFeeLookbackBlocks = int64(600)

func processNetworkFeeAttestation(
	ctx cosmos.Context,
	mgr Manager,
	voter *keeper.ObservedNetworkFeeVoter,
	attester cosmos.AccAddress,
	active NodeAccounts,
	nf *common.NetworkFee,
	shouldSlashForDuplicate bool,
) error {
	observeSlashPoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	lackOfObservationPenalty := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observeFlex := mgr.Keeper().GetConfigInt64(ctx, constants.ObservationDelayFlexibility)

	slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
		telemetry.NewLabel("reason", "failed_observe_network_fee"),
		telemetry.NewLabel("chain", string(nf.Chain)),
	}))

	if !voter.Sign(attester) {
		// Slash for the network having to handle the extra message/s.
		if shouldSlashForDuplicate {
			mgr.Slasher().IncSlashPoints(slashCtx, observeSlashPoints, attester)
		}
		ctx.Logger().Info("signer already signed network fee", "signer", attester.String(), "block height", nf.Height, "chain", nf.Chain.String())
		return nil
	}

	// doesn't have consensus yet
	if !voter.HasConsensus(active) {
		// Before consensus, slash until consensus.
		mgr.Slasher().IncSlashPoints(slashCtx, observeSlashPoints, attester)
		return nil
	}

	if voter.BlockHeight > 0 {
		// After consensus, only decrement slash points if within the ObservationDelayFlexibility period.
		if (voter.BlockHeight + observeFlex) >= ctx.BlockHeight() {
			mgr.Slasher().DecSlashPoints(slashCtx, lackOfObservationPenalty, attester)
		}
		// MsgNetworkFeeQuorum tx already processed
		return nil
	}

	voter.BlockHeight = ctx.BlockHeight()

	// This signer brings the voter to consensus; increment the signer's slash points like the before-consensus signers,
	// then decrement all the signers' slash points and increment the non-signers' slash points.
	mgr.Slasher().IncSlashPoints(slashCtx, observeSlashPoints, attester)
	signers := voter.GetSigners()
	nonSigners := getNonSigners(active, signers)
	mgr.Slasher().DecSlashPoints(slashCtx, observeSlashPoints, signers...)
	mgr.Slasher().IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)

	ctx.Logger().Info("update network fee", "chain", nf.Chain.String(), "transaction-size", nf.TransactionSize, "fee-rate", nf.TransactionRate)
	networkFeeToSave, err := applyNetworkFeeLookback(ctx, mgr, NetworkFee{
		Chain:              nf.Chain,
		TransactionSize:    nf.TransactionSize,
		TransactionFeeRate: nf.TransactionRate,
	})
	if err != nil {
		return ErrInternal(err, "fail to apply network fee lookback")
	}
	if err := mgr.Keeper().SaveNetworkFee(ctx, nf.Chain, networkFeeToSave); err != nil {
		return ErrInternal(err, "fail to save network fee")
	}

	if nf.Height > 0 {
		if err := mgr.Keeper().SetLastChainHeight(ctx, nf.Chain, nf.Height); err != nil {
			ctx.Logger().Error("fail to set last chain height from network fee", "chain", nf.Chain, "height", nf.Height, "error", err)
		}
	}

	return nil
}

func applyNetworkFeeLookback(ctx cosmos.Context, mgr Manager, incoming NetworkFee) (NetworkFee, error) {
	if !incoming.Chain.Equals(common.XMRChain) {
		return incoming, nil
	}

	current, err := mgr.Keeper().GetNetworkFee(ctx, incoming.Chain)
	if err != nil {
		return incoming, err
	}
	floorBlock, err := mgr.Keeper().GetNetworkFeeFloorBlock(ctx, incoming.Chain)
	if err != nil {
		return incoming, err
	}

	incomingTotal := incoming.TransactionSize * incoming.TransactionFeeRate
	currentTotal := current.TransactionSize * current.TransactionFeeRate

	if currentTotal > incomingTotal && floorBlock > ctx.BlockHeight() {
		ctx.Logger().Info("preserving xmr network fee floor",
			"current_size", current.TransactionSize,
			"current_rate", current.TransactionFeeRate,
			"incoming_size", incoming.TransactionSize,
			"incoming_rate", incoming.TransactionFeeRate,
			"floor_until", floorBlock)
		return current, nil
	}

	if incomingTotal >= currentTotal && incomingTotal > 0 {
		mgr.Keeper().SetNetworkFeeFloorBlock(ctx, incoming.Chain, ctx.BlockHeight()+xmrNetworkFeeLookbackBlocks)
		return incoming, nil
	}

	mgr.Keeper().SetNetworkFeeFloorBlock(ctx, incoming.Chain, 0)
	return incoming, nil
}
