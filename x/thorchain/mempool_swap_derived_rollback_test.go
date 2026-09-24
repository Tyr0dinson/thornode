package thorchain

import (
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
)

func (s *MempoolSwapLifecycleIntegrationSuite) TestDerivedMempoolRollbackAfterTradingDisabled(c *C) {
	for _, slices := range []int{1, 2} {
		for _, disable := range []bool{false, true} {
			ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
			mgr.Keeper().SetMimir(ctx, constants.EnableDerivedAssets.String(), 1)
			mgr.Keeper().SetMimir(ctx, "TorAnchor-BTC-BTC", 1)
			mgr.Keeper().SetMimir(ctx, constants.DerivedDepthBasisPts.String(), 10_000)
			mgr.NetworkMgr().SpawnDerivedAsset(ctx, common.TOR, mgr)
			msg := fixture.msg
			msg.TargetAsset = common.TOR
			msg.Tx.Memo = strings.ReplaceAll(msg.Tx.Memo, "THOR.RUNE", "THOR.TOR")
			voter := fixture.voter
			voter.Tx.Tx = msg.Tx
			voter.Txs[0].Tx = msg.Tx
			mgr.Keeper().SetObservedTxInVoter(ctx, voter)
			record, _, err := mgr.Keeper().GetMempoolSwap(ctx, voter.TxID, 0)
			c.Assert(err, IsNil)
			record.LatestSwap = msg
			c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
			c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, msg), IsNil)

			// Speculative mode is not a bypass for disabled derived trading.
			mgr.Keeper().SetMimir(ctx, constants.EnableDerivedAssets.String(), 0)
			speculativeCtx := withMempoolSwapExecutionMode(ctx, mempoolSwapExecutionSpeculative)
			speculativeCtx = withMempoolSwapExecutionIdentity(speculativeCtx, voter.TxID, 0)
			c.Check(NewSwapHandler(mgr).validate(speculativeCtx, msg), ErrorMatches, "swapping to/from a derived asset is not allowed.*")
			mgr.Keeper().SetMimir(ctx, constants.EnableDerivedAssets.String(), 1)
			runeSupply := mgr.Keeper().GetBankSupply(ctx, common.RuneAsset())
			torSupply := mgr.Keeper().GetBankSupply(ctx, common.TOR)
			for i := 0; i < slices; i++ {
				ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
				c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
			}
			c.Assert(mgr.Keeper().GetBankSupply(ctx, common.TOR).GT(torSupply), Equals, true)
			c.Assert(mgr.Keeper().HasStagedOutbound(ctx, voter.TxID), Equals, slices == 2)
			if disable {
				mgr.Keeper().SetMimir(ctx, constants.EnableDerivedAssets.String(), 0)
			}
			voter, err = mgr.Keeper().GetObservedTxInVoter(ctx, voter.TxID)
			c.Assert(err, IsNil)
			c.Assert(rollbackMempoolSwapAtomic(ctx, mgr.Keeper(), mgr, voter), IsNil, Commentf("slices=%d disabled=%v", slices, disable))
			c.Check(mgr.Keeper().HasMempoolSwap(ctx, voter.TxID, 0), Equals, false)
			c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, voter.TxID, 0), Equals, false)
			c.Check(mgr.Keeper().HasStagedOutbound(ctx, voter.TxID), Equals, false)
			c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
			c.Check(mgr.Keeper().GetBankSupply(ctx, common.TOR).Equal(torSupply), Equals, true)
			c.Check(mgr.Keeper().GetBankSupply(ctx, common.RuneAsset()).Equal(runeSupply), Equals, true)
			if disable {
				c.Check(NewSwapHandler(mgr).validate(ctx, msg), ErrorMatches, "swapping to/from a derived asset is not allowed.*")
			}
		}
	}
}
