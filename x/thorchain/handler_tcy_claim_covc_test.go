package thorchain

import (
	"errors"

	"cosmossdk.io/math"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tcysmartcontract"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerTCYClaimCovCESuite struct{}

var _ = Suite(&HandlerTCYClaimCovCESuite{})

func covCEFundTCYClaiming(c *C, ctx cosmos.Context, k keeper.Keeper, amt uint64) {
	coin := common.NewCoin(common.TCY, cosmos.NewUint(amt))
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, TCYClaimingName, common.NewCoins(coin)), IsNil)
}

func (s *HandlerTCYClaimCovCESuite) TestRunCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, "TCYClaimingHalt", 0)
	h := NewTCYClaimHandler(NewDummyMgrWithKeeper(k))

	_, err := h.Run(ctx, &types.MsgMimir{})
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)

	_, err = h.Run(ctx, &MsgTCYClaim{})
	c.Check(errors.Is(err, errTCYClaimFailValidation), Equals, true)

	runeAddr := GetRandomRUNEAddress()
	l1 := GetRandomBTCAddress()
	signer := cosmos.AccAddress(l1.String())

	// claiming module has no funds
	_, err = h.Run(ctx, NewMsgTCYClaim(runeAddr, l1, signer))
	c.Check(err, ErrorMatches, "claiming module doesn't have tcy funds")

	// claim to a smart contract address is rejected
	scAddrs, err := tcysmartcontract.GetTCYSmartContractAddresses()
	c.Assert(err, IsNil)
	_, err = h.Run(ctx, NewMsgTCYClaim(scAddrs[0], l1, signer))
	c.Check(err, ErrorMatches, "cannot claim to TCY smart contract address.*")

	covCEFundTCYClaiming(c, ctx, k, 100)
	c.Assert(k.SetTCYClaimer(ctx, types.TCYClaimer{L1Address: l1, Asset: common.BTCAsset, Amount: math.NewUint(40)}), IsNil)
	c.Assert(k.SetTCYClaimer(ctx, types.TCYClaimer{L1Address: l1, Asset: common.ETHAsset, Amount: math.NewUint(20)}), IsNil)

	res, err := h.Run(ctx, NewMsgTCYClaim(runeAddr, l1, signer))
	c.Assert(err, IsNil)
	c.Assert(res, NotNil)
	staker, err := k.GetTCYStaker(ctx, runeAddr)
	c.Assert(err, IsNil)
	c.Check(staker.Amount.Equal(math.NewUint(60)), Equals, true)
	c.Check(k.GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native()).Equal(math.NewUint(40)), Equals, true)
	_, err = k.GetTCYClaimer(ctx, l1, common.BTCAsset)
	c.Check(err, NotNil)
}

func (s *HandlerTCYClaimCovCESuite) TestHandleErrorsCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	runeAddr := GetRandomRUNEAddress()
	l1 := GetRandomBTCAddress()
	msg := NewMsgTCYClaim(runeAddr, l1, cosmos.AccAddress(l1.String()))

	covCEFundTCYClaiming(c, ctx, k, 50)
	// claim larger than the module balance
	c.Assert(k.SetTCYClaimer(ctx, types.TCYClaimer{L1Address: l1, Asset: common.BTCAsset, Amount: math.NewUint(80)}), IsNil)
	h := NewTCYClaimHandler(NewDummyMgrWithKeeper(k))
	_, err := h.handle(ctx, *msg)
	c.Check(err, ErrorMatches, "insufficient module balance for claim.*")
	// nothing moved, the claimer is still there
	_, err = k.GetTCYClaimer(ctx, l1, common.BTCAsset)
	c.Check(err, IsNil)

	c.Assert(k.SetTCYClaimer(ctx, types.TCYClaimer{L1Address: l1, Asset: common.BTCAsset, Amount: math.NewUint(10)}), IsNil)

	for _, tc := range []struct {
		method string
		msg    string
	}{
		{"ListTCYClaimersFromL1Address", "kaboom"},
		{"SendFromModuleToModule", "failed to send from claiming to staking module: kaboom"},
		{"UpdateTCYStaker", "failed to update tcy staker: kaboom"},
	} {
		h = NewTCYClaimHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, tc.method)))
		_, err = h.handle(ctx, *msg)
		c.Check(err, ErrorMatches, tc.msg, Commentf("method %s", tc.method))
	}
}
