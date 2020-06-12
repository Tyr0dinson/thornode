package types

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common"
)

type LedgerVoterSuite struct{}

var _ = Suite(&LedgerVoterSuite{})

func (s *LedgerVoterSuite) TestVoter(c *C) {
	ledger := NewLedgerVoter("hello", common.BNBChain, 400)
	c.Check(ledger.Empty(), Equals, false)

	addr := GetRandomBech32Addr()
	c.Check(ledger.HasSigned(addr), Equals, false)
	ledger.Sign(addr)
	c.Check(ledger.Signers, HasLen, 1)
	c.Check(ledger.HasSigned(addr), Equals, true)
	ledger.Sign(addr) // ensure signing twice doesn't duplicate
	c.Check(ledger.Signers, HasLen, 1)

	c.Check(ledger.HasConsensus(nil), Equals, false)
	nas := NodeAccounts{
		NodeAccount{NodeAddress: addr, Status: Active},
	}
	c.Check(ledger.HasConsensus(nas), Equals, true)
}
