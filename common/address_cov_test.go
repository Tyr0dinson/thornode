package common

import (
	"bytes"
	"crypto/sha256"

	adacommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcutil"
	dogchaincfg "github.com/eager7/dogd/chaincfg"
	"github.com/eager7/dogutil"
	bchchaincfg "github.com/gcash/bchd/chaincfg"
	"github.com/gcash/bchutil"
	ltcchaincfg "github.com/ltcsuite/ltcd/chaincfg"
	"github.com/ltcsuite/ltcutil"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/zecutil"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ss58"
)

type AddressCovSuite struct{}

var _ = Suite(&AddressCovSuite{})

var addressCovHash = bytes.Repeat([]byte{0x42}, 20)

// addressCovMainNet mirrors what GetNetwork reports for a mainnet address on the
// network the tests are compiled for.
func addressCovMainNet() ChainNetwork {
	if CurrentChainNetwork == MockNet {
		return MainNet
	}
	return CurrentChainNetwork
}

func addressCovBCHParams() *bchchaincfg.Params {
	if CurrentChainNetwork == MockNet {
		return &bchchaincfg.RegressionNetParams
	}
	return &bchchaincfg.MainNetParams
}

func (s *AddressCovSuite) TestBCHAddressFormats(c *C) {
	for _, params := range []*bchchaincfg.Params{
		&bchchaincfg.MainNetParams, &bchchaincfg.TestNet3Params, &bchchaincfg.RegressionNetParams,
	} {
		pkh, err := bchutil.NewAddressPubKeyHash(addressCovHash, params)
		c.Assert(err, IsNil)
		sh, err := bchutil.NewAddressScriptHashFromHash(addressCovHash, params)
		c.Assert(err, IsNil)
		legacy, err := bchutil.NewLegacyAddressPubKeyHash(addressCovHash, params)
		c.Assert(err, IsNil)

		c.Assert(Address(pkh.String()).IsValidBCHAddress(), Equals, true, Commentf("%s", params.Name))
		c.Assert(Address(sh.String()).IsValidBCHAddress(), Equals, true, Commentf("%s", params.Name))
		c.Assert(Address(legacy.String()).IsValidBCHAddress(), Equals, false, Commentf("%s", params.Name))
		c.Assert(Address(pkh.String()).IsChain(BCHChain), Equals, true)
	}
	c.Assert(Address("garbage").IsValidBCHAddress(), Equals, false)
	// regtest-only cashaddr prefix
	reg, err := bchutil.NewAddressPubKeyHash(addressCovHash, &bchchaincfg.RegressionNetParams)
	c.Assert(err, IsNil)
	c.Assert(Address(reg.String()).IsValidBCHAddress(), Equals, true)
	legacyReg, err := bchutil.NewLegacyAddressScriptHashFromHash(addressCovHash, &bchchaincfg.RegressionNetParams)
	c.Assert(err, IsNil)
	c.Assert(Address(legacyReg.String()).IsValidBCHAddress(), Equals, false)
}

func (s *AddressCovSuite) TestConvertToNewBCHAddressFormat(c *C) {
	params := addressCovBCHParams()

	_, err := ConvertToNewBCHAddressFormat(Address("0x1234567890123456789012345678901234567890"))
	c.Assert(err, NotNil)

	// legacy pubkey hash converts to cashaddr pubkey hash
	legacy, err := bchutil.NewLegacyAddressPubKeyHash(addressCovHash, params)
	c.Assert(err, IsNil)
	want, err := bchutil.NewAddressPubKeyHash(addressCovHash, params)
	c.Assert(err, IsNil)
	got, err := ConvertToNewBCHAddressFormat(Address(legacy.String()))
	c.Assert(err, IsNil)
	c.Assert(got.String(), Equals, want.String())

	// a cashaddr stays the same
	got, err = ConvertToNewBCHAddressFormat(Address(want.String()))
	c.Assert(err, IsNil)
	c.Assert(got.String(), Equals, want.String())

	// legacy script hash converts to cashaddr script hash
	legacySH, err := bchutil.NewLegacyAddressScriptHashFromHash(addressCovHash, params)
	c.Assert(err, IsNil)
	wantSH, err := bchutil.NewAddressScriptHashFromHash(addressCovHash, params)
	c.Assert(err, IsNil)
	got, err = ConvertToNewBCHAddressFormat(Address(legacySH.String()))
	c.Assert(err, IsNil)
	c.Assert(got.String(), Equals, wantSH.String())

	// a BCH address of a different network cannot be decoded with this network's params
	other := &bchchaincfg.TestNet3Params
	otherAddr, err := bchutil.NewAddressPubKeyHash(addressCovHash, other)
	c.Assert(err, IsNil)
	_, err = ConvertToNewBCHAddressFormat(Address(otherAddr.String()))
	c.Assert(err, NotNil)

	// unsupported address type
	pub, err := bchutil.NewAddressPubKey(append([]byte{0x02}, bytes.Repeat([]byte{0x11}, 32)...), params)
	if err == nil {
		_, err = getBCHAddress(pub, params)
		c.Assert(err, NotNil)
	}
}

func (s *AddressCovSuite) TestGetNetworkTHOR(c *C) {
	for prefix, want := range map[string]ChainNetwork{
		"thor":  addressCovMainNet(),
		"tthor": MockNet,
		"sthor": StageNet,
		"cthor": ChainNet,
	} {
		str, err := ConvertAndEncode(prefix, addressCovHash)
		c.Assert(err, IsNil)
		c.Assert(Address(str).GetNetwork(THORChain), Equals, want, Commentf("%s", prefix))
	}
	// unknown prefix falls back to the current network
	str, err := ConvertAndEncode("other", addressCovHash)
	c.Assert(err, IsNil)
	c.Assert(Address(str).GetNetwork(THORChain), Equals, CurrentChainNetwork)
	// EVM addresses always report the current network
	c.Assert(Address("0x1234567890123456789012345678901234567890").GetNetwork(ETHChain), Equals, CurrentChainNetwork)
}

func (s *AddressCovSuite) TestGetNetworkBTC(c *C) {
	main, err := btcutil.NewAddressPubKeyHash(addressCovHash, &chaincfg.MainNetParams)
	c.Assert(err, IsNil)
	test, err := btcutil.NewAddressPubKeyHash(addressCovHash, &chaincfg.TestNet3Params)
	c.Assert(err, IsNil)
	witness, err := btcutil.NewAddressWitnessPubKeyHash(addressCovHash, &chaincfg.MainNetParams)
	c.Assert(err, IsNil)
	witReg, err := btcutil.NewAddressWitnessPubKeyHash(addressCovHash, &chaincfg.RegressionNetParams)
	c.Assert(err, IsNil)

	c.Assert(Address(main.String()).GetNetwork(BTCChain), Equals, addressCovMainNet())
	c.Assert(Address(test.String()).GetNetwork(BTCChain), Equals, MockNet)
	c.Assert(Address(witness.String()).GetNetwork(BTCChain), Equals, addressCovMainNet())
	c.Assert(Address(witReg.String()).GetNetwork(BTCChain), Equals, MockNet)
	c.Assert(Address("garbage").GetNetwork(BTCChain), Equals, CurrentChainNetwork)
}

func (s *AddressCovSuite) TestGetNetworkLTC(c *C) {
	main, err := ltcutil.NewAddressPubKeyHash(addressCovHash, &ltcchaincfg.MainNetParams)
	c.Assert(err, IsNil)
	test, err := ltcutil.NewAddressPubKeyHash(addressCovHash, &ltcchaincfg.TestNet4Params)
	c.Assert(err, IsNil)
	witness, err := ltcutil.NewAddressWitnessPubKeyHash(addressCovHash, &ltcchaincfg.MainNetParams)
	c.Assert(err, IsNil)
	witReg, err := ltcutil.NewAddressWitnessPubKeyHash(addressCovHash, &ltcchaincfg.RegressionNetParams)
	c.Assert(err, IsNil)

	c.Assert(Address(main.String()).GetNetwork(LTCChain), Equals, addressCovMainNet())
	c.Assert(Address(test.String()).GetNetwork(LTCChain), Equals, MockNet)
	c.Assert(Address(witness.String()).GetNetwork(LTCChain), Equals, addressCovMainNet())
	c.Assert(Address(witReg.String()).GetNetwork(LTCChain), Equals, MockNet)
	c.Assert(Address("garbage").GetNetwork(LTCChain), Equals, CurrentChainNetwork)
	c.Assert(Address(main.String()).IsChain(LTCChain), Equals, true)
	c.Assert(Address(test.String()).IsChain(LTCChain), Equals, true)
}

func (s *AddressCovSuite) TestGetNetworkBCH(c *C) {
	main, err := bchutil.NewAddressPubKeyHash(addressCovHash, &bchchaincfg.MainNetParams)
	c.Assert(err, IsNil)
	test, err := bchutil.NewAddressPubKeyHash(addressCovHash, &bchchaincfg.TestNet3Params)
	c.Assert(err, IsNil)
	reg, err := bchutil.NewAddressPubKeyHash(addressCovHash, &bchchaincfg.RegressionNetParams)
	c.Assert(err, IsNil)

	c.Assert(Address(main.String()).GetNetwork(BCHChain), Equals, addressCovMainNet())
	c.Assert(Address(test.String()).GetNetwork(BCHChain), Equals, MockNet)
	c.Assert(Address(reg.String()).GetNetwork(BCHChain), Equals, MockNet)
	c.Assert(Address("garbage").GetNetwork(BCHChain), Equals, CurrentChainNetwork)
	c.Assert(Address("garbage").IsChain(BCHChain), Equals, false)
}

func (s *AddressCovSuite) TestGetNetworkDOGE(c *C) {
	main, err := dogutil.NewAddressPubKeyHash(addressCovHash, &dogchaincfg.MainNetParams)
	c.Assert(err, IsNil)
	test, err := dogutil.NewAddressPubKeyHash(addressCovHash, &dogchaincfg.TestNet3Params)
	c.Assert(err, IsNil)
	reg, err := dogutil.NewAddressPubKeyHash(addressCovHash, &dogchaincfg.RegressionNetParams)
	c.Assert(err, IsNil)

	c.Assert(Address(main.String()).GetNetwork(DOGEChain), Equals, addressCovMainNet())
	c.Assert(Address(test.String()).GetNetwork(DOGEChain), Equals, MockNet)
	c.Assert(Address(reg.String()).GetNetwork(DOGEChain), Equals, MockNet)
	c.Assert(Address("garbage").GetNetwork(DOGEChain), Equals, CurrentChainNetwork)
	c.Assert(Address(reg.String()).IsChain(DOGEChain), Equals, true)
	c.Assert(Address("garbage").IsChain(DOGEChain), Equals, false)
}

func (s *AddressCovSuite) TestGetNetworkZECAndADA(c *C) {
	var h [20]byte
	copy(h[:], addressCovHash)
	mainZ := zecutil.NewAddressPubKeyHash(h, "mainnet").String()
	testZ := zecutil.NewAddressPubKeyHash(h, "testnet3").String()
	c.Assert(Address(mainZ).GetNetwork(ZECChain), Equals, addressCovMainNet())
	c.Assert(Address(testZ).GetNetwork(ZECChain), Equals, MockNet)
	c.Assert(Address("garbage").GetNetwork(ZECChain), Equals, CurrentChainNetwork)

	key := bytes.Repeat([]byte{0x07}, 32)
	mainA, err := adacommon.NewAddressFromParts(adacommon.AddressTypeKeyNone, adacommon.AddressNetworkMainnet, key, nil)
	c.Assert(err, IsNil)
	testA, err := adacommon.NewAddressFromParts(adacommon.AddressTypeKeyNone, adacommon.AddressNetworkTestnet, key, nil)
	c.Assert(err, IsNil)
	c.Assert(Address(mainA.String()).GetNetwork(ADAChain), Equals, addressCovMainNet())
	c.Assert(Address(testA.String()).GetNetwork(ADAChain), Equals, MockNet)

	for _, ch := range []Chain{SOLChain, SUIChain, TAOChain, DOTChain} {
		c.Assert(Address("whatever").GetNetwork(ch), Equals, CurrentChainNetwork)
	}
}

func (s *AddressCovSuite) TestToTexAddressAndEquivalence(c *C) {
	var h [20]byte
	copy(h[:], addressCovHash)
	network := "mainnet"
	if CurrentChainNetwork == MockNet {
		network = "testnet3"
	}
	p2pkh := Address(zecutil.NewAddressPubKeyHash(h, network).String())

	tex, err := p2pkh.ToTexAddress()
	c.Assert(err, IsNil)
	c.Assert(tex.IsChain(ZECChain), Equals, true)
	c.Assert(EquivalentAddresses(ZECChain, p2pkh, tex), Equals, true)
	c.Assert(EquivalentAddresses(ZECChain, tex, p2pkh), Equals, true)
	c.Assert(EquivalentAddresses(BTCChain, p2pkh, tex), Equals, false)

	// non zcash
	_, err = Address("0x1234567890123456789012345678901234567890").ToTexAddress()
	c.Assert(err, NotNil)
	// tex is not a P2PKH address
	_, err = tex.ToTexAddress()
	c.Assert(err, NotNil)
}

func (s *AddressCovSuite) TestIsValidDOTAddress(c *C) {
	pub := bytes.Repeat([]byte{0x05}, 32)
	dot, err := ss58.Encode(DOTChain.SS58Prefix(), pub)
	c.Assert(err, IsNil)
	// kusama prefix (2) is never used by DOT or TAO
	ksm, err := ss58.Encode(2, pub)
	c.Assert(err, IsNil)
	c.Assert(IsValidDOTAddress(dot), Equals, true)
	c.Assert(IsValidDOTAddress(ksm), Equals, false)
	c.Assert(IsValidDOTAddress("bogus"), Equals, false)
	c.Assert(IsValidTAOAddress(ksm), Equals, false)
}

func (s *AddressCovSuite) TestAccAddressAndNoop(c *C) {
	acc := cosmos.AccAddress(addressCovHash)
	addr := Address(acc.String())
	got, err := addr.AccAddress()
	c.Assert(err, IsNil)
	c.Assert(got.Equals(acc), Equals, true)

	_, err = Address("bogus").AccAddress()
	c.Assert(err, NotNil)

	c.Assert(NoopAddress.IsNoop(), Equals, true)
	c.Assert(addr.IsNoop(), Equals, false)
}

func (s *AddressCovSuite) TestMappedAccAddress(c *C) {
	// EVM address maps hex bytes directly
	evm := Address("0x4242424242424242424242424242424242424242")
	acc, err := evm.MappedAccAddress()
	c.Assert(err, IsNil)
	c.Assert([]byte(acc), DeepEquals, addressCovHash)

	// hashed chains use sha256 of the address string
	sui := Address("0x" + "ab" + string(bytes.Repeat([]byte("cd"), 31)))
	c.Assert(sui.IsChain(SUIChain), Equals, true)
	pub := bytes.Repeat([]byte{0x05}, 32)
	dotStr, err := ss58.Encode(DOTChain.SS58Prefix(), pub)
	c.Assert(err, IsNil)
	dot := Address(dotStr)
	acc, err = dot.MappedAccAddress()
	c.Assert(err, IsNil)
	sum := sha256.Sum256([]byte(dotStr))
	c.Assert([]byte(acc), DeepEquals, sum[:20])

	// bech32 address is re-encoded with the thor prefix
	other, err := ConvertAndEncode("cosmos", addressCovHash)
	c.Assert(err, IsNil)
	acc, err = Address(other).MappedAccAddress()
	c.Assert(err, IsNil)
	c.Assert([]byte(acc), DeepEquals, addressCovHash)

	// not bech32
	_, err = Address("garbage").MappedAccAddress()
	c.Assert(err, NotNil)
}
