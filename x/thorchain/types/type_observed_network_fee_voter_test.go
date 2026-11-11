package types

import (
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

type ObservedNetworkFeeVoterTestSuite struct{}

var _ = Suite(&ObservedNetworkFeeVoterTestSuite{})

func (ObservedNetworkFeeVoterTestSuite) TestObservedNetworkFeeVoter(c *C) {
	voter := NewObservedNetworkFeeVoter(1024, common.BTCChain)
	c.Check(voter.IsEmpty(), Equals, false)
	addr := GetRandomBech32Addr()
	c.Check(voter.HasSigned(addr), Equals, false)
	voter.Sign(addr)
	c.Check(voter.HasSigned(addr), Equals, true)
	voter.Sign(addr)
	c.Check(voter.Signers, HasLen, 1)
	c.Check(voter.HasConsensus(nil), Equals, false)
	nas := NodeAccounts{
		NodeAccount{NodeAddress: addr, Status: NodeStatus_Active},
	}
	c.Check(voter.HasConsensus(nas), Equals, true)
	c.Check(len(voter.String()) > 0, Equals, true)

	voter1 := NewObservedNetworkFeeVoter(0, common.EmptyChain)
	c.Check(voter1.IsEmpty(), Equals, true)
}

func (ObservedNetworkFeeVoterTestSuite) TestObservedNetworkFeeVoterSignerStrings(c *C) {
	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()
	addr3 := GetRandomBech32Addr()
	voter := NewObservedNetworkFeeVoter(1024, common.BTCChain)
	// an undecodable signer is skipped and an upper-case one decodes like its
	// lower-case form
	voter.Signers = []string{"bogus", strings.ToUpper(addr1.String()), addr2.String()}

	signers := voter.GetSigners()
	c.Assert(signers, HasLen, 2)
	c.Check(signers[0].Equals(addr1), Equals, true)
	c.Check(signers[1].Equals(addr2), Equals, true)
	// the returned addresses are the caller's own
	signers[1][0] ^= 0xff
	c.Check(voter.GetSigners()[1].Equals(addr2), Equals, true)

	c.Check(voter.HasSigned(addr1), Equals, true)
	c.Check(voter.HasSigned(addr2), Equals, true)
	c.Check(voter.HasSigned(addr3), Equals, false)
	c.Check(voter.Sign(addr1), Equals, false)
	c.Check(voter.Sign(addr3), Equals, true)
	c.Check(voter.Signers, HasLen, 4)

	nas := NodeAccounts{
		NodeAccount{NodeAddress: addr1, Status: NodeStatus_Active},
		NodeAccount{NodeAddress: addr2, Status: NodeStatus_Active},
		NodeAccount{NodeAddress: addr3, Status: NodeStatus_Active},
		NodeAccount{NodeAddress: GetRandomBech32Addr(), Status: NodeStatus_Active},
	}
	c.Check(voter.HasConsensus(nas), Equals, true)
	c.Check(voter.HasConsensus(append(nas, NodeAccount{NodeAddress: GetRandomBech32Addr(), Status: NodeStatus_Active})), Equals, false)
}
