package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// ragnarokBTCGasManager quotes max gas in BTC, the outbound's own gas asset.
type ragnarokBTCGasManager struct {
	*DummyGasManager
}

func (m *ragnarokBTCGasManager) GetMaxGas(cosmos.Context, common.Chain) (common.Coin, error) {
	return common.NewCoin(common.BTCAsset, cosmos.NewUint(1_000)), nil
}

func (m *ragnarokBTCGasManager) GetGasRate(cosmos.Context, common.Chain) cosmos.Uint {
	return cosmos.NewUint(1)
}

// A gas-asset ragnarok outbound goes unsigned for a signing period, and real
// LackSigning retries it at the due block rounded up to RescheduleCoalesceBlocks
// (10 reference blocks). The ragnarok handler must find that retry, not the due
// block, whether the observation lands after the retry's height or before it
// (the original's late signature), so the vault is not slashed:
//   - the block time halves at 10050: due at 10551, retried at 10560, the next
//     multiple of the scaled 20-block interval
//   - no block time change, scheduled off the interval at 10005: due at 10305,
//     retried at 10310 (on develop the walk stepped to 10305 only)
func (HandlerRagnarokSuite) TestRagnarokFindsCoalescedRetry(c *C) {
	refMs := constants.ThorchainBlockTime.Milliseconds()
	for _, tc := range []struct {
		name               string
		scheduled          int64
		change             int64 // 0: no block time change
		due, retry         int64
		observeBeforeRetry bool // observe between the due block and the retry height
	}{
		{name: "after a speed-up", scheduled: 10000, change: 10050, due: 10551, retry: 10560},
		{name: "late, after a speed-up", scheduled: 10000, change: 10050, due: 10551, retry: 10560, observeBeforeRetry: true},
		{name: "no change, off the interval", scheduled: 10005, due: 10305, retry: 10310},
		{name: "late, no change, off the interval", scheduled: 10005, due: 10305, retry: 10310, observeBeforeRetry: true},
	} {
		comment := Commentf(tc.name)
		ctx, k := setupKeeperForTest(c)
		mgr := NewDummyMgrWithKeeper(k)
		gas := &ragnarokBTCGasManager{DummyGasManager: NewDummyGasManager()}
		mgr.gasMgr = gas
		mgr.txOutStore = newTxOutStorage(k, mgr.GetConstants(), mgr.EventMgr(), gas)
		recorder := &xmrMemoCompatibilitySlasher{DummySlasher: NewDummySlasher()}
		mgr.slasher = recorder

		ctx = ctx.WithBlockHeight(tc.scheduled)
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
		from, err := vault.PubKey.GetAddress(common.BTCChain)
		c.Assert(err, IsNil)

		item := TxOutItem{
			Chain:       common.BTCChain,
			InHash:      common.BlankTxID,
			ToAddress:   GetRandomBTCAddress(),
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
			MaxGas:      common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1_000))},
			GasRate:     1,
			Memo:        NewRagnarokMemo(tc.scheduled).String(),
		}
		queued := NewTxOut(tc.scheduled)
		queued.TxArray = []TxOutItem{item}
		c.Assert(k.SetTxOut(ctx, queued), IsNil)
		k.SetRagnarokPending(ctx, 1)

		if tc.change > 0 {
			k.SetMimir(ctx, constants.BlockTimeMs.String(), refMs/2)
			ctx = ctx.WithBlockHeight(tc.change)
			k.UpdateBlockTime(ctx)
			c.Assert(k.GetBlockTimeHistory(ctx), HasLen, 1, comment)
		}
		period := mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
		c.Assert(k.GetBlockTimeHistory(ctx).HeightAfter(tc.scheduled, period*refMs, refMs), Equals, tc.due, comment)

		// unsigned for a whole period: LackSigning retries it on the same vault
		ctx = ctx.WithBlockHeight(tc.due)
		c.Assert(newSlasher(k, mgr.EventMgr()).LackSigning(ctx, mgr), IsNil)
		original, err := k.GetTxOut(ctx, tc.scheduled)
		c.Assert(err, IsNil)
		c.Assert(original.TxArray, HasLen, 1, comment)
		c.Assert(original.TxArray[0].OutHash, Equals, common.BlankTxID, comment)
		retry, err := k.GetTxOut(ctx, tc.retry)
		c.Assert(err, IsNil)
		c.Assert(retry.TxArray, HasLen, 1, comment)
		c.Assert(retry.TxArray[0].VaultPubKey.Equals(vault.PubKey), Equals, true, comment)

		observeAt := tc.retry + 1
		if tc.observeBeforeRetry {
			observeAt = tc.due + 1
		}
		ctx = ctx.WithBlockHeight(observeAt)
		tx := NewObservedTx(
			common.NewTx(GetRandomTxHash(), from, item.ToAddress, common.NewCoins(item.Coin), item.MaxGas, item.Memo),
			ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight(),
		)
		_, err = NewRagnarokHandler(mgr).handle(ctx, *NewMsgRagnarok(tx, tc.scheduled, GetRandomBech32Addr()))
		c.Assert(err, IsNil)

		retry, err = k.GetTxOut(ctx, tc.retry)
		c.Assert(err, IsNil)
		c.Check(retry.TxArray[0].OutHash.Equals(tx.Tx.ID), Equals, true, comment)
		c.Check(recorder.calls, Equals, 0, comment)
		pending, err := k.GetRagnarokPending(ctx)
		c.Assert(err, IsNil)
		c.Check(pending, Equals, int64(0), comment)
	}
}
