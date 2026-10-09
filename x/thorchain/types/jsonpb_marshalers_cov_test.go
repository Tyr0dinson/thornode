package types

import (
	"encoding/json"

	"github.com/cosmos/gogoproto/jsonpb"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
)

type JSONPBMarshalersCovSuite struct{}

var _ = Suite(&JSONPBMarshalersCovSuite{})

func mustIndentCov(c *C, v any) string {
	bz, err := json.MarshalIndent(v, "", "  ")
	c.Assert(err, IsNil)
	return string(bz)
}

func marshalCov(c *C, m jsonpb.JSONPBMarshaler) string {
	bz, err := m.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	return string(bz)
}

func decodeCov(c *C, s string, v any) {
	c.Assert(json.Unmarshal([]byte(s), v), IsNil, Commentf(s))
}

func (s *JSONPBMarshalersCovSuite) TestPassthroughMarshalers(c *C) {
	pool := &QueryPoolResponse{Asset: "BTC.BTC", Status: "Available"}
	node := &QueryNodeResponse{NodeAddress: "thor1abc", Status: "Active"}
	type tc struct {
		name     string
		m        jsonpb.JSONPBMarshaler
		expected any
	}
	asgard := &QueryAsgardVaultsResponse{AsgardVaults: []*QueryVaultResponse{{BlockHeight: 5}}}
	derived := &QueryDerivedPoolsResponse{Pools: []*QueryDerivedPoolResponse{{Asset: "THOR.BTC"}}}
	lastBlocks := &QueryLastBlocksResponse{LastBlocks: []*ChainsLastBlock{{Chain: "BTC", LastObservedIn: 7}}}
	lps := &QueryLiquidityProvidersResponse{LiquidityProviders: []*QueryLiquidityProviderResponse{{Asset: "BTC.BTC"}}}
	nodes := &QueryNodesResponse{Nodes: []*QueryNodeResponse{node}}
	fees := &QueryOutboundFeesResponse{OutboundFees: []*QueryOutboundFeeResponse{{Asset: "ETH.ETH", OutboundFee: "1"}}}
	outbound := &QueryOutboundResponse{TxOutItems: []*QueryTxOutItem{{Chain: "BTC", Height: 3}}}
	pools := &QueryPoolsResponse{Pools: []*QueryPoolResponse{pool}}
	rps := &QueryRuneProvidersResponse{Providers: []*QueryRuneProviderResponse{{RuneAddress: "thor1"}}}
	savers := &QuerySaversResponse{Savers: []*QuerySaverResponse{{Asset: "BTC.BTC"}}}
	streams := &QueryStreamingSwapsResponse{StreamingSwaps: []*QueryStreamingSwapResponse{{TxId: "abc", Interval: 2}}}
	tas := &QueryTradeAccountsResponse{TradeAccounts: []*QueryTradeAccountResponse{{Asset: "BTC~BTC"}}}
	tus := &QueryTradeUnitsResponse{TradeUnits: []*QueryTradeUnitResponse{{Asset: "BTC~BTC"}}}
	sas := &QuerySecuredAssetsResponse{Assets: []*QuerySecuredAssetResponse{{Asset: "BTC-BTC"}}}
	metrics := &QueryTssKeygenMetricResponse{Metrics: []*TssKeygenMetric{{PubKey: "pk"}}}
	props := &QueryUpgradeProposalsResponse{UpgradeProposals: []*QueryUpgradeProposalResponse{{Name: "v1", Height: 9}}}
	votes := &QueryUpgradeVotesResponse{UpgradeVotes: []*UpgradeVote{{Vote: "approve"}}}
	mimirKey := &QueryMimirWithKeyResponse{Value: 42}
	ragnarok := &QueryRagnarokResponse{InProgress: true}

	cases := []tc{
		{"solVaultNonce", &QuerySolVaultNonceResponse{}, &QuerySolVaultNonceResponse{}},
		{"asgard", asgard, asgard.AsgardVaults},
		{"balanceModule", &QueryBalanceModuleResponse{Name: "reserve"}, &QueryBalanceModuleResponse{Name: "reserve"}},
		{"banVoter", &BanVoter{BlockHeight: 4}, &BanVoter{BlockHeight: 4}},
		{"derivedPool", &QueryDerivedPoolResponse{Asset: "THOR.BTC"}, &QueryDerivedPoolResponse{Asset: "THOR.BTC"}},
		{"derivedPools", derived, derived.Pools},
		{"keygen", &QueryKeygenResponse{Signature: "sig"}, &QueryKeygenResponse{Signature: "sig"}},
		{"keysign", &QueryKeysignResponse{Signature: "sig"}, &QueryKeysignResponse{Signature: "sig"}},
		{"lastBlocks", lastBlocks, lastBlocks.LastBlocks},
		{"lp", &QueryLiquidityProviderResponse{Asset: "BTC.BTC"}, &QueryLiquidityProviderResponse{Asset: "BTC.BTC"}},
		{"lps", lps, lps.LiquidityProviders},
		{"mimirNodesAll", &QueryMimirNodesAllValuesResponse{}, &QueryMimirNodesAllValuesResponse{}},
		{"mimirKey", mimirKey, mimirKey.Value},
		{"node", node, node},
		{"nodes", nodes, nodes.Nodes},
		{"observedTxVoter", &QueryObservedTxVoter{TxID: "abc", Height: 2}, &QueryObservedTxVoter{TxID: "abc", Height: 2}},
		{"outboundFees", fees, fees.OutboundFees},
		{"outbound", outbound, outbound.TxOutItems},
		{"pool", pool, pool},
		{"pools", pools, pools.Pools},
		{"queue", &QueryQueueResponse{Swap: 3}, &QueryQueueResponse{Swap: 3}},
		{"quoteSwap", &QueryQuoteSwapResponse{Memo: "m"}, &QueryQuoteSwapResponse{Memo: "m"}},
		{"ragnarok", ragnarok, ragnarok.InProgress},
		{"runeProvider", &QueryRuneProviderResponse{RuneAddress: "thor1"}, &QueryRuneProviderResponse{RuneAddress: "thor1"}},
		{"runeProviders", rps, rps.Providers},
		{"saver", &QuerySaverResponse{Asset: "BTC.BTC"}, &QuerySaverResponse{Asset: "BTC.BTC"}},
		{"savers", savers, savers.Savers},
		{"supply", &QuerySupplyResponse{}, &QuerySupplyResponse{}},
		{"streamingSwap", &QueryStreamingSwapResponse{TxId: "abc"}, &QueryStreamingSwapResponse{TxId: "abc"}},
		{"streamingSwaps", streams, streams.StreamingSwaps},
		{"thorname", &QueryThornameResponse{Name: "n"}, &QueryThornameResponse{Name: "n"}},
		{"tradeAccounts", tas, tas.TradeAccounts},
		{"tradeUnits", tus, tus.TradeUnits},
		{"securedAssets", sas, sas.Assets},
		{"keygenMetrics", metrics, metrics.Metrics},
		{"tssMetric", &QueryTssMetricResponse{}, &QueryTssMetricResponse{}},
		{"tx", &QueryTxResponse{}, &QueryTxResponse{}},
		{"upgradeProposal", &QueryUpgradeProposalResponse{Name: "v1"}, &QueryUpgradeProposalResponse{Name: "v1"}},
		{"upgradeProposals", props, props.UpgradeProposals},
		{"upgradeVotes", votes, votes.UpgradeVotes},
		{"vault", &QueryVaultResponse{BlockHeight: 7}, &QueryVaultResponse{BlockHeight: 7}},
		{"version", &QueryVersionResponse{Current: "1.0.0"}, &QueryVersionResponse{Current: "1.0.0"}},
	}
	for _, t := range cases {
		c.Check(marshalCov(c, t.m), Equals, mustIndentCov(c, t.expected), Commentf(t.name))
	}

	clout := NewSwapperClout(GetRandomBTCAddress())
	c.Check(marshalCov(c, &clout), Equals, mustIndentCov(c, &clout))
}

func (s *JSONPBMarshalersCovSuite) TestSpecialMarshalers(c *C) {
	// export returns raw content
	c.Assert(marshalCov(c, &QueryExportResponse{Content: []byte(`{"a":1}`)}), Equals, `{"a":1}`)

	// account response is wrapped
	var acct map[string]map[string]map[string]any
	decodeCov(c, marshalCov(c, &QueryAccountResponse{Address: "thor1x", Sequence: "3"}), &acct)
	c.Assert(acct["result"]["value"]["address"], Equals, "thor1x")
	c.Assert(acct["result"]["value"]["sequence"], Equals, "3")

	var bal map[string][]map[string]string
	decodeCov(c, marshalCov(c, &QueryBalancesResponse{Balances: []*Amount{{Denom: "rune", Amount: "5"}}}), &bal)
	c.Assert(bal["result"], DeepEquals, []map[string]string{{"denom": "rune", "amount": "5"}})

	// inbound addresses: nil -> [] ; otherwise the list
	c.Assert(marshalCov(c, &QueryInboundAddressesResponse{}), Equals, "[]")
	inb := &QueryInboundAddressesResponse{InboundAddresses: []*QueryInboundAddressResponse{{Chain: "BTC"}}}
	c.Assert(marshalCov(c, inb), Equals, mustIndentCov(c, inb.InboundAddresses))
}

func (s *JSONPBMarshalersCovSuite) TestConstantsAndMimirMarshalers(c *C) {
	cv := &QueryConstantValuesResponse{
		Int_64Values: []*Int64Constants{{Name: "A", Value: 1}},
		BoolValues:   []*BoolConstants{{Name: "B", Value: true}},
		StringValues: []*StringConstants{{Name: "C", Value: "x"}},
	}
	var out struct {
		Int64Values  map[string]int64  `json:"int_64_values"`
		BoolValues   map[string]bool   `json:"bool_values"`
		StringValues map[string]string `json:"string_values"`
	}
	decodeCov(c, marshalCov(c, cv), &out)
	c.Assert(out.Int64Values, DeepEquals, map[string]int64{"A": 1})
	c.Assert(out.BoolValues, DeepEquals, map[string]bool{"B": true})
	c.Assert(out.StringValues, DeepEquals, map[string]string{"C": "x"})

	mimirs := []*Mimir{{Key: "K1", Value: 1}, {Key: "K2", Value: -1}}
	expected := map[string]int64{"K1": 1, "K2": -1}
	for i, m := range []jsonpb.JSONPBMarshaler{
		&QueryMimirAdminValuesResponse{AdminMimirs: mimirs},
		&QueryMimirNodesValuesResponse{Mimirs: mimirs},
		&QueryMimirValuesResponse{Mimirs: mimirs},
	} {
		var got map[string]int64
		decodeCov(c, marshalCov(c, m), &got)
		c.Check(got, DeepEquals, expected, Commentf("case %d", i))
	}
}

func (s *JSONPBMarshalersCovSuite) TestPoolSlipsMarshaler(c *C) {
	one := &QueryPoolSlipsResponse{PoolSlips: []*QueryPoolSlipResponse{{Asset: "BTC.BTC", PoolSlip: 1, RollupCount: 2, LongRollup: 3, Rollup: 4, SummedRollup: 0}}}
	var res []openapi.PoolSlipResponseInner
	decodeCov(c, marshalCov(c, one), &res)
	c.Assert(res, HasLen, 1)
	c.Assert(res[0].Asset, Equals, "BTC.BTC")
	c.Assert(res[0].PoolSlip, Equals, int64(1))
	c.Assert(res[0].RollupCount, Equals, int64(2))
	c.Assert(res[0].LongRollup, Equals, int64(3))
	c.Assert(res[0].Rollup, Equals, int64(4))
	// single asset queries always include summed rollup, even if zero
	c.Assert(res[0].SummedRollup, NotNil)
	c.Assert(*res[0].SummedRollup, Equals, int64(0))

	two := &QueryPoolSlipsResponse{PoolSlips: []*QueryPoolSlipResponse{{Asset: "A", SummedRollup: 5}, {Asset: "B"}}}
	res = nil
	decodeCov(c, marshalCov(c, two), &res)
	c.Assert(res, HasLen, 2)
	c.Assert(res[0].SummedRollup, IsNil)
	c.Assert(res[1].Asset, Equals, "B")
}

func swapForMarshalCov() *MsgSwap {
	limit := cosmos.NewUint(77)
	tx := GetRandomTx()
	tx.Memo = "=:ETH.ETH"
	return &MsgSwap{
		Tx:                      tx,
		TargetAsset:             common.ETHAsset,
		Destination:             GetRandomETHAddress(),
		TradeTarget:             cosmos.NewUint(10),
		AffiliateAddress:        GetRandomTHORAddress(),
		AffiliateBasisPoints:    cosmos.NewUint(5),
		Signer:                  GetRandomBech32Addr(),
		Aggregator:              "agg",
		AggregatorTargetAddress: "aggaddr",
		AggregatorTargetLimit:   &limit,
		SwapType:                SwapType_limit,
		StreamQuantity:          3,
		StreamInterval:          1,
		InitialBlockHeight:      12,
	}
}

func (s *JSONPBMarshalersCovSuite) TestSwapQueueMarshalers(c *C) {
	limitSwap := swapForMarshalCov()
	marketSwap := swapForMarshalCov()
	marketSwap.SwapType = SwapType_market
	marketSwap.AggregatorTargetLimit = nil
	marketSwap.StreamQuantity = 0

	var res []openapi.MsgSwap
	decodeCov(c, marshalCov(c, &QuerySwapQueueResponse{SwapQueue: []*MsgSwap{limitSwap, marketSwap}}), &res)
	c.Assert(res, HasLen, 2)
	c.Assert(res[0].TargetAsset, Equals, "ETH.ETH")
	c.Assert(*res[0].SwapType, Equals, "limit")
	c.Assert(*res[0].AggregatorTargetLimit, Equals, "77")
	c.Assert(*res[0].StreamQuantity, Equals, int64(3))
	c.Assert(*res[0].Tx.Id, Equals, limitSwap.Tx.ID.String())
	c.Assert(*res[0].Tx.Memo, Equals, "=:ETH.ETH")
	c.Assert(res[0].Tx.Coins, HasLen, len(limitSwap.Tx.Coins))
	c.Assert(res[1].SwapType, IsNil)
	c.Assert(res[1].AggregatorTargetLimit, IsNil)
	c.Assert(res[1].StreamQuantity, IsNil)

	type page struct {
		SwapQueue  []openapi.MsgSwap `json:"swap_queue"`
		Pagination *struct {
			Offset  uint64 `json:"offset"`
			Limit   uint64 `json:"limit"`
			Total   uint64 `json:"total"`
			HasNext bool   `json:"has_next"`
			HasPrev bool   `json:"has_prev"`
		} `json:"pagination"`
	}
	var p page
	decodeCov(c, marshalCov(c, &QuerySwapQueuePaginatedResponse{
		SwapQueue:  []*MsgSwap{limitSwap},
		Pagination: &PaginationMeta{Offset: 1, Limit: 2, Total: 3, HasNext: true},
	}), &p)
	c.Assert(p.SwapQueue, HasLen, 1)
	c.Assert(p.Pagination, NotNil)
	c.Assert(p.Pagination.Offset, Equals, uint64(1))
	c.Assert(p.Pagination.Limit, Equals, uint64(2))
	c.Assert(p.Pagination.Total, Equals, uint64(3))
	c.Assert(p.Pagination.HasNext, Equals, true)
	c.Assert(p.Pagination.HasPrev, Equals, false)

	p = page{}
	decodeCov(c, marshalCov(c, &QuerySwapQueuePaginatedResponse{}), &p)
	c.Assert(p.SwapQueue, HasLen, 0)
	c.Assert(p.Pagination, IsNil)
}

func (s *JSONPBMarshalersCovSuite) TestSwapDetailsMarshaler(c *C) {
	var res openapi.SwapDetailsResponse
	decodeCov(c, marshalCov(c, &QuerySwapDetailsResponse{Status: "not_found"}), &res)
	c.Assert(res.Swap, IsNil)
	c.Assert(*res.Status, Equals, "not_found")
	c.Assert(res.QueueType, IsNil)

	swap := swapForMarshalCov()
	swap.State = &SwapState{
		Interval: 1, Quantity: 2, Ttl: 3, Count: 4, LastHeight: 5,
		Deposit: cosmos.NewUint(6), Withdrawn: cosmos.NewUint(7), In: cosmos.NewUint(8), Out: cosmos.NewUint(9),
		FailedSwaps: []uint64{1, 2}, FailedSwapReasons: []string{"a", "b"},
	}
	res = openapi.SwapDetailsResponse{}
	decodeCov(c, marshalCov(c, &QuerySwapDetailsResponse{Swap: swap, Status: "pending", QueueType: "advanced"}), &res)
	c.Assert(res.Swap, NotNil)
	c.Assert(*res.Swap.SwapType, Equals, "limit")
	c.Assert(*res.Swap.InitialBlockHeight, Equals, int64(12))
	c.Assert(*res.QueueType, Equals, "advanced")
	st := res.Swap.State
	c.Assert(st, NotNil)
	c.Assert(*st.Interval, Equals, int64(1))
	c.Assert(*st.Ttl, Equals, int64(3))
	c.Assert(*st.LastHeight, Equals, int64(5))
	c.Assert(*st.Deposit, Equals, "6")
	c.Assert(*st.Out, Equals, "9")
	c.Assert(st.FailedSwaps, DeepEquals, []int64{1, 2})
	c.Assert(st.FailedSwapReasons, DeepEquals, []string{"a", "b"})

	// market swap with no state
	swap.SwapType = SwapType_market
	swap.State = nil
	res = openapi.SwapDetailsResponse{}
	decodeCov(c, marshalCov(c, &QuerySwapDetailsResponse{Swap: swap}), &res)
	c.Assert(res.Swap.SwapType, IsNil)
	c.Assert(res.Swap.State, IsNil)
	c.Assert(res.Status, IsNil)
}

func (s *JSONPBMarshalersCovSuite) TestTxStagesMarshaler(c *C) {
	// incomplete inbound observation returns early with Started set
	var res openapi.TxStagesResponse
	decodeCov(c, marshalCov(c, &QueryTxStagesResponse{InboundObserved: InboundObservedStage{PreConfirmationCount: 2, FinalCount: 1}}), &res)
	c.Assert(res.InboundObserved.Completed, Equals, false)
	c.Assert(res.InboundObserved.Started, NotNil)
	c.Assert(*res.InboundObserved.Started, Equals, false)
	c.Assert(*res.InboundObserved.PreConfirmationCount, Equals, int64(2))
	c.Assert(res.InboundObserved.FinalCount, Equals, int64(1))
	c.Assert(res.InboundConfirmationCounted, IsNil)

	full := &QueryTxStagesResponse{
		InboundObserved: InboundObservedStage{Completed: true, FinalCount: 3},
		InboundConfirmationCounted: &InboundConfirmationCountedStage{
			CountingStartHeight: 1, Chain: "BTC", ExternalObservedHeight: 2,
			ExternalConfirmationDelayHeight: 3, RemainingConfirmationSeconds: 4,
		},
		InboundFinalised: &InboundFinalisedStage{Completed: true},
		SwapStatus:       &SwapStatus{Pending: true, Streaming: &StreamingStatus{Interval: 1, Quantity: 2, Count: 3}},
		SwapFinalised:    &SwapFinalisedStage{Completed: false},
		OutboundDelay:    &OutboundDelayStage{RemainingDelayBlocks: 5, RemainingDelaySeconds: 30},
		OutboundSigned:   &OutboundSignedStage{ScheduledOutboundHeight: 9, BlocksSinceScheduled: &ProtoInt64{Value: 2}},
	}
	res = openapi.TxStagesResponse{}
	decodeCov(c, marshalCov(c, full), &res)
	c.Assert(res.InboundObserved.Started, IsNil)
	icc := res.InboundConfirmationCounted
	c.Assert(icc, NotNil)
	c.Assert(*icc.Chain, Equals, "BTC")
	c.Assert(*icc.ExternalConfirmationDelayHeight, Equals, int64(3))
	c.Assert(*icc.RemainingConfirmationSeconds, Equals, int64(4))
	c.Assert(res.InboundFinalised.Completed, Equals, true)
	c.Assert(res.SwapStatus.Pending, Equals, true)
	c.Assert(res.SwapStatus.Streaming.Count, Equals, int64(3))
	c.Assert(res.SwapFinalised.Completed, Equals, false)
	c.Assert(*res.OutboundDelay.RemainingDelayBlocks, Equals, int64(5))
	c.Assert(*res.OutboundDelay.RemainingDelaySeconds, Equals, int64(30))
	c.Assert(*res.OutboundSigned.ScheduledOutboundHeight, Equals, int64(9))
	c.Assert(*res.OutboundSigned.BlocksSinceScheduled, Equals, int64(2))

	// completed outbound stages omit the remaining/scheduled values
	full.OutboundDelay = &OutboundDelayStage{Completed: true, RemainingDelayBlocks: 5}
	full.OutboundSigned = &OutboundSignedStage{Completed: true, ScheduledOutboundHeight: 9}
	full.SwapStatus.Streaming = nil
	res = openapi.TxStagesResponse{}
	decodeCov(c, marshalCov(c, full), &res)
	c.Assert(res.OutboundDelay.RemainingDelayBlocks, IsNil)
	c.Assert(res.OutboundSigned.ScheduledOutboundHeight, IsNil)
	c.Assert(res.SwapStatus.Streaming, IsNil)

	// incomplete signed stage without blocks-since-scheduled
	full.OutboundSigned = &OutboundSignedStage{ScheduledOutboundHeight: 9}
	res = openapi.TxStagesResponse{}
	decodeCov(c, marshalCov(c, full), &res)
	c.Assert(*res.OutboundSigned.ScheduledOutboundHeight, Equals, int64(9))
	c.Assert(res.OutboundSigned.BlocksSinceScheduled, IsNil)
}

func (s *JSONPBMarshalersCovSuite) TestTxStatusMarshaler(c *C) {
	var res openapi.TxStatusResponse
	decodeCov(c, marshalCov(c, &QueryTxStatusResponse{}), &res)
	c.Assert(res.Tx, IsNil)
	c.Assert(res.PlannedOutTxs, IsNil)
	c.Assert(res.OutTxs, IsNil)

	tx := GetRandomTx()
	out := GetRandomTx()
	out.Coins = nil
	coin := common.NewCoin(common.BTCAsset, cosmos.NewUint(5))
	coin.Decimals = 8
	m := &QueryTxStatusResponse{
		Tx:            &tx,
		PlannedOutTxs: []*PlannedOutTx{{Chain: "BTC", ToAddress: "addr", Coin: &coin, Refund: true}},
		OutTxs:        common.Txs{out},
		Stages:        QueryTxStagesResponse{InboundObserved: InboundObservedStage{Completed: true}},
	}
	res = openapi.TxStatusResponse{}
	decodeCov(c, marshalCov(c, m), &res)
	c.Assert(*res.Tx.Id, Equals, tx.ID.String())
	c.Assert(*res.Tx.Chain, Equals, tx.Chain.String())
	c.Assert(res.PlannedOutTxs, HasLen, 1)
	c.Assert(res.PlannedOutTxs[0].Refund, Equals, true)
	c.Assert(res.PlannedOutTxs[0].Coin.Asset, Equals, "BTC.BTC")
	c.Assert(res.PlannedOutTxs[0].Coin.Amount, Equals, "5")
	c.Assert(*res.PlannedOutTxs[0].Coin.Decimals, Equals, int64(8))
	c.Assert(res.OutTxs, HasLen, 1)
	c.Assert(res.OutTxs[0].Coins, IsNil)
	c.Assert(res.Stages.InboundObserved.Completed, Equals, true)
}

func (s *JSONPBMarshalersCovSuite) TestBlockMarshaler(c *C) {
	ev := func(k, v string) *BlockEvent {
		return &BlockEvent{EventKvPair: []*EventKeyValuePair{{Key: "type", Value: k}, {Key: "attr", Value: v}}}
	}
	m := &QueryBlockResponse{
		Id: &BlockResponseId{Hash: "H", Parts: &BlockResponseIdParts{Total: 1, Hash: "PH"}},
		Header: &BlockResponseHeader{
			Version:     &BlockResponseHeaderVersion{Block: "11", App: "0"},
			ChainId:     "thorchain",
			Height:      100,
			Time:        "now",
			LastBlockId: &BlockResponseId{Hash: "LH", Parts: &BlockResponseIdParts{Total: 2, Hash: "LPH"}},
			AppHash:     "APP",
		},
		BeginBlockEvents:    []*BlockEvent{ev("begin", "1")},
		EndBlockEvents:      []*BlockEvent{ev("end", "2")},
		FinalizeBlockEvents: []*BlockEvent{ev("fin", "3")},
		Txs: []*QueryBlockTx{{
			Hash:   "TXH",
			Tx:     []byte(`"raw-tx"`),
			Result: &BlockTxResult{Code: 0, Log: "ok", GasUsed: "10", Events: []*BlockEvent{ev("tx", "4")}},
		}},
	}
	var res struct {
		openapi.BlockResponse
		Txs []struct {
			Hash   string          `json:"hash"`
			Tx     json.RawMessage `json:"tx"`
			Result struct {
				Code    *int64              `json:"code"`
				Log     *string             `json:"log"`
				Data    *string             `json:"data"`
				GasUsed *string             `json:"gas_used"`
				Events  []map[string]string `json:"events"`
			} `json:"result"`
		} `json:"txs"`
	}
	decodeCov(c, marshalCov(c, m), &res)
	c.Assert(res.Id.Hash, Equals, "H")
	c.Assert(res.Id.Parts.Total, Equals, int64(1))
	c.Assert(res.Header.ChainId, Equals, "thorchain")
	c.Assert(res.Header.Height, Equals, int64(100))
	c.Assert(res.Header.Version.Block, Equals, "11")
	c.Assert(res.Header.LastBlockId.Parts.Hash, Equals, "LPH")
	c.Assert(res.Header.AppHash, Equals, "APP")
	c.Assert(res.BeginBlockEvents, DeepEquals, []map[string]string{{"type": "begin", "attr": "1"}})
	c.Assert(res.EndBlockEvents, DeepEquals, []map[string]string{{"type": "end", "attr": "2"}})
	c.Assert(res.FinalizeBlockEvents, DeepEquals, []map[string]string{{"type": "fin", "attr": "3"}})
	c.Assert(res.Txs, HasLen, 1)
	c.Assert(res.Txs[0].Hash, Equals, "TXH")
	c.Assert(string(res.Txs[0].Tx), Equals, `"raw-tx"`)
	c.Assert(*res.Txs[0].Result.Code, Equals, int64(0))
	c.Assert(*res.Txs[0].Result.Log, Equals, "ok")
	c.Assert(res.Txs[0].Result.Data, IsNil)
	c.Assert(*res.Txs[0].Result.GasUsed, Equals, "10")
	c.Assert(res.Txs[0].Result.Events, DeepEquals, []map[string]string{{"type": "tx", "attr": "4"}})
}

func (s *JSONPBMarshalersCovSuite) TestHelpers(c *C) {
	c.Assert(wrapString(""), IsNil)
	c.Assert(*wrapString("a"), Equals, "a")
	c.Assert(wrapInt64(0), IsNil)
	c.Assert(*wrapInt64(3), Equals, int64(3))
	c.Assert(wrapUintPtr(nil), IsNil)
	u := cosmos.NewUint(4)
	c.Assert(*wrapUintPtr(&u), Equals, "4")
	c.Assert(castCoins(), IsNil)
	c.Assert(convertUint64SliceToInt64(nil), IsNil)
	c.Assert(convertUint64SliceToInt64([]uint64{5}), DeepEquals, []int64{5})

	// jsonify surfaces marshal errors
	_, err := jsonify(map[string]any{"bad": make(chan int)})
	c.Assert(err, ErrorMatches, "fail to marshal response to json.*")
}
