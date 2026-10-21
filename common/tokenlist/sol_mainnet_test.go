//go:build !mocknet
// +build !mocknet

package tokenlist

import (
	. "gopkg.in/check.v1"
)

// TestSOLMainnetTokensHaveDepositFloor: every mainnet SPL token sets min_deposit.
// Bifrost does not observe deposits from outside the vaults below it, as with SOL's
// dust threshold; without one, a deposit costs only the transaction fee. Pick a
// floor worth about SOL's dust threshold (0.001 SOL).
func (s *TokenListSuite) TestSOLMainnetTokensHaveDepositFloor(c *C) {
	tokens := GetSOLTokenList().Tokens
	c.Assert(len(tokens) > 0, Equals, true)
	for _, token := range tokens {
		c.Check(token.MinDeposit > 0, Equals, true, Commentf("%s has no min_deposit", token.Symbol))
	}
}
