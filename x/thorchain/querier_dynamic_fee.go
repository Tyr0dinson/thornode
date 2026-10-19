package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// uintOrZero normalizes a Uint that unmarshalled from a record written before
// the field existed, where the inner big.Int is nil and String() would panic.
func uintOrZero(u cosmos.Uint) cosmos.Uint {
	if u.IsNil() {
		return cosmos.ZeroUint()
	}
	return u
}

// dynamicFeePhase names where a record is in its controller lifecycle: the
// bracket search, the +/-step tracking that follows it, or monitor (history
// only, no fee decisions) for a thorname in a monitor state.
func dynamicFeePhase(rec types.DynamicFeeRecord, state int64) string {
	if state == types.DynamicFeeStatePerPairMonitor || state == types.DynamicFeeStateGlobalMonitor {
		return "monitor"
	}
	if rec.SearchDone {
		return "track"
	}
	return "search"
}

func (qs queryServer) queryDynamicL1Fees(ctx cosmos.Context, req *types.QueryDynamicL1FeesRequest) (*types.QueryDynamicL1FeesResponse, error) {
	resp := &types.QueryDynamicL1FeesResponse{}

	iter := qs.mgr.Keeper().GetDynamicFeeRecordIterator(ctx)
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var rec types.DynamicFeeRecord
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}

		state := dynamicFeeWhitelistState(ctx, qs.mgr.Keeper(), rec.Thorname)
		entry := types.DynamicL1FeeEntry{
			Thorname:        rec.Thorname,
			Pair:            fmt.Sprintf("%s|%s", rec.PairAssetA, rec.PairAssetB),
			DynamicBps:      rec.DynamicBps,
			WhitelistState:  state,
			LastActiveEpoch: rec.LastActiveEpoch,
			Phase:           dynamicFeePhase(rec, state),
		}
		if latest, ok := rec.LatestHistory(); ok {
			entry.LatestFeesTor = latest.FeesTor.String()
		}
		resp.Entries = append(resp.Entries, entry)
	}

	return resp, nil
}

func (qs queryServer) queryDynamicL1FeesByThorname(ctx cosmos.Context, req *types.QueryDynamicL1FeesByThornameRequest) (*types.QueryDynamicL1FeesByThornameResponse, error) {
	thorname := req.Thorname
	state := dynamicFeeWhitelistState(ctx, qs.mgr.Keeper(), thorname)

	resp := &types.QueryDynamicL1FeesByThornameResponse{
		Thorname:       thorname,
		WhitelistState: state,
	}

	iter := qs.mgr.Keeper().GetDynamicFeeRecordIteratorForThorname(ctx, thorname)
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var rec types.DynamicFeeRecord
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}

		detail := types.DynamicL1FeePairDetail{
			Pair:            fmt.Sprintf("%s|%s", rec.PairAssetA, rec.PairAssetB),
			DynamicBps:      rec.DynamicBps,
			LastActiveEpoch: rec.LastActiveEpoch,
			Phase:           dynamicFeePhase(rec, state),
		}
		for _, pr := range rec.Probes {
			detail.Probes = append(detail.Probes, types.DynamicL1FeeProbe{
				Bps:    pr.Bps,
				Signal: pr.Mean().String(),
				Epochs: pr.Epochs,
			})
		}
		for _, h := range rec.History {
			detail.History = append(detail.History, types.DynamicL1FeeHistoryEntry{
				Epoch:            h.Epoch,
				VolumeTor:        h.VolumeTor.String(),
				FeesTor:          h.FeesTor.String(),
				BpsAtClose:       h.BpsAtClose,
				SwapCount:        h.SwapCount,
				PoolVolumeTor:    uintOrZero(h.PoolVolumeTor).String(),
				OwnPoolVolumeTor: uintOrZero(h.OwnPoolVolumeTor).String(),
				Blocks:           h.Blocks,
			})
		}
		resp.Pairs = append(resp.Pairs, detail)
	}

	return resp, nil
}

// queryDynamicL1FeesCurrent returns the live (in-progress, not yet sealed)
// per-(thorname, pair) accumulators for the current epoch. The current epoch
// is computed at query time from block height and the configured epoch length.
func (qs queryServer) queryDynamicL1FeesCurrent(ctx cosmos.Context, req *types.QueryDynamicL1FeesCurrentRequest) (*types.QueryDynamicL1FeesCurrentResponse, error) {
	resp := &types.QueryDynamicL1FeesCurrentResponse{}

	epochBlocks := qs.mgr.Keeper().GetConfigInt64(ctx, constants.L1DynamicFeeEpochBlocks)
	if epochBlocks > 0 {
		resp.Epoch = uint64(ctx.BlockHeight() / epochBlocks)
	}

	iter := qs.mgr.Keeper().GetDynamicFeeAccumulatorIterator(ctx)
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var acc types.DynamicFeeAccumulator
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &acc); err != nil {
			continue
		}
		// Pool volume the accumulator carries from rolled epochs, plus the
		// pair's in-progress slice, which is only folded in at the seal.
		// Together with swap_count this is the answer to "why hasn't the
		// controller moved" for a gated integration.
		poolVolumeTor := uintOrZero(acc.PoolVolumeTor)
		if acc.PairAssetA == types.DynamicFeeGlobalPair {
			poolVolumeTor = poolVolumeTor.Add(dynamicFeePairsPoolVolume(ctx, qs.mgr.Keeper(), acc.Pairs))
		} else if poolAcc, err := qs.mgr.Keeper().GetDynamicFeePoolAccumulator(ctx, acc.PairAssetA, acc.PairAssetB); err == nil {
			poolVolumeTor = poolVolumeTor.Add(poolAcc.VolumeTor)
		}
		// How long this span has run: the cap seals it at
		// L1DynamicFeeMaxEpochBlocks even below the liquidity gate.
		var spanBlocks int64
		if rec, err := qs.mgr.Keeper().GetDynamicFeeRecord(ctx, acc.Thorname, acc.PairAssetA, acc.PairAssetB); err == nil && rec.SpanStartHeight > 0 {
			spanBlocks = ctx.BlockHeight() - rec.SpanStartHeight
		}
		resp.Entries = append(resp.Entries, types.DynamicL1FeeCurrentEntry{
			Thorname:         acc.Thorname,
			Pair:             fmt.Sprintf("%s|%s", acc.PairAssetA, acc.PairAssetB),
			VolumeTor:        acc.VolumeTor.String(),
			FeesTor:          acc.FeesTor.String(),
			Epoch:            resp.Epoch,
			SwapCount:        acc.SwapCount,
			PoolVolumeTor:    poolVolumeTor.String(),
			OwnPoolVolumeTor: uintOrZero(acc.OwnPoolVolumeTor).String(),
			SpanBlocks:       spanBlocks,
		})
	}

	return resp, nil
}
