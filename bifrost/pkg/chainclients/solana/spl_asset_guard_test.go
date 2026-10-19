package solana

import (
	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

// TestAssetGuardAllowsWhitelistedSPLTokens: canSendAsset keeps the client from
// building an asset it cannot send as a native SOL transfer of the coin amount. With
// SPL support it must also pass whitelisted SPL tokens, which the builders send with
// TransferChecked; otherwise SignTx drops every SPL outbound before building it. It
// still refuses tokens that are not whitelisted and the trade, synth and secured
// forms of a whitelisted one.
func (s *SolTestSuite) TestAssetGuardAllowsWhitelistedSPLTokens(c *C) {
	cl := &SOLClient{logger: log.Logger, spl: testSPLRegistry(testUSDCMint)}
	usdc := usdcAsset(c)
	c.Check(cl.canSendAsset(common.SOLAsset), Equals, true)
	c.Check(cl.canSendAsset(usdc), Equals, true)

	usdt, err := common.NewAsset("SOL.USDT-ES9VMFRZACERMJFRF4H2FYD4KCONKY11MCCE8BENWNYB")
	c.Assert(err, IsNil)
	c.Check(cl.canSendAsset(usdt), Equals, false) // listed, not whitelisted
	c.Check(cl.canSendAsset(usdc.GetTradeAsset()), Equals, false)
	c.Check(cl.canSendAsset(usdc.GetSyntheticAsset()), Equals, false)
	c.Check(cl.canSendAsset(usdc.GetSecuredAsset()), Equals, false)

	// Without a registry (SPL not configured) the client sends SOL only.
	c.Check((&SOLClient{logger: log.Logger}).canSendAsset(usdc), Equals, false)
}
