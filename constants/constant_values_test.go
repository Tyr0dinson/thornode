package constants

import (
	"regexp"

	"github.com/blang/semver"
	. "gopkg.in/check.v1"
)

type ConstantsTestSuite struct{}

var _ = Suite(&ConstantsTestSuite{})

func (ConstantsTestSuite) TestConstantName_String(c *C) {
	constantNames := []ConstantName{
		EmissionCurve,
		BlocksPerYear,
		OutboundTransactionFee,
		PoolCycle,
		MinimumNodesForBFT,
		DesiredValidatorSet,
		ChurnInterval,
		LackOfObservationPenalty,
		SigningTransactionPeriod,
		DoubleSignMaxAge,
		MinimumBondInRune,
		ValidatorMaxRewardRatio,
		MinNodeChainSupport,
		ChainLiquidityRunePerValidator,
	}
	for _, item := range constantNames {
		c.Assert(item.String(), Not(Equals), "NA")
	}
}

func (ConstantsTestSuite) TestGetConstantValues(c *C) {
	ver := semver.MustParse("0.0.9")
	c.Assert(GetConstantValues(ver), NotNil)
	c.Assert(GetConstantValues(SWVersion), NotNil)
}

func (ConstantsTestSuite) TestChainSupportMimirDefaults(c *C) {
	vals := NewConstantValue()
	c.Assert(vals.GetInt64Value(MinNodeChainSupport), Equals, int64(1))
	c.Assert(vals.GetInt64Value(ChainLiquidityRunePerValidator), Equals, int64(100_000_00000000))
	c.Assert(vals.GetInt64Value(MinimumNodesForBFT), Equals, int64(4))
}

func (ConstantsTestSuite) TestAllConstantName(c *C) {
	keyRegex := regexp.MustCompile(MimirKeyRegex).MatchString
	for i := 0; i < len(_ConstantName_index)-1; i++ {
		key := ConstantName(i)
		if !keyRegex(key.String()) {
			c.Errorf("key:%s can't be used to set mimir", key)
		}
	}
}
