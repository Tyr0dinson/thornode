package xrp

import (
	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

type UtilTestSuite struct{}

var _ = Suite(&UtilTestSuite{})

func (s *UtilTestSuite) SetUpSuite(c *C) {}

func (s *UtilTestSuite) TestFromXrpToThorchain(c *C) {
	// 5 XRP, 6 decimals
	xrpCoin := common.NewCoin(common.XRPAsset, sdkmath.NewUint(5000000))
	thorchainCoin, err := fromXrpToThorchain(xrpCoin)
	c.Assert(err, IsNil)

	// 5 XRP, 8 decimals
	expectedCoin := common.Coin{
		Asset:    common.XRPAsset,
		Amount:   sdkmath.NewUint(500000000),
		Decimals: 6,
	}
	c.Check(thorchainCoin.Asset.Equals(expectedCoin.Asset), Equals, true)
	c.Check(thorchainCoin.Amount.String(), Equals, expectedCoin.Amount.String())
	c.Check(thorchainCoin.Decimals, Equals, expectedCoin.Decimals)
}

func (s *UtilTestSuite) TestFromThorchainToXrp(c *C) {
	// 6 XRP, 8 decimals
	thorchainCoin := common.NewCoin(common.XRPAsset, sdkmath.NewUint(600000000))
	xrpCoin, err := fromThorchainToXrp(thorchainCoin)
	c.Assert(err, IsNil)

	// 6 XRP, 6 decimals
	expectedCoin := common.Coin{
		Asset:    common.XRPAsset,
		Amount:   sdkmath.NewUint(6000000),
		Decimals: 6,
	}
	c.Check(xrpCoin.Asset, Equals, expectedCoin.Asset)
	c.Check(xrpCoin.Amount.String(), Equals, expectedCoin.Amount.String())
	c.Check(xrpCoin.Decimals, Equals, expectedCoin.Decimals)
}
