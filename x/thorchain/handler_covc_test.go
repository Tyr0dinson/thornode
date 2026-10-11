package thorchain

import (
	"fmt"
	"reflect"

	"cosmossdk.io/math"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// covCEFailKeeper wraps a real keeper and returns errKaboom from the methods
// named in fail. For methods that support it, skip lets the first N calls
// succeed so a test can fail a later call, and calls counts every call.
type covCEFailKeeper struct {
	keeper.Keeper
	fail  map[string]bool
	skip  map[string]int
	calls map[string]int
}

func newCovCEFailKeeper(k keeper.Keeper, methods ...string) *covCEFailKeeper {
	f := &covCEFailKeeper{Keeper: k, fail: map[string]bool{}, skip: map[string]int{}, calls: map[string]int{}}
	for _, m := range methods {
		f.fail[m] = true
	}
	return f
}

// failNth reports whether this call of method should fail, honouring skip.
func (k *covCEFailKeeper) failNth(method string) bool {
	k.calls[method]++
	if !k.fail[method] {
		return false
	}
	if k.skip[method] > 0 {
		k.skip[method]--
		return false
	}
	return true
}

func (k *covCEFailKeeper) GetNodeAccount(ctx cosmos.Context, addr cosmos.AccAddress) (NodeAccount, error) {
	if k.fail["GetNodeAccount"] {
		return NodeAccount{}, errKaboom
	}
	return k.Keeper.GetNodeAccount(ctx, addr)
}

func (k *covCEFailKeeper) SetNodeAccount(ctx cosmos.Context, na NodeAccount) error {
	if k.failNth("SetNodeAccount") {
		return errKaboom
	}
	return k.Keeper.SetNodeAccount(ctx, na)
}

func (k *covCEFailKeeper) UpdateTCYStaker(ctx cosmos.Context, address common.Address, amount math.Uint) error {
	if k.fail["UpdateTCYStaker"] {
		return errKaboom
	}
	return k.Keeper.UpdateTCYStaker(ctx, address, amount)
}

func (k *covCEFailKeeper) ListTCYClaimersFromL1Address(ctx cosmos.Context, l1Address common.Address) ([]TCYClaimer, error) {
	if k.fail["ListTCYClaimersFromL1Address"] {
		return nil, errKaboom
	}
	return k.Keeper.ListTCYClaimersFromL1Address(ctx, l1Address)
}

func (k *covCEFailKeeper) SendFromModuleToModule(ctx cosmos.Context, from, to string, coins common.Coins) error {
	if k.fail["SendFromModuleToModule"] {
		return errKaboom
	}
	return k.Keeper.SendFromModuleToModule(ctx, from, to, coins)
}

func (k *covCEFailKeeper) GetAsgardVaultsByStatus(ctx cosmos.Context, status VaultStatus) (Vaults, error) {
	if k.fail["GetAsgardVaultsByStatus"] {
		return nil, errKaboom
	}
	return k.Keeper.GetAsgardVaultsByStatus(ctx, status)
}

func (k *covCEFailKeeper) GetMimir(ctx cosmos.Context, key string) (int64, error) {
	if k.fail["GetMimir"] {
		return 0, errKaboom
	}
	return k.Keeper.GetMimir(ctx, key)
}

func (k *covCEFailKeeper) GetBondProviders(ctx cosmos.Context, addr cosmos.AccAddress) (BondProviders, error) {
	if k.fail["GetBondProviders"] {
		return BondProviders{}, errKaboom
	}
	return k.Keeper.GetBondProviders(ctx, addr)
}

func (k *covCEFailKeeper) SetBondProviders(ctx cosmos.Context, bp BondProviders) error {
	if k.failNth("SetBondProviders") {
		return errKaboom
	}
	return k.Keeper.SetBondProviders(ctx, bp)
}

func (k *covCEFailKeeper) GetTxOut(ctx cosmos.Context, height int64) (*TxOut, error) {
	if k.fail["GetTxOut"] {
		return nil, errKaboom
	}
	return k.Keeper.GetTxOut(ctx, height)
}

func (k *covCEFailKeeper) SetTxOut(ctx cosmos.Context, blockOut *TxOut) error {
	if k.fail["SetTxOut"] {
		return errKaboom
	}
	return k.Keeper.SetTxOut(ctx, blockOut)
}

func (k *covCEFailKeeper) SetNodeMimir(ctx cosmos.Context, key string, value int64, acc cosmos.AccAddress) error {
	if k.fail["SetNodeMimir"] {
		return errKaboom
	}
	return k.Keeper.SetNodeMimir(ctx, key, value, acc)
}

func (k *covCEFailKeeper) GetNodeMimirs(ctx cosmos.Context, key string) (NodeMimirs, error) {
	if k.fail["GetNodeMimirs"] {
		return NodeMimirs{}, errKaboom
	}
	return k.Keeper.GetNodeMimirs(ctx, key)
}

func (k *covCEFailKeeper) ListActiveValidators(ctx cosmos.Context) (NodeAccounts, error) {
	if k.fail["ListActiveValidators"] {
		return nil, errKaboom
	}
	return k.Keeper.ListActiveValidators(ctx)
}

func (k *covCEFailKeeper) GetAdvSwapQueueIndex(ctx cosmos.Context, msg MsgSwap) ([]types.AdvSwapQueueIndexItem, error) {
	if k.fail["GetAdvSwapQueueIndex"] {
		return nil, errKaboom
	}
	return k.Keeper.GetAdvSwapQueueIndex(ctx, msg)
}

func (k *covCEFailKeeper) GetAdvSwapQueueItem(ctx cosmos.Context, txID common.TxID, index int) (MsgSwap, error) {
	if k.fail["GetAdvSwapQueueItem"] {
		return MsgSwap{}, errKaboom
	}
	return k.Keeper.GetAdvSwapQueueItem(ctx, txID, index)
}

func (k *covCEFailKeeper) RemoveAdvSwapQueueIndex(ctx cosmos.Context, msg MsgSwap) error {
	if k.fail["RemoveAdvSwapQueueIndex"] {
		return errKaboom
	}
	return k.Keeper.RemoveAdvSwapQueueIndex(ctx, msg)
}

func (k *covCEFailKeeper) SetAdvSwapQueueItem(ctx cosmos.Context, msg MsgSwap) error {
	if k.fail["SetAdvSwapQueueItem"] {
		return errKaboom
	}
	return k.Keeper.SetAdvSwapQueueItem(ctx, msg)
}

func (k *covCEFailKeeper) GetPool(ctx cosmos.Context, asset common.Asset) (Pool, error) {
	if k.fail["GetPool"] {
		return Pool{}, errKaboom
	}
	return k.Keeper.GetPool(ctx, asset)
}

func (k *covCEFailKeeper) SetPool(ctx cosmos.Context, pool Pool) error {
	if k.fail["SetPool"] {
		return errKaboom
	}
	return k.Keeper.SetPool(ctx, pool)
}

func (k *covCEFailKeeper) SendFromModuleToAccount(ctx cosmos.Context, from string, to cosmos.AccAddress, coins common.Coins) error {
	if k.fail["SendFromModuleToAccount"] {
		return errKaboom
	}
	return k.Keeper.SendFromModuleToAccount(ctx, from, to, coins)
}

func (k *covCEFailKeeper) GetPools(ctx cosmos.Context) (Pools, error) {
	if k.fail["GetPools"] {
		return nil, errKaboom
	}
	return k.Keeper.GetPools(ctx)
}

// covCEFailEventMgr fails the n-th (1-based) EmitEvent call; other calls are
// forwarded.
type covCEFailEventMgr struct {
	EventManager
	failOn int
	calls  int
}

func (e *covCEFailEventMgr) EmitEvent(ctx cosmos.Context, evt EmitEventItem) error {
	e.calls++
	if e.calls == e.failOn {
		return errKaboom
	}
	return e.EventManager.EmitEvent(ctx, evt)
}

func (k *covCEFailKeeper) SetLastSignedHeight(ctx cosmos.Context, height int64) error {
	if k.fail["SetLastSignedHeight"] {
		return errKaboom
	}
	return k.Keeper.SetLastSignedHeight(ctx, height)
}

type HandlerProcessTxInCovCESuite struct{}

var _ = Suite(&HandlerProcessTxInCovCESuite{})

// covCEUnregisteredMsg is a cosmos.Msg that has no internal handler.
type covCEUnregisteredMsg struct{ types.MsgEmpty }

func (s *HandlerProcessTxInCovCESuite) TestInternalHandlerUnknownMsgCovCE(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewInternalHandler(mgr)
	_, err := h(ctx, &covCEUnregisteredMsg{})
	c.Check(err, ErrorMatches, "Unrecognized thorchain Msg type.*")
}

func (s *HandlerProcessTxInCovCESuite) TestProcessOneTxInMemoMappingCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	signer := GetRandomBech32Addr()
	thorAddr := GetRandomRUNEAddress()
	nodeAddr := GetRandomBech32Addr()
	btcAddr := GetRandomBTCAddress()

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	runeCoin := common.NewCoin(common.RuneNative, cosmos.NewUint(common.One))
	tradeCoin := common.NewCoin(common.BTCAsset.GetTradeAsset(), cosmos.NewUint(common.One))
	secCoin := common.NewCoin(common.BTCAsset.GetSecuredAsset(), cosmos.NewUint(common.One))
	tcyCoin := common.NewCoin(common.TCY, cosmos.NewUint(common.One))
	btcCoin := common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))

	cases := []struct {
		memo string
		coin common.Coin
		from common.Address
		want any
	}{
		{"m=<:25BTC.BTC:1003ETH.ETH:900", runeCoin, thorAddr, &MsgModifyLimitSwap{}},
		{"donate:BTC.BTC", runeCoin, thorAddr, &MsgDonate{}},
		{fmt.Sprintf("BONDYIELD:%s:2500", nodeAddr), runeCoin, thorAddr, &MsgBondYield{}},
		{"noop", runeCoin, thorAddr, &MsgNoOp{}},
		{"consolidate", btcCoin, btcAddr, &MsgConsolidate{}},
		{"reference:BTC.BTC:=:THOR.RUNE:" + thorAddr.String(), runeCoin, thorAddr, &MsgReferenceMemo{}},
		{"trade+:" + signer.String(), btcCoin, btcAddr, &MsgTradeAccountDeposit{}},
		{"trade-:" + btcAddr.String(), tradeCoin, thorAddr, &MsgTradeAccountWithdrawal{}},
		{"secure+:" + signer.String(), btcCoin, btcAddr, &MsgSecuredAssetDeposit{}},
		{"secure-:" + btcAddr.String(), secCoin, thorAddr, &MsgSecuredAssetWithdraw{}},
		{"pool+", runeCoin, thorAddr, &MsgRunePoolDeposit{}},
		{"pool-:5000", runeCoin, thorAddr, &MsgRunePoolWithdraw{}},
		{"switch:" + signer.String(), btcCoin, btcAddr, &MsgSwitch{}},
		{"tcy:" + thorAddr.String(), runeCoin, btcAddr, &MsgTCYClaim{}},
		{"tcy+", tcyCoin, thorAddr, &MsgTCYStake{}},
		{"tcy-:5000", runeCoin, thorAddr, &MsgTCYUnstake{}},
		{"maint:" + nodeAddr.String(), runeCoin, thorAddr, &types.MsgMaint{}},
		{"operator:" + signer.String(), runeCoin, thorAddr, &MsgOperatorRotate{}},
		{"stable-:12345678", common.NewCoin(common.BTCAsset, cosmos.NewUint(1)), btcAddr, &MsgSecuredAssetWithdraw{}},
	}
	for _, tc := range cases {
		tx := NewObservedTx(common.NewTx(GetRandomTxHash(), tc.from, GetRandomRUNEAddress(),
			common.Coins{tc.coin}, common.Gas{}, tc.memo), 1, GetRandomPubKey(), 1)
		tx.Tx.Chain = tc.coin.Asset.GetChain()
		msg, err := processOneTxIn(ctx, k, tx, signer)
		c.Assert(msg, NotNil, Commentf("memo %s err %v", tc.memo, err))
		c.Check(reflect.TypeOf(msg), Equals, reflect.TypeOf(tc.want), Commentf("memo %s", tc.memo))
	}

	// THORName without coins is rejected by the helper
	_, err := getMsgManageTHORNameFromMemo(ManageTHORNameMemo{}, ObservedTx{}, signer)
	c.Check(err, ErrorMatches, "transaction must have rune in it")

	// multiple coins are rejected
	tx := NewObservedTx(common.NewTx(GetRandomTxHash(), thorAddr, thorAddr,
		common.Coins{runeCoin, tcyCoin}, common.Gas{}, "noop"), 1, GetRandomPubKey(), 1)
	_, err = processOneTxIn(ctx, k, tx, signer)
	c.Check(err, ErrorMatches, ".*only send 1 coins per message.*")

	// memos without a matching message type are invalid inbound memos
	tx = NewObservedTx(common.NewTx(GetRandomTxHash(), thorAddr, thorAddr,
		common.Coins{runeCoin}, common.Gas{}, "r:12345"), 1, GetRandomPubKey(), 1)
	_, err = processOneTxIn(ctx, k, tx, signer)
	c.Check(err, NotNil)
}

func (s *HandlerProcessTxInCovCESuite) TestModifyLimitSwapMsgFromMemoCovCE(c *C) {
	signer := GetRandomBech32Addr()
	from := GetRandomBTCAddress()
	memo := ModifyLimitSwapMemo{
		Source:               common.NewCoin(common.BTCAsset, cosmos.NewUint(25)),
		Target:               common.NewCoin(common.ETHAsset, cosmos.NewUint(1003)),
		ModifiedTargetAmount: cosmos.NewUint(900),
	}

	// no coins: deposit fields empty
	msg, err := getMsgModifyLimitSwap(memo, ObservedTx{Tx: common.Tx{FromAddress: from}}, signer)
	c.Assert(err, IsNil)
	m, ok := msg.(*MsgModifyLimitSwap)
	c.Assert(ok, Equals, true)
	c.Check(m.DepositAsset.IsEmpty(), Equals, true)
	c.Check(m.ModifiedTargetAmount.Uint64(), Equals, uint64(900))
	c.Check(m.From.Equals(from), Equals, true)

	// with a coin: deposit fields copied from the tx
	tx := ObservedTx{Tx: common.Tx{FromAddress: from, Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(7))}}}
	msg, err = getMsgModifyLimitSwap(memo, tx, signer)
	c.Assert(err, IsNil)
	m, ok = msg.(*MsgModifyLimitSwap)
	c.Assert(ok, Equals, true)
	c.Check(m.DepositAsset.Equals(common.BTCAsset), Equals, true)
	c.Check(m.DepositAmount.Uint64(), Equals, uint64(7))
}
