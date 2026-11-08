package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type HandlerRagnarokSuite struct{}

var _ = Suite(&HandlerRagnarokSuite{})

type TestRagnarokKeeper struct {
	keeper.KVStoreDummy
	activeNodeAccount NodeAccount
	vault             Vault
}

func (k *TestRagnarokKeeper) GetNodeAccount(_ cosmos.Context, addr cosmos.AccAddress) (NodeAccount, error) {
	if k.activeNodeAccount.NodeAddress.Equals(addr) {
		return k.activeNodeAccount, nil
	}
	return NodeAccount{}, nil
}

func (HandlerRagnarokSuite) TestRagnarok(c *C) {
	ctx, _ := setupKeeperForTest(c)

	keeper := &TestRagnarokKeeper{
		activeNodeAccount: GetRandomValidatorNode(NodeActive),
		vault:             GetRandomVault(),
	}

	handler := NewRagnarokHandler(NewDummyMgrWithKeeper(keeper))

	// invalid message should result errors
	msg := NewMsgNetworkFee(ctx.BlockHeight(), common.ETHChain, 1, 10000, GetRandomBech32Addr())
	result, err := handler.Run(ctx, msg)
	c.Check(result, IsNil, Commentf("invalid message should result an error"))
	c.Check(err, NotNil, Commentf("invalid message should result an error"))
	addr, err := keeper.vault.PubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	tx := NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.ETHChain,
		Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))},
		Memo:        "",
		FromAddress: GetRandomETHAddress(),
		ToAddress:   addr,
		Gas: common.Gas{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(10000)),
		},
	}, 12, GetRandomPubKey(), 12)

	msgRagnarok := NewMsgRagnarok(tx, 1, keeper.activeNodeAccount.NodeAddress)
	err = handler.validate(ctx, *msgRagnarok)
	c.Assert(err, IsNil)

	// invalid msg
	msgRagnarok = &MsgRagnarok{}
	err = handler.validate(ctx, *msgRagnarok)
	c.Assert(err, NotNil)
	result, err = handler.Run(ctx, msgRagnarok)
	c.Check(err, NotNil, Commentf("invalid message should fail validation"))
	c.Check(result, IsNil, Commentf("invalid message should fail validation"))
}

type TestRagnarokKeeperHappyPath struct {
	keeper.Keeper
	activeNodeAccount NodeAccount
	newVault          Vault
	retireVault       Vault
	txout             *TxOut
	pool              Pool
}

func (k *TestRagnarokKeeperHappyPath) GetTxOut(ctx cosmos.Context, blockHeight int64) (*TxOut, error) {
	if k.txout != nil && k.txout.Height == blockHeight {
		return k.txout, nil
	}
	return nil, errKaboom
}

func (k *TestRagnarokKeeperHappyPath) SetTxOut(ctx cosmos.Context, blockOut *TxOut) error {
	if k.txout.Height == blockOut.Height {
		k.txout = blockOut
		return nil
	}
	return errKaboom
}

func (k *TestRagnarokKeeperHappyPath) GetVault(_ cosmos.Context, pk common.PubKey) (Vault, error) {
	if pk.Equals(k.retireVault.PubKey) {
		return k.retireVault, nil
	}
	if pk.Equals(k.newVault.PubKey) {
		return k.newVault, nil
	}
	return Vault{}, fmt.Errorf("vault not found")
}

func (k *TestRagnarokKeeperHappyPath) GetNodeAccountByPubKey(_ cosmos.Context, _ common.PubKey) (NodeAccount, error) {
	return k.activeNodeAccount, nil
}

func (k *TestRagnarokKeeperHappyPath) SetNodeAccount(_ cosmos.Context, na NodeAccount) error {
	k.activeNodeAccount = na
	return nil
}

func (k *TestRagnarokKeeperHappyPath) GetPool(_ cosmos.Context, _ common.Asset) (Pool, error) {
	return k.pool, nil
}

func (k *TestRagnarokKeeperHappyPath) SetPool(_ cosmos.Context, p Pool) error {
	k.pool = p
	return nil
}

func (HandlerRagnarokSuite) TestRagnarokHappyPath(c *C) {
	ctx, k := setupKeeperForTest(c)
	retireVault := GetRandomVault()
	vaultCoins := common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(2*common.One)),
	}
	retireVault.AddFunds(vaultCoins)
	newVault := GetRandomVault()
	txout := NewTxOut(1)
	newVaultAddr, err := newVault.PubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	txout.TxArray = append(txout.TxArray, TxOutItem{
		Chain:       common.ETHChain,
		InHash:      common.BlankTxID,
		ToAddress:   newVaultAddr,
		VaultPubKey: retireVault.PubKey,
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(1024)),
		Memo:        NewRagnarokMemo(1).String(),
	})
	keeper := &TestRagnarokKeeperHappyPath{
		Keeper:            k,
		activeNodeAccount: GetRandomValidatorNode(NodeActive),
		newVault:          newVault,
		retireVault:       retireVault,
		txout:             txout,
	}
	addr, err := keeper.retireVault.PubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	handler := NewRagnarokHandler(NewDummyMgrWithKeeper(keeper))
	tx := NewObservedTx(common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.ETHChain,
		Coins: common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(1024)),
		},
		Memo:        NewRagnarokMemo(1).String(),
		FromAddress: addr,
		ToAddress:   newVaultAddr,
		Gas: common.Gas{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(10000)),
		},
	}, 1, retireVault.PubKey, 1)

	msgRagnarok := NewMsgRagnarok(tx, 1, keeper.activeNodeAccount.NodeAddress)
	_, err = handler.handle(ctx, *msgRagnarok)
	c.Assert(err, IsNil)
	c.Assert(keeper.txout.TxArray[0].OutHash.Equals(tx.Tx.ID), Equals, true)

	// fail to get tx out
	msgRagnarok1 := NewMsgRagnarok(tx, 1024, keeper.activeNodeAccount.NodeAddress)
	result, err := handler.handle(ctx, *msgRagnarok1)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (HandlerRagnarokSuite) TestRagnarokHappyPathXMR(c *C) {
	ctx, k := setupKeeperForTest(c)

	retireVault := GetRandomVault()
	retireVault.PubKeyFrost = GetRandomEd25519PubKey()
	retireVault.Chains = common.Chains{common.XMRChain}.Strings()
	retireVault.AddFunds(common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(2*common.One)),
	})

	newVault := GetRandomVault()
	newVault.PubKeyFrost = GetRandomEd25519PubKey()
	newVault.Chains = common.Chains{common.XMRChain}.Strings()

	txout := NewTxOut(1)
	newVaultAddr, err := newVault.PubKeyFrost.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	txout.TxArray = append(txout.TxArray, TxOutItem{
		Chain:            common.XMRChain,
		InHash:           common.BlankTxID,
		ToAddress:        newVaultAddr,
		VaultPubKey:      retireVault.PubKey,
		VaultPubKeyFrost: retireVault.PubKeyFrost,
		Coin:             common.NewCoin(common.XMRAsset, cosmos.NewUint(1024)),
		Memo:             NewRagnarokMemo(1).String(),
	})

	keeper := &TestRagnarokKeeperHappyPath{
		Keeper:            k,
		activeNodeAccount: GetRandomValidatorNode(NodeActive),
		newVault:          newVault,
		retireVault:       retireVault,
		txout:             txout,
	}

	fromAddr, err := keeper.retireVault.PubKeyFrost.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	handler := NewRagnarokHandler(NewDummyMgrWithKeeper(keeper))
	tx := NewObservedTx(common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.XMRChain,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1024)),
		},
		Memo:        NewRagnarokMemo(1).String(),
		FromAddress: fromAddr,
		ToAddress:   newVaultAddr,
		Gas: common.Gas{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(10)),
		},
	}, 1, retireVault.PubKeyFrost, 1)

	msgRagnarok := NewMsgRagnarok(tx, 1, keeper.activeNodeAccount.NodeAddress)
	_, err = handler.handle(ctx, *msgRagnarok)
	c.Assert(err, IsNil)
	c.Assert(keeper.txout.TxArray[0].OutHash.Equals(tx.Tx.ID), Equals, true)
}

func (HandlerRagnarokSuite) TestSlash(c *C) {
	ctx, k := setupKeeperForTest(c)
	retireVault := GetRandomVault()
	vaultCoins := common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(2*common.One)),
	}
	retireVault.AddFunds(vaultCoins)
	newVault := GetRandomVault()
	txout := NewTxOut(1)
	newVaultAddr, err := newVault.PubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(100 * common.One)
	retireVault.Membership = []string{
		na.PubKeySet.Secp256k1.String(),
	}
	keeper := &TestRagnarokKeeperHappyPath{
		Keeper:            k,
		activeNodeAccount: na,
		newVault:          newVault,
		retireVault:       retireVault,
		txout:             txout,
		pool:              pool,
	}
	addr, err := keeper.retireVault.PubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	mgr := NewDummyMgrWithKeeper(keeper)
	mgr.slasher = newSlasher(keeper, NewDummyEventMgr())
	handler := NewRagnarokHandler(mgr)

	tx := NewObservedTx(common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.ETHChain,
		Coins: common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(1024)),
		},
		Memo:        NewRagnarokMemo(1).String(),
		FromAddress: addr,
		ToAddress:   newVaultAddr,
		Gas: common.Gas{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(10000)),
		},
	}, 1, retireVault.PubKey, 1)

	msgRagnarok := NewMsgRagnarok(tx, 1, keeper.activeNodeAccount.NodeAddress)
	_, err = handler.handle(ctx, *msgRagnarok)
	c.Assert(err, IsNil)
	c.Assert(keeper.activeNodeAccount.Bond, DeepEquals, cosmos.NewUint(9999983464))
}

func (HandlerRagnarokSuite) TestRagnarokZecTexDestination(c *C) {
	p2pkhAddress, err := GetRandomPubKey().GetAddress(common.ZECChain)
	c.Assert(err, IsNil)
	texAddress, err := p2pkhAddress.ToTexAddress()
	c.Assert(err, IsNil)
	c.Assert(p2pkhAddress.Equals(texAddress), Equals, false)

	// setup schedules a ragnarok outbound to texAddress and observes it as paid
	// to observedTo, returning everything the assertions below read.
	setup := func(observedTo common.Address) (cosmos.Context, *TestRagnarokKeeperHappyPath, RagnarokHandler, MsgRagnarok) {
		ctx, k := setupKeeperForTest(c)

		na := GetRandomValidatorNode(NodeActive)
		na.Bond = cosmos.NewUint(100 * common.One)

		retireVault := GetRandomVault()
		retireVault.Chains = common.Chains{common.ZECChain}.Strings()
		retireVault.Membership = []string{na.PubKeySet.Secp256k1.String()}
		retireVault.AddFunds(common.Coins{
			common.NewCoin(common.ZECAsset, cosmos.NewUint(2*common.One)),
		})

		pool := NewPool()
		pool.Asset = common.ZECAsset
		pool.BalanceAsset = cosmos.NewUint(100 * common.One)
		pool.BalanceRune = cosmos.NewUint(100 * common.One)

		coin := common.NewCoin(common.ZECAsset, cosmos.NewUint(1024))
		txout := NewTxOut(1)
		txout.TxArray = append(txout.TxArray, TxOutItem{
			Chain:       common.ZECChain,
			InHash:      common.BlankTxID,
			ToAddress:   texAddress,
			VaultPubKey: retireVault.PubKey,
			Coin:        coin,
			MaxGas:      common.Gas{common.NewCoin(common.ZECAsset, cosmos.NewUint(10000))},
			Memo:        NewRagnarokMemo(1).String(),
		})

		keeper := &TestRagnarokKeeperHappyPath{
			Keeper:            k,
			activeNodeAccount: na,
			newVault:          GetRandomVault(),
			retireVault:       retireVault,
			txout:             txout,
			pool:              pool,
		}
		keeper.SetRagnarokPending(ctx, 2)

		mgr := NewDummyMgrWithKeeper(keeper)
		mgr.slasher = newSlasher(keeper, NewDummyEventMgr())

		fromAddress, fromErr := retireVault.PubKey.GetAddress(common.ZECChain)
		c.Assert(fromErr, IsNil)
		tx := NewObservedTx(common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ZECChain,
			Coins:       common.Coins{coin},
			Memo:        NewRagnarokMemo(1).String(),
			FromAddress: fromAddress,
			ToAddress:   observedTo,
			Gas:         common.Gas{common.NewCoin(common.ZECAsset, cosmos.NewUint(10000))},
		}, 1, retireVault.PubKey, 1)

		return ctx, keeper, NewRagnarokHandler(mgr), *NewMsgRagnarok(tx, 1, na.NodeAddress)
	}

	// The scheduled tex destination, observed in its P2PKH form.
	ctx, keeper, handler, msg := setup(p2pkhAddress)
	_, err = handler.handle(ctx, msg)
	c.Assert(err, IsNil)
	c.Check(keeper.txout.TxArray[0].OutHash.Equals(msg.Tx.Tx.ID), Equals, true,
		Commentf("tex outbound observed as P2PKH should be marked complete"))
	pending, err := keeper.GetRagnarokPending(ctx)
	c.Assert(err, IsNil)
	c.Check(pending, Equals, int64(1), Commentf("pending ragnarok count should be decremented"))
	c.Check(keeper.activeNodeAccount.Bond.String(), Equals, cosmos.NewUint(100*common.One).String(),
		Commentf("vault must not be slashed for an outbound the network ordered"))

	// An unrelated destination must still fail to match.
	strangerAddress, err := GetRandomPubKey().GetAddress(common.ZECChain)
	c.Assert(err, IsNil)
	ctx, keeper, handler, msg = setup(strangerAddress)
	_, err = handler.handle(ctx, msg)
	c.Assert(err, IsNil)
	c.Check(keeper.txout.TxArray[0].OutHash.IsEmpty(), Equals, true)
	pending, err = keeper.GetRagnarokPending(ctx)
	c.Assert(err, IsNil)
	c.Check(pending, Equals, int64(2))
	c.Check(keeper.activeNodeAccount.Bond.LT(cosmos.NewUint(100*common.One)), Equals, true,
		Commentf("an unmatched ragnarok outbound should still slash"))
}

// ragnarokRetryKeeper stores TxOuts in the real keeper, so the handler can walk
// several heights.
type ragnarokRetryKeeper struct {
	*TestRagnarokKeeperHappyPath
}

func (k ragnarokRetryKeeper) GetTxOut(ctx cosmos.Context, height int64) (*TxOut, error) {
	return k.Keeper.GetTxOut(ctx, height)
}

func (k ragnarokRetryKeeper) SetTxOut(ctx cosmos.Context, txOut *TxOut) error {
	return k.Keeper.SetTxOut(ctx, txOut)
}

// After a block time change, LackSigning reschedules an XMR ragnarok outbound
// one extended (3x) signing period later in wall-clock time. At a block time
// that does not divide the period, three rounded standard steps overshoot that
// height, so the handler must step by the extended period to find the retry.
func (HandlerRagnarokSuite) TestRagnarokFindsExtendedRetryAfterBlockTimeChange(c *C) {
	ctx, k := setupKeeperForTest(c)
	refMs := constants.ThorchainBlockTime.Milliseconds()
	history := constants.BlockTimeHistory{{Height: 1, Ms: refMs * 7 / 10}}
	k.SetBlockTimeHistory(ctx, history[0].Ms, history)

	periodMs := constants.NewConstantValue().GetInt64Value(constants.SigningTransactionPeriod) * refMs
	retryHeight := history.HeightAfter(1, periodMs*xmrSigningPeriodMultiplier, refMs)
	standard := int64(1)
	for range xmrSigningPeriodMultiplier {
		standard = history.HeightAfter(standard, periodMs, refMs)
	}
	c.Assert(standard, Not(Equals), retryHeight, Commentf("the block time must not divide the period"))
	ctx = ctx.WithBlockHeight(retryHeight + 10)

	retireVault := GetRandomVault()
	retireVault.PubKeyFrost = GetRandomEd25519PubKey()
	retireVault.Chains = common.Chains{common.XMRChain}.Strings()
	newVault := GetRandomVault()
	newVault.PubKeyFrost = GetRandomEd25519PubKey()
	newVaultAddr, err := newVault.PubKeyFrost.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	fromAddr, err := retireVault.PubKeyFrost.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	item := TxOutItem{
		Chain:            common.XMRChain,
		InHash:           common.BlankTxID,
		ToAddress:        newVaultAddr,
		VaultPubKey:      retireVault.PubKey,
		VaultPubKeyFrost: retireVault.PubKeyFrost,
		Coin:             common.NewCoin(common.XMRAsset, cosmos.NewUint(1024)),
		Memo:             NewRagnarokMemo(1).String(),
	}
	original := NewTxOut(1)
	rescheduled := item
	rescheduled.OutHash = common.BlankTxID // marked by LackSigning when it rescheduled the item
	original.TxArray = append(original.TxArray, rescheduled)
	c.Assert(k.SetTxOut(ctx, original), IsNil)
	retry := NewTxOut(retryHeight)
	retry.TxArray = append(retry.TxArray, item)
	c.Assert(k.SetTxOut(ctx, retry), IsNil)
	k.SetRagnarokPending(ctx, 1)

	keeper := ragnarokRetryKeeper{&TestRagnarokKeeperHappyPath{
		Keeper:            k,
		activeNodeAccount: GetRandomValidatorNode(NodeActive),
		newVault:          newVault,
		retireVault:       retireVault,
	}}
	handler := NewRagnarokHandler(NewDummyMgrWithKeeper(keeper))
	tx := NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.XMRChain,
		Coins:       common.Coins{common.NewCoin(common.XMRAsset, cosmos.NewUint(1024))},
		Memo:        NewRagnarokMemo(1).String(),
		FromAddress: fromAddr,
		ToAddress:   newVaultAddr,
		Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(10))},
	}, ctx.BlockHeight(), retireVault.PubKeyFrost, ctx.BlockHeight())

	_, err = handler.handle(ctx, *NewMsgRagnarok(tx, 1, keeper.activeNodeAccount.NodeAddress))
	c.Assert(err, IsNil)
	got, err := k.GetTxOut(ctx, retryHeight)
	c.Assert(err, IsNil)
	c.Check(got.TxArray[0].OutHash.Equals(tx.Tx.ID), Equals, true)
	pending, err := k.GetRagnarokPending(ctx)
	c.Assert(err, IsNil)
	c.Check(pending, Equals, int64(0))
}

// ragnarokXMRGasManager quotes max gas in XMR, the outbound's own gas asset.
type ragnarokXMRGasManager struct {
	*DummyGasManager
}

func (m *ragnarokXMRGasManager) GetMaxGas(cosmos.Context, common.Chain) (common.Coin, error) {
	return common.NewCoin(common.XMRAsset, cosmos.NewUint(1_000)), nil
}

func (m *ragnarokXMRGasManager) GetGasRate(cosmos.Context, common.Chain) cosmos.Uint {
	return cosmos.NewUint(1)
}

// LackSigning reads SigningTransactionPeriod with its mimir. With the mimir at
// 100 (the constant is 300), no block time change and coalescing off, the real
// LackSigning retries an XMR ragnarok outbound queued at 10000 one extended
// period later, at 10300 (100 * 3). The handler must step by the same period
// to find that retry, not by the constant's 900, whether the payment lands
// right after the retry or a period later. A mimir of zero must not stall the
// walk: it steps by the constant's period, and still slashes a payment that
// matches no copy.
func (HandlerRagnarokSuite) TestRagnarokFindsXMRRetryWithOverriddenSigningPeriod(c *C) {
	const scheduled, retryHeight = int64(10000), int64(10300)
	for _, tc := range []struct {
		name      string
		mimir     int64 // SigningTransactionPeriod when observed
		observeAt int64
		stranger  bool // paid to another destination
	}{
		{name: "period overridden by mimir", mimir: 100, observeAt: 10301},
		{name: "period overridden, observed a period later", mimir: 100, observeAt: 10650},
		{name: "mimir at zero, payment to another destination", mimir: 0, observeAt: 10301, stranger: true},
	} {
		comment := Commentf(tc.name)
		ctx, k := setupKeeperForTest(c)
		mgr := NewDummyMgrWithKeeper(k)
		gas := &ragnarokXMRGasManager{DummyGasManager: NewDummyGasManager()}
		mgr.gasMgr = gas
		mgr.txOutStore = newTxOutStorage(k, mgr.GetConstants(), mgr.EventMgr(), gas)
		recorder := &xmrMemoCompatibilitySlasher{DummySlasher: NewDummySlasher()}
		mgr.slasher = recorder
		c.Assert(mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod), Equals, int64(300))

		ctx = ctx.WithBlockHeight(scheduled)
		k.SetMimir(ctx, constants.SigningTransactionPeriod.String(), 100)
		k.SetMimir(ctx, constants.RescheduleCoalesceBlocks.String(), 0)
		k.SetMimir(ctx, constants.EnableMemolessOutbound.String(), 0)
		k.SetMimir(ctx, constants.MaxOutboundAttempts.String(), 0)
		pool := NewPool()
		pool.Asset = common.XMRAsset
		pool.BalanceRune = cosmos.NewUint(100 * common.One)
		pool.BalanceAsset = cosmos.NewUint(100 * common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)
		// the only XMR vault, so the retry keeps it
		vault := GetRandomVault()
		vault.Chains = common.Chains{common.THORChain, common.XMRChain}.Strings()
		vault.PubKeyFrost = GetRandomEd25519PubKey()
		vault.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100*common.One))))
		c.Assert(k.SetVault(ctx, vault), IsNil)
		from, err := vault.PubKeyFrost.GetAddress(common.XMRChain)
		c.Assert(err, IsNil)
		to, err := common.NewAddress(common.XMRChecksummedDummyAddress('4'))
		c.Assert(err, IsNil)

		item := TxOutItem{
			Chain:            common.XMRChain,
			InHash:           common.BlankTxID,
			ToAddress:        to,
			VaultPubKey:      vault.PubKey,
			VaultPubKeyFrost: vault.PubKeyFrost,
			Coin:             common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)),
			MaxGas:           common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1_000))},
			GasRate:          1,
			Memo:             NewRagnarokMemo(scheduled).String(),
		}
		queued := NewTxOut(scheduled)
		queued.TxArray = []TxOutItem{item}
		c.Assert(k.SetTxOut(ctx, queued), IsNil)
		k.SetRagnarokPending(ctx, 1)

		// the real LackSigning retries it one configured extended period later
		ctx = ctx.WithBlockHeight(retryHeight)
		c.Assert(newSlasher(k, mgr.EventMgr()).LackSigning(ctx, mgr), IsNil, comment)
		original, err := k.GetTxOut(ctx, scheduled)
		c.Assert(err, IsNil)
		c.Assert(original.TxArray, HasLen, 1, comment)
		c.Assert(original.TxArray[0].OutHash, Equals, common.BlankTxID, comment)
		retry, err := k.GetTxOut(ctx, retryHeight)
		c.Assert(err, IsNil)
		c.Assert(retry.TxArray, HasLen, 1, comment)
		c.Assert(retry.TxArray[0].OutHash.IsEmpty(), Equals, true, comment)

		k.SetMimir(ctx, constants.SigningTransactionPeriod.String(), tc.mimir)
		payTo := item.ToAddress
		if tc.stranger {
			payTo, err = common.NewAddress(common.XMRChecksummedDummyAddress('8'))
			c.Assert(err, IsNil)
		}
		ctx = ctx.WithBlockHeight(tc.observeAt)
		tx := NewObservedTx(
			common.NewTx(GetRandomTxHash(), from, payTo, common.NewCoins(item.Coin), item.MaxGas, item.Memo),
			tc.observeAt, vault.PubKeyFrost, tc.observeAt,
		)
		_, err = NewRagnarokHandler(mgr).handle(ctx, *NewMsgRagnarok(tx, scheduled, GetRandomBech32Addr()))
		c.Assert(err, IsNil, comment)

		retry, err = k.GetTxOut(ctx, retryHeight)
		c.Assert(err, IsNil)
		c.Assert(retry.TxArray, HasLen, 1, comment)
		pending, err := k.GetRagnarokPending(ctx)
		c.Assert(err, IsNil)
		if tc.stranger {
			c.Check(retry.TxArray[0].OutHash.IsEmpty(), Equals, true, comment)
			c.Check(recorder.calls, Equals, 1, comment)
			c.Check(pending, Equals, int64(1), comment)
			continue
		}
		c.Check(retry.TxArray[0].OutHash.Equals(tx.Tx.ID), Equals, true, comment)
		c.Check(recorder.calls, Equals, 0, comment)
		c.Check(pending, Equals, int64(0), comment)
	}
}
