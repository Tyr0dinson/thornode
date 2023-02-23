package thorchain

import (
	"fmt"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
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
	msg := NewMsgNetworkFee(ctx.BlockHeight(), common.BNBChain, 1, bnbSingleTxFee.Uint64(), GetRandomBech32Addr())
	result, err := handler.Run(ctx, msg)
	c.Check(result, IsNil, Commentf("invalid message should result an error"))
	c.Check(err, NotNil, Commentf("invalid message should result an error"))
	addr, err := keeper.vault.PubKey.GetAddress(common.BNBChain)
	c.Assert(err, IsNil)

	tx := NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.BNBChain,
		Coins:       common.Coins{common.NewCoin(common.BNBAsset, cosmos.NewUint(1*common.One))},
		Memo:        "",
		FromAddress: GetRandomBNBAddress(),
		ToAddress:   addr,
		Gas:         BNBGasFeeSingleton,
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
	keeper.KVStoreDummy
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

func (k *TestRagnarokKeeperHappyPath) GetNodeAccount(_ cosmos.Context, addr cosmos.AccAddress) (NodeAccount, error) {
	if k.activeNodeAccount.NodeAddress.Equals(addr) {
		return k.activeNodeAccount, nil
	}
	return NodeAccount{}, nil
}

func (k *TestRagnarokKeeperHappyPath) ListActiveValidators(_ cosmos.Context) (NodeAccounts, error) {
	return NodeAccounts{k.activeNodeAccount}, nil
}

func (k *TestRagnarokKeeperHappyPath) GetPool(_ cosmos.Context, _ common.Asset) (Pool, error) {
	return k.pool, nil
}

func (k *TestRagnarokKeeperHappyPath) SetPool(_ cosmos.Context, p Pool) error {
	k.pool = p
	return nil
}

func (HandlerRagnarokSuite) TestRagnarokHappyPath(c *C) {
	ctx, _ := setupKeeperForTest(c)
	retireVault := GetRandomVault()

	newVault := GetRandomVault()
	txout := NewTxOut(1)
	newVaultAddr, err := newVault.PubKey.GetAddress(common.BNBChain)
	c.Assert(err, IsNil)
	txout.TxArray = append(txout.TxArray, TxOutItem{
		Chain:       common.BNBChain,
		InHash:      common.BlankTxID,
		ToAddress:   newVaultAddr,
		VaultPubKey: retireVault.PubKey,
		Coin:        common.NewCoin(common.BNBAsset, cosmos.NewUint(1024)),
		Memo:        NewRagnarokMemo(1).String(),
	})
	keeper := &TestRagnarokKeeperHappyPath{
		activeNodeAccount: GetRandomValidatorNode(NodeActive),
		newVault:          newVault,
		retireVault:       retireVault,
		txout:             txout,
	}
	addr, err := keeper.retireVault.PubKey.GetAddress(common.BNBChain)
	c.Assert(err, IsNil)
	handler := NewRagnarokHandler(NewDummyMgrWithKeeper(keeper))
	tx := NewObservedTx(common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.BNBChain,
		Coins: common.Coins{
			common.NewCoin(common.BNBAsset, cosmos.NewUint(1024)),
		},
		Memo:        NewRagnarokMemo(1).String(),
		FromAddress: addr,
		ToAddress:   newVaultAddr,
		Gas:         BNBGasFeeSingleton,
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

func (HandlerRagnarokSuite) TestSlash(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(keeper)
	mgr.slasher = newSlasherV92(keeper, NewDummyEventMgr())

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	retireVault := GetRandomVault()
	vaultCoin := common.NewCoin(common.BNBAsset, cosmos.NewUint(100*common.One))
	retireVault.AddFunds(common.NewCoins(vaultCoin))
	retireVault.Membership = []string{
		na.PubKeySet.Secp256k1.String(),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, retireVault), IsNil)

	newVault := GetRandomVault()
	c.Assert(mgr.Keeper().SetVault(ctx, newVault), IsNil)

	pool := NewPool()
	pool.Asset = common.BNBAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	newVaultAddr, err := newVault.PubKey.GetAddress(common.BNBChain)
	c.Assert(err, IsNil)
	addr, err := retireVault.PubKey.GetAddress(common.BNBChain)
	c.Assert(err, IsNil)

	handler := NewInternalHandler(mgr)

	tx := NewObservedTx(common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.BNBChain,
		Coins: common.Coins{
			common.NewCoin(common.BNBAsset, cosmos.NewUint(1024)),
		},
		Memo:        NewRagnarokMemo(1).String(),
		FromAddress: addr,
		ToAddress:   newVaultAddr,
		Gas:         BNBGasFeeSingleton,
	}, 1, retireVault.PubKey, 1)

	msgRagnarok := NewMsgRagnarok(tx, 1, na.NodeAddress)
	_, err = handler(ctx, msgRagnarok)
	c.Assert(err, NotNil)
	// The internal handler returns an error, not approving the observed txout as authorised.
	c.Assert(strings.Contains(err.Error(), "slash vault, ragnarok has no matched tx out item"), Equals, true, Commentf(err.Error()))

	obsTxOutHandler := NewObservedTxOutHandler(mgr)
	obsTxOutMsg := NewMsgObservedTxOut([]ObservedTx{msgRagnarok.Tx}, msgRagnarok.Signer)
	_, err = obsTxOutHandler.Run(ctx, obsTxOutMsg)
	c.Assert(err, IsNil)

	na, err = mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	retireVault, err = mgr.Keeper().GetVault(ctx, retireVault.PubKey)
	c.Assert(err, IsNil)
	vaultCoin = retireVault.Coins.GetCoin(common.BNBAsset)
	pool, err = mgr.Keeper().GetPool(ctx, pool.Asset)
	c.Assert(err, IsNil)

	c.Check(na.Bond.Equal(cosmos.NewUint(9999942214)), Equals, true, Commentf("%d", na.Bond.Uint64()))
	c.Check(vaultCoin.Amount.Equal(cosmos.NewUint(9999961476)), Equals, true, Commentf("%d", vaultCoin.Amount.Uint64()))
	c.Check(pool.BalanceAsset.Equal(cosmos.NewUint(9999961476)), Equals, true, Commentf("%d", pool.BalanceAsset.Uint64()))
	c.Check(pool.BalanceRune.Equal(cosmos.NewUint(10000038524)), Equals, true, Commentf("%d", pool.BalanceRune.Uint64()))
}
