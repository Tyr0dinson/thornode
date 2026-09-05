package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// These regressions deliberately use the real ADD, errata, and queue handlers
// against the production keeper. An opaque observed sender is never the LP key.
func xmrErrataFixture(c *C) (cosmos.Context, *Mgrs, NodeAccount, Pool, common.Tx, common.Address) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	pool := NewPool()
	pool.Asset = common.XMRAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1_000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1_000 * common.One)
	pool.LPUnits = cosmos.NewUint(1_000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	addr, err := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	txID := GetRandomTxHash()
	unknown, err := common.NewAddress(common.XMRUnknownSenderAddress(txID.String(), addr.String()))
	c.Assert(err, IsNil)
	tx := common.Tx{
		ID: txID, Chain: common.XMRChain, FromAddress: unknown, ToAddress: addr,
		Coins: common.Coins{common.NewCoin(common.XMRAsset, cosmos.NewUint(100*common.One))},
		Gas:   common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(10_000))},
		Memo:  fmt.Sprintf("+:XMR.XMR:%s", addr),
	}
	return ctx, mgr, na, pool, tx, addr
}

func recordXMRFinalInbound(ctx cosmos.Context, mgr Manager, tx common.Tx) ObservedTxVoter {
	observed := common.NewObservedTx(tx, 20, common.EmptyPubKey, 20)
	voter := NewObservedTxVoter(tx.ID, []common.ObservedTx{observed})
	voter.Tx = observed
	voter.FinalisedHeight = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	return voter
}

func (s *HandlerErrataTxSuite) TestXMRAddErrataUsesBookedOwnerAndPreservesPriorUnits(c *C) {
	for _, memoCase := range []string{"explicit", "reference", "thorname"} {
		ctx, mgr, na, base, tx, addr := xmrErrataFixture(c)
		owner := GetRandomTHORAddress()
		tx.Memo = fmt.Sprintf("+:XMR.XMR:%s/%s", owner, addr)
		if memoCase == "thorname" {
			mgr.Keeper().SetTHORName(ctx, NewTHORName("receiptowner", 1000, []types.THORNameAlias{{Chain: common.THORChain, Address: owner}}))
			tx.Memo = fmt.Sprintf("+:XMR.XMR:receiptowner.THOR/%s", addr)
		}
		lp, err := mgr.Keeper().GetLiquidityProvider(ctx, common.XMRAsset, owner)
		c.Assert(err, IsNil)
		lp.Units = cosmos.NewUint(25 * common.One)
		lp.AssetAddress = addr
		mgr.Keeper().SetLiquidityProvider(ctx, lp)
		beforeUnits := lp.Units
		runeTx := GetRandomTx()
		c.Assert(NewAddLiquidityHandler(mgr).handle(ctx, *NewMsgAddLiquidity(runeTx, common.XMRAsset, cosmos.NewUint(100*common.One), cosmos.ZeroUint(), owner, addr, common.NoAddress, cosmos.ZeroUint(), na.NodeAddress)), IsNil)
		voter := recordXMRFinalInbound(ctx, mgr, tx)
		if memoCase == "reference" {
			mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 200)
			mgr.Keeper().SetReferenceMemo(ctx, NewReferenceMemo(common.XMRAsset, tx.Memo, "40000", 10))
			voter.Tx.Tx.Memo = "r:40000"
			voter.UnfinalizedHeight = 100
			observed := voter.Tx
			_, ok := resolveReferenceMemo(ctx, mgr, mgr.Keeper(), &observed, &voter)
			c.Assert(ok, Equals, true)
			tx = observed.Tx
		}
		message, err := processOneTxIn(ctx, mgr.Keeper(), voter.Tx, na.NodeAddress)
		c.Assert(err, IsNil)
		add, ok := message.(*MsgAddLiquidity)
		c.Assert(ok, Equals, true)
		c.Assert(NewAddLiquidityHandler(mgr).handle(ctx, *add), IsNil)
		lp, err = mgr.Keeper().GetLiquidityProvider(ctx, common.XMRAsset, owner)
		c.Assert(err, IsNil)
		c.Assert(lp.Units.GT(beforeUnits), Equals, true)
		if memoCase == "thorname" {
			mgr.Keeper().SetTHORName(ctx, NewTHORName("receiptowner", 1000, []types.THORNameAlias{{Chain: common.THORChain, Address: GetRandomTHORAddress()}}))
		}

		errata := NewErrataTxHandler(mgr)
		_, err = errata.Run(ctx, NewMsgErrataTx(tx.ID, common.XMRChain, na.NodeAddress))
		c.Assert(err, IsNil)
		lp, err = mgr.Keeper().GetLiquidityProvider(ctx, common.XMRAsset, owner)
		c.Assert(err, IsNil)
		c.Check(lp.Units.Equal(beforeUnits), Equals, true)
		after, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
		c.Assert(err, IsNil)
		c.Check(after.LPUnits.Equal(base.LPUnits), Equals, true)
		c.Check(after.BalanceAsset.Equal(base.BalanceAsset), Equals, true)
		_, err = errata.Run(ctx.WithBlockHeight(101), NewMsgErrataTx(tx.ID, common.XMRChain, na.NodeAddress))
		c.Assert(err, IsNil)
		repeated, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
		c.Assert(err, IsNil)
		c.Check(repeated, DeepEquals, after)
	}
}

func (s *HandlerErrataTxSuite) TestXMRAddErrataPendingAndPairedLegs(c *C) {
	for _, order := range []string{"asset-pending", "asset-first", "rune-first"} {
		ctx, mgr, na, base, tx, assetAddr := xmrErrataFixture(c)
		runeAddr := GetRandomTHORAddress()
		tx.Memo = fmt.Sprintf("+:XMR.XMR:%s/%s", runeAddr, assetAddr)
		recordXMRFinalInbound(ctx, mgr, tx)
		runeTx := GetRandomTx()
		runeTx.ID = GetRandomTxHash()
		runeTx.Chain = common.THORChain
		runeTx.FromAddress = runeAddr
		runeTx.Coins = common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))}
		add := NewAddLiquidityHandler(mgr)
		assetMsg := NewMsgAddLiquidity(tx, common.XMRAsset, cosmos.ZeroUint(), tx.Coins[0].Amount, runeAddr, assetAddr, common.NoAddress, cosmos.ZeroUint(), na.NodeAddress)
		runeMsg := NewMsgAddLiquidity(runeTx, common.XMRAsset, runeTx.Coins[0].Amount, cosmos.ZeroUint(), runeAddr, assetAddr, common.NoAddress, cosmos.ZeroUint(), na.NodeAddress)
		if order == "rune-first" {
			c.Assert(add.handle(ctx, *runeMsg), IsNil)
		}
		c.Assert(add.handle(ctx, *assetMsg), IsNil)
		if order == "asset-first" {
			c.Assert(add.handle(ctx, *runeMsg), IsNil)
		}
		_, err := NewErrataTxHandler(mgr).Run(ctx, NewMsgErrataTx(tx.ID, common.XMRChain, na.NodeAddress))
		c.Assert(err, IsNil, Commentf("order %s", order))
		lp, err := mgr.Keeper().GetLiquidityProvider(ctx, common.XMRAsset, runeAddr)
		c.Assert(err, IsNil)
		pool, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
		c.Assert(err, IsNil)
		c.Check(lp.Units.IsZero(), Equals, true)
		c.Check(lp.PendingAsset.IsZero(), Equals, true)
		c.Check(pool.PendingInboundAsset.IsZero(), Equals, true)
		c.Check(pool.LPUnits.Equal(base.LPUnits), Equals, true)
		c.Check(pool.BalanceAsset.Equal(base.BalanceAsset), Equals, true)
		c.Check(pool.BalanceRune.Equal(base.BalanceRune), Equals, true)
		expectedRune := cosmos.ZeroUint()
		if order != "asset-pending" {
			expectedRune = runeTx.Coins[0].Amount
		}
		c.Check(lp.PendingRune.Equal(expectedRune), Equals, true)
		c.Check(pool.PendingInboundRune.Equal(expectedRune), Equals, true)
		// Only the real paired RUNE deposit can be withdrawn after reversal.
		withdrawMsg := NewMsgWithdrawLiquidity(GetRandomTx(), runeAddr, cosmos.NewUint(10_000), common.XMRAsset, common.EmptyAsset, na.NodeAddress)
		runeOut, assetOut, units, _, withdrawErr := withdraw(ctx, *withdrawMsg, mgr)
		if expectedRune.IsZero() {
			c.Check(withdrawErr, NotNil)
		} else {
			c.Check(withdrawErr, IsNil)
		}
		c.Check(runeOut.Equal(expectedRune), Equals, true)
		c.Check(assetOut.IsZero(), Equals, true)
		c.Check(units.IsZero(), Equals, true)
	}
}

func (s *HandlerErrataTxSuite) TestXMRErrataCancelsQueuedRemainderWithoutSettlement(c *C) {
	for _, advanced := range []bool{false, true} {
		for _, partial := range []bool{false, true} {
			ctx, mgr, na, base, tx, _ := xmrErrataFixture(c)
			mgr.txOutStore = NewTxStoreDummy()
			target := GetRandomETHAddress()
			tx.Memo = fmt.Sprintf("=:ETH.ETH:%s", target)
			recordXMRFinalInbound(ctx, mgr, tx)
			version := types.SwapVersion_v1
			if advanced {
				version = types.SwapVersion_v2
			}
			msg := NewMsgSwap(tx, common.ETHAsset, target, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 2, 1, version, na.NodeAddress)
			if partial {
				msg.State.Count = 1
				msg.State.In = cosmos.NewUint(50 * common.One)
				msg.State.Out = cosmos.NewUint(10 * common.One)
			}
			queue := func() {
				if advanced {
					// Sparse indices must not escape cancellation.
					for _, index := range []uint32{3} {
						msg.Index = index
						c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *msg), IsNil)
					}
				} else {
					for _, index := range []int{3} {
						c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, *msg, index), IsNil)
					}
					stream := msg.GetStreamingSwap()
					stream.In, stream.Out, stream.Count = msg.State.In, msg.State.Out, msg.State.Count
					mgr.Keeper().SetStreamingSwap(ctx, stream)
				}
			}
			queue()
			_, err := NewErrataTxHandler(mgr).Run(ctx, NewMsgErrataTx(tx.ID, common.XMRChain, na.NodeAddress))
			c.Assert(err, IsNil)
			errataPool, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
			c.Assert(err, IsNil)
			expectedAsset := base.BalanceAsset
			if partial {
				expectedAsset = expectedAsset.Sub(msg.State.In)
			}
			c.Check(errataPool.BalanceAsset.Equal(expectedAsset), Equals, true)
			for _, index := range []int{0, 3} {
				c.Check(mgr.Keeper().HasSwapQueueItem(ctx, tx.ID, index), Equals, false)
				c.Check(mgr.Keeper().HasAdvSwapQueueItem(ctx, tx.ID, index), Equals, false)
			}
			c.Check(mgr.Keeper().StreamingSwapExists(ctx, tx.ID), Equals, false)
			// Reinsert stale admitted work to exercise the independent execution
			// barrier, including ordinary failure/refund paths and held output.
			queue()
			before, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
			c.Assert(err, IsNil)
			if advanced {
				c.Assert(settleSwap(ctx, mgr, *msg, "expired stale orphan"), IsNil)
				outbounds, err := mgr.TxOutStore().GetOutboundItems(ctx)
				c.Assert(err, IsNil)
				c.Check(outbounds, HasLen, 0)
				queue()
			}
			if advanced {
				mgr.Keeper().SetMimir(ctx, constants.AdvSwapQueueRapidSwapMax.String(), 1)
				c.Assert(newSwapQueueAdv(mgr.Keeper()).EndBlock(ctx, mgr, false), IsNil)
			} else {
				legacy := newSwapQueue(mgr.Keeper())
				calls := 0
				legacy.handler = func(Manager) cosmos.Handler {
					return func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
						calls++
						return nil, fmt.Errorf("must not invoke swap or refund an orphan")
					}
				}
				c.Assert(legacy.EndBlock(ctx, mgr), IsNil)
				c.Check(calls, Equals, 0)
			}
			after, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
			c.Assert(err, IsNil)
			c.Check(after, DeepEquals, before)
			outbounds, err := mgr.TxOutStore().GetOutboundItems(ctx)
			c.Assert(err, IsNil)
			c.Check(outbounds, HasLen, 0)
		}
	}
}

func (s *HandlerErrataTxSuite) TestXMRRejectedAddErrataHasNoPoolEffect(c *C) {
	ctx, mgr, na, base, tx, addr := xmrErrataFixture(c)
	tx.Memo = fmt.Sprintf("+:XMR.XMR:%s/%s", GetRandomTHORAddress(), addr)
	recordXMRFinalInbound(ctx, mgr, tx)
	// Model the observation's finalized ADD that the handler rejected, so no
	// receipt or liquidity credit exists on this fresh hard-cutover state.
	msg := NewMsgAddLiquidity(tx, common.XMRAsset, cosmos.ZeroUint(), tx.Coins[0].Amount, common.NoAddress, common.NoAddress, common.NoAddress, cosmos.ZeroUint(), na.NodeAddress)
	c.Assert(NewAddLiquidityHandler(mgr).handle(ctx, *msg), NotNil)
	_, err := NewErrataTxHandler(mgr).Run(ctx, NewMsgErrataTx(tx.ID, common.XMRChain, na.NodeAddress))
	c.Assert(err, IsNil)
	after, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
	c.Assert(err, IsNil)
	c.Check(after, DeepEquals, base)
	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Check(voter.Reverted, Equals, true)
}

func (s *HandlerErrataTxSuite) TestXMRWithdrawalRetiresOldAttributionBeforeReAdd(c *C) {
	for _, pending := range []bool{true, false} {
		ctx, mgr, na, _, tx, addr := xmrErrataFixture(c)
		owner := GetRandomTHORAddress()
		tx.Memo = fmt.Sprintf("+:XMR.XMR:%s/%s", owner, addr)
		recordXMRFinalInbound(ctx, mgr, tx)
		mgr.Keeper().SetMimir(ctx, constants.LiquidityLockUpBlocks.String(), 0)
		add := NewAddLiquidityHandler(mgr)
		assetMsg := NewMsgAddLiquidity(tx, common.XMRAsset, cosmos.ZeroUint(), tx.Coins[0].Amount, owner, addr, common.NoAddress, cosmos.ZeroUint(), na.NodeAddress)
		c.Assert(add.handle(ctx, *assetMsg), IsNil)
		if !pending {
			c.Assert(add.handle(ctx, *NewMsgAddLiquidity(GetRandomTx(), common.XMRAsset, tx.Coins[0].Amount, cosmos.ZeroUint(), owner, addr, common.NoAddress, cosmos.ZeroUint(), na.NodeAddress)), IsNil)
		}
		withdrawMsg := NewMsgWithdrawLiquidity(GetRandomTx(), owner, cosmos.NewUint(5_000), common.XMRAsset, common.EmptyAsset, na.NodeAddress)
		_, _, _, _, err := withdraw(ctx, *withdrawMsg, mgr)
		c.Assert(err, IsNil)
		// Replenishment must not revive the old receipt or poison the pending
		// index; subsequent errata cannot seize these unrelated new units.
		assetMsg.Tx.ID = GetRandomTxHash()
		c.Assert(add.handle(ctx, *assetMsg), IsNil)
		if !pending {
			c.Assert(add.handle(ctx, *NewMsgAddLiquidity(GetRandomTx(), common.XMRAsset, tx.Coins[0].Amount, cosmos.ZeroUint(), owner, addr, common.NoAddress, cosmos.ZeroUint(), na.NodeAddress)), IsNil)
		}
		before, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
		c.Assert(err, IsNil)
		_, err = NewErrataTxHandler(mgr).Run(ctx, NewMsgErrataTx(tx.ID, common.XMRChain, na.NodeAddress))
		c.Assert(err, NotNil)
		after, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
		c.Assert(err, IsNil)
		c.Check(after, DeepEquals, before)
	}
}

func (s *HandlerErrataTxSuite) TestXMRRepeatedStagedDepositsKeepSeparateReceipts(c *C) {
	ctx, mgr, na, base, tx, addr := xmrErrataFixture(c)
	owner := GetRandomTHORAddress()
	add := NewAddLiquidityHandler(mgr)
	var txIDs []common.TxID
	for i := 0; i < 2; i++ {
		tx.ID = GetRandomTxHash()
		tx.Memo = fmt.Sprintf("+:XMR.XMR:%s/%s", owner, addr)
		recordXMRFinalInbound(ctx, mgr, tx)
		txIDs = append(txIDs, tx.ID)
		c.Assert(add.handle(ctx, *NewMsgAddLiquidity(tx, common.XMRAsset, cosmos.ZeroUint(), tx.Coins[0].Amount, owner, addr, common.NoAddress, cosmos.ZeroUint(), na.NodeAddress)), IsNil)
	}
	c.Assert(add.handle(ctx, *NewMsgAddLiquidity(GetRandomTx(), common.XMRAsset, cosmos.NewUint(200*common.One), cosmos.ZeroUint(), owner, addr, common.NoAddress, cosmos.ZeroUint(), na.NodeAddress)), IsNil)
	for i, txID := range txIDs {
		_, err := NewErrataTxHandler(mgr).Run(ctx, NewMsgErrataTx(txID, common.XMRChain, na.NodeAddress))
		c.Assert(err, IsNil)
		pool, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
		c.Assert(err, IsNil)
		remaining := cosmos.NewUint(uint64(1-i) * 100 * common.One)
		c.Check(pool.BalanceAsset.Equal(base.BalanceAsset.Add(remaining)), Equals, true)
		c.Check(pool.BalanceRune.Equal(base.BalanceRune.Add(remaining)), Equals, true)
		c.Check(pool.PendingInboundRune.Equal(cosmos.NewUint(uint64(i+1)*100*common.One)), Equals, true)
	}
	pool, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
	c.Assert(err, IsNil)
	c.Check(pool.LPUnits.Equal(base.LPUnits), Equals, true)
}

func (s *HandlerErrataTxSuite) TestXMRAddErrataReversesAffiliateReceipt(c *C) {
	ctx, mgr, na, base, tx, addr := xmrErrataFixture(c)
	owner, affiliate := GetRandomTHORAddress(), GetRandomTHORAddress()
	tx.Memo = fmt.Sprintf("+:XMR.XMR:%s/%s:%s:1000", owner, addr, affiliate)
	recordXMRFinalInbound(ctx, mgr, tx)
	add := NewAddLiquidityHandler(mgr)
	c.Assert(add.handle(ctx, *NewMsgAddLiquidity(tx, common.XMRAsset, cosmos.ZeroUint(), tx.Coins[0].Amount, owner, addr, affiliate, cosmos.NewUint(1_000), na.NodeAddress)), IsNil)
	c.Assert(add.handle(ctx, *NewMsgAddLiquidity(GetRandomTx(), common.XMRAsset, cosmos.NewUint(100*common.One), cosmos.ZeroUint(), owner, addr, common.NoAddress, cosmos.ZeroUint(), na.NodeAddress)), IsNil)
	receipts, err := mgr.Keeper().GetXMRLiquidityReceipts(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(receipts, HasLen, 2)
	_, err = NewErrataTxHandler(mgr).Run(ctx, NewMsgErrataTx(tx.ID, common.XMRChain, na.NodeAddress))
	c.Assert(err, IsNil)
	for _, lpOwner := range []common.Address{owner, affiliate} {
		lp, err := mgr.Keeper().GetLiquidityProvider(ctx, common.XMRAsset, lpOwner)
		c.Assert(err, IsNil)
		c.Check(lp.Units.IsZero(), Equals, true)
	}
	pool, err := mgr.Keeper().GetPool(ctx, common.XMRAsset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Equal(base.BalanceAsset), Equals, true)
	c.Check(pool.BalanceRune.Equal(base.BalanceRune), Equals, true)
	c.Check(pool.LPUnits.Equal(base.LPUnits), Equals, true)
}

func (s *HandlerErrataTxSuite) TestXMRPendingWithdrawalPoolErrorRollsBackReceipt(c *C) {
	ctx, mgr, na, base, tx, addr := xmrErrataFixture(c)
	owner := GetRandomTHORAddress()
	c.Assert(NewAddLiquidityHandler(mgr).handle(ctx, *NewMsgAddLiquidity(tx, common.XMRAsset, cosmos.ZeroUint(), tx.Coins[0].Amount, owner, addr, common.NoAddress, cosmos.ZeroUint(), na.NodeAddress)), IsNil)
	underlying := mgr.Keeper()
	broken := NewErrataTxHandlerTestHelper(underlying)
	broken.failSetPool = true
	mgr.K = broken
	msg := NewMsgWithdrawLiquidity(GetRandomTx(), owner, cosmos.NewUint(10_000), common.XMRAsset, common.EmptyAsset, na.NodeAddress)
	_, _, _, _, err := withdraw(ctx, *msg, mgr)
	c.Assert(err, NotNil)
	lp, err := underlying.GetLiquidityProvider(ctx, common.XMRAsset, owner)
	c.Assert(err, IsNil)
	c.Check(lp.PendingAsset.Equal(tx.Coins[0].Amount), Equals, true)
	receipts, err := underlying.GetXMRLiquidityReceipts(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(receipts, HasLen, 1)
	c.Check(receipts[0].Withdrawn, Equals, false)
	pool, err := underlying.GetPool(ctx, common.XMRAsset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Equal(base.BalanceAsset), Equals, true)
	c.Check(pool.PendingInboundAsset.Equal(tx.Coins[0].Amount), Equals, true)
}
