package thorchain

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// adrMimirPrefix marks a mimir key as an ADR vote, which carries the required-voting
// rules: once 1/3+1 of the active set has voted, the remaining active nodes are at risk
// of being churned out and standby nodes cannot churn in until they vote.
const adrMimirPrefix = "ADR"

// nodeMimirKeyPrefix is the store key prefix trimmed off node mimir iterator keys. The
// existing mimir queriers use the same literal (querier.go queryMimirNodesValues).
const nodeMimirKeyPrefix = "nodemimir//"

// isADRMimir reports whether a mimir key is subject to the required-voting rules.
func isADRMimir(key string) bool {
	return strings.HasPrefix(strings.ToUpper(key), adrMimirPrefix)
}

// standbyMimirCost is the bond charged to a non-active node for each ADR mimir vote.
func standbyMimirCost(ctx cosmos.Context, k keeper.Keeper) cosmos.Uint {
	return cosmos.NewUint(uint64(k.GetConfigInt64(ctx, constants.StandbyMimirCost)))
}

// adrKeyHasActiveVote reports whether any currently active node has voted on the key.
// Non-active nodes may only vote on keys that pass this, so they cannot create new ADR
// keys of their own.
func adrKeyHasActiveVote(ctx cosmos.Context, k keeper.Keeper, key string) bool {
	activeNodes, err := k.ListActiveValidators(ctx)
	if err != nil {
		ctx.Logger().Error("fail to list active validators", "error", err)
		return false
	}
	mimirs, err := k.GetNodeMimirs(ctx, key)
	if err != nil {
		ctx.Logger().Error("fail to get node mimirs", "error", err, "key", key)
		return false
	}
	return mimirs.VotersOf(key, activeNodes.GetNodeAddresses()) > 0
}

// adrVote is an ADR mimir key together with the votes cast on it.
type adrVote struct {
	Key    string
	Mimirs NodeMimirs
}

// pendingADRVotes returns the ADR mimir keys currently in the required-voting state, in
// store (byte) order. A key is pending when at least 1/3+1 of the active set has voted,
// not every active node has voted, and no 2/3 supermajority has formed on a single value.
//
// This is the single source of truth for both the churn-out penalty and the churn-in
// preflight gate; they must not derive it separately or they will drift apart.
func pendingADRVotes(ctx cosmos.Context, k keeper.Keeper) ([]adrVote, error) {
	if k.GetConfigInt64(ctx, constants.RequiredVotingEnabled) <= 0 {
		return nil, nil
	}

	activeNodes, err := k.ListActiveValidators(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to list active validators: %w", err)
	}
	active := activeNodes.GetNodeAddresses()
	if len(active) == 0 {
		return nil, nil
	}

	var pending []adrVote
	iter := k.GetNodeMimirIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key := strings.TrimPrefix(string(iter.Key()), nodeMimirKeyPrefix)
		if !isADRMimir(key) {
			continue
		}
		mimirs := NodeMimirs{}
		if err := k.Cdc().Unmarshal(iter.Value(), &mimirs); err != nil {
			return nil, fmt.Errorf("fail to unmarshal node mimir %s: %w", key, err)
		}

		voters := mimirs.VotersOf(key, active)
		// voters*3 > len(active) is exactly floor(n/3)+1, matching the integer style of
		// HasSuperMajority.
		if voters*3 <= len(active) {
			continue // not enough turnout to make voting required yet
		}
		if voters >= len(active) {
			continue // everyone has voted, nothing left to require
		}
		if _, ok := mimirs.HasSuperMajority(key, active); ok {
			continue // consensus formed, requirement clears for everyone
		}
		pending = append(pending, adrVote{Key: key, Mimirs: mimirs})
	}

	return pending, nil
}

// nodeHasPendingADRVote returns the first pending ADR mimir key this node has not voted
// on. Used to keep non-voting standby nodes out of the next churn.
func nodeHasPendingADRVote(ctx cosmos.Context, k keeper.Keeper, addr cosmos.AccAddress) (string, bool) {
	pending, err := pendingADRVotes(ctx, k)
	if err != nil {
		ctx.Logger().Error("fail to get pending ADR votes", "error", err)
		return "", false
	}
	for _, v := range pending {
		if !v.Mimirs.Has(v.Key, addr) {
			return v.Key, true
		}
	}
	return "", false
}
