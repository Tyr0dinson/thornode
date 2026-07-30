package utxo

import (
	"github.com/gcash/bchutil"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

type ECashSuite struct{}

var _ = Suite(&ECashSuite{})

func (s *ECashSuite) TestNormalizeCashAddressMatchesXECVaultDerivation(c *C) {
	if common.CurrentChainNetwork != common.MockNet {
		c.Skip("XEC address params are network specific")
	}

	client := &Client{}
	client.cfg.ChainID = common.XECChain

	pk := common.PubKey("tthorpub1addwnpepqwznsrgk2t5vn2cszr6ku6zned6tqxknugzw3vhdcjza284d7djp5rql6vn")
	vaultAddr, err := pk.GetAddress(common.XECChain)
	c.Assert(err, IsNil)

	// THORChain's canonical XEC form includes its network-specific prefix.
	c.Check(common.IsValidXECAddress(vaultAddr.String()), Equals, true)

	params := common.GetXECAddressParams(common.MockNet)
	c.Assert(params, NotNil)
	bare := vaultAddr.String()[len(params.CashAddressPrefix)+1:]
	c.Check(common.IsValidXECAddress(bare), Equals, false)

	// Daemon output must use the same form as the vault address comparison.
	c.Check(client.normalizeCashAddress(bare), Equals, vaultAddr.String())

	// Canonical input remains stable across repeated normalization.
	c.Check(client.normalizeCashAddress(vaultAddr.String()), Equals, vaultAddr.String())

	// The signer round-trip uses the same canonical form as address validation.
	decoded, err := bchutil.DecodeAddress(vaultAddr.String(), params)
	c.Assert(err, IsNil)
	c.Check(client.normalizeCashAddress(decoded.String()), Equals, vaultAddr.String())
}

func (s *ECashSuite) TestNormalizeCashAddressLeavesNonCashAddrAlone(c *C) {
	if common.CurrentChainNetwork != common.MockNet {
		c.Skip("XEC address params are network specific")
	}

	client := &Client{}
	client.cfg.ChainID = common.XECChain

	c.Check(client.normalizeCashAddress(""), Equals, "")

	// Legacy addresses are not converted into CashAddr payloads.
	legacy := "mkVvHDUJgxRvvBBFkTZTgSthQGgnBRJTQV"
	c.Check(client.normalizeCashAddress(legacy), Equals, legacy)

	// A foreign network prefix must not be rewritten as an XEC prefix.
	other := "bitcoincash:qqsqgupa7tajw5wkunpjezjjnkmmuevcmgs7saqa99"
	c.Check(client.normalizeCashAddress(other), Equals, other)
}
