//go:build mocknet
// +build mocknet

package common

import (
	. "gopkg.in/check.v1"
)

var _ = Suite(&AddressSuite{})

func (s *AddressSuite) TestMocknetAddress(c *C) {
	// BCH mocknet address, invalid on mainnet
	addr, err := NewAddress("qq0y8fmkq48rt3z5dlkv87ged93ranf2ggkuz9gfl8")
	c.Assert(err, IsNil)
	c.Assert(addr.IsChain(BCHChain), Equals, true)
	c.Assert(addr.GetNetwork(BCHChain), Equals, MockNet)
	for _, chain := range AllChains {
		if chain == BCHChain {
			continue
		}

		c.Assert(addr.IsChain(chain), Equals, false)
	}

	// DOGE mocknet address, invalid on mainnet
	addr, err = NewAddress("nfWiQeddE4zsYsDuYhvpgVC7y4gjr5RyqK")
	c.Assert(err, IsNil)
	c.Assert(addr.IsChain(DOGEChain), Equals, true)
	c.Assert(addr.GetNetwork(DOGEChain), Equals, MockNet)
	for _, chain := range AllChains {
		if chain == DOGEChain {
			continue
		}

		c.Assert(addr.IsChain(chain), Equals, false)
	}

	// DOGE mainnet address, invalid on mocknet
	_, err = NewAddress("DJbKker23xfz3ufxAbqUuQwp1EBibGJJHu")
	c.Assert(err, NotNil)
}

func (s *AddressSuite) TestToTexConversion(c *C) {
	ethAddr, _ := NewAddress("0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	addr, err := ethAddr.ToTexAddress()
	c.Assert(err, NotNil)
	c.Assert(addr, Equals, NoAddress)

	p2pkhAddrMain, _ := NewAddress("tmEp4k7dSU8eGj32uJPi2wBSGuiNaVFBLN7")
	addr, err = p2pkhAddrMain.ToTexAddress()
	c.Assert(err, IsNil)
	c.Assert(addr.String(), Equals, "textest1xlk5w3dleprzdug6z68wtjvcw4mtqa3s9pfays")

	// tex address must be p2pkh
	p2shAddrMain, _ := NewAddress("t2BeXPVjcVq4XDf7dgW4w3mK6ZjoePxy7Gb")
	addr, err = p2shAddrMain.ToTexAddress()
	c.Assert(err, NotNil)
}

// A Zcash outbound scheduled to a tex address is observed in its P2PKH form -
// the signer leaves tex destinations unencoded while observers report what the
// daemon decoded - so the two spellings must compare equal everywhere an
// observed outbound is matched against a scheduled one.
func (s *AddressSuite) TestEquivalentAddressesZecTex(c *C) {
	p2pkhAddr, err := NewAddress("tmEp4k7dSU8eGj32uJPi2wBSGuiNaVFBLN7")
	c.Assert(err, IsNil)
	otherAddr, err := NewAddress("tmAbHS91acTju5QaDawZ1M5Pkbv2FFnRwUX")
	c.Assert(err, IsNil)

	texAddr, err := p2pkhAddr.ToTexAddress()
	c.Assert(err, IsNil)
	c.Assert(p2pkhAddr.Equals(texAddr), Equals, false)

	c.Check(EquivalentAddresses(ZECChain, p2pkhAddr, texAddr), Equals, true)
	c.Check(EquivalentAddresses(ZECChain, texAddr, p2pkhAddr), Equals, true)
	c.Check(EquivalentAddresses(ZECChain, p2pkhAddr, p2pkhAddr), Equals, true)
	c.Check(EquivalentAddresses(ZECChain, texAddr, texAddr), Equals, true)

	// Distinct destinations stay distinct in either spelling.
	otherTexAddr, err := otherAddr.ToTexAddress()
	c.Assert(err, IsNil)
	c.Check(EquivalentAddresses(ZECChain, p2pkhAddr, otherAddr), Equals, false)
	c.Check(EquivalentAddresses(ZECChain, texAddr, otherTexAddr), Equals, false)
	c.Check(EquivalentAddresses(ZECChain, texAddr, otherAddr), Equals, false)
}

// An address with no tex encoding - a P2SH destination, a malformed string, or
// one too short to even hold a prefix - must fall back to plain equality rather
// than panicking or collapsing distinct destinations into one.
func (s *AddressSuite) TestEquivalentAddressesZecWithoutTexForm(c *C) {
	p2shAddr, err := NewAddress("t2BeXPVjcVq4XDf7dgW4w3mK6ZjoePxy7Gb")
	c.Assert(err, IsNil)
	_, err = p2shAddr.ToTexAddress()
	c.Assert(err, NotNil)

	c.Check(EquivalentAddresses(ZECChain, p2shAddr, p2shAddr), Equals, true)

	// Unparsable input reaches this comparison from observed transactions, so
	// it must be handled rather than trusted.
	for _, malformed := range []Address{"", "t", "tm", "not-an-address", "t1"} {
		c.Check(EquivalentAddresses(ZECChain, malformed, malformed), Equals, true)
		c.Check(EquivalentAddresses(ZECChain, malformed, p2shAddr), Equals, false)
		c.Check(EquivalentAddresses(ZECChain, p2shAddr, malformed), Equals, false)
	}

	// A P2PKH destination is not equivalent to a P2SH one that shares no bytes.
	p2pkhAddr, err := NewAddress("tmEp4k7dSU8eGj32uJPi2wBSGuiNaVFBLN7")
	c.Assert(err, IsNil)
	c.Check(EquivalentAddresses(ZECChain, p2pkhAddr, p2shAddr), Equals, false)
}
