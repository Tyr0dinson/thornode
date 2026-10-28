package main

import (
	"time"

	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type KeygenTimeoutSuite struct{}

var _ = Suite(&KeygenTimeoutSuite{})

func (s *KeygenTimeoutSuite) TestCheckKeygenTimeout(c *C) {
	newConsts := func(jailTimeKeygen, jailTimeKeygenBase int64) constants.ConstantValues {
		return constants.NewDummyConstants(map[constants.ConstantName]int64{
			constants.JailTimeKeygen:     jailTimeKeygen,
			constants.JailTimeKeygenBase: jailTimeKeygenBase,
		}, nil, nil)
	}
	block := constants.ThorchainBlockTime

	// Shorter than both jail times.
	c.Check(checkKeygenTimeout(9*block, newConsts(10, 10)), IsNil)

	// Not shorter than JailTimeKeygen.
	err := checkKeygenTimeout(10*block, newConsts(10, 20))
	c.Assert(err, NotNil)
	c.Check(err, ErrorMatches, ".*JailTimeKeygen .*")

	// Shorter than JailTimeKeygen but not than JailTimeKeygenBase.
	err = checkKeygenTimeout(15*block, newConsts(20, 10))
	c.Assert(err, NotNil)
	c.Check(err, ErrorMatches, ".*JailTimeKeygenBase.*")

	// The shipped constants accept a timeout just under the shortest jail.
	consts := constants.NewConstantValue()
	shortest := min(consts.GetInt64Value(constants.JailTimeKeygen), consts.GetInt64Value(constants.JailTimeKeygenBase))
	c.Check(checkKeygenTimeout(time.Duration(shortest)*block-time.Nanosecond, consts), IsNil)
}
