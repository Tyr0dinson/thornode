package thorchain

import (
	"errors"
	"fmt"

	"github.com/blang/semver"
	tmtypes "github.com/cometbft/cometbft/types"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type HandlerDepositCovCESuite struct{}

var _ = Suite(&HandlerDepositCovCESuite{})

func covCEDepositSetup(c *C) (cosmos.Context, *Mgrs, cosmos.AccAddress) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithTxBytes([]byte("covce-deposit-tx"))
	signer := GetRandomBech32Addr()
	FundAccount(c, ctx, mgr.Keeper(), signer, 100*common.One)
	return ctx, mgr, signer
}

func (s *HandlerDepositCovCESuite) TestBalanceChecksCovCE(c *C) {
	ctx, mgr, signer := covCEDepositSetup(c)
	h := NewDepositHandler(mgr)

	// trade and secured assets need an account balance
	for _, asset := range []common.Asset{common.BTCAsset.GetTradeAsset(), common.BTCAsset.GetSecuredAsset()} {
		msg := NewMsgDeposit(common.Coins{common.NewCoin(asset, cosmos.NewUint(1))}, "noop", signer)
		_, err := h.handle(ctx, *msg, 0)
		c.Check(errors.Is(err, se.ErrInsufficientFunds), Equals, true, Commentf("asset %s", asset))
	}

	// more RUNE than the account holds
	msg := NewMsgDeposit(common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(1000*common.One))}, "noop", signer)
	_, err := h.handle(ctx, *msg, 0)
	c.Check(errors.Is(err, se.ErrInsufficientFunds), Equals, true)

	// THORChain halted
	mgr.Keeper().SetMimir(ctx, "HaltTHORChain", 1)
	_, err = h.handle(ctx, *msg, 0)
	c.Check(err, ErrorMatches, "unable to use MsgDeposit while THORChain is halted")
}

func (s *HandlerDepositCovCESuite) TestMemoGuardsCovCE(c *C) {
	ctx, mgr, signer := covCEDepositSetup(c)
	h := NewDepositHandler(mgr)
	rune1 := common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(common.One))}

	// outbound memos are rejected
	_, err := h.handle(ctx, *NewMsgDeposit(rune1, "OUT:"+GetRandomTxHash().String(), signer), 0)
	c.Check(err, ErrorMatches, "cannot send inbound an outbound or internal transaction")

	// memoless deposit with a zero amount cannot derive a reference
	zero := common.Coins{common.NewCoin(common.RuneNative, cosmos.ZeroUint())}
	_, err = h.handle(ctx, *NewMsgDeposit(zero, "", signer), 0)
	c.Check(err, ErrorMatches, "memoless native deposit failed: zero amount.*")

	// non RUNE/TCY coins cannot be sent to the bond or TCY stake modules
	synth := common.BTCAsset.GetSyntheticAsset()
	coin := common.NewCoin(synth, cosmos.NewUint(common.One))
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToAccount(ctx, ModuleName, signer, common.NewCoins(coin)), IsNil)
	for memo, module := range map[string]string{
		"BOND:" + GetRandomBech32Addr().String(): BondName,
		"TCY+":                                   TCYStakeName,
	} {
		_, err = h.handle(ctx, *NewMsgDeposit(common.Coins{coin}, memo, signer), 0)
		c.Check(err, ErrorMatches, fmt.Sprintf(`\(.*\) memos are for the \(%s\) module.*`, module), Commentf("memo %s", memo))
	}
	// funds never left the account
	c.Check(mgr.Keeper().GetBalanceOfModule(ctx, BondName, synth.Native()).IsZero(), Equals, true)

	// swaps are rejected while trading is halted
	mgr.Keeper().SetMimir(ctx, "HaltTrading", 1)
	_, err = h.handle(ctx, *NewMsgDeposit(rune1, "=:BTC.BTC:"+GetRandomBTCAddress().String(), signer), 0)
	c.Check(err, ErrorMatches, "trading is halted")
}

func (s *HandlerDepositCovCESuite) TestTxIDCollisionsCovCE(c *C) {
	ctx, mgr, signer := covCEDepositSetup(c)
	h := NewDepositHandler(mgr)
	k := mgr.Keeper()

	hash := fmt.Sprintf("%X", tmtypes.Tx(ctx.TxBytes()).Hash())
	ids := []common.TxID{common.TxID(hash)}
	for i := 1; i <= 2; i++ {
		ids = append(ids, common.TxID(fmt.Sprintf("%s-%d", hash, i)))
	}
	occupy := func(id common.TxID) {
		tx := common.NewTx(id, GetRandomRUNEAddress(), GetRandomRUNEAddress(), common.Coins{}, common.Gas{}, "noop")
		k.SetObservedTxInVoter(ctx, NewObservedTxVoter(id, []common.ObservedTx{{Tx: tx}}))
	}

	k.SetMimir(ctx, constants.MaxDepositTxIDRetries.String(), 1)
	occupy(ids[0])
	occupy(ids[1])
	msg := NewMsgDeposit(common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(common.One))}, "RESERVE", signer)
	_, err := h.handle(ctx, *msg, 0)
	c.Check(err, ErrorMatches, fmt.Sprintf("txid: %s already exist after max retries \\(1\\)", ids[1]))

	// with a higher retry limit the next free index is used
	k.SetMimir(ctx, constants.MaxDepositTxIDRetries.String(), 5)
	_, err = h.handle(ctx, *msg, 0)
	c.Assert(err, IsNil)
	voter, err := k.GetObservedTxInVoter(ctx, ids[2])
	c.Assert(err, IsNil)
	c.Assert(voter.Txs, HasLen, 1)
	c.Check(voter.Tx.Tx.Memo, Equals, "RESERVE")
	// reserve contributions expect no outbound, so the voter is marked done
	c.Check(voter.FinalisedHeight, Equals, ctx.BlockHeight())
}

func (s *HandlerDepositCovCESuite) TestAnteHandlerCovCE(c *C) {
	ctx, mgr, signer := covCEDepositSetup(c)
	before := mgr.Keeper().GetBalance(ctx, signer).AmountOf(common.RuneNative.Native())
	msg := NewMsgDeposit(common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(common.One))}, "noop", signer)
	_, err := DepositAnteHandler(ctx, semver.MustParse("1.0.0"), mgr.Keeper(), *msg)
	c.Assert(err, IsNil)
	after := mgr.Keeper().GetBalance(ctx, signer).AmountOf(common.RuneNative.Native())
	fee := mgr.Keeper().GetNativeTxFee(ctx)
	c.Check(before.Sub(after).Uint64(), Equals, fee.Uint64())

	// an account with no funds cannot pay the fee
	empty := NewMsgDeposit(common.Coins{}, "noop", GetRandomBech32Addr())
	_, err = DepositAnteHandler(ctx, semver.MustParse("1.0.0"), mgr.Keeper(), *empty)
	c.Check(err, NotNil)
}
