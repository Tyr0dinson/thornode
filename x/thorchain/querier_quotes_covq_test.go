package thorchain

import (
	"strings"
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// covQQuoteEnv sets up an active node, an asgard vault and deep pools for
// the UTXO chains so swap quotes can be simulated end to end.
func covQQuoteEnv(c *C, ctx cosmos.Context, mgr *Mgrs) {
	k := mgr.Keeper()
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	chains := common.Chains{
		common.BTCChain, common.ETHChain, common.LTCChain,
		common.DOGEChain, common.BCHChain, common.ZECChain,
	}
	asgard := NewVaultV2(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(),
		chains.Strings(), []ChainContract{}, GetRandomEd25519PubKey())
	c.Assert(k.SetVault(ctx, asgard), IsNil)

	for _, chain := range chains {
		pool := NewPool()
		pool.Asset = chain.GetGasAsset()
		pool.BalanceAsset = cosmos.NewUint(1_000_000 * common.One)
		pool.BalanceRune = cosmos.NewUint(10_000_000 * common.One)
		pool.LPUnits = cosmos.NewUint(100)
		pool.Status = PoolAvailable
		pool.Decimals = 8
		c.Assert(k.SetPool(ctx, pool), IsNil)
		c.Assert(k.SaveNetworkFee(ctx, chain, NetworkFee{
			Chain:              chain,
			TransactionSize:    250,
			TransactionFeeRate: 10,
		}), IsNil)
	}
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
}

func (s *QuerierCovQSuite) TestQuoteLimitValidationCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	base := types.QueryQuoteLimitRequest{
		FromAsset:   "BTC.BTC",
		ToAsset:     "ETH.ETH",
		Amount:      "100000",
		Destination: GetRandomETHAddress().String(),
		TargetOut:   "5000",
	}

	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)
	_, err := s.qs.QuoteLimit(ctx, &base)
	c.Assert(err, ErrorMatches, "limit orders require advanced swap queue to be enabled")
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	maxAge := k.GetConfigInt64(ctx, constants.StreamingLimitSwapMaxAge)

	cases := []struct {
		mod func(r *types.QueryQuoteLimitRequest)
		err string
	}{
		{func(r *types.QueryQuoteLimitRequest) { r.FromAsset = "" }, "missing from_asset parameter"},
		{func(r *types.QueryQuoteLimitRequest) { r.ToAsset = "" }, "missing to_asset parameter"},
		{func(r *types.QueryQuoteLimitRequest) { r.Amount = "" }, "missing amount parameter"},
		{func(r *types.QueryQuoteLimitRequest) { r.Destination = "" }, "missing destination parameter"},
		{func(r *types.QueryQuoteLimitRequest) { r.TargetOut = "" }, "missing target_out parameter"},
		{func(r *types.QueryQuoteLimitRequest) { r.FromAsset = ".." }, "bad from_asset.*"},
		{func(r *types.QueryQuoteLimitRequest) { r.ToAsset = ".." }, "bad to_asset.*"},
		{func(r *types.QueryQuoteLimitRequest) { r.Amount = "x" }, "bad amount.*"},
		{func(r *types.QueryQuoteLimitRequest) { r.Amount = "0" }, "amount must be positive"},
		{func(r *types.QueryQuoteLimitRequest) { r.TargetOut = "x" }, "bad target_out.*"},
		{func(r *types.QueryQuoteLimitRequest) { r.TargetOut = "0" }, "target_out must be positive"},
		{func(r *types.QueryQuoteLimitRequest) { r.CustomTtl = "x" }, "bad custom_ttl.*"},
		{func(r *types.QueryQuoteLimitRequest) { r.CustomTtl = "0" }, "custom_ttl must be positive"},
		{
			func(r *types.QueryQuoteLimitRequest) { r.CustomTtl = cosmos.NewUint(uint64(maxAge) + 1).String() },
			"custom_ttl must be less than or equal to the max age.*",
		},
		{func(r *types.QueryQuoteLimitRequest) { r.StreamingQuantity = "x" }, "bad streaming_quantity.*"},
		{func(r *types.QueryQuoteLimitRequest) { r.AffiliateBps = "10" }, "bad affiliate params.*"},
	}
	for i, tc := range cases {
		req := base
		tc.mod(&req)
		_, err = s.qs.QuoteLimit(ctx, &req)
		c.Assert(err, NotNil, Commentf("case %d", i))
		c.Assert(err, ErrorMatches, tc.err, Commentf("case %d", i))
	}
}

func (s *QuerierCovQSuite) TestQuoteSwapValidationCovQ(c *C) {
	ctx := s.ctx
	base := types.QueryQuoteSwapRequest{
		FromAsset: "BTC.BTC",
		ToAsset:   "ETH.ETH",
		Amount:    "100000",
	}
	cases := []struct {
		mod func(r *types.QueryQuoteSwapRequest)
		err string
	}{
		{func(r *types.QueryQuoteSwapRequest) { r.FromAsset = "" }, "missing from_asset parameter"},
		{func(r *types.QueryQuoteSwapRequest) { r.ToAsset = "" }, "missing to_asset parameter"},
		{func(r *types.QueryQuoteSwapRequest) { r.Amount = "" }, "missing Amount parameter"},
		{
			func(r *types.QueryQuoteSwapRequest) { r.ToleranceBps = "1"; r.LiquidityToleranceBps = "1" },
			"must only include one of: tolerance_bps or liquidity_tolerance_bps",
		},
		{func(r *types.QueryQuoteSwapRequest) { r.FromAsset = ".." }, "bad from asset.*"},
		{func(r *types.QueryQuoteSwapRequest) { r.ToAsset = ".." }, "bad to asset.*"},
		{func(r *types.QueryQuoteSwapRequest) { r.Amount = "x" }, "bad amount.*"},
		{func(r *types.QueryQuoteSwapRequest) { r.Amount = "1" }, "amount less than dust threshold"},
		{func(r *types.QueryQuoteSwapRequest) { r.StreamingInterval = "x" }, "bad streaming interval amount.*"},
		{func(r *types.QueryQuoteSwapRequest) { r.StreamingQuantity = "x" }, "bad streaming quantity amount.*"},
		{func(r *types.QueryQuoteSwapRequest) { r.AffiliateBps = "10" }, "bad affiliate params.*"},
	}
	for i, tc := range cases {
		req := base
		tc.mod(&req)
		_, err := s.qs.QuoteSwap(ctx, &req)
		c.Assert(err, NotNil, Commentf("case %d", i))
		c.Assert(err, ErrorMatches, tc.err, Commentf("case %d", i))
	}

	// stale state is refused unless a height is explicitly requested
	stale := ctx.WithBlockTime(time.Unix(1, 0))
	_, err := s.qs.QuoteSwap(stale, &base)
	c.Assert(err, ErrorMatches, "refusing quote on node with stale state")
}

func (s *QuerierCovQSuite) TestQuoteSwapToleranceCovQ(c *C) {
	ctx := s.ctx
	covQQuoteEnv(c, ctx, s.mgr)
	dest := GetRandomTHORAddress().String()

	for _, tc := range []struct{ tol, liqTol, err string }{
		{"x", "", "bad tolerance basis points.*"},
		{"10001", "", "tolerance basis points must be less than 10000"},
		{"", "x", "bad liquidity tolerance basis points.*"},
		{"", "10000", "liquidity tolerance basis points must be less than 10000"},
	} {
		_, err := s.qs.QuoteSwap(ctx, &types.QueryQuoteSwapRequest{
			FromAsset: "BTC.BTC", ToAsset: "THOR.RUNE", Amount: "10000000",
			Destination: dest, ToleranceBps: tc.tol, LiquidityToleranceBps: tc.liqTol,
		})
		c.Assert(err, ErrorMatches, tc.err)
	}

	// liquidity tolerance writes an output limit into the memo
	resp, err := s.qs.QuoteSwap(ctx, &types.QueryQuoteSwapRequest{
		FromAsset: "BTC.BTC", ToAsset: "THOR.RUNE", Amount: "10000000",
		Destination: dest, LiquidityToleranceBps: "500",
	})
	c.Assert(err, IsNil)
	expOut := cosmos.NewUintFromString(resp.ExpectedAmountOut)
	limit := expOut.MulUint64(9500).QuoUint64(10000)
	c.Assert(strings.Contains(resp.Memo, dest), Equals, true)
	c.Assert(limit.IsZero(), Equals, false)
	parts := strings.Split(resp.Memo, ":")
	c.Assert(len(parts) >= 4, Equals, true)
	// short-form memo encodes the limit in scientific shorthand, so just check
	// it is set
	c.Assert(parts[3], Not(Equals), "")
	c.Assert(parts[3], Not(Equals), "0")
}

func (s *QuerierCovQSuite) TestQuoteSwapExtendedUTXOCovQ(c *C) {
	ctx := s.ctx
	covQQuoteEnv(c, ctx, s.mgr)
	dest := GetRandomTHORAddress().String()
	aff := GetRandomTHORAddress().String() + "/" + GetRandomTHORAddress().String()

	for _, chain := range []common.Chain{common.LTCChain, common.DOGEChain, common.BCHChain, common.ZECChain} {
		resp, err := s.qs.QuoteSwap(ctx, &types.QueryQuoteSwapRequest{
			FromAsset:    chain.GetGasAsset().String(),
			ToAsset:      "THOR.RUNE",
			Amount:       "100000000",
			Destination:  dest,
			Affiliate:    aff,
			AffiliateBps: "10/10",
			Extended:     true,
		})
		c.Assert(err, IsNil, Commentf("chain %s", chain))
		c.Assert(len(resp.Memo) > 80, Equals, true, Commentf("chain %s", chain))
		parts := splitMemo(resp.Memo)
		c.Assert(resp.Vout, HasLen, len(parts), Commentf("chain %s", chain))
		c.Assert(resp.Vout[0].Type, Equals, "op_return")
		c.Assert(resp.Vout[0].Data, Equals, parts[0])
		for _, v := range resp.Vout[1:] {
			c.Assert(v.Type, Equals, "address", Commentf("chain %s", chain))
			c.Assert(v.Data, Not(Equals), "")
			c.Assert(v.Amount, Equals, chain.P2WPKHOutputValue())
		}
		c.Assert(resp.InboundAddress, Not(Equals), "")
		c.Assert(resp.RecommendedGasRate, Not(Equals), "")
	}
}

func (s *QuerierCovQSuite) TestQuoteReverseFuzzyAssetCovQ(c *C) {
	ctx := s.ctx

	// nothing to shorten
	out, err := quoteReverseFuzzyAsset(ctx, s.mgr, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(out.Equals(common.ETHAsset), Equals, true)

	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	covQPool(c, ctx, s.mgr, usdc, 1000, 1000)

	// the only USDC pool: the address is dropped entirely
	out, err = quoteReverseFuzzyAsset(ctx, s.mgr, usdc)
	c.Assert(err, IsNil)
	c.Assert(out.String(), Equals, "ETH.USDC")

	// a second USDC pool sharing a suffix forces the shortest unique suffix
	other, err := common.NewAsset("ETH.USDC-0X9999999999999999999999999999999999999A48")
	c.Assert(err, IsNil)
	covQPool(c, ctx, s.mgr, other, 1000, 1000)
	out, err = quoteReverseFuzzyAsset(ctx, s.mgr, usdc)
	c.Assert(err, IsNil)
	c.Assert(out.String(), Equals, "ETH.USDC-B48")

	c.Assert(hasSuffixMatch("48", []string{"0XA48"}), Equals, true)
	c.Assert(hasSuffixMatch("B48", []string{"0XA48"}), Equals, false)

	// a same-ticker pool without an address makes the match ambiguous
	bare, err := common.NewAsset("ETH.USDC")
	c.Assert(err, IsNil)
	covQPool(c, ctx, s.mgr, bare, 1000, 1000)
	_, err = quoteReverseFuzzyAsset(ctx, s.mgr, usdc)
	c.Assert(err, ErrorMatches, "ambiguous match: USDC")
}

func (s *QuerierCovQSuite) TestQuoteConvertAssetCovQ(c *C) {
	ctx := s.ctx
	covQPool(c, ctx, s.mgr, common.BTCAsset, 1000*common.One, 10*common.One)

	amt, err := quoteConvertAsset(ctx, s.mgr, common.BTCAsset, cosmos.NewUint(common.One), common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(amt.Uint64(), Equals, uint64(common.One))

	amt, err = quoteConvertAsset(ctx, s.mgr, common.BTCAsset, cosmos.NewUint(common.One), common.RuneAsset())
	c.Assert(err, IsNil)
	c.Assert(amt.Uint64(), Equals, uint64(100*common.One))

	amt, err = quoteConvertAsset(ctx, s.mgr, common.RuneAsset(), cosmos.NewUint(100*common.One), common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(amt.Uint64(), Equals, uint64(common.One))

	_, err = quoteConvertAsset(ctx, s.mgr, common.ETHAsset, cosmos.NewUint(1), common.RuneAsset())
	c.Assert(err, ErrorMatches, "pool does not exist")
	_, err = quoteConvertAsset(ctx, s.mgr, common.RuneAsset(), cosmos.NewUint(1), common.ETHAsset)
	c.Assert(err, ErrorMatches, "pool does not exist")
}

func (s *QuerierCovQSuite) TestLimitSwapsSortFilterCovQ(c *C) {
	ctx, k := s.ctx.WithBlockHeight(100), s.mgr.Keeper()

	// disabled queue returns an empty page
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)
	resp, err := s.qs.LimitSwaps(ctx, &types.QueryLimitSwapsRequest{Offset: 2})
	c.Assert(err, IsNil)
	c.Assert(resp.LimitSwaps, HasLen, 0)
	c.Assert(resp.Pagination.Total, Equals, uint64(0))
	c.Assert(resp.Pagination.HasPrev, Equals, true)
	c.Assert(resp.Pagination.Limit > 0, Equals, true)
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	k.SetMimir(ctx, constants.StreamingLimitSwapMaxAge.String(), 80)

	addrA, addrB := GetRandomBTCAddress(), GetRandomBTCAddress()
	mk := func(from common.Address, src, tgt common.Asset, amt, target uint64, height int64) common.TxID {
		id := GetRandomTxHash()
		swap := types.MsgSwap{
			Tx: common.Tx{
				ID:          id,
				Chain:       src.Chain,
				FromAddress: from,
				Coins:       common.Coins{common.NewCoin(src, cosmos.NewUint(amt))},
			},
			TargetAsset:        tgt,
			TradeTarget:        cosmos.NewUint(target),
			SwapType:           types.SwapType_limit,
			InitialBlockHeight: height,
			State: &types.SwapState{
				Deposit: cosmos.NewUint(amt),
				In:      cosmos.ZeroUint(),
				Out:     cosmos.ZeroUint(),
			},
		}
		c.Assert(k.SetAdvSwapQueueItem(ctx, swap), IsNil)
		return id
	}
	idA := mk(addrA, common.BTCAsset, common.ETHAsset, 100, 200, 10) // ratio 2e8, age 90
	idB := mk(addrB, common.BTCAsset, common.ETHAsset, 300, 300, 50) // ratio 1e8, age 50
	idC := mk(addrA, common.ETHAsset, common.BTCAsset, 50, 500, 30)  // ratio 10e8, age 70
	// a market swap is never listed
	market := types.MsgSwap{
		Tx: common.Tx{
			ID: GetRandomTxHash(), Chain: common.BTCChain, FromAddress: addrA,
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(5))},
		},
		TargetAsset: common.ETHAsset,
		SwapType:    types.SwapType_market,
		State:       &types.SwapState{Quantity: 1, Deposit: cosmos.NewUint(5), In: cosmos.ZeroUint(), Out: cosmos.ZeroUint()},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, market), IsNil)

	ids := func(r *types.QueryLimitSwapsResponse) []common.TxID {
		out := []common.TxID{}
		for _, ls := range r.LimitSwaps {
			out = append(out, ls.Swap.Tx.ID)
		}
		return out
	}
	order := map[string][]common.TxID{
		"ratio/asc":           {idB, idA, idC},
		"ratio/desc":          {idC, idA, idB},
		"age/asc":             {idB, idC, idA},
		"age/desc":            {idA, idC, idB},
		"created_height/asc":  {idA, idC, idB},
		"created_height/desc": {idB, idC, idA},
		"amount/asc":          {idC, idA, idB},
		"amount/desc":         {idB, idA, idC},
		"/":                   {idB, idA, idC}, // defaults: ratio asc
	}
	for key, exp := range order {
		sp := strings.Split(key, "/")
		resp, err = s.qs.LimitSwaps(ctx, &types.QueryLimitSwapsRequest{SortBy: sp[0], SortOrder: sp[1]})
		c.Assert(err, IsNil)
		c.Assert(ids(resp), DeepEquals, exp, Commentf("sort %s", key))
	}

	resp, err = s.qs.LimitSwaps(ctx, &types.QueryLimitSwapsRequest{SortBy: "ratio"})
	c.Assert(err, IsNil)
	c.Assert(resp.LimitSwaps[0].Ratio, Equals, cosmos.NewUint(common.One).String())
	c.Assert(resp.LimitSwaps[0].BlocksSinceCreated, Equals, int64(50))
	c.Assert(resp.LimitSwaps[0].TimeToExpiryBlocks, Equals, int64(30))
	// A is older than the max age: expiry floors at zero
	c.Assert(resp.LimitSwaps[1].TimeToExpiryBlocks, Equals, int64(0))

	// filters
	resp, err = s.qs.LimitSwaps(ctx, &types.QueryLimitSwapsRequest{SourceAsset: "btc.btc"})
	c.Assert(err, IsNil)
	c.Assert(ids(resp), DeepEquals, []common.TxID{idB, idA})
	resp, err = s.qs.LimitSwaps(ctx, &types.QueryLimitSwapsRequest{TargetAsset: "BTC.BTC"})
	c.Assert(err, IsNil)
	c.Assert(ids(resp), DeepEquals, []common.TxID{idC})
	resp, err = s.qs.LimitSwaps(ctx, &types.QueryLimitSwapsRequest{Sender: addrB.String()})
	c.Assert(err, IsNil)
	c.Assert(ids(resp), DeepEquals, []common.TxID{idB})

	// pagination
	resp, err = s.qs.LimitSwaps(ctx, &types.QueryLimitSwapsRequest{Offset: 1, Limit: 1})
	c.Assert(err, IsNil)
	c.Assert(ids(resp), DeepEquals, []common.TxID{idA})
	c.Assert(resp.Pagination.Total, Equals, uint64(3))
	c.Assert(resp.Pagination.HasNext, Equals, true)
	c.Assert(resp.Pagination.HasPrev, Equals, true)
	resp, err = s.qs.LimitSwaps(ctx, &types.QueryLimitSwapsRequest{Offset: 5, Limit: 1 << 40})
	c.Assert(err, IsNil)
	c.Assert(resp.LimitSwaps, HasLen, 0)
	c.Assert(resp.Pagination.Limit < 1<<40, Equals, true)
	c.Assert(resp.Pagination.HasNext, Equals, false)

	// summary filters
	covQPool(c, ctx, s.mgr, common.BTCAsset, 1000*common.One, 10*common.One)
	covQPool(c, ctx, s.mgr, common.ETHAsset, 1000*common.One, 10*common.One)
	sum, err := s.qs.LimitSwapsSummary(ctx, &types.QueryLimitSwapsSummaryRequest{SourceAsset: "BTC.BTC"})
	c.Assert(err, IsNil)
	c.Assert(sum.TotalLimitSwaps, Equals, uint64(2))
	c.Assert(sum.OldestSwapBlocks, Equals, int64(90))
	c.Assert(sum.AssetPairs, HasLen, 1)
	c.Assert(sum.AssetPairs[0].Count, Equals, uint64(2))
	sum, err = s.qs.LimitSwapsSummary(ctx, &types.QueryLimitSwapsSummaryRequest{TargetAsset: "BTC.BTC"})
	c.Assert(err, IsNil)
	c.Assert(sum.TotalLimitSwaps, Equals, uint64(1))
	c.Assert(sum.AverageAgeBlocks, Equals, int64(70))
}

func (s *QuerierCovQSuite) TestReferenceMemoPreflightCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	_, err := s.qs.ReferenceMemoPreflight(ctx, &types.QueryReferenceMemoPreflightRequest{Asset: ".."})
	c.Assert(err, ErrorMatches, "invalid asset.*")

	k.SetMimir(ctx, constants.HaltMemoless.String(), 1)
	_, err = s.qs.ReferenceMemoPreflight(ctx, &types.QueryReferenceMemoPreflightRequest{Asset: "BTC.BTC", Amount: "1"})
	c.Assert(err, ErrorMatches, "memoless transactions are currently halted")
	k.SetMimir(ctx, constants.HaltMemoless.String(), 0)

	k.SetMimir(ctx, constants.MemolessTxnTTL.String(), 0)
	_, err = s.qs.ReferenceMemoPreflight(ctx, &types.QueryReferenceMemoPreflightRequest{Asset: "BTC.BTC", Amount: "1"})
	c.Assert(err, ErrorMatches, "memoless transactions are currently disabled")

	k.SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)
	_, err = s.qs.ReferenceMemoPreflight(ctx, &types.QueryReferenceMemoPreflightRequest{Asset: "BTC.BTC", Amount: "x"})
	c.Assert(err, ErrorMatches, "invalid amount.*")

	covQPool(c, ctx, s.mgr, common.BTCAsset, 100*common.One, 100*common.One)
	resp, err := s.qs.ReferenceMemoPreflight(ctx, &types.QueryReferenceMemoPreflightRequest{Asset: "BTC.BTC", Amount: "100012345"})
	c.Assert(err, IsNil)
	c.Assert(resp.Available, Equals, true)
	ref := resp.Reference

	// a live registration makes the reference unavailable
	k.SetReferenceMemo(ctx, ReferenceMemo{
		Asset:     common.BTCAsset,
		Memo:      "=:ETH.ETH:0xabc",
		Reference: ref,
		Height:    ctx.BlockHeight(),
	})
	resp, err = s.qs.ReferenceMemoPreflight(ctx, &types.QueryReferenceMemoPreflightRequest{Asset: "BTC.BTC", Amount: "100012345"})
	c.Assert(err, IsNil)
	c.Assert(resp.Reference, Equals, ref)
	c.Assert(resp.Available, Equals, false)
	c.Assert(resp.CanRegister, Equals, false)
	c.Assert(resp.Memo, Equals, "=:ETH.ETH:0xabc")
	c.Assert(resp.ExpiresAt, Equals, ctx.BlockHeight()+100)
}
