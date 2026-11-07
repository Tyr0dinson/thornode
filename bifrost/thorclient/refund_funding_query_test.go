package thorclient

import (
	"context"
	"strings"

	. "gopkg.in/check.v1"
)

func (s *FreshQuerySuite) TestFreshInboundRefundVoterCancelsStalledHTTP(c *C) {
	testFreshQueryCancelsStalledHTTP(c, func(ctx context.Context, bridge *thorchainBridge) error {
		_, _, err := bridge.GetRefundFundingVoter(ctx, strings.Repeat("A", 64))
		return err
	})
}
