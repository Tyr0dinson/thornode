package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (s *MempoolSwapLifecycleIntegrationSuite) TestFinalizedMempoolStreamSurvivesVaultRetirement(c *C) {
	for _, worker := range []string{"vault-job", "due-worker"} {
		ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
		c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
		voter := observeMempoolSwapFinality(c, ctx, mgr, fixture)
		c.Assert(voter.UpdatedVault, Equals, true)
		record, found, err := mgr.Keeper().GetMempoolSwap(ctx, voter.TxID, 0)
		c.Assert(err, IsNil)
		c.Assert(found, Equals, true)
		c.Assert(record.Status, Equals, types.MempoolSwapStatus_FINALIZED)
		queued, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, voter.TxID, 0)
		c.Assert(err, IsNil)
		ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
		c.Assert(newSwapQueueAdv(mgr.Keeper()).isSwapReady(ctx, queued), Equals, true)

		// Churn migrates the already-credited funds to another active vault.
		retired, err := mgr.Keeper().GetVault(ctx, fixture.vault.PubKey)
		c.Assert(err, IsNil)
		replacement := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), retired.Chains, nil)
		replacement.Coins = retired.Coins
		c.Assert(mgr.Keeper().SetVault(ctx, replacement), IsNil)
		retired.Status = InactiveVault
		retired.Coins = nil
		c.Assert(mgr.Keeper().SetVault(ctx, retired), IsNil)
		c.Check(newSwapQueueAdv(mgr.Keeper()).isSwapReady(ctx, queued), Equals, true, Commentf("%s", worker))
		if worker == "vault-job" {
			startMempoolSwapVaultRecovery(ctx, mgr.Keeper(), retired)
			c.Assert(newSlasher(mgr.Keeper(), mgr.EventMgr()).recoverMempoolSwapVaultJobs(ctx, mgr), IsNil)
		} else {
			record.NextRecoveryHeight = ctx.BlockHeight()
			c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
			c.Assert(newSlasher(mgr.Keeper(), mgr.EventMgr()).recoverDueMempoolSwaps(ctx, mgr, 300, 2700), IsNil)
		}
		retained, found, err := mgr.Keeper().GetMempoolSwap(ctx, voter.TxID, 0)
		c.Assert(err, IsNil)
		c.Assert(found, Equals, true)
		c.Check(retained.Status, Equals, types.MempoolSwapStatus_FINALIZED)
		queueAfter, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, voter.TxID, 0)
		c.Assert(err, IsNil)
		c.Check(queueAfter, DeepEquals, queued)
		c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
		c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, voter.TxID, 0), Equals, false)
		c.Check(mgr.Keeper().HasMempoolSwap(ctx, voter.TxID, 0), Equals, false)
		acc, err := fixture.msg.Destination.AccAddress()
		c.Assert(err, IsNil)
		c.Check(mgr.Keeper().GetBalance(ctx, acc).AmountOf(common.RuneNative.Native()).IsPositive(), Equals, true)
		items, err := mgr.TxOutStore().GetOutboundItems(ctx)
		c.Assert(err, IsNil)
		c.Check(items, HasLen, 0, Commentf("retirement must not schedule an external refund"))
	}
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestInactiveVaultCursorSkipsFinalizedTombstones(c *C) {
	ctx, mgr := setupAdvancedMempoolManagerForTest(c)
	vault := NewVault(ctx.BlockHeight(), InactiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), nil)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	var records []types.MempoolSwap
	for i := 1; i <= 3; i++ {
		txID := common.TxID(fmt.Sprintf("%064X", i))
		voter := NewObservedTxVoter(txID, ObservedTxs{})
		voter.Tx = ObservedTx{Tx: common.Tx{ID: txID, Chain: common.BTCChain, Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.OneUint())}}}
		voter.UnfinalizedHeight = ctx.BlockHeight()
		voter.MempoolSwapTOR = cosmos.OneUint()
		status := types.MempoolSwapStatus_RESERVED
		if i != 2 {
			status = types.MempoolSwapStatus_FINALIZED
			voter.FinalisedHeight = ctx.BlockHeight()
		}
		if i == 3 {
			voter.SetReverted()
		}
		voter = seedMempoolSwapRecoveryRecord(c, ctx, mgr, voter, vault.PubKey, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), ctx.BlockHeight()+100, types.MempoolSwapStatus_RESERVED)
		record, _, err := mgr.Keeper().GetMempoolSwap(ctx, txID, 0)
		c.Assert(err, IsNil)
		record.Status = status
		if status == types.MempoolSwapStatus_FINALIZED {
			voter.MempoolSwapTOR = cosmos.ZeroUint()
			mgr.Keeper().SetObservedTxInVoter(ctx, voter)
			record.ReservedTor = cosmos.ZeroUint()
		}
		if i == 1 {
			record.PendingEffects.Affiliates = []types.MempoolSwapAffiliateEffect{{Thorname: "retired-vault-orphan", FeeRune: cosmos.NewUint(17)}}
		}
		c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
		records = append(records, record)
	}
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, cosmos.OneUint())
	startMempoolSwapVaultRecovery(ctx, mgr.Keeper(), vault)
	c.Assert(processMempoolSwapVaultRecoveryBatch(ctx, mgr.Keeper(), mgr, vault.PubKey, 1), IsNil)
	retained, _, err := mgr.Keeper().GetMempoolSwap(ctx, records[0].TxId, 0)
	c.Assert(err, IsNil)
	c.Check(retained, DeepEquals, records[0])
	cursor, err := mgr.Keeper().GetMempoolSwapVaultRecovery(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Check(cursor, Equals, records[0].Cursor())
	c.Assert(processMempoolSwapVaultRecoveryBatch(ctx, mgr.Keeper(), mgr, vault.PubKey, 2), IsNil)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, records[1].TxId, 0), Equals, false)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, records[2].TxId, 0), Equals, false, Commentf("reverted FINALIZED records must still be cancelled"))
	jobs, err := mgr.Keeper().GetMempoolSwapVaultRecoveries(ctx, 10)
	c.Assert(err, IsNil)
	c.Check(jobs, HasLen, 0)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)

	// The skipped orphan has no queue work. The due worker must still flush
	// its journal exactly once, even though the original vault is inactive.
	ctx = ctx.WithBlockHeight(records[0].NextRecoveryHeight)
	for i := 0; i < 2; i++ {
		c.Assert(newSlasher(mgr.Keeper(), mgr.EventMgr()).recoverDueMempoolSwaps(ctx, mgr, 300, 2700), IsNil)
	}
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, records[0].TxId, 0), Equals, false)
	fees := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(fees, HasLen, 1)
	c.Check(fees[0].Fee.Equal(cosmos.NewUint(17)), Equals, true)
}
