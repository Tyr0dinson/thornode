package thorchain

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	. "gopkg.in/check.v1"
)

type HandlerSolvencyTestSuite struct{}

var _ = Suite(&HandlerSolvencyTestSuite{})

func (s *HandlerSolvencyTestSuite) TestValidate(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewSolvencyHandler(mgr)
	// msgSolvency signed by  none active node should be rejected
	msgSolvency, err := NewMsgSolvency(common.ETHChain,
		GetRandomPubKey(),
		common.NewCoins(
			common.NewCoin(common.ETHAsset, cosmos.NewUint(1024)),
		),
		1024,
		GetRandomBech32Addr())
	c.Assert(err, IsNil)
	c.Assert(handler.validate(ctx, *msgSolvency), NotNil)
	// active node
}
