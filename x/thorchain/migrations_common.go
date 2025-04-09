package thorchain

import (
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Migrate4to5 migrates from version 4 to 5.
func (m Migrator) ClearObsoleteMimirs(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// Issue #2112, clearing obsolete Mimir keys.

	toClear := func(key string) bool {
		upperKey := strings.ToUpper(key)
		return (strings.Contains(upperKey, "BNB") && !strings.Contains(upperKey, "BSC")) || // Do not clear BSC-BNB keys.
			strings.Contains(upperKey, "TERRA") ||
			strings.Contains(upperKey, "YGG") ||
			strings.EqualFold(key, "MaxConfirmations") || // Only effective with -<Chain> .
			strings.EqualFold(key, "ConfMultiplierBasisPoints") || // Only effective with -<Chain> .
			strings.EqualFold(key, "SystemIncomeBurnRateBp") // Only Bps effective, not Bp.
	}

	iterNode := m.mgr.Keeper().GetNodeMimirIterator(ctx)
	defer iterNode.Close()
	for ; iterNode.Valid(); iterNode.Next() {
		key := trimKeyPrefix(iterNode.Key())

		if !toClear(key) {
			continue
		}

		// As with PurgeOperationalNodeMimirs,
		// not emitting individual EventSetNodeMimir events.
		m.mgr.Keeper().DeleteNodeMimirs(ctx, key)
	}

	iter := m.mgr.Keeper().GetMimirIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key := trimKeyPrefix(iter.Key())

		if !toClear(key) {
			continue
		}

		if err := m.mgr.Keeper().DeleteMimir(ctx, key); err != nil {
			ctx.Logger().Error("fail to delete mimir", "key", key, "error", err)
			continue
		}

		// As with Admin key deletion, emit a deletion event.
		mimirEvent := NewEventSetMimir(strings.ToUpper(key), "-1")
		if err := m.mgr.EventMgr().EmitEvent(ctx, mimirEvent); err != nil {
			ctx.Logger().Error("fail to emit set_mimir event", "error", err)
		}
	}

	return nil
}

func (m Migrator) UpdateMimirsFor2sBlockTime(ctx sdk.Context) {
	k := m.mgr.Keeper()
	// Block time changed from ~6s to ~2s (3x faster)
	// Update all time-based constants

	// Main block timing constant
	k.SetMimir(ctx, "BlocksPerYear", 15768000)

	// Pool and liquidity timing
	k.SetMimir(ctx, "PoolCycle", 129600)
	k.SetMimir(ctx, "PendingLiquidityAgeLimit", 302400)

	// Anchor and dynamic calculations
	k.SetMimir(ctx, "MaxAnchorBlocks", 900)
	k.SetMimir(ctx, "DynamicMaxAnchorSlipBlocks", 604800)
	k.SetMimir(ctx, "DynamicMaxAnchorCalcInterval", 43200)

	// Fund and vault migration
	k.SetMimir(ctx, "FundMigrationInterval", 1080)

	// Churn-related intervals
	k.SetMimir(ctx, "ChurnInterval", 129600)
	k.SetMimir(ctx, "ChurnRetryInterval", 2160)
	k.SetMimir(ctx, "ChurnOutForLowVersionBlocks", 64800)

	// Transaction signing and validation
	k.SetMimir(ctx, "SigningTransactionPeriod", 900)
	k.SetMimir(ctx, "DoubleSignMaxAge", 72)

	// Slashing and penalties
	k.SetMimir(ctx, "FailKeygenSlashPoints", 2160)
	k.SetMimir(ctx, "ObservationDelayFlexibility", 30)

	// Jailing durations
	k.SetMimir(ctx, "JailTimeKeygen", 12960)
	k.SetMimir(ctx, "JailTimeKeysign", 180)

	// Node operations
	k.SetMimir(ctx, "NodePauseChainBlocks", 2160)
	k.SetMimir(ctx, "CloutReset", 2160)

	// Streaming swap timings
	k.SetMimir(ctx, "StreamingSwapMaxLength", 43200)
	k.SetMimir(ctx, "StreamingSwapMaxLengthNative", 15768000)

	// Transaction output timings
	k.SetMimir(ctx, "TxOutDelayMax", 51840)
	k.SetMimir(ctx, "MaxTxOutOffset", 2160)

	// TNS fees - divided by 3 (rounded up)
	k.SetMimir(ctx, "TNSFeePerBlock", 7)
	k.SetMimir(ctx, "TNSFeePerBlockUSD", 7)

	// RUNE Pool maturity blocks
	k.SetMimir(ctx, "RUNEPoolDepositMaturityBlocks", 3888000)
}
