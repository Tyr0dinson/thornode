//go:build mainnet
// +build mainnet

package thorchain

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	v2 "gitlab.com/thorchain/thornode/v3/x/thorchain/migrations/v2"
)

// Migrator is a struct for handling in-place store migrations.
type Migrator struct {
	mgr *Mgrs
}

// NewMigrator returns a new Migrator.
func NewMigrator(mgr *Mgrs) Migrator {
	return Migrator{mgr: mgr}
}

// Migrate1to2 migrates from version 1 to 2.
func (m Migrator) Migrate1to2(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}
	return v2.MigrateStore(ctx, m.mgr.storeService)
}

// Migrate2to3 migrates from version 2 to 3.
func (m Migrator) Migrate2to3(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// refund stagenet funding wallet for user refund
	// original user tx: https://runescan.io/tx/A9AF3ED203079BB246CEE0ACD837FBA024BC846784DE488D5BE70044D8877C52
	// refund to user from stagenet funding wallet: https://bscscan.com/tx/0xba67f3a88f8c998f29e774ffa8328e5625521e37c2db282b29a04ab3d2593f48
	stagenetWallet := "0x3021C479f7F8C9f1D5c7d8523BA5e22C0Bcb5430"
	inTxId := "A9AF3ED203079BB246CEE0ACD837FBA024BC846784DE488D5BE70044D8877C52" // original user tx

	bscUsdt, err := common.NewAsset("BSC.USDT-0X55D398326F99059FF775485246999027B3197955")
	if err != nil {
		return err
	}
	usdtCoin := common.NewCoin(bscUsdt, cosmos.NewUint(4860737515919))
	blockHeight := ctx.BlockHeight()

	// schedule refund
	if err := unsafeAddRefundOutbound(ctx, m.mgr, inTxId, stagenetWallet, usdtCoin, blockHeight); err != nil {
		return err
	}

	return nil
}

// Migrate3to4 migrates from version 4 to 5.
func (m Migrator) Migrate4to5(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// ------------------------------ TCY ------------------------------

	totalTCYCoin := common.NewCoin(common.TCY, cosmos.NewUint(210_000_000_00000000))
	err := m.mgr.Keeper().MintToModule(ctx, ModuleName, totalTCYCoin)
	if err != nil {
		return err
	}

	// Claims 206_606_541_28874864
	claimingModuleCoin := common.NewCoin(common.TCY, cosmos.NewUint(206_606_541_28874864))
	err = m.mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, TCYClaimingName, common.NewCoins(claimingModuleCoin))
	if err != nil {
		return err
	}

	// 210M minus claims: 206_606_541_28874864
	treasuryCoin := common.NewCoin(common.TCY, totalTCYCoin.Amount.Sub(claimingModuleCoin.Amount))
	treasuryAddress, err := common.NewAddress("thor10qh5272ktq4wes8ex343ky9rsuehcypddjh08k")
	if err != nil {
		return err
	}

	treasuryAccAddress, err := treasuryAddress.AccAddress()
	if err != nil {
		return err
	}

	err = m.mgr.Keeper().SendFromModuleToAccount(ctx, ModuleName, treasuryAccAddress, common.NewCoins(treasuryCoin))
	if err != nil {
		return err
	}

	err = setTCYClaims(ctx, m.mgr)
	if err != nil {
		return err
	}

	// ------------------------------ Bond Slash Refund ------------------------------

	for _, slashRefund := range mainnetSlashRefunds4to5 {
		recipient, err := cosmos.AccAddressFromBech32(slashRefund.address)
		if err != nil {
			ctx.Logger().Error("error parsing address in store migration", "error", err)
			continue
		}
		amount := cosmos.NewUint(slashRefund.amount)
		refundCoins := common.NewCoins(common.NewCoin(common.RuneAsset(), amount))
		if err := m.mgr.Keeper().SendFromModuleToAccount(ctx, ReserveName, recipient, refundCoins); err != nil {
			ctx.Logger().Error("fail to store migration transfer RUNE from Reserve to recipient", "error", err, "recipient", recipient, "amount", amount)
		}
	}

	// ------------------------------ Mimir Cleanup ------------------------------

	return m.ClearObsoleteMimirs(ctx)
}

// Migrate5to6 migrates from version 5 to 6.
func (m Migrator) Migrate5to6(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// ------------------------------ Bond Slash Refund ------------------------------

	// Validate Reserve module has sufficient funds before starting refunds
	totalRefundAmount := cosmos.NewUint(14856919212689) // Total amount to be refunded
	reserveBalance := m.mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	if reserveBalance.LT(totalRefundAmount) {
		return fmt.Errorf("insufficient reserve balance for migration: have %s, need %s",
			reserveBalance.String(), totalRefundAmount.String())
	}
	ctx.Logger().Info("Reserve balance validation passed",
		"reserve_balance", reserveBalance.String(),
		"required_amount", totalRefundAmount.String())

	for _, slashRefund := range mainnetSlashRefunds5to6 {
		recipient, err := cosmos.AccAddressFromBech32(slashRefund.address)
		if err != nil {
			ctx.Logger().Error("error parsing address in store migration",
				"error", err,
				"address", slashRefund.address)
			continue
		}
		amount := cosmos.NewUint(slashRefund.amount)
		refundCoins := common.NewCoins(common.NewCoin(common.RuneAsset(), amount))
		if err := m.mgr.Keeper().SendFromModuleToAccount(ctx, ReserveName, recipient, refundCoins); err != nil {
			ctx.Logger().Error("fail to store migration transfer RUNE from Reserve to recipient",
				"error", err,
				"recipient", recipient.String(),
				"address", slashRefund.address,
				"amount", amount.String())
		} else {
			ctx.Logger().Debug("successfully transferred bond slash refund",
				"recipient", recipient.String(),
				"amount", amount.String())
		}
	}

	return nil
}

// Migrate6to7 migrates from version 6 to 7.
func (m Migrator) Migrate6to7(ctx sdk.Context) error {
	// loads the manager for this migration
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// handle manual outbounds
	outbounds, err := mainnetManualOutbounds6to7(ctx, m.mgr)
	if err != nil {
		ctx.Logger().Error("failed to create manual outbounds for migration", "error", err)
	}
	for _, out := range outbounds {
		// schedule outbound for 30 minutes after upgrade height
		outboundHeight := ctx.BlockHeight() + 300
		err := m.mgr.TxOutStore().UnSafeAddTxOutItem(ctx, m.mgr, out, outboundHeight)
		if err != nil {
			ctx.Logger().Error("failed to add manual outbound", "error", err, "outbound", out)
		} else {
			ctx.Logger().Info("successfully added manual outbound", "outbound", out)
		}
	}

	// handle manual observations of dropped inbounds
	inbounds, err := mainnetManualInbounds6to7()
	if err != nil {
		ctx.Logger().Error("failed to create manual inbounds for migration", "error", err)
	}
	for _, in := range inbounds {
		err = makeFakeTxInObservation(ctx, m.mgr, ObservedTxs{in})
		if err != nil {
			ctx.Logger().Error("failed to create fake inbound observation", "error", err, "inbound", in)
		} else {
			ctx.Logger().Info("successfully created fake inbound observation", "inbound", in)
		}
	}

	return nil
}

// makeFakeTxInObservation accepts an array of unobserved inbounds, queries for active
// node accounts, and makes a fake observation for each validator and unobserved TxIn.
// Returns an error if there are no active validators or if every per-validator handler
// invocation failed, so that callers can distinguish total failure from success.
func makeFakeTxInObservation(ctx cosmos.Context, mgr Manager, txs ObservedTxs) error {
	activeNodes, err := mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		ctx.Logger().Error("Failed to get active nodes", "err", err)
		return err
	}
	if len(activeNodes) == 0 {
		return fmt.Errorf("no active validators to record fake observation")
	}

	handler := NewObservedTxInHandler(mgr)

	successCount := 0
	for _, na := range activeNodes {
		txInMsg := NewMsgObservedTxIn(txs, na.NodeAddress)
		_, err := handler.handle(ctx, *txInMsg)
		if err != nil {
			ctx.Logger().Error("failed ObservedTxIn handler", "error", err)
			continue
		}
		successCount++
	}

	if successCount == 0 {
		return fmt.Errorf("all %d ObservedTxIn handler invocations failed", len(activeNodes))
	}

	return nil
}

// Migrate8to9 migrates from version 8 to 9.
func (m Migrator) Migrate8to9(ctx sdk.Context) error {
	// loads the manager for this migration
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	err := m.CommonMigrate8to9(ctx)
	if err != nil {
		return err
	}

	// handle manual outbounds
	outbounds, err := mainnetManualOutbounds8to9(ctx, m.mgr)
	if err != nil {
		ctx.Logger().Error("failed to create manual outbounds for migration", "error", err)
	}
	for _, out := range outbounds {
		outboundHeight := ctx.BlockHeight()
		err := m.mgr.TxOutStore().UnSafeAddTxOutItem(ctx, m.mgr, out, outboundHeight)
		if err != nil {
			ctx.Logger().Error("failed to add manual outbound", "error", err, "outbound", out)
		} else {
			ctx.Logger().Info("successfully added manual outbound", "outbound", out)
		}
	}

	return nil
}

// Migrate9to10 migrates from version 9 to 10.
func (m Migrator) Migrate9to10(ctx sdk.Context) error {
	// loads the manager for this migration
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// move excess thor.nami to treasury
	coins := common.Coins{{
		Asset:  common.NAMI,
		Amount: cosmos.NewUint(524245),
	}}
	err := m.mgr.Keeper().SendFromModuleToModule(
		ctx, AsgardName, TreasuryName, coins,
	)
	if err != nil {
		ctx.Logger().Error("failed to move excess nami to treasury", "error", err)
	} else {
		ctx.Logger().Info("successfully moved excess nami to treasury", "coins", coins)
	}

	// re-attempt recoveries
	outbounds, err := mainnetManualOutbounds9to10(ctx, m.mgr)
	if err != nil {
		ctx.Logger().Error("failed to create manual outbounds for migration", "error", err)
	}
	for _, out := range outbounds {
		outboundHeight := ctx.BlockHeight()
		err := m.mgr.TxOutStore().UnSafeAddTxOutItem(ctx, m.mgr, out, outboundHeight)
		if err != nil {
			ctx.Logger().Error("failed to add manual outbound", "error", err, "outbound", out)
		} else {
			ctx.Logger().Info("successfully added manual outbound", "outbound", out)
		}
	}

	return nil
}

// Migrate10to11 migrates from version 10 to 11.
func (m Migrator) Migrate10to11(ctx sdk.Context) error {
	return nil
}

// Migrate11to12 migrates from version 11 to 12.
func (m Migrator) Migrate11to12(ctx sdk.Context) error {
	// loads the manager for this migration
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// avax.usdc asset
	avaxUSDC, err := common.NewAsset("AVAX.USDC-0XB97EF9EF8734C71904D8002F8B6BC66DD9C48A6E")
	if err != nil {
		return fmt.Errorf("failed to parse AVAX.USDC asset: %w", err)
	}

	// records to be removed
	removeLPs := []LiquidityProvider{
		// https://gateway.liquify.com/chain/thorchain_api/cosmos/tx/v1beta1/txs/5BF8911A9DD947EC1EE7E990085ECB21DD7E395F152B5137F7CD73B76E46870A
		{
			RuneAddress:  "thor1pe0pspu4ep85gxr5h9l6k49g024vemtr80hg4c",
			AssetAddress: "0x03c42ab083bd46202ee430AfC4D3dd8eD8c76c07",
		},
		// https://gateway.liquify.com/chain/thorchain_api/cosmos/tx/v1beta1/txs/248CE4B07E6A63E8CA3E111754849DD560FA9F4BF83BF2FD0B0B40947633B1B9
		{
			RuneAddress:  "",
			AssetAddress: "0x467a0ec2d2f23f0bb165eb7e44a9b16b12f5a7b4",
		},
		// affiliate position for the prior add liquidity transaction
		{
			RuneAddress:  "thor122h9hlrugzdny9ct95z6g7afvpzu34s73uklju",
			AssetAddress: "",
		},
	}

	// get the pool
	pool, err := m.mgr.Keeper().GetPool(ctx, avaxUSDC)
	if err != nil {
		return fmt.Errorf("failed to get AVAX.USDC pool: %w", err)
	}

	for _, lp := range removeLPs {
		// fetch the record
		lp, err = m.mgr.Keeper().GetLiquidityProvider(ctx, avaxUSDC, lp.GetAddress())
		if err != nil {
			ctx.Logger().Error("failed to get liquidity provider ",
				"error", err,
				"rune_address", lp.RuneAddress,
				"asset_address", lp.AssetAddress)
			continue
		}

		// remove the units from the pool
		pool.LPUnits = common.SafeSub(pool.LPUnits, lp.Units)
		ctx.Logger().Info("adjusted pool LP units",
			"asset", avaxUSDC.String(),
			"removed_units", lp.Units.String(),
			"new_pool_lp_units", pool.LPUnits.String())

		// remove the record
		m.mgr.Keeper().RemoveLiquidityProvider(ctx, lp)
		ctx.Logger().Info(
			"removed bad liquidity provider record",
			"asset", avaxUSDC.String(),
			"rune_address", lp.RuneAddress,
			"asset_address", lp.AssetAddress,
		)
	}

	// save the updated pool
	if err := m.mgr.Keeper().SetPool(ctx, pool); err != nil {
		return fmt.Errorf("failed to save updated pool: %w", err)
	}

	// remove obsolete/typo mimir keys
	obsoleteMimirs := []string{
		"HALTRADING",
		"ADR012",
		"ADR020",
		"ADR18",
		"DEPRECATEILP",
		"FULLIMPLOSSPROTECTIONBLOCKS",
		"ILPCUTOFF",
		"KILLSWITCHSTART",
		"PROPOSAL6",
		"VOTEDOFM",
		"VOTELENDING",
		"VOTEMAXSYNTHSFORSAVERSYIELD",
		"ENABLEAVAXCHAIN",
		"ENABLEBSC",
		"ENABLESAVINGSVAULTS",
		"ADD-CHAIN-BASE",
		"ADD-CHAIN-XRP",
	}

	for _, key := range obsoleteMimirs {
		if err := m.mgr.Keeper().DeleteMimir(ctx, key); err != nil {
			ctx.Logger().Error("failed to delete obsolete mimir", "key", key, "error", err)
			continue
		}
		ctx.Logger().Info("deleted obsolete mimir", "key", key)
	}

	// refund 43F310A416A4ED8CF8B645B1EBBB5E25FB89F9777A4350F7023DEB62B90EA3AD
	refundRune := common.NewCoin(common.RuneNative, cosmos.NewUint(40000000000))
	userAddr, err := common.NewAddress("thor1dvvr4kdeurs8fdwgrql6je7l2v9ma73dp50n7m")
	if err != nil {
		return err
	}
	userAccAddr, err := userAddr.AccAddress()
	if err != nil {
		return err
	}
	err = m.mgr.Keeper().SendFromModuleToAccount(ctx, ReserveName, userAccAddr, common.NewCoins(refundRune))
	if err != nil {
		return err
	}

	return nil
}

// Migrate12to13 migrates from version 12 to 13.
func (m Migrator) Migrate12to13(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// ------------------------------ Bond Slash Refund ------------------------------

	// Validate Reserve module has sufficient funds before starting refunds
	totalRefundAmount := cosmos.NewUint(mainnetSlashRefunds12to13Total) // Total amount to be refunded
	reserveBalance := m.mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	if reserveBalance.LT(totalRefundAmount) {
		return fmt.Errorf("insufficient reserve balance for migration: have %s, need %s",
			reserveBalance.String(), totalRefundAmount.String())
	}
	ctx.Logger().Info("Reserve balance validation passed",
		"reserve_balance", reserveBalance.String(),
		"required_amount", totalRefundAmount.String())

	for _, slashRefund := range mainnetSlashRefunds12to13 {
		recipient, err := cosmos.AccAddressFromBech32(slashRefund.address)
		if err != nil {
			ctx.Logger().Error("error parsing address in store migration",
				"error", err,
				"address", slashRefund.address)
			continue
		}
		amount := cosmos.NewUint(slashRefund.amount)
		refundCoins := common.NewCoins(common.NewCoin(common.RuneAsset(), amount))
		if err := m.mgr.Keeper().SendFromModuleToAccount(ctx, ReserveName, recipient, refundCoins); err != nil {
			ctx.Logger().Error("fail to store migration transfer RUNE from Reserve to recipient",
				"error", err,
				"recipient", recipient.String(),
				"address", slashRefund.address,
				"amount", amount.String())
		} else {
			ctx.Logger().Debug("successfully transferred bond slash refund",
				"recipient", recipient.String(),
				"amount", amount.String())
		}
	}

	return nil
}

// Migrate13to14 migrates from version 13 to 14.
func (m Migrator) Migrate13to14(ctx sdk.Context) error {
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// ADR-023: Burn ~87% of Reserve and reduce MaxRuneSupply to 360M.
	return m.BurnReserveAndReduceMaxSupply(ctx)
}

// Migrate14to15 migrates from version 14 to 15.
//
// Recovers state from the DOGE asgard migration double-broadcast at block
// 26,001,464 (slash tx 1DFE09ABE8D8DD11BA55BD6B499AC1EF91FA0B5CA83D492AB12C08AC89C2B54D)
// where the retiring vault transmitted the same migration outbound twice,
// draining 122,221,658,117,429 sats and triggering a 200% bond slash on the
// 19 vault members. The slash was later absorbed by each member's bond
// providers proportionally via bp.Adjust() during subsequent reward
// distributions.
//
// Four corrections, in order:
//
//  1. Reverse the pool slash event: pool.BalanceAsset += 122,221,658,117,429,
//     pool.BalanceRune -= 26,008,730,208,467. A strict-subtract guards against
//     pool drift between the slash block and the migration block — if the
//     pool's RUNE depth dropped below the credited amount (e.g. via LP
//     activity), the migration aborts cleanly rather than silently clamping.
//  2. Move 26,008,730,208,467 RUNE from Asgard back to Reserve, undoing the
//     bond->asgard transfer that backed the (now-reversed) pool credit.
//  3. Refund the slash directly from Reserve to each provider's wallet, using
//     the pre-computed bond deltas in mainnetSlashRefunds14to15. Per-row sends
//     log-and-continue on failure (matches Migrate4to5 / 5to6 / 12to13). na.Bond
//     is intentionally NOT touched: some slashed nodes will have churned out
//     and unbonded by the time this runs, and others will have a different
//     provider distribution than at slash time. Crediting na.Bond would also
//     trigger a NodeOperatorFee skim on the next bp.Adjust() since providers
//     have already absorbed the slash.
//  4. Reconcile phantom credit on two retiring asgard ETH vaults caused by
//     SafeSub clamping in vault.SubFunds during outbound observation when the
//     vault was already drained. See fixVaultClamps_v3_18_0 and
//     mainnetVaultClampFixes14to15 for details. Pre-flight check skips any
//     entry that would clamp again (loud error logged) — better to leave the
//     phantom credit in place than halt the upgrade.
//
// Net coin movement: Asgard -26B, Reserve 0 (in +26B from Asgard, out -52B to
// providers, was +26B from original slash), providers' wallets +52B total.
// ETH vault Coins reduced by 270.427671 ETH across two retiring vaults (no
// module/account movement — purely a vault.Coins ledger correction).
func (m Migrator) Migrate14to15(ctx sdk.Context) error {
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	const (
		stolenDogeSats   = uint64(122_221_658_117_429)
		runeCreditedSats = uint64(26_008_730_208_467)
		asgardToReserve  = uint64(26_008_730_208_467)
	)

	// Step 1: reverse pool slash event.
	pool, err := m.mgr.Keeper().GetPool(ctx, common.DOGEAsset)
	if err != nil {
		return fmt.Errorf("fail to get DOGE pool: %w", err)
	}
	if pool.IsEmpty() {
		return fmt.Errorf("DOGE pool is empty, cannot reverse slash")
	}
	runeCredit := cosmos.NewUint(runeCreditedSats)
	if pool.BalanceRune.LT(runeCredit) {
		return fmt.Errorf(
			"DOGE pool BalanceRune (%s) below expected slash credit (%d); "+
				"pool drifted between slash and migration — investigate before retrying",
			pool.BalanceRune.String(), runeCreditedSats,
		)
	}
	pool.BalanceAsset = pool.BalanceAsset.Add(cosmos.NewUint(stolenDogeSats))
	pool.BalanceRune = pool.BalanceRune.Sub(runeCredit)
	if err := m.mgr.Keeper().SetPool(ctx, pool); err != nil {
		return fmt.Errorf("fail to save DOGE pool: %w", err)
	}
	poolSlashAmt := []PoolAmt{
		{Asset: common.DOGEAsset, Amount: int64(stolenDogeSats)},
		{Asset: common.RuneAsset(), Amount: -int64(runeCreditedSats)},
	}
	if err := m.mgr.EventMgr().EmitEvent(ctx, NewEventSlash(common.DOGEAsset, poolSlashAmt)); err != nil {
		ctx.Logger().Error("fail to emit pool reversal event", "error", err)
	}

	// Step 2: Asgard -> Reserve.
	asgardCoin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(asgardToReserve))
	if err := m.mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, common.NewCoins(asgardCoin)); err != nil {
		return fmt.Errorf("fail to move asgard->reserve: %w", err)
	}

	// Step 3: Reserve -> per-provider direct refund.
	// Pre-flight Reserve balance check (matches Migrate5to6 / 12to13).
	totalRefundAmount := cosmos.NewUint(mainnetSlashRefunds14to15Total)
	reserveBalance := m.mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	if reserveBalance.LT(totalRefundAmount) {
		return fmt.Errorf("insufficient reserve balance for migration: have %s, need %s",
			reserveBalance.String(), totalRefundAmount.String())
	}
	ctx.Logger().Info("Reserve balance validation passed",
		"reserve_balance", reserveBalance.String(),
		"required_amount", totalRefundAmount.String())

	for _, refund := range mainnetSlashRefunds14to15 {
		recipient, err := cosmos.AccAddressFromBech32(refund.address)
		if err != nil {
			ctx.Logger().Error("error parsing address in store migration",
				"error", err,
				"address", refund.address)
			continue
		}
		amount := cosmos.NewUint(refund.amount)
		refundCoins := common.NewCoins(common.NewCoin(common.RuneAsset(), amount))
		if err := m.mgr.Keeper().SendFromModuleToAccount(ctx, ReserveName, recipient, refundCoins); err != nil {
			ctx.Logger().Error("fail to store migration transfer RUNE from Reserve to recipient",
				"error", err,
				"recipient", recipient.String(),
				"address", refund.address,
				"amount", amount.String())
		} else {
			ctx.Logger().Debug("successfully transferred bond slash refund",
				"recipient", recipient.String(),
				"amount", amount.String())
		}
	}

	// Step 4: reconcile phantom credit on retiring asgard ETH vaults from
	// SafeSub clamping in vault.SubFunds during outbound observation.
	// Failures inside this routine are logged and skipped per-row; we do
	// not return an error from this step so the migration as a whole still
	// completes the prior 3 steps.
	fixVaultClamps_v3_18_0(ctx, m.mgr, mainnetVaultClampFixes14to15)

	return nil
}

// adr028PoolHaircut describes the pool-level depth adjustments applied by the
// ADR-028 conciliation waterfall (steps 1–4).  All amounts are in 1e8 units.
// Fields are populated from on-chain state snapshotted at upgrade height;
// zero values mean the step does not apply to that pool.
type adr028PoolHaircut struct {
	asset common.Asset
	// step1RuneHaircut: ΔR — reduce pool.BalanceRune by this amount and burn it
	// from Asgard. pool.BalanceAsset is reduced proportionally:
	// ΔA = ΔR × BalanceAsset / BalanceRune (symmetric AMM identity).
	// LP units at the reserve address are NOT mutated; polPoolValue() reads
	// pool.BalanceRune live so the depth reduction propagates automatically.
	// After all step-1 pool writes, RUNEPool.ReserveUnits is re-solved to keep
	// providers whole (see Migrate15to16 for the formula).
	step1RuneHaircut uint64
	// step2ReserveSynthBurn: synth amount (in the pool's synth denom) to burn
	// directly from the reserve module bank. No pool balance change — the freed
	// backing shifts from synth-claim to LP-claim on the next CalcUnits recompute.
	step2ReserveSynthBurn uint64
	// step3SaversHaircutBps: savers haircut in basis points applied to the synth
	// vault pool (asset = synthAsset). Reduces every saver LP record pro-rata,
	// reduces the synth pool LPUnits and BalanceAsset, and burns the corresponding
	// synth supply from Asgard.
	step3SaversHaircutBps uint64
	// step4TreasuryDraw: ΔA — asset amount to draw from the treasury LP position.
	// Reduces treasury LP units, pool.LPUnits, pool.BalanceAsset by ΔA, and
	// pool.BalanceRune / Asgard RUNE by ΔR = ΔA × BalanceRune / BalanceAsset.
	// Non-zero only for zero-saver assets where treasury holds LP coverage.
	step4TreasuryDraw uint64
}

// vault2v28PubKey is the compromised Asgard vault whose keeper balance is
// zeroed in step 0.  The vault's L1 assets churn to the new active vault
// through normal bifrost operations.
const vault2v28PubKey = common.PubKey("thorpub1addwnpepqvpnhwzqkd2zqkxsj272faf4ezsngwh773wjdfkus8m4f860u3g5ka22v28")


// adr028TreasuryAddr is the treasury multisig providing step-4 LP coverage.
const adr028TreasuryAddr = "thor10qh5272ktq4wes8ex343ky9rsuehcypddjh08k"

// Migrate15to16 implements ADR-028 conciliation.
// Waterfall order (ADR-028 §3, re-run amounts at upgrade height):
//
//	— zero vault 2v28 keeper balances (log only, no event).
//	— POL haircut: reduce pool.BalanceRune/Asset, burn RUNE from Asgard,
//	  then solve RUNEPool.ReserveUnits to hold providers whole.
//	— reserve synth burn: burn reserve module's synth bank balances.
//	— savers haircut: reduce saver LP units + synth pool depth pro-rata,
//	  burn synths from Asgard.
//	— treasury LP draw: cover residual for zero-saver pools with treasury LP.
//
// Snapshot date: 2026-05-25 (re-run before release at upgrade height).
// POL drain = ΔR (RUNE to burn from Asgard; ΔA = ΔR×BalanceAsset/BalanceRune).
// Only the reserve-bucket fraction (55.76%) of LP value is consumed;
// providers are held whole via RUNEPool.ReserveUnits solve after all pool writes.
func (m Migrator) Migrate15to16(ctx sdk.Context) error {
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// ── Step 0: zero vault 2v28 keeper record ───────────────────────────────
	vault, err := m.mgr.Keeper().GetVault(ctx, vault2v28PubKey)
	if err != nil {
		return fmt.Errorf("conciliation: vault 2v28: fail to get vault 2v28: %w", err)
	}
	originalCoins := vault.Coins
	vault.Coins = common.Coins{}
	if err := m.mgr.Keeper().SetVault(ctx, vault); err != nil {
		return fmt.Errorf("conciliation: vault 2v28: fail to zero vault 2v28: %w", err)
	}
	ctx.Logger().Info("conciliation: vault 2v28: zeroed vault 2v28 keeper record",
		"pubkey", vault2v28PubKey,
		"cleared_coins", originalCoins.String())

	// ── Build per-pool haircut table (assets requiring NewAsset parsing) ─────
	ethUSDT, err := common.NewAsset("ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	if err != nil {
		return fmt.Errorf("adr028: failed to parse ETH.USDT: %w", err)
	}
	avaxUSDC, err := common.NewAsset("AVAX.USDC-0XB97EF9EF8734C71904D8002F8B6BC66DD9C48A6E")
	if err != nil {
		return fmt.Errorf("adr028: failed to parse AVAX.USDC: %w", err)
	}
	bscBNB, err := common.NewAsset("BSC.BNB-0XBB4CDB9CBD36B01BD1CBAEBF2DE08D9173BC095C")
	if err != nil {
		return fmt.Errorf("adr028: failed to parse BSC.BNB: %w", err)
	}
	baseUSDC, err := common.NewAsset("BASE.USDC-0X833589FCD6EDB6E08F4C7C32D4F71B54BDA02913")
	if err != nil {
		return fmt.Errorf("adr028: failed to parse BASE.USDC: %w", err)
	}
	bscBUSD, err := common.NewAsset("BSC.BUSD-0XE9E7CEA3DEDCA5984780BAFC599BD69ADD087D56")
	if err != nil {
		return fmt.Errorf("adr028: failed to parse BSC.BUSD: %w", err)
	}
	bscETH, err := common.NewAsset("BSC.ETH-0X2170ED0880AC9A755FD29B2688956BD959F933F8")
	if err != nil {
		return fmt.Errorf("adr028: failed to parse BSC.ETH: %w", err)
	}
	bscBTCB, err := common.NewAsset("BSC.BTCB-0X7130D2A12B9BCBFAE4F2634D864A1EE1CE3EAD9C")
	if err != nil {
		return fmt.Errorf("adr028: failed to parse BSC.BTCB: %w", err)
	}

	// Per-pool waterfall table — 2026-05-25 live-chain snapshot; re-run at upgrade height.
	// Step1 = ΔR (raw 1e8 RUNE to burn from Asgard); ΔA derived at runtime.
	// Step2 = synth units in 1e8 to burn from reserve bank (no pool mutation).
	// Step3 = savers haircut bps applied to the synth vault pool.
	// Step4 = ΔA (raw 1e8 asset) drawn from treasury LP; only assets w/ treasury coverage.
	// Zero values mean the step does not apply to that pool.
	haircuts := []adr028PoolHaircut{
		// DOGE: fully covered by POL haircut (72,302 DOGE) + reserve synth burn (3,392,456 DOGE synths).
		{asset: common.DOGEAsset, step1RuneHaircut: 1_419_918_684_958, step2ReserveSynthBurn: 339_245_565_058_092},
		// BCH: fully covered by POL haircut (174 BCH).
		{asset: common.BCHAsset, step1RuneHaircut: 13_013_460_708_269},
		// BSC.BNB: fully covered by reserve synth burn (45.74 BNB synths).
		{asset: bscBNB, step2ReserveSynthBurn: 4_574_000_000},
		// ETH.USDT: POL haircut (198,410 USDT) + reserve synth burn (27,022 USDT) → residual 2591 bps savers haircut.
		{asset: ethUSDT, step1RuneHaircut: 33_880_349_267_266, step2ReserveSynthBurn: 2_702_221_779_137, step3SaversHaircutBps: 2591},
		// AVAX.USDC: synth units inflated, near-zero LP share → full 2713 bps savers haircut.
		{asset: avaxUSDC, step3SaversHaircutBps: 2713},
		// ETH.ETH: no POL or synth coverage → 1514 bps savers haircut.
		{asset: common.ETHAsset, step3SaversHaircutBps: 1514},
		// GAIA.ATOM: no POL or synth coverage → 3121 bps savers haircut.
		{asset: common.ATOMAsset, step3SaversHaircutBps: 3121},
		// Zero-saver pools with confirmed treasury LP coverage:
		{asset: baseUSDC, step4TreasuryDraw: 4_904_100_000_000},
		{asset: bscBUSD, step4TreasuryDraw: 3_214_300_000_000},
		{asset: bscETH, step4TreasuryDraw: 1_600_000_000},
		{asset: bscBTCB, step4TreasuryDraw: 51_000_000},
	}

	// ── Step 1: POL haircut (per pool) ──────────────────────────────────────
	// Snapshot provider value BEFORE any pool write.
	runePool, err := m.mgr.Keeper().GetRUNEPool(ctx)
	if err != nil {
		return fmt.Errorf("conciliation: pol haircut: fail to get RUNEPool: %w", err)
	}
	vPre, err := runePoolValue(ctx, m.mgr)
	if err != nil {
		return fmt.Errorf("conciliation: pol haircut: fail to compute runePoolValue pre: %w", err)
	}
	providerValuePre := common.GetSafeShare(runePool.PoolUnits, runePool.TotalUnits(), vPre)

	totalStep1Rune := cosmos.ZeroUint()
	for _, h := range haircuts {
		if h.step1RuneHaircut == 0 {
			continue
		}
		pool, err := m.mgr.Keeper().GetPool(ctx, h.asset)
		if err != nil {
			return fmt.Errorf("conciliation: pol haircut: fail to get pool %s: %w", h.asset, err)
		}
		deltaR := cosmos.NewUint(h.step1RuneHaircut)
		if pool.BalanceRune.LT(deltaR) {
			return fmt.Errorf("conciliation: pol haircut: pool %s BalanceRune %s < haircut %d — abort",
				h.asset, pool.BalanceRune, h.step1RuneHaircut)
		}
		// ΔA = ΔR × BalanceAsset / BalanceRune (price-ratio preservation).
		deltaA := common.GetSafeShare(deltaR, pool.BalanceRune, pool.BalanceAsset)
		pool.BalanceRune = common.SafeSub(pool.BalanceRune, deltaR)
		pool.BalanceAsset = common.SafeSub(pool.BalanceAsset, deltaA)
		if err := m.mgr.Keeper().SetPool(ctx, pool); err != nil {
			return fmt.Errorf("conciliation: pol haircut: fail to save pool %s: %w", h.asset, err)
		}
		// Burn ΔR from Asgard (bank pairing: Asgard→thorchain→burn).
		runeCoin := common.NewCoin(common.RuneNative, deltaR)
		if err := m.mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ModuleName, common.NewCoins(runeCoin)); err != nil {
			return fmt.Errorf("conciliation: pol haircut: fail to move RUNE from Asgard for %s: %w", h.asset, err)
		}
		if err := m.mgr.Keeper().BurnFromModule(ctx, ModuleName, runeCoin); err != nil {
			return fmt.Errorf("conciliation: pol haircut: fail to burn RUNE for %s: %w", h.asset, err)
		}
		mod := NewPoolMod(h.asset, deltaR, false, deltaA, false)
		emitPoolBalanceChangedEvent(ctx, mod, "conciliation_pol_haircut", m.mgr)
		burnEvt := NewEventMintBurn(BurnSupplyType, runeCoin.Asset.Native(), runeCoin.Amount, "conciliation_pol_haircut")
		if err := m.mgr.EventMgr().EmitEvent(ctx, burnEvt); err != nil {
			ctx.Logger().Error("conciliation: pol haircut: fail to emit burn event", "error", err, "asset", h.asset)
		}
		totalStep1Rune = totalStep1Rune.Add(deltaR)
		ctx.Logger().Info("conciliation: pol haircut: POL haircut applied",
			"asset", h.asset, "delta_rune", deltaR, "delta_asset", deltaA)
	}

	// Re-snapshot after all pool writes, then solve RUNEPool.ReserveUnits so
	// provider value is preserved (reserve absorbs the full POL loss).
	vPost, err := runePoolValue(ctx, m.mgr)
	if err != nil {
		return fmt.Errorf("conciliation: pol haircut: fail to compute runePoolValue post: %w", err)
	}
	if providerValuePre.GTE(vPost) || runePool.TotalUnits().IsZero() {
		// Edge case: reserve is fully wiped or no units — set ReserveUnits to 0.
		ctx.Logger().Error("conciliation: pol haircut: providerValuePre >= vPost or TotalUnits=0; zeroing ReserveUnits",
			"v_pre", vPre, "v_post", vPost, "provider_value_pre", providerValuePre)
		runePool.ReserveUnits = cosmos.ZeroUint()
	} else {
		// ReserveUnits_post = PoolUnits × vPost / providerValuePre − PoolUnits
		newTotalUnits := common.GetUncappedShare(runePool.PoolUnits, providerValuePre, vPost)
		runePool.ReserveUnits = common.SafeSub(newTotalUnits, runePool.PoolUnits)
	}
	m.mgr.Keeper().SetRUNEPool(ctx, runePool)
	ctx.Logger().Info("conciliation: pol haircut: RUNEPool.ReserveUnits adjusted",
		"v_pre", vPre, "v_post", vPost,
		"provider_value_pre", providerValuePre,
		"new_reserve_units", runePool.ReserveUnits)

	// Track POL withdrawal in the POL record so CurrentDeposit() stays accurate.
	if !totalStep1Rune.IsZero() {
		pol, err := m.mgr.Keeper().GetPOL(ctx)
		if err != nil {
			return fmt.Errorf("conciliation: pol haircut: fail to get POL: %w", err)
		}
		pol.RuneWithdrawn = pol.RuneWithdrawn.Add(totalStep1Rune)
		if err := m.mgr.Keeper().SetPOL(ctx, pol); err != nil {
			return fmt.Errorf("conciliation: pol haircut: fail to save POL: %w", err)
		}
	}

	// ── Step 2: reserve synth burn ───────────────────────────────────────────
	// Pure bank burn — no pool balance mutation.  FreedSynthUnits shifts from
	// synth-claim to LP-claim on the next CalcUnits recompute.
	for _, h := range haircuts {
		if h.step2ReserveSynthBurn == 0 {
			continue
		}
		synthAsset := h.asset.GetSyntheticAsset()
		synthCoin := common.NewCoin(synthAsset, cosmos.NewUint(h.step2ReserveSynthBurn))
		if err := m.mgr.Keeper().BurnFromModule(ctx, ReserveName, synthCoin); err != nil {
			return fmt.Errorf("conciliation: reserve synth burn: fail to burn %s from reserve: %w", synthAsset, err)
		}
		burnEvt := NewEventMintBurn(BurnSupplyType, synthCoin.Asset.Native(), synthCoin.Amount, "conciliation_reserve_synth_burn")
		if err := m.mgr.EventMgr().EmitEvent(ctx, burnEvt); err != nil {
			ctx.Logger().Error("conciliation: reserve synth burn: fail to emit burn event", "error", err, "asset", synthAsset)
		}
		ctx.Logger().Info("conciliation: reserve synth burn: burned reserve synths",
			"asset", synthAsset, "amount", h.step2ReserveSynthBurn)
	}

	// ── Step 3: savers haircut ───────────────────────────────────────────────
	// Operates on the synth vault pool (asset = synthAsset, one-sided LP with
	// BalanceAsset only).  Reduces every saver LP unit pro-rata, reduces pool
	// LPUnits and BalanceAsset, then burns the freed synth supply from Asgard.
	for _, h := range haircuts {
		if h.step3SaversHaircutBps == 0 {
			continue
		}
		synthAsset := h.asset.GetSyntheticAsset()
		saverPool, err := m.mgr.Keeper().GetPool(ctx, synthAsset)
		if err != nil {
			return fmt.Errorf("conciliation: savers haircut: fail to get saver pool %s: %w", synthAsset, err)
		}
		// burnAmt = pool.BalanceAsset × bps / 10000
		burnAmt := saverPool.BalanceAsset.MulUint64(h.step3SaversHaircutBps).QuoUint64(10000)
		if burnAmt.IsZero() {
			ctx.Logger().Error("conciliation: savers haircut: zero burnAmt, skipping", "asset", synthAsset)
			continue
		}
		// Cap burnAmt to what Asgard actually holds rather than aborting; log any shortfall.
		asgardSynthBal := m.mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, synthAsset.Native())
		if asgardSynthBal.LT(burnAmt) {
			ctx.Logger().Error("conciliation: savers haircut: Asgard synth balance below burnAmt, capping — shortfall left",
				"asset", synthAsset,
				"asgard_balance", asgardSynthBal,
				"burn_amt", burnAmt,
				"shortfall", common.SafeSub(burnAmt, asgardSynthBal))
			burnAmt = asgardSynthBal
		}
		// Collect all saver LP records before modifying (safe iteration pattern).
		var saverLPs []LiquidityProvider
		iter := m.mgr.Keeper().GetLiquidityProviderIterator(ctx, synthAsset)
		for ; iter.Valid(); iter.Next() {
			var lp LiquidityProvider
			m.mgr.Keeper().Cdc().MustUnmarshal(iter.Value(), &lp)
			saverLPs = append(saverLPs, lp)
		}
		_ = iter.Close()
		// Reduce each saver's LP units pro-rata, accumulating actual deductions
		// to use as the pool-level deduction. This avoids the divergence that
		// arises from two independent ⌊× bps/10000⌋ truncations: the sum of
		// per-LP truncations is always ≤ the pool-level truncation, which would
		// leave saverPool.LPUnits < Σ lp.Units after the migration.
		var sumLPDeductions cosmos.Uint = cosmos.ZeroUint()
		for _, lp := range saverLPs {
			deduct := lp.Units.MulUint64(h.step3SaversHaircutBps).QuoUint64(10000)
			lp.Units = common.SafeSub(lp.Units, deduct)
			m.mgr.Keeper().SetLiquidityProvider(ctx, lp)
			sumLPDeductions = sumLPDeductions.Add(deduct)
		}
		// Reduce synth pool depth using the accumulated LP deduction so that
		// saverPool.LPUnits == Σ lp.Units is maintained exactly.
		saverPool.LPUnits = common.SafeSub(saverPool.LPUnits, sumLPDeductions)
		saverPool.BalanceAsset = common.SafeSub(saverPool.BalanceAsset, burnAmt)
		if err := m.mgr.Keeper().SetPool(ctx, saverPool); err != nil {
			return fmt.Errorf("conciliation: savers haircut: fail to save saver pool %s: %w", synthAsset, err)
		}
		// Burn the freed synth supply from Asgard.
		synthCoin := common.NewCoin(synthAsset, burnAmt)
		if err := m.mgr.Keeper().BurnFromModule(ctx, AsgardName, synthCoin); err != nil {
			return fmt.Errorf("conciliation: savers haircut: fail to burn synths from Asgard for %s: %w", synthAsset, err)
		}
		mod := NewPoolMod(synthAsset, cosmos.ZeroUint(), false, burnAmt, false)
		emitPoolBalanceChangedEvent(ctx, mod, "conciliation_saver_haircut", m.mgr)
		burnEvt := NewEventMintBurn(BurnSupplyType, synthCoin.Asset.Native(), synthCoin.Amount, "conciliation_saver_haircut")
		if err := m.mgr.EventMgr().EmitEvent(ctx, burnEvt); err != nil {
			ctx.Logger().Error("conciliation: savers haircut: fail to emit burn event", "error", err, "asset", synthAsset)
		}
		ctx.Logger().Info("conciliation: savers haircut: savers haircut applied",
			"asset", synthAsset, "bps", h.step3SaversHaircutBps, "burn_amt", burnAmt,
			"lp_count", len(saverLPs))
	}

	// ── Step 4: treasury LP draw ─────────────────────────────────────────────
	// Covers residual shortfall for zero-saver assets where treasury holds LP.
	// ΔA = step4TreasuryDraw; ΔR = ΔA × BalanceRune / BalanceAsset.
	// Treasury LP units are reduced by ΔA / BalanceAsset (proportional share).
	totalStep4PlannedRune := cosmos.ZeroUint()
	totalStep4DrawnRune := cosmos.ZeroUint()
	treasuryAddr, err := common.NewAddress(adr028TreasuryAddr)
	if err != nil {
		return fmt.Errorf("conciliation: treasury draw: fail to parse treasury address: %w", err)
	}
	for _, h := range haircuts {
		if h.step4TreasuryDraw == 0 {
			continue
		}
		pool, err := m.mgr.Keeper().GetPool(ctx, h.asset)
		if err != nil {
			return fmt.Errorf("conciliation: treasury draw: fail to get pool %s: %w", h.asset, err)
		}
		if pool.BalanceAsset.IsZero() || pool.BalanceRune.IsZero() {
			ctx.Logger().Error("conciliation: treasury draw: pool has zero balance, skipping",
				"asset", h.asset, "balance_asset", pool.BalanceAsset, "balance_rune", pool.BalanceRune)
			continue
		}
		deltaA := cosmos.NewUint(h.step4TreasuryDraw)
		// Explicit pre-condition: deltaA must not exceed pool depth. GetSafeShare
		// silently caps deltaR at BalanceRune when deltaA > BalanceAsset, so the
		// "LT(deltaR)" guard below would never fire. A bad snapshot amount must be
		// a hard error, not a silent full-pool drain.
		if deltaA.GT(pool.BalanceAsset) {
			return fmt.Errorf("conciliation: treasury draw: pool %s step4TreasuryDraw %s exceeds BalanceAsset %s — snapshot out of date",
				h.asset, deltaA, pool.BalanceAsset)
		}
		deltaR := common.GetSafeShare(deltaA, pool.BalanceAsset, pool.BalanceRune)
		plannedR := deltaR
		// LP units corresponding to ΔA of the pool (prop to total).
		deltaLP := common.GetSafeShare(deltaA, pool.BalanceAsset, pool.LPUnits)
		// Reduce treasury LP record.
		treasuryLP, err := m.mgr.Keeper().GetLiquidityProvider(ctx, h.asset, treasuryAddr)
		if err != nil {
			ctx.Logger().Error("conciliation: treasury draw: fail to get treasury LP, skipping",
				"asset", h.asset, "error", err)
			totalStep4PlannedRune = totalStep4PlannedRune.Add(plannedR)
			continue
		}
		if treasuryLP.Units.LT(deltaLP) {
			ctx.Logger().Error("conciliation: treasury draw: treasury LP units < deltaLP, capping",
				"asset", h.asset, "treasury_units", treasuryLP.Units, "delta_lp", deltaLP)
			deltaLP = treasuryLP.Units
			// Recompute deltaA, deltaR from the capped deltaLP.
			deltaA = common.GetSafeShare(deltaLP, pool.LPUnits, pool.BalanceAsset)
			deltaR = common.GetSafeShare(deltaA, pool.BalanceAsset, pool.BalanceRune)
		}
		treasuryLP.Units = common.SafeSub(treasuryLP.Units, deltaLP)
		m.mgr.Keeper().SetLiquidityProvider(ctx, treasuryLP)
		pool.LPUnits = common.SafeSub(pool.LPUnits, deltaLP)
		pool.BalanceAsset = common.SafeSub(pool.BalanceAsset, deltaA)
		pool.BalanceRune = common.SafeSub(pool.BalanceRune, deltaR)
		if err := m.mgr.Keeper().SetPool(ctx, pool); err != nil {
			return fmt.Errorf("conciliation: treasury draw: fail to save pool %s: %w", h.asset, err)
		}
		// Burn ΔR from Asgard (bank pairing: Asgard→thorchain→burn).
		runeCoin := common.NewCoin(common.RuneNative, deltaR)
		if err := m.mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ModuleName, common.NewCoins(runeCoin)); err != nil {
			return fmt.Errorf("conciliation: treasury draw: fail to move RUNE from Asgard for %s: %w", h.asset, err)
		}
		if err := m.mgr.Keeper().BurnFromModule(ctx, ModuleName, runeCoin); err != nil {
			return fmt.Errorf("conciliation: treasury draw: fail to burn RUNE for %s: %w", h.asset, err)
		}
		mod := NewPoolMod(h.asset, deltaR, false, deltaA, false)
		emitPoolBalanceChangedEvent(ctx, mod, "conciliation_treasury_draw", m.mgr)
		burnEvt := NewEventMintBurn(BurnSupplyType, runeCoin.Asset.Native(), runeCoin.Amount, "conciliation_treasury_draw")
		if err := m.mgr.EventMgr().EmitEvent(ctx, burnEvt); err != nil {
			ctx.Logger().Error("conciliation: treasury draw: fail to emit burn event", "error", err, "asset", h.asset)
		}
		totalStep4PlannedRune = totalStep4PlannedRune.Add(plannedR)
		totalStep4DrawnRune = totalStep4DrawnRune.Add(deltaR)
		ctx.Logger().Info("conciliation: treasury draw: treasury LP draw applied",
			"asset", h.asset, "delta_asset", deltaA, "delta_rune", deltaR, "delta_lp", deltaLP)
	}
	ctx.Logger().Info("conciliation: treasury draw: coverage summary",
		"total_planned_rune", totalStep4PlannedRune,
		"total_drawn_rune", totalStep4DrawnRune,
		"uncovered_rune", common.SafeSub(totalStep4PlannedRune, totalStep4DrawnRune))

	return nil
}
