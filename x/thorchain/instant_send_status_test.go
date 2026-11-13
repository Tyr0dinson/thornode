package thorchain

import (
	"errors"

	"github.com/cometbft/cometbft/crypto/secp256k1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

func assertInstantSendHalt(c *C, ctx cosmos.Context, mgr *Mgrs, height int64) {
	for _, key := range []string{"HaltDASHTrading", "HaltSigningDASH"} {
		actual, err := mgr.Keeper().GetMimir(ctx, key)
		c.Assert(err, IsNil)
		if height == 0 {
			c.Assert(actual <= 0, Equals, true, Commentf(key))
		} else {
			c.Assert(actual, Equals, height, Commentf(key))
		}
	}
}

func (s *DashAccountingSuite) TestSettlementTimeoutAndLateSettlement(c *C) {
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	_, voter := creditTestDeposit(c, ctx, mgr, vault, tx)
	mgr.Keeper().SetMimir(ctx, "DashInstantSendEnabled", 0)
	before := ctx.WithBlockHeight(ctx.BlockHeight() + 299)
	checkInstantSend(before, mgr)
	assertInstantSendHalt(c, before, mgr, 0)
	deadline := ctx.WithBlockHeight(ctx.BlockHeight() + 300)
	checkInstantSend(deadline, mgr)
	assertInstantSendHalt(c, deadline, mgr, deadline.BlockHeight())
	for _, key := range []string{"HaltDASHChain", "SolvencyHaltDASHChain", "HaltBTCTrading", "HaltSigningBTC"} {
		value, err := mgr.Keeper().GetMimir(deadline, key)
		c.Assert(err, IsNil)
		c.Assert(value <= 0, Equals, true, Commentf(key))
	}
	state, err := mgr.Keeper().GetInstantSend(deadline, common.DASHChain, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(state.Pending, HasLen, 1)
	block := tx
	block.InstantSend = nil
	block.BlockHeight, block.FinaliseHeight = 101, 101
	signers := []cosmos.AccAddress{GetRandomBech32Addr(), GetRandomBech32Addr(), GetRandomBech32Addr()}
	c.Assert(settleTestInstantSend(deadline, mgr, block, voter, signers, 4), IsNil)
	checkInstantSend(deadline.WithBlockHeight(deadline.BlockHeight()+1), mgr)
	assertInstantSendHalt(c, deadline, mgr, deadline.BlockHeight())
	status, err := mgr.Keeper().GetInstantSendStatus(deadline, common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(status.Overdue, HasLen, 0)
	for _, key := range []string{"HaltDASHTrading", "HaltSigningDASH"} {
		mgr.Keeper().SetMimir(deadline, key, 0)
	}
	next := deadline.WithBlockHeight(deadline.BlockHeight() + 2)
	checkInstantSend(next, mgr)
	assertInstantSendHalt(c, next, mgr, 0)
}

func (s *DashAccountingSuite) TestManualClearanceRehaltsWhileOverdue(c *C) {
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	vault, voter := creditTestDeposit(c, ctx, mgr, vault, tx)
	other := tx
	other.Tx.ID = GetRandomTxHash()
	other.SpentOutputRefs = []common.OutputRef{{TxHash: GetRandomTxHash().String()}}
	creditTestDeposit(c, ctx, mgr, vault, other)
	deadline := ctx.WithBlockHeight(ctx.BlockHeight() + 300)
	checkInstantSend(deadline, mgr)
	assertInstantSendHalt(c, deadline, mgr, deadline.BlockHeight())
	tx.InstantSend = nil
	tx.BlockHeight, tx.FinaliseHeight = 101, 101
	c.Assert(settleTestInstantSend(deadline, mgr, tx, voter, []cosmos.AccAddress{GetRandomBech32Addr(), GetRandomBech32Addr(), GetRandomBech32Addr()}, 4), IsNil)
	// Raising the timeout does not erase the other deposit's overdue status.
	mgr.Keeper().SetMimir(deadline, "DashInstantSendTimeout", 600)
	for _, key := range []string{"HaltDASHTrading", "HaltSigningDASH"} {
		mgr.Keeper().SetMimir(deadline, key, 0)
	}
	next := deadline.WithBlockHeight(deadline.BlockHeight() + 1)
	checkInstantSend(next, mgr)
	assertInstantSendHalt(c, next, mgr, next.BlockHeight())
	status, err := mgr.Keeper().GetInstantSendStatus(next, common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(status.Overdue, DeepEquals, []common.TxID{other.Tx.ID})
}

func (s *DashAccountingSuite) TestTimeoutKeepsActiveHaltsAndBringsScheduledHaltsForward(c *C) {
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	_, voter := creditTestDeposit(c, ctx, mgr, vault, tx)
	active := ctx.BlockHeight() - 1
	mgr.Keeper().SetMimir(ctx, "HaltDASHTrading", active)
	mgr.Keeper().SetMimir(ctx, "HaltSigningDASH", ctx.BlockHeight()+1000)
	deadline := ctx.WithBlockHeight(ctx.BlockHeight() + 300)
	checkInstantSend(deadline, mgr)
	for _, settled := range []bool{false, true} {
		if settled {
			tx.InstantSend = nil
			tx.BlockHeight, tx.FinaliseHeight = 101, 101
			c.Assert(settleTestInstantSend(deadline, mgr, tx, voter, []cosmos.AccAddress{GetRandomBech32Addr(), GetRandomBech32Addr(), GetRandomBech32Addr()}, 4), IsNil)
			checkInstantSend(deadline.WithBlockHeight(deadline.BlockHeight()+1), mgr)
		}
		trading, err := mgr.Keeper().GetMimir(deadline, "HaltDASHTrading")
		c.Assert(err, IsNil)
		c.Assert(trading, Equals, active)
		signing, err := mgr.Keeper().GetMimir(deadline, "HaltSigningDASH")
		c.Assert(err, IsNil)
		c.Assert(signing, Equals, deadline.BlockHeight())
	}
}

func (s *DashAccountingSuite) TestTimeoutPreservesFinancialState(c *C) {
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	vault, voter := creditTestDeposit(c, ctx, mgr, vault, tx)
	pool := NewPool()
	pool.Asset = common.DASHAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(200 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	msg := NewMsgSwap(tx.Tx, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, *msg, 0), IsNil)
	state, err := mgr.Keeper().GetInstantSend(ctx, common.DASHChain, vault.PubKey)
	c.Assert(err, IsNil)
	for _, age := range []int64{300, 301, 1000} {
		deadline := ctx.WithBlockHeight(ctx.BlockHeight() + age)
		checkInstantSend(deadline, mgr)
		actual, readErr := mgr.Keeper().GetVault(deadline, vault.PubKey)
		c.Assert(readErr, IsNil)
		c.Assert(actual.Coins, DeepEquals, vault.Coins)
		pending, readErr := mgr.Keeper().GetInstantSend(deadline, common.DASHChain, vault.PubKey)
		c.Assert(readErr, IsNil)
		c.Assert(pending, DeepEquals, state)
		observed, readErr := mgr.Keeper().GetObservedTxInVoter(deadline, tx.Tx.ID)
		c.Assert(readErr, IsNil)
		c.Assert(observed, DeepEquals, voter)
		balance, readErr := mgr.Keeper().GetPool(deadline, common.DASHAsset)
		c.Assert(readErr, IsNil)
		c.Assert(balance, DeepEquals, pool)
		c.Assert(mgr.Keeper().HasSwapQueueItem(deadline, tx.Tx.ID, 0), Equals, true)
	}
}

func (s *DashAccountingSuite) TestInstantSendErrataCompletesWithoutReversal(c *C) {
	for _, legacy := range []bool{false, true} {
		ctx, mgr, vault, tx := dashAccountingFixture(c)
		vault, voter := creditTestDeposit(c, ctx, mgr, vault, tx)
		ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 300)
		checkInstantSend(ctx, mgr)
		nodes := make([]NodeAccount, 4)
		keys := make([]secp256k1.PrivKey, 4)
		for i := range nodes {
			nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		}
		var err error
		if legacy {
			for i := 0; i < 3; i++ {
				cache, commit := ctx.CacheContext()
				_, err = NewErrataTxHandler(mgr).Run(cache, NewMsgErrataTx(tx.Tx.ID, common.DASHChain, nodes[i].NodeAddress))
				c.Assert(err, IsNil)
				commit()
			}
		} else {
			quorum := &common.QuorumErrataTx{ErrataTx: &common.ErrataTx{Id: tx.Tx.ID, Chain: common.DASHChain}}
			for _, key := range keys[:3] {
				quorum.Attestations = append(quorum.Attestations, attestErrataTx(c, key, quorum.ErrataTx))
			}
			cache, commit := ctx.CacheContext()
			_, err = NewErrataTxQuorumHandler(mgr).Run(cache, types.NewMsgErrataTxQuorum(quorum, nodes[0].NodeAddress))
			c.Assert(err, IsNil)
			commit()
			_, err = NewErrataTxQuorumHandler(mgr).Run(ctx, types.NewMsgErrataTxQuorum(quorum, nodes[0].NodeAddress))
		}
		c.Assert(err, IsNil)
		errata, readErr := mgr.Keeper().GetErrataTxVoter(ctx, tx.Tx.ID, common.DASHChain)
		c.Assert(readErr, IsNil)
		c.Assert(errata.BlockHeight, Equals, ctx.BlockHeight())
		for _, node := range nodes[:3] {
			points, pointErr := mgr.Keeper().GetNodeAccountSlashPoints(ctx, node.NodeAddress)
			c.Assert(pointErr, IsNil)
			c.Assert(points, Equals, int64(0))
		}
		actual, readErr := mgr.Keeper().GetVault(ctx, vault.PubKey)
		c.Assert(readErr, IsNil)
		c.Assert(actual.Coins, DeepEquals, vault.Coins)
		observed, readErr := mgr.Keeper().GetObservedTxInVoter(ctx, tx.Tx.ID)
		c.Assert(readErr, IsNil)
		c.Assert(observed, DeepEquals, voter)
		pending, readErr := mgr.Keeper().GetInstantSend(ctx, common.DASHChain, vault.PubKey)
		c.Assert(readErr, IsNil)
		c.Assert(pending.Pending, HasLen, 1)
		assertInstantSendHalt(c, ctx, mgr, ctx.BlockHeight())
	}
}

func (s *DashAccountingSuite) TestTimeoutStateSurvivesGenesisAndSettlement(c *C) {
	for _, disabled := range []bool{false, true} {
		ctx, mgr, vault, tx := dashAccountingFixture(c)
		_, voter := creditTestDeposit(c, ctx, mgr, vault, tx)
		ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 300)
		checkInstantSend(ctx, mgr)
		status, err := mgr.Keeper().GetInstantSendStatus(ctx, common.DASHChain)
		c.Assert(err, IsNil)
		// A coordinated state repair can set this flag. Mimir cannot clear it.
		status.Disabled = disabled
		c.Assert(mgr.Keeper().SetInstantSendStatus(ctx, status), IsNil)
		data := ExportGenesis(ctx, mgr.Keeper())
		c.Assert(validateInstantSendGenesis(data), IsNil)
		next, nextMgr := setupManagerForTest(c)
		next = next.WithBlockHeight(ctx.BlockHeight() + 1)
		initGenesis(next, nextMgr.Keeper(), data)
		assertInstantSendHalt(c, next, nextMgr, ctx.BlockHeight())
		pending, err := nextMgr.Keeper().GetInstantSend(next, common.DASHChain, vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(pending.Pending, HasLen, 1)
		c.Assert(pending.Pending[0].Id, Equals, tx.Tx.ID)
		restored, err := nextMgr.Keeper().GetInstantSendStatus(next, common.DASHChain)
		c.Assert(err, IsNil)
		c.Assert(restored, DeepEquals, status)
		credit, err := nextMgr.Keeper().GetInstantSendInput(next, tx.SpentOutputRefs[0])
		c.Assert(err, IsNil)
		c.Assert(credit.Id, Equals, tx.Tx.ID)
		c.Assert(credit.Height, Equals, int64(0))
		tx.InstantSend = nil
		tx.BlockHeight, tx.FinaliseHeight = 101, 101
		c.Assert(settleTestInstantSend(next, nextMgr, tx, voter, []cosmos.AccAddress{GetRandomBech32Addr(), GetRandomBech32Addr(), GetRandomBech32Addr()}, 4), IsNil)
		checkInstantSend(next, nextMgr)
		assertInstantSendHalt(c, next, nextMgr, ctx.BlockHeight())
		// Manual clearance cannot override a permanent admission disable.
		for _, key := range []string{"HaltDASHTrading", "HaltSigningDASH"} {
			nextMgr.Keeper().SetMimir(next, key, 0)
		}
		nextMgr.Keeper().SetMimir(next, "DashInstantSendEnabled", 1)
		enabled, err := instantSendEnabled(next, nextMgr.Keeper(), common.DASHChain)
		c.Assert(err, IsNil)
		c.Assert(enabled, Equals, !disabled)
	}
}

func (s *DashAccountingSuite) TestSettlementDeadlinePolicy(c *C) {
	for _, timeout := range []int64{-1, 0, 20, 300} {
		ctx, mgr, vault, tx := dashAccountingFixture(c)
		creditTestDeposit(c, ctx, mgr, vault, tx)
		mgr.Keeper().SetMimir(ctx, "DashInstantSendTimeout", timeout)
		age := timeout
		if age <= 0 {
			age = 300
		}
		checkInstantSend(ctx.WithBlockHeight(ctx.BlockHeight()+age-1), mgr)
		assertInstantSendHalt(c, ctx, mgr, 0)
		deadline := ctx.WithBlockHeight(ctx.BlockHeight() + age)
		checkInstantSend(deadline, mgr)
		assertInstantSendHalt(c, deadline, mgr, deadline.BlockHeight())
		checkInstantSend(deadline.WithBlockHeight(deadline.BlockHeight()+1), mgr)
		assertInstantSendHalt(c, deadline, mgr, deadline.BlockHeight())
	}
}

type instantSendReadFailure struct{ keeper.Keeper }

func (k instantSendReadFailure) ListInstantSend(cosmos.Context, common.Chain) ([]common.InstantSendState, error) {
	return nil, errors.New("ledger unavailable")
}

func (s *DashAccountingSuite) TestInvalidSettlementStateHaltsOnlyDash(c *C) {
	for _, missing := range []bool{false, true} {
		ctx, mgr, vault, tx := dashAccountingFixture(c)
		creditTestDeposit(c, ctx, mgr, vault, tx)
		if missing {
			mgr.Keeper().SetObservedTxInVoter(ctx, NewObservedTxVoter(tx.Tx.ID, nil))
		} else {
			mgr.K = instantSendReadFailure{mgr.Keeper()}
		}
		checkInstantSend(ctx, mgr)
		assertInstantSendHalt(c, ctx, mgr, ctx.BlockHeight())
		c.Assert(mgr.Keeper().IsChainTradingHalted(ctx, common.BTCChain), Equals, false)
		c.Assert(mgr.Keeper().IsChainTradingHalted(ctx, common.DASHChain), Equals, true)
		c.Assert(mgr.Keeper().IsChainHalted(ctx, common.DASHChain), Equals, false)
	}
}

func (s *DashAccountingSuite) TestPendingOutboundCannotHideSettlementTimeout(c *C) {
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	vault, _ = creditTestDeposit(c, ctx, mgr, vault, tx)
	item := TxOutItem{
		Chain: common.DASHChain, VaultPubKey: vault.PubKey,
		Coin: vault.GetCoin(common.DASHAsset), InHash: tx.Tx.ID, Memo: NewOutboundMemo(tx.Tx.ID).String(),
	}
	c.Assert(mgr.Keeper().AppendTxOut(ctx, ctx.BlockHeight(), item), IsNil)
	deadline := ctx.WithBlockHeight(ctx.BlockHeight() + 300)
	// Ordinary solvency excuses the unsigned outbound. The deadline does not.
	actual := common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(100*common.One)))
	c.Assert(insolvencyCheck(deadline, mgr, vault, actual, common.DASHChain), Equals, false)
	checkInstantSend(deadline, mgr)
	assertInstantSendHalt(c, deadline, mgr, deadline.BlockHeight())
	state, err := mgr.Keeper().GetInstantSend(deadline, common.DASHChain, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(state.Pending, HasLen, 1)
}

func (s *DashAccountingSuite) TestPendingExposureMayExceedRemainingBalance(c *C) {
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	vault, _ = creditTestDeposit(c, ctx, mgr, vault, tx)
	vault.Coins = common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(1000)))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	actual, err := solvencyVault(ctx, mgr.Keeper(), vault, common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(actual.GetCoin(common.DASHAsset).Amount.IsZero(), Equals, true)
	server := queryServer{mgr: mgr}
	response, err := server.InstantSend(ctx, &types.QueryInstantSendRequest{Chain: "DASH"})
	c.Assert(err, IsNil)
	c.Assert(response.Vaults, HasLen, 1)
	c.Assert(response.Vaults[0].State.PendingAmount().Uint64(), Equals, uint64(common.One))
	c.Assert(response.Vaults[0].Coins, DeepEquals, vault.Coins)
	checkInstantSend(ctx, mgr)
	assertInstantSendHalt(c, ctx, mgr, 0)
}
