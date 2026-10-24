package keeperv1

import (
	"math"

	storetypes "cosmossdk.io/store/types"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// GetConstants returns the constant values
func (k KVStore) GetConstants() constants.ConstantValues {
	return k.constAccessor
}

// GetConfigInt64 returns the mimir value for the key if set, otherwise the
// constant value, converted from reference blocks to blocks at the current
// block time (see ScaleConfigInt64).
func (k KVStore) GetConfigInt64(ctx cosmos.Context, key constants.ConstantName) int64 {
	return k.ScaleConfigInt64(ctx, key, k.GetConfigInt64Ref(ctx, key))
}

// GetConfigInt64Ref is GetConfigInt64 in reference blocks, before scaling.
func (k KVStore) GetConfigInt64Ref(ctx cosmos.Context, key constants.ConstantName) int64 {
	val, err := k.GetMimir(ctx, key.String())
	if val < 0 || err != nil {
		val = k.GetConstants().GetInt64Value(key)
		if err != nil {
			ctx.Logger().Error("fail to get mimir", "key", key.String(), "error", err)
		}
	}
	return val
}

// blockTimeTargetMs returns the BLOCKTIMEMS target: the mimir clamped to
// [BlockTimeMsMin, BlockTimeMsMax], or the network's default ThorchainBlockTime
// while the mimir is unset or zero.
func (k KVStore) blockTimeTargetMs(ctx cosmos.Context) int64 {
	v, err := k.GetMimir(ctx, constants.BlockTimeMs.String())
	if err != nil || v <= 0 {
		return constants.ThorchainBlockTime.Milliseconds()
	}
	return min(max(v, constants.BlockTimeMsMin), constants.BlockTimeMsMax)
}

// GetBlockTimeMs returns the effective block time in milliseconds: the
// BLOCKTIMEMS target as latched by UpdateBlockTime at the start of this block,
// or the network's default ThorchainBlockTime if it was never changed. It
// reads without charging gas, so the scaling it drives never changes a tx's
// gas_used.
func (k KVStore) GetBlockTimeMs(ctx cosmos.Context) int64 {
	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	var ms int64
	if found, err := k.getInt64(ctx, k.GetKey(prefixBlockTime, "ms"), &ms); err != nil || !found || ms <= 0 {
		return constants.ThorchainBlockTime.Milliseconds()
	}
	return ms
}

// GetBlockTimeHistory returns every change of the effective block time,
// oldest first, or nil if it never changed. It reads without charging gas, like
// GetBlockTimeMs.
func (k KVStore) GetBlockTimeHistory(ctx cosmos.Context) constants.BlockTimeHistory {
	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	store := k.kvStore(ctx)
	key := k.GetKey(prefixBlockTime, "history")
	if !store.Has(key) {
		return nil
	}
	var record BlockTimeHistory
	if err := k.cdc.Unmarshal(store.Get(key), &record); err != nil {
		_ = dbError(ctx, "Unmarshal kvstore: block time history", err)
		return nil
	}
	history := make(constants.BlockTimeHistory, 0, len(record.Segments))
	for _, seg := range record.Segments {
		history = append(history, constants.BlockTimeSegment{Height: seg.Height, Ms: seg.Ms})
	}
	return history
}

// GetBlockTimeChange returns the height at which the effective block time last
// changed and the block time before it, or zeros if it never changed.
func (k KVStore) GetBlockTimeChange(ctx cosmos.Context) (height, prevMs int64) {
	history := k.GetBlockTimeHistory(ctx)
	if len(history) == 0 {
		return 0, 0
	}
	prevMs = constants.ThorchainBlockTime.Milliseconds()
	if len(history) > 1 {
		prevMs = history[len(history)-2].Ms
	}
	return history[len(history)-1].Height, prevMs
}

// UpdateBlockTime latches the BLOCKTIMEMS target as the effective block time
// for this block and the rest of the chain until it changes again, records the
// change in the block time history, and converts the deadlines stored as
// future heights to the new block time (convertStoredHeights). It runs at the
// start of BeginBlock, so a mimir set during block H takes effect from H+1 and
// every read within a block sees one block time. It writes only on a change,
// so nothing is stored while BLOCKTIMEMS is unset. Changes are rate limited
// (rateLimitBlockTime): the target may be reached in steps.
func (k KVStore) UpdateBlockTime(ctx cosmos.Context) {
	current := k.GetBlockTimeMs(ctx)
	target := k.rateLimitBlockTime(ctx, current, k.blockTimeTargetMs(ctx))
	if target == current {
		return
	}
	k.setInt64(ctx, k.GetKey(prefixBlockTime, "ms"), target)
	var record BlockTimeHistory
	for _, seg := range k.GetBlockTimeHistory(ctx) {
		record.Segments = append(record.Segments, BlockTimeSegment{Height: seg.Height, Ms: seg.Ms})
	}
	record.Segments = append(record.Segments, BlockTimeSegment{Height: ctx.BlockHeight(), Ms: target})
	k.kvStore(ctx).Set(k.GetKey(prefixBlockTime, "history"), k.cdc.MustMarshal(&record))
	k.convertStoredHeights(ctx, current, target)
	ctx.Logger().Info("block time changed", "from_ms", current, "to_ms", target)
}

// SetBlockTimeHistory restores the latched block time and its history, as
// exported by genesis, without converting any stored heights.
func (k KVStore) SetBlockTimeHistory(ctx cosmos.Context, ms int64, history constants.BlockTimeHistory) {
	if ms <= 0 || len(history) == 0 {
		return
	}
	k.setInt64(ctx, k.GetKey(prefixBlockTime, "ms"), ms)
	var record BlockTimeHistory
	for _, seg := range history {
		record.Segments = append(record.Segments, BlockTimeSegment{Height: seg.Height, Ms: seg.Ms})
	}
	k.kvStore(ctx).Set(k.GetKey(prefixBlockTime, "history"), k.cdc.MustMarshal(&record))
}

// rateLimitBlockTime returns the block time to latch on the way from current
// to target. A step back toward the default ThorchainBlockTime, without going
// past it, applies at once, so a change can always be undone quickly. Any
// other step waits until ChurnInterval of wall-clock time has passed since the
// previous change, and goes at most half or double the current block time.
func (k KVStore) rateLimitBlockTime(ctx cosmos.Context, current, target int64) int64 {
	if target == current {
		return current
	}
	refMs := constants.ThorchainBlockTime.Milliseconds()
	if (current < target && target <= refMs) || (current > target && target >= refMs) {
		return target
	}
	if history := k.GetBlockTimeHistory(ctx); len(history) > 0 {
		last := history[len(history)-1].Height
		if k.RefBlocksSince(ctx, last) < k.GetConfigInt64Ref(ctx, constants.ChurnInterval) {
			return current
		}
	}
	return min(max(target, (current+1)/2), current*2)
}

// RefWindowStart returns the oldest height still inside a window of refBlocks
// reference blocks of wall-clock time that ends at the previous block: the
// first height a signing-period scan must cover. Across a block time change it
// measures the window in wall-clock time, so an outbound scheduled under a
// faster block time is still covered; without one it is now - refBlocks
// scaled to the current block time. The result can be below 1, and above now
// for a negative refBlocks: refBlocks <= 0 is plain now - refBlocks.
func (k KVStore) RefWindowStart(ctx cosmos.Context, refBlocks int64) int64 {
	if refBlocks <= 0 {
		return ctx.BlockHeight() - refBlocks
	}
	refMs := constants.ThorchainBlockTime.Milliseconds()
	if refBlocks > math.MaxInt64/refMs {
		return 1
	}
	return k.GetBlockTimeHistory(ctx).HeightAgo(ctx.BlockHeight()-1, refBlocks*refMs, refMs) + 1
}

// RefWindowEnd returns the furthest height a window of refBlocks reference
// blocks can have scheduled to from any block so far: outbounds scheduled under
// a faster block time can sit further ahead than the current window. Without a
// block time change it is now + refBlocks scaled to the current block time.
func (k KVStore) RefWindowEnd(ctx cosmos.Context, refBlocks int64) int64 {
	return k.GetBlockTimeHistory(ctx).Reach(ctx.BlockHeight(), refBlocks, constants.ThorchainBlockTime.Milliseconds())
}

// ScaleConfigInt64 converts v, a value of key in reference blocks (blocks of
// the default ThorchainBlockTime), to blocks at the current block time by the
// key's unit: durations stretch, per-block rates shrink, counts and heights
// pass through. It is the identity while BLOCKTIMEMS is unset. Use it on
// values of key read without GetConfigInt64.
func (k KVStore) ScaleConfigInt64(ctx cosmos.Context, key constants.ConstantName, v int64) int64 {
	unit := key.Unit()
	if !unit.Scales() {
		return v
	}
	return unit.Scale(v, constants.ThorchainBlockTime.Milliseconds(), k.GetBlockTimeMs(ctx))
}

// RefBlocks converts a duration of refBlocks reference blocks to blocks at
// the current block time, rounding up; values <= 0 pass through. Hardcoded
// block counts that stand for a wall-clock span go through it.
func (k KVStore) RefBlocks(ctx cosmos.Context, refBlocks int64) int64 {
	return constants.ScaleDuration(refBlocks, constants.ThorchainBlockTime.Milliseconds(), k.GetBlockTimeMs(ctx))
}
