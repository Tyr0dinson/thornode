package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// GetMempoolSwapUsageTOR returns the current total TOR value of in-flight mempool swaps.
// This is a network-level value that tracks how much liquidity is currently being used
// by mempool swaps that haven't finalized yet.
func (k KVStore) GetMempoolSwapUsageTOR(ctx cosmos.Context) cosmos.Uint {
	key := k.GetKey(prefixMempoolSwapUsage, "total")
	var record cosmos.Uint
	found, _ := k.getUint(ctx, key, &record)
	if !found {
		return cosmos.ZeroUint()
	}
	return record
}

// SetMempoolSwapUsageTOR sets the current total TOR value of in-flight mempool swaps.
func (k KVStore) SetMempoolSwapUsageTOR(ctx cosmos.Context, tor cosmos.Uint) {
	key := k.GetKey(prefixMempoolSwapUsage, "total")
	k.setUint(ctx, key, tor)
}

// AddMempoolSwapUsageTOR adds to the current mempool swap usage.
// Returns the new total after addition.
func (k KVStore) AddMempoolSwapUsageTOR(ctx cosmos.Context, tor cosmos.Uint) cosmos.Uint {
	current := k.GetMempoolSwapUsageTOR(ctx)
	newTotal := current.Add(tor)
	k.SetMempoolSwapUsageTOR(ctx, newTotal)
	return newTotal
}

// SubMempoolSwapUsageTOR subtracts from the current mempool swap usage.
// Uses SafeSub to prevent underflow.
// Returns the new total after subtraction.
func (k KVStore) SubMempoolSwapUsageTOR(ctx cosmos.Context, tor cosmos.Uint) cosmos.Uint {
	current := k.GetMempoolSwapUsageTOR(ctx)
	if tor.GT(current) {
		ctx.Logger().Error("mempool swap usage underflow; clamping to zero",
			"current_tor", current,
			"subtract_tor", tor)
	}
	newTotal := common.SafeSub(current, tor)
	k.SetMempoolSwapUsageTOR(ctx, newTotal)
	return newTotal
}
