package thorchain

import (
	"errors"

	"github.com/blang/semver"
	sdk "github.com/cosmos/cosmos-sdk/types"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerVersionCovCESuite struct{}

var _ = Suite(&HandlerVersionCovCESuite{})

func (s *HandlerVersionCovCESuite) TestRunAndValidateCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	na.Version = "1.0.0"
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	h := NewVersionHandler(NewDummyMgrWithKeeper(k))

	_, err := h.Run(ctx, &types.MsgMimir{})
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)

	c.Check(h.validate(ctx, *NewMsgSetVersion("not-a-version", na.NodeAddress)), ErrorMatches, ".*No Major.Minor.Patch elements found.*")
	c.Check(h.validate(ctx, *NewMsgSetVersion("2.0.0-rc1", na.NodeAddress)), ErrorMatches, ".*doesn't use Pre/Build version.*")
	c.Check(h.validate(ctx, *NewMsgSetVersion("2.0.0+build1", na.NodeAddress)), ErrorMatches, ".*doesn't use Pre/Build version.*")

	// vault type nodes cannot set versions
	vaultNode := GetRandomVaultNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, vaultNode), IsNil)
	err = h.validate(ctx, *NewMsgSetVersion("2.0.0", vaultNode.NodeAddress))
	c.Check(errors.Is(err, se.ErrUnauthorized), Equals, true)

	// happy path: version bumped and event emitted
	ctx = ctx.WithEventManager(sdk.NewEventManager())
	_, err = h.Run(ctx, NewMsgSetVersion("2.0.0", na.NodeAddress))
	c.Assert(err, IsNil)
	got, err := k.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.Version, Equals, "2.0.0")
	found := false
	for _, evt := range ctx.EventManager().Events() {
		if evt.Type == "set_version" {
			found = true
		}
	}
	c.Check(found, Equals, true)

	// a lower version never downgrades
	_, err = h.Run(ctx, NewMsgSetVersion("1.5.0", na.NodeAddress))
	c.Assert(err, IsNil)
	got, err = k.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.Version, Equals, "2.0.0")
}

func (s *HandlerVersionCovCESuite) TestHandleErrorsCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	msg := NewMsgSetVersion("2.0.0", na.NodeAddress)

	h := NewVersionHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, "GetNodeAccount")))
	c.Check(errors.Is(h.handle(ctx, *msg), se.ErrUnauthorized), Equals, true)
	c.Check(errors.Is(validateVersionAuth(ctx, newCovCEFailKeeper(k, "GetNodeAccount"), na.NodeAddress), se.ErrUnauthorized), Equals, true)

	h = NewVersionHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, "SetNodeAccount")))
	c.Check(h.handle(ctx, *msg), ErrorMatches, "fail to save node account: kaboom")

	h = NewVersionHandler(NewDummyMgrWithKeeper(k))
	c.Check(h.handle(ctx, *NewMsgSetVersion("bogus", na.NodeAddress)), ErrorMatches, "fail to parse version.*")
}

func (s *HandlerVersionCovCESuite) TestAnteHandlerCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	FundModule(c, ctx, k, BondName, 100*common.One)

	_, err := VersionAnteHandler(ctx, semver.MustParse("1.0.0"), k, *NewMsgSetVersion("2.0.0", na.NodeAddress))
	c.Assert(err, IsNil)
	got, err := k.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	fee := k.GetNativeTxFee(ctx)
	c.Check(got.Bond.Equal(cosmos.NewUint(100*common.One).Sub(fee)), Equals, true)

	_, err = VersionAnteHandler(ctx, semver.MustParse("1.0.0"), k, *NewMsgSetVersion("2.0.0", GetRandomBech32Addr()))
	c.Check(errors.Is(err, se.ErrUnauthorized), Equals, true)
}
