package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

type HandlerManageTHORNameSuite struct{}

var _ = Suite(&HandlerManageTHORNameSuite{})

type KeeperManageTHORNameTest struct {
	keeper.Keeper
}

func NewKeeperManageTHORNameTest(k keeper.Keeper) KeeperManageTHORNameTest {
	return KeeperManageTHORNameTest{Keeper: k}
}

func (s *HandlerManageTHORNameSuite) TestValidator(c *C) {
	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)
	ctx, k := setupKeeperForTest(c)

	mgr := NewManagers(k)
	c.Assert(mgr.BeginBlock(ctx), IsNil)
	handler := NewManageTHORNameHandler(k, mgr)
	coin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))
	addr := GetRandomTHORAddress()
	acc, _ := addr.AccAddress()
	name := NewTHORName("hello", common.THORChain, addr, 50)
	k.SetTHORName(ctx, name)

	// happy path
	msg := NewMsgManageTHORName("I-am_the_99th_walrus+", common.THORChain, addr, coin, 0, acc)
	c.Assert(handler.validate(ctx, *msg, ver, constAccessor), IsNil)

	// fail: name is too long
	msg.Name = "this_name_is_way_too_long_to_be_a_valid_name"
	c.Assert(handler.validate(ctx, *msg, ver, constAccessor), NotNil)

	// fail: bad characters
	msg.Name = "i am the walrus"
	c.Assert(handler.validate(ctx, *msg, ver, constAccessor), NotNil)

	// fail: bad attempt to inflate expire block height
	msg.Name = "hello"
	msg.ExpireBlockHeight = 100
	c.Assert(handler.validate(ctx, *msg, ver, constAccessor), NotNil)

	// fail: bad auth
	msg.ExpireBlockHeight = 0
	msg.Signer = GetRandomBech32Addr()
	c.Assert(handler.validate(ctx, *msg, ver, constAccessor), NotNil)

	// fail: not enough funds for new THORName
	msg.Name = "bang"
	msg.Coin.Amount = cosmos.ZeroUint()
	c.Assert(handler.validate(ctx, *msg, ver, constAccessor), NotNil)
}
