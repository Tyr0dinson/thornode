package thorchain

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

// orphanCountingKeeper counts the orphaned index entries the advanced swap
// queue's EndBlock removes.
type orphanCountingKeeper struct {
	keeper.Keeper
	marketRemovals, limitRemovals int
	itemIterators                 int
}

func (k *orphanCountingKeeper) GetAdvSwapQueueItemIterator(ctx cosmos.Context) cosmos.Iterator {
	k.itemIterators++
	return k.Keeper.GetAdvSwapQueueItemIterator(ctx)
}

func (k *orphanCountingKeeper) RemoveMarketAdvSwapQueueIndexEntry(ctx cosmos.Context, txID common.TxID, index int) error {
	k.marketRemovals++
	return k.Keeper.RemoveMarketAdvSwapQueueIndexEntry(ctx, txID, index)
}

func (k *orphanCountingKeeper) RemoveLimitAdvSwapQueueIndexEntry(ctx cosmos.Context, key []byte, txID common.TxID, index int) error {
	k.limitRemovals++
	return k.Keeper.RemoveLimitAdvSwapQueueIndexEntry(ctx, key, txID, index)
}

// A quote simulates its swap on its own, so it deletes every queued swap first.
// For a query the queue's indexes must go too: otherwise the simulated EndBlock
// finds every entry orphaned and removes them one at a time, and each removal
// decodes and rewrites the whole market index. That is quadratic in the queue
// size, on every quote.
func (s *QuotesSuite) TestQuoteSwapClearsQueuedSwapIndexes(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}

	c.Assert(mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	vault := NewVaultV2(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.BTCChain}.Strings(), nil, GetRandomEd25519PubKey())
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(1_000_000_000_000)
	pool.BalanceRune = cosmos.NewUint(10_000_000_000_000)
	pool.LPUnits = cosmos.NewUint(100)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.BTCChain, NewNetworkFee(common.BTCChain, 1000, 10)), IsNil)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.L1SlipMinBps.String(), 1)

	req := &types.QueryQuoteSwapRequest{
		FromAsset:   common.BTCAsset.String(),
		ToAsset:     common.RuneNative.String(),
		Amount:      "100000000",
		Destination: GetRandomTHORAddress().String(),
	}
	alone, err := qs.queryQuoteSwap(ctx, req)
	c.Assert(err, IsNil)

	// queue market and limit swaps on the quoted pair, as mainnet has
	const queued = 50
	for i := 0; i < queued; i++ {
		tx := GetRandomTx()
		tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)))
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, MsgSwap{
			Tx:          tx,
			TargetAsset: common.RuneNative,
			SwapType:    types.SwapType_market,
		}), IsNil)

		tx = GetRandomTx()
		tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)))
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, MsgSwap{
			Tx:          tx,
			TargetAsset: common.RuneNative,
			TradeTarget: cosmos.NewUint(uint64(1000+i) * common.One),
			SwapType:    types.SwapType_limit,
		}), IsNil)
	}

	counting := &orphanCountingKeeper{Keeper: mgr.Keeper()}
	mgr.advSwapQueue = newSwapQueueAdv(counting)

	quote, err := qs.queryQuoteSwap(ctx, req)
	c.Assert(err, IsNil)
	c.Check(counting.marketRemovals, Equals, 0)
	c.Check(counting.limitRemovals, Equals, 0)
	// the queued swaps never took part in the simulation
	c.Check(quote.ExpectedAmountOut, Equals, alone.ExpectedAmountOut)

	// and the simulation left the real queue alone
	items, err := mgr.Keeper().GetAdvSwapQueueIndex(ctx, MsgSwap{SwapType: types.SwapType_market})
	c.Assert(err, IsNil)
	c.Check(items, HasLen, queued)

	// Inside a block (a contract calling QuoteSwap) and in transaction
	// simulation, the orphan removals stay: they are gas, and gas_used is
	// consensus-visible.
	for _, mode := range []sdk.ExecMode{sdk.ExecModeFinalize, sdk.ExecModeSimulate} {
		counting.marketRemovals, counting.limitRemovals = 0, 0
		inTx, err := qs.queryQuoteSwap(ctx.WithExecMode(mode), req)
		c.Assert(err, IsNil, Commentf("exec mode %d", mode))
		c.Check(counting.marketRemovals, Equals, queued, Commentf("exec mode %d", mode))
		c.Check(inTx.ExpectedAmountOut, Equals, alone.ExpectedAmountOut, Commentf("exec mode %d", mode))
	}
}

// After each simulated end-block pass a quote asks whether the queue has
// drained. Iterating the queue items for that has to skip every queued swap the
// simulation deleted, so for a query it asks the indexes instead, and iterates
// the items only to delete them.
func (s *QuotesSuite) TestQuoteChecksDrainedQueueByIndex(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}

	c.Assert(mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	vault := NewVaultV2(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.BTCChain}.Strings(), nil, GetRandomEd25519PubKey())
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(1_000_000_000_000)
	pool.BalanceRune = cosmos.NewUint(10_000_000_000_000)
	pool.LPUnits = cosmos.NewUint(100)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.BTCChain, NewNetworkFee(common.BTCChain, 1000, 10)), IsNil)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.L1SlipMinBps.String(), 1)

	for i := 0; i < 20; i++ {
		tx := GetRandomTx()
		tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)))
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, MsgSwap{
			Tx:          tx,
			TargetAsset: common.RuneNative,
			SwapType:    types.SwapType_market,
		}), IsNil)
	}

	counting := &orphanCountingKeeper{Keeper: mgr.Keeper()}
	mgr.K = counting
	mgr.advSwapQueue = newSwapQueueAdv(counting)

	req := &types.QueryQuoteSwapRequest{
		FromAsset:         common.BTCAsset.String(),
		ToAsset:           common.RuneNative.String(),
		Amount:            "1000000000",
		StreamingInterval: "1",
		StreamingQuantity: "5",
		Destination:       GetRandomTHORAddress().String(),
	}

	// Block execution keeps the item iterator, once per simulation to delete the
	// queue and once per end-block pass. (A quote runs more than one simulation.)
	inBlock, err := qs.queryQuoteSwap(ctx.WithExecMode(sdk.ExecModeFinalize), req)
	c.Assert(err, IsNil)
	c.Assert(inBlock.StreamingSwapBlocks > 0, Equals, true)
	withPasses := counting.itemIterators

	// a query iterates only to delete
	counting.itemIterators = 0
	quote, err := qs.queryQuoteSwap(ctx, req)
	c.Assert(err, IsNil)
	c.Check(counting.itemIterators > 0, Equals, true)
	c.Check(counting.itemIterators < withPasses, Equals, true,
		Commentf("query %d item iterators, block execution %d", counting.itemIterators, withPasses))
	// and the quote is the same either way
	c.Check(quote.ExpectedAmountOut, Equals, inBlock.ExpectedAmountOut)
	c.Check(quote.StreamingSwapBlocks, Equals, inBlock.StreamingSwapBlocks)
	c.Check(quote.Fees.Total, Equals, inBlock.Fees.Total)
}
