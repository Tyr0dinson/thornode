package tcyclaimlist

import (
	. "gopkg.in/check.v1"
)

type TCYClaimListCovSuite struct{}

var _ = Suite(&TCYClaimListCovSuite{})

func (s *TCYClaimListCovSuite) TestMustParseClaims(c *C) {
	claims := mustParseClaims([]byte(`[{"asset":"btc.btc","address":"bc1qxyz","tcy_claim":42}]`))
	c.Assert(claims, DeepEquals, []TCYClaimsJSON{{Asset: "btc.btc", Address: "bc1qxyz", TCYClaim: 42}})

	c.Assert(mustParseClaims([]byte(`[]`)), HasLen, 0)

	// The embedded list decodes to exactly what init stored.
	c.Assert(mustParseClaims(TCYClaimsListRaw), DeepEquals, GetTCYClaimsList())

	c.Assert(func() { mustParseClaims([]byte(`{not json`)) }, PanicMatches, ".*invalid character.*")
	c.Assert(func() { mustParseClaims([]byte(`[{"tcy_claim":"nan"}]`)) }, PanicMatches, ".*cannot unmarshal.*")
}
