package tokenlist

import (
	"encoding/json"
	"os"

	. "gopkg.in/check.v1"
)

// TestSOLMainnetTokensHaveDepositFloor: every mainnet SPL token sets min_deposit.
// Bifrost does not observe deposits from outside the vaults below it, as with SOL's
// dust threshold; without one, a deposit costs only the transaction fee. Pick a
// floor worth about SOL's dust threshold (0.001 SOL). The mainnet list is read from
// its file, since a mocknet build embeds the mocknet list instead.
func (s *TokenListSuite) TestSOLMainnetTokensHaveDepositFloor(c *C) {
	raw, err := os.ReadFile("soltokens/sol_mainnet_latest.json")
	c.Assert(err, IsNil)
	var list EVMTokenList
	c.Assert(json.Unmarshal(raw, &list), IsNil)
	c.Assert(len(list.Tokens) > 0, Equals, true)
	for _, token := range list.Tokens {
		c.Check(token.MinDeposit > 0, Equals, true, Commentf("%s has no min_deposit", token.Symbol))
	}
}
