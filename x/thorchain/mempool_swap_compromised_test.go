package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func observeMempoolSwapFinality(c *C, ctx cosmos.Context, mgr Manager, fixture mempoolSwapLifecycleFixture) ObservedTxVoter {
	observed := NewObservedTx(fixture.msg.Tx, fixture.voter.Tx.BlockHeight, fixture.vault.PubKey, fixture.voter.Tx.BlockHeight)
	_, err := NewObservedTxInHandler(mgr).handle(ctx, *NewMsgObservedTxIn(ObservedTxs{observed}, fixture.node.NodeAddress))
	c.Assert(err, IsNil)
	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	c.Assert(voter.FinalisedHeight, Equals, ctx.BlockHeight())
	return voter
}

func checkCancelledMempoolSwap(c *C, ctx cosmos.Context, mgr *Mgrs, fixture mempoolSwapLifecycleFixture) {
	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, fixture.voter.TxID)
	c.Assert(err, IsNil)
	c.Check(voter.Reverted, Equals, true)
	c.Check(voter.UpdatedVault, Equals, false)
	c.Check(mgr.Keeper().HasMempoolSwap(ctx, voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, voter.TxID, 0), Equals, false)
	c.Check(mgr.Keeper().HasStagedOutbound(ctx, voter.TxID), Equals, false)
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetMempoolSwapCount(ctx).IsZero(), Equals, true)
	pool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pool, DeepEquals, fixture.sourcePool)
	vault, err := mgr.Keeper().GetVault(ctx, fixture.vault.PubKey)
	c.Assert(err, IsNil)
	c.Check(vault.Coins, DeepEquals, fixture.vault.Coins)
	acc, err := fixture.msg.Destination.AccAddress()
	c.Assert(err, IsNil)
	c.Check(mgr.Keeper().GetBalance(ctx, acc).AmountOf(common.RuneNative.Native()).IsZero(), Equals, true)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 0)
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestCompromisedVaultCancelsMempoolSwap(c *C) {
	for _, slices := range []int{0, 1, 2} {
		for _, finalized := range []bool{false, true} {
			ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
			for i := 0; i < slices; i++ {
				ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
				c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
			}
			mgr.Keeper().SetMimir(ctx, compromisedVaultMimirKeyPrefix+fixture.vault.PubKey.String(), 1)
			if finalized {
				observeMempoolSwapFinality(c, ctx, mgr, fixture)
			} else {
				voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, fixture.voter.TxID)
				c.Assert(err, IsNil)
				c.Assert(handleObservedTxInQuorum(ctx, mgr, fixture.node.NodeAddress, NodeAccounts{fixture.node}, NewInternalHandler(mgr), voter.Tx, voter, voter.Tx.GetSigners(), true), IsNil)
			}
			// Run past both recovery deadlines and terminal queue settlement. A
			// mid-stream lifecycle can otherwise become FINALIZED on recovery and
			// only pay when the queue executes its next slice.
			for _, height := range []int64{ctx.BlockHeight() + 900, ctx.BlockHeight() + 901, ctx.BlockHeight() + 2701} {
				ctx = ctx.WithBlockHeight(height)
				c.Assert(newSlasher(mgr.Keeper(), mgr.EventMgr()).recoverDueMempoolSwaps(ctx, mgr, 300, 2700), IsNil)
				c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
			}
			checkCancelledMempoolSwap(c, ctx, mgr, fixture)
			// Clearing the flag and replaying finality cannot revive the swap.
			mgr.Keeper().SetMimir(ctx, compromisedVaultMimirKeyPrefix+fixture.vault.PubKey.String(), 0)
			observed := NewObservedTx(fixture.msg.Tx, fixture.voter.Tx.BlockHeight, fixture.vault.PubKey, fixture.voter.Tx.BlockHeight)
			_, err := NewObservedTxInHandler(mgr).handle(ctx, *NewMsgObservedTxIn(ObservedTxs{observed}, fixture.node.NodeAddress))
			c.Assert(err, IsNil)
			checkCancelledMempoolSwap(c, ctx, mgr, fixture)
		}
	}
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestCompromisedVaultRecoveryCannotCompleteOrRefund(c *C) {
	for _, recovery := range []string{"complete", "force-refund", "inactive-vault"} {
		ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
		c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
		// Model a lifecycle left by the old no-op path, which saved finality
		// metadata but neither credited the deposit nor cancelled the swap.
		voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, fixture.voter.TxID)
		c.Assert(err, IsNil)
		voter.FinalisedHeight = ctx.BlockHeight()
		voter.Tx.FinaliseHeight = ctx.BlockHeight()
		voter.SetDone()
		mgr.Keeper().SetObservedTxInVoter(ctx, voter)
		mgr.Keeper().SetMimir(ctx, compromisedVaultMimirKeyPrefix+fixture.vault.PubKey.String(), 1)
		ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 901)
		if recovery == "force-refund" {
			ctx = ctx.WithBlockHeight(voter.FinalisedHeight + 2701)
		}
		if recovery == "inactive-vault" {
			vault := fixture.vault
			vault.Status = InactiveVault
			c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
			startMempoolSwapVaultRecovery(ctx, mgr.Keeper(), vault)
			c.Assert(newSlasher(mgr.Keeper(), mgr.EventMgr()).recoverMempoolSwapVaultJobs(ctx, mgr), IsNil)
		} else {
			c.Assert(newSlasher(mgr.Keeper(), mgr.EventMgr()).recoverDueMempoolSwaps(ctx, mgr, 300, 2700), IsNil)
		}
		c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx.WithBlockHeight(ctx.BlockHeight()+1), mgr, false), IsNil)
		checkCancelledMempoolSwap(c, ctx, mgr, fixture)
	}
}

func (s *MempoolSwapLifecycleIntegrationSuite) TestCompromisedVaultRollbackFailureStaysStopped(c *C) {
	ctx, mgr, fixture := setupMempoolSwapLifecycleFixture(c)
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
	poolBefore, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	queueBefore, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, fixture.voter.TxID, 0)
	c.Assert(err, IsNil)
	// Force a real rollback accounting error, without changing the inbound.
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, fixture.reservedTOR.Sub(cosmos.OneUint()))
	mgr.Keeper().SetMimir(ctx, compromisedVaultMimirKeyPrefix+fixture.vault.PubKey.String(), 1)
	voter := observeMempoolSwapFinality(c, ctx, mgr, fixture)
	c.Check(voter.Reverted, Equals, true)
	c.Check(voter.UpdatedVault, Equals, false)
	record, found, err := mgr.Keeper().GetMempoolSwap(ctx, voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(record.Status, Equals, types.MempoolSwapStatus_ROLLBACK_PENDING)
	c.Check(record.NextRecoveryHeight, Equals, ctx.BlockHeight()+1)
	c.Check(newSwapQueueAdv(mgr.Keeper()).isSwapReady(ctx, queueBefore), Equals, false)
	c.Check(completeMempoolSwapAtomic(ctx, mgr, voter), ErrorMatches, "cannot complete reverted mempool swap .*")
	c.Check(rollbackAndRefundFinalizedMempoolSwapAtomic(ctx, mgr, voter), ErrorMatches, "cannot refund reverted mempool swap .*")
	c.Check(rollbackAndRefundInactiveVaultMempoolSwap(ctx, mgr, voter.Tx, voter), ErrorMatches, "cannot refund reverted mempool swap .*")
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx.WithBlockHeight(ctx.BlockHeight()+1), mgr, false), IsNil)
	poolAfter, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(poolAfter, DeepEquals, poolBefore)
	queueAfter, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, voter.TxID, 0)
	c.Assert(err, IsNil)
	c.Check(queueAfter, DeepEquals, queueBefore)

	// Repair the accounting and clear the flag. A fresh worker must recover
	// from the persisted cancellation, with no dependence on the old Mimir.
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, fixture.reservedTOR)
	mgr.Keeper().SetMimir(ctx, compromisedVaultMimirKeyPrefix+fixture.vault.PubKey.String(), 0)
	ctx = ctx.WithBlockHeight(record.NextRecoveryHeight)
	c.Assert(newSlasher(mgr.Keeper(), mgr.EventMgr()).recoverDueMempoolSwaps(ctx, mgr, 300, 2700), IsNil)
	checkCancelledMempoolSwap(c, ctx, mgr, fixture)
	c.Assert(newSlasher(mgr.Keeper(), mgr.EventMgr()).recoverDueMempoolSwaps(ctx, mgr, 300, 2700), IsNil)
	checkCancelledMempoolSwap(c, ctx, mgr, fixture)
}
