package keeperv1

import (
	"fmt"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

type KeySuite struct{}

var _ = Suite(&KeySuite{})

func (s *KeySuite) TestGetKeyMatchesToUpper(c *C) {
	var k KVStore
	// GetKey as it was: prefix + "/" + strings.ToUpper of every part
	want := func(prefix types.DbPrefix, key string, other ...string) []byte {
		out := string(prefix) + "/" + strings.ToUpper(key)
		for _, o := range other {
			out += strings.ToUpper(o)
		}
		return []byte(out)
	}
	keys := []string{
		"", "a", "Z", "abcdefghijklmnopqrstuvwxyz", "@AZ[`az{", "BTC.BTC", "btc.btc", "HaltBTCChain", "TorAnchor-ETH-USDC-0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
		"thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq", "a/b_c-d~e{|}`@[", "\x00\x7f",
		"straße", "éclair", "ﬀ", "ǅ", "\xff\xfe", "mixed ascii and ü",
	}
	for _, key := range keys {
		for _, other := range [][]string{nil, {"x"}, {"/", "ü"}, {"", "Abc"}} {
			got := k.GetKey(prefixMimir, key, other...)
			c.Check(string(got), Equals, string(want(prefixMimir, key, other...)), Commentf("%q %q", key, other))
		}
	}
}

func (s *KeySuite) TestTorAnchorMimirKey(c *C) {
	for _, asset := range []common.Asset{common.BTCAsset, common.ETHAsset, common.TOR, common.RuneAsset()} {
		old := strings.ReplaceAll(fmt.Sprintf("TorAnchor-%s", asset.String()), ".", "-")
		c.Check(TorAnchorMimirKey(asset), Equals, old)
	}
	usdc, err := common.NewAsset("ETH.USDC-0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	c.Assert(err, IsNil)
	c.Check(TorAnchorMimirKey(usdc), Equals, "TorAnchor-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
}
