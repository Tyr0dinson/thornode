package dashutil

import (
	"testing"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/wire"
	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type ParamsSuite struct{}

var _ = Suite(&ParamsSuite{})

// TestMainnetPrefixes pins the Dash mainnet prefix bytes — anyone who
// accidentally changes these would silently produce invalid addresses on
// production.
func (s *ParamsSuite) TestMainnetPrefixes(c *C) {
	c.Assert(MainNetParams.PubKeyHashAddrID, Equals, byte(0x4c),
		Commentf("mainnet PubKeyHashAddrID must be 0x4c (Dash 'X' prefix)"))
	c.Assert(MainNetParams.ScriptHashAddrID, Equals, byte(0x10),
		Commentf("mainnet ScriptHashAddrID must be 0x10 (Dash '7' prefix)"))
}

// TestRegtestPrefixes pins Dash regtest prefix bytes. Regtest and testnet
// intentionally share prefix bytes in dashd — both produce 'y' / '8'-prefixed
// addresses. Distinguish at the daemon level, not the address level.
func (s *ParamsSuite) TestRegtestPrefixes(c *C) {
	c.Assert(RegressionNetParams.PubKeyHashAddrID, Equals, byte(0x8c),
		Commentf("regtest PubKeyHashAddrID must be 0x8c (Dash 'y' prefix)"))
	c.Assert(TestNetParams.PubKeyHashAddrID, Equals, RegressionNetParams.PubKeyHashAddrID,
		Commentf("testnet and regtest PubKeyHashAddrID must match"))
}

// TestNoSegwit verifies Bech32HRPSegwit is empty on all Dash params. Dash
// never shipped SegWit; a non-empty value would cause the generic btcutil
// address-decode branch to attempt segwit parsing and misclassify addresses.
func (s *ParamsSuite) TestNoSegwit(c *C) {
	for name, p := range map[string]*chaincfg.Params{
		"mainnet": &MainNetParams,
		"testnet": &TestNetParams,
		"regtest": &RegressionNetParams,
	} {
		c.Assert(p.Bech32HRPSegwit, Equals, "",
			Commentf("%s: Bech32HRPSegwit must be empty (Dash has no SegWit)", name))
	}
}

// TestGlobalRegistration verifies dashutil's init() registered the mainnet
// params with chaincfg's global registry. Bitcoin-compatible code paths that
// dispatch via chaincfg.IsPubKeyHashAddrID depend on this registration; if it
// silently failed, downstream callers would misclassify Dash script types.
func (s *ParamsSuite) TestGlobalRegistration(c *C) {
	c.Assert(chaincfg.IsPubKeyHashAddrID(MainNetParams.PubKeyHashAddrID), Equals, true,
		Commentf("mainnet PubKeyHashAddrID 0x%x not registered with chaincfg.IsPubKeyHashAddrID",
			MainNetParams.PubKeyHashAddrID))
	c.Assert(chaincfg.IsScriptHashAddrID(MainNetParams.ScriptHashAddrID), Equals, true,
		Commentf("mainnet ScriptHashAddrID 0x%x not registered with chaincfg.IsScriptHashAddrID",
			MainNetParams.ScriptHashAddrID))
}

// TestHDCoinType pins the SLIP-0044 coin type for Dash mainnet (5). Testnet
// and regtest conventionally use 1.
func (s *ParamsSuite) TestHDCoinType(c *C) {
	c.Assert(MainNetParams.HDCoinType, Equals, uint32(5),
		Commentf("mainnet HDCoinType must be 5 (SLIP-0044 Dash)"))
	c.Assert(TestNetParams.HDCoinType, Equals, uint32(1),
		Commentf("testnet HDCoinType must be 1 (SLIP-0044 convention)"))
	c.Assert(RegressionNetParams.HDCoinType, Equals, uint32(1),
		Commentf("regtest HDCoinType must be 1 (SLIP-0044 convention)"))
}

// TestNetMagicsUnique verifies the three Dash param sets each carry a
// distinct network magic. Shared magic between mainnet and regtest would let
// a regtest peer connect to a mainnet daemon (and vice versa) during tests.
func (s *ParamsSuite) TestNetMagicsUnique(c *C) {
	nets := map[wire.BitcoinNet]string{
		MainNetParams.Net:       "mainnet",
		TestNetParams.Net:       "testnet",
		RegressionNetParams.Net: "regtest",
	}
	c.Assert(nets, HasLen, 3,
		Commentf("expected 3 unique Net magics; got %+v", nets))
}
