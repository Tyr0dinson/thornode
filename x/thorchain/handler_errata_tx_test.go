package thorchain

import (
	"fmt"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

var _ = Suite(&HandlerErrataTxSuite{})

type HandlerErrataTxSuite struct{}

type TestErrataTxKeeper struct {
	keeper.KVStoreDummy
	observedTx ObservedTxVoter
	pool       Pool
	na         NodeAccount
	lps        LiquidityProviders
	err        error
}

func (k *TestErrataTxKeeper) ListActiveValidators(_ cosmos.Context) (NodeAccounts, error) {
	return NodeAccounts{k.na}, k.err
}

func (k *TestErrataTxKeeper) GetNodeAccount(_ cosmos.Context, _ cosmos.AccAddress) (NodeAccount, error) {
	return k.na, k.err
}

func (k *TestErrataTxKeeper) GetObservedTxInVoter(_ cosmos.Context, txID common.TxID) (ObservedTxVoter, error) {
	return k.observedTx, k.err
}

func (k *TestErrataTxKeeper) GetPool(_ cosmos.Context, _ common.Asset) (Pool, error) {
	return k.pool, k.err
}

func (k *TestErrataTxKeeper) SetPool(_ cosmos.Context, pool Pool) error {
	k.pool = pool
	return k.err
}

func (k *TestErrataTxKeeper) GetLiquidityProvider(_ cosmos.Context, asset common.Asset, addr common.Address) (LiquidityProvider, error) {
	for _, lp := range k.lps {
		if lp.RuneAddress.Equals(addr) {
			return lp, k.err
		}
	}
	return LiquidityProvider{}, k.err
}

func (k *TestErrataTxKeeper) SetLiquidityProvider(_ cosmos.Context, lp LiquidityProvider) {
	for i, skr := range k.lps {
		if skr.RuneAddress.Equals(lp.RuneAddress) {
			k.lps[i] = lp
		}
	}
}

func (k *TestErrataTxKeeper) GetErrataTxVoter(_ cosmos.Context, txID common.TxID, chain common.Chain) (ErrataTxVoter, error) {
	return NewErrataTxVoter(txID, chain), k.err
}

func (s *HandlerErrataTxSuite) TestValidate(c *C) {
	ctx, _ := setupKeeperForTest(c)

	keeper := &TestErrataTxKeeper{
		na: GetRandomValidatorNode(NodeActive),
	}

	handler := NewErrataTxHandler(NewDummyMgrWithKeeper(keeper))
	// happy path
	msg := NewMsgErrataTx(GetRandomTxHash(), common.ETHChain, keeper.na.NodeAddress)
	err := handler.validate(ctx, *msg)
	c.Assert(err, IsNil)

	// invalid msg
	msg = &MsgErrataTx{}
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
}

func (s *HandlerErrataTxSuite) TestErrataHandlerPairedFallback(c *C) {
	ctx, mgr := setupManagerForTest(c)

	txID := GetRandomTxHash()
	na := GetRandomValidatorNode(NodeActive)
	addr := GetRandomRUNEAddress()
	totalUnits := cosmos.NewUint(1600)
	observedTx := ObservedTx{
		Tx: common.Tx{
			ID:          txID,
			Chain:       common.ETHChain,
			FromAddress: addr,
			Coins: common.Coins{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(30*common.One)),
			},
			Memo: fmt.Sprintf("ADD:ETH.ETH:%s", addr),
		},
	}
	keeper := &TestErrataTxKeeper{
		na: na,
		observedTx: ObservedTxVoter{
			TxID:         txID,
			Tx:           observedTx,
			Txs:          ObservedTxs{observedTx},
			Height:       1024,
			UpdatedVault: true,
		},
		pool: Pool{
			Asset:        common.ETHAsset,
			LPUnits:      totalUnits,
			BalanceRune:  cosmos.NewUint(100 * common.One),
			BalanceAsset: cosmos.NewUint(100 * common.One),
		},
		lps: LiquidityProviders{
			{
				Asset:         common.ETHAsset,
				RuneAddress:   addr,
				LastAddHeight: 5,
				Units:         totalUnits.QuoUint64(2),
				PendingRune:   cosmos.ZeroUint(),
				PendingAsset:  cosmos.ZeroUint(),
			},
			{
				Asset:         common.ETHAsset,
				RuneAddress:   GetRandomRUNEAddress(),
				LastAddHeight: 10,
				Units:         totalUnits.QuoUint64(2),
				PendingRune:   cosmos.ZeroUint(),
				PendingAsset:  cosmos.ZeroUint(),
			},
		},
	}
	mgr.K = keeper

	handler := NewErrataTxHandler(mgr)
	msg := NewMsgErrataTx(txID, common.ETHChain, na.NodeAddress)
	_, err := handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	c.Check(keeper.pool.BalanceRune.Equal(cosmos.NewUint(100*common.One)), Equals, true)
	c.Check(keeper.pool.BalanceAsset.Equal(cosmos.NewUint(70*common.One)), Equals, true)
	c.Check(keeper.pool.LPUnits.Equal(totalUnits), Equals, true)
	c.Check(keeper.lps[0].Units.Equal(totalUnits.QuoUint64(2)), Equals, true)
	c.Check(keeper.lps[1].Units.Equal(totalUnits.QuoUint64(2)), Equals, true)
	c.Check(keeper.lps[0].LastAddHeight, Equals, int64(5))
}

type ErrataTxHandlerTestHelper struct {
	keeper.Keeper
	failListActiveNodeAccount bool
	failGetErrataTxVoter      bool
	failGetObserveTxVoter     bool
	failGetPool               bool
	failGetLiquidityProvider  bool
	failSetPool               bool
}

func NewErrataTxHandlerTestHelper(k keeper.Keeper) *ErrataTxHandlerTestHelper {
	return &ErrataTxHandlerTestHelper{
		Keeper: k,
	}
}

func (k *ErrataTxHandlerTestHelper) ListActiveValidators(ctx cosmos.Context) (NodeAccounts, error) {
	if k.failListActiveNodeAccount {
		return NodeAccounts{}, errKaboom
	}
	return k.Keeper.ListActiveValidators(ctx)
}

func (k *ErrataTxHandlerTestHelper) GetErrataTxVoter(ctx cosmos.Context, txID common.TxID, chain common.Chain) (ErrataTxVoter, error) {
	if k.failGetErrataTxVoter {
		return ErrataTxVoter{}, errKaboom
	}
	return k.Keeper.GetErrataTxVoter(ctx, txID, chain)
}

func (k *ErrataTxHandlerTestHelper) GetObservedTxInVoter(ctx cosmos.Context, txID common.TxID) (ObservedTxVoter, error) {
	if k.failGetObserveTxVoter {
		return ObservedTxVoter{}, errKaboom
	}
	return k.Keeper.GetObservedTxInVoter(ctx, txID)
}

func (k *ErrataTxHandlerTestHelper) GetPool(ctx cosmos.Context, asset common.Asset) (Pool, error) {
	if k.failGetPool {
		return NewPool(), errKaboom
	}
	return k.Keeper.GetPool(ctx, asset)
}

func (k *ErrataTxHandlerTestHelper) GetLiquidityProvider(ctx cosmos.Context, asset common.Asset, addr common.Address) (LiquidityProvider, error) {
	if k.failGetLiquidityProvider {
		return LiquidityProvider{}, errKaboom
	}
	return k.Keeper.GetLiquidityProvider(ctx, asset, addr)
}

func (k *ErrataTxHandlerTestHelper) SetPool(ctx cosmos.Context, pool Pool) error {
	if k.failSetPool {
		return errKaboom
	}
	return k.Keeper.SetPool(ctx, pool)
}

func (s *HandlerErrataTxSuite) TestErrataHandlerDifferentError(c *C) {
	testCases := []struct {
		name            string
		messageProvider func(ctx cosmos.Context, helper *ErrataTxHandlerTestHelper) cosmos.Msg
		validator       func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ErrataTxHandlerTestHelper, name string)
	}{
		{
			name: "invalid message should return an error",
			messageProvider: func(ctx cosmos.Context, helper *ErrataTxHandlerTestHelper) cosmos.Msg {
				return NewMsgNetworkFee(1024, common.ETHChain, 1, 10000, GetRandomBech32Addr())
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ErrataTxHandlerTestHelper, name string) {
				c.Check(result, IsNil, Commentf(name))
				c.Check(err, NotNil, Commentf(name))
			},
		},
		{
			name: "message fail validation should return an error",
			messageProvider: func(ctx cosmos.Context, helper *ErrataTxHandlerTestHelper) cosmos.Msg {
				return NewMsgErrataTx(GetRandomTxHash(), common.BTCChain, GetRandomBech32Addr())
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ErrataTxHandlerTestHelper, name string) {
				c.Check(result, IsNil, Commentf(name))
				c.Check(err, NotNil, Commentf(name))
			},
		},
		{
			name: "fail to list active account should return an error",
			messageProvider: func(ctx cosmos.Context, helper *ErrataTxHandlerTestHelper) cosmos.Msg {
				// add an active node account
				nodeAccount := GetRandomValidatorNode(NodeActive)
				c.Assert(helper.SetNodeAccount(ctx, nodeAccount), IsNil)
				helper.failListActiveNodeAccount = true
				return NewMsgErrataTx(GetRandomTxHash(), common.BTCChain, nodeAccount.NodeAddress)
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ErrataTxHandlerTestHelper, name string) {
				c.Check(result, IsNil, Commentf(name))
				c.Check(err, NotNil, Commentf(name))
			},
		},
		{
			name: "fail to get errata tx voter should return an error",
			messageProvider: func(ctx cosmos.Context, helper *ErrataTxHandlerTestHelper) cosmos.Msg {
				// add an active node account
				nodeAccount := GetRandomValidatorNode(NodeActive)
				c.Assert(helper.SetNodeAccount(ctx, nodeAccount), IsNil)
				helper.failGetErrataTxVoter = true
				return NewMsgErrataTx(GetRandomTxHash(), common.BTCChain, nodeAccount.NodeAddress)
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ErrataTxHandlerTestHelper, name string) {
				c.Check(result, IsNil, Commentf(name))
				c.Check(err, NotNil, Commentf(name))
			},
		},
		{
			name: "if voter already sign the errata tx voter it should not do anything",
			messageProvider: func(ctx cosmos.Context, helper *ErrataTxHandlerTestHelper) cosmos.Msg {
				// add an active node account
				nodeAccount := GetRandomValidatorNode(NodeActive)
				c.Assert(helper.SetNodeAccount(ctx, nodeAccount), IsNil)
				txID := GetRandomTxHash()
				voter, _ := helper.Keeper.GetErrataTxVoter(ctx, txID, common.BTCChain)
				voter.Sign(nodeAccount.NodeAddress)
				helper.Keeper.SetErrataTxVoter(ctx, voter)
				return NewMsgErrataTx(txID, common.BTCChain, nodeAccount.NodeAddress)
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ErrataTxHandlerTestHelper, name string) {
				c.Check(result, NotNil, Commentf(name))
				c.Check(err, IsNil, Commentf(name))
			},
		},
		{
			name: "if voter doesn't have consensus it should not do anything",
			messageProvider: func(ctx cosmos.Context, helper *ErrataTxHandlerTestHelper) cosmos.Msg {
				// add an active node account
				nodeAccount := GetRandomValidatorNode(NodeActive)
				c.Assert(helper.SetNodeAccount(ctx, nodeAccount), IsNil)
				txID := GetRandomTxHash()
				nodeAcct1 := GetRandomValidatorNode(NodeActive)
				c.Assert(helper.SetNodeAccount(ctx, nodeAcct1), IsNil)
				return NewMsgErrataTx(txID, common.BTCChain, nodeAccount.NodeAddress)
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ErrataTxHandlerTestHelper, name string) {
				c.Check(result, NotNil, Commentf(name))
				c.Check(err, IsNil, Commentf(name))
			},
		},
		{
			name: "if voter had been processed it should not do anything",
			messageProvider: func(ctx cosmos.Context, helper *ErrataTxHandlerTestHelper) cosmos.Msg {
				// add an active node account
				nodeAccount := GetRandomValidatorNode(NodeActive)
				c.Assert(helper.SetNodeAccount(ctx, nodeAccount), IsNil)
				txID := GetRandomTxHash()
				voter, _ := helper.Keeper.GetErrataTxVoter(ctx, txID, common.BTCChain)
				voter.BlockHeight = ctx.BlockHeight()
				helper.Keeper.SetErrataTxVoter(ctx, voter)
				return NewMsgErrataTx(txID, common.BTCChain, nodeAccount.NodeAddress)
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ErrataTxHandlerTestHelper, name string) {
				c.Check(result, NotNil, Commentf(name))
				c.Check(err, IsNil, Commentf(name))
			},
		},
		{
			name: "if fail to get observed tx in it should return err",
			messageProvider: func(ctx cosmos.Context, helper *ErrataTxHandlerTestHelper) cosmos.Msg {
				// add an active node account
				nodeAccount := GetRandomValidatorNode(NodeActive)
				c.Assert(helper.SetNodeAccount(ctx, nodeAccount), IsNil)
				helper.failGetObserveTxVoter = true
				return NewMsgErrataTx(GetRandomTxHash(), common.BTCChain, nodeAccount.NodeAddress)
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ErrataTxHandlerTestHelper, name string) {
				c.Check(result, IsNil, Commentf(name))
				c.Check(err, NotNil, Commentf(name))
			},
		},
		{
			name: "if observed tx is empty it should return err",
			messageProvider: func(ctx cosmos.Context, helper *ErrataTxHandlerTestHelper) cosmos.Msg {
				// add an active node account
				nodeAccount := GetRandomValidatorNode(NodeActive)
				c.Assert(helper.SetNodeAccount(ctx, nodeAccount), IsNil)
				return NewMsgErrataTx(GetRandomTxHash(), common.BTCChain, nodeAccount.NodeAddress)
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ErrataTxHandlerTestHelper, name string) {
				c.Check(result, IsNil, Commentf(name))
				c.Check(err, NotNil, Commentf(name))
			},
		},
		{
			name: "if chain doesn't match it should not do anything",
			messageProvider: func(ctx cosmos.Context, helper *ErrataTxHandlerTestHelper) cosmos.Msg {
				// add an active node account
				nodeAccount := GetRandomValidatorNode(NodeActive)
				c.Assert(helper.SetNodeAccount(ctx, nodeAccount), IsNil)
				observedTx := GetRandomObservedTx()
				voter := ObservedTxVoter{
					TxID:   observedTx.Tx.ID,
					Tx:     observedTx,
					Txs:    ObservedTxs{observedTx},
					Height: observedTx.BlockHeight,
				}
				helper.Keeper.SetObservedTxInVoter(ctx, voter)
				return NewMsgErrataTx(voter.TxID, common.BTCChain, nodeAccount.NodeAddress)
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ErrataTxHandlerTestHelper, name string) {
				c.Check(result, NotNil, Commentf(name))
				c.Check(err, IsNil, Commentf(name))
			},
		},
		{
			name: "if the tx is not swap nor provide liquidity, it should not do anything",
			messageProvider: func(ctx cosmos.Context, helper *ErrataTxHandlerTestHelper) cosmos.Msg {
				// add an active node account
				nodeAccount := GetRandomValidatorNode(NodeActive)
				c.Assert(helper.SetNodeAccount(ctx, nodeAccount), IsNil)
				observedTx := GetRandomObservedTx()
				observedTx.Tx.Chain = common.BTCChain
				observedTx.Tx.Memo = "withdraw"
				voter := ObservedTxVoter{
					TxID:   observedTx.Tx.ID,
					Tx:     observedTx,
					Txs:    ObservedTxs{observedTx},
					Height: observedTx.BlockHeight,
				}
				helper.Keeper.SetObservedTxInVoter(ctx, voter)
				return NewMsgErrataTx(voter.TxID, common.BTCChain, nodeAccount.NodeAddress)
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ErrataTxHandlerTestHelper, name string) {
				c.Check(result, NotNil, Commentf(name))
				c.Check(err, IsNil, Commentf(name))
			},
		},
		{
			name: "if it fail to get pool it should return an error",
			messageProvider: func(ctx cosmos.Context, helper *ErrataTxHandlerTestHelper) cosmos.Msg {
				// add an active node account
				nodeAccount := GetRandomValidatorNode(NodeActive)
				c.Assert(helper.SetNodeAccount(ctx, nodeAccount), IsNil)
				observedTx := GetRandomObservedTx()
				observedTx.Tx.Chain = common.BTCChain
				observedTx.Tx.Memo = "swap:ETH"
				helper.failGetPool = true
				voter := ObservedTxVoter{
					TxID:   observedTx.Tx.ID,
					Tx:     observedTx,
					Txs:    ObservedTxs{observedTx},
					Height: observedTx.BlockHeight,
				}
				helper.Keeper.SetObservedTxInVoter(ctx, voter)
				return NewMsgErrataTx(voter.TxID, common.BTCChain, nodeAccount.NodeAddress)
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ErrataTxHandlerTestHelper, name string) {
				c.Check(result, IsNil, Commentf(name))
				c.Check(err, NotNil, Commentf(name))
			},
		},
		{
			name: "legacy ADD uses pool fallback without an LP lookup",
			messageProvider: func(ctx cosmos.Context, helper *ErrataTxHandlerTestHelper) cosmos.Msg {
				// add an active node account
				nodeAccount := GetRandomValidatorNode(NodeActive)
				c.Assert(helper.SetNodeAccount(ctx, nodeAccount), IsNil)
				observedTx := GetRandomObservedTx()
				observedTx.Tx.Chain = common.BTCChain
				observedTx.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)))
				observedTx.Tx.Memo = "add:BTC:" + observedTx.Tx.FromAddress.String()
				lp := LiquidityProvider{
					Asset:         common.BTCAsset,
					AssetAddress:  GetRandomETHAddress(),
					LastAddHeight: 1024,
					RuneAddress:   observedTx.Tx.FromAddress,
				}
				helper.SetLiquidityProvider(ctx, lp)
				helper.failGetLiquidityProvider = true
				pool := NewPool()
				pool.Asset = common.BTCAsset
				pool.BalanceRune = cosmos.NewUint(common.One * 100)
				pool.BalanceAsset = cosmos.NewUint(common.One * 100)
				pool.Status = PoolAvailable
				c.Assert(helper.Keeper.SetPool(ctx, pool), IsNil)
				// The executed ADD credited this vault, so errata debits it.
				vault := NewVault(1, types.VaultStatus_ActiveVault, AsgardVault, observedTx.ObservedPubKey, []string{common.BTCChain.String()}, []ChainContract{})
				vault.AddFunds(observedTx.Tx.Coins)
				c.Assert(helper.Keeper.SetVault(ctx, vault), IsNil)
				voter := ObservedTxVoter{
					TxID:         observedTx.Tx.ID,
					Tx:           observedTx,
					Txs:          ObservedTxs{observedTx},
					Height:       observedTx.BlockHeight,
					UpdatedVault: true,
				}
				helper.Keeper.SetObservedTxInVoter(ctx, voter)
				return NewMsgErrataTx(voter.TxID, common.BTCChain, nodeAccount.NodeAddress)
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ErrataTxHandlerTestHelper, name string) {
				c.Check(result, NotNil, Commentf(name))
				c.Check(err, IsNil, Commentf(name))
				pool, poolErr := helper.Keeper.GetPool(ctx, common.BTCAsset)
				c.Assert(poolErr, IsNil)
				c.Check(pool.BalanceAsset.Equal(cosmos.NewUint(99*common.One)), Equals, true)
				c.Check(pool.BalanceRune.Equal(cosmos.NewUint(100*common.One)), Equals, true)
			},
		},
		{
			name: "fail to save pool should error out",
			messageProvider: func(ctx cosmos.Context, helper *ErrataTxHandlerTestHelper) cosmos.Msg {
				// add an active node account
				nodeAccount := GetRandomValidatorNode(NodeActive)
				c.Assert(helper.SetNodeAccount(ctx, nodeAccount), IsNil)
				observedTx := GetRandomObservedTx()
				observedTx.Tx.Chain = common.BTCChain
				observedTx.Tx.Memo = "swap:BTC"
				helper.failSetPool = true
				pool := NewPool()
				pool.Asset = common.BTCAsset
				pool.BalanceRune = cosmos.NewUint(common.One * 100)
				pool.BalanceAsset = cosmos.NewUint(common.One * 100)
				pool.Status = PoolAvailable
				c.Assert(helper.Keeper.SetPool(ctx, pool), IsNil)
				voter := ObservedTxVoter{
					TxID:   observedTx.Tx.ID,
					Tx:     observedTx,
					Txs:    ObservedTxs{observedTx},
					Height: observedTx.BlockHeight,
				}
				helper.Keeper.SetObservedTxInVoter(ctx, voter)
				return NewMsgErrataTx(voter.TxID, common.BTCChain, nodeAccount.NodeAddress)
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ErrataTxHandlerTestHelper, name string) {
				c.Check(result, IsNil, Commentf(name))
				c.Check(err, NotNil, Commentf(name))
			},
		},
	}

	for _, tc := range testCases {
		ctx, mgr := setupManagerForTest(c)
		helper := NewErrataTxHandlerTestHelper(mgr.Keeper())
		mgr.K = helper
		msg := tc.messageProvider(ctx, helper)
		handler := NewErrataTxHandler(mgr)
		result, err := handler.Run(ctx, msg)
		tc.validator(c, ctx, result, err, helper, tc.name)
	}
}

func (*HandlerErrataTxSuite) TestProcessErrataOutboundTx(c *C) {
	ctx, mgr := setupManagerForTest(c)
	helper := NewErrataTxHandlerTestHelper(mgr.Keeper())
	node1 := GetRandomValidatorNode(NodeActive)
	node2 := GetRandomValidatorNode(NodeActive)
	node3 := GetRandomValidatorNode(NodeActive)
	c.Assert(helper.Keeper.SetNodeAccount(ctx, node1), IsNil)
	c.Assert(helper.Keeper.SetNodeAccount(ctx, node2), IsNil)
	c.Assert(helper.Keeper.SetNodeAccount(ctx, node3), IsNil)

	// fail to get observed tx out voter
	txID := GetRandomTxHash()
	er := &common.ErrataTx{
		Id:    txID,
		Chain: common.LTCChain,
	}
	k := mgr.Keeper()
	eventMgr := mgr.EventMgr()
	_, err := processErrataOutboundTx(ctx, k, eventMgr, er)
	c.Assert(err, NotNil)

	observedPubKey := GetRandomPubKey()
	tx := common.NewTx(txID, GetRandomLTCAddress(), GetRandomLTCAddress(),
		common.Coins{
			common.NewCoin(common.LTCAsset, cosmos.NewUint(102400000)),
		},
		common.Gas{
			common.NewCoin(common.LTCAsset, cosmos.NewUint(1000)),
		}, "swap:LTC.LTC")
	observedTx := []common.ObservedTx{
		common.NewObservedTx(
			tx,
			1024, observedPubKey, 1024,
		),
	}
	txOutVoter := NewObservedTxVoter(txID, observedTx)
	helper.Keeper.SetObservedTxOutVoter(ctx, txOutVoter)
	// Tx is empty , it should fail
	_, err = processErrataOutboundTx(ctx, k, eventMgr, er)
	c.Assert(err, NotNil)
	txOutVoter.Add(observedTx[0], node2.NodeAddress)
	txOutVoter.Add(observedTx[0], node3.NodeAddress)

	finalisedTx := txOutVoter.GetTx(NodeAccounts{node1, node2, node3})
	c.Assert(finalisedTx.IsEmpty(), Equals, false)
	// voter.Tx must be explicitly updated in the handler,
	// for instance on consensus.
	txOutVoter.Tx = *finalisedTx

	helper.Keeper.SetObservedTxOutVoter(ctx, txOutVoter)

	// not outbound tx , it should fail
	_, err = processErrataOutboundTx(ctx, k, eventMgr, er)
	c.Assert(err, NotNil)

	// fail to get vault
	txInID := GetRandomTxHash()
	txOutVoter.Tx.Tx.Memo = "OUT:" + txInID.String()
	helper.Keeper.SetObservedTxOutVoter(ctx, txOutVoter)
	_, err = processErrataOutboundTx(ctx, k, eventMgr, er)
	c.Assert(err, NotNil)

	// Active Asgard vault, TxInVoter not exist
	asgardVault := NewVault(1, types.VaultStatus_ActiveVault, AsgardVault, observedPubKey, []string{
		common.LTCChain.String(),
		common.BTCChain.String(),
		common.ETHChain.String(),
	}, []ChainContract{})
	c.Assert(helper.Keeper.SetVault(ctx, asgardVault), IsNil)
	_, err = processErrataOutboundTx(ctx, k, eventMgr, er)
	c.Assert(err, IsNil)

	// inactive vault - funds are credited but vault stays InactiveVault
	// (no resurrection to RetiringVault to avoid side effects)
	asgardVault.UpdateStatus(types.VaultStatus_InactiveVault, 1024)
	c.Assert(helper.Keeper.SetVault(ctx, asgardVault), IsNil)
	_, err = processErrataOutboundTx(ctx, k, eventMgr, er)
	c.Assert(err, IsNil)
	// vault should remain inactive (recovery via migration)
	v, err := helper.Keeper.GetVault(ctx, asgardVault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(v.Status == types.VaultStatus_InactiveVault, Equals, true)

	// With POOL , but no txin voter
	pool := NewPool()
	pool.Asset = common.LTCAsset
	pool.BalanceAsset = cosmos.NewUint(1024 * common.One)
	pool.BalanceRune = cosmos.NewUint(1024 * common.One)
	pool.Status = PoolAvailable
	c.Assert(helper.Keeper.SetPool(ctx, pool), IsNil)
	_, err = processErrataOutboundTx(ctx, k, eventMgr, er)
	c.Assert(err, IsNil)

	txInbound := common.NewTx(txInID, GetRandomLTCAddress(), GetRandomLTCAddress(),
		common.Coins{
			common.NewCoin(common.LTCAsset, cosmos.NewUint(102400000)),
		},
		common.Gas{
			common.NewCoin(common.LTCAsset, cosmos.NewUint(1000)),
		}, "swap:LTC.LTC")
	observedTxInbound := []common.ObservedTx{
		NewObservedTx(
			txInbound,
			1024, observedPubKey, 1024,
		),
	}
	txInVoter := NewObservedTxVoter(txInID, observedTxInbound)
	helper.Keeper.SetObservedTxInVoter(ctx, txInVoter)
	newActiveAsgardVault := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{
		common.BTCChain.String(),
		common.LTCChain.String(),
		common.ETHChain.String(),
	}, []ChainContract{})
	newActiveAsgardVault.AddFunds(common.Coins{
		common.NewCoin(common.LTCAsset, cosmos.NewUint(1024*common.One)),
	})
	c.Assert(helper.Keeper.SetVault(ctx, newActiveAsgardVault), IsNil)
	c.Assert(helper.Keeper.SaveNetworkFee(ctx, common.LTCChain, NetworkFee{
		Chain:              common.LTCChain,
		TransactionSize:    250,
		TransactionFeeRate: 10,
	}), IsNil)

	// clear events
	ctx = ctx.WithEventManager(cosmos.NewEventManager())

	_, err = processErrataOutboundTx(ctx, k, eventMgr, er)
	c.Assert(err, IsNil)
	txOut, err := helper.Keeper.GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)

	// we no longer re-attempt the outbound, and instead just emit a security event
	c.Assert(txOut.TxArray, HasLen, 0)
	found := 0
	for _, event := range ctx.EventManager().Events() {
		if event.Type == "security" {
			found++
		}
	}
	c.Assert(found, Equals, 1)
}

func (*HandlerErrataTxSuite) TestProcessErrataOutboundTx_EnsureFundsAreCreditedToInactiveVault(c *C) {
	ctx, mgr := setupManagerForTest(c)
	helper := NewErrataTxHandlerTestHelper(mgr.Keeper())
	mgr.K = helper
	k := mgr.Keeper()
	eventMgr := mgr.EventMgr()
	handler := NewErrataTxHandler(mgr)
	node1 := GetRandomValidatorNode(NodeActive)
	node2 := GetRandomValidatorNode(NodeActive)
	node3 := GetRandomValidatorNode(NodeActive)
	c.Assert(helper.Keeper.SetNodeAccount(ctx, node1), IsNil)
	c.Assert(helper.Keeper.SetNodeAccount(ctx, node2), IsNil)
	c.Assert(helper.Keeper.SetNodeAccount(ctx, node3), IsNil)

	retiredPubKey := GetRandomPubKey()
	activePubKey := GetRandomPubKey()
	// inactive vault, TxInVoter not exist
	inactiveVault := NewVault(1, types.VaultStatus_InactiveVault, AsgardVault, retiredPubKey, []string{
		common.LTCChain.String(),
		common.BTCChain.String(),
		common.ETHChain.String(),
	}, []ChainContract{})

	activeVault := NewVault(1, types.VaultStatus_ActiveVault, AsgardVault, activePubKey, []string{
		common.LTCChain.String(),
		common.BTCChain.String(),
		common.ETHChain.String(),
	}, []ChainContract{})
	c.Assert(helper.Keeper.SetVault(ctx, inactiveVault), IsNil)
	c.Assert(helper.Keeper.SetVault(ctx, activeVault), IsNil)
	// migration , internal tx , should cause the vault to be set back to retiring

	txID1 := GetRandomTxHash()
	internalMigrationTx := NewMsgErrataTx(txID1, common.LTCChain, node1.NodeAddress)
	internalErrata := &common.ErrataTx{
		Id:    txID1,
		Chain: common.LTCChain,
	}

	migrateTx := common.NewTx(txID1,
		GetRandomLTCAddress(),
		GetRandomLTCAddress(),
		common.Coins{
			common.NewCoin(common.LTCAsset, cosmos.NewUint(102400000)),
		},
		common.Gas{
			common.NewCoin(common.LTCAsset, cosmos.NewUint(1000)),
		}, "migrate:111")

	// observed outbound tx in the retired vault
	observedTx := []common.ObservedTx{
		common.NewObservedTx(
			migrateTx,
			1024, retiredPubKey, 1024,
		),
	}
	txOutVoter := NewObservedTxVoter(txID1, observedTx)
	txOutVoter.Add(observedTx[0], node2.NodeAddress)
	txOutVoter.Add(observedTx[0], node3.NodeAddress)

	finalisedTx := txOutVoter.GetTx(NodeAccounts{node1, node2, node3})
	c.Assert(finalisedTx.IsEmpty(), Equals, false)
	// voter.Tx must be explicitly updated in the handler,
	// for instance on consensus.
	txOutVoter.Tx = *finalisedTx

	helper.Keeper.SetObservedTxOutVoter(ctx, txOutVoter)

	// (identical) observed inbound tx in the same retired vault
	observedInboundTx := []common.ObservedTx{
		common.NewObservedTx(

			migrateTx,
			1024, retiredPubKey, 1024,
		),
	}
	txInVoter := NewObservedTxVoter(txID1, observedTx)
	txInVoter.Add(observedInboundTx[0], node2.NodeAddress)
	txInVoter.Add(observedInboundTx[0], node3.NodeAddress)

	finalisedTx = txInVoter.GetTx(NodeAccounts{node1, node2, node3})
	c.Assert(finalisedTx.IsEmpty(), Equals, false)
	// voter.Tx must be explicitly updated in the handler,
	// for instance on consensus.
	txInVoter.Tx = *finalisedTx
	helper.Keeper.SetObservedTxInVoter(ctx, txInVoter)

	_, err := processErrataOutboundTx(ctx, k, eventMgr, internalErrata)
	c.Assert(err, IsNil)
	v, err := helper.Keeper.GetVault(ctx, retiredPubKey)
	c.Assert(err, IsNil)
	// Vault should remain InactiveVault (not resurrected to RetiringVault)
	// to avoid side effects like blocking churns or affecting node unbonding.
	// Recovery from InactiveVaults with funds should be handled via migration.
	c.Assert(v.Status == types.VaultStatus_InactiveVault, Equals, true)
	c.Assert(v.HasFunds(), Equals, true)
	ltcCoin := v.GetCoin(common.LTCAsset)
	c.Assert(ltcCoin.Equals(common.NewCoin(common.LTCAsset, cosmos.NewUint(102400000))), Equals, true)

	// reset the inactive vault
	c.Assert(helper.Keeper.SetVault(ctx, inactiveVault), IsNil)
	errataVoter := NewErrataTxVoter(txID1, common.LTCChain)
	errataVoter.Sign(node2.NodeAddress)
	errataVoter.Sign(node3.NodeAddress)
	helper.Keeper.SetErrataTxVoter(ctx, errataVoter)
	result, err := handler.handle(ctx, *internalMigrationTx)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
	v, err = helper.Keeper.GetVault(ctx, retiredPubKey)
	c.Assert(err, IsNil)
	// Same here - vault stays InactiveVault, funds are credited
	c.Assert(v.Status == types.VaultStatus_InactiveVault, Equals, true)
	c.Assert(v.HasFunds(), Equals, true)
	ltcCoin = v.GetCoin(common.LTCAsset)
	c.Assert(ltcCoin.Equals(common.NewCoin(common.LTCAsset, cosmos.NewUint(102400000))), Equals, true)
}

func (s *HandlerErrataTxSuite) TestObservingSlashing(c *C) {
	ctx, mgr := setupManagerForTest(c)
	height := int64(1024)
	ctx = ctx.WithBlockHeight(height)

	// Check expected slash point amounts
	observeSlashPoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	lackOfObservationPenalty := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observeFlex := mgr.GetConstants().GetInt64Value(constants.ObservationDelayFlexibility)
	c.Assert(observeSlashPoints, Equals, int64(1))
	c.Assert(lackOfObservationPenalty, Equals, int64(2))
	c.Assert(observeFlex, Equals, int64(10))

	asgardVault := GetRandomVault()
	c.Assert(mgr.Keeper().SetVault(ctx, asgardVault), IsNil)

	nas := NodeAccounts{
		// 6 Active nodes, 1 Standby node; 2/3rds consensus needs 4.
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeStandby),
	}
	for _, item := range nas {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, item), IsNil)
	}

	txID := GetRandomTxHash()
	addr := GetRandomETHAddress()
	totalUnits := cosmos.NewUint(1600)

	observedTx := ObservedTx{
		Tx: common.Tx{
			ID:          txID,
			Chain:       common.ETHChain,
			FromAddress: addr,
			Coins: common.Coins{
				common.NewCoin(common.RuneAsset(), cosmos.NewUint(30*common.One)),
			},
			Memo: fmt.Sprintf("ADD:ETH.ETH:%s", GetRandomRUNEAddress()),
		},
		BlockHeight:    1024,
		FinaliseHeight: 1024,
	}
	voter := ObservedTxVoter{
		TxID:            observedTx.Tx.ID,
		Tx:              observedTx,
		Txs:             common.ObservedTxs{observedTx},
		Height:          observedTx.BlockHeight,
		FinalisedHeight: observedTx.BlockHeight,
	}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	pool := Pool{
		Asset:        common.ETHAsset,
		LPUnits:      totalUnits,
		BalanceRune:  cosmos.NewUint(100 * common.One),
		BalanceAsset: cosmos.NewUint(100 * common.One),
		Status:       PoolAvailable,
	}
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	lp := LiquidityProvider{
		RuneAddress:   addr,
		LastAddHeight: 5,
		Units:         totalUnits.QuoUint64(2),
		PendingRune:   cosmos.ZeroUint(),
	}
	mgr.Keeper().SetLiquidityProvider(ctx, lp)
	lp = LiquidityProvider{
		RuneAddress:   GetRandomETHAddress(),
		LastAddHeight: 10,
		Units:         totalUnits.QuoUint64(2),
		PendingRune:   cosmos.ZeroUint(),
	}
	mgr.Keeper().SetLiquidityProvider(ctx, lp)

	msg := NewMsgErrataTx(voter.TxID, common.BTCChain, cosmos.AccAddress{})
	handler := NewErrataTxHandler(mgr)

	broadcast := func(c *C, ctx cosmos.Context, na NodeAccount, msg *MsgErrataTx) {
		msg.Signer = na.NodeAddress
		_, err := handler.handle(ctx, *msg)
		c.Assert(err, IsNil)
	}

	checkSlashPoints := func(c *C, ctx cosmos.Context, nas NodeAccounts, expected [7]int64) {
		var slashPoints [7]int64
		for i, na := range nas {
			slashPoint, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
			c.Assert(err, IsNil)
			slashPoints[i] = slashPoint
		}
		c.Assert(slashPoints == expected, Equals, true, Commentf(fmt.Sprint(slashPoints)))
	}

	checkSlashPoints(c, ctx, nas, [7]int64{0, 0, 0, 0, 0, 0, 0})

	// 3/6 Active nodes observe.
	broadcast(c, ctx, nas[0], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{1, 0, 0, 0, 0, 0, 0})
	broadcast(c, ctx, nas[1], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{1, 1, 0, 0, 0, 0, 0})
	broadcast(c, ctx, nas[2], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{1, 1, 1, 0, 0, 0, 0})

	// nas[0] observes again.
	broadcast(c, ctx, nas[0], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 1, 1, 0, 0, 0, 0})

	// nas[3] observes, reaching consensus (4/6, being exactly the 2/3 threshold).
	// (Active nodes which observed are decremented ObserveSlashPoints;
	//  those which haven't are incremented LackOfObservationPenalty.)
	broadcast(c, ctx, nas[3], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{1, 0, 0, 0, 2, 2, 0})

	// nas[0] observes again.
	broadcast(c, ctx, nas[0], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 0, 0, 2, 2, 0})

	// Within the ObservationDelayFlexibility period, nas[4] observes
	// (and is decremented LackOfObservationPenalty).
	height += observeFlex
	ctx = ctx.WithBlockHeight(height)
	broadcast(c, ctx, nas[4], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 0, 0, 0, 2, 0})

	// The ObservationDelayFlexibility period ends, after which nas[5] observes;
	// it is not incremented ObserveSlashPoints (and it is added to the list of signers)
	// and is also not decremented LackOfObservationPenalty.
	height++
	ctx = ctx.WithBlockHeight(height)
	broadcast(c, ctx, nas[5], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 0, 0, 0, 2, 0})

	// nas[5] observes again, this time incremented ObserveSlashPoints for the extra signing.
	broadcast(c, ctx, nas[5], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 0, 0, 0, 3, 0})

	// Note that nas[6], the Standby node, remains unaffected by the Actives nodes' observations.
}

func (HandlerErrataTxSuite) TestWithdrawnDepositErrataCorrectsCustody(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	tx := f.stageMemo(c, fmt.Sprintf("+:%s", f.asset), 10)
	msg := NewMsgWithdrawLiquidity(GetRandomTx(), f.refund, cosmos.NewUint(MaxWithdrawBasisPoints), f.asset, common.EmptyAsset, f.nodes[0].NodeAddress)
	_, _, _, _, err := withdraw(f.ctx, *msg, f.mgr)
	c.Assert(err, IsNil)
	c.Assert(liquidityProviderStored(c, f.mgr.Keeper(), f.ctx, f.asset, f.refund), Equals, false)
	before, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	f.reverse(c, tx)
	after, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	c.Assert(after.BalanceAsset.Equal(before.BalanceAsset.Sub(tx.Coins[0].Amount)), Equals, true)
	c.Assert(after.LPUnits.Equal(before.LPUnits), Equals, true)
	c.Assert(liquidityProviderStored(c, f.mgr.Keeper(), f.ctx, f.asset, f.refund), Equals, false)
	vault, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.GetCoin(f.asset).Amount.Equal(f.vault.GetCoin(f.asset).Amount), Equals, true)
	f.reverse(c, tx)
	again, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	c.Assert(again, DeepEquals, after)
}

func (HandlerErrataTxSuite) TestRefundedAddErrataFundsRefund(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	lp := LiquidityProvider{Asset: f.asset, RuneAddress: f.rune, AssetAddress: GetRandomBTCAddress(), Units: cosmos.NewUint(100 * common.One), PendingRune: cosmos.ZeroUint(), PendingAsset: cosmos.ZeroUint(), RuneDepositValue: cosmos.ZeroUint(), AssetDepositValue: cosmos.ZeroUint()}
	f.mgr.Keeper().SetLiquidityProvider(f.ctx, lp)
	tx := f.observeMemo(c, fmt.Sprintf("+:%s:%s", f.asset, f.rune), 10)
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.PendingReceipt, IsNil)
	c.Assert(len(voter.Actions), Equals, 1)
	poolBefore, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	vaultBefore, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	// Before the reorg, custody backs both the pool (including its fee) and
	// the outstanding refund. Errata must preserve that accounting equality.
	c.Assert(poolBefore.BalanceAsset.Add(voter.Actions[0].Coin.Amount).Equal(vaultBefore.GetCoin(f.asset).Amount), Equals, true)
	f.ctx = f.ctx.WithEventManager(cosmos.NewEventManager())
	f.reverse(c, tx)
	poolAfter, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	expectedPool := poolBefore
	expectedPool.BalanceAsset = expectedPool.BalanceAsset.Sub(tx.Coins[0].Amount)
	c.Assert(poolAfter, DeepEquals, expectedPool)
	lpAfter, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(lpAfter, DeepEquals, lp)
	vaultAfter, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(vaultAfter.GetCoin(f.asset).Amount.Equal(vaultBefore.GetCoin(f.asset).Amount.Sub(tx.Coins[0].Amount)), Equals, true)
	after, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(after.Actions, DeepEquals, voter.Actions)
	c.Assert(poolAfter.BalanceAsset.Add(after.Actions[0].Coin.Amount).Equal(vaultAfter.GetCoin(f.asset).Amount), Equals, true)
	assertLiquidityFallbackSecurityEvent(c, f.ctx, tx)
	// A duplicate cannot fund the same refund a second time or repeat its event.
	f.ctx = f.ctx.WithEventManager(cosmos.NewEventManager())
	f.reverse(c, tx)
	again, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	c.Assert(again, DeepEquals, poolAfter)
	c.Assert(len(f.ctx.EventManager().Events()), Equals, 0)
}

// A missing asset makes these ADDs fail and schedule a refund. The memo parser
// ignores the payload suffix, so errata must classify each spelling identically.
func (HandlerErrataTxSuite) TestMalformedRefundedAddErrataFundsRefund(c *C) {
	for _, memo := range []string{"ADD", "ADD|payload", "+|payload"} {
		c.Logf("memo=%s", memo)
		f := newPendingLiquidityFixture(c, common.BTCAsset)
		tx := f.observeMemo(c, memo, 10)
		voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
		c.Assert(err, IsNil)
		c.Assert(voter.UpdatedVault, Equals, true)
		c.Assert(voter.PendingReceipt, IsNil)
		c.Assert(voter.Actions, HasLen, 1)
		poolBefore, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		vaultBefore, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(poolBefore.BalanceAsset.Add(voter.Actions[0].Coin.Amount).Equal(vaultBefore.GetCoin(f.asset).Amount), Equals, true)

		f.ctx = f.ctx.WithEventManager(cosmos.NewEventManager())
		f.reverse(c, tx)
		poolAfter, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		vaultAfter, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
		c.Assert(err, IsNil)
		after, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
		c.Assert(err, IsNil)
		// Custody loses the full inbound. The refund remains payable, so the
		// pool must absorb the same amount even though this ADD never executed.
		c.Assert(after.Actions, DeepEquals, voter.Actions)
		c.Assert(vaultAfter.GetCoin(f.asset).Amount.Equal(vaultBefore.GetCoin(f.asset).Amount.Sub(tx.Coins[0].Amount)), Equals, true)
		c.Check(poolAfter.BalanceAsset.Equal(poolBefore.BalanceAsset.Sub(tx.Coins[0].Amount)), Equals, true)
		c.Check(poolAfter.BalanceAsset.Add(after.Actions[0].Coin.Amount).Equal(vaultAfter.GetCoin(f.asset).Amount), Equals, true)
		assertLiquidityFallbackSecurityEvent(c, f.ctx, tx)
	}
}

func (HandlerErrataTxSuite) TestLiquidityErrataReportsPoolShortfall(c *C) {
	for _, remaining := range []int64{-1, 0, 3} {
		f := newPendingLiquidityFixture(c, common.BTCAsset)
		tx := f.stageMemo(c, fmt.Sprintf("+:%s", f.asset), 10)
		pool, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		if remaining < 0 {
			f.mgr.Keeper().RemovePool(f.ctx, f.asset)
		} else {
			pool.BalanceAsset = cosmos.NewUint(uint64(remaining) * common.One)
			c.Assert(f.mgr.Keeper().SetPool(f.ctx, pool), IsNil)
		}
		f.ctx = f.ctx.WithEventManager(cosmos.NewEventManager())
		f.reverse(c, tx)
		after, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		c.Assert(after.BalanceAsset.IsZero(), Equals, true)
		c.Assert(f.mgr.Keeper().PoolExist(f.ctx, f.asset), Equals, remaining >= 0)
		vault, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(vault.GetCoin(f.asset).Amount.Equal(f.vault.GetCoin(f.asset).Amount), Equals, true)
		c.Assert(vault.GetDeficit(f.asset).Amount.IsZero(), Equals, true)
		uncovered := cosmos.NewUint(10 * common.One)
		if remaining > 0 {
			uncovered = uncovered.Sub(cosmos.NewUint(uint64(remaining) * common.One))
		}
		found := false
		for _, event := range f.ctx.EventManager().Events() {
			if event.Type != "security" {
				continue
			}
			for _, attribute := range event.Attributes {
				if attribute.Key == "msg" && strings.Contains(attribute.Value, "liquidity errata pool shortfall") {
					c.Assert(strings.Contains(attribute.Value, "asset_uncovered="+uncovered.String()), Equals, true)
					found = true
				}
			}
		}
		c.Assert(found, Equals, true, Commentf("remaining=%d", remaining))
	}
}

func (HandlerErrataTxSuite) TestUntrackedPendingErrataUsesOnlyOriginalAmount(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	first := f.stage(c, f.rune, 10)
	second := f.stage(c, f.rune, 20)
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, first.ID)
	c.Assert(err, IsNil)
	voter.PendingReceipt = nil
	f.mgr.Keeper().SetObservedTxInVoter(f.ctx, voter)
	f.reverse(c, first)
	pool, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	vault, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(lp.PendingTxID, Equals, second.ID)
	c.Assert(lp.PendingAsset.Equal(cosmos.NewUint(30*common.One)), Equals, true)
	c.Assert(pool.PendingInboundAsset.Equal(lp.PendingAsset), Equals, true)
	c.Assert(pool.BalanceAsset.Equal(f.initial.BalanceAsset.Sub(first.Coins[0].Amount)), Equals, true)
	c.Assert(vault.GetCoin(f.asset).Amount.Equal(pool.BalanceAsset.Add(pool.PendingInboundAsset)), Equals, true)
	// A duplicate must not charge the fallback again. After export/import the
	// unreferenced inbound is missing, so errata fails without changing balances.
	f.reverse(c, first)
	state := ExportGenesis(f.ctx, f.mgr.Keeper())
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(f.ctx.BlockHeight())
	initGenesis(ctx, mgr.Keeper(), state)
	f.ctx, f.mgr = ctx, mgr
	c.Assert(mgr.ValidatorMgr().BeginBlock(ctx, mgr, nil), IsNil)
	c.Assert(f.tryReverse(c, first), ErrorMatches, "fail to process errata tx attestation: cannot find tx: "+first.ID.String())
	after, err := mgr.Keeper().GetPool(ctx, f.asset)
	c.Assert(err, IsNil)
	c.Assert(after, DeepEquals, pool)
	afterVault, err := mgr.Keeper().GetVault(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(afterVault, DeepEquals, vault)
	afterLP, err := mgr.Keeper().GetLiquidityProvider(ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(afterLP, DeepEquals, lp)
}

// Naming an affiliate must never put that affiliate's existing position at risk.
// The chosen policy also deliberately retains units from the orphaned ADD itself.
func (HandlerErrataTxSuite) TestAffiliateErrataKeepsAllCommittedUnits(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	affiliate := GetRandomTHORAddress()
	position := LiquidityProvider{Asset: f.asset, RuneAddress: affiliate, Units: cosmos.NewUint(400 * common.One), PendingRune: cosmos.ZeroUint(), PendingAsset: cosmos.ZeroUint(), RuneDepositValue: cosmos.ZeroUint(), AssetDepositValue: cosmos.ZeroUint()}
	f.mgr.Keeper().SetLiquidityProvider(f.ctx, position)
	tx := f.stageMemo(c, fmt.Sprintf("+:%s:%s:%s:10", f.asset, f.rune, affiliate), 1)
	before, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, affiliate)
	c.Assert(err, IsNil)
	c.Assert(before.Units.GT(position.Units), Equals, true)
	poolBefore, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.PendingReceipt, NotNil)
	affiliateAmount := tx.Coins[0].Amount.Sub(voter.PendingReceipt.AssetAmount)
	f.reverse(c, tx)
	after, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, affiliate)
	c.Assert(err, IsNil)
	c.Assert(after, DeepEquals, before)
	poolAfter, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	c.Assert(poolAfter.LPUnits.Equal(poolBefore.LPUnits), Equals, true)
	c.Assert(poolAfter.BalanceAsset.Equal(poolBefore.BalanceAsset.Sub(affiliateAmount)), Equals, true)
	c.Assert(poolAfter.PendingInboundAsset.IsZero(), Equals, true)
	owner, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(owner.PendingAsset.IsZero(), Equals, true)
}

// The restored penalty concerns the current sender-keyed position, not a
// contribution's remaining units. It can also take later legitimate deposits.
func (HandlerErrataTxSuite) TestAssetOnlyErrataPenalizesWithdrawalAndRedeposit(c *C) {
	for _, basisPoints := range []uint64{MaxWithdrawBasisPoints / 2, MaxWithdrawBasisPoints} {
		f := newPendingLiquidityFixture(c, common.BTCAsset)
		old := f.stageMemo(c, fmt.Sprintf("+:%s", f.asset), 10)
		msg := NewMsgWithdrawLiquidity(GetRandomTx(), f.refund, cosmos.NewUint(basisPoints), f.asset, common.EmptyAsset, f.nodes[0].NodeAddress)
		_, _, _, _, err := withdraw(f.ctx, *msg, f.mgr)
		c.Assert(err, IsNil)
		f.stageMemo(c, fmt.Sprintf("+:%s", f.asset), 20)
		before, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.refund)
		c.Assert(err, IsNil)
		poolBefore, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		f.reverse(c, old)
		c.Assert(liquidityProviderStored(c, f.mgr.Keeper(), f.ctx, f.asset, f.refund), Equals, false)
		poolAfter, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		c.Assert(poolAfter.LPUnits.Equal(poolBefore.LPUnits.Sub(before.Units)), Equals, true)
		c.Assert(poolAfter.BalanceAsset.Equal(poolBefore.BalanceAsset.Sub(old.Coins[0].Amount)), Equals, true)
	}
}

// A sender lookup may return the keeper's absent-record default. Never write or
// delete that default as though it were a real position.
type committedErrataLPAccessKeeper struct {
	keeper.Keeper
	reads    int
	writes   int
	deletes  int
	failRead bool
}

func (k *committedErrataLPAccessKeeper) GetLiquidityProvider(ctx cosmos.Context, asset common.Asset, address common.Address) (LiquidityProvider, error) {
	k.reads++
	if k.failRead {
		return LiquidityProvider{}, fmt.Errorf("unreadable LP")
	}
	return k.Keeper.GetLiquidityProvider(ctx, asset, address)
}

func (k *committedErrataLPAccessKeeper) SetLiquidityProvider(ctx cosmos.Context, lp LiquidityProvider) {
	k.writes++
	k.Keeper.SetLiquidityProvider(ctx, lp)
}

func (k *committedErrataLPAccessKeeper) RemoveLiquidityProvider(ctx cosmos.Context, lp LiquidityProvider) {
	k.deletes++
	k.Keeper.RemoveLiquidityProvider(ctx, lp)
}

func (HandlerErrataTxSuite) TestAssetOnlyErrataDoesNotCreateMissingLP(c *C) {
	for _, withdrawn := range []bool{false, true} {
		f := newPendingLiquidityFixture(c, common.BTCAsset)
		tx := f.stageMemo(c, fmt.Sprintf("+:%s", f.asset), 10)
		if withdrawn {
			msg := NewMsgWithdrawLiquidity(GetRandomTx(), f.refund, cosmos.NewUint(MaxWithdrawBasisPoints), f.asset, common.EmptyAsset, f.nodes[0].NodeAddress)
			_, _, _, _, err := withdraw(f.ctx, *msg, f.mgr)
			c.Assert(err, IsNil)
		}
		count := &committedErrataLPAccessKeeper{Keeper: f.mgr.Keeper()}
		f.mgr.K = count
		before, err := count.Keeper.GetObservedTxInVoter(f.ctx, tx.ID)
		c.Assert(err, IsNil)
		c.Assert(before.PendingReceipt, IsNil)
		c.Assert(before.Reverted, Equals, false)
		f.reverse(c, tx)
		after, err := count.Keeper.GetObservedTxInVoter(f.ctx, tx.ID)
		c.Assert(err, IsNil)
		c.Assert(after.PendingReceipt, IsNil)
		c.Assert(after.Reverted, Equals, true)
		c.Assert(count.reads, Equals, 1)
		c.Assert(count.writes, Equals, 0)
		expectedDeletes := 1
		if withdrawn {
			expectedDeletes = 0
		}
		c.Assert(count.deletes, Equals, expectedDeletes)
		c.Assert(liquidityProviderStored(c, count.Keeper, f.ctx, f.asset, f.refund), Equals, false)
	}
}

// The fallback security event identifies the actual orphaned transaction and
// distinguishes socialized loss from an exact pending reversal.
func assertLiquidityFallbackSecurityEvent(c *C, ctx cosmos.Context, tx common.Tx) {
	count := 0
	for _, event := range ctx.EventManager().Events() {
		if event.Type != "security" {
			continue
		}
		attributes := make(map[string]string)
		for _, attr := range event.Attributes {
			attributes[attr.Key] = attr.Value
		}
		if !strings.HasPrefix(attributes["msg"], "liquidity errata ownership fallback:") {
			continue
		}
		count++
		c.Assert(attributes["id"], Equals, tx.ID.String())
		c.Assert(attributes["coin"], Equals, tx.Coins.String())
		c.Assert(attributes["chain"], Equals, tx.Chain.String())
	}
	c.Assert(count, Equals, 1)
}

// Exact pending reversal reports through the withdraw event Midgard consumes.
func isPendingLiquidityWithdrawEvent(event cosmos.Event) bool {
	if event.Type != "pending_liquidity" {
		return false
	}
	for _, attr := range event.Attributes {
		if attr.Key == "type" {
			return attr.Value == "withdraw"
		}
	}
	return false
}

// Whole-position removal deliberately includes earlier honest units: reversing
// the 1 BTC ADD below forfeits the position funded by both 100 BTC and 1 BTC.
func (HandlerErrataTxSuite) TestAssetOnlyErrataPenalizesWholePosition(c *C) {
	// The memo parser drops everything after "|", so the last two still execute
	// as plain asset-only ADDs and must not escape the penalty.
	for _, memo := range []string{"+:BTC.BTC", "ADD:" + common.BTCAsset.ShortCode(), "+:BTC.BTC|payload", "+:BTC.BTC:|payload"} {
		f := newPendingLiquidityFixture(c, common.BTCAsset)
		f.mgr.Keeper().SetMimir(f.ctx, "LiquidityLockUpBlocks", 600)
		f.stageMemo(c, memo, 100)
		orphan := f.stageMemo(c, memo, 1)
		lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.refund)
		c.Assert(err, IsNil)
		before, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		msg := NewMsgWithdrawLiquidity(GetRandomTx(), f.refund, cosmos.NewUint(MaxWithdrawBasisPoints), f.asset, common.EmptyAsset, f.nodes[0].NodeAddress)
		_, _, _, _, err = withdraw(f.ctx, *msg, f.mgr)
		c.Assert(err, Equals, errWithdrawLockup)
		f.ctx = f.ctx.WithEventManager(cosmos.NewEventManager())
		f.reverse(c, orphan)
		c.Assert(liquidityProviderStored(c, f.mgr.Keeper(), f.ctx, f.asset, f.refund), Equals, false)
		after, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		c.Assert(after.LPUnits, DeepEquals, before.LPUnits.Sub(lp.Units))
		c.Assert(after.BalanceAsset, DeepEquals, before.BalanceAsset.Sub(orphan.Coins[0].Amount))
		c.Assert(after.BalanceRune, DeepEquals, before.BalanceRune)
		vault, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(vault.GetCoin(f.asset).Amount, DeepEquals, f.vault.GetCoin(f.asset).Amount.Add(cosmos.NewUint(100*common.One)))
		assertLiquidityFallbackSecurityEvent(c, f.ctx, orphan)
		penaltyReported := false
		for _, event := range f.ctx.EventManager().Events() {
			for _, attr := range event.Attributes {
				if event.Type == "security" && attr.Key == "msg" && strings.Contains(attr.Value, "whole-position penalty: units="+lp.Units.String()) {
					penaltyReported = true
				}
			}
		}
		c.Assert(penaltyReported, Equals, true)
		// A duplicate errata must not confiscate a subsequent position.
		f.stageMemo(c, memo, 5)
		fresh, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.refund)
		c.Assert(err, IsNil)
		f.reverse(c, orphan)
		again, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.refund)
		c.Assert(err, IsNil)
		c.Assert(again, DeepEquals, fresh)
	}
}

func (HandlerErrataTxSuite) TestPairedErrataPreservesSeparateSenderPosition(c *C) {
	for _, completed := range []bool{false, true} {
		for _, tracked := range []bool{false, true} {
			f := newPendingLiquidityFixture(c, common.BTCAsset)
			f.stageMemo(c, "+:BTC.BTC", 100)
			before, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.refund)
			c.Assert(err, IsNil)
			paired := f.stage(c, f.rune, 10)
			if completed {
				completePendingLiquidity(c, f)
			}
			if !tracked {
				// Pre-upgrade paired ADDs have no receipt either. The memo's pair
				// field must still protect the sender's separate asset-only LP.
				voter, voterErr := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, paired.ID)
				c.Assert(voterErr, IsNil)
				voter.PendingReceipt = nil
				f.mgr.Keeper().SetObservedTxInVoter(f.ctx, voter)
			}
			f.reverse(c, paired)
			after, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.refund)
			c.Assert(err, IsNil)
			c.Assert(after, DeepEquals, before)
		}
	}
}

func (HandlerErrataTxSuite) TestAssetOnlyPenaltyPreservesAffiliatePosition(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	affiliate := GetRandomTHORAddress()
	position := LiquidityProvider{Asset: f.asset, RuneAddress: affiliate, Units: cosmos.NewUint(400 * common.One), PendingRune: cosmos.ZeroUint(), PendingAsset: cosmos.ZeroUint(), RuneDepositValue: cosmos.ZeroUint(), AssetDepositValue: cosmos.ZeroUint()}
	f.mgr.Keeper().SetLiquidityProvider(f.ctx, position)
	tx := f.stageMemo(c, fmt.Sprintf("+:BTC.BTC::%s:10", affiliate), 100)
	before, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, affiliate)
	c.Assert(err, IsNil)
	c.Assert(before.Units.GT(position.Units), Equals, true)
	owner, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.refund)
	c.Assert(err, IsNil)
	poolBefore, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	f.reverse(c, tx)
	after, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, affiliate)
	c.Assert(err, IsNil)
	c.Assert(after, DeepEquals, before)
	c.Assert(liquidityProviderStored(c, f.mgr.Keeper(), f.ctx, f.asset, f.refund), Equals, false)
	poolAfter, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	c.Assert(poolAfter.LPUnits, DeepEquals, poolBefore.LPUnits.Sub(owner.Units))
}

func (HandlerErrataTxSuite) TestFailedAssetOnlyAddPreservesPosition(c *C) {
	for _, refundState := range []string{"scheduled", "paid", "too-small"} {
		f := newPendingLiquidityFixture(c, common.BTCAsset)
		f.stageMemo(c, "+:BTC.BTC", 100)
		before, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.refund)
		c.Assert(err, IsNil)
		f.mgr.Keeper().SetMimir(f.ctx, "HaltTrading", 1)
		if refundState == "too-small" {
			c.Assert(f.mgr.Keeper().SaveNetworkFee(f.ctx, common.BTCChain, NewNetworkFee(common.BTCChain, 100000, 100000)), IsNil)
		}
		tx := f.observeMemo(c, "+:BTC.BTC", 1)
		voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
		c.Assert(err, IsNil)
		if refundState == "too-small" {
			c.Assert(len(voter.Actions), Equals, 0)
			c.Assert(len(voter.OutTxs), Equals, 0)
			c.Assert(voter.IsDone(), Equals, true) // count-based helper is not execution proof
			c.Assert(voter.Tx.Status, Equals, common.Status_incomplete)
		} else {
			c.Assert(len(voter.Actions), Equals, 1)
			if refundState == "paid" {
				action := voter.Actions[0]
				out := common.NewTx(GetRandomTxHash(), tx.ToAddress, action.ToAddress, common.NewCoins(action.Coin), tx.Gas, action.GetMemo())
				c.Assert(voter.AddOutTx(out), Equals, true)
				c.Assert(voter.Tx.Status, Equals, common.Status_done)
				f.mgr.Keeper().SetObservedTxInVoter(f.ctx, voter)
			}
		}
		f.reverse(c, tx)
		after, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.refund)
		c.Assert(err, IsNil)
		c.Assert(after, DeepEquals, before)
	}
}

func (HandlerErrataTxSuite) TestUncreditedDoneAddPreservesPosition(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	tx := f.stageMemo(c, "+:BTC.BTC", 100)
	before, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.refund)
	c.Assert(err, IsNil)
	// Compromised-vault observations can be done without executing an ADD or
	// crediting custody. Reproduce that persisted guard state explicitly.
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Tx.Status, Equals, common.Status_done)
	voter.UpdatedVault = false
	f.mgr.Keeper().SetObservedTxInVoter(f.ctx, voter)
	poolBefore, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	vaultBefore, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	f.reverse(c, tx)
	after, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.refund)
	c.Assert(err, IsNil)
	c.Assert(after, DeepEquals, before)
	// Neither custody nor the pool received the coins, so neither is debited.
	poolAfter, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	c.Assert(poolAfter, DeepEquals, poolBefore)
	vaultAfter, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(vaultAfter.GetCoin(f.asset).Amount, DeepEquals, vaultBefore.GetCoin(f.asset).Amount)
}

func (HandlerErrataTxSuite) TestAssetOnlyPenaltyReadFailureStillCorrectsCustody(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	tx := f.stageMemo(c, "+:BTC.BTC", 100)
	before, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	f.mgr.K = &committedErrataLPAccessKeeper{Keeper: f.mgr.Keeper(), failRead: true}
	f.reverse(c, tx)
	after, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	c.Assert(after.LPUnits, DeepEquals, before.LPUnits)
	c.Assert(after.BalanceAsset, DeepEquals, f.initial.BalanceAsset)
	vault, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.GetCoin(f.asset).Amount, DeepEquals, f.vault.GetCoin(f.asset).Amount)
}

func (HandlerErrataTxSuite) TestXMRErrataPreservesCompletedPosition(c *C) {
	f := newPendingLiquidityFixture(c, common.XMRAsset)
	tx := f.stage(c, f.rune, 10)
	completePendingLiquidity(c, f)
	before, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(before.Units.IsZero(), Equals, false)
	poolBefore, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	f.reverse(c, tx)
	after, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(after, DeepEquals, before)
	poolAfter, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	c.Assert(poolAfter.LPUnits, DeepEquals, poolBefore.LPUnits)
	c.Assert(poolAfter.BalanceAsset, DeepEquals, poolBefore.BalanceAsset.Sub(tx.Coins[0].Amount))
}

func (HandlerErrataTxSuite) TestAssetOnlyErrataPenalizesTokenMemoVariants(c *C) {
	asset, err := common.NewAsset("ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	c.Assert(err, IsNil)
	for _, hint := range []string{asset.String(), "ETH.USDT", "ETH.USDT-", "ETH.USDT-1EC7"} {
		for _, changedRanking := range []bool{false, true} {
			f := newPendingLiquidityFixture(c, asset)
			gasPool := f.initial
			gasPool.Asset = common.ETHAsset
			c.Assert(f.mgr.Keeper().SetPool(f.ctx, gasPool), IsNil)
			tx := f.stageMemo(c, "+:"+hint, 100)
			lp, lpErr := f.mgr.Keeper().GetLiquidityProvider(f.ctx, asset, f.refund)
			c.Assert(lpErr, IsNil)
			c.Assert(lp.Units.IsZero(), Equals, false)
			poolBefore, poolErr := f.mgr.Keeper().GetPool(f.ctx, asset)
			c.Assert(poolErr, IsNil)
			if changedRanking {
				// Re-resolving a short memo now would select a different token.
				// The successful original ADD already proves the deposited asset.
				competitor := f.initial
				competitor.Asset, err = common.NewAsset("ETH.USDT-0XD084B83C305DAFD76AE3E1B4E1F1FE213D831EC7")
				c.Assert(err, IsNil)
				competitor.BalanceRune = f.initial.BalanceRune.MulUint64(2)
				c.Assert(f.mgr.Keeper().SetPool(f.ctx, competitor), IsNil)
				if hint != asset.String() {
					memoAsset, parseErr := common.NewAsset(hint)
					c.Assert(parseErr, IsNil)
					c.Assert(fuzzyAssetMatch(f.ctx, f.mgr.Keeper(), memoAsset).Equals(competitor.Asset), Equals, true)
				}
			}
			f.reverse(c, tx)
			c.Assert(liquidityProviderStored(c, f.mgr.Keeper(), f.ctx, asset, f.refund), Equals, false, Commentf("hint=%s changedRanking=%v", hint, changedRanking))
			poolAfter, poolErr := f.mgr.Keeper().GetPool(f.ctx, asset)
			c.Assert(poolErr, IsNil)
			c.Assert(poolAfter.LPUnits, DeepEquals, poolBefore.LPUnits.Sub(lp.Units))
			c.Assert(poolAfter.BalanceAsset, DeepEquals, poolBefore.BalanceAsset.Sub(tx.Coins[0].Amount))
			vault, vaultErr := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
			c.Assert(vaultErr, IsNil)
			c.Assert(vault.GetCoin(asset).Amount, DeepEquals, f.vault.GetCoin(asset).Amount)
		}
	}
}

func (HandlerErrataTxSuite) TestSaversErrataPreservesSeparateAssetOnlyPosition(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	f.stageMemo(c, "+:BTC.BTC", 100)
	before, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.refund)
	c.Assert(err, IsNil)
	tx := f.stageMemo(c, "+:BTC/BTC", 10)
	c.Assert(f.mgr.SwapQ().EndBlock(f.ctx, f.mgr), IsNil)
	c.Assert(f.mgr.AdvSwapQueueMgr().EndBlock(f.ctx, f.mgr, false), IsNil)
	saver, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset.GetSyntheticAsset(), f.refund)
	c.Assert(err, IsNil)
	c.Assert(saver.Units.IsZero(), Equals, false)
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Tx.Status, Equals, common.Status_done)
	c.Assert(voter.UpdatedVault, Equals, true)
	c.Assert(voter.PendingReceipt, IsNil)
	c.Assert(len(voter.Actions), Equals, 0)
	c.Assert(len(voter.OutTxs), Equals, 0)
	poolBefore, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	f.reverse(c, tx)
	after, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.refund)
	c.Assert(err, IsNil)
	c.Assert(after, DeepEquals, before)
	poolAfter, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	c.Assert(poolAfter.LPUnits, DeepEquals, poolBefore.LPUnits)
}
