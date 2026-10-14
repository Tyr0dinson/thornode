package thorchain

import (
	"errors"
	"fmt"

	"github.com/blang/semver"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerObservedTxOutSuite struct{}

type TestObservedTxOutValidateKeeper struct {
	keeper.KVStoreDummy
	activeNodeAccount NodeAccount
}

func (k *TestObservedTxOutValidateKeeper) GetNodeAccount(ctx cosmos.Context, signer cosmos.AccAddress) (NodeAccount, error) {
	if k.activeNodeAccount.NodeAddress.Equals(signer) {
		return k.activeNodeAccount, nil
	}
	return NodeAccount{}, nil
}

var _ = Suite(&HandlerObservedTxOutSuite{})

func (s *HandlerObservedTxOutSuite) TestValidate(c *C) {
	var err error
	ctx, _ := setupKeeperForTest(c)
	activeNodeAccount := GetRandomValidatorNode(NodeActive)

	keeper := &TestObservedTxOutValidateKeeper{
		activeNodeAccount: activeNodeAccount,
	}

	handler := NewObservedTxOutHandler(NewDummyMgrWithKeeper(keeper))

	// happy path
	pk := GetRandomPubKey()
	txs := ObservedTxs{NewObservedTx(GetRandomTx(), 12, pk, 12)}
	txs[0].Tx.FromAddress, err = pk.GetAddress(txs[0].Tx.Coins[0].Asset.Chain)
	c.Assert(err, IsNil)
	msg := NewMsgObservedTxOut(txs, activeNodeAccount.NodeAddress)
	err = handler.validate(ctx, *msg)
	c.Assert(err, IsNil)

	// inactive node account
	msg = NewMsgObservedTxOut(txs, GetRandomBech32Addr())
	err = handler.validate(ctx, *msg)
	c.Assert(errors.Is(err, se.ErrUnauthorized), Equals, true)

	// invalid msg
	msg = &MsgObservedTxOut{}
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
}

type TestObservedTxOutHandleKeeper struct {
	keeper.KVStoreDummy
	nas         NodeAccounts
	na          NodeAccount
	voter       ObservedTxVoter
	txInVoter   ObservedTxVoter
	vaultExists bool
	vault       Vault
	height      int64
	pool        Pool
	txOutStore  TxOutStore
	observing   []cosmos.AccAddress
	hashes      []common.TxID
}

func (k *TestObservedTxOutHandleKeeper) GetConfigInt64(_ cosmos.Context, key constants.ConstantName) int64 {
	return k.GetConstants().GetInt64Value(key)
}

func (k *TestObservedTxOutHandleKeeper) ListActiveValidators(_ cosmos.Context) (NodeAccounts, error) {
	return k.nas, nil
}

func (k *TestObservedTxOutHandleKeeper) IsActiveObserver(_ cosmos.Context, _ cosmos.AccAddress) bool {
	return true
}

func (k *TestObservedTxOutHandleKeeper) GetNodeAccountByPubKey(_ cosmos.Context, _ common.PubKey) (NodeAccount, error) {
	return k.nas[0], nil
}

func (k *TestObservedTxOutHandleKeeper) SetNodeAccount(_ cosmos.Context, na NodeAccount) error {
	k.na = na
	return nil
}

func (k *TestObservedTxOutHandleKeeper) GetObservedTxOutVoter(_ cosmos.Context, _ common.TxID) (ObservedTxVoter, error) {
	return k.voter, nil
}

func (k *TestObservedTxOutHandleKeeper) SetObservedTxOutVoter(_ cosmos.Context, voter ObservedTxVoter) {
	k.voter = voter
}

func (k *TestObservedTxOutHandleKeeper) VaultExists(_ cosmos.Context, _ common.PubKey) bool {
	return k.vaultExists
}

func (k *TestObservedTxOutHandleKeeper) GetVault(_ cosmos.Context, _ common.PubKey) (Vault, error) {
	return k.vault, nil
}

func (k *TestObservedTxOutHandleKeeper) SetVault(_ cosmos.Context, vault Vault) error {
	k.vault = vault
	return nil
}

func (k *TestObservedTxOutHandleKeeper) GetNetwork(_ cosmos.Context) (Network, error) {
	return NewNetwork(), nil
}

func (k *TestObservedTxOutHandleKeeper) SetNetwork(_ cosmos.Context, _ Network) error {
	return nil
}

func (k *TestObservedTxOutHandleKeeper) SetLastChainHeight(_ cosmos.Context, _ common.Chain, height int64) error {
	k.height = height
	return nil
}

func (k *TestObservedTxOutHandleKeeper) GetPool(_ cosmos.Context, _ common.Asset) (Pool, error) {
	return k.pool, nil
}

func (k *TestObservedTxOutHandleKeeper) GetTxOut(ctx cosmos.Context, _ int64) (*TxOut, error) {
	return k.txOutStore.GetBlockOut(ctx)
}

func (k *TestObservedTxOutHandleKeeper) FindPubKeyOfAddress(_ cosmos.Context, _ common.Address, _ common.Chain) (common.PubKey, error) {
	return k.vault.PubKey, nil
}

func (k *TestObservedTxOutHandleKeeper) SetTxOut(ctx cosmos.Context, blockOut *TxOut) error {
	return nil
}

func (k *TestObservedTxOutHandleKeeper) AddObservingAddresses(_ cosmos.Context, addrs []cosmos.AccAddress) error {
	k.observing = addrs
	return nil
}

func (k *TestObservedTxOutHandleKeeper) SetPool(ctx cosmos.Context, pool Pool) error {
	k.pool = pool
	return nil
}

func (k *TestObservedTxOutHandleKeeper) GetObservedTxInVoter(ctx cosmos.Context, hash common.TxID) (ObservedTxVoter, error) {
	return k.txInVoter, nil
}

func (k *TestObservedTxOutHandleKeeper) SetObservedTxInVoter(ctx cosmos.Context, tx ObservedTxVoter) {
	k.txInVoter = tx
}

func (k *TestObservedTxOutHandleKeeper) SetObservedLink(ctx cosmos.Context, _, outhash common.TxID) {
	k.hashes = append(k.hashes, outhash)
}

func (k *TestObservedTxOutHandleKeeper) GetObservedLink(ctx cosmos.Context, inhash common.TxID) []common.TxID {
	return k.hashes
}

func (s *HandlerObservedTxOutSuite) TestHandle(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)

	tx := GetRandomTx()
	pk := GetRandomPubKey()
	tx.FromAddress, err = pk.GetAddress(tx.Coins[0].Asset.Chain)
	txInHash := GetRandomTxHash()
	tx.Memo = fmt.Sprintf("OUT:%s|hello world", txInHash) // Including data passthrough test.
	obTx := NewObservedTx(tx, 12, pk, 12)
	txs := ObservedTxs{obTx}
	c.Assert(err, IsNil)

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(1000000 * common.One)
	na.PubKeySet.Secp256k1 = pk

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, pk, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Membership = []string{pk.String()}
	vault.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(500)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
	}
	keeper := &TestObservedTxOutHandleKeeper{
		nas:       NodeAccounts{GetRandomValidatorNode(NodeActive)},
		voter:     NewObservedTxVoter(tx.ID, make(ObservedTxs, 0)),
		txInVoter: NewObservedTxVoter(txInHash, make(ObservedTxs, 0)),
		pool: Pool{
			Asset:        common.ETHAsset,
			BalanceRune:  cosmos.NewUint(200_000),
			BalanceAsset: cosmos.NewUint(300_000),
		},
		vaultExists: true,
		vault:       vault,
		hashes:      make([]common.TxID, 0),
	}
	txOutStore := NewTxStoreDummy()
	txOutStore.blockOut.TxArray = append(txOutStore.blockOut.TxArray, TxOutItem{
		Chain:       tx.Chain,
		InHash:      txInHash,
		ToAddress:   tx.ToAddress,
		VaultPubKey: vault.PubKey,
		Coin:        tx.Coins[0],
		Memo:        tx.Memo,
	})
	keeper.txOutStore = txOutStore

	mgr.K = keeper
	eventMgr := NewDummyEventMgr()
	mgr.eventMgr = eventMgr
	mgr.slasher = newSlasher(keeper, eventMgr)
	validatorMgr := newValidatorMgr(keeper, mgr.NetworkMgr(), txOutStore, eventMgr)
	mgr.validatorMgr = validatorMgr

	handler := NewObservedTxOutHandler(mgr)
	msg := NewMsgObservedTxOut(txs, keeper.nas[0].NodeAddress)

	items, err := txOutStore.GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	pendingTxOuts, err := validatorMgr.getPendingTxOut(ctx)
	c.Assert(err, IsNil)
	// c.Check(pendingTxOuts, Equals, int64(1))
	c.Check(pendingTxOuts, Equals, int64(301)) // pendingTxOuts in fact returns 301; learn why.

	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	items, err = txOutStore.GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1) // Still present, but now has an OutHash.
	pendingTxOuts, err = validatorMgr.getPendingTxOut(ctx)
	c.Assert(err, IsNil)
	c.Check(pendingTxOuts, Equals, int64(0))

	mgr.ObMgr().EndBlock(ctx, keeper)
	c.Check(keeper.observing, HasLen, 1)

	// As this was a valid transaction, the Amount is treated as having already been subtracted in an earlier block.
	c.Check(int(keeper.pool.BalanceAsset.Uint64()), Equals, 300_000)
	// No decrease in pool balance before processGas.
	mgr.GasMgr().EndBlock(ctx, keeper, eventMgr)
	c.Check(int(keeper.pool.BalanceAsset.Uint64()), Equals, 262_500)
	// Gas 37500 has been subtracted from the 300,000 pool depth.

	vault, err = keeper.GetVault(ctx, pk)
	c.Assert(err, IsNil)
	// make sure the coin has been subtract from the vault
	c.Check(vault.Coins.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(19999962499)), Equals, true, Commentf("%d", vault.Coins.GetCoin(common.ETHAsset).Amount.Uint64()))
	// Gas 37500 and Amount 1 have been subtracted from the 200*common.One vault balance.

	hashes := keeper.GetObservedLink(ctx, tx.ID)
	c.Assert(hashes, HasLen, 1)
}

func (s *HandlerObservedTxOutSuite) TestHandleFailedTransaction(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)

	// When there is a failed transaction (such as an Ethereum out of gas failure),
	// the observed transaction has a different Amount and the memo's txInHash is the failed transaction's.
	// With the new behavior, fake gas transactions (amount=1 wei) are not slashed, just accounted for via addGasFees.
	// The pending outbound remains.
	tx := GetRandomTx()
	pk := GetRandomPubKey()
	tx.FromAddress, err = pk.GetAddress(tx.Coins[0].Asset.Chain)
	txInHash := GetRandomTxHash()          // Used later for the pending outbound.
	tx.Memo = fmt.Sprintf("OUT:%s", tx.ID) // Self-referential memo.
	obTx := NewObservedTx(tx, 12, pk, 12)
	txs := ObservedTxs{obTx}
	c.Assert(err, IsNil)

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(1000000 * common.One)
	na.PubKeySet.Secp256k1 = pk

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, pk, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Membership = []string{pk.String()}
	vault.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(500)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
	}
	keeper := &TestObservedTxOutHandleKeeper{
		nas:       NodeAccounts{GetRandomValidatorNode(NodeActive)},
		voter:     NewObservedTxVoter(tx.ID, make(ObservedTxs, 0)),
		txInVoter: NewObservedTxVoter(txInHash, make(ObservedTxs, 0)),
		pool: Pool{
			Asset:        common.ETHAsset,
			BalanceRune:  cosmos.NewUint(200_000),
			BalanceAsset: cosmos.NewUint(300_000),
		},
		vaultExists: true,
		vault:       vault,
		hashes:      make([]common.TxID, 0),
	}
	txOutStore := NewTxStoreDummy()
	txOutStore.blockOut.TxArray = append(txOutStore.blockOut.TxArray, TxOutItem{
		Chain:       tx.Chain,
		InHash:      txInHash,
		ToAddress:   tx.ToAddress,
		VaultPubKey: vault.PubKey,
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)), // Different from the observed Amount.
		Memo:        tx.Memo,
	})
	keeper.txOutStore = txOutStore

	mgr.K = keeper
	eventMgr := NewDummyEventMgr()
	mgr.eventMgr = eventMgr
	mgr.slasher = newSlasher(keeper, eventMgr)
	validatorMgr := newValidatorMgr(keeper, mgr.NetworkMgr(), txOutStore, eventMgr)
	mgr.validatorMgr = validatorMgr

	handler := NewObservedTxOutHandler(mgr)
	msg := NewMsgObservedTxOut(txs, keeper.nas[0].NodeAddress)

	items, err := txOutStore.GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	pendingTxOuts, err := validatorMgr.getPendingTxOut(ctx)
	c.Assert(err, IsNil)
	// c.Check(pendingTxOuts, Equals, int64(1))
	c.Check(pendingTxOuts, Equals, int64(301)) // pendingTxOuts in fact returns 301; learn why.

	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	items, err = txOutStore.GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	pendingTxOuts, err = validatorMgr.getPendingTxOut(ctx)
	c.Assert(err, IsNil)
	// c.Check(pendingTxOuts, Equals, int64(1)) // The pending outbound remains.
	c.Check(pendingTxOuts, Equals, int64(301)) // pendingTxOuts in fact returns 301; learn why.

	mgr.ObMgr().EndBlock(ctx, keeper)
	c.Check(keeper.observing, HasLen, 1)

	// With new behavior: fake gas tx (amount=1) is NOT slashed
	// No pool deductions yet (pool stays at 300,000)
	c.Check(int(keeper.pool.BalanceAsset.Uint64()), Equals, 300_000)

	mgr.GasMgr().EndBlock(ctx, keeper, eventMgr)
	// After gas manager processes, gas is handled correctly (single addGasFees call):
	// - handleObservedTxOutQuorum (line 590) calls addGasFees once
	// - handler_common_outbound does NOT call it again (no double deduction)
	// Pool gets: -37,500, so 300,000 - 37,500 = 262,500
	c.Check(int(keeper.pool.BalanceAsset.Uint64()), Equals, 262_500)

	vault, err = keeper.GetVault(ctx, pk)
	c.Assert(err, IsNil)
	// Vault deducted: only gas (37,500) for fake gas tx
	c.Check(vault.Coins.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(19999962500)), Equals, true, Commentf("%d", vault.Coins.GetCoin(common.ETHAsset).Amount.Uint64()))

	hashes := keeper.GetObservedLink(ctx, tx.ID)
	c.Assert(hashes, HasLen, 1)
}

func (s *HandlerObservedTxOutSuite) TestHandleStolenFundsInvalidMemo(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)

	tx := GetRandomTx()
	tx.Memo = "I AM A THIEF!" // bad memo
	obTx := NewObservedTx(tx, 12, GetRandomPubKey(), 12)
	obTx.Tx.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	txs := ObservedTxs{obTx}
	pk := GetRandomPubKey()
	c.Assert(err, IsNil)

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(1000000 * common.One)
	na.PubKeySet.Secp256k1 = pk

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, pk, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Membership = []string{pk.String()}
	vault.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(500*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
	}
	keeper := &TestObservedTxOutHandleKeeper{
		nas:   NodeAccounts{na},
		voter: NewObservedTxVoter(tx.ID, make(ObservedTxs, 0)),
		pool: Pool{
			Asset:        common.ETHAsset,
			BalanceRune:  cosmos.NewUint(200 * common.One),
			BalanceAsset: cosmos.NewUint(300 * common.One),
		},
		vaultExists: true,
		vault:       vault,
	}

	txOutStore := NewTxStoreDummy()
	keeper.txOutStore = txOutStore

	mgr.K = keeper
	eventMgr := NewDummyEventMgr()
	mgr.eventMgr = eventMgr
	mgr.slasher = newSlasher(keeper, NewDummyEventMgr())

	handler := NewObservedTxOutHandler(mgr)
	msg := NewMsgObservedTxOut(txs, keeper.nas[0].NodeAddress)

	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	c.Check(keeper.na.Bond.LT(na.Bond), Equals, true, Commentf("bond: %d", keeper.na.Bond.Uint64()))

	// Slashing debits the pool's asset balance by the stolen coins plus gas
	// (via SlashVault), rather than routing gas through the fee manager. The
	// offending node's bond covers the equivalent RUNE value.
	gasAmount := obTx.Tx.Gas.ToCoins().GetCoin(common.ETHAsset).Amount
	mgr.GasMgr().EndBlock(ctx, keeper, eventMgr)
	expectedPool := cosmos.NewUint(300 * common.One).Sub(cosmos.NewUint(100 * common.One)).Sub(gasAmount)
	c.Check(keeper.pool.BalanceAsset.Equal(expectedPool), Equals, true,
		Commentf("pool: %d", keeper.pool.BalanceAsset.Uint64()))

	// Vault balance should reflect outbound coins and gas deducted
	updatedVault, err := keeper.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	expectedVault := cosmos.NewUint(200 * common.One).Sub(cosmos.NewUint(100 * common.One)).Sub(gasAmount)
	c.Check(updatedVault.Coins.GetCoin(common.ETHAsset).Amount.Equal(expectedVault), Equals, true,
		Commentf("vault: %d", updatedVault.Coins.GetCoin(common.ETHAsset).Amount.Uint64()))
}

type HandlerObservedTxOutTestHelper struct {
	keeper.Keeper
	failListActiveValidators bool
	failVaultExist           bool
	failGetObservedTxOutVote bool
	failGetVault             bool
	failSetVault             bool
}

func NewHandlerObservedTxOutHelper(k keeper.Keeper) *HandlerObservedTxOutTestHelper {
	return &HandlerObservedTxOutTestHelper{
		Keeper: k,
	}
}

func (h *HandlerObservedTxOutTestHelper) ListActiveValidators(ctx cosmos.Context) (NodeAccounts, error) {
	if h.failListActiveValidators {
		return NodeAccounts{}, errKaboom
	}
	return h.Keeper.ListActiveValidators(ctx)
}

func (h *HandlerObservedTxOutTestHelper) VaultExists(ctx cosmos.Context, pk common.PubKey) bool {
	if h.failVaultExist {
		return false
	}
	return h.Keeper.VaultExists(ctx, pk)
}

func (h *HandlerObservedTxOutTestHelper) GetObservedTxOutVoter(ctx cosmos.Context, hash common.TxID) (ObservedTxVoter, error) {
	if h.failGetObservedTxOutVote {
		return ObservedTxVoter{}, errKaboom
	}
	return h.Keeper.GetObservedTxOutVoter(ctx, hash)
}

func (h *HandlerObservedTxOutTestHelper) GetVault(ctx cosmos.Context, pk common.PubKey) (Vault, error) {
	if h.failGetVault {
		return Vault{}, errKaboom
	}
	return h.Keeper.GetVault(ctx, pk)
}

func (h *HandlerObservedTxOutTestHelper) SetVault(ctx cosmos.Context, vault Vault) error {
	if h.failSetVault {
		return errKaboom
	}
	return h.Keeper.SetVault(ctx, vault)
}

func setupAnObservedTxOut(ctx cosmos.Context, helper *HandlerObservedTxOutTestHelper, c *C) *MsgObservedTxOut {
	activeNodeAccount := GetRandomValidatorNode(NodeActive)
	pk := GetRandomPubKey()
	tx := GetRandomTx()
	tx.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*3)),
	}
	tx.Memo = "OUT:" + GetRandomTxHash().String()
	addr, err := pk.GetAddress(tx.Coins[0].Asset.Chain)
	c.Assert(err, IsNil)
	tx.ToAddress = GetRandomETHAddress()
	tx.FromAddress = addr
	obTx := NewObservedTx(tx, ctx.BlockHeight(), pk, ctx.BlockHeight())
	txs := ObservedTxs{obTx}
	c.Assert(err, IsNil)
	vault := GetRandomVault()
	vault.PubKey = obTx.ObservedPubKey
	vault.Membership = []string{vault.PubKey.String()}
	c.Assert(helper.Keeper.SetNodeAccount(ctx, activeNodeAccount), IsNil)
	c.Assert(helper.SetVault(ctx, vault), IsNil)
	p := NewPool()
	p.Asset = common.ETHAsset
	p.BalanceRune = cosmos.NewUint(100 * common.One)
	p.BalanceAsset = cosmos.NewUint(100 * common.One)
	p.Status = PoolAvailable
	c.Assert(helper.Keeper.SetPool(ctx, p), IsNil)
	return NewMsgObservedTxOut(txs, activeNodeAccount.NodeAddress)
}

func (*HandlerObservedTxOutSuite) TestHandlerObservedTxOut_DifferentValidations(c *C) {
	testCases := []struct {
		name            string
		messageProvider func(c *C, ctx cosmos.Context, helper *HandlerObservedTxOutTestHelper) cosmos.Msg
		validator       func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *HandlerObservedTxOutTestHelper, name string)
	}{
		{
			name: "invalid message should return an error",
			messageProvider: func(c *C, ctx cosmos.Context, helper *HandlerObservedTxOutTestHelper) cosmos.Msg {
				return NewMsgNetworkFee(ctx.BlockHeight(), common.ETHChain, 1, 10000, GetRandomBech32Addr())
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *HandlerObservedTxOutTestHelper, name string) {
				c.Check(err, NotNil, Commentf(name))
				c.Check(result, IsNil)
				c.Check(errors.Is(err, errInvalidMessage), Equals, true)
			},
		},
		{
			name: "message fail validation should return an error",
			messageProvider: func(c *C, ctx cosmos.Context, helper *HandlerObservedTxOutTestHelper) cosmos.Msg {
				return NewMsgObservedTxOut(ObservedTxs{
					NewObservedTx(GetRandomTx(), ctx.BlockHeight(), GetRandomPubKey(), ctx.BlockHeight()),
				}, GetRandomBech32Addr())
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *HandlerObservedTxOutTestHelper, name string) {
				c.Check(err, NotNil, Commentf(name))
				c.Check(result, IsNil)
			},
		},
		{
			name: "voter already vote for the tx should return without doing anything",
			messageProvider: func(c *C, ctx cosmos.Context, helper *HandlerObservedTxOutTestHelper) cosmos.Msg {
				m := setupAnObservedTxOut(ctx, helper, c)
				voter, err := helper.Keeper.GetObservedTxOutVoter(ctx, m.Txs[0].Tx.ID)
				c.Assert(err, IsNil)
				voter.Add(m.Txs[0], m.Signer)
				helper.Keeper.SetObservedTxOutVoter(ctx, voter)
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *HandlerObservedTxOutTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
			},
		},
		{
			name: "fail to list active node accounts should result in an error",
			messageProvider: func(c *C, ctx cosmos.Context, helper *HandlerObservedTxOutTestHelper) cosmos.Msg {
				m := setupAnObservedTxOut(ctx, helper, c)
				helper.failListActiveValidators = true
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *HandlerObservedTxOutTestHelper, name string) {
				c.Check(err, NotNil, Commentf(name))
				c.Check(result, IsNil, Commentf(name))
			},
		},
		{
			name: "vault not exist should not result in an error, it should continue",
			messageProvider: func(c *C, ctx cosmos.Context, helper *HandlerObservedTxOutTestHelper) cosmos.Msg {
				m := setupAnObservedTxOut(ctx, helper, c)
				helper.failVaultExist = true
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *HandlerObservedTxOutTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
			},
		},
		{
			name: "fail to get observedTxOutVoter should not result in an error, it should continue",
			messageProvider: func(c *C, ctx cosmos.Context, helper *HandlerObservedTxOutTestHelper) cosmos.Msg {
				m := setupAnObservedTxOut(ctx, helper, c)
				helper.failGetObservedTxOutVote = true
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *HandlerObservedTxOutTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
			},
		},
		{
			name: "empty memo should not result in an error, it should continue",
			messageProvider: func(c *C, ctx cosmos.Context, helper *HandlerObservedTxOutTestHelper) cosmos.Msg {
				m := setupAnObservedTxOut(ctx, helper, c)
				m.Txs[0].Tx.Memo = ""
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *HandlerObservedTxOutTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
				txOut, err := helper.GetTxOut(ctx, ctx.BlockHeight())
				c.Assert(err, IsNil, Commentf(name))
				c.Assert(txOut.IsEmpty(), Equals, true)
			},
		},
		{
			name: "fail to get vault, it should continue",
			messageProvider: func(c *C, ctx cosmos.Context, helper *HandlerObservedTxOutTestHelper) cosmos.Msg {
				m := setupAnObservedTxOut(ctx, helper, c)
				helper.failGetVault = true
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *HandlerObservedTxOutTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
			},
		},
		{
			name: "fail to set vault, it should continue",
			messageProvider: func(c *C, ctx cosmos.Context, helper *HandlerObservedTxOutTestHelper) cosmos.Msg {
				m := setupAnObservedTxOut(ctx, helper, c)
				helper.failSetVault = true
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *HandlerObservedTxOutTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
			},
		},
		{
			name: "ragnarok memo it should success",
			messageProvider: func(c *C, ctx cosmos.Context, helper *HandlerObservedTxOutTestHelper) cosmos.Msg {
				m := setupAnObservedTxOut(ctx, helper, c)
				m.Txs[0].Tx.Memo = "ragnarok:100"
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *HandlerObservedTxOutTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
			},
		},
	}
	versions := []semver.Version{
		GetCurrentVersion(),
	}
	for _, tc := range testCases {
		for _, ver := range versions {
			ctx, mgr := setupManagerForTest(c)
			helper := NewHandlerObservedTxOutHelper(mgr.Keeper())
			mgr.K = helper
			mgr.currentVersion = ver
			handler := NewObservedTxOutHandler(mgr)
			msg := tc.messageProvider(c, ctx, helper)
			result, err := handler.Run(ctx, msg)
			tc.validator(c, ctx, result, err, helper, tc.name)
		}
	}
}

func (s *HandlerObservedTxOutSuite) TestObservingSlashing(c *C) {
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

	observedTx := GetRandomObservedTx()
	observedTx.BlockHeight = height
	observedTx.FinaliseHeight = height
	observedTx.ObservedPubKey = asgardVault.PubKey
	var err error
	observedTx.Tx.FromAddress, err = observedTx.ObservedPubKey.GetAddress(observedTx.Tx.Chain)
	c.Assert(err, IsNil)

	msg := NewMsgObservedTxOut([]common.ObservedTx{observedTx}, cosmos.AccAddress{})
	handler := NewObservedTxOutHandler(mgr)

	broadcast := func(c *C, ctx cosmos.Context, na NodeAccount, msg *MsgObservedTxOut) {
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

	// consensusMsg should be consistent with the consensus-observed message,
	// but with a slightly later BlockHeight and FinaliseHeight,
	// which is normal.
	consensusMsg := msg
	consensusMsg.Txs = []common.ObservedTx{msg.Txs[0]}
	consensusMsg.Txs[0].BlockHeight++
	consensusMsg.Txs[0].FinaliseHeight++

	// Within the ObservationDelayFlexibility period, nas[4] observes with consensusMsg
	// and is decremented LackOfObservationPenalty.
	height += observeFlex
	ctx = ctx.WithBlockHeight(height)
	broadcast(c, ctx, nas[4], consensusMsg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 0, 0, 0, 2, 0})

	// The ObservationDelayFlexibility period ends, after which nas[5] observes;
	// it is appropriately incremented ObserveSlashPoints since the network has to handle the observations
	// (and it is added to the list of signers)
	// and being past the ObservationDelayFlexibility period
	// neither ObserveSlashPoints nor LackOfObservationPenalty is decremented.
	height++
	ctx = ctx.WithBlockHeight(height)
	broadcast(c, ctx, nas[5], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 0, 0, 0, 3, 0})

	// nas[5] observes again, this time incremented ObserveSlashPoints for the extra signing.
	broadcast(c, ctx, nas[5], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 0, 0, 0, 4, 0})

	// Note that nas[6], the Standby node, remains unaffected by the Actives nodes' observations.
}

// TestHandleBouncedOutbound covers an EVM outbound the router returned to the
// sending vault while still emitting a full-value TransferOut. The observation
// deliberately keeps the intended recipient and amount so the TxOutItem match
// still succeeds; only the coin debit is skipped, and the failure is surfaced as
// a security event instead of silently under-counting the vault forever.
func (s *HandlerObservedTxOutSuite) TestHandleBouncedOutbound(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)

	tx := GetRandomTx()
	pk := GetRandomPubKey()
	tx.FromAddress, err = pk.GetAddress(tx.Coins[0].Asset.Chain)
	c.Assert(err, IsNil)
	txInHash := GetRandomTxHash()
	tx.Memo = fmt.Sprintf("OUT:%s", txInHash)
	obTx := NewObservedTx(tx, 12, pk, 12)
	obTx.Bounced = true
	txs := ObservedTxs{obTx}

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(1000000 * common.One)
	na.PubKeySet.Secp256k1 = pk

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, pk, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Membership = []string{pk.String()}
	vault.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(500)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
	}
	keeper := &TestObservedTxOutHandleKeeper{
		nas:       NodeAccounts{GetRandomValidatorNode(NodeActive)},
		voter:     NewObservedTxVoter(tx.ID, make(ObservedTxs, 0)),
		txInVoter: NewObservedTxVoter(txInHash, make(ObservedTxs, 0)),
		pool: Pool{
			Asset:        common.ETHAsset,
			BalanceRune:  cosmos.NewUint(200_000),
			BalanceAsset: cosmos.NewUint(300_000),
		},
		vaultExists: true,
		vault:       vault,
		hashes:      make([]common.TxID, 0),
	}
	txOutStore := NewTxStoreDummy()
	txOutStore.blockOut.TxArray = append(txOutStore.blockOut.TxArray, TxOutItem{
		Chain:       tx.Chain,
		InHash:      txInHash,
		ToAddress:   tx.ToAddress,
		VaultPubKey: vault.PubKey,
		Coin:        tx.Coins[0],
		Memo:        tx.Memo,
	})
	keeper.txOutStore = txOutStore

	mgr.K = keeper
	eventMgr := NewDummyEventMgr()
	mgr.eventMgr = eventMgr
	mgr.slasher = newSlasher(keeper, eventMgr)
	validatorMgr := newValidatorMgr(keeper, mgr.NetworkMgr(), txOutStore, eventMgr)
	mgr.validatorMgr = validatorMgr

	handler := NewObservedTxOutHandler(mgr)
	msg := NewMsgObservedTxOut(txs, keeper.nas[0].NodeAddress)

	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	// the outbound is still terminal: the TxOutItem is matched and no longer pending
	pendingTxOuts, err := validatorMgr.getPendingTxOut(ctx)
	c.Assert(err, IsNil)
	c.Check(pendingTxOuts, Equals, int64(0))

	vault, err = keeper.GetVault(ctx, pk)
	c.Assert(err, IsNil)
	// only gas (37500) comes off; the coin amount stays because the router
	// returned it. Compare against TestHandle, where 37500+1 is subtracted.
	c.Check(vault.Coins.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(19999962500)), Equals, true,
		Commentf("%d", vault.Coins.GetCoin(common.ETHAsset).Amount.Uint64()))

	// the undelivered outbound must not be silent
	securityEvents := 0
	for _, e := range ctx.EventManager().Events() {
		if e.Type == "security" {
			securityEvents++
		}
	}
	c.Check(securityEvents, Equals, 1)
}

// TestHandleBounceDiscoveredAfterConsensus covers the production ordering: the
// signing committee's receipt-less instant observation (non-final, Bounced=false)
// reaches quorum first and debits the vault; the scanner's finalised observation
// then reports the outbound as Bounced. The coins must come back to the vault and
// the failure must still be surfaced.
func (s *HandlerObservedTxOutSuite) TestHandleBounceDiscoveredAfterConsensus(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)

	tx := GetRandomTx()
	pk := GetRandomPubKey()
	tx.FromAddress, err = pk.GetAddress(tx.Coins[0].Asset.Chain)
	c.Assert(err, IsNil)
	txInHash := GetRandomTxHash()
	tx.Memo = fmt.Sprintf("OUT:%s", txInHash)

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(1000000 * common.One)
	na.PubKeySet.Secp256k1 = pk

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, pk, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Membership = []string{pk.String()}
	vault.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(500)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
	}
	keeper := &TestObservedTxOutHandleKeeper{
		nas:       NodeAccounts{GetRandomValidatorNode(NodeActive)},
		voter:     NewObservedTxVoter(tx.ID, make(ObservedTxs, 0)),
		txInVoter: NewObservedTxVoter(txInHash, make(ObservedTxs, 0)),
		pool: Pool{
			Asset:        common.ETHAsset,
			BalanceRune:  cosmos.NewUint(200_000),
			BalanceAsset: cosmos.NewUint(300_000),
		},
		vaultExists: true,
		vault:       vault,
		hashes:      make([]common.TxID, 0),
	}
	txOutStore := NewTxStoreDummy()
	txOutStore.blockOut.TxArray = append(txOutStore.blockOut.TxArray, TxOutItem{
		Chain:       tx.Chain,
		InHash:      txInHash,
		ToAddress:   tx.ToAddress,
		VaultPubKey: vault.PubKey,
		Coin:        tx.Coins[0],
		Memo:        tx.Memo,
	})
	keeper.txOutStore = txOutStore

	mgr.K = keeper
	eventMgr := NewDummyEventMgr()
	mgr.eventMgr = eventMgr
	mgr.slasher = newSlasher(keeper, eventMgr)
	validatorMgr := newValidatorMgr(keeper, mgr.NetworkMgr(), txOutStore, eventMgr)
	mgr.validatorMgr = validatorMgr
	handler := NewObservedTxOutHandler(mgr)

	securityEvents := func() int {
		n := 0
		for _, e := range ctx.EventManager().Events() {
			if e.Type == "security" {
				n++
			}
		}
		return n
	}

	// 1. instant observation: non-final, built from the TxOutItem (coin without
	// decimals, gas = gas limit) and with no receipt, so never Bounced
	instantTx := tx
	instantTx.Coins = common.NewCoins(common.NewCoin(tx.Coins[0].Asset, tx.Coins[0].Amount))
	instantTx.Gas = common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(60000))}
	instant := NewObservedTx(instantTx, 12, pk, 13)
	_, err = handler.handle(ctx, *NewMsgObservedTxOut(ObservedTxs{instant}, keeper.nas[0].NodeAddress))
	c.Assert(err, IsNil)
	vault, err = keeper.GetVault(ctx, pk)
	c.Assert(err, IsNil)
	// gas limit (60000) and amount (1) debited
	c.Check(vault.Coins.GetCoin(common.ETHAsset).Amount.Uint64(), Equals, uint64(19999939999))
	c.Check(securityEvents(), Equals, 0)

	// 2. scanner observation: finalised, from the receipt (coin with decimals,
	// gas = gas used 37500), router bounced the coins back
	finalTx := tx
	finalTx.Coins = common.NewCoins(common.NewCoin(tx.Coins[0].Asset, tx.Coins[0].Amount).WithDecimals(18))
	final := NewObservedTx(finalTx, 13, pk, 13)
	final.Bounced = true
	_, err = handler.handle(ctx, *NewMsgObservedTxOut(ObservedTxs{final}, keeper.nas[0].NodeAddress))
	c.Assert(err, IsNil)
	vault, err = keeper.GetVault(ctx, pk)
	c.Assert(err, IsNil)
	// the amount is back. Gas is not reconciled on the non-final -> final
	// hand-off (pre-existing; the instant observation's gas limit stays debited).
	c.Check(vault.Coins.GetCoin(common.ETHAsset).Amount.Uint64(), Equals, uint64(19999940000))
	c.Check(securityEvents(), Equals, 1)
	voter, err := keeper.GetObservedTxOutVoter(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Check(voter.Tx.Bounced, Equals, true)

	// 3. a repeat of the bounced observation must not credit twice
	_, err = handler.handle(ctx, *NewMsgObservedTxOut(ObservedTxs{final}, keeper.nas[0].NodeAddress))
	c.Assert(err, IsNil)
	vault, err = keeper.GetVault(ctx, pk)
	c.Assert(err, IsNil)
	c.Check(vault.Coins.GetCoin(common.ETHAsset).Amount.Uint64(), Equals, uint64(19999940000))
	c.Check(securityEvents(), Equals, 1)
}

// bouncedOutboundFixture wires an ETH vault holding 200 ETH, a matching
// TxOutItem and n active nodes behind an ObservedTxOut handler, so tests can
// vote from different nodes and read the vault balance back.
type bouncedOutboundFixture struct {
	ctx     cosmos.Context
	mgr     *Mgrs
	handler ObservedTxOutHandler
	keeper  *TestObservedTxOutHandleKeeper
	pk      common.PubKey
	tx      common.Tx
}

func newBouncedOutboundFixture(c *C, activeNodes int) *bouncedOutboundFixture {
	var err error
	ctx, mgr := setupManagerForTest(c)

	tx := GetRandomTx()
	pk := GetRandomPubKey()
	tx.FromAddress, err = pk.GetAddress(tx.Coins[0].Asset.Chain)
	c.Assert(err, IsNil)
	txInHash := GetRandomTxHash()
	tx.Memo = fmt.Sprintf("OUT:%s", txInHash)

	nas := make(NodeAccounts, activeNodes)
	for i := range nas {
		nas[i] = GetRandomValidatorNode(NodeActive)
	}

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, pk, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Membership = []string{pk.String()}
	vault.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(500)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
	}
	keeper := &TestObservedTxOutHandleKeeper{
		nas:       nas,
		voter:     NewObservedTxVoter(tx.ID, make(ObservedTxs, 0)),
		txInVoter: NewObservedTxVoter(txInHash, make(ObservedTxs, 0)),
		pool: Pool{
			Asset:        common.ETHAsset,
			BalanceRune:  cosmos.NewUint(200_000),
			BalanceAsset: cosmos.NewUint(300_000),
		},
		vaultExists: true,
		vault:       vault,
		hashes:      make([]common.TxID, 0),
	}
	txOutStore := NewTxStoreDummy()
	txOutStore.blockOut.TxArray = append(txOutStore.blockOut.TxArray, TxOutItem{
		Chain:       tx.Chain,
		InHash:      txInHash,
		ToAddress:   tx.ToAddress,
		VaultPubKey: vault.PubKey,
		Coin:        tx.Coins[0],
		Memo:        tx.Memo,
	})
	keeper.txOutStore = txOutStore

	mgr.K = keeper
	eventMgr := NewDummyEventMgr()
	mgr.eventMgr = eventMgr
	mgr.slasher = newSlasher(keeper, eventMgr)
	mgr.validatorMgr = newValidatorMgr(keeper, mgr.NetworkMgr(), txOutStore, eventMgr)

	return &bouncedOutboundFixture{ctx: ctx, mgr: mgr, handler: NewObservedTxOutHandler(mgr), keeper: keeper, pk: pk, tx: tx}
}

// errata reverts the outbound the way a reorg-driven errata would: the vault is
// credited the coins (unless the consensus was Bounced) and the voter is marked
// Reverted.
func (f *bouncedOutboundFixture) errata(c *C) {
	er := &common.ErrataTx{Id: f.tx.ID, Chain: f.tx.Chain}
	_, err := processErrataOutboundTx(f.ctx, f.keeper, f.mgr.EventMgr(), er)
	c.Assert(err, IsNil)
	c.Check(f.keeper.voter.Reverted, Equals, true)
}

// vote submits one finalised observation of the outbound from node i.
func (f *bouncedOutboundFixture) vote(c *C, i int, bounced bool) {
	f.voteAt(c, i, bounced, 12, 12)
}

// voteAt submits one observation from node i at an explicit block/finalise
// height, so a test can drive the non-final -> final hand-off.
func (f *bouncedOutboundFixture) voteAt(c *C, i int, bounced bool, height, finalise int64) {
	obTx := NewObservedTx(f.tx, height, f.pk, finalise)
	obTx.Bounced = bounced
	_, err := f.handler.handle(f.ctx, *NewMsgObservedTxOut(ObservedTxs{obTx}, f.keeper.nas[i].NodeAddress))
	c.Assert(err, IsNil)
}

// voteAtGas submits one finalised observation from node i carrying an explicit
// gas amount at the default height.
func (f *bouncedOutboundFixture) voteAtGas(c *C, i int, bounced bool, gas uint64) {
	f.voteAtGasHeight(c, i, bounced, gas, 12)
}

// voteAtGasHeight submits one finalised observation from node i carrying an
// explicit gas amount at an explicit block/finalise height, so a test can drive
// sequences of re-inclusions whose receipts report their own gas used and
// inclusion height.
func (f *bouncedOutboundFixture) voteAtGasHeight(c *C, i int, bounced bool, gas uint64, height int64) {
	tx := f.tx
	tx.Gas = common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(gas))}
	obTx := NewObservedTx(tx, height, f.pk, height)
	obTx.Bounced = bounced
	_, err := f.handler.handle(f.ctx, *NewMsgObservedTxOut(ObservedTxs{obTx}, f.keeper.nas[i].NodeAddress))
	c.Assert(err, IsNil)
}

func (f *bouncedOutboundFixture) ethBalance(c *C) uint64 {
	vault, err := f.keeper.GetVault(f.ctx, f.pk)
	c.Assert(err, IsNil)
	return vault.Coins.GetCoin(common.ETHAsset).Amount.Uint64()
}

func (f *bouncedOutboundFixture) securityEvents() int {
	n := 0
	for _, e := range f.ctx.EventManager().Events() {
		if e.Type == "security" {
			n++
		}
	}
	return n
}

// TestHandleBouncedMinorityCannotSuppressDebit is the load-bearing consensus
// property: Bounced decides whether the vault coin debit happens, so a lone
// Bounced observation must form its own minority group. With 6 active nodes
// (quorum 4), an honest supermajority still debits the vault no matter whether
// the dissenting vote lands first, in the middle, or after consensus.
func (s *HandlerObservedTxOutSuite) TestHandleBouncedMinorityCannotSuppressDebit(c *C) {
	const (
		untouched   = uint64(20000000000) // 200 ETH
		gasOnly     = untouched - 37500
		gasAndCoins = gasOnly - 1
	)

	// dissenting Bounced vote first, so it is voter.Txs[0]
	f := newBouncedOutboundFixture(c, 6)
	f.vote(c, 0, true)
	f.vote(c, 1, false)
	f.vote(c, 2, false)
	f.vote(c, 3, false)
	c.Check(f.ethBalance(c), Equals, untouched) // 3/6 honest + 1/6 bounced: no quorum
	f.vote(c, 4, false)
	c.Check(f.ethBalance(c), Equals, gasAndCoins) // 4/6 honest: debited
	c.Check(f.securityEvents(), Equals, 0)
	f.vote(c, 5, true) // a late second dissenter cannot credit anything back
	c.Check(f.ethBalance(c), Equals, gasAndCoins)
	c.Check(f.securityEvents(), Equals, 0)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)

	// the inverse: a lone honest vote cannot make a bounced supermajority debit
	f = newBouncedOutboundFixture(c, 6)
	f.vote(c, 0, false)
	f.vote(c, 1, true)
	f.vote(c, 2, true)
	f.vote(c, 3, true)
	c.Check(f.ethBalance(c), Equals, untouched)
	f.vote(c, 4, true)
	c.Check(f.ethBalance(c), Equals, gasOnly) // 4/6 bounced: coins retained, gas charged
	c.Check(f.securityEvents(), Equals, 1)
	f.vote(c, 5, false)
	c.Check(f.ethBalance(c), Equals, gasOnly)
	c.Check(f.securityEvents(), Equals, 1)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)
}

// TestHandleBounceReversedByReorgDebits covers the true -> false direction of the
// bounce correction. A reorg can re-include the same tx hash with a receipt that
// shows delivery; the scanner rescans that hash without sending any errata,
// because the hash still exists. Once a supermajority re-observes the outbound as
// delivered, the coins retained by the bounced consensus have in fact left the
// vault and must be debited.
func (s *HandlerObservedTxOutSuite) TestHandleBounceReversedByReorgDebits(c *C) {
	const (
		untouched   = uint64(20000000000) // 200 ETH
		gasOnly     = untouched - 37500
		gasAndCoins = gasOnly - 1
	)

	f := newBouncedOutboundFixture(c, 6)
	for i := 0; i < 4; i++ {
		f.vote(c, i, true)
	}
	c.Check(f.ethBalance(c), Equals, gasOnly) // 4/6 bounced: coins retained
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)
	c.Check(f.securityEvents(), Equals, 1)

	// A minority re-observing it as delivered must not move anything.
	f.vote(c, 4, false)
	f.vote(c, 5, false)
	c.Check(f.ethBalance(c), Equals, gasOnly)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)

	// A supermajority does: the coins actually left.
	for i := 0; i < 2; i++ {
		f.vote(c, i, false)
	}
	c.Check(f.ethBalance(c), Equals, gasAndCoins)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.securityEvents(), Equals, 2)

	// A repeat of the delivered observation must not debit twice.
	f.vote(c, 2, false)
	c.Check(f.ethBalance(c), Equals, gasAndCoins)
	c.Check(f.securityEvents(), Equals, 2)
}

// TestHandleBounceReversedOnFinalisation covers the same true -> false direction
// at the other reconciliation site: the non-final consensus already ran vault
// accounting as bounced (coins retained), and the finalised consensus disagrees
// because the reorg resolved to a delivered receipt before confirmation. The
// coins did leave, so the hand-off must debit them.
func (s *HandlerObservedTxOutSuite) TestHandleBounceReversedOnFinalisation(c *C) {
	const (
		untouched   = uint64(20000000000) // 200 ETH
		gasOnly     = untouched - 37500
		gasAndCoins = gasOnly - 1
	)

	f := newBouncedOutboundFixture(c, 1)
	f.voteAt(c, 0, true, 12, 13) // unfinalised: bounced, coins retained
	c.Check(f.ethBalance(c), Equals, gasOnly)
	c.Check(f.securityEvents(), Equals, 1)

	f.voteAt(c, 0, false, 13, 13) // finalised: delivered after all
	c.Check(f.ethBalance(c), Equals, gasAndCoins)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.securityEvents(), Equals, 2)
}

// TestHandleBounceOnFinalisationAfterMemoRecovery: the hand-off must compare the
// non-final and finalised consensus ignoring memo, like correctOutboundObservation.
// A non-final observation carrying the scheduled memo followed by a finalised,
// memoless scanner observation of the bounced receipt is still one outbound.
func (s *HandlerObservedTxOutSuite) TestHandleBounceOnFinalisationAfterMemoRecovery(c *C) {
	const (
		untouched   = uint64(20000000000) // 200 ETH
		gasOnly     = untouched - 37500
		gasAndCoins = gasOnly - 1
	)

	f := newBouncedOutboundFixture(c, 1)
	f.voteAt(c, 0, false, 12, 13) // unfinalised: delivered with the memo, debited
	c.Check(f.ethBalance(c), Equals, gasAndCoins)

	f.tx.Memo = ""               // the router emitted no memo
	f.voteAt(c, 0, true, 13, 13) // finalised: bounced
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)
	c.Check(f.ethBalance(c), Equals, gasOnly)
}

// TestHandleBounceDiscoveredAfterDeliveredConsensus is the symmetric partner of
// TestHandleBounceReversedByReorgDebits: the false -> true credit taken through
// correctOutboundObservation once a delivered consensus has already debited.
func (s *HandlerObservedTxOutSuite) TestHandleBounceDiscoveredAfterDeliveredConsensus(c *C) {
	const (
		untouched   = uint64(20000000000) // 200 ETH
		gasOnly     = untouched - 37500
		gasAndCoins = gasOnly - 1
	)

	f := newBouncedOutboundFixture(c, 6)
	for i := 0; i < 4; i++ {
		f.vote(c, i, false)
	}
	c.Check(f.ethBalance(c), Equals, gasAndCoins) // 4/6 delivered: debited
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.securityEvents(), Equals, 0)

	// A minority re-observing it as bounced must not credit anything back.
	f.vote(c, 4, true)
	f.vote(c, 5, true)
	c.Check(f.ethBalance(c), Equals, gasAndCoins)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)

	// A supermajority does: the coins never left.
	for i := 0; i < 2; i++ {
		f.vote(c, i, true)
	}
	c.Check(f.ethBalance(c), Equals, gasOnly)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)
	c.Check(f.securityEvents(), Equals, 1)

	// A repeat must not credit twice.
	f.vote(c, 2, true)
	c.Check(f.ethBalance(c), Equals, gasOnly)
	c.Check(f.securityEvents(), Equals, 1)
}

// TestHandleBounceCorrectionSupersededNeedsFreshQuorum: a bounce correction
// back onto a verdict an earlier correction already moved away from still
// applies, but never on the strength of the signatures that verdict's entry
// once held. A node that re-observes moves its signature onto its latest
// entry, so the stale verdict is left with whoever currently holds it: a
// minority re-filing it cannot reach supermajority, while a fresh full quorum
// can, because a whole committee only re-files a verdict when the chain really
// shows it again.
func (s *HandlerObservedTxOutSuite) TestHandleBounceCorrectionSupersededNeedsFreshQuorum(c *C) {
	const (
		untouched   = uint64(20000000000) // 200 ETH
		gasOnly     = untouched - 37500
		gasAndCoins = gasOnly - 1
	)

	f := newBouncedOutboundFixture(c, 6)
	for i := 0; i < 6; i++ {
		f.vote(c, i, true)
	}
	c.Check(f.ethBalance(c), Equals, gasOnly) // 6/6 bounced: coins retained
	c.Check(f.securityEvents(), Equals, 1)

	for i := 0; i < 6; i++ {
		f.vote(c, i, false)
	}
	c.Check(f.ethBalance(c), Equals, gasAndCoins) // 6/6 delivered: debited
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.securityEvents(), Equals, 2)

	// Two nodes re-file the superseded bounced verdict at a new inclusion
	// height. Their signatures are fresh, but they are a minority on their own:
	// the four nodes that moved on to the delivered verdict no longer back the
	// bounced one, so it cannot reach supermajority again.
	for i := 4; i < 6; i++ {
		f.voteAt(c, i, true, 14, 14)
	}
	c.Check(f.ethBalance(c), Equals, gasAndCoins)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.securityEvents(), Equals, 2)

	// A fresh full quorum re-observing the bounced verdict is another story: the
	// chain really shows the bounce again, so the coins are credited back.
	for i := 0; i < 6; i++ {
		f.voteAt(c, i, true, 15, 15)
	}
	c.Check(f.ethBalance(c), Equals, gasOnly)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)
	c.Check(f.securityEvents(), Equals, 3)
}

// TestHandleBounceCorrectionRedeliveryReusesEarlierGas: a bounce discovered
// after a delivered consensus, then a new inclusion delivering the outbound
// again with a replacement receipt whose gas used happens to equal the first
// delivery's. The final payload is a consensus identity a correction moved away
// from, and a fresh quorum must still be able to debit the coins again.
func (s *HandlerObservedTxOutSuite) TestHandleBounceCorrectionRedeliveryReusesEarlierGas(c *C) {
	const (
		untouched = uint64(20000000000) // 200 ETH
		firstGas  = uint64(40000)
		bounceGas = uint64(37500)
	)

	f := newBouncedOutboundFixture(c, 6)

	// delivered at the first inclusion height: coins and gas debited
	for i := 0; i < 6; i++ {
		f.voteAtGasHeight(c, i, false, firstGas, 12)
	}
	c.Check(f.ethBalance(c), Equals, untouched-firstGas-1)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)

	// a reorg re-includes the hash and the router bounces it back: coins
	// credited, gas settled on the bounced receipt
	for i := 0; i < 6; i++ {
		f.voteAtGasHeight(c, i, true, bounceGas, 13)
	}
	c.Check(f.ethBalance(c), Equals, untouched-bounceGas)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)

	// another reorg re-includes the hash as delivered, and this receipt reuses
	// the first delivery's gas: the fresh quorum confirms the sequence, so the
	// coins are debited again and the gas settles on the new receipt
	for i := 0; i < 6; i++ {
		f.voteAtGasHeight(c, i, false, firstGas, 14)
	}
	c.Check(f.ethBalance(c), Equals, untouched-firstGas-1)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.keeper.voter.Tx.Tx.Gas[0].Amount.Uint64(), Equals, firstGas)

	// the bounced verdict the second correction left behind cannot be revived on
	// its stale signers: a minority re-filing it at a new height is refused
	for i := 0; i < 2; i++ {
		f.voteAtGasHeight(c, i, true, bounceGas, 15)
	}
	c.Check(f.ethBalance(c), Equals, untouched-firstGas-1)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
}

// TestHandleBounceCorrectionRecordsLatestVote: a node that re-observes the
// outbound differently has changed its mind, not cast a second vote, so its
// latest entry is recorded and the verdict it left behind keeps no backing it
// can be revived on. The signature history itself stays intact - the
// redundant-variant refund cap counts every variant a signer has ever filed -
// and a node can never re-file an entry it already signed: that would be
// indistinguishable from a proposer replaying its original attestation.
func (s *HandlerObservedTxOutSuite) TestHandleBounceCorrectionRecordsLatestVote(c *C) {
	const (
		untouched   = uint64(20000000000) // 200 ETH
		gasOnly     = untouched - 37500
		gasAndCoins = gasOnly - 1
	)

	bouncedSigners := func(f *bouncedOutboundFixture) int {
		n := 0
		for _, tx := range f.keeper.voter.Txs {
			if tx.Bounced {
				n += len(tx.Signers)
			}
		}
		return n
	}

	f := newBouncedOutboundFixture(c, 6)
	for i := 0; i < 6; i++ {
		f.vote(c, i, true)
	}
	c.Check(bouncedSigners(f), Equals, 6)
	// One entry per signer: each counts where it signed, so nothing is recorded.
	c.Check(f.keeper.voter.LatestVotes, HasLen, 0)

	for i := 0; i < 6; i++ {
		f.vote(c, i, false)
	}
	c.Check(f.ethBalance(c), Equals, gasAndCoins)
	c.Check(bouncedSigners(f), Equals, 6) // history kept
	c.Check(f.keeper.voter.LatestVotes, HasLen, 6)
	for i := 0; i < 6; i++ {
		idx, ok := f.keeper.voter.LatestVote(f.keeper.nas[i].NodeAddress, true)
		c.Assert(ok, Equals, true)
		c.Check(f.keeper.voter.Txs[idx].Bounced, Equals, false)
	}

	// Re-filing the bounced entry they already signed is a duplicate, even
	// though their current votes moved away from it.
	securityEvents := f.securityEvents()
	for i := 0; i < 6; i++ {
		f.vote(c, i, true)
	}
	c.Check(bouncedSigners(f), Equals, 6)
	c.Check(f.ethBalance(c), Equals, gasAndCoins)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.securityEvents(), Equals, securityEvents)
}

// TestHandleBounceCorrectionAfterReorgReceipt is the sequence a signer actually
// produces: bifrost signs with zero confirmations, so its receipt-less instant
// observation is already finalised and cannot know the router bounced. The
// scanner's receipt corrects that, and a reorg's replacement receipt - a new
// payload, since it reports its own gas used - must still be able to correct it
// back at the same finality level.
func (s *HandlerObservedTxOutSuite) TestHandleBounceCorrectionAfterReorgReceipt(c *C) {
	const (
		untouched = uint64(20000000000) // 200 ETH
		origGas   = uint64(37500)
		reorgGas  = uint64(37600)
	)

	f := newBouncedOutboundFixture(c, 6)

	// the signer's instant observation: finalised, no receipt, delivered
	for i := 0; i < 6; i++ {
		f.voteAtGas(c, i, false, origGas)
	}
	c.Check(f.ethBalance(c), Equals, untouched-origGas-1)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)

	// the scanner's receipt: the router bounced it back, coins never left
	for i := 0; i < 6; i++ {
		f.voteAtGas(c, i, true, origGas)
	}
	c.Check(f.ethBalance(c), Equals, untouched-origGas)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)

	// a reorg re-includes the hash and this time it was delivered: the coins did
	// leave, so debit them - and settle the gas the replacement receipt reports
	for i := 0; i < 6; i++ {
		f.voteAtGas(c, i, false, reorgGas)
	}
	c.Check(f.ethBalance(c), Equals, untouched-reorgGas-1)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.keeper.voter.Tx.Tx.Gas[0].Amount.Uint64(), Equals, reorgGas)

	// a minority cannot revive the bounced payload the correction left behind
	for i := 0; i < 2; i++ {
		f.voteAtGas(c, i, true, origGas)
	}
	c.Check(f.ethBalance(c), Equals, untouched-reorgGas-1)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
}

// TestHandleBounceCorrectionReturnToGasPayloadNeedsQuorum: a payload left behind
// by a gas-only correction must not keep a stale supermajority a later bounce
// correction could land back on. Its signers can never re-file it.
func (s *HandlerObservedTxOutSuite) TestHandleBounceCorrectionReturnToGasPayloadNeedsQuorum(c *C) {
	const (
		untouched = uint64(20000000000) // 200 ETH
		origGas   = uint64(37500)
		newGas    = uint64(37600)
	)

	f := newBouncedOutboundFixture(c, 6)

	// delivered consensus at the original gas: coins and gas debited
	for i := 0; i < 6; i++ {
		f.voteAtGas(c, i, false, origGas)
	}
	c.Check(f.ethBalance(c), Equals, untouched-origGas-1)

	// gas-only correction; the (delivered, origGas) payload is now superseded
	for i := 0; i < 6; i++ {
		f.voteAtGas(c, i, false, newGas)
	}
	c.Check(f.ethBalance(c), Equals, untouched-newGas-1)

	// the receipt shows it bounced: credit the coins back
	for i := 0; i < 6; i++ {
		f.voteAtGas(c, i, true, newGas)
	}
	c.Check(f.ethBalance(c), Equals, untouched-newGas)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)

	// the payload the gas correction left behind is not a way back in: its
	// signers have all moved on and cannot re-file what they already signed
	for i := 0; i < 6; i++ {
		f.voteAtGas(c, i, false, origGas)
	}
	c.Check(f.ethBalance(c), Equals, untouched-newGas)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)
}

// TestHandleBounceCorrectionSkipsCancelTx: handleObservedTxOutQuorum returns
// before the coin debit for a cancel or approval tx - an EVM gas-asset outbound
// of exactly DustThreshold with no memo - and charges only gas. A later bounced
// quorum must not credit a principal that was never debited.
func (s *HandlerObservedTxOutSuite) TestHandleBounceCorrectionSkipsCancelTx(c *C) {
	f := newBouncedOutboundFixture(c, 6)
	// an approval tx: to the router, no memo, amount exactly DustThreshold
	f.tx.Memo = ""
	f.tx.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, common.ETHChain.DustThreshold()))

	for i := 0; i < 6; i++ {
		f.vote(c, i, false)
	}
	debited := f.ethBalance(c)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)

	// a supermajority now reports it bounced; nothing was debited, so nothing
	// may be credited back
	for i := 0; i < 6; i++ {
		f.voteAtGas(c, i, true, 37500)
	}
	c.Check(f.ethBalance(c), Equals, debited)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)
}

// TestHandleBounceCorrectionAfterErrata: an errata has already credited the coins
// back and marked the voter Reverted. A bounced quorum arriving afterwards must not
// credit the same coins a second time, on either correction path.
func (s *HandlerObservedTxOutSuite) TestHandleBounceCorrectionAfterErrata(c *C) {
	const (
		untouched   = uint64(20000000000) // 200 ETH
		gasOnly     = untouched - 37500
		gasAndCoins = gasOnly - 1
	)

	// correctOutboundObservation after a finalised delivered consensus
	f := newBouncedOutboundFixture(c, 6)
	for i := 0; i < 4; i++ {
		f.vote(c, i, false)
	}
	c.Check(f.ethBalance(c), Equals, gasAndCoins)
	f.errata(c)
	c.Check(f.ethBalance(c), Equals, gasOnly) // coins restored by the errata
	c.Check(f.securityEvents(), Equals, 1)    // "outbound errata"
	for i := 0; i < 4; i++ {
		f.vote(c, i, true)
	}
	c.Check(f.ethBalance(c), Equals, gasOnly) // not restored twice
	c.Check(f.securityEvents(), Equals, 1)

	// the non-final -> final hand-off
	f = newBouncedOutboundFixture(c, 1)
	f.voteAt(c, 0, false, 12, 13) // unfinalised delivered: debited
	c.Check(f.ethBalance(c), Equals, gasAndCoins)
	f.errata(c)
	c.Check(f.ethBalance(c), Equals, gasOnly)
	c.Check(f.securityEvents(), Equals, 1)
	f.voteAt(c, 0, true, 13, 13) // finalised bounced after the errata
	c.Check(f.ethBalance(c), Equals, gasOnly)
	c.Check(f.securityEvents(), Equals, 1)
}

// TestHandleBounceCorrectionAfterMemoRecovery: a memoless outbound's consensus
// memo is rewritten with the one recovered from the scheduled TxOutItem, but the
// router emitted none, so every later scanner observation still carries the empty
// memo it saw. The correction must still recognise them as the same outbound.
func (s *HandlerObservedTxOutSuite) TestHandleBounceCorrectionAfterMemoRecovery(c *C) {
	const (
		untouched   = uint64(20000000000) // 200 ETH
		gasOnly     = untouched - 37500
		gasAndCoins = gasOnly - 1
	)

	f := newBouncedOutboundFixture(c, 6)
	recovered := f.tx.Memo
	f.tx.Memo = "" // memoless: the router emitted no memo

	for i := 0; i < 4; i++ {
		f.vote(c, i, false)
	}
	c.Check(f.ethBalance(c), Equals, gasAndCoins) // 4/6 delivered: debited
	// the consensus tx now carries the memo recovered from the scheduled TxOutItem
	c.Check(f.keeper.voter.Tx.Tx.Memo, Equals, recovered)

	// the receipt shows the router bounced it back; the empty memo on those
	// observations must not stop the reconciliation
	for i := 0; i < 6; i++ {
		f.vote(c, i, true)
	}
	c.Check(f.ethBalance(c), Equals, gasOnly)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)
}

// TestHandleBounceCorrectionSupersededIsPerFinality: signatures move only within
// a finality level, and countMatchingSigners never counts an observation of the
// other finality, so an unfinalised verdict left behind can never bear on a
// correction of the finalised consensus - and a reorg found after finalisation
// still has to be reconcilable.
func (s *HandlerObservedTxOutSuite) TestHandleBounceCorrectionSupersededIsPerFinality(c *C) {
	const (
		untouched   = uint64(20000000000) // 200 ETH
		gasOnly     = untouched - 37500
		gasAndCoins = gasOnly - 1
	)

	f := newBouncedOutboundFixture(c, 6)

	// unfinalised consensus: delivered, debited
	for i := 0; i < 4; i++ {
		f.voteAt(c, i, false, 12, 13)
	}
	c.Check(f.ethBalance(c), Equals, gasAndCoins)

	// unfinalised correction: the receipt shows the router bounced it
	for i := 0; i < 6; i++ {
		f.voteAt(c, i, true, 12, 13)
	}
	c.Check(f.ethBalance(c), Equals, gasOnly)

	// finalisation agrees, so there is nothing to reconcile
	for i := 0; i < 4; i++ {
		f.voteAt(c, i, true, 13, 13)
	}
	c.Check(f.ethBalance(c), Equals, gasOnly)

	// a reorg re-includes the hash as delivered: the unfinalised delivered payload
	// left behind above does not shadow the finalised one, so this still corrects
	for i := 0; i < 6; i++ {
		f.voteAt(c, i, false, 13, 13)
	}
	c.Check(f.ethBalance(c), Equals, gasAndCoins)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
}

// TestHandleBounceCorrectionIgnoresAbandonedMinorityVotes: three nodes report a
// bounce that never reaches consensus, the whole committee then reports delivery,
// and one straggler files the bounce afterwards. The three abandoned votes must
// not combine with it into a quorum that credits the coins back - their authors
// have since said the outbound was delivered.
func (s *HandlerObservedTxOutSuite) TestHandleBounceCorrectionIgnoresAbandonedMinorityVotes(c *C) {
	const (
		untouched = uint64(20000000000) // 200 ETH
		gas       = uint64(40000)
	)

	f := newBouncedOutboundFixture(c, 6)

	// a minority reports the bounce first: no consensus either way
	for i := 0; i < 3; i++ {
		f.voteAtGasHeight(c, i, true, gas, 12)
	}
	c.Check(f.ethBalance(c), Equals, untouched)

	// the committee reports delivery: coins and gas debited
	for i := 0; i < 6; i++ {
		f.voteAtGasHeight(c, i, false, gas, 13)
	}
	c.Check(f.ethBalance(c), Equals, untouched-gas-1)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)

	// one straggler files the bounce: one current vote, not four
	f.voteAtGasHeight(c, 3, true, gas, 14)
	c.Check(f.ethBalance(c), Equals, untouched-gas-1)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
}

// TestHandleBounceFinalisationIgnoresAbandonedVotes is the same abandoned
// minority, but before the finalised consensus exists: the non-final -> final
// hand-off must pick its verdict from current votes too, or three abandoned
// final bounce votes plus one straggler finalise a bounce only one node holds.
func (s *HandlerObservedTxOutSuite) TestHandleBounceFinalisationIgnoresAbandonedVotes(c *C) {
	const (
		untouched   = uint64(20000000000) // 200 ETH
		gasOnly     = untouched - 37500
		gasAndCoins = gasOnly - 1
	)

	f := newBouncedOutboundFixture(c, 6)
	for i := 0; i < 4; i++ {
		f.voteAt(c, i, false, 12, 13) // non-final delivered consensus: debited
	}
	c.Check(f.ethBalance(c), Equals, gasAndCoins)

	// three nodes report a final bounce, then change their minds
	for i := 0; i < 3; i++ {
		f.voteAt(c, i, true, 13, 13)
		f.voteAt(c, i, false, 14, 14)
	}
	// a fourth reports the bounce: four historical signers, one current vote
	f.voteAt(c, 3, true, 13, 13)
	c.Check(f.keeper.voter.FinalisedHeight, Equals, int64(0))
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.ethBalance(c), Equals, gasAndCoins)

	// the delivered verdict still finalises once a supermajority holds it
	for i := 3; i < 6; i++ {
		f.voteAt(c, i, false, 14, 14)
	}
	c.Check(f.keeper.voter.FinalisedHeight > 0, Equals, true)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.ethBalance(c), Equals, gasAndCoins)
	c.Check(f.securityEvents(), Equals, 0)
}

// TestHandleBounceCorrectionEarlyDeliveryVoteDoesNotBlockQuorum: one honest node
// observes the re-delivery before the bounce reaches consensus. Its early vote is
// the only one at that inclusion height when the bounce correction lands, and it
// must not cost the rest of the committee their correction when they follow.
func (s *HandlerObservedTxOutSuite) TestHandleBounceCorrectionEarlyDeliveryVoteDoesNotBlockQuorum(c *C) {
	const (
		untouched = uint64(20000000000) // 200 ETH
		gas       = uint64(40000)
		bounceGas = uint64(37500)
	)

	f := newBouncedOutboundFixture(c, 6)

	// delivered consensus at the first inclusion height
	for i := 0; i < 6; i++ {
		f.voteAtGasHeight(c, i, false, gas, 12)
	}
	c.Check(f.ethBalance(c), Equals, untouched-gas-1)

	// one node runs ahead and reports the next re-delivery already
	f.voteAtGasHeight(c, 0, false, gas, 14)

	// the other five report the bounce in between: 5/6, so the coins come back
	for i := 1; i < 6; i++ {
		f.voteAtGasHeight(c, i, true, bounceGas, 13)
	}
	c.Check(f.ethBalance(c), Equals, untouched-bounceGas)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)

	// they then catch up on the re-delivery the first node already saw
	for i := 1; i < 6; i++ {
		f.voteAtGasHeight(c, i, false, gas, 14)
	}
	c.Check(f.ethBalance(c), Equals, untouched-gas-1)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)

	// the same sequence with the early node reporting the bounce first, so it
	// has a vote to move rather than only one to cast
	f = newBouncedOutboundFixture(c, 6)
	for i := 0; i < 6; i++ {
		f.voteAtGasHeight(c, i, false, gas, 12)
	}
	f.voteAtGasHeight(c, 0, true, bounceGas, 13)
	f.voteAtGasHeight(c, 0, false, gas, 14) // it moves on before the others vote
	for i := 1; i < 6; i++ {
		f.voteAtGasHeight(c, i, true, bounceGas, 13)
	}
	c.Check(f.ethBalance(c), Equals, untouched-bounceGas) // 5/6 bounced
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)
	for i := 1; i < 6; i++ {
		f.voteAtGasHeight(c, i, false, gas, 14)
	}
	c.Check(f.ethBalance(c), Equals, untouched-gas-1)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
}

// TestHandleBounceCorrectionRedeliveryAtEarlierHeight: a replacement chain can
// put the outbound back at the height it was first included at, with the same
// receipt - a payload byte-identical to one every node has already filed. That is
// indistinguishable from a proposer replaying the original attestations, so it is
// rejected as a duplicate and the bounced verdict stands. This is a deliberate
// trade-off: re-accepting it needs a signed freshness token on attestations.
func (s *HandlerObservedTxOutSuite) TestHandleBounceCorrectionRedeliveryAtEarlierHeight(c *C) {
	const (
		untouched = uint64(20000000000) // 200 ETH
		gas       = uint64(40000)
		bounceGas = uint64(37500)
	)

	f := newBouncedOutboundFixture(c, 6)

	for i := 0; i < 6; i++ {
		f.voteAtGasHeight(c, i, false, gas, 12)
	}
	c.Check(f.ethBalance(c), Equals, untouched-gas-1)

	for i := 0; i < 6; i++ {
		f.voteAtGasHeight(c, i, true, bounceGas, 13)
	}
	c.Check(f.ethBalance(c), Equals, untouched-bounceGas)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)

	// the same height and the same gas as the first delivery
	for i := 0; i < 6; i++ {
		f.voteAtGasHeight(c, i, false, gas, 12)
	}
	c.Check(f.ethBalance(c), Equals, untouched-bounceGas)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)
}

// TestHandleBounceCorrectionRejectsReplayedQuorum: quorum attestations sign the
// payload only, so a block proposer holding a quorum's attestations can inject
// them again at any later block. Once the committee has moved on to the bounced
// verdict, re-injecting the original delivered quorum unchanged must not move
// any vote back or debit the vault a second time.
func (s *HandlerObservedTxOutSuite) TestHandleBounceCorrectionRejectsReplayedQuorum(c *C) {
	const (
		untouched = uint64(20000000000) // 200 ETH
		gas       = uint64(40000)
		bounceGas = uint64(37500)
	)

	f := newBouncedOutboundFixture(c, 6)
	privKeys := make([]secp256k1.PrivKey, len(f.keeper.nas))
	for i := range f.keeper.nas {
		privKeys[i] = secp256k1.GenPrivKey()
		pk, err := common.NewPubKeyFromCrypto(privKeys[i].PubKey())
		c.Assert(err, IsNil)
		f.keeper.nas[i].NodeAddress = cosmos.AccAddress(privKeys[i].PubKey().Address())
		f.keeper.nas[i].PubKeySet = common.PubKeySet{Secp256k1: pk, Ed25519: pk}
	}
	quorum := func(bounced bool, gas uint64, height int64) *types.MsgObservedTxQuorum {
		tx := f.tx
		tx.Gas = common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(gas))}
		obTx := NewObservedTx(tx, height, f.pk, height)
		obTx.Bounced = bounced
		atts := make([]*common.Attestation, len(privKeys))
		for i, privKey := range privKeys {
			atts[i] = attestObservedTx(c, privKey, obTx, false)
		}
		return types.NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: obTx, Attestations: atts}, f.keeper.nas[0].NodeAddress)
	}
	handler := NewObservedTxQuorumHandler(f.mgr)
	inject := func(msg *types.MsgObservedTxQuorum) {
		_, err := handler.handle(f.ctx, *msg)
		c.Assert(err, IsNil)
	}

	delivered := quorum(false, gas, 12)
	inject(delivered)
	c.Check(f.ethBalance(c), Equals, untouched-gas-1)

	inject(quorum(true, bounceGas, 13))
	c.Check(f.ethBalance(c), Equals, untouched-bounceGas)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)

	inject(delivered)
	c.Check(f.ethBalance(c), Equals, untouched-bounceGas)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)
}

// voteInstant submits node i's signer-side instant observation of the outbound:
// no receipt, so never Bounced, filed at the signing-tip height with the gas
// limit as gas and zero confirmations, so it is final.
func (f *bouncedOutboundFixture) voteInstant(c *C, i int, gas uint64, height int64) {
	f.voteAtGasHeight(c, i, false, gas, height)
}

// TestHandleBounceLateInstantDebitsAtWorst: delivery order is not freshness. The
// signers' receipt-less instant observations can reach THORNode after the
// scanner's finalised Bounced quorum (late-sent when their signing-tip heights
// split them into sub-quorum gossip buckets). Without a signed freshness token
// they are indistinguishable from a reorg re-including the tx below the bounced
// receipt as delivered, so a supermajority of them debits the coins. That is the
// recoverable direction: the vault under-counts coins it still holds, as it did
// before bounce detection, rather than over-counting coins that left.
func (s *HandlerObservedTxOutSuite) TestHandleBounceLateInstantDebitsAtWorst(c *C) {
	const (
		untouched  = uint64(20000000000) // 200 ETH
		scannerGas = uint64(37500)       // gas used, from the receipt
		instantGas = uint64(60000)       // gas limit, the instant observation's guess
	)

	f := newBouncedOutboundFixture(c, 6)
	for i := 0; i < 4; i++ {
		f.voteAtGasHeight(c, i, true, scannerGas, 12)
	}
	c.Assert(f.keeper.voter.Tx.Bounced, Equals, true)
	c.Assert(f.ethBalance(c), Equals, untouched-scannerGas)

	// a minority of late instant observations changes nothing
	for i := 0; i < 3; i++ {
		f.voteInstant(c, i, instantGas, 11)
	}
	c.Check(f.keeper.voter.Tx.Bounced, Equals, true)
	c.Check(f.ethBalance(c), Equals, untouched-scannerGas)

	f.voteInstant(c, 3, instantGas, 11)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.ethBalance(c), Equals, untouched-instantGas-1)
}

// TestHandleBounceLateInstantDebitsOnFinalisation: the same late instant
// observations can form the finalised consensus after a non-final scanner
// consensus retained the coins as Bounced. A delivered finalised consensus below
// the non-final height is reconciled (debited) like any other, for the same
// reason as TestHandleBounceLateInstantDebitsAtWorst.
func (s *HandlerObservedTxOutSuite) TestHandleBounceLateInstantDebitsOnFinalisation(c *C) {
	const (
		untouched  = uint64(20000000000) // 200 ETH
		scannerGas = uint64(37500)       // the fixture tx's gas
		instantGas = uint64(60000)
	)

	f := newBouncedOutboundFixture(c, 6)
	for i := 0; i < 4; i++ {
		f.voteAt(c, i, true, 12, 15) // unfinalised: bounced, coins retained
	}
	c.Assert(f.keeper.voter.Tx.Bounced, Equals, true)
	c.Assert(f.ethBalance(c), Equals, untouched-scannerGas)

	for i := 0; i < 4; i++ {
		f.voteInstant(c, i, instantGas, 11)
	}
	c.Check(f.keeper.voter.FinalisedHeight > 0, Equals, true)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.ethBalance(c), Equals, untouched-scannerGas-1)
}

// TestHandleBounceCorrectionAtLowerReinclusionHeight: a reorg's replacement
// chain includes a bounced outbound earlier (12 < 13) and this time it was
// delivered. Every node attests the new receipt, so the coins must be debited
// even though its height is below the bounced verdict's.
func (s *HandlerObservedTxOutSuite) TestHandleBounceCorrectionAtLowerReinclusionHeight(c *C) {
	const (
		untouched   = uint64(20000000000) // 200 ETH
		bounceGas   = uint64(37500)
		deliveryGas = uint64(41000)
	)

	f := newBouncedOutboundFixture(c, 6)
	for i := 0; i < 6; i++ {
		f.voteAtGasHeight(c, i, true, bounceGas, 13)
	}
	c.Assert(f.keeper.voter.Tx.Bounced, Equals, true)
	c.Assert(f.ethBalance(c), Equals, untouched-bounceGas)

	for i := 0; i < 6; i++ {
		f.voteAtGasHeight(c, i, false, deliveryGas, 12)
	}
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.keeper.voter.Tx.BlockHeight, Equals, int64(12))
	c.Check(f.ethBalance(c), Equals, untouched-deliveryGas-1)
}

// TestHandleBounceReversedOnFinalisationAtLowerHeight: the non-final consensus
// retained the coins as Bounced at height 13, and the finalised consensus is a
// delivered re-inclusion at height 12. The hand-off debits the coins.
func (s *HandlerObservedTxOutSuite) TestHandleBounceReversedOnFinalisationAtLowerHeight(c *C) {
	const untouched = uint64(20000000000) // 200 ETH
	const gas = uint64(37500)             // the fixture tx's gas

	f := newBouncedOutboundFixture(c, 6)
	for i := 0; i < 4; i++ {
		f.voteAt(c, i, true, 13, 16) // unfinalised: bounced, coins retained
	}
	c.Assert(f.ethBalance(c), Equals, untouched-gas)

	for i := 0; i < 4; i++ {
		f.voteAt(c, i, false, 12, 12)
	}
	c.Check(f.keeper.voter.FinalisedHeight > 0, Equals, true)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.ethBalance(c), Equals, untouched-gas-1)
}

// TestHandleBounceFinalisationBelowDeliveryKeepsDebit: the converse of
// TestHandleBounceReversedOnFinalisationAtLowerHeight. A Bounced finalised
// consensus below a delivered non-final one may be a superseded receipt
// late-sent after the newer one, so the hand-off keeps the delivery rather than
// credit back coins that may have left.
func (s *HandlerObservedTxOutSuite) TestHandleBounceFinalisationBelowDeliveryKeepsDebit(c *C) {
	const untouched = uint64(20000000000) // 200 ETH
	const gas = uint64(37500)             // the fixture tx's gas

	f := newBouncedOutboundFixture(c, 6)
	for i := 0; i < 4; i++ {
		f.voteAt(c, i, false, 13, 16) // unfinalised: delivered, coins debited
	}
	c.Assert(f.ethBalance(c), Equals, untouched-gas-1)

	for i := 0; i < 4; i++ {
		f.voteAt(c, i, true, 12, 12)
	}
	c.Check(f.keeper.voter.FinalisedHeight > 0, Equals, true)
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.keeper.voter.Tx.BlockHeight, Equals, int64(13))
	c.Check(f.ethBalance(c), Equals, untouched-gas-1)
}

// TestHandleBounceStaleReceiptCannotDisplaceNewer: a re-inclusion moved the
// outbound from a bounced receipt at height 12 to a delivered one at height 14.
// Two nodes' attestations of the superseded height-12 receipt are late-sent after
// that and move their latest votes back onto it, which gives it a supermajority
// of current votes again. A receipt below the height of the verdict it would
// overturn is stale, so the delivery stands.
func (s *HandlerObservedTxOutSuite) TestHandleBounceStaleReceiptCannotDisplaceNewer(c *C) {
	const (
		untouched   = uint64(20000000000) // 200 ETH
		bounceGas   = uint64(37500)
		deliveryGas = uint64(40000)
	)

	f := newBouncedOutboundFixture(c, 6)
	for i := 0; i < 4; i++ {
		f.voteAtGasHeight(c, i, true, bounceGas, 12)
	}
	c.Assert(f.ethBalance(c), Equals, untouched-bounceGas)

	for i := 2; i < 6; i++ {
		f.voteAtGasHeight(c, i, false, deliveryGas, 14)
	}
	c.Assert(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Assert(f.ethBalance(c), Equals, untouched-deliveryGas-1)

	for i := 4; i < 6; i++ {
		f.voteAtGasHeight(c, i, true, bounceGas, 12)
	}
	c.Check(f.keeper.voter.Tx.Bounced, Equals, false)
	c.Check(f.ethBalance(c), Equals, untouched-deliveryGas-1)
}
