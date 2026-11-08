package signer

import (
	"errors"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

func (s *SignSuite) TestXMRKeygenDiagnosticFailureDoesNotBroadcast(c *C) {
	bridge := &retryAwareKeygenBridge{
		batchingKeygenBridge: batchingKeygenBridge{fakeBridge: fakeBridge{s.bridge}},
		mimirs: map[string]int64{
			constants.ChurnRetryInterval.String():  1000,
			constants.KeygenRetryInterval.String(): 0,
		},
	}
	a, b := types2.GetRandomPubKey(), types2.GetRandomPubKey()
	custom := &MockCustomKeygenChainClient{
		MockChainClient: &MockChainClient{},
		retBlame:        []types2.Blame{{FailReason: "timeout", BlameNodes: []types2.Node{{Pubkey: b.String()}}}},
		retErr:          errors.New("local XMR coordinator failure"),
	}
	sign := &Signer{
		chains:          map[common.Chain]chainclients.ChainClient{common.XMRChain: custom},
		thorchainBridge: bridge,
		logger:          log.With().Str("module", "signer").Logger(),
		errCounter:      s.metrics.GetCounterVec(metrics.SignerError),
	}
	keygen, err := types2.NewKeygen(2700, []string{a.String(), b.String()}, types2.KeygenType_XMRMonovaultKeygen)
	c.Assert(err, IsNil)
	sign.runKeygenRequest(types2.KeygenBlock{Height: 2700, Keygens: []types2.Keygen{keygen}}, keygen)
	c.Assert(custom.callCount, Equals, 1)
	c.Assert(bridge.batchSizes, HasLen, 0)
	c.Log("an XMR hook error carrying timeout blame reaches no on-chain broadcast")
}
