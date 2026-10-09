package types

import (
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type TypeSmallCovSuite struct{}

var _ = Suite(&TypeSmallCovSuite{})

func (s *TypeSmallCovSuite) TestSolVaultNonce(c *C) {
	var nilNonce *SolVaultNonce
	c.Assert(nilNonce.IsEmpty(), Equals, true)

	pk := GetRandomPubKey()
	txID := GetRandomTxHash()
	n := &SolVaultNonce{VaultPubKey: pk, NonceAccount: "acct", Nonce: "nonce", Slot: 7, UpdatedHeight: 9, SetByTx: txID}
	c.Assert(n.IsEmpty(), Equals, false)
	c.Assert(n.Valid(), IsNil)
	c.Assert(n.String(), Equals, "vault:"+pk.String()+" nonce_account:acct nonce:nonce slot:7 updated_height:9 set_by_tx:"+txID.String())

	c.Assert((&SolVaultNonce{NonceAccount: "a", Nonce: "n", Slot: 1}).Valid(), ErrorMatches, ".*empty vault pubkey.*")
	c.Assert((&SolVaultNonce{VaultPubKey: pk, Nonce: "n", Slot: 1}).Valid(), ErrorMatches, ".*empty nonce or nonce account.*")
	c.Assert((&SolVaultNonce{VaultPubKey: pk, NonceAccount: "a", Slot: 1}).Valid(), ErrorMatches, ".*empty nonce or nonce account.*")
	c.Assert((&SolVaultNonce{VaultPubKey: pk, NonceAccount: "a", Nonce: "n"}).Valid(), ErrorMatches, ".*invalid slot 0.*")

	v := &SolNonceAttestVoter{VaultPubKey: pk, NonceAccount: "acct", Nonce: "nonce", LastTx: txID, Slot: 3, CommittedHeight: 4}
	c.Assert(v.Add("a"), Equals, true)
	c.Assert(v.Add("b"), Equals, true)
	c.Assert(v.Add("a"), Equals, false)
	c.Assert(v.Signers, DeepEquals, []string{"a", "b"})
	c.Assert(v.String(), Equals, "vault:"+pk.String()+" nonce_account:acct nonce:nonce last_tx:"+txID.String()+" slot:3 signers:2 committed_height:4")
}

func (s *TypeSmallCovSuite) TestReferenceMemo(c *C) {
	ref := NewReferenceMemo(common.BTCAsset, "=:ETH.ETH:addr", "12345", 100)
	c.Assert(ref.Asset.Equals(common.BTCAsset), Equals, true)
	c.Assert(ref.Memo, Equals, "=:ETH.ETH:addr")
	c.Assert(ref.Key(), Equals, "BTC.BTC/12345")
	c.Assert(ref.IsExpired(150, 50), Equals, false)
	c.Assert(ref.IsExpired(151, 50), Equals, true)

	tx1 := GetRandomTxHash()
	tx2 := GetRandomTxHash()
	c.Assert(ref.GetUsageCount(), Equals, int64(0))
	c.Assert(ref.HasBeenUsedBy(tx1), Equals, false)
	c.Assert(ref.AddUsage(tx1), Equals, true)
	c.Assert(ref.AddUsage(tx1), Equals, false)
	c.Assert(ref.AddUsage(tx2), Equals, true)
	c.Assert(ref.GetUsageCount(), Equals, int64(2))
	c.Assert(ref.HasBeenUsedBy(tx2), Equals, true)
}

func (s *TypeSmallCovSuite) TestStableReserveKeys(c *C) {
	c.Assert(StableReservePendingRescueSwapKey("ETH.USDC"), Equals, "rescue/ETH.USDC")
	cases := []struct {
		memo string
		key  string
		ok   bool
	}{
		{StableReserveDeployMemoPrefix + "anything", StableReservePendingDeploySwapKey, true},
		{StableReserveRescueMemoPrefix + "ETH.USDC:more", "rescue/ETH.USDC", true},
		{StableReserveRescueMemoPrefix + "ETH.USDC", "", false},
		{StableReserveRescueMemoPrefix + ":x", "", false},
		{StableReserveWithdrawFeeMemoPrefix + "x", "", false},
		{"=:BTC.BTC:addr", "", false},
	}
	for _, tc := range cases {
		key, ok := StableReservePendingSwapKeyFromMemo(tc.memo)
		c.Check(key, Equals, tc.key, Commentf(tc.memo))
		c.Check(ok, Equals, tc.ok, Commentf(tc.memo))
	}
}

func (s *TypeSmallCovSuite) TestTCYClaimerAndStaker(c *C) {
	addr := GetRandomBTCAddress()
	cl := NewTCYClaimer(addr, common.BTCAsset, cosmos.NewUint(5))
	c.Assert(cl.Valid(), IsNil)
	c.Assert(cl.IsEmpty(), Equals, false)
	empty := NewTCYClaimer(common.NoAddress, common.EmptyAsset, cosmos.ZeroUint())
	c.Assert(empty.IsEmpty(), Equals, true)
	c.Assert(empty.Valid(), ErrorMatches, "L1 address is empty")
	noAmt := NewTCYClaimer(addr, common.BTCAsset, cosmos.ZeroUint())
	c.Assert(noAmt.Valid(), ErrorMatches, "claim amount is zero")
	noAsset := NewTCYClaimer(addr, common.EmptyAsset, cosmos.NewUint(1))
	c.Assert(noAsset.Valid(), ErrorMatches, "asset is empty")

	thor := GetRandomTHORAddress()
	st := NewTCYStaker(thor, cosmos.NewUint(3))
	c.Assert(st.Valid(), IsNil)
	c.Assert(st.IsEmpty(), Equals, false)
	est := NewTCYStaker(common.NoAddress, cosmos.ZeroUint())
	c.Assert(est.IsEmpty(), Equals, true)
	c.Assert(est.Valid(), ErrorMatches, "address is empty")
	zst := NewTCYStaker(thor, cosmos.ZeroUint())
	c.Assert(zst.Valid(), ErrorMatches, "staking amount is zero")
}

func (s *TypeSmallCovSuite) TestOraclePriceAndPriceFeed(c *C) {
	c.Assert((&OraclePrice{Symbol: "BTC", Price: "1"}).Valid(), IsNil)
	c.Assert((&OraclePrice{Price: "1"}).Valid(), ErrorMatches, "empty symbol")
	c.Assert((&OraclePrice{Symbol: "BTC"}).Valid(), ErrorMatches, "empty amount")

	node := GetRandomBech32Addr()
	rates := []*common.OraclePrice{{Amount: 1, Decimals: 8}}
	c.Assert((&PriceFeed{Node: node, Rates: rates}).Valid(), IsNil)
	c.Assert((&PriceFeed{Rates: rates}).Valid(), ErrorMatches, "node is empty")
	c.Assert((&PriceFeed{Node: node}).Valid(), ErrorMatches, "rates is empty")
}

func (s *TypeSmallCovSuite) TestRUNEPoolAndProvider(c *C) {
	rp := NewRUNEPool()
	c.Assert(rp.TotalUnits().IsZero(), Equals, true)
	c.Assert(rp.CurrentDeposit().IsZero(), Equals, true)
	rp.ReserveUnits = cosmos.NewUint(10)
	rp.PoolUnits = cosmos.NewUint(5)
	rp.RuneDeposited = cosmos.NewUint(100)
	rp.RuneWithdrawn = cosmos.NewUint(150)
	c.Assert(rp.TotalUnits().Uint64(), Equals, uint64(15))
	c.Assert(rp.CurrentDeposit().Int64(), Equals, int64(-50))

	addr := GetRandomBech32Addr()
	prov := NewRUNEProvider(addr)
	c.Assert(prov.Units.IsZero(), Equals, true)
	c.Assert(prov.Key(), Equals, addr.String())
}

func (s *TypeSmallCovSuite) TestSimpleConstructors(c *C) {
	gen := DefaultGenesis()
	c.Assert(gen, NotNil)
	c.Assert(gen.Validate(), IsNil)

	sa := NewSecuredAsset(common.BTCAsset)
	c.Assert(sa.Depth.IsZero(), Equals, true)
	c.Assert(sa.Key(), Equals, "BTC.BTC")

	owner := GetRandomBech32Addr()
	ta := NewTradeAccount(owner, common.ETHAsset)
	c.Assert(ta.Units.IsZero(), Equals, true)
	c.Assert(ta.Key(), Equals, owner.String()+"/ETH.ETH")

	tu := NewTradeUnit(common.ETHAsset)
	c.Assert(tu.Units.IsZero(), Equals, true)
	c.Assert(tu.Depth.IsZero(), Equals, true)
	c.Assert(tu.Key(), Equals, "ETH.ETH")

	from := GetRandomBTCAddress()
	src := common.NewCoin(common.BTCAsset, cosmos.NewUint(1))
	tgt := common.NewCoin(common.ETHAsset, cosmos.NewUint(2))
	mls := NewModifyLimitSwap(from, src, tgt, cosmos.NewUint(3))
	c.Assert(mls.From, Equals, from)
	c.Assert(mls.Source.Equals(src), Equals, true)
	c.Assert(mls.Target.Equals(tgt), Equals, true)
	c.Assert(mls.ModifiedTargetAmount.Uint64(), Equals, uint64(3))

	pr := NewPOLReserveDeposit(common.BTCAsset)
	c.Assert(pr.Asset.Equals(common.BTCAsset), Equals, true)
	c.Assert(pr.RuneDeposited.IsZero(), Equals, true)

	c.Assert(RagnarokWithdrawPosition{Number: 1, Pool: common.BTCAsset}.IsEmpty(), Equals, false)
	c.Assert(RagnarokWithdrawPosition{Number: -1, Pool: common.BTCAsset}.IsEmpty(), Equals, true)
	c.Assert(RagnarokWithdrawPosition{Number: 1}.IsEmpty(), Equals, true)
}

func (s *TypeSmallCovSuite) TestBlameString(c *C) {
	n := &Node{Pubkey: "pk1"}
	c.Assert(n.String(), Equals, "Node: pk1")

	b := &Blame{FailReason: "timeout", IsUnicast: true, BlameNodes: []Node{{Pubkey: "pk1"}}}
	c.Assert(b.IsEmpty(), Equals, false)
	str := b.String()
	c.Assert(strings.HasPrefix(str, "reason:timeout is_unicast:true\n"), Equals, true)
	c.Assert(strings.Contains(str, "pk1"), Equals, true)
	c.Assert(strings.HasSuffix(str, "is unicast:true\n"), Equals, true)
	c.Assert((&Blame{}).IsEmpty(), Equals, true)
}

func (s *TypeSmallCovSuite) TestPOLPnL(c *C) {
	pol := NewProtocolOwnedLiquidity()
	pol.RuneDeposited = cosmos.NewUint(100)
	pol.RuneWithdrawn = cosmos.NewUint(30)
	// withdrawn - deposited + value
	c.Assert(pol.PnL(cosmos.NewUint(50)).Int64(), Equals, int64(-20))
	c.Assert(pol.PnL(cosmos.NewUint(100)).Int64(), Equals, int64(30))
	c.Assert(pol.CurrentDeposit().Int64(), Equals, int64(70))
}

func (s *TypeSmallCovSuite) TestSwapperCloutValid(c *C) {
	// NOTE: Valid() currently returns an error when the address is NOT empty
	// and nil when it is empty (the condition looks inverted). This test
	// documents the current behaviour.
	c.Assert(NewSwapperClout(common.NoAddress).Valid(), IsNil)
	c.Assert(NewSwapperClout(GetRandomBTCAddress()).Valid(), ErrorMatches, "invalid swapper clout address: .*")
}
