package thorchain

import (
	"errors"

	"github.com/blang/semver"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerNodePauseChainCovCESuite struct{}

var _ = Suite(&HandlerNodePauseChainCovCESuite{})

func (s *HandlerNodePauseChainCovCESuite) TestRunCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	h := NewNodePauseChainHandler(NewDummyMgrWithKeeper(k))

	_, err := h.Run(ctx, &types.MsgMimir{})
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)

	// non active signer is unauthorized
	_, err = h.Run(ctx, NewMsgNodePauseChain(1, GetRandomBech32Addr()))
	c.Check(errors.Is(err, se.ErrUnauthorized), Equals, true)

	// an active vault churned at height 10
	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.StatusSince = 10
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// resume while the chain is not paused is a no-op (no node record set)
	res, err := h.Run(ctx, NewMsgNodePauseChain(-1, na.NodeAddress))
	c.Assert(err, IsNil)
	c.Assert(res, NotNil)
	c.Check(k.GetNodePauseChain(ctx, na.NodeAddress), Equals, int64(-1))
	pause, err := k.GetMimir(ctx, "NodePauseChainGlobal")
	c.Assert(err, IsNil)
	c.Check(pause, Equals, int64(-1))

	// pause
	_, err = h.Run(ctx, NewMsgNodePauseChain(1, na.NodeAddress))
	c.Assert(err, IsNil)
	pause, err = k.GetMimir(ctx, "NodePauseChainGlobal")
	c.Assert(err, IsNil)
	c.Check(pause > ctx.BlockHeight(), Equals, true)

	// same node cannot vote again since the last churn
	_, err = h.Run(ctx, NewMsgNodePauseChain(1, na.NodeAddress))
	c.Check(err, ErrorMatches, "node has already chosen pause/resume since the last churn")

	// ante handler raises priority for active nodes and rejects others
	newCtx, err := NodePauseChainAnteHandler(ctx, semver.MustParse("1.0.0"), k, *NewMsgNodePauseChain(1, na.NodeAddress))
	c.Assert(err, IsNil)
	c.Check(newCtx.Priority(), Equals, ActiveNodePriority)
	_, err = NodePauseChainAnteHandler(ctx, semver.MustParse("1.0.0"), k, *NewMsgNodePauseChain(1, GetRandomBech32Addr()))
	c.Check(errors.Is(err, se.ErrUnauthorized), Equals, true)
}

func (s *HandlerNodePauseChainCovCESuite) TestHandleErrorsCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	msg := NewMsgNodePauseChain(1, na.NodeAddress)

	h := NewNodePauseChainHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, "GetAsgardVaultsByStatus")))
	c.Check(h.handle(ctx, *msg), ErrorMatches, "kaboom")

	h = NewNodePauseChainHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, "GetMimir")))
	c.Check(h.handle(ctx, *msg), ErrorMatches, "kaboom")
	c.Check(k.GetNodePauseChain(ctx, na.NodeAddress), Equals, int64(-1))
}
