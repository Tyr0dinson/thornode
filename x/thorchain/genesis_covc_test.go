package thorchain

import (
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type GenesisCovCBSuite struct{}

var _ = Suite(&GenesisCovCBSuite{})

func (s *GenesisCovCBSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

func (s *GenesisCovCBSuite) TestValidateGenesisErrorsCovCB(c *C) {
	longName := strings.Repeat("a", 31)
	cases := []struct {
		name   string
		mutate func(*GenesisState)
		match  string
	}{
		{"sol nonce", func(g *GenesisState) { g.SolVaultNonces = []types.SolVaultNonce{{}} }, ".*"},
		{"pool", func(g *GenesisState) { g.Pools = []Pool{{}} }, ".*"},
		// NOTE: SwapperClout.Valid rejects records WITH an address (the check
		// is inverted), so any non-empty clout fails genesis validation.
		{"clout", func(g *GenesisState) {
			g.SwapperClout = []SwapperClout{NewSwapperClout(GetRandomETHAddress())}
		}, "invalid swapper clout address: .*"},
		{"in voter", func(g *GenesisState) { g.ObservedTxInVoters = ObservedTxVoters{{}} }, ".*"},
		{"out voter", func(g *GenesisState) { g.ObservedTxOutVoters = ObservedTxVoters{{}} }, ".*"},
		{"tx out", func(g *GenesisState) {
			g.TxOuts = []TxOut{{Height: 1, TxArray: []TxOutItem{{}}}}
		}, ".*"},
		{"node account", func(g *GenesisState) { g.NodeAccounts = NodeAccounts{{}} }, ".*"},
		{"vault", func(g *GenesisState) { g.Vaults = Vaults{{}} }, ".*"},
		{"last signed height", func(g *GenesisState) { g.LastSignedHeight = -1 }, "last signed height cannot be negative"},
		{"chain height", func(g *GenesisState) {
			g.LastChainHeights = []LastChainHeight{{Chain: "BTC", Height: -1}}
		}, "invalid chain\\(BTC\\) height"},
		{"adv swap", func(g *GenesisState) { g.AdvSwapQueueItems = []MsgSwap{{}} }, "invalid swap msg: .*"},
		{"swap", func(g *GenesisState) { g.SwapQueueItems = []MsgSwap{{}} }, "invalid swap msg: .*"},
		{"streaming swap", func(g *GenesisState) { g.StreamingSwaps = []StreamingSwap{{}} }, "invalid streaming swap: .*"},
		{"network fee", func(g *GenesisState) { g.NetworkFees = []NetworkFee{{}} }, "invalid network fee: .*"},
		{"chain contract", func(g *GenesisState) { g.ChainContracts = []ChainContract{{}} }, "chain contract cannot be empty"},
		{"long thorname", func(g *GenesisState) {
			g.THORNames = []THORName{{Name: longName}}
		}, "THORName cannot exceed 30 characters"},
		{"bad thorname", func(g *GenesisState) {
			g.THORNames = []THORName{{Name: "bad name!"}}
		}, "invalid THORName"},
		{"empty collector", func(g *GenesisState) {
			g.AffiliateCollectors = []AffiliateFeeCollector{{Name: "", RuneAmount: cosmos.NewUint(1)}}
		}, "invalid affiliate collector.*"},
		{"nil collector amount", func(g *GenesisState) {
			g.AffiliateCollectors = []AffiliateFeeCollector{{Name: "abc"}}
		}, "invalid affiliate collector.*"},
		{"duplicate collector", func(g *GenesisState) {
			g.AffiliateCollectors = []AffiliateFeeCollector{
				{Name: "abc", RuneAmount: cosmos.NewUint(1)},
				{Name: "ABC", RuneAmount: cosmos.NewUint(2)},
			}
		}, "duplicate affiliate collector \"ABC\""},
	}
	for _, tc := range cases {
		g := DefaultGenesisState()
		tc.mutate(&g)
		err := ValidateGenesis(g)
		c.Assert(err, NotNil, Commentf(tc.name))
		c.Assert(err, ErrorMatches, tc.match, Commentf(tc.name))
	}

	// distinct collector names are fine
	g := DefaultGenesisState()
	g.AffiliateCollectors = []AffiliateFeeCollector{
		{Name: "abc", RuneAmount: cosmos.NewUint(1)},
		{Name: "def", RuneAmount: cosmos.NewUint(2)},
	}
	g.THORNames = []THORName{{Name: "valid-name"}}
	c.Assert(ValidateGenesis(g), IsNil)
}

func covCBSwapMsg(c *C, coin common.Coin, target common.Asset) MsgSwap {
	tx := GetRandomTx()
	tx.Coins = common.Coins{coin}
	msg := NewMsgSwap(tx, target, GetRandomETHAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
		"", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	return *msg
}

// TestExportImportFullStateCovCB populates every exported category, exports,
// re-imports into a fresh keeper and checks the round-tripped state.
func (s *GenesisCovCBSuite) TestExportImportFullStateCovCB(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(500)
	k := mgr.Keeper()

	// pools and LPs
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	suspended := pool
	suspended.Asset = common.DOGEAsset
	suspended.Status = PoolSuspended
	c.Assert(k.SetPool(ctx, suspended), IsNil)
	lp := LiquidityProvider{
		Asset:        common.BTCAsset,
		RuneAddress:  GetRandomRUNEAddress(),
		AssetAddress: GetRandomBTCAddress(),
		Units:        cosmos.NewUint(10),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)
	emptyLP := lp
	emptyLP.RuneAddress = GetRandomRUNEAddress()
	emptyLP.Units = cosmos.ZeroUint()
	k.SetLiquidityProvider(ctx, emptyLP)

	// vaults
	activeVault := GetRandomVault()
	c.Assert(k.SetVault(ctx, activeVault), IsNil)
	inactiveVault := GetRandomVault()
	inactiveVault.Status = InactiveVault
	inactiveVault.Coins = common.Coins{}
	c.Assert(k.SetVault(ctx, inactiveVault), IsNil)

	// node accounts
	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(1000 * common.One)
	na.SignerMembership = []string{activeVault.PubKey.String(), inactiveVault.PubKey.String(), GetRandomPubKey().String()}
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	noBond := GetRandomValidatorNode(NodeStandby)
	noBond.Bond = cosmos.ZeroUint()
	c.Assert(k.SetNodeAccount(ctx, noBond), IsNil)

	// tcy claimers/stakers
	claimer := types.NewTCYClaimer(GetRandomBTCAddress(), common.BTCAsset, cosmos.NewUint(77))
	c.Assert(k.SetTCYClaimer(ctx, claimer), IsNil)
	staker := types.NewTCYStaker(GetRandomRUNEAddress(), cosmos.NewUint(88))
	c.Assert(k.SetTCYStaker(ctx, staker), IsNil)

	// tx outs: pending item with an observed inbound, a fulfilled item, an
	// item from an inactive vault, and an item from an unknown vault
	inHash := GetRandomTxHash()
	observed := common.NewObservedTx(GetRandomTx(), 10, activeVault.PubKey, 10)
	voter := NewObservedTxVoter(inHash, []common.ObservedTx{observed})
	k.SetObservedTxInVoter(ctx, voter)
	pendingItem := TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   GetRandomBTCAddress(),
		VaultPubKey: activeVault.PubKey,
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		InHash:      inHash,
		Memo:        "OUT:" + inHash.String(),
	}
	c.Assert(k.AppendTxOut(ctx, ctx.BlockHeight(), pendingItem), IsNil)
	blankItem := pendingItem
	blankItem.InHash = common.BlankTxID
	blankItem.Memo = "MIGRATE:1"
	c.Assert(k.AppendTxOut(ctx, ctx.BlockHeight(), blankItem), IsNil)
	missingVoterItem := pendingItem
	missingVoterItem.InHash = GetRandomTxHash()
	c.Assert(k.AppendTxOut(ctx, ctx.BlockHeight(), missingVoterItem), IsNil)
	doneItem := pendingItem
	doneItem.InHash = GetRandomTxHash()
	doneItem.OutHash = GetRandomTxHash()
	c.Assert(k.AppendTxOut(ctx, ctx.BlockHeight()+1, doneItem), IsNil)
	inactiveItem := pendingItem
	inactiveItem.InHash = GetRandomTxHash()
	inactiveItem.VaultPubKey = inactiveVault.PubKey
	c.Assert(k.AppendTxOut(ctx, ctx.BlockHeight()+2, inactiveItem), IsNil)
	unknownItem := pendingItem
	unknownItem.InHash = GetRandomTxHash()
	unknownItem.VaultPubKey = GetRandomPubKey()
	c.Assert(k.AppendTxOut(ctx, ctx.BlockHeight()+3, unknownItem), IsNil)

	c.Assert(k.SetLastSignedHeight(ctx, 400), IsNil)
	c.Assert(k.SetLastChainHeight(ctx, common.ETHChain, 222), IsNil)
	c.Assert(k.SetLastChainHeight(ctx, common.BTCChain, 111), IsNil)

	// swap queues and streaming swaps
	advMsg := covCBSwapMsg(c, common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)), common.ETHAsset)
	c.Assert(k.SetAdvSwapQueueItem(ctx, advMsg), IsNil)
	qMsg := covCBSwapMsg(c, common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)), common.BTCAsset)
	c.Assert(k.SetSwapQueueItem(ctx, qMsg, 0), IsNil)
	ss := NewStreamingSwap(qMsg.Tx.ID, 2, 1, cosmos.ZeroUint(), cosmos.NewUint(common.One))
	k.SetStreamingSwap(ctx, ss)

	k.SetChainContract(ctx, types.NewChainContract(common.ETHChain, GetRandomETHAddress()))
	k.SetTHORName(ctx, NewTHORName("covcbname", 10000, []THORNameAlias{{Chain: common.THORChain, Address: GetRandomRUNEAddress()}}))

	k.SetMimir(ctx, "CovCBMimir", 42)
	c.Assert(k.SetNodeMimir(ctx, "CovCBNodeMimir", 7, na.NodeAddress), IsNil)

	clout := NewSwapperClout(GetRandomETHAddress())
	clout.Score = cosmos.NewUint(1234)
	c.Assert(k.SetSwapperClout(ctx, clout), IsNil)

	rp := types.NewRUNEProvider(GetRandomBech32Addr())
	rp.Units = cosmos.NewUint(55)
	k.SetRUNEProvider(ctx, rp)

	ta := types.NewTradeAccount(GetRandomBech32Addr(), common.BTCAsset.GetTradeAsset())
	ta.Units = cosmos.NewUint(66)
	k.SetTradeAccount(ctx, ta)
	tu := types.NewTradeUnit(common.BTCAsset.GetTradeAsset())
	tu.Units = cosmos.NewUint(66)
	tu.Depth = cosmos.NewUint(99)
	k.SetTradeUnit(ctx, tu)

	sa := types.NewSecuredAsset(common.ETHAsset.GetSecuredAsset())
	sa.Depth = cosmos.NewUint(321)
	k.SetSecuredAsset(ctx, sa)

	// the first read seeds the withheld record with the equilibrium surplus
	seed, err := k.GetOutboundFeeWithheldRune(ctx, common.AVAXAsset)
	c.Assert(err, IsNil)
	c.Assert(seed.IsZero(), Equals, false)
	c.Assert(k.AddToOutboundFeeWithheldRune(ctx, common.AVAXAsset, cosmos.NewUint(500)), IsNil)
	c.Assert(k.AddToOutboundFeeSpentRune(ctx, common.AVAXAsset, cosmos.NewUint(400)), IsNil)

	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: "covcb", RuneAmount: cosmos.NewUint(9)}), IsNil)

	// ---- export ----
	gs := ExportGenesis(ctx, k)

	c.Assert(gs.Pools, HasLen, 1)
	c.Assert(gs.Pools[0].Asset.Equals(common.BTCAsset), Equals, true)
	c.Assert(gs.LiquidityProviders, HasLen, 1)
	c.Assert(gs.LiquidityProviders[0].RuneAddress.Equals(lp.RuneAddress), Equals, true)

	c.Assert(gs.NodeAccounts, HasLen, 1)
	c.Assert(gs.NodeAccounts[0].NodeAddress.Equals(na.NodeAddress), Equals, true)
	c.Assert(gs.NodeAccounts[0].SignerMembership, DeepEquals, []string{activeVault.PubKey.String()})
	c.Assert(gs.BondProviders, HasLen, 1)

	c.Assert(gs.TcyClaimers, HasLen, 1)
	c.Assert(gs.TcyClaimers[0].Amount.Uint64(), Equals, uint64(77))
	foundStaker := false
	for _, st := range gs.TcyStakers {
		if st.Address.Equals(staker.Address) {
			foundStaker = true
		}
	}
	c.Assert(foundStaker, Equals, true)

	// only the block holding pending active-vault items is exported
	c.Assert(gs.TxOuts, HasLen, 1)
	c.Assert(gs.TxOuts[0].Height, Equals, ctx.BlockHeight())
	// the stored voter is exported; an in-hash with no stored voter is
	// exported as an empty placeholder keyed by that hash
	c.Assert(gs.ObservedTxInVoters, HasLen, 2)
	foundVoter := false
	for _, v := range gs.ObservedTxInVoters {
		if v.TxID.Equals(inHash) {
			foundVoter = true
			c.Assert(v.Txs, HasLen, 1)
		} else {
			c.Assert(v.TxID.Equals(missingVoterItem.InHash), Equals, true)
			c.Assert(v.Txs, HasLen, 0)
		}
	}
	c.Assert(foundVoter, Equals, true)

	c.Assert(gs.LastSignedHeight, Equals, int64(400))
	c.Assert(gs.LastChainHeights, DeepEquals, []LastChainHeight{
		{Chain: "BTC", Height: 111},
		{Chain: "ETH", Height: 222},
	})

	c.Assert(gs.AdvSwapQueueItems, HasLen, 1)
	c.Assert(gs.AdvSwapQueueItems[0].Tx.ID.Equals(advMsg.Tx.ID), Equals, true)
	c.Assert(gs.SwapQueueItems, HasLen, 1)
	c.Assert(gs.SwapQueueItems[0].Tx.ID.Equals(qMsg.Tx.ID), Equals, true)
	c.Assert(gs.StreamingSwaps, HasLen, 1)
	c.Assert(gs.ChainContracts, HasLen, 1)
	c.Assert(gs.THORNames, HasLen, 1)
	c.Assert(gs.THORNames[0].Name, Equals, "covcbname")

	foundMimir := false
	for _, m := range gs.Mimirs {
		if strings.EqualFold(m.Key, "CovCBMimir") {
			foundMimir = true
			c.Assert(m.Value, Equals, int64(42))
		}
	}
	c.Assert(foundMimir, Equals, true)
	foundNodeMimir := false
	for _, m := range gs.NodeMimirs {
		if strings.EqualFold(m.Key, "CovCBNodeMimir") {
			foundNodeMimir = true
			c.Assert(m.Value, Equals, int64(7))
			c.Assert(m.Signer.Equals(na.NodeAddress), Equals, true)
		}
	}
	c.Assert(foundNodeMimir, Equals, true)

	c.Assert(gs.SwapperClout, HasLen, 1)
	c.Assert(gs.SwapperClout[0].Score.Uint64(), Equals, uint64(1234))
	c.Assert(gs.RuneProviders, HasLen, 1)
	c.Assert(gs.TradeAccounts, HasLen, 1)
	c.Assert(gs.TradeUnits, HasLen, 1)
	c.Assert(gs.TradeUnits[0].Depth.Uint64(), Equals, uint64(99))
	c.Assert(gs.SecuredAssets, HasLen, 1)
	c.Assert(gs.SecuredAssets[0].Depth.Uint64(), Equals, uint64(321))
	findCoin := func(coins common.Coins, asset common.Asset) uint64 {
		for _, coin := range coins {
			if coin.Asset.Equals(asset) {
				return coin.Amount.Uint64()
			}
		}
		return 0
	}
	c.Assert(findCoin(gs.OutboundFeeWithheldRune, common.AVAXAsset), Equals, seed.Uint64()+500)
	c.Assert(findCoin(gs.OutboundFeeSpentRune, common.AVAXAsset), Equals, uint64(400))
	c.Assert(gs.AffiliateCollectors, HasLen, 1)

	// ---- import into a fresh chain ----
	gs.Reserve = 5 * common.One
	gs.Mimirs = append(gs.Mimirs, Mimir{Key: "", Value: 1})
	gs.NodeMimirs = append(gs.NodeMimirs, NodeMimir{Key: "", Value: 1})
	dstCtx, dstMgr := setupManagerForTest(c)
	dstCtx = dstCtx.WithBlockHeight(ctx.BlockHeight())
	dk := dstMgr.Keeper()
	reserveBefore := dk.GetRuneBalanceOfModule(dstCtx, ReserveName)

	updates := InitGenesis(dstCtx, dk, gs)
	c.Assert(updates, HasLen, 1)
	c.Assert(updates[0].Power, Equals, int64(100))

	c.Assert(dk.GetRuneBalanceOfModule(dstCtx, ReserveName).Sub(reserveBefore).Uint64(), Equals, uint64(5*common.One))

	gotNA, err := dk.GetNodeAccount(dstCtx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(gotNA.Status, Equals, NodeActive)
	gotPool, err := dk.GetPool(dstCtx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(gotPool.BalanceRune.Equal(pool.BalanceRune), Equals, true)
	lsh, err := dk.GetLastSignedHeight(dstCtx)
	c.Assert(err, IsNil)
	c.Assert(lsh, Equals, int64(400))
	mimir, err := dk.GetMimir(dstCtx, "CovCBMimir")
	c.Assert(err, IsNil)
	c.Assert(mimir, Equals, int64(42))
	gotName, err := dk.GetTHORName(dstCtx, "covcbname")
	c.Assert(err, IsNil)
	c.Assert(gotName.ExpireBlockHeight, Equals, int64(10000))
	gotTU, err := dk.GetTradeUnit(dstCtx, common.BTCAsset.GetTradeAsset())
	c.Assert(err, IsNil)
	c.Assert(gotTU.Depth.Uint64(), Equals, uint64(99))
	withheld, err := dk.GetOutboundFeeWithheldRune(dstCtx, common.AVAXAsset)
	c.Assert(err, IsNil)
	// NOTE: InitGenesis restores withheld rune via AddToOutboundFeeWithheldRune,
	// which re-seeds an empty record before adding, so the imported value is
	// the exported value plus one extra seed (round trip is not idempotent).
	c.Assert(withheld.Uint64(), Equals, 2*seed.Uint64()+500)
	gotClaimer, err := dk.GetTCYClaimer(dstCtx, claimer.L1Address, claimer.Asset)
	c.Assert(err, IsNil)
	c.Assert(gotClaimer.Amount.Uint64(), Equals, uint64(77))

	// re-export matches the original export on the round-tripped categories
	gs2 := ExportGenesis(dstCtx, dk)
	c.Assert(gs2.Pools, HasLen, len(gs.Pools))
	c.Assert(gs2.LiquidityProviders, HasLen, len(gs.LiquidityProviders))
	c.Assert(gs2.NodeAccounts, HasLen, 1)
	c.Assert(gs2.TxOuts, HasLen, 1)
	c.Assert(gs2.ObservedTxInVoters, HasLen, 2)
	c.Assert(gs2.LastChainHeights, DeepEquals, gs.LastChainHeights)
	c.Assert(gs2.AdvSwapQueueItems, HasLen, 1)
	c.Assert(gs2.SwapQueueItems, HasLen, 1)
	c.Assert(gs2.StreamingSwaps, HasLen, 1)
	c.Assert(gs2.ChainContracts, HasLen, 1)
	c.Assert(gs2.THORNames, HasLen, 1)
	c.Assert(gs2.SwapperClout, HasLen, 1)
	c.Assert(gs2.RuneProviders, HasLen, 1)
	c.Assert(gs2.TradeAccounts, HasLen, 1)
	c.Assert(gs2.TradeUnits, HasLen, 1)
	c.Assert(gs2.SecuredAssets, HasLen, 1)
	c.Assert(gs2.OutboundFeeSpentRune, DeepEquals, gs.OutboundFeeSpentRune)
	c.Assert(gs2.AffiliateCollectors, HasLen, 1)
	c.Assert(gs2.TcyClaimers, HasLen, 1)
}

func (s *GenesisCovCBSuite) TestInitGenesisPanicsOnBadInputCovCB(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// active node with an unparsable consensus pubkey
	g := DefaultGenesisState()
	na := GetRandomValidatorNode(NodeActive)
	na.ValidatorConsPubKey = "bogus"
	g.NodeAccounts = NodeAccounts{na}
	c.Assert(func() { InitGenesis(ctx, mgr.Keeper(), g) }, PanicMatches, ".*")

	// unknown chain in last chain heights
	g = DefaultGenesisState()
	g.LastChainHeights = []LastChainHeight{{Chain: "", Height: 1}}
	c.Assert(func() { InitGenesis(ctx, mgr.Keeper(), g) }, PanicMatches, ".*")

	// invalid stable reserve credit asset
	g = DefaultGenesisState()
	g.StableReserveCredits = []StableReserveCredit{{Asset: "", Address: "x", Amount: "1"}}
	c.Assert(func() { InitGenesis(ctx, mgr.Keeper(), g) }, PanicMatches, ".*")
}
