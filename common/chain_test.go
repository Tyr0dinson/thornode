package common

import (
	. "gopkg.in/check.v1"
)

type ChainSuite struct{}

var _ = Suite(&ChainSuite{})

func (s ChainSuite) TestChain(c *C) {
	ethChain, err := NewChain("eth")
	c.Assert(err, IsNil)
	c.Check(ethChain.Equals(ETHChain), Equals, true)
	c.Check(ethChain.IsEmpty(), Equals, false)
	c.Check(ethChain.String(), Equals, "ETH")

	_, err = NewChain("B") // too short
	c.Assert(err, NotNil)

	chains := Chains{"DOGE", "DOGE", "BTC"}
	c.Check(chains.Has("BTC"), Equals, true)
	c.Check(chains.Has("ETH"), Equals, false)
	uniq := chains.Distinct()
	c.Assert(uniq, HasLen, 2)

	algo := ETHChain.GetSigningAlgo()
	c.Assert(algo, Equals, SigningAlgoSecp256k1)

	c.Assert(BTCChain.GetGasAsset(), Equals, BTCAsset)
	c.Assert(ETHChain.GetGasAsset(), Equals, ETHAsset)
	c.Assert(LTCChain.GetGasAsset(), Equals, LTCAsset)
	c.Assert(BCHChain.GetGasAsset(), Equals, BCHAsset)
	c.Assert(DOGEChain.GetGasAsset(), Equals, DOGEAsset)
	c.Assert(GAIAChain.GetGasAsset(), Equals, ATOMAsset)
	c.Assert(NOBLEChain.GetGasAsset(), Equals, USDCAsset)
	c.Assert(TRONChain.GetGasAsset(), Equals, TRXAsset)
	c.Assert(TONChain.GetGasAsset(), Equals, GRAMAsset)
	c.Assert(EmptyChain.GetGasAsset(), Equals, EmptyAsset)

	// Test GetGasUnits
	nobleGasUnits, _ := NOBLEChain.GetGasUnits()
	gaiaGasUnits, _ := GAIAChain.GetGasUnits()
	c.Assert(nobleGasUnits, Equals, "uusdc")
	c.Assert(gaiaGasUnits, Equals, "uatom")

	// Test GetGasAssetDecimal
	c.Assert(NOBLEChain.GetGasAssetDecimal(), Equals, int64(6))
	c.Assert(GAIAChain.GetGasAssetDecimal(), Equals, int64(6))

	// Test ApproximateBlockMilliseconds
	c.Assert(NOBLEChain.ApproximateBlockMilliseconds(), Equals, int64(1500))
	c.Assert(GAIAChain.ApproximateBlockMilliseconds(), Equals, int64(6000))

	// Test InboundNotes
	c.Assert(NOBLEChain.InboundNotes(), Equals, "Transfer the inbound_address the asset with the memo. Do not use multi-in, multi-out transactions.")
	c.Assert(GAIAChain.InboundNotes(), Equals, "Transfer the inbound_address the asset with the memo. Do not use multi-in, multi-out transactions.")

	// Test BechPrefix
	c.Assert(NOBLEChain.BechPrefix(), Equals, "noble")
	c.Assert(GAIAChain.BechPrefix(), Equals, "cosmos")

	// Tron chain tests
	tronGasUnits, _ := TRONChain.GetGasUnits()
	c.Assert(tronGasUnits, Equals, "sun")
	c.Assert(TRONChain.GetGasAssetDecimal(), Equals, int64(6))
	c.Assert(TRONChain.ApproximateBlockMilliseconds(), Equals, int64(3000))

	// TON chain tests
	tonGasUnits, _ := TONChain.GetGasUnits()
	c.Assert(tonGasUnits, Equals, "nanoton")
	c.Assert(TONChain.GetGasAssetDecimal(), Equals, int64(9))
	c.Assert(TONChain.ApproximateBlockMilliseconds(), Equals, int64(2500))
	c.Assert(TONChain.GetSigningAlgo(), Equals, SigningAlgoEd25519)
}
