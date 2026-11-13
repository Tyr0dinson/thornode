package thorchain

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// API simulations snapshot pools with their stored bytes and find the pools a
// swap touched from one iteration; the result must be the set the marshal
// comparison (kept for contract simulations) finds.
func TestSimulateTouchedPoolsByBytes(t *testing.T) {
	ctx, mgr := setupBenchmarkManagerWith(t, false)
	k := mgr.Keeper()
	assets := []common.Asset{common.BTCAsset, common.ETHAsset, common.BCHAsset, common.LTCAsset, common.DOGEAsset}
	for i, asset := range assets {
		pool := NewPool()
		pool.Asset = asset
		pool.Status = PoolAvailable
		pool.BalanceRune = cosmos.NewUint(uint64(i+1) * 1000 * common.One)
		pool.BalanceAsset = cosmos.NewUint(uint64(i+1) * 10 * common.One)
		pool.LPUnits = cosmos.NewUint(uint64(i+1) * common.One)
		require.NoError(t, k.SetPool(ctx, pool))
	}

	byMarshal := func(ctx cosmos.Context, pools Pools) []common.Asset {
		var touched []common.Asset
		for i := range pools {
			current, err := k.GetPool(ctx, pools[i].Asset)
			require.NoError(t, err)
			if !bytes.Equal(k.Cdc().MustMarshal(&current), k.Cdc().MustMarshal(&pools[i])) {
				touched = append(touched, pools[i].Asset)
			}
		}
		return touched
	}
	assetsOf := func(pools []Pool) []common.Asset {
		var out []common.Asset
		for _, p := range pools {
			out = append(out, p.Asset)
		}
		return out
	}

	for _, tc := range []struct {
		name   string
		mutate func(ctx cosmos.Context)
	}{
		{"none", func(cosmos.Context) {}},
		{"swap", func(ctx cosmos.Context) {
			for _, asset := range []common.Asset{common.BTCAsset, common.ETHAsset} {
				pool, err := k.GetPool(ctx, asset)
				require.NoError(t, err)
				pool.BalanceRune = pool.BalanceRune.Add(cosmos.NewUint(common.One))
				require.NoError(t, k.SetPool(ctx, pool))
			}
		}},
		{"rewrite-same", func(ctx cosmos.Context) {
			pool, err := k.GetPool(ctx, common.LTCAsset)
			require.NoError(t, err)
			require.NoError(t, k.SetPool(ctx, pool))
		}},
		{"new-and-removed", func(ctx cosmos.Context) {
			pool := NewPool()
			pool.Asset = common.AVAXAsset
			pool.BalanceRune = cosmos.NewUint(common.One)
			require.NoError(t, k.SetPool(ctx, pool))
			k.RemovePool(ctx, common.DOGEAsset)
		}},
		{"last-removed", func(ctx cosmos.Context) {
			k.RemovePool(ctx, common.LTCAsset)
			pool, err := k.GetPool(ctx, common.BCHAsset)
			require.NoError(t, err)
			pool.Status = types.PoolStatus_Staged
			require.NoError(t, k.SetPool(ctx, pool))
		}},
	} {
		mutate := tc.mutate
		t.Run(tc.name, func(t *testing.T) {
			cctx, _ := ctx.CacheContext()
			want, err := k.GetPools(cctx)
			require.NoError(t, err)
			pools, keys, values, err := snapshotPools(cctx, k)
			require.NoError(t, err)
			require.Equal(t, want, pools)
			require.Len(t, keys, len(pools))
			mutate(cctx)
			require.Equal(t, byMarshal(cctx, want), assetsOf(touchedPoolsByBytes(cctx, k, pools, keys, values)))
		})
	}

	// a pool that does not decode fails the snapshot with GetPools' error
	cctx, _ := ctx.CacheContext()
	cctx.KVStore(keyThornodeBench).Set(k.GetKey("pool/", "BTC.ZZZ"), []byte{0x0a, 0x01})
	_, wantErr := k.GetPools(cctx)
	require.Error(t, wantErr)
	_, _, _, err := snapshotPools(cctx, k)
	require.EqualError(t, err, wantErr.Error())
}

// API and contract simulations take different store paths (pool snapshot
// diff vs point reads) but must emit the same events, here on a
// mainnet-shaped state with a mainnet-sized swap queue.
func TestSimulateAPIMatchesContract(t *testing.T) {
	f := newPerfFixture(t)
	height := f.ctx.BlockHeight() + 1
	f.seedPendingOutbounds(t, height)
	f.seedSwapQueue(t, height)
	for _, tc := range []struct {
		name               string
		from, to           common.Asset
		amount             uint64
		quantity, interval uint64
	}{
		{"l1-to-l1", common.BTCAsset, common.ETHAsset, common.One / 10, 0, 0},
		{"streaming", common.BTCAsset, common.ETHAsset, common.One / 20, 20, 1},
		{"rune-to-l1", common.RuneAsset(), common.BTCAsset, 1000 * common.One, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := func(wasm bool) string {
				ctx := f.branch(height)
				f.mgr.gasMgr.BeginBlock()
				f.mgr.obMgr = newObserverMgr()
				if wasm {
					ctx = ctx.WithValue(constants.CtxWASMQuery, true)
				}
				fromChain := tc.from.GetChain()
				to := perfAddr(t, tc.to.GetChain(), "sim/to")
				msg := NewMsgSwap(common.Tx{
					ID:          common.BlankTxID,
					Chain:       fromChain,
					FromAddress: perfAddr(t, fromChain, "sim/from"),
					ToAddress:   common.NoopAddress,
					Coins:       common.NewCoins(common.NewCoin(tc.from, cosmos.NewUint(tc.amount))),
					Gas:         common.Gas{common.NewCoin(common.RuneAsset(), cosmos.NewUint(1))},
					Memo:        "=:" + tc.to.String() + ":" + to.String(),
				}, tc.to, to, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
					"", "", nil, types.SwapType_market, tc.quantity, tc.interval, types.SwapVersion_v2, nil)
				events, err := simulate(ctx, f.mgr, msg)
				require.NoError(t, err)
				bz, err := json.Marshal(events)
				require.NoError(t, err)
				return string(bz)
			}
			api := run(false)
			require.Contains(t, api, `"swap"`)
			require.Equal(t, run(true), api)
		})
	}
}
