package thorchain

import (
	"errors"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerSecuredAssetWithdrawCovCESuite struct{}

var _ = Suite(&HandlerSecuredAssetWithdrawCovCESuite{})

// covCEStableReserve stubs StableReserveManager.Withdraw.
type covCEStableReserve struct {
	StableReserveManager
	coin   common.Coin
	filled bool
	err    error
	calls  int
}

func (s *covCEStableReserve) Withdraw(_ cosmos.Context, _ common.Asset, _ cosmos.Uint, _ common.Address, _ common.TxID) (common.Coin, bool, error) {
	s.calls++
	return s.coin, s.filled, s.err
}

// covCETxOutStore stubs TryAddTxOutItem and records the items it receives.
type covCETxOutStore struct {
	TxOutStore
	ok    bool
	err   error
	items []TxOutItem
}

func (t *covCETxOutStore) TryAddTxOutItem(_ cosmos.Context, _ Manager, toi TxOutItem, _ cosmos.Uint) (bool, error) {
	t.items = append(t.items, toi)
	return t.ok, t.err
}

func covCEStableWithdrawMsg(coins common.Coins) *MsgSecuredAssetWithdraw {
	tx := common.Tx{ID: GetRandomTxHash(), FromAddress: GetRandomRUNEAddress(), Coins: coins, Memo: "STABLE-"}
	return NewMsgSecuredAssetWithdraw(common.BTCAsset.GetSecuredAsset(), cosmos.NewUint(100), GetRandomBTCAddress(), GetRandomBech32Addr(), tx)
}

func (s *HandlerSecuredAssetWithdrawCovCESuite) TestValidateCovCE(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSecuredAssetWithdrawHandler(mgr)

	_, err := h.Run(ctx, &types.MsgMimir{})
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)

	msg := covCEStableWithdrawMsg(common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(1))})
	mgr.Keeper().SetMimir(ctx, "HaltSecuredWithdraw-BTC", 1)
	_, err = h.Run(ctx, msg)
	c.Check(err, ErrorMatches, "BTC secured asset withdrawals are disabled")

	// a halt scheduled in the future is not yet in effect
	mgr.Keeper().SetMimir(ctx, "HaltSecuredWithdraw-BTC", ctx.BlockHeight()+10)
	c.Check(h.validate(ctx, *msg), IsNil)
}

func (s *HandlerSecuredAssetWithdrawCovCESuite) TestStableReserveWithdrawCovCE(c *C) {
	ctx, mgr := setupManagerForTest(c)
	sr := &covCEStableReserve{StableReserveManager: mgr.stableReserveManager}
	mgr.stableReserveManager = sr
	txo := &covCETxOutStore{TxOutStore: mgr.txOutStore, ok: true}
	mgr.txOutStore = txo
	h := NewSecuredAssetWithdrawHandler(mgr)
	reqCoin := common.NewCoin(common.RuneNative, cosmos.NewUint(5000))

	// more than one request coin
	err := h.handle(ctx, *covCEStableWithdrawMsg(common.Coins{reqCoin, common.NewCoin(common.TCY, cosmos.NewUint(1))}))
	c.Check(err, ErrorMatches, ".*only send 1 coins per message.*")

	// oversized request coin
	err = h.handle(ctx, *covCEStableWithdrawMsg(common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(10_001))}))
	c.Check(err, ErrorMatches, "stable reserve withdrawal request coin too large")
	c.Check(sr.calls, Equals, 0)

	// manager error
	sr.err = errKaboom
	err = h.handle(ctx, *covCEStableWithdrawMsg(common.Coins{reqCoin}))
	c.Check(err, Equals, errKaboom)
	sr.err = nil

	// unfilled: no fee swap, no outbound
	msg := covCEStableWithdrawMsg(common.Coins{reqCoin})
	err = h.handle(ctx, *msg)
	c.Check(err, ErrorMatches, "stable reserve withdrawal unfilled")
	feeID := stableReserveSyntheticTxID(ctx, "withdraw-fee-"+msg.Tx.ID.String(), reqCoin.Asset)
	_, err = mgr.Keeper().GetSwapQueueItem(ctx, feeID, 0)
	c.Check(err, NotNil)
	c.Check(txo.items, HasLen, 0)

	// filled: fee swap is queued and the outbound scheduled
	sr.filled = true
	sr.coin = common.NewCoin(common.BTCAsset, cosmos.NewUint(99))
	c.Assert(h.handle(ctx, *msg), IsNil)
	swap, err := mgr.Keeper().GetSwapQueueItem(ctx, feeID, 0)
	c.Assert(err, IsNil)
	c.Check(swap.TargetAsset.Equals(common.RuneAsset()), Equals, true)
	c.Check(swap.Tx.Coins.EqualsEx(common.NewCoins(reqCoin)), Equals, true)
	reserve, err := mgr.Keeper().GetModuleAddress(ReserveName)
	c.Assert(err, IsNil)
	c.Check(swap.Destination.Equals(reserve), Equals, true)
	c.Assert(txo.items, HasLen, 1)
	c.Check(txo.items[0].Coin.Equals(sr.coin), Equals, true)
	c.Check(txo.items[0].ToAddress.Equals(msg.AssetAddress), Equals, true)
	c.Check(txo.items[0].InHash.Equals(msg.Tx.ID), Equals, true)

	// a zero request coin skips the fee swap
	zmsg := covCEStableWithdrawMsg(common.Coins{common.NewCoin(common.RuneNative, cosmos.ZeroUint())})
	c.Assert(h.handle(ctx, *zmsg), IsNil)
	zfeeID := stableReserveSyntheticTxID(ctx, "withdraw-fee-"+zmsg.Tx.ID.String(), common.RuneNative)
	_, err = mgr.Keeper().GetSwapQueueItem(ctx, zfeeID, 0)
	c.Check(err, NotNil)
	c.Check(txo.items, HasLen, 2)
}

func (s *HandlerSecuredAssetWithdrawCovCESuite) TestOutboundFailuresCovCE(c *C) {
	ctx, mgr := setupManagerForTest(c)
	msg := covCEStableWithdrawMsg(nil)
	coin := common.NewCoin(common.BTCAsset, cosmos.NewUint(10))

	txo := &covCETxOutStore{TxOutStore: mgr.txOutStore, ok: false}
	mgr.txOutStore = txo
	err := addSecuredAssetWithdrawOutbound(ctx, mgr, *msg, coin)
	c.Check(err, Equals, errFailAddOutboundTx)

	txo.err = errKaboom
	err = addSecuredAssetWithdrawOutbound(ctx, mgr, *msg, coin)
	c.Check(err, ErrorMatches, "(?s).*kaboom.*")
	c.Check(errors.Is(err, errFailAddOutboundTx), Equals, true)

	// the regular (non stable reserve) path surfaces secured asset manager errors
	msg.Tx.Memo = "SECURE-:" + msg.AssetAddress.String()
	err = NewSecuredAssetWithdrawHandler(mgr).handle(ctx, *msg)
	c.Check(err, NotNil)
	c.Check(txo.items, HasLen, 2)
}
