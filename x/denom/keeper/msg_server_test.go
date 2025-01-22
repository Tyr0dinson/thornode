package keeper_test

import (
	"gitlab.com/thorchain/thornode/v3/x/denom/types"
)

func (s *KeeperTestSuite) TestMsgServerCreateDenom() {
	s.SetupTest()

	resp, err := s.msgServer.CreateDenom(
		s.ctx,
		&types.MsgCreateDenom{
			Sender: accAddrs[0].String(),
			Id:     "foo",
		},
	)
	s.Require().NoError(err)
	denom, err := types.GetTokenDenom("foo")
	s.Require().NoError(err)
	s.Require().Equal(resp.NewTokenDenom, denom)

	queryRes, err := s.queryClient.DenomAdmin(s.ctx,
		&types.QueryDenomAdminRequest{
			Denom: denom,
		})
	s.Require().NoError(err)
	s.Require().Equal(
		accAddrs[0].String(),
		queryRes.Admin,
	)
}
