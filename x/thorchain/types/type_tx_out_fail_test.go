package types

import (
	. "gopkg.in/check.v1"
)

type TypeTxOutFailTestSuite struct{}

var _ = Suite(&TypeTxOutFailTestSuite{})

func (s *TypeTxOutFailTestSuite) TestTxOutFailVoter(c *C) {
	inHash := GetRandomTxHash()
	voter := NewTxOutFailVoter(12, 3, inHash)

	c.Check(voter.Empty(), Equals, false)
	c.Check(voter.String(), Equals, TxOutFailVoterID(12, 3, inHash))
	c.Check(voter.FailReason, Equals, "")
	c.Check(voter.Fatal, Equals, false)

	signer := GetRandomBech32Addr()
	c.Check(voter.HasSigned(signer), Equals, false)
	voter.Sign(signer)
	c.Check(voter.HasSigned(signer), Equals, true)
	// second signature should be ignored
	voter.Sign(signer)
	c.Check(voter.Signers, HasLen, 1)

	active := NodeAccounts{
		{NodeAddress: signer, Status: NodeStatus_Active},
	}
	c.Check(voter.HasConsensus(active), Equals, true)
}
