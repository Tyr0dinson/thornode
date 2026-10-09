package thorchain

import (
	"sort"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	ckeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/app/params"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type QuerierCovQSuite struct {
	ctx cosmos.Context
	mgr *Mgrs
	qs  *queryServer
}

var _ = Suite(&QuerierCovQSuite{})

func (s *QuerierCovQSuite) SetUpTest(c *C) {
	s.ctx, s.mgr = setupManagerForTest(c)
	s.qs = covQNewQueryServer(c, s.mgr)
}

func covQNewQueryServer(c *C, mgr *Mgrs) *queryServer {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := ckeys.NewInMemory(cdc)
	_, _, err := kb.NewMnemonic("thorchain", ckeys.English, cmd.THORChainHDPath, "password", hd.Secp256k1)
	c.Assert(err, IsNil)
	txConfig, err := params.TxConfig(cdc, nil)
	c.Assert(err, IsNil)
	qs, ok := NewQueryServerImpl(mgr, txConfig, cosmos.KeybaseStore{
		SignerName:   "thorchain",
		SignerPasswd: "password",
		Keybase:      kb,
	}).(*queryServer)
	c.Assert(ok, Equals, true)
	return qs
}

func covQPool(c *C, ctx cosmos.Context, mgr *Mgrs, asset common.Asset, rune, bal uint64) Pool {
	pool := NewPool()
	pool.Asset = asset
	pool.BalanceRune = cosmos.NewUint(rune)
	pool.BalanceAsset = cosmos.NewUint(bal)
	pool.LPUnits = cosmos.NewUint(rune)
	pool.Status = PoolAvailable
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	return pool
}

func (s *QuerierCovQSuite) TestThornameCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()

	// missing name
	_, err := s.qs.Thorname(ctx, &types.QueryThornameRequest{Name: "missing"})
	c.Assert(err, NotNil)

	covQPool(c, ctx, s.mgr, common.BTCAsset, 1000*common.One, 10*common.One)
	owner := GetRandomBech32Addr()
	btcAddr := GetRandomBTCAddress()
	name := NewTHORName("covq", 1000, []THORNameAlias{{Chain: common.BTCChain, Address: btcAddr}})
	name.Owner = owner
	name.PreferredAsset = common.BTCAsset
	k.SetTHORName(ctx, name)
	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{
		Name:       "covq",
		RuneAmount: cosmos.NewUint(1234),
	}), IsNil)

	resp, err := s.qs.Thorname(ctx, &types.QueryThornameRequest{Name: "covq"})
	c.Assert(err, IsNil)
	c.Assert(resp.Name, Equals, "covq")
	c.Assert(resp.ExpireBlockHeight, Equals, int64(1000))
	c.Assert(resp.Owner, Equals, owner.String())
	c.Assert(resp.PreferredAsset, Equals, common.BTCAsset.String())
	c.Assert(resp.Aliases, HasLen, 1)
	c.Assert(resp.Aliases[0].Chain, Equals, "BTC")
	c.Assert(resp.Aliases[0].Address, Equals, btcAddr.String())
	c.Assert(resp.AffiliateCollectorRune, Equals, "1234")
	// a preferred asset with a pool yields a non-zero swap threshold
	c.Assert(resp.PreferredAssetSwapThresholdRune, Not(Equals), "0")
}

func (s *QuerierCovQSuite) TestXMRSpentOutputRefsCovQ(c *C) {
	ctx := s.ctx
	_, err := s.qs.XMRSpentOutputRefs(ctx, &types.QueryXMRSpentOutputRefsRequest{})
	c.Assert(err, ErrorMatches, "missing vault_pub_key parameter")
	_, err = s.qs.XMRSpentOutputRefs(ctx, &types.QueryXMRSpentOutputRefsRequest{VaultPubKey: "bogus"})
	c.Assert(err, ErrorMatches, "bogus is invalid pubkey")

	pk := GetRandomPubKey()
	resp, err := s.qs.XMRSpentOutputRefs(ctx, &types.QueryXMRSpentOutputRefsRequest{VaultPubKey: pk.String()})
	c.Assert(err, IsNil)
	c.Assert(resp.SpentOutputRefs, IsNil)

	spend := GetRandomTxHash()
	refs := []common.OutputRef{
		{TxHash: "aa", OutputIndex: 1, KeyImage: "k1", SpendTxHash: spend.String()},
		{TxHash: "bb", OutputIndex: 0, KeyImage: "k2", SpendTxHash: spend.String()},
	}
	c.Assert(s.mgr.Keeper().SetXMRSpentOutputRefs(ctx, pk, spend, refs, 10, 12), IsNil)
	resp, err = s.qs.XMRSpentOutputRefs(ctx, &types.QueryXMRSpentOutputRefsRequest{VaultPubKey: pk.String()})
	c.Assert(err, IsNil)
	c.Assert(resp.SpentOutputRefs, HasLen, 2)
	keyImages := []string{resp.SpentOutputRefs[0].KeyImage, resp.SpentOutputRefs[1].KeyImage}
	sort.Strings(keyImages)
	c.Assert(keyImages, DeepEquals, []string{"k1", "k2"})
}

func (s *QuerierCovQSuite) TestRunePoolAndProvidersCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()

	resp, err := s.qs.RunePool(ctx, &types.QueryRunePoolRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp.Pol.RuneDeposited, Equals, "0")
	c.Assert(resp.Providers.Units, Equals, "0")
	c.Assert(resp.Reserve.Units, Equals, "0")

	// provider errors
	_, err = s.qs.RuneProvider(ctx, &types.QueryRuneProviderRequest{})
	c.Assert(err, ErrorMatches, "address not provided")
	_, err = s.qs.RuneProvider(ctx, &types.QueryRuneProviderRequest{Address: "bad"})
	c.Assert(err, ErrorMatches, "unable to decode address")

	runePool := NewRUNEPool()
	runePool.PoolUnits = cosmos.NewUint(100)
	runePool.ReserveUnits = cosmos.NewUint(100)
	runePool.RuneDeposited = cosmos.NewUint(500)
	k.SetRUNEPool(ctx, runePool)

	addr := GetRandomBech32Addr()
	rp := RUNEProvider{
		RuneAddress:        addr,
		DepositAmount:      cosmos.NewUint(500),
		WithdrawAmount:     cosmos.NewUint(20),
		Units:              cosmos.NewUint(100),
		LastDepositHeight:  5,
		LastWithdrawHeight: 7,
	}
	k.SetRUNEProvider(ctx, rp)

	pResp, err := s.qs.RuneProvider(ctx, &types.QueryRuneProviderRequest{Address: addr.String()})
	c.Assert(err, IsNil)
	c.Assert(pResp.RuneAddress, Equals, addr.String())
	c.Assert(pResp.Units, Equals, "100")
	c.Assert(pResp.DepositAmount, Equals, "500")
	c.Assert(pResp.WithdrawAmount, Equals, "20")
	c.Assert(pResp.LastDepositHeight, Equals, int64(5))
	c.Assert(pResp.LastWithdrawHeight, Equals, int64(7))
	// rune pool holds no value, so pnl = 0 - 500 + 20
	c.Assert(pResp.Value, Equals, "0")
	c.Assert(pResp.Pnl, Equals, "-480")

	all, err := s.qs.RuneProviders(ctx, &types.QueryRuneProvidersRequest{})
	c.Assert(err, IsNil)
	c.Assert(all.Providers, HasLen, 1)
	c.Assert(all.Providers[0].RuneAddress, Equals, addr.String())
	c.Assert(all.Providers[0].Pnl, Equals, "-480")

	resp, err = s.qs.RunePool(ctx, &types.QueryRunePoolRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp.Providers.Units, Equals, "100")
	c.Assert(resp.Reserve.Units, Equals, "100")
	c.Assert(resp.Providers.CurrentDeposit, Equals, "500")
}

func (s *QuerierCovQSuite) TestLiquidityProviderCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	cases := []struct {
		req *types.QueryLiquidityProviderRequest
		err string
	}{
		{&types.QueryLiquidityProviderRequest{}, "asset not provided"},
		{&types.QueryLiquidityProviderRequest{Asset: "BTC.BTC"}, "lp not provided"},
		{&types.QueryLiquidityProviderRequest{Asset: "..", Address: "x"}, "fail to parse asset.*"},
		{&types.QueryLiquidityProviderRequest{Asset: "THOR.BTC", Address: "x"}, "must not be a derived asset"},
		{&types.QueryLiquidityProviderRequest{Asset: "BTC/BTC", Address: "x"}, "invalid request: requested pool is a SaversPool"},
		{&types.QueryLiquidityProviderRequest{Asset: "BTC.BTC", Address: "!!"}, "fail to parse address.*"},
	}
	for _, tc := range cases {
		_, err := s.qs.LiquidityProvider(ctx, tc.req)
		c.Assert(err, ErrorMatches, tc.err, Commentf("%+v", tc.req))
	}

	pool := covQPool(c, ctx, s.mgr, common.BTCAsset, 1000*common.One, 10*common.One)
	runeAddr := GetRandomRUNEAddress()
	lp := LiquidityProvider{
		Asset:             common.BTCAsset,
		RuneAddress:       runeAddr,
		AssetAddress:      GetRandomBTCAddress(),
		Units:             pool.LPUnits.QuoUint64(2),
		LastAddHeight:     3,
		RuneDepositValue:  cosmos.NewUint(400 * common.One),
		AssetDepositValue: cosmos.NewUint(4 * common.One),
		PendingRune:       cosmos.ZeroUint(),
		PendingAsset:      cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	resp, err := s.qs.LiquidityProvider(ctx, &types.QueryLiquidityProviderRequest{
		Asset: "BTC.BTC", Address: runeAddr.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(resp.Asset, Equals, "BTC.BTC")
	c.Assert(resp.RuneAddress, Equals, runeAddr.String())
	c.Assert(resp.Units, Equals, lp.Units.String())
	c.Assert(resp.LastAddHeight, Equals, int64(3))
	// half the pool is redeemable
	c.Assert(resp.RuneRedeemValue, Equals, cosmos.NewUint(500*common.One).String())
	c.Assert(resp.AssetRedeemValue, Equals, cosmos.NewUint(5*common.One).String())
	c.Assert(resp.LuviDepositValue, Not(Equals), "0")
	c.Assert(resp.LuviGrowthPct, Not(Equals), "0.000000000000000000")

	list, err := s.qs.LiquidityProviders(ctx, &types.QueryLiquidityProvidersRequest{Asset: "BTC.BTC"})
	c.Assert(err, IsNil)
	c.Assert(list.LiquidityProviders, HasLen, 1)
	c.Assert(list.LiquidityProviders[0].RuneDepositValue, Equals, lp.RuneDepositValue.String())

	for _, a := range []string{"", "..", "THOR.BTC", "BTC/BTC"} {
		_, err = s.qs.LiquidityProviders(ctx, &types.QueryLiquidityProvidersRequest{Asset: a})
		c.Assert(err, NotNil, Commentf("asset %q", a))
	}
}

func (s *QuerierCovQSuite) TestSaverCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	cases := []struct {
		req *types.QuerySaverRequest
		err string
	}{
		{&types.QuerySaverRequest{}, "asset not provided"},
		{&types.QuerySaverRequest{Asset: "BTC.BTC"}, "lp not provided"},
		{&types.QuerySaverRequest{Asset: "..", Address: "x"}, "fail to parse asset.*"},
		{&types.QuerySaverRequest{Asset: "BTC~BTC", Address: "x"}, "invalid request: requested pool is not a SaversPool"},
		{&types.QuerySaverRequest{Asset: "BTC.BTC", Address: "!!"}, "fail to parse address.*"},
	}
	for _, tc := range cases {
		_, err := s.qs.Saver(ctx, tc.req)
		c.Assert(err, ErrorMatches, tc.err, Commentf("%+v", tc.req))
	}
	for _, a := range []string{"", "..", "BTC~BTC"} {
		_, err := s.qs.Savers(ctx, &types.QuerySaversRequest{Asset: a})
		c.Assert(err, NotNil, Commentf("asset %q", a))
	}

	saversPool := NewPool()
	saversPool.Asset = common.BTCAsset.GetSyntheticAsset()
	saversPool.BalanceAsset = cosmos.NewUint(110)
	saversPool.LPUnits = cosmos.NewUint(100)
	saversPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, saversPool), IsNil)

	addr := GetRandomBTCAddress()
	lp := LiquidityProvider{
		Asset:             common.BTCAsset.GetSyntheticAsset(),
		AssetAddress:      addr,
		Units:             cosmos.NewUint(50),
		AssetDepositValue: cosmos.NewUint(50),
		LastAddHeight:     9,
	}
	k.SetLiquidityProvider(ctx, lp)

	// "." is converted to "/" so the L1 notation finds the savers pool
	resp, err := s.qs.Saver(ctx, &types.QuerySaverRequest{Asset: "BTC.BTC", Address: addr.String()})
	c.Assert(err, IsNil)
	c.Assert(resp.Asset, Equals, "BTC.BTC")
	c.Assert(resp.AssetAddress, Equals, addr.String())
	c.Assert(resp.Units, Equals, "50")
	c.Assert(resp.AssetDepositValue, Equals, "50")
	c.Assert(resp.AssetRedeemValue, Equals, "55")
	c.Assert(resp.GrowthPct, Equals, "0.100000000000000000")
	c.Assert(resp.LastAddHeight, Equals, int64(9))

	list, err := s.qs.Savers(ctx, &types.QuerySaversRequest{Asset: "BTC.BTC"})
	c.Assert(err, IsNil)
	c.Assert(list.Savers, HasLen, 1)
	c.Assert(list.Savers[0].AssetRedeemValue, Equals, "55")
}

func (s *QuerierCovQSuite) TestStreamingSwapsCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()

	_, err := s.qs.StreamingSwap(ctx, &types.QueryStreamingSwapRequest{})
	c.Assert(err, ErrorMatches, "tx id not provided")
	_, err = s.qs.StreamingSwap(ctx, &types.QueryStreamingSwapRequest{TxId: "nothex"})
	c.Assert(err, ErrorMatches, "could not parse txid.*")
	// GetStreamingSwap never errors for an unknown hash, so an unknown tx
	// returns an empty legacy-format response rather than "not found"
	missing := GetRandomTxHash()
	unknown, err := s.qs.StreamingSwap(ctx, &types.QueryStreamingSwapRequest{TxId: missing.String()})
	c.Assert(err, IsNil)
	c.Assert(unknown.TxId, Equals, missing.String())
	c.Assert(unknown.Quantity, Equals, int64(0))
	c.Assert(unknown.SourceAsset, Equals, common.EmptyAsset.String())

	empty, err := s.qs.StreamingSwaps(ctx, &types.QueryStreamingSwapsRequest{})
	c.Assert(err, IsNil)
	c.Assert(empty.StreamingSwaps, HasLen, 0)

	// legacy streaming swap + matching v1 swap queue item
	legacyTx := GetRandomTx()
	legacyTx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1000)))
	legacyMsg := NewMsgSwap(legacyTx, common.ETHAsset, GetRandomETHAddress(), cosmos.ZeroUint(),
		common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 5, 2, types.SwapVersion_v1, GetRandomBech32Addr())
	legacyMsg.StreamInterval = 2
	legacyMsg.InitialBlockHeight = 11
	// index 0 is a non-streaming item, index 1 the matching one
	other := *legacyMsg
	other.StreamInterval = 0
	c.Assert(k.SetSwapQueueItem(ctx, other, 0), IsNil)
	c.Assert(k.SetSwapQueueItem(ctx, *legacyMsg, 1), IsNil)
	ss := NewStreamingSwap(legacyTx.ID, 5, 2, cosmos.NewUint(10), cosmos.NewUint(1000))
	ss.Count = 1
	ss.FailedSwaps = []uint64{1}
	ss.FailedSwapReasons = []string{"oops"}
	k.SetStreamingSwap(ctx, ss)

	resp, err := s.qs.StreamingSwap(ctx, &types.QueryStreamingSwapRequest{TxId: legacyTx.ID.String()})
	c.Assert(err, IsNil)
	c.Assert(resp.TxId, Equals, legacyTx.ID.String())
	c.Assert(resp.Quantity, Equals, int64(5))
	c.Assert(resp.Interval, Equals, int64(2))
	c.Assert(resp.Count, Equals, int64(1))
	c.Assert(resp.InitialHeight, Equals, int64(11))
	c.Assert(resp.SourceAsset, Equals, common.BTCAsset.String())
	c.Assert(resp.TargetAsset, Equals, common.ETHAsset.String())
	c.Assert(resp.FailedSwaps, DeepEquals, []int64{1})
	c.Assert(resp.FailedSwapReasons, DeepEquals, []string{"oops"})

	// advanced queue streaming swap
	advTx := GetRandomTx()
	advTx.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(2000)))
	advMsg := NewMsgSwap(advTx, common.BTCAsset, GetRandomBTCAddress(), cosmos.ZeroUint(),
		common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 3, 1, types.SwapVersion_v2, GetRandomBech32Addr())
	advMsg.State = &types.SwapState{
		Quantity:    3,
		Interval:    1,
		Count:       2,
		Deposit:     cosmos.NewUint(2000),
		In:          cosmos.NewUint(1000),
		Out:         cosmos.NewUint(10),
		FailedSwaps: []uint64{0},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, *advMsg), IsNil)
	// a non-streaming advanced item is skipped
	plainTx := GetRandomTx()
	plain := NewMsgSwap(plainTx, common.BTCAsset, GetRandomBTCAddress(), cosmos.ZeroUint(),
		common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 1, 0, types.SwapVersion_v2, GetRandomBech32Addr())
	plain.State = &types.SwapState{Quantity: 1, Deposit: cosmos.NewUint(1), In: cosmos.ZeroUint(), Out: cosmos.ZeroUint()}
	c.Assert(k.SetAdvSwapQueueItem(ctx, *plain), IsNil)

	resp, err = s.qs.StreamingSwap(ctx, &types.QueryStreamingSwapRequest{TxId: advTx.ID.String()})
	c.Assert(err, IsNil)
	c.Assert(resp.Quantity, Equals, int64(3))
	c.Assert(resp.Count, Equals, int64(2))
	c.Assert(resp.Deposit, Equals, "2000")
	c.Assert(resp.In, Equals, "1000")
	c.Assert(resp.Out, Equals, "10")
	c.Assert(resp.SourceAsset, Equals, common.ETHAsset.String())
	c.Assert(resp.FailedSwaps, DeepEquals, []int64{0})

	all, err := s.qs.StreamingSwaps(ctx, &types.QueryStreamingSwapsRequest{})
	c.Assert(err, IsNil)
	c.Assert(all.StreamingSwaps, HasLen, 2)
	ids := []string{all.StreamingSwaps[0].TxId, all.StreamingSwaps[1].TxId}
	sort.Strings(ids)
	exp := []string{legacyTx.ID.String(), advTx.ID.String()}
	sort.Strings(exp)
	c.Assert(ids, DeepEquals, exp)

	// multi-coin inputs leave the source asset blank
	multi := newStreamingSwapFromAdvQueue(MsgSwap{
		Tx: common.Tx{Coins: common.NewCoins(
			common.NewCoin(common.BTCAsset, cosmos.NewUint(1)),
			common.NewCoin(common.ETHAsset, cosmos.NewUint(1)),
		)},
		State: &types.SwapState{Deposit: cosmos.ZeroUint(), In: cosmos.ZeroUint(), Out: cosmos.ZeroUint()},
	})
	c.Assert(multi.SourceAsset, Equals, common.EmptyAsset.String())
	c.Assert(multi.FailedSwaps, IsNil)
}

func (s *QuerierCovQSuite) TestSwapperCloutCovQ(c *C) {
	ctx := s.ctx
	_, err := s.qs.Clout(ctx, &types.QuerySwapperCloutRequest{})
	c.Assert(err, ErrorMatches, "address not provided")
	_, err = s.qs.Clout(ctx, &types.QuerySwapperCloutRequest{Address: "!!"})
	c.Assert(err, ErrorMatches, "could not parse address.*")

	addr := GetRandomBTCAddress()
	clout := types.NewSwapperClout(addr)
	clout.Score = cosmos.NewUint(77)
	c.Assert(s.mgr.Keeper().SetSwapperClout(ctx, clout), IsNil)
	resp, err := s.qs.Clout(ctx, &types.QuerySwapperCloutRequest{Address: addr.String()})
	c.Assert(err, IsNil)
	c.Assert(resp.Address.Equals(addr), Equals, true)
	c.Assert(resp.Score.Uint64(), Equals, uint64(77))
}

func (s *QuerierCovQSuite) TestPoolCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	_, err := s.qs.Pool(ctx, &types.QueryPoolRequest{})
	c.Assert(err, ErrorMatches, "asset not provided")
	_, err = s.qs.Pool(ctx, &types.QueryPoolRequest{Asset: ".."})
	c.Assert(err, ErrorMatches, "could not parse asset.*")
	_, err = s.qs.Pool(ctx, &types.QueryPoolRequest{Asset: "THOR.BTC"})
	c.Assert(err, ErrorMatches, ".*is a derived asset")
	_, err = s.qs.Pool(ctx, &types.QueryPoolRequest{Asset: "BTC.BTC"})
	c.Assert(err, ErrorMatches, "pool: BTC.BTC doesn't exist")

	covQPool(c, ctx, s.mgr, common.BTCAsset, 1000*common.One, 10*common.One)
	k.SetMimir(ctx, "DollarsPerRune", 2*common.One)
	c.Assert(k.SetPOLReserveDeposit(ctx, POLReserveDeposit{
		Asset:         common.BTCAsset,
		RuneDeposited: cosmos.NewUint(42),
	}), IsNil)

	resp, err := s.qs.Pool(ctx, &types.QueryPoolRequest{Asset: "BTC.BTC"})
	c.Assert(err, IsNil)
	c.Assert(resp.Asset, Equals, "BTC.BTC")
	c.Assert(resp.Status, Equals, PoolAvailable.String())
	c.Assert(resp.BalanceRune, Equals, cosmos.NewUint(1000*common.One).String())
	c.Assert(resp.BalanceAsset, Equals, cosmos.NewUint(10*common.One).String())
	c.Assert(resp.TradingHalted, Equals, false)
	c.Assert(resp.PolReserveRuneDeposited, Equals, "42")
	c.Assert(resp.SynthSupply, Equals, "0")
	c.Assert(resp.DerivedDepthBps, Equals, "0")
	// $2 per rune, 100 rune per BTC
	c.Assert(resp.AssetTorPrice, Equals, cosmos.NewUint(200*common.One).String())

	// halting the chain marks trading halted
	k.SetMimir(ctx, "HaltBTCChain", 1)
	resp, err = s.qs.Pool(ctx, &types.QueryPoolRequest{Asset: "BTC.BTC"})
	c.Assert(err, IsNil)
	c.Assert(resp.TradingHalted, Equals, true)
	k.SetMimir(ctx, "HaltBTCChain", 0)
	k.SetMimir(ctx, "HaltBTCTrading", 1)
	resp, err = s.qs.Pool(ctx, &types.QueryPoolRequest{Asset: "BTC.BTC"})
	c.Assert(err, IsNil)
	c.Assert(resp.TradingHalted, Equals, true)
	k.SetMimir(ctx, "HaltBTCTrading", 0)
	k.SetMimir(ctx, "RAGNAROK-BTC-BTC", 1)
	resp, err = s.qs.Pool(ctx, &types.QueryPoolRequest{Asset: "BTC.BTC"})
	c.Assert(err, IsNil)
	c.Assert(resp.TradingHalted, Equals, true)

	// staged pool is always reported halted
	k.SetMimir(ctx, "RAGNAROK-BTC-BTC", 0)
	staged := covQPool(c, ctx, s.mgr, common.ETHAsset, 100, 0)
	staged.Status = PoolStaged
	c.Assert(k.SetPool(ctx, staged), IsNil)
	resp, err = s.qs.Pool(ctx, &types.QueryPoolRequest{Asset: "ETH.ETH"})
	c.Assert(err, IsNil)
	c.Assert(resp.TradingHalted, Equals, true)
	c.Assert(resp.AssetTorPrice, Equals, "")
}

func (s *QuerierCovQSuite) TestPoolSlipsCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	_, err := s.qs.PoolSlip(ctx, &types.QueryPoolSlipRequest{Asset: ".."})
	c.Assert(err, ErrorMatches, "fail to parse asset.*")

	covQPool(c, ctx, s.mgr, common.BTCAsset, 1000, 10)
	covQPool(c, ctx, s.mgr, common.ETHAsset, 1000, 10)
	staged := covQPool(c, ctx, s.mgr, common.DOGEAsset, 1000, 10)
	staged.Status = PoolStaged
	c.Assert(k.SetPool(ctx, staged), IsNil)

	c.Assert(k.AddToSwapSlip(ctx, common.BTCAsset, cosmos.NewInt(25)), IsNil)
	k.SetCurrentRollup(ctx, common.BTCAsset, 7)
	k.SetLongRollup(ctx, common.BTCAsset, 9)

	resp, err := s.qs.PoolSlip(ctx, &types.QueryPoolSlipRequest{Asset: "BTC.BTC"})
	c.Assert(err, IsNil)
	c.Assert(resp.PoolSlips, HasLen, 1)
	c.Assert(resp.PoolSlips[0].Asset, Equals, "BTC.BTC")
	c.Assert(resp.PoolSlips[0].PoolSlip, Equals, int64(25))
	c.Assert(resp.PoolSlips[0].Rollup, Equals, int64(7))
	c.Assert(resp.PoolSlips[0].LongRollup, Equals, int64(9))
	// earlier heights hold no slip
	c.Assert(resp.PoolSlips[0].SummedRollup, Equals, int64(0))

	all, err := s.qs.PoolSlips(ctx, &types.QueryPoolSlipsRequest{})
	c.Assert(err, IsNil)
	assets := []string{}
	for _, ps := range all.PoolSlips {
		assets = append(assets, ps.Asset)
		c.Assert(ps.SummedRollup, Equals, int64(0))
	}
	sort.Strings(assets)
	// staged DOGE pool excluded
	c.Assert(assets, DeepEquals, []string{"BTC.BTC", "ETH.ETH"})
}

func (s *QuerierCovQSuite) TestDerivedPoolsCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	_, err := s.qs.DerivedPool(ctx, &types.QueryDerivedPoolRequest{})
	c.Assert(err, ErrorMatches, "asset not provided")
	_, err = s.qs.DerivedPool(ctx, &types.QueryDerivedPoolRequest{Asset: ".."})
	c.Assert(err, ErrorMatches, "could not parse asset.*")
	_, err = s.qs.DerivedPool(ctx, &types.QueryDerivedPoolRequest{Asset: "BTC.BTC"})
	c.Assert(err, ErrorMatches, "asset is not a derived asset.*")

	dpools, err := s.qs.DerivedPools(ctx, &types.QueryDerivedPoolsRequest{})
	c.Assert(err, IsNil)
	c.Assert(dpools.Pools, HasLen, 0)

	covQPool(c, ctx, s.mgr, common.BTCAsset, 1000*common.One, 10*common.One)
	dpool := NewPool()
	dpool.Asset = common.BTCAsset.GetDerivedAsset()
	dpool.BalanceRune = cosmos.NewUint(100 * common.One)
	dpool.BalanceAsset = cosmos.NewUint(common.One)
	dpool.Status = PoolAvailable
	dpool.Decimals = 8
	c.Assert(k.SetPool(ctx, dpool), IsNil)

	resp, err := s.qs.DerivedPool(ctx, &types.QueryDerivedPoolRequest{Asset: "THOR.BTC"})
	c.Assert(err, IsNil)
	c.Assert(resp.Asset, Equals, "THOR.BTC")
	c.Assert(resp.Status, NotNil)

	dpools, err = s.qs.DerivedPools(ctx, &types.QueryDerivedPoolsRequest{})
	c.Assert(err, IsNil)
	c.Assert(dpools.Pools, HasLen, 1)
	c.Assert(dpools.Pools[0].Asset, Equals, "THOR.BTC")
}

func (s *QuerierCovQSuite) TestTradeUnitsAndAccountsCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	_, err := s.qs.TradeUnit(ctx, &types.QueryTradeUnitRequest{})
	c.Assert(err, ErrorMatches, "asset not provided")
	_, err = s.qs.TradeUnit(ctx, &types.QueryTradeUnitRequest{Asset: ".."})
	c.Assert(err, ErrorMatches, "could not parse asset.*")
	_, err = s.qs.TradeAccounts(ctx, &types.QueryTradeAccountsRequest{})
	c.Assert(err, ErrorMatches, "asset not provided")
	_, err = s.qs.TradeAccounts(ctx, &types.QueryTradeAccountsRequest{Asset: ".."})
	c.Assert(err, ErrorMatches, "could not parse address.*")
	_, err = s.qs.TradeAccount(ctx, &types.QueryTradeAccountRequest{})
	c.Assert(err, ErrorMatches, "address not provided")
	_, err = s.qs.TradeAccount(ctx, &types.QueryTradeAccountRequest{Address: "bad"})
	c.Assert(err, ErrorMatches, "could not parse address.*")

	covQPool(c, ctx, s.mgr, common.BTCAsset, 1000, 10)
	tradeBTC := common.BTCAsset.GetTradeAsset()
	k.SetTradeUnit(ctx, TradeUnit{Asset: tradeBTC, Units: cosmos.NewUint(10), Depth: cosmos.NewUint(20)})

	tu, err := s.qs.TradeUnit(ctx, &types.QueryTradeUnitRequest{Asset: tradeBTC.String()})
	c.Assert(err, IsNil)
	c.Assert(tu.Asset, Equals, tradeBTC.String())
	c.Assert(tu.Units, Equals, "10")
	c.Assert(tu.Depth, Equals, "20")

	tus, err := s.qs.TradeUnits(ctx, &types.QueryTradeUnitsRequest{})
	c.Assert(err, IsNil)
	c.Assert(tus.TradeUnits, HasLen, 1)
	c.Assert(tus.TradeUnits[0].Units, Equals, "10")

	owner := GetRandomBech32Addr()
	k.SetTradeAccount(ctx, TradeAccount{Asset: tradeBTC, Units: cosmos.NewUint(5), Owner: owner, LastAddHeight: 4})
	tradeETH := common.ETHAsset.GetTradeAsset()
	k.SetTradeAccount(ctx, TradeAccount{Asset: tradeETH, Units: cosmos.NewUint(6), Owner: owner})
	// zero-unit accounts are hidden
	zeroOwner := GetRandomBech32Addr()
	k.SetTradeAccount(ctx, TradeAccount{Asset: tradeBTC, Units: cosmos.ZeroUint(), Owner: zeroOwner})

	tas, err := s.qs.TradeAccounts(ctx, &types.QueryTradeAccountsRequest{Asset: tradeBTC.String()})
	c.Assert(err, IsNil)
	c.Assert(tas.TradeAccounts, HasLen, 1)
	c.Assert(tas.TradeAccounts[0].Owner, Equals, owner.String())
	c.Assert(tas.TradeAccounts[0].LastAddHeight, Equals, int64(4))

	ta, err := s.qs.TradeAccount(ctx, &types.QueryTradeAccountRequest{Address: owner.String()})
	c.Assert(err, IsNil)
	c.Assert(ta.TradeAccounts, HasLen, 2)
	ta, err = s.qs.TradeAccount(ctx, &types.QueryTradeAccountRequest{Address: zeroOwner.String()})
	c.Assert(err, IsNil)
	c.Assert(ta.TradeAccounts, HasLen, 0)
}

func (s *QuerierCovQSuite) TestSecuredAssetCovQ(c *C) {
	ctx := s.ctx
	_, err := s.qs.SecuredAsset(ctx, &types.QuerySecuredAssetRequest{})
	c.Assert(err, ErrorMatches, "asset not provided")
	_, err = s.qs.SecuredAsset(ctx, &types.QuerySecuredAssetRequest{Asset: ".."})
	c.Assert(err, ErrorMatches, "could not parse asset.*")
}

func (s *QuerierCovQSuite) TestMimirQueriesCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()

	_, err := s.qs.MimirWithKey(ctx, &types.QueryMimirWithKeyRequest{})
	c.Assert(err, ErrorMatches, "no mimir key")
	resp, err := s.qs.MimirWithKey(ctx, &types.QueryMimirWithKeyRequest{Key: "CovQKey"})
	c.Assert(err, IsNil)
	c.Assert(resp.Value, Equals, int64(-1))

	k.SetMimir(ctx, "CovQKey", 42)
	k.SetMimir(ctx, "CovQNeg", -1)
	resp, err = s.qs.MimirWithKey(ctx, &types.QueryMimirWithKeyRequest{Key: "CovQKey"})
	c.Assert(err, IsNil)
	c.Assert(resp.Value, Equals, int64(42))

	vals, err := s.qs.MimirValues(ctx, &types.QueryMimirValuesRequest{})
	c.Assert(err, IsNil)
	found := map[string]int64{}
	for _, m := range vals.Mimirs {
		found[m.Key] = m.Value
	}
	c.Assert(found["COVQKEY"], Equals, int64(42))
	_, hasNeg := found["COVQNEG"]
	c.Assert(hasNeg, Equals, false)

	admin, err := s.qs.MimirAdminValues(ctx, &types.QueryMimirAdminValuesRequest{})
	c.Assert(err, IsNil)
	adminFound := map[string]int64{}
	for _, m := range admin.AdminMimirs {
		adminFound[m.Key] = m.Value
	}
	c.Assert(adminFound["COVQKEY"], Equals, int64(42))
	c.Assert(adminFound["COVQNEG"], Equals, int64(-1))

	// node mimirs
	na1 := GetRandomValidatorNode(NodeActive)
	na2 := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)
	c.Assert(k.SetNodeAccount(ctx, na2), IsNil)
	c.Assert(k.SetNodeMimir(ctx, "CovQNode", 7, na1.NodeAddress), IsNil)
	c.Assert(k.SetNodeMimir(ctx, "CovQNode", 7, na2.NodeAddress), IsNil)
	c.Assert(k.SetNodeMimir(ctx, "CovQSolo", 3, na1.NodeAddress), IsNil)

	allNodes, err := s.qs.MimirNodesAllValues(ctx, &types.QueryMimirNodesAllValuesRequest{})
	c.Assert(err, IsNil)
	c.Assert(allNodes.Mimirs, HasLen, 3)

	nodesVals, err := s.qs.MimirNodesValues(ctx, &types.QueryMimirNodesValuesRequest{})
	c.Assert(err, IsNil)
	c.Assert(nodesVals.Mimirs, HasLen, 1)
	c.Assert(nodesVals.Mimirs[0].Key, Equals, "COVQNODE")
	c.Assert(nodesVals.Mimirs[0].Value, Equals, int64(7))

	_, err = s.qs.MimirNodeValues(ctx, &types.QueryMimirNodeValuesRequest{Address: "bad"})
	c.Assert(err, ErrorMatches, "fail to parse thor address.*")
	nodeVals, err := s.qs.MimirNodeValues(ctx, &types.QueryMimirNodeValuesRequest{Address: na1.NodeAddress.String()})
	c.Assert(err, IsNil)
	c.Assert(nodeVals.NodeMimirs, HasLen, 2)
	nodeVals, err = s.qs.MimirNodeValues(ctx, &types.QueryMimirNodeValuesRequest{Address: na2.NodeAddress.String()})
	c.Assert(err, IsNil)
	c.Assert(nodeVals.NodeMimirs, HasLen, 1)
}

func (s *QuerierCovQSuite) TestOutboundFeesCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	_, err := s.qs.OutboundFee(ctx, &types.QueryOutboundFeeRequest{Asset: ".."})
	c.Assert(err, ErrorMatches, "fail to parse asset.*")

	covQPool(c, ctx, s.mgr, common.BTCAsset, 1000*common.One, 10*common.One)
	// empty pool is skipped by the default listing
	covQPool(c, ctx, s.mgr, common.DOGEAsset, 0, 0)
	baseWithheld, err := k.GetOutboundFeeWithheldRune(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	baseSpent, err := k.GetOutboundFeeSpentRune(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(k.AddToOutboundFeeWithheldRune(ctx, common.BTCAsset, cosmos.NewUint(300)), IsNil)
	c.Assert(k.AddToOutboundFeeSpentRune(ctx, common.BTCAsset, cosmos.NewUint(100)), IsNil)
	withheld := baseWithheld.AddUint64(300)
	spent := baseSpent.AddUint64(100)

	resp, err := s.qs.OutboundFee(ctx, &types.QueryOutboundFeeRequest{Asset: "BTC.BTC"})
	c.Assert(err, IsNil)
	c.Assert(resp.OutboundFees, HasLen, 1)
	fee := resp.OutboundFees[0]
	c.Assert(fee.Asset, Equals, "BTC.BTC")
	c.Assert(fee.FeeWithheldRune, Equals, withheld.String())
	c.Assert(fee.FeeSpentRune, Equals, spent.String())
	c.Assert(fee.SurplusRune, Equals, common.SafeSub(withheld, spent).String())
	c.Assert(fee.DynamicMultiplierBasisPoints, Not(Equals), "")

	all, err := s.qs.OutboundFees(ctx, &types.QueryOutboundFeesRequest{})
	c.Assert(err, IsNil)
	c.Assert(all.OutboundFees, HasLen, 2)
	c.Assert(all.OutboundFees[0].Asset, Equals, common.RuneAsset().String())
	// native assets only show the fee
	c.Assert(all.OutboundFees[0].FeeWithheldRune, Equals, "")
	c.Assert(all.OutboundFees[1].Asset, Equals, "BTC.BTC")
}

// covQTxOutItem returns a random tx out item with the pointer fields
// populated, as the outbound queue does before storing items.
func covQTxOutItem() TxOutItem {
	item := GetRandomTxOutItem()
	zero := cosmos.ZeroUint()
	limit := cosmos.ZeroUint()
	item.CloutSpent = &zero
	item.AggregatorTargetLimit = &limit
	return item
}

// TestCastTxOutItemNilPointersCovQ documents that castTxOutItem dereferences
// the optional AggregatorTargetLimit/CloutSpent pointers without nil checks.
func (s *QuerierCovQSuite) TestCastTxOutItemNilPointersCovQ(c *C) {
	item := GetRandomTxOutItem()
	c.Assert(item.AggregatorTargetLimit, IsNil)
	c.Assert(func() { castTxOutItem(item, 1) }, PanicMatches, ".*nil pointer dereference.*")

	ok := covQTxOutItem()
	limit := cosmos.NewUint(9)
	ok.AggregatorTargetLimit = &limit
	ok.Aggregator = "0xagg"
	out := castTxOutItem(ok, 0)
	c.Assert(out.Height, Equals, int64(0))
	c.Assert(out.AggregatorTargetLimit, Equals, "9")
	c.Assert(out.CloutSpent, Equals, "0")
	c.Assert(out.Aggregator, Equals, "0xagg")
	c.Assert(out.VaultPubKey, Equals, ok.VaultPubKey.String())
}

func (s *QuerierCovQSuite) TestScheduledAndPendingOutboundCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	empty, err := s.qs.ScheduledOutbound(ctx, &types.QueryScheduledOutboundRequest{})
	c.Assert(err, IsNil)
	c.Assert(empty.TxOutItems, HasLen, 0)

	future := covQTxOutItem()
	future.Memo = "OUT:" + GetRandomTxHash().String()
	c.Assert(k.AppendTxOut(ctx, ctx.BlockHeight()+3, future), IsNil)

	sched, err := s.qs.ScheduledOutbound(ctx, &types.QueryScheduledOutboundRequest{})
	c.Assert(err, IsNil)
	c.Assert(sched.TxOutItems, HasLen, 1)
	c.Assert(sched.TxOutItems[0].Height, Equals, ctx.BlockHeight()+3)
	c.Assert(sched.TxOutItems[0].ToAddress, Equals, future.ToAddress.String())
	c.Assert(sched.TxOutItems[0].Memo, Equals, future.Memo)

	pendingItem := covQTxOutItem()
	pendingItem.Memo = "OUT:" + GetRandomTxHash().String()
	c.Assert(k.AppendTxOut(ctx, ctx.BlockHeight(), pendingItem), IsNil)
	signed := covQTxOutItem()
	signed.OutHash = GetRandomTxHash()
	c.Assert(k.AppendTxOut(ctx, ctx.BlockHeight()-1, signed), IsNil)

	k.SetMimir(ctx, constants.RescheduleCoalesceBlocks.String(), 5)
	pending, err := s.qs.PendingOutbound(ctx, &types.QueryPendingOutboundRequest{})
	c.Assert(err, IsNil)
	// the signed item is excluded; future item at height+3 is within the
	// coalesce window (18 -> 20) only if <= 20
	heights := []int64{}
	for _, it := range pending.TxOutItems {
		heights = append(heights, it.Height)
	}
	c.Assert(heights, DeepEquals, []int64{ctx.BlockHeight()})
}

func (s *QuerierCovQSuite) TestSwapQueueAndDetailsCovQ(c *C) {
	if notMocknetCov() {
		c.Skip("mocknet only: relies on the mocknet advanced swap queue index")
	}
	ctx, k := s.ctx, s.mgr.Keeper()
	_, err := s.qs.SwapDetails(ctx, &types.QuerySwapDetailsRequest{})
	c.Assert(err, ErrorMatches, "missing tx_id parameter")
	_, err = s.qs.SwapDetails(ctx, &types.QuerySwapDetailsRequest{TxId: "zz"})
	c.Assert(err, ErrorMatches, "invalid tx_id.*")
	missing := GetRandomTxHash().String()
	_, err = s.qs.SwapDetails(ctx, &types.QuerySwapDetailsRequest{TxId: missing})
	c.Assert(err, ErrorMatches, "swap with tx_id .* not found in any queue")

	regTx := GetRandomTx()
	regMsg := NewMsgSwap(regTx, common.ETHAsset, GetRandomETHAddress(), cosmos.ZeroUint(),
		common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(k.SetSwapQueueItem(ctx, *regMsg, 0), IsNil)

	advTx := GetRandomTx()
	advMsg := NewMsgSwap(advTx, common.BTCAsset, GetRandomBTCAddress(), cosmos.ZeroUint(),
		common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 1, 0, types.SwapVersion_v2, GetRandomBech32Addr())
	advMsg.State = &types.SwapState{Quantity: 1, Deposit: cosmos.NewUint(1), In: cosmos.ZeroUint(), Out: cosmos.ZeroUint()}
	c.Assert(k.SetAdvSwapQueueItem(ctx, *advMsg), IsNil)

	d, err := s.qs.SwapDetails(ctx, &types.QuerySwapDetailsRequest{TxId: regTx.ID.String()})
	c.Assert(err, IsNil)
	c.Assert(d.QueueType, Equals, QueueTypeRegular)
	c.Assert(d.Status, Equals, SwapStatusQueued)
	c.Assert(d.Swap.Tx.ID.Equals(regTx.ID), Equals, true)

	d, err = s.qs.SwapDetails(ctx, &types.QuerySwapDetailsRequest{TxId: advTx.ID.String()})
	c.Assert(err, IsNil)
	c.Assert(d.QueueType, Equals, QueueTypeAdvanced)
	c.Assert(d.Swap.Tx.ID.Equals(advTx.ID), Equals, true)

	q, err := s.qs.SwapQueue(ctx, &types.QuerySwapQueueRequest{})
	c.Assert(err, IsNil)
	c.Assert(q.SwapQueue, HasLen, 2)

	page, err := s.qs.SwapQueuePaginated(ctx, &types.QuerySwapQueuePaginatedRequest{Offset: 1, Limit: 1})
	c.Assert(err, IsNil)
	c.Assert(page.SwapQueue, HasLen, 1)
	c.Assert(page.Pagination.Total, Equals, uint64(2))
	c.Assert(page.Pagination.HasNext, Equals, false)
	c.Assert(page.Pagination.HasPrev, Equals, true)

	page, err = s.qs.SwapQueuePaginated(ctx, &types.QuerySwapQueuePaginatedRequest{Limit: 1})
	c.Assert(err, IsNil)
	c.Assert(page.SwapQueue, HasLen, 1)
	c.Assert(page.Pagination.HasNext, Equals, true)
	c.Assert(page.Pagination.HasPrev, Equals, false)

	page, err = s.qs.SwapQueuePaginated(ctx, &types.QuerySwapQueuePaginatedRequest{Limit: 1 << 40})
	c.Assert(err, IsNil)
	c.Assert(page.SwapQueue, HasLen, 2)
	c.Assert(page.Pagination.Limit < 1<<40, Equals, true)

	page, err = s.qs.SwapQueuePaginated(ctx, &types.QuerySwapQueuePaginatedRequest{})
	c.Assert(err, IsNil)
	c.Assert(page.Pagination.Limit > 0, Equals, true)

	queue, err := s.qs.Queue(ctx, &types.QueryQueueRequest{})
	c.Assert(err, IsNil)
	c.Assert(queue.Swap, Equals, int64(2))
}

func (s *QuerierCovQSuite) TestTssMetricsCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	_, err := s.qs.TssKeygenMetric(ctx, &types.QueryTssKeygenMetricRequest{})
	c.Assert(err, ErrorMatches, "missing pub_key parameter")
	_, err = s.qs.TssKeygenMetric(ctx, &types.QueryTssKeygenMetricRequest{PubKey: "bad"})
	c.Assert(err, ErrorMatches, "fail to parse pubkey.*")

	pk := GetRandomPubKey()
	addr := GetRandomBech32Addr()
	k.SetTssKeygenMetric(ctx, &types.TssKeygenMetric{
		PubKey:       pk,
		NodeTssTimes: []types.NodeTssTime{{Address: addr, TssTime: 12}},
	})
	resp, err := s.qs.TssKeygenMetric(ctx, &types.QueryTssKeygenMetricRequest{PubKey: pk.String()})
	c.Assert(err, IsNil)
	c.Assert(resp.Metrics, HasLen, 1)
	c.Assert(resp.Metrics[0].NodeTssTimes, HasLen, 1)
	c.Assert(resp.Metrics[0].NodeTssTimes[0].TssTime, Equals, int64(12))

	// active vault with metrics, and one without
	v1 := GetRandomVault()
	v1.PubKey = pk
	v1.Status = ActiveVault
	c.Assert(k.SetVault(ctx, v1), IsNil)
	v2 := GetRandomVault()
	v2.Status = ActiveVault
	c.Assert(k.SetVault(ctx, v2), IsNil)

	txID := GetRandomTxHash()
	k.SetTssKeysignMetric(ctx, &types.TssKeysignMetric{
		TxID:         txID,
		NodeTssTimes: []types.NodeTssTime{{Address: addr, TssTime: 5}},
	})

	m, err := s.qs.TssMetric(ctx, &types.QueryTssMetricRequest{})
	c.Assert(err, IsNil)
	c.Assert(m.Keygen, HasLen, 1)
	c.Assert(m.Keygen[0].PubKey.Equals(pk), Equals, true)
	c.Assert(m.Keysign, NotNil)
	c.Assert(m.Keysign.TxID.Equals(txID), Equals, true)
}

func (s *QuerierCovQSuite) TestInvariantsCovQ(c *C) {
	ctx := s.ctx
	resp, err := s.qs.Invariants(ctx, &types.QueryInvariantsRequest{})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Invariants) > 0, Equals, true)

	_, err = s.qs.Invariant(ctx, &types.QueryInvariantRequest{})
	c.Assert(err, ErrorMatches, "invalid path.*")
	_, err = s.qs.Invariant(ctx, &types.QueryInvariantRequest{Path: "covq-nope"})
	c.Assert(err, ErrorMatches, "invariant not registered: covq-nope")

	route := resp.Invariants[0]
	inv, err := s.qs.Invariant(ctx, &types.QueryInvariantRequest{Path: route})
	c.Assert(err, IsNil)
	c.Assert(inv.Invariant, Equals, route)
}

func (s *QuerierCovQSuite) TestAccountAndBalancesCovQ(c *C) {
	ctx := s.ctx
	_, err := s.qs.Account(ctx, &types.QueryAccountRequest{Address: "bad"})
	c.Assert(err, ErrorMatches, "fail to parse address.*")
	_, err = s.qs.Balances(ctx, &types.QueryBalancesRequest{Address: "bad"})
	c.Assert(err, ErrorMatches, "fail to parse address.*")

	addr := s.mgr.Keeper().GetModuleAccAddress(ReserveName)
	acc, err := s.qs.Account(ctx, &types.QueryAccountRequest{Address: addr.String()})
	c.Assert(err, IsNil)
	c.Assert(acc.Address, Equals, addr.String())
	c.Assert(acc.PubKey, Equals, "")
	c.Assert(acc.Sequence, Equals, "0")

	bals, err := s.qs.Balances(ctx, &types.QueryBalancesRequest{Address: addr.String()})
	c.Assert(err, IsNil)
	c.Assert(bals.Balances, HasLen, 1)
	c.Assert(bals.Balances[0].Denom, Equals, "rune")
	c.Assert(bals.Balances[0].Amount, Equals, cosmos.NewUint(100_000_000*common.One).String())
}

func (s *QuerierCovQSuite) TestTCYQueriesCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()

	stakers, err := s.qs.TCYStakers(ctx, &types.QueryTCYStakersRequest{})
	c.Assert(err, IsNil)
	baseStakers := len(stakers.TcyStakers)

	_, err = s.qs.TCYStaker(ctx, &types.QueryTCYStakerRequest{Address: "!!"})
	c.Assert(err, ErrorMatches, "fail to parse address.*")

	stakerAddr := GetRandomRUNEAddress()
	c.Assert(k.SetTCYStaker(ctx, TCYStaker{Address: stakerAddr, Amount: cosmos.NewUint(55)}), IsNil)
	staker, err := s.qs.TCYStaker(ctx, &types.QueryTCYStakerRequest{Address: stakerAddr.String()})
	c.Assert(err, IsNil)
	c.Assert(staker.Address, Equals, stakerAddr.String())
	c.Assert(staker.Amount, Equals, "55")

	stakers, err = s.qs.TCYStakers(ctx, &types.QueryTCYStakersRequest{})
	c.Assert(err, IsNil)
	c.Assert(stakers.TcyStakers, HasLen, baseStakers+1)
	foundStaker := false
	for _, st := range stakers.TcyStakers {
		if st.Address == stakerAddr.String() {
			foundStaker = true
			c.Assert(st.Amount, Equals, "55")
		}
	}
	c.Assert(foundStaker, Equals, true)

	_, err = s.qs.TCYClaimer(ctx, &types.QueryTCYClaimerRequest{Address: "!!"})
	c.Assert(err, ErrorMatches, "fail to parse address.*")

	l1 := GetRandomBTCAddress()
	c.Assert(k.SetTCYClaimer(ctx, TCYClaimer{Asset: common.BTCAsset, L1Address: l1, Amount: cosmos.NewUint(9)}), IsNil)
	c.Assert(k.SetTCYClaimer(ctx, TCYClaimer{Asset: common.ETHAsset, L1Address: l1, Amount: cosmos.NewUint(11)}), IsNil)

	claimer, err := s.qs.TCYClaimer(ctx, &types.QueryTCYClaimerRequest{Address: l1.String()})
	c.Assert(err, IsNil)
	c.Assert(claimer.TcyClaimer, HasLen, 2)

	claimers, err := s.qs.TCYClaimers(ctx, &types.QueryTCYClaimersRequest{})
	c.Assert(err, IsNil)
	c.Assert(claimers.TcyClaimers, HasLen, 2)
	total := uint64(0)
	for _, cl := range claimers.TcyClaimers {
		c.Assert(cl.L1Address, Equals, l1.String())
		total += cosmos.NewUintFromString(cl.Amount).Uint64()
	}
	c.Assert(total, Equals, uint64(20))
}

func (s *QuerierCovQSuite) TestOraclePricesCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	_, err := s.qs.OraclePrice(ctx, &types.QueryOraclePriceRequest{Symbol: "NOPE"})
	c.Assert(err, ErrorMatches, "fail to get price for symbol 'NOPE'.*")

	c.Assert(k.SetPrice(ctx, OraclePrice{Symbol: "ZZZ", Price: "2.5"}), IsNil)
	c.Assert(k.SetPrice(ctx, OraclePrice{Symbol: "AAA", Price: "1.5"}), IsNil)

	p, err := s.qs.OraclePrice(ctx, &types.QueryOraclePriceRequest{Symbol: "ZZZ"})
	c.Assert(err, IsNil)
	c.Assert(p.Price.Price, Equals, "2.5")

	ps, err := s.qs.OraclePrices(ctx, &types.QueryOraclePricesRequest{})
	c.Assert(err, IsNil)
	c.Assert(ps.Prices, HasLen, 2)
	// sorted by symbol
	c.Assert(ps.Prices[0].Symbol, Equals, "AAA")
	c.Assert(ps.Prices[1].Symbol, Equals, "ZZZ")
}

func (s *QuerierCovQSuite) TestSupplyCovQ(c *C) {
	ctx := s.ctx
	resp, err := s.qs.Supply(ctx, &types.QuerySupplyRequest{})
	c.Assert(err, IsNil)
	runeSupply := s.mgr.Keeper().GetTotalSupply(ctx, common.RuneAsset())
	c.Assert(resp.Total, Equals, int64(runeSupply.Uint64()/common.One))
	c.Assert(resp.Locked.Reserve, Equals, int64(100_000_000))
	c.Assert(resp.Circulating, Equals, resp.Total-resp.Locked.Reserve)
}

func (s *QuerierCovQSuite) TestContractInfoCovQ(c *C) {
	ctx := s.ctx
	_, err := s.qs.ContractInfo(ctx, &types.QueryContractInfoRequest{Address: "bad"})
	c.Assert(err, ErrorMatches, "invalid address.*")
	addr := GetRandomBech32Addr()
	_, err = s.qs.ContractInfo(ctx, &types.QueryContractInfoRequest{Address: addr.String()})
	c.Assert(err, ErrorMatches, "no wasm contract at address.*")

	infos, err := s.qs.ContractInfos(ctx, &types.QueryContractInfosRequest{})
	c.Assert(err, IsNil)
	c.Assert(infos.Infos, HasLen, 0)
}

func (s *QuerierCovQSuite) TestEip712TypedDataCovQ(c *C) {
	_, err := s.qs.Eip712TypedData(s.ctx, &types.QueryEip712TypedDataRequest{SignBytes: []byte("not json")})
	c.Assert(err, ErrorMatches, "(?s)failed to convert to EIP-712 typed data.*")
}

func (s *QuerierCovQSuite) TestReferenceMemoCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	_, err := s.qs.ReferenceMemo(ctx, &types.QueryReferenceMemoRequest{Asset: ".."})
	c.Assert(err, ErrorMatches, "invalid asset.*")
	_, err = s.qs.ReferenceMemo(ctx, &types.QueryReferenceMemoRequest{Asset: "BTC.BTC"})
	c.Assert(err, ErrorMatches, "reference cannot be empty")
	_, err = s.qs.ReferenceMemo(ctx, &types.QueryReferenceMemoRequest{Asset: "BTC.BTC", Reference: "12345"})
	c.Assert(err, ErrorMatches, "reference memo not found.*")

	_, err = s.qs.ReferenceMemoByHash(ctx, &types.QueryReferenceMemoByHashRequest{})
	c.Assert(err, ErrorMatches, "hash cannot be empty")
	_, err = s.qs.ReferenceMemoByHash(ctx, &types.QueryReferenceMemoByHashRequest{Hash: "zz"})
	c.Assert(err, ErrorMatches, "invalid hash.*")
	_, err = s.qs.ReferenceMemoByHash(ctx, &types.QueryReferenceMemoByHashRequest{Hash: GetRandomTxHash().String()})
	c.Assert(err, ErrorMatches, "reference memo not found for hash.*")

	regHash := GetRandomTxHash()
	signer := GetRandomBech32Addr()
	used := GetRandomTxHash()
	k.SetReferenceMemo(ctx, ReferenceMemo{
		Asset:            common.BTCAsset,
		Memo:             "=:ETH.ETH:0xabc",
		Reference:        "12345",
		Height:           10,
		RegistrationHash: regHash,
		RegisteredBy:     signer,
		UsedByTxs:        []common.TxID{used},
	})

	resp, err := s.qs.ReferenceMemo(ctx, &types.QueryReferenceMemoRequest{Asset: "BTC.BTC", Reference: "12345"})
	c.Assert(err, IsNil)
	c.Assert(resp.Memo, Equals, "=:ETH.ETH:0xabc")
	c.Assert(resp.Height, Equals, int64(10))
	c.Assert(resp.RegistrationHash.Equals(regHash), Equals, true)
	c.Assert(resp.UsedByTxs, HasLen, 1)

	byHash, err := s.qs.ReferenceMemoByHash(ctx, &types.QueryReferenceMemoByHashRequest{Hash: regHash.String()})
	c.Assert(err, IsNil)
	c.Assert(byHash.Reference, Equals, "12345")
	c.Assert(byHash.RegisteredBy.Equals(signer), Equals, true)
}

func (s *QuerierCovQSuite) TestDynamicL1FeesCurrentCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	resp, err := s.qs.DynamicL1FeesCurrent(ctx, &types.QueryDynamicL1FeesCurrentRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp.Entries, HasLen, 0)

	k.SetMimir(ctx, constants.L1DynamicFeeEpochBlocks.String(), 5)
	c.Assert(k.SetDynamicFeeAccumulator(ctx, types.DynamicFeeAccumulator{
		Thorname:   "covq",
		PairAssetA: "BTC.BTC",
		PairAssetB: "ETH.ETH",
		VolumeTor:  cosmos.NewUint(1000),
		FeesTor:    cosmos.NewUint(3),
	}), IsNil)
	resp, err = s.qs.DynamicL1FeesCurrent(ctx, &types.QueryDynamicL1FeesCurrentRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp.Epoch, Equals, uint64(ctx.BlockHeight()/5))
	c.Assert(resp.Entries, HasLen, 1)
	c.Assert(resp.Entries[0].Thorname, Equals, "covq")
	c.Assert(resp.Entries[0].Pair, Equals, "BTC.BTC|ETH.ETH")
	c.Assert(resp.Entries[0].VolumeTor, Equals, "1000")
	c.Assert(resp.Entries[0].FeesTor, Equals, "3")
	c.Assert(resp.Entries[0].Epoch, Equals, resp.Epoch)
}
