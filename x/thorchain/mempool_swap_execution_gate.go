package thorchain

import (
	"errors"
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

var errMempoolSwapPoolExecutionPending = errors.New("swap pool is reserved by another mempool swap lifecycle")

// mempoolSwapLegacyPoolGateCache memoizes pool-isolation results for one
// legacy-queue EndBlock pass. Normal-mode legacy swap execution can mutate
// canonical pool balances, but it cannot mutate mempool lifecycle records,
// owner indexes, or speculative overlays; admission, finalization, rollback,
// and recovery all happen outside this loop. The cache must therefore remain
// pass-scoped so the next EndBlock observes any lifecycle transition.
//
// Failures are cached as deliberately as availability: a missing owner with a
// live overlay must remain fail-closed, while a blocked pool must not force
// repeated keeper reads as the queue skips forward to unrelated pools.
// SwapHandler still performs its own uncached isolation check at the mutation
// boundary; this cache is only the queue's pre-attempt filter.
type mempoolSwapLegacyPoolGateCache map[string]error

func newMempoolSwapLegacyPoolGateCache() mempoolSwapLegacyPoolGateCache {
	return make(mempoolSwapLegacyPoolGateCache)
}

func (cache mempoolSwapLegacyPoolGateCache) ensureExecutionPoolsAvailable(
	ctx cosmos.Context,
	k keeper.Keeper,
	msg MsgSwap,
) error {
	pools, err := mempoolSwapExecutionPools(msg)
	if err != nil {
		return err
	}
	for _, asset := range pools {
		asset = asset.GetLayer1Asset()
		key := strings.ToUpper(asset.String())
		poolErr, ok := cache[key]
		if !ok {
			poolErr = ensureMempoolSwapExecutionPoolAvailable(ctx, k, asset, nil)
			cache[key] = poolErr
		}
		if poolErr != nil {
			return poolErr
		}
	}
	return nil
}

// mempoolSwapQueueAdmissionContextKey marks validation performed solely to
// persist a swap in the advanced queue. Admission may defer the expected
// pool-owner conflict; all other isolation errors remain fatal.
type mempoolSwapQueueAdmissionContextKey struct{}

func withMempoolSwapQueueAdmission(ctx cosmos.Context) cosmos.Context {
	return ctx.WithValue(mempoolSwapQueueAdmissionContextKey{}, true)
}

func isMempoolSwapQueueAdmission(ctx cosmos.Context) bool {
	admission, _ := ctx.Value(mempoolSwapQueueAdmissionContextKey{}).(bool)
	return admission
}

// mempoolSwapExecutionPools returns every AMM pool touched by a swap. It uses
// the same layer-1 normalization as swapOne and preserves derived pools, whose
// THOR-native asset is itself the virtual pool key.
func mempoolSwapExecutionPools(msg MsgSwap) ([]common.Asset, error) {
	if len(msg.Tx.Coins) != 1 {
		return nil, fmt.Errorf("swap must have exactly one source coin")
	}
	source := msg.Tx.Coins[0].Asset.GetLayer1Asset()
	target := msg.TargetAsset.GetLayer1Asset()

	var pools []common.Asset
	switch {
	case source.IsRune():
		pools = append(pools, target)
	case target.IsRune():
		pools = append(pools, source)
	default:
		pools = append(pools, source)
		if !target.Equals(source) {
			pools = append(pools, target)
		}
	}
	return pools, nil
}

// ensureMempoolSwapExecutionPoolsAvailable serializes speculative pool
// mutations by lifecycle owner. An active owner may continue its own stream;
// after finality its zero-overlay tombstone keeps a different speculative
// lifecycle out until terminal deletion. The tombstone's own continuation and
// ordinary swaps without a lifecycle remain safe to execute.
//
// GetMempoolSwapPoolOwner validates the canonical record against both active
// aggregate overlays. If an aggregate somehow exists without its derived owner
// index, fail closed instead of allowing an irreversible consumer through.
func ensureMempoolSwapExecutionPoolsAvailable(
	ctx cosmos.Context,
	k keeper.Keeper,
	msg MsgSwap,
	owner *types.MempoolSwap,
) error {
	pools, err := mempoolSwapExecutionPools(msg)
	if err != nil {
		return err
	}
	for _, asset := range pools {
		if err := ensureMempoolSwapExecutionPoolAvailable(ctx, k, asset, owner); err != nil {
			return err
		}
	}
	return nil
}

func ensureMempoolSwapExecutionPoolAvailable(
	ctx cosmos.Context,
	k keeper.Keeper,
	asset common.Asset,
	owner *types.MempoolSwap,
) error {
	indexedOwner, found, err := k.GetMempoolSwapPoolOwner(ctx, asset)
	if err != nil {
		return fmt.Errorf("fail to load mempool swap pool ownership for %s: %w", asset, err)
	}
	if found {
		if owner != nil && owner.TxId.Equals(indexedOwner.TxId) && owner.Index == indexedOwner.Index {
			return nil
		}
		if owner == nil && indexedOwner.Status == types.MempoolSwapStatus_FINALIZED {
			return nil
		}
		return fmt.Errorf("%w: %s", errMempoolSwapPoolExecutionPending, asset)
	}

	poolOverlay, err := k.GetMempoolSwapPoolBalanceOverlay(ctx, asset)
	if err != nil {
		return fmt.Errorf("fail to load mempool swap pool ownership for %s: %w", asset, err)
	}
	synthOverlay, err := k.GetMempoolSwapNativeSupplyOverlay(ctx, asset.GetLayer1Asset().GetSyntheticAsset())
	if err != nil {
		return fmt.Errorf("fail to load mempool swap synth ownership for %s: %w", asset, err)
	}
	if mempoolSwapPoolBalanceOverlayIsZero(poolOverlay) && mempoolSwapNativeSupplyOverlayIsZero(synthOverlay) {
		return nil
	}
	return fmt.Errorf("mempool swap pool %s has an active overlay without an owner", asset)
}

func mempoolSwapExecutionOwner(
	ctx cosmos.Context,
	k keeper.Keeper,
	mode mempoolSwapExecutionMode,
) (*types.MempoolSwap, error) {
	if mode != mempoolSwapExecutionSpeculative {
		return nil, nil
	}
	identity, ok := getMempoolSwapExecutionIdentity(ctx)
	if !ok {
		return nil, fmt.Errorf("speculative swap is missing its lifecycle identity")
	}
	record, found, err := k.GetMempoolSwap(ctx, identity.txID, identity.index)
	if err != nil {
		return nil, fmt.Errorf("fail to load speculative swap lifecycle record: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("speculative swap lifecycle record not found: %s/%d", identity.txID, identity.index)
	}
	return &record, nil
}

// ensureSwapExecutionFinalityIsolation is the mutation-boundary defense for
// callers outside the advanced queue. Queue readiness performs the same check
// so ordinary deferred swaps are not counted as failed attempts.
func ensureSwapExecutionFinalityIsolation(ctx cosmos.Context, mgr Manager, msg MsgSwap) error {
	mode := getMempoolSwapExecutionMode(ctx)
	if mode == mempoolSwapExecutionRollback {
		// Rollback runs atomically after removing its own overlay. Per-pool
		// serialization guarantees no foreign owner could have entered meanwhile.
		return nil
	}
	owner, err := mempoolSwapExecutionOwner(ctx, mgr.Keeper(), mode)
	if err != nil {
		return err
	}
	err = ensureMempoolSwapExecutionPoolsAvailable(ctx, mgr.Keeper(), msg, owner)
	if isMempoolSwapQueueAdmission(ctx) && errors.Is(err, errMempoolSwapPoolExecutionPending) {
		return nil
	}
	return err
}
