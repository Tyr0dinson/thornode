package thorchain

import (
	"errors"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/x/thorchain/types"
)

type HandlerUpgradeSuite struct{}

type TestUpgradeKeeper struct {
	keeper.KVStoreDummy
	na               NodeAccount
	failNodeAccount  NodeAccount
	emptyNodeAccount NodeAccount
	vaultNodeAccount NodeAccount
}

func (k *TestUpgradeKeeper) GetNodeAccount(_ cosmos.Context, addr cosmos.AccAddress) (NodeAccount, error) {
	if k.failNodeAccount.NodeAddress.Equals(addr) {
		return NodeAccount{}, errKaboom
	}
	if k.emptyNodeAccount.NodeAddress.Equals(addr) {
		return NodeAccount{}, nil
	}
	if k.vaultNodeAccount.NodeAddress.Equals(addr) {
		return NodeAccount{Type: NodeTypeVault}, nil
	}
	return k.na, nil
}

func (k *TestUpgradeKeeper) ProposeUpgrade(ctx cosmos.Context, name string, upgrade types.Upgrade) error {
	return nil
}

func (k *TestUpgradeKeeper) ApproveUpgrade(ctx cosmos.Context, addr cosmos.AccAddress, name string) error {
	return nil
}

func (k *TestUpgradeKeeper) RejectUpgrade(ctx cosmos.Context, addr cosmos.AccAddress, name string) error {
	return nil
}

var _ = Suite(&HandlerUpgradeSuite{})

func (s *HandlerUpgradeSuite) TestUpgrade(c *C) {
	ctx, _ := setupKeeperForTest(c)

	const (
		chainHeight   = 9000
		upgradeName   = "berserk"
		upgradeHeight = 10000
		upgradeInfo   = "scheduled upgrade"
	)

	ctx = ctx.WithBlockHeight(chainHeight)

	keeper := &TestUpgradeKeeper{
		na:               GetRandomValidatorNode(NodeActive),
		failNodeAccount:  GetRandomValidatorNode(NodeActive),
		emptyNodeAccount: GetRandomValidatorNode(NodeStandby),
		vaultNodeAccount: GetRandomVaultNode(NodeActive),
	}

	handler := NewProposeUpgradeHandler(NewDummyMgrWithKeeper(keeper))

	// invalid height
	msg := NewMsgProposeUpgrade(upgradeName, chainHeight, upgradeInfo, keeper.na.NodeAddress)
	result, err := handler.Run(ctx, msg)
	c.Assert(result, IsNil)
	c.Assert(err, NotNil)

	// invalid msg
	msg = &MsgProposeUpgrade{}
	result, err = handler.Run(ctx, msg)
	c.Assert(result, IsNil)
	c.Assert(err, NotNil)

	// fail to get node account should fail
	msg1 := NewMsgProposeUpgrade(upgradeName, upgradeHeight, upgradeInfo, keeper.failNodeAccount.NodeAddress)
	result, err = handler.Run(ctx, msg1)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// node account empty should fail
	msg2 := NewMsgProposeUpgrade(upgradeName, upgradeHeight, upgradeInfo, keeper.emptyNodeAccount.NodeAddress)
	result, err = handler.Run(ctx, msg2)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(errors.Is(err, se.ErrUnauthorized), Equals, true)

	// vault node should fail
	msg3 := NewMsgProposeUpgrade(upgradeName, upgradeHeight, upgradeInfo, keeper.vaultNodeAccount.NodeAddress)
	result, err = handler.Run(ctx, msg3)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(errors.Is(err, se.ErrUnauthorized), Equals, true)

	// happy path
	msg = NewMsgProposeUpgrade(upgradeName, upgradeHeight, upgradeInfo, keeper.na.NodeAddress)
	result, err = handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

}
