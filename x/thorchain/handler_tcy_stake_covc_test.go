package thorchain

import (
	"errors"

	"cosmossdk.io/math"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tcysmartcontract"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerTCYStakeCovCESuite struct{}

var _ = Suite(&HandlerTCYStakeCovCESuite{})

func covCETCYStakeMsg(c *C, from common.Address, amt uint64) *MsgTCYStake {
	signer, err := GetRandomRUNEAddress().AccAddress()
	c.Assert(err, IsNil)
	tx := common.NewTx(GetRandomTxHash(), from, GetRandomRUNEAddress(),
		common.NewCoins(common.NewCoin(common.TCY, cosmos.NewUint(amt))), common.Gas{}, "")
	tx.Chain = common.THORChain
	return NewMsgTCYStake(tx, signer)
}

func (s *HandlerTCYStakeCovCESuite) TestRunCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, "TCYStakingHalt", 0)
	h := NewTCYStakeHandler(NewDummyMgrWithKeeper(k))

	_, err := h.Run(ctx, &types.MsgMimir{})
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)

	// invalid msg fails validation
	_, err = h.Run(ctx, &MsgTCYStake{})
	c.Check(errors.Is(err, errTCYStakeFailValidation), Equals, true)

	// happy path: staking twice accumulates the staked amount
	from := GetRandomRUNEAddress()
	res, err := h.Run(ctx, covCETCYStakeMsg(c, from, 100))
	c.Assert(err, IsNil)
	c.Assert(res, NotNil)
	_, err = h.Run(ctx, covCETCYStakeMsg(c, from, 50))
	c.Assert(err, IsNil)
	staker, err := k.GetTCYStaker(ctx, from)
	c.Assert(err, IsNil)
	c.Check(staker.Amount.Equal(math.NewUint(150)), Equals, true)

	// staking halted
	k.SetMimir(ctx, "TCYStakingHalt", 1)
	_, err = h.Run(ctx, covCETCYStakeMsg(c, from, 10))
	c.Check(err, ErrorMatches, "tcy staking is halted")
	k.SetMimir(ctx, "TCYStakingHalt", 0)

	// smart contract addresses cannot stake
	scAddrs, err := tcysmartcontract.GetTCYSmartContractAddresses()
	c.Assert(err, IsNil)
	c.Assert(len(scAddrs) > 0, Equals, true)
	_, err = h.Run(ctx, covCETCYStakeMsg(c, scAddrs[0], 10))
	c.Check(err, ErrorMatches, "cannot stake from TCY smart contract address.*")

	// keeper failure surfaces from handle
	h = NewTCYStakeHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, "UpdateTCYStaker")))
	_, err = h.Run(ctx, covCETCYStakeMsg(c, from, 10))
	c.Check(err, ErrorMatches, "failed to update tcy staker: kaboom")
	staker, err = k.GetTCYStaker(ctx, from)
	c.Assert(err, IsNil)
	c.Check(staker.Amount.Equal(math.NewUint(150)), Equals, true)
}
