package solana

import (
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestCanSendAssetIsNativeSOLOnly: only native SOL is sendable. Synth, trade and
// secured SOL share its chain and ticker but are not L1 SOL, and an SPL token is not
// native SOL.
func (s *SolTestSuite) TestCanSendAssetIsNativeSOLOnly(c *C) {
	cl := &SOLClient{logger: log.Logger}
	c.Check(cl.canSendAsset(common.SOLAsset), Equals, true)
	for _, a := range []string{guardTokenAsset, "SOL/SOL", "SOL~SOL", "SOL-SOL"} {
		asset, err := common.NewAsset(a)
		c.Assert(err, IsNil)
		c.Check(cl.canSendAsset(asset), Equals, false, Commentf("%s", a))
	}
}

// TestNonSOLCoinAnywhereDropsOutbound: SignTx drops an outbound when ANY coin is not
// native SOL, not only the first, before any RPC or signer-cache work (this client
// has neither), and counts the drop under its own abstain reason.
func (s *SolTestSuite) TestNonSOLCoinAnywhereDropsOutbound(c *C) {
	cl := &SOLClient{logger: log.Logger, m: s.m}
	dropped := s.m.GetCounterVec(metrics.SolKeysignAbstain).WithLabelValues("non_sol_asset_dropped")
	before := testutil.ToFloat64(dropped)

	tx := guardTokenOutbound(c, types2.GetRandomEd25519PubKey())
	tx.Coins = append(common.Coins{common.NewCoin(common.SOLAsset, cosmos.NewUint(11047250))}, tx.Coins...)
	signed, checkpoint, obs, err := cl.SignTx(tx, 1)
	c.Check(err, IsNil)
	c.Check(signed, IsNil)
	c.Check(checkpoint, IsNil)
	c.Check(obs, IsNil)
	c.Check(testutil.ToFloat64(dropped), Equals, before+1)
}
