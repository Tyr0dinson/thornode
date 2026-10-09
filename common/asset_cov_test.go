package common

import (
	"encoding/json"

	"github.com/gogo/protobuf/jsonpb"
	. "gopkg.in/check.v1"
)

type AssetCovSuite struct{}

var _ = Suite(&AssetCovSuite{})

func (s *AssetCovSuite) TestOneOf(c *C) {
	c.Assert(BTCAsset.OneOf(ETHAsset, BTCAsset), Equals, true)
	c.Assert(BTCAsset.OneOf(ETHAsset, LTCAsset), Equals, false)
	c.Assert(BTCAsset.OneOf(), Equals, false)
	c.Assert(BTCAsset.OneOf(BTCAsset.GetSyntheticAsset()), Equals, false)
}

func (s *AssetCovSuite) TestVariantConversions(c *C) {
	synth := BTCAsset.GetSyntheticAsset()
	c.Assert(synth.Synth, Equals, true)
	c.Assert(synth.String(), Equals, "BTC/BTC")
	c.Assert(synth.GetSyntheticAsset(), Equals, synth)
	c.Assert(synth.IsVaultAsset(), Equals, true)
	c.Assert(BTCAsset.IsVaultAsset(), Equals, false)

	trade := BTCAsset.GetTradeAsset()
	c.Assert(trade.Trade, Equals, true)
	c.Assert(trade.String(), Equals, "BTC~BTC")
	c.Assert(trade.GetTradeAsset(), Equals, trade)

	secured := BTCAsset.GetSecuredAsset()
	c.Assert(secured.Secured, Equals, true)
	c.Assert(secured.String(), Equals, "BTC-BTC")
	c.Assert(secured.GetSecuredAsset(), Equals, secured)

	// all variants convert back to the layer1 asset
	for _, a := range []Asset{synth, trade, secured, BTCAsset} {
		c.Assert(a.GetLayer1Asset(), Equals, BTCAsset, Commentf("%s", a))
	}

	derived := BTCAsset.GetDerivedAsset()
	c.Assert(derived, Equals, THORBTC)
	c.Assert(derived.IsDerivedAsset(), Equals, true)
}

func (s *AssetCovSuite) TestNative(c *C) {
	for a, want := range map[Asset]string{
		RuneNative: "rune",
		TOR:        "tor",
		TCY:        "tcy",
		RUJI:       "x/ruji",
		NAMI:       "thor.nami",
		LQDY:       "thor.lqdy",
		XUSK:       "thor.xusk",
		AUTO:       "thor.auto",
		BTCAsset:   "btc.btc",
	} {
		c.Assert(a.Native(), Equals, want, Commentf("%s", a))
	}
	c.Assert(BTCAsset.GetSyntheticAsset().Native(), Equals, "btc/btc")
}

func (s *AssetCovSuite) TestIsWhitelistedAndGas(c *C) {
	for _, a := range []Asset{RUJI, NAMI, LQDY, XUSK, AUTO} {
		c.Assert(a.IsWhitelisted(), Equals, true, Commentf("%s", a))
		c.Assert(a.IsDerivedAsset(), Equals, false, Commentf("%s", a))
	}
	c.Assert(RuneNative.IsWhitelisted(), Equals, false)
	c.Assert(BTCAsset.IsWhitelisted(), Equals, false)

	c.Assert(BTCAsset.IsGasAsset(), Equals, true)
	c.Assert(BTCAsset.GetSyntheticAsset().IsGasAsset(), Equals, false)
	// a chain without a gas asset is never a gas asset
	unknown := Asset{Chain: Chain("NOPE"), Symbol: "NOPE", Ticker: "NOPE"}
	c.Assert(unknown.IsGasAsset(), Equals, false)
}

func (s *AssetCovSuite) TestJSONRoundTrip(c *C) {
	for _, a := range []Asset{BTCAsset, BTCAsset.GetSyntheticAsset(), BTCAsset.GetTradeAsset(), BTCAsset.GetSecuredAsset()} {
		buf, err := json.Marshal(a)
		c.Assert(err, IsNil)
		c.Assert(string(buf), Equals, `"`+a.String()+`"`)
		var out Asset
		c.Assert(json.Unmarshal(buf, &out), IsNil)
		c.Assert(out.Equals(a), Equals, true, Commentf("%s", a))

		buf, err = a.MarshalJSONPB(&jsonpb.Marshaler{})
		c.Assert(err, IsNil)
		c.Assert(string(buf), Equals, `"`+a.String()+`"`)
		var out2 Asset
		c.Assert(out2.UnmarshalJSONPB(&jsonpb.Unmarshaler{}, buf), IsNil)
		c.Assert(out2.Equals(a), Equals, true)
	}

	// "." is decoded as the empty asset
	out := BTCAsset
	c.Assert(json.Unmarshal([]byte(`"."`), &out), IsNil)
	c.Assert(out, Equals, EmptyAsset)

	// non string json is rejected
	c.Assert(json.Unmarshal([]byte(`123`), &out), NotNil)
	// invalid asset string is rejected
	c.Assert(json.Unmarshal([]byte(`"BTC.B@D"`), &out), NotNil)
}

func (s *AssetCovSuite) TestMimirString(c *C) {
	c.Assert(BTCAsset.MimirString(), Equals, "BTC-BTC")
	c.Assert(RuneNative.MimirString(), Equals, "THOR-RUNE")
}
