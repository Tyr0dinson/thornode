package thorchain

import (
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (s *MempoolSwapLifecycleIntegrationSuite) TestSynthMempoolRollbackAfterBurnSynthsDisabled(c *C) {
	synth := common.ETHAsset.GetSyntheticAsset()
	for _, slices := range []int{1, 2} {
		comment := Commentf("slices=%d", slices)
		ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
		ethPool := NewPool()
		ethPool.Asset = common.ETHAsset
		ethPool.BalanceRune = cosmos.NewUint(1_000 * common.One)
		ethPool.BalanceAsset = cosmos.NewUint(1_000 * common.One)
		ethPool.LPUnits = ethPool.BalanceRune
		ethPool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

		msg := fixture.msg
		msg.TargetAsset = synth
		msg.Tx.Memo = strings.ReplaceAll(msg.Tx.Memo, "THOR.RUNE", synth.String())
		voter := fixture.voter
		voter.Tx.Tx = msg.Tx
		voter.Txs[0].Tx = msg.Tx
		mgr.Keeper().SetObservedTxInVoter(ctx, voter)
		record, _, err := mgr.Keeper().GetMempoolSwap(ctx, voter.TxID, 0)
		c.Assert(err, IsNil)
		record.LatestSwap = msg
		c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, msg), IsNil)

		synthSupply := mgr.Keeper().GetTotalSupply(ctx, synth)
		for i := 0; i < slices; i++ {
			ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
			c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
		}
		c.Assert(mgr.Keeper().GetTotalSupply(ctx, synth).GT(synthSupply), Equals, true, comment)
		// One slice leaves output in the queue; two complete and stage it.
		c.Assert(mgr.Keeper().HasStagedOutbound(ctx, voter.TxID), Equals, slices == 2, comment)

		mgr.Keeper().SetMimir(ctx, constants.BurnSynths.String(), 1)
		voter, err = mgr.Keeper().GetObservedTxInVoter(ctx, voter.TxID)
		c.Assert(err, IsNil)
		c.Assert(rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter), IsNil, comment)
		c.Check(mgr.Keeper().HasMempoolSwap(ctx, voter.TxID, 0), Equals, false, comment)
		c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, voter.TxID, 0), Equals, false, comment)
		c.Check(mgr.Keeper().HasStagedOutbound(ctx, voter.TxID), Equals, false, comment)
		c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true, comment)
		c.Check(mgr.Keeper().GetTotalSupply(ctx, synth).Equal(synthSupply), Equals, true, comment)
		for _, asset := range []common.Asset{common.BTCAsset, common.ETHAsset} {
			_, hasOwner, ownerErr := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, asset)
			c.Assert(ownerErr, IsNil)
			c.Check(hasOwner, Equals, false, Commentf("slices=%d pool=%s", slices, asset))
		}

		// Ordinary synth burns stay disabled. Speculative swaps never burn:
		// native (synth) sources are not mempool-swap eligible.
		burn := fixture.msg
		burn.Tx.ID = GetRandomTxHash()
		burn.Tx.Chain = common.THORChain
		burn.Tx.FromAddress = GetRandomTHORAddress()
		burn.Tx.Coins = common.NewCoins(common.NewCoin(synth, cosmos.NewUint(common.One)))
		burn.Tx.Gas = nil
		burn.TargetAsset = common.ETHAsset
		burn.Destination = GetRandomETHAddress()
		burn.State = nil
		burn.SwapType = types.SwapType_market
		c.Check(executeSwapWithoutOutboundsCached(ctx, mgr, &burn), ErrorMatches, ".*burning synthetics has been disabled.*", comment)
	}
}
