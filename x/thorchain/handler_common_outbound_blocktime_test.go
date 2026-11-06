package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// A retry LackSigning rounded up to the coalesce interval just before a
// speed-up is moved past the next multiple of the new (scaled) interval. A late
// observation of the outbound from the same vault in the next block must still
// find it there: matched, not slashed as a missing tx out.
func (s *HandlerCommonOutboundSuite) TestLateObservationFindsRetryMovedByBlockTimeChange(c *C) {
	ctx, k := setupKeeperForTest(c)
	refMs := constants.ThorchainBlockTime.Milliseconds()
	ctx = ctx.WithBlockHeight(10101)
	mgr := NewDummyMgrWithKeeper(k)
	gas := &zecRescheduleGasManager{DummyGasManager: NewDummyGasManager(), amount: 1_000, rate: 1}
	mgr.gasMgr = gas
	mgr.txOutStore = newTxOutStorage(k, mgr.GetConstants(), mgr.EventMgr(), gas)
	recorder := &xmrMemoCompatibilitySlasher{DummySlasher: NewDummySlasher()}
	mgr.slasher = recorder
	k.SetMimir(ctx, constants.SigningTransactionPeriod.String(), 10)
	k.SetMimir(ctx, constants.RescheduleCoalesceBlocks.String(), 10)
	k.SetMimir(ctx, constants.EnableMemolessOutbound.String(), 0)
	k.SetMimir(ctx, constants.MaxOutboundAttempts.String(), 0)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	// the only vault, so the retry keeps it
	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	inHash := GetRandomTxHash()
	item := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inHash,
		ToAddress:   GetRandomBTCAddress(),
		VaultPubKey: vault.PubKey,
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		MaxGas:      common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(gas.amount))},
		GasRate:     int64(gas.rate),
		Memo:        NewOutboundMemo(inHash).String(),
	}
	queued := NewTxOut(ctx.BlockHeight())
	queued.TxArray = []TxOutItem{item}
	c.Assert(k.SetTxOut(ctx, queued), IsNil)
	voter := NewObservedTxVoter(inHash, nil)
	voter.Height, voter.FinalisedHeight = ctx.BlockHeight(), ctx.BlockHeight()
	voter.Actions = []TxOutItem{item}
	k.SetObservedTxInVoter(ctx, voter)

	// unsigned a period later: retried at the next multiple of 10, same vault
	ctx = ctx.WithBlockHeight(10111)
	c.Assert(newSlasher(k, mgr.EventMgr()).LackSigning(ctx, mgr), IsNil)
	retry, err := k.GetTxOut(ctx, 10120)
	c.Assert(err, IsNil)
	c.Assert(retry.TxArray, HasLen, 1)
	c.Assert(retry.TxArray[0].VaultPubKey.Equals(vault.PubKey), Equals, true)

	// blocks go twice as fast from 10112: the retry's 9 blocks left take 18,
	// past 10120, the next multiple of the new 20-block interval
	k.SetMimir(ctx, constants.BlockTimeMs.String(), refMs/2)
	ctx = ctx.WithBlockHeight(10112)
	k.UpdateBlockTime(ctx)
	c.Assert(k.GetConfigInt64(ctx, constants.RescheduleCoalesceBlocks), Equals, int64(20))
	moved, err := k.GetTxOut(ctx, 10129)
	c.Assert(err, IsNil)
	c.Assert(moved.TxArray, HasLen, 1)
	c.Check(rescheduleReach(ctx, k), Equals, int64(10129))

	// the vault's original signature lands late, in the next block
	ctx = ctx.WithBlockHeight(10113)
	from, err := vault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	outHash := GetRandomTxHash()
	observed := NewObservedTx(common.NewTx(outHash, from, item.ToAddress, common.NewCoins(item.Coin), item.MaxGas, item.Memo),
		ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight())
	c.Check(findOriginalMemoForOutbound(ctx, mgr, observed), Equals, item.Memo)
	_, err = NewCommonOutboundTxHandler(mgr).handle(ctx, observed, inHash)
	c.Assert(err, IsNil)
	c.Check(recorder.calls, Equals, 0)
	c.Check(hasMissingTxOutSecurityEvent(ctx), Equals, false)
	moved, err = k.GetTxOut(ctx, 10129)
	c.Assert(err, IsNil)
	c.Check(moved.TxArray[0].OutHash, Equals, outHash)
}
