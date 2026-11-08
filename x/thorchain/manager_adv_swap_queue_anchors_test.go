package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	keeperv1 "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gopkg.in/check.v1"
)

// A queue of single swaps never reads the anchors before a swap writes its
// pools (only double swaps check stable-to-stable), so EndBlock fills the
// anchors cache up front; otherwise the first swap's pool write disables it and
// every DollarsPerRune of the block scans the pool store.
func (s AdvSwapQueueSuite) TestEndBlockAnchorsCacheSurvivesSingleSwaps(c *C) {
	ctx, mgr := setupManagerForTest(c)
	seedActiveAdvSwapVault(c, ctx, mgr)
	mgr.txOutStore = NewTxStoreDummy()
	book := newSwapQueueAdv(mgr.Keeper())

	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	for _, p := range []struct {
		asset        common.Asset
		rune, amount uint64
	}{
		{common.ETHAsset, 199019591474591, 2088519094783},
		{usdc, 100_000 * common.One, 50_000 * common.One},
	} {
		pool := NewPool()
		pool.Asset = p.asset
		pool.BalanceRune = cosmos.NewUint(p.rune)
		pool.BalanceAsset = cosmos.NewUint(p.amount)
		pool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	}
	mgr.Keeper().SetMimir(ctx, keeperv1.TorAnchorMimirKey(usdc), 1)

	tx := GetRandomTx()
	ethAddr := GetRandomETHAddress()
	tx.Memo = fmt.Sprintf("swap:ETH.ETH:%s", ethAddr)
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(2*common.One)))
	market := NewMsgSwap(
		tx, common.ETHAsset, ethAddr, cosmos.ZeroUint(),
		common.NoAddress, cosmos.ZeroUint(),
		"", "", nil,
		types.SwapType_market,
		0, 0, types.SwapVersion_v1, GetRandomBech32Addr(),
	)
	market.State = &types.SwapState{
		Quantity: 1,
		Deposit:  cosmos.NewUint(2 * common.One),
	}
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *market), IsNil)

	ctx = keeper.WithAnchorsCache(ctx)
	c.Assert(book.EndBlock(ctx, mgr, false), IsNil)

	// the swap ran (and wrote the ETH pool) ...
	_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, market.Tx.ID, 0)
	c.Assert(err, NotNil)
	pool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.BalanceRune.GT(cosmos.NewUint(199019591474591)), Equals, true)
	// ... and its fees read the anchors from the still-filled cache
	anchors, ok := keeper.AnchorsCacheFromContext(ctx).Get()
	c.Assert(ok, Equals, true)
	c.Assert(anchors, DeepEquals, []common.Asset{usdc})
}
