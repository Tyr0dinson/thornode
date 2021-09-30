package thorchain

import (
	"errors"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

type HandlerSetNodeTypeSuite struct{}

type TestSetNodeTypeKeeper struct {
	keeper.KVStoreDummy
	na NodeAccount
}

func (k *TestSetNodeTypeKeeper) SendFromAccountToModule(ctx cosmos.Context, from cosmos.AccAddress, to string, coins common.Coins) error {
	return nil
}

func (k *TestSetNodeTypeKeeper) GetNodeAccount(ctx cosmos.Context, signer cosmos.AccAddress) (NodeAccount, error) {
	return k.na, nil
}

func (k *TestSetNodeTypeKeeper) GetNetwork(ctx cosmos.Context) (Network, error) {
	return NewNetwork(), nil
}

func (k *TestSetNodeTypeKeeper) SetNetwork(ctx cosmos.Context, data Network) error {
	return nil
}

func (k *TestSetNodeTypeKeeper) SetNodeAccount(ctx cosmos.Context, na NodeAccount) error {
	return nil
}

func (k *TestSetNodeTypeKeeper) SendFromModuleToModule(ctx cosmos.Context, from, to string, coins common.Coins) error {
	return nil
}

var _ = Suite(&HandlerSetNodeTypeSuite{})

func (s *HandlerSetNodeTypeSuite) TestValidate(c *C) {
	ctx, _ := setupKeeperForTest(c)

	keeper := &TestSetNodeTypeKeeper{
		na: GetRandomUnknownNode(NodeStandby),
	}
	keeper.na.PubKeySet = common.PubKeySet{}

	handler := NewSetNodeTypeHandler(NewDummyMgrWithKeeper(keeper))

	// happy path
	signer := GetRandomBech32Addr()
	c.Assert(signer.Empty(), Equals, false)

	msg := NewMsgSetNodeType(NodeTypeValidator, signer)
	err := handler.validate(ctx, *msg)
	c.Assert(err, IsNil)
	result, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// cannot set node type if already set
	keeper.na.Type = NodeTypeValidator
	msg = NewMsgSetNodeType(NodeTypeValidator, signer)
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	keeper.na.Type = NodeTypeLite
	msg = NewMsgSetNodeType(NodeTypeValidator, signer)
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
}

type TestSetNodeTypeHandleKeeper struct {
	keeper.Keeper
	failGetNodeAccount bool
	failSetNodeAccount bool
	failGetNetwork     bool
	failSetNetwork     bool
}

func NewTestSetNodeTypeHandleKeeper(k keeper.Keeper) *TestSetNodeTypeHandleKeeper {
	return &TestSetNodeTypeHandleKeeper{
		Keeper: k,
	}
}

func (k *TestSetNodeTypeHandleKeeper) SendFromAccountToModule(ctx cosmos.Context, from cosmos.AccAddress, to string, coins common.Coins) error {
	return nil
}

func (k *TestSetNodeTypeHandleKeeper) GetNodeAccount(ctx cosmos.Context, signer cosmos.AccAddress) (NodeAccount, error) {
	if k.failGetNodeAccount {
		return NodeAccount{}, kaboom
	}
	return k.Keeper.GetNodeAccount(ctx, signer)
}

func (k *TestSetNodeTypeHandleKeeper) SetNodeAccount(ctx cosmos.Context, na NodeAccount) error {
	if k.failSetNodeAccount {
		return kaboom
	}
	return k.Keeper.SetNodeAccount(ctx, na)
}

func (s *HandlerSetNodeTypeSuite) TestHandle(c *C) {
	ctx, mgr := setupManagerForTest(c)
	helper := NewTestSetNodeTypeHandleKeeper(mgr.Keeper())
	mgr.K = helper
	handler := NewSetNodeTypeHandler(mgr)
	ctx = ctx.WithBlockHeight(1)
	emptyPubKeySet := common.PubKeySet{}
	signer := GetRandomBech32Addr()

	bondAddr := GetRandomBNBAddress()

	msgSetNodeType := NewMsgSetNodeType(NodeTypeValidator, signer)

	bond := cosmos.NewUint(common.One * 100)
	nodeAccount := NewNodeAccount(signer, NodeActive, emptyPubKeySet, "", bond, bondAddr, common.BlockHeight(ctx))
	c.Assert(helper.Keeper.SetNodeAccount(ctx, nodeAccount), IsNil)

	nodeAccount = NewNodeAccount(signer, NodeWhiteListed, emptyPubKeySet, "", bond, bondAddr, common.BlockHeight(ctx))
	nodeAccount.Type = NodeTypeUnknown
	c.Assert(helper.Keeper.SetNodeAccount(ctx, nodeAccount), IsNil)
	FundModule(c, ctx, helper, BondName, common.One*100)

	// happy path
	_, err := handler.handle(ctx, *msgSetNodeType)
	c.Assert(err, IsNil)
	na, err := helper.Keeper.GetNodeAccount(ctx, msgSetNodeType.Signer)
	c.Assert(err, IsNil)
	c.Assert(na.StatusSince, Equals, int64(1))

	testCases := []struct {
		name              string
		messageProvider   func(c *C, ctx cosmos.Context, helper *TestSetNodeTypeHandleKeeper) cosmos.Msg
		validator         func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *TestSetNodeTypeHandleKeeper, name string)
		skipForNativeRune bool
	}{
		{
			name: "fail to get node account should return an error",
			messageProvider: func(c *C, ctx cosmos.Context, helper *TestSetNodeTypeHandleKeeper) cosmos.Msg {
				helper.failGetNodeAccount = true
				return NewMsgSetNodeType(NodeTypeLite, GetRandomBech32Addr())
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *TestSetNodeTypeHandleKeeper, name string) {
				c.Check(result, IsNil, Commentf(name))
				c.Check(err, NotNil, Commentf(name))
				c.Check(errors.Is(err, se.ErrUnauthorized), Equals, true)
			},
		},
		{
			name: "node account is empty should return an error",
			messageProvider: func(c *C, ctx cosmos.Context, helper *TestSetNodeTypeHandleKeeper) cosmos.Msg {
				return NewMsgSetNodeType(NodeTypeLite, GetRandomBech32Addr())
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *TestSetNodeTypeHandleKeeper, name string) {
				c.Check(result, IsNil, Commentf(name))
				c.Check(err, NotNil, Commentf(name))
				c.Check(errors.Is(err, se.ErrUnauthorized), Equals, true)
			},
		},
		{
			name: "fail to save node account should return an error",
			messageProvider: func(c *C, ctx cosmos.Context, helper *TestSetNodeTypeHandleKeeper) cosmos.Msg {
				nodeAcct := GetRandomUnknownNode(NodeWhiteListed)
				c.Assert(helper.Keeper.SetNodeAccount(ctx, nodeAcct), IsNil)
				helper.failSetNodeAccount = true
				return NewMsgSetNodeType(NodeTypeLite, GetRandomBech32Addr())
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *TestSetNodeTypeHandleKeeper, name string) {
				c.Check(result, IsNil, Commentf(name))
				c.Check(err, NotNil, Commentf(name))
			},
		},
	}
	for _, tc := range testCases {
		if common.RuneAsset().Native() != "" && tc.skipForNativeRune {
			continue
		}
		ctx, mgr := setupManagerForTest(c)
		helper := NewTestSetNodeTypeHandleKeeper(mgr.Keeper())
		mgr.K = helper
		handler := NewSetNodeTypeHandler(mgr)
		msg := tc.messageProvider(c, ctx, helper)
		result, err := handler.Run(ctx, msg)
		tc.validator(c, ctx, result, err, helper, tc.name)
	}
}
