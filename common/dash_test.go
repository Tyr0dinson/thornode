package common

import (
	"bytes"
	"encoding/hex"

	"github.com/blang/semver"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/dashpay/dashd-go/btcutil"
	"github.com/dashpay/dashd-go/chaincfg"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// TestDashPubKeyAddress pins the address derived from a compressed public key
// on both supported networks, independently of the address validation helpers.
func (s *PubKeyTestSuite) TestDashPubKeyAddress(c *C) {
	publicKey, err := hex.DecodeString("02b4632d08485ff1df2db55b9dafd23347d1c47a457072a1e87be26896549a8737")
	c.Assert(err, IsNil)
	key, err := codec.FromCmtPubKeyInterface(secp256k1.PubKey(publicKey))
	c.Assert(err, IsNil)
	encoded, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, key)
	c.Assert(err, IsNil)
	pubKey, err := NewPubKey(encoded)
	c.Assert(err, IsNil)
	address, err := pubKey.GetAddress(DASHChain)
	c.Assert(err, IsNil)
	expected := "XpANHDZNTgEyM2FN8QjERafkRaK4X8Uaeq"
	if CurrentChainNetwork == MockNet {
		expected = "yZnyJAdouDu3gmAuhG3dTc66hroS4AXxnL"
	}
	c.Assert(address.String(), Equals, expected)
}

// TestDASH checks the chain settings used by fees, memo encoding and scanning.
func (s *ChainSuite) TestDASH(c *C) {
	c.Assert(DASHChain.IsUTXO(), Equals, true)
	c.Assert(DASHChain.GetGasAsset(), Equals, DASHAsset)
	c.Assert(DASHChain.GetGasAssetDecimal(), Equals, int64(8))
	unit, perCoin := DASHChain.GetGasUnits()
	c.Assert(unit, Equals, "satsperbyte")
	c.Assert(perCoin.Uint64(), Equals, uint64(100_000_000))
	c.Assert(DASHChain.MaxMemoLength(), Equals, 80)
	c.Assert(DASHChain.P2WPKHOutputValue(), Equals, int64(546))
	c.Assert(DASHChain.DustThreshold().Uint64(), Equals, uint64(10_000))
	c.Assert(DASHChain.ApproximateBlockMilliseconds(), Equals, int64(150_000))
}

// TestDashShortCode checks both encoding and parsing of the public memo alias.
func (s *AssetSuite) TestDashShortCode(c *C) {
	c.Assert(DASHAsset.ShortCode(), Equals, "dh")
	asset, err := NewAssetWithShortCodes(semver.MustParse("999.0.0"), "dh")
	c.Assert(err, IsNil)
	c.Assert(asset, Equals, DASHAsset)
}

func (s *AddressSuite) TestDashIsCurrentNetwork(c *C) {
	for _, params := range []*chaincfg.Params{&chaincfg.MainNetParams, &chaincfg.RegressionNetParams, &chaincfg.TestNet3Params} {
		p2pkh, err := btcutil.NewAddressPubKeyHash(bytes.Repeat([]byte{1}, 20), params)
		c.Assert(err, IsNil)
		p2sh, err := btcutil.NewAddressScriptHashFromHash(bytes.Repeat([]byte{2}, 20), params)
		c.Assert(err, IsNil)
		for _, addr := range []btcutil.Address{p2pkh, p2sh} {
			want := (params != &chaincfg.MainNetParams) == (CurrentChainNetwork == MockNet)
			c.Check(Address(addr.String()).IsCurrentNetwork(DASHChain), Equals, want,
				Commentf("address: %s, build network: %s", addr, CurrentChainNetwork))
		}
	}
	for _, addr := range []Address{
		"", "invalid", "1BoatSLRHtKNngkdXEeobR76b53LETtpyT",
		"0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798",
	} {
		c.Check(addr.IsCurrentNetwork(DASHChain), Equals, false, Commentf("address: %s", addr))
	}
}
