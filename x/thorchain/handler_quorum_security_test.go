package thorchain

import (
	"sort"
	"time"

	"github.com/cometbft/cometbft/crypto/secp256k1"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type HandlerQuorumSecuritySuite struct{}

var _ = Suite(&HandlerQuorumSecuritySuite{})

// The expected identities are selected by the fixture, independently of the
// voter/handler. Four active nodes require three distinct attestations.
func (s *HandlerQuorumSecuritySuite) TestNetworkFeeAdversarialMatrix(c *C) {
	cases := []struct {
		name      string
		indices   []int
		mutate    bool
		want      []int
		consensus bool
	}{
		{"threshold minus one", []int{0, 1}, false, []int{0, 1}, false},
		{"threshold", []int{0, 1, 2}, false, []int{0, 1, 2}, true},
		{"threshold plus one", []int{0, 1, 2, 3}, false, []int{0, 1, 2, 3}, true},
		{"reordered", []int{2, 0, 1}, false, []int{0, 1, 2}, true},
		{"duplicate below threshold", []int{0, 1, 0, 1}, false, []int{0, 1}, false},
		{"departed signer", []int{0, 1, 4}, false, []int{0, 1}, false},
		{"changed signed rate", []int{0, 1, 2}, true, nil, false},
	}
	for _, tc := range cases {
		c.Logf("network fee: %s", tc.name)
		ctx, mgr := setupManagerForTest(c)
		keys := make([]secp256k1.PrivKey, 5)
		nodes := make(NodeAccounts, 5)
		for i := 0; i < 4; i++ {
			nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		}
		keys[4] = secp256k1.GenPrivKey() // valid signature, no active account
		fee := makeNetworkFee(common.ETHChain, 1024, 256, 100)
		baseline, err := mgr.Keeper().GetNetworkFee(ctx, fee.Chain)
		c.Assert(err, IsNil)
		atts := make([]*common.Attestation, 0, len(tc.indices))
		for _, i := range tc.indices {
			atts = append(atts, attestNetworkFee(c, keys[i], fee))
		}
		if tc.mutate {
			fee.TransactionRate++
		}
		msg := types.NewMsgNetworkFeeQuorum(&common.QuorumNetworkFee{NetworkFee: fee, Attestations: atts}, nodes[0].NodeAddress)
		_, err = NewNetworkFeeQuorumHandler(mgr).Run(ctx, msg)
		c.Assert(err, IsNil, Commentf("%s", tc.name))
		voter, err := mgr.Keeper().GetObservedNetworkFeeVoter(ctx, fee.Height, fee.Chain, int64(fee.TransactionRate), int64(fee.TransactionSize))
		c.Assert(err, IsNil)
		var want []string
		for _, i := range tc.want {
			want = append(want, nodes[i].NodeAddress.String())
		}
		sort.Strings(want)
		got := append([]string(nil), voter.Signers...)
		sort.Strings(got)
		c.Assert(got, DeepEquals, want, Commentf("%s", tc.name))
		c.Assert(voter.BlockHeight > 0, Equals, tc.consensus, Commentf("%s", tc.name))
		saved, err := mgr.Keeper().GetNetworkFee(ctx, fee.Chain)
		c.Assert(err, IsNil)
		if tc.consensus {
			c.Assert(saved.TransactionFeeRate, Equals, fee.TransactionRate)
			c.Assert(saved.TransactionSize, Equals, fee.TransactionSize)
		} else {
			c.Assert(saved.TransactionFeeRate, Equals, baseline.TransactionFeeRate, Commentf("%s", tc.name))
			c.Assert(saved.TransactionSize, Equals, baseline.TransactionSize, Commentf("%s", tc.name))
		}
	}
}

// A valid active price reporter must not be able to lend its signature to a
// changed rate, timestamp, or required-symbol version. The handler silently
// discards bad feeds, so absence of a saved price is the security assertion.
func (s *HandlerPriceFeedQuorumBatchSuite) TestSignedFeedTamperSecurity(c *C) {
	for _, change := range []struct {
		name   string
		mutate func(*common.PriceFeed)
	}{
		{"rate", func(p *common.PriceFeed) { p.Rates[0].Amount++ }},
		{"timestamp", func(p *common.PriceFeed) { p.Time += int64(time.Minute / time.Millisecond) }},
		{"version", func(p *common.PriceFeed) { p.Version = []byte("revision") }},
	} {
		c.Logf("price feed: %s", change.name)
		ctx, mgr := setupManagerForTest(c)
		_, feed, node, err := s.setUp(map[string]float64{"BTC": 100000})
		c.Assert(err, IsNil)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
		change.mutate(feed.PriceFeed)
		msg := types.NewMsgPriceFeedQuorumBatch([]*common.QuorumPriceFeed{feed}, node.NodeAddress)
		_, err = NewPriceFeedQuorumBatchHandler(mgr).Run(ctx, msg)
		c.Assert(err, IsNil)
		iterator := mgr.Keeper().GetPriceIterator(ctx)
		c.Assert(iterator.Valid(), Equals, false, Commentf("%s", change.name))
		iterator.Close()
	}
}

func (s *HandlerQuorumSecuritySuite) TestErrataAdversarialMatrix(c *C) {
	cases := []struct {
		name        string
		indices     []int
		changeChain bool
		want        []int
		consensus   bool
	}{
		{"threshold minus one", []int{0, 1}, false, []int{0, 1}, false},
		{"threshold", []int{0, 1, 2}, false, []int{0, 1, 2}, true},
		{"threshold plus one", []int{0, 1, 2, 3}, false, []int{0, 1, 2, 3}, true},
		{"reordered", []int{2, 0, 1}, false, []int{0, 1, 2}, true},
		{"duplicate below threshold", []int{0, 1, 0, 1}, false, []int{0, 1}, false},
		{"departed signer", []int{0, 1, 4}, false, []int{0, 1}, false},
		{"cross chain replay", []int{0, 1, 2}, true, nil, false},
	}
	for _, tc := range cases {
		c.Logf("errata: %s", tc.name)
		ctx, mgr := setupManagerForTest(c)
		keys := make([]secp256k1.PrivKey, 5)
		nodes := make(NodeAccounts, 5)
		for i := 0; i < 4; i++ {
			nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		}
		keys[4] = secp256k1.GenPrivKey()
		er := makeErrataTx(GetRandomTxHash(), common.ETHChain)
		mgr.Keeper().SetObservedTxInVoter(ctx, types.ObservedTxVoter{
			TxID: er.Id,
			Txs:  common.ObservedTxs{{Tx: common.Tx{ID: er.Id, Chain: er.Chain}}},
		})
		atts := make([]*common.Attestation, 0, len(tc.indices))
		for _, i := range tc.indices {
			atts = append(atts, attestErrataTx(c, keys[i], er))
		}
		if tc.changeChain {
			er.Chain = common.BTCChain
		}
		msg := types.NewMsgErrataTxQuorum(&common.QuorumErrataTx{ErrataTx: er, Attestations: atts}, nodes[0].NodeAddress)
		_, err := NewErrataTxQuorumHandler(mgr).Run(ctx, msg)
		c.Assert(err, IsNil, Commentf("%s", tc.name))
		voter, err := mgr.Keeper().GetErrataTxVoter(ctx, er.Id, er.Chain)
		c.Assert(err, IsNil)
		var want []string
		for _, i := range tc.want {
			want = append(want, nodes[i].NodeAddress.String())
		}
		sort.Strings(want)
		got := append([]string(nil), voter.Signers...)
		sort.Strings(got)
		c.Assert(got, DeepEquals, want, Commentf("%s", tc.name))
		c.Assert(voter.BlockHeight > 0, Equals, tc.consensus, Commentf("%s", tc.name))
	}
}
