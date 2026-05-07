package thorchain

import (
	"crypto/sha256"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	BondYieldSwapMemoPrefix               = "THOR-BOND-YIELD"
	defaultBondYieldClaimableBpsCap       = int64(2500)
	defaultBondYieldOutboundFeeMultiplier = int64(100)
)

// getBondYieldClaimableBpsCap returns the Mimir cap limiting how much accrued
// yield can remain claimable relative to a provider's principal.
func getBondYieldClaimableBpsCap(ctx cosmos.Context, mgr Manager) cosmos.Uint {
	value, err := mgr.Keeper().GetMimir(ctx, "BondYieldClaimableBpsCap")
	if err != nil || value < 0 {
		value = defaultBondYieldClaimableBpsCap
	}
	if value > int64(constants.MaxBasisPts) {
		value = int64(constants.MaxBasisPts)
	}
	return cosmos.NewUint(uint64(value))
}

// isBondYieldPaused reports whether manual and automatic bond yield claims are disabled.
func isBondYieldPaused(ctx cosmos.Context, mgr Manager) bool {
	pause, err := mgr.Keeper().GetMimir(ctx, "PauseBondYield")
	return err == nil && pause > 0
}

// getBondYieldOutboundFeeMultiplier returns the fee multiple a non-RUNE payout
// must exceed before the collector triggers a swap.
func getBondYieldOutboundFeeMultiplier(ctx cosmos.Context, mgr Manager) int64 {
	value, err := mgr.Keeper().GetMimir(ctx, "BondYieldOutboundFeeMultiplier")
	if err != nil || value < 1 {
		value = defaultBondYieldOutboundFeeMultiplier
	}
	return value
}

// getBondYieldSwapThreshold returns the accrued RUNE threshold required before
// swapping a provider's collected bond yield to its configured asset.
func getBondYieldSwapThreshold(ctx cosmos.Context, mgr Manager, asset common.Asset) (cosmos.Uint, error) {
	outboundFeeRune, err := mgr.GasMgr().GetAssetOutboundFee(ctx, asset, true)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	return outboundFeeRune.Mul(cosmos.NewUint(uint64(getBondYieldOutboundFeeMultiplier(ctx, mgr)))), nil
}

// getBondYieldPayout resolves an unset payout to the provider's THORChain bond address.
func getBondYieldPayout(provider BondProvider) (common.Asset, common.Address) {
	asset := provider.PayoutAsset
	address := provider.PayoutAddress
	if asset.IsEmpty() || asset.IsRune() {
		asset = common.RuneAsset()
		if address.IsEmpty() {
			address = common.Address(provider.BondAddress.String())
		}
	}
	return asset, address
}

// triggerBondYieldPayoutSwap queues an internal RUNE swap from the bond yield
// collector to the provider's configured non-RUNE payout address.
func triggerBondYieldPayoutSwap(ctx cosmos.Context, mgr Manager, collector BondYieldCollector, asset common.Asset, destination common.Address, queueIndex int) error {
	if collector.RuneAmount.IsZero() {
		return fmt.Errorf("can't execute bond yield payout swap, accrued RUNE amount is zero")
	}

	collectorBalance := mgr.Keeper().GetRuneBalanceOfModule(ctx, BondYieldCollectorName)
	if collector.RuneAmount.GT(collectorBalance) {
		return fmt.Errorf("rune amount greater than module balance: (%s/%s)", collector.RuneAmount.String(), collectorBalance.String())
	}

	coin := common.NewCoin(common.RuneAsset(), collector.RuneAmount)
	asgardAddress, err := mgr.Keeper().GetModuleAddress(AsgardName)
	if err != nil {
		return err
	}
	collectorAddress, err := mgr.Keeper().GetModuleAddress(BondYieldCollectorName)
	if err != nil {
		return err
	}

	// Include node, provider, amount, and height so each collector payout has a
	// deterministic tx id while still allowing later payouts for the same pair.
	networkMemo := fmt.Sprintf("%s-%s-%s", BondYieldSwapMemoPrefix, collector.NodeAddress.String(), collector.BondProviderAddress.String())
	hashInput := fmt.Sprintf("%s|%s|%d", networkMemo, coin.String(), ctx.BlockHeight())
	hash := fmt.Sprintf("%X", sha256.Sum256([]byte(hashInput)))
	txID, err := common.NewTxID(hash)
	if err != nil {
		return err
	}

	existingVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, txID)
	if err != nil {
		return fmt.Errorf("fail to get existing voter: %w", err)
	}
	if len(existingVoter.Txs) > 0 {
		return fmt.Errorf("bond yield payout tx already exists: %s", hashInput)
	}

	tx := common.NewTx(
		txID,
		collectorAddress,
		asgardAddress,
		common.NewCoins(coin),
		common.Gas{},
		networkMemo,
	)

	version := types.SwapVersion_v1
	if mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		version = types.SwapVersion_v2
	}

	swap := NewMsgSwap(
		tx,
		asset,
		destination,
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"", nil,
		MarketSwap,
		1, 0,
		version,
		collector.BondProviderAddress,
	)

	txIn := ObservedTx{Tx: tx}
	txInVoter := NewObservedTxVoter(txIn.Tx.ID, []common.ObservedTx{txIn})
	txInVoter.Height = ctx.BlockHeight()
	txInVoter.FinalisedHeight = ctx.BlockHeight()
	txInVoter.Tx = txIn
	mgr.Keeper().SetObservedTxInVoter(ctx, txInVoter)

	if mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		if err = mgr.AdvSwapQueueMgr().AddSwapQueueItem(ctx, mgr, swap); err != nil {
			return fmt.Errorf("fail to add bond yield payout swap to advanced queue: %w", err)
		}
	} else if err = mgr.Keeper().SetSwapQueueItem(ctx, *swap, queueIndex); err != nil {
		return fmt.Errorf("fail to add bond yield payout swap to queue: %w", err)
	}

	if err = mgr.Keeper().SendFromModuleToModule(ctx, BondYieldCollectorName, AsgardName, common.NewCoins(coin)); err != nil {
		return fmt.Errorf("failed to send rune to asgard: %w", err)
	}

	collector.RuneAmount = cosmos.ZeroUint()
	mgr.Keeper().SetBondYieldCollector(ctx, collector)
	return nil
}

// tryBondYieldCollectorPayout pays collected RUNE immediately or queues a
// non-RUNE payout once the collector balance exceeds the swap threshold.
func tryBondYieldCollectorPayout(ctx cosmos.Context, mgr Manager, provider BondProvider, collector BondYieldCollector, queueIndex int) error {
	asset, destination := getBondYieldPayout(provider)
	if asset.IsRune() {
		to, err := destination.AccAddress()
		if err != nil {
			return fmt.Errorf("fail to parse bond yield payout address: %w", err)
		}
		coin := common.NewCoin(common.RuneNative, collector.RuneAmount)
		if err = mgr.Keeper().SendFromModuleToAccount(ctx, BondYieldCollectorName, to, common.NewCoins(coin)); err != nil {
			return fmt.Errorf("fail to send bond yield payout: %w", err)
		}
		collector.RuneAmount = cosmos.ZeroUint()
		mgr.Keeper().SetBondYieldCollector(ctx, collector)
		return nil
	}

	threshold, err := getBondYieldSwapThreshold(ctx, mgr, asset)
	if err != nil {
		ctx.Logger().Error("failed to get outbound fee for bond yield payout, skipping swap", "asset", asset, "error", err)
		return nil
	}
	if collector.RuneAmount.LTE(threshold) {
		return nil
	}
	if err = triggerBondYieldPayoutSwap(ctx, mgr, collector, asset, destination, queueIndex); err != nil {
		ctx.Logger().Error("fail to swap bond yield payout", "node", collector.NodeAddress.String(), "provider", collector.BondProviderAddress.String(), "err", err)
	}
	return nil
}

// claimBondYield moves claimable yield out of the provider bond and into the
// bond yield collector, then attempts the configured payout path.
func claimBondYield(ctx cosmos.Context, mgr Manager, tx common.Tx, nodeAcc *NodeAccount, bp *BondProviders, providerAcc cosmos.AccAddress, amount cosmos.Uint, queueIndex int) (cosmos.Uint, error) {
	provider := bp.Get(providerAcc)
	if provider.IsEmpty() {
		return cosmos.ZeroUint(), fmt.Errorf("bond provider not found")
	}

	claimed := bp.ClaimYield(amount, providerAcc)
	if claimed.IsZero() {
		return cosmos.ZeroUint(), nil
	}
	nodeAcc.Bond = common.SafeSub(nodeAcc.Bond, claimed)

	coin := common.NewCoin(common.RuneNative, claimed)
	if err := mgr.Keeper().SendFromModuleToModule(ctx, BondName, BondYieldCollectorName, common.NewCoins(coin)); err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("fail to move bond yield to collector: %w", err)
	}

	collector, err := mgr.Keeper().GetBondYieldCollector(ctx, nodeAcc.NodeAddress, providerAcc)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("fail to get bond yield collector: %w", err)
	}
	collector.RuneAmount = collector.RuneAmount.Add(claimed)
	mgr.Keeper().SetBondYieldCollector(ctx, collector)

	if err = tryBondYieldCollectorPayout(ctx, mgr, provider, collector, queueIndex); err != nil {
		return cosmos.ZeroUint(), err
	}

	bondReturnedEvent := NewEventBond(claimed, BondReturned, tx, nodeAcc, providerAcc)
	if err = mgr.EventMgr().EmitEvent(ctx, bondReturnedEvent); err != nil {
		ctx.Logger().Error("fail to emit bond event", "error", err)
	}

	return claimed, nil
}

// autoClaimBondYield claims each provider's configured share of claimable yield
// after a validator reward distribution.
func autoClaimBondYield(ctx cosmos.Context, mgr Manager, nodeAcc *NodeAccount, bp *BondProviders) error {
	if isBondYieldPaused(ctx, mgr) {
		return nil
	}

	queueIndex := 0
	for _, provider := range bp.Providers {
		provider.NormaliseYieldAccounting()
		if provider.AutoClaimBps == 0 || provider.ClaimableYield.IsZero() {
			continue
		}
		amount := common.GetSafeShare(cosmos.NewUint(provider.AutoClaimBps), cosmos.NewUint(10000), provider.ClaimableYield)
		if amount.IsZero() {
			continue
		}

		asset, _ := getBondYieldPayout(provider)
		if !asset.IsRune() {
			// Leave small non-RUNE claims in ClaimableYield until the collector can
			// swap more than the configured outbound-fee threshold.
			collector, err := mgr.Keeper().GetBondYieldCollector(ctx, nodeAcc.NodeAddress, provider.BondAddress)
			if err != nil {
				return err
			}
			threshold, err := getBondYieldSwapThreshold(ctx, mgr, asset)
			if err != nil {
				ctx.Logger().Error("failed to get outbound fee for bond yield payout, skipping auto claim", "asset", asset, "error", err)
				continue
			}
			if collector.RuneAmount.Add(amount).LTE(threshold) {
				continue
			}
		}

		if _, err := claimBondYield(ctx, mgr, common.Tx{}, nodeAcc, bp, provider.BondAddress, amount, queueIndex); err != nil {
			return err
		}
		queueIndex++
	}
	return nil
}
