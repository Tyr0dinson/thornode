package thorchain

import (
	"fmt"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type CovCCAffiliatesSuite struct{}

var _ = Suite(&CovCCAffiliatesSuite{})

func (CovCCAffiliatesSuite) TestSkimSanityChecks(c *C) {
	ctx, mgr := setupManagerForTest(c)
	signer := GetRandomBech32Addr()
	coin := common.NewCoin(common.RuneNative, cosmos.NewUint(common.One))
	memo := fmt.Sprintf("=:THOR.RUNE:%s::%s:100", GetRandomTHORAddress(), GetRandomTHORAddress())

	_, err := skimAffiliateFees(ctx, mgr, common.Tx{}, signer, coin, memo)
	c.Assert(err, ErrorMatches, "main tx is empty")

	_, err = skimAffiliateFees(ctx, mgr, GetRandomTx(), signer, common.NoCoin, memo)
	c.Assert(err, ErrorMatches, "coin is empty")

	_, err = skimAffiliateFees(ctx, mgr, GetRandomTx(), signer, coin, "BOGUS:memo")
	c.Assert(err, NotNil)

	// no affiliates -> nothing skimmed
	fee, err := skimAffiliateFees(ctx, mgr, GetRandomTx(), signer, coin, fmt.Sprintf("=:THOR.RUNE:%s", GetRandomTHORAddress()))
	c.Assert(err, IsNil)
	c.Assert(fee.IsZero(), Equals, true)
}

func (CovCCAffiliatesSuite) TestSkimSkipsIneligibleAffiliates(c *C) {
	ctx, mgr := setupManagerForTest(c)
	signer := GetRandomBech32Addr()
	coin := common.NewCoin(common.RuneNative, cosmos.NewUint(10*common.One))

	// thorname with a preferred asset but no alias for it cannot receive fees
	owner := GetRandomBech32Addr()
	tn := NewTHORName("nob", 1000, []THORNameAlias{{Chain: common.THORChain, Address: common.Address(owner.String())}})
	tn.Owner = owner
	tn.PreferredAsset = common.BTCAsset
	mgr.Keeper().SetTHORName(ctx, tn)
	c.Assert(tn.CanReceiveAffiliateFee(), Equals, false)

	good := GetRandomTHORAddress()
	memo := fmt.Sprintf("=:THOR.RUNE:%s::nob/%s:100/100", GetRandomTHORAddress(), good)
	fee, err := skimAffiliateFees(ctx, mgr, GetRandomTx(), signer, coin, memo)
	c.Assert(err, IsNil)
	// only the plain address affiliate is paid
	c.Assert(fee.Uint64(), Equals, uint64(10*common.One/100))
	ownerBal := mgr.Keeper().GetBalance(ctx, owner).AmountOf(common.RuneNative.Native())
	c.Assert(ownerBal.IsZero(), Equals, true)
	goodAcc, err := good.AccAddress()
	c.Assert(err, IsNil)
	c.Assert(mgr.Keeper().GetBalance(ctx, goodAcc).AmountOf(common.RuneNative.Native()).Uint64(), Equals, uint64(10*common.One/100))
}

func (CovCCAffiliatesSuite) TestSkimRuneToCollectorViaOwner(c *C) {
	ctx, mgr, _, _ := setupAffiliateCollectorPartners(c)
	k := mgr.Keeper()

	// thorname with preferred asset + BTC alias but no THOR alias
	owner := GetRandomBech32Addr()
	tn := NewTHORName("own", 1000, []THORNameAlias{{Chain: common.BTCChain, Address: GetRandomBTCAddress()}})
	tn.Owner = owner
	tn.PreferredAsset = common.BTCAsset
	tn.PreferredAssetOutboundFeeMultiplier = 1000
	k.SetTHORName(ctx, tn)

	memo := fmt.Sprintf("=:THOR.RUNE:%s::own:100", GetRandomTHORAddress())
	fee, err := skimAffiliateFees(ctx, mgr, GetRandomTx(), GetRandomBech32Addr(), common.NewCoin(common.RuneNative, cosmos.NewUint(5000)), memo)
	c.Assert(err, IsNil)
	c.Assert(fee.Uint64(), Equals, uint64(50))
	assertNamedAffiliateBalance(c, ctx, k, "own", 50)
	c.Assert(k.GetBalance(ctx, owner).AmountOf(common.RuneNative.Native()).IsZero(), Equals, true)
}

func (CovCCAffiliatesSuite) TestSkimNonRuneQueuesSwapToRune(c *C) {
	for _, advanced := range []int64{0, 1} {
		ctx, mgr, _, _ := setupAffiliateCollectorPartners(c)
		k := mgr.Keeper()
		k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), advanced)

		aff := GetRandomTHORAddress()
		tx := GetRandomTx()
		tx.Chain = common.BTCChain
		tx.FromAddress = GetRandomBTCAddress()
		tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)))
		memo := fmt.Sprintf("=:BTC.BTC:%s::%s:100", GetRandomBTCAddress(), aff)
		fee, err := skimAffiliateFees(ctx, mgr, tx, GetRandomBech32Addr(), common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)), memo)
		c.Assert(err, IsNil)
		c.Assert(fee.Uint64(), Equals, uint64(10*common.One/100))

		swaps := affiliateIntegrationQueuedSwaps(c, ctx, k)
		c.Assert(swaps, HasLen, 1, Commentf("advanced=%d", advanced))
		c.Assert(swaps[0].TargetAsset.IsRune(), Equals, true)
		c.Assert(swaps[0].Destination.Equals(aff), Equals, true)
		c.Assert(swaps[0].Tx.Coins[0].Amount.Uint64(), Equals, uint64(10*common.One/100))
	}
}

func (CovCCAffiliatesSuite) TestSkimNonRuneToPreferredAssetCollector(c *C) {
	for _, advanced := range []int64{0, 1} {
		ctx, mgr, few, _ := setupAffiliateCollectorPartners(c)
		k := mgr.Keeper()
		k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), advanced)
		// accrued balance above threshold (100 * multiplier 1) triggers a payout
		fundNamedCollector(c, ctx, k, "few", 1000)

		tx := GetRandomTx()
		tx.Chain = common.BTCChain
		tx.FromAddress = GetRandomBTCAddress()
		tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)))
		memo := fmt.Sprintf("=:BTC.BTC:%s::few:100", GetRandomBTCAddress())
		fee, err := skimAffiliateFees(ctx, mgr, tx, GetRandomBech32Addr(), common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)), memo)
		c.Assert(err, IsNil)
		c.Assert(fee.Uint64(), Equals, uint64(10*common.One/100))

		collectorAddr, err := k.GetModuleAddress(AffiliateCollectorName)
		c.Assert(err, IsNil)
		swaps := affiliateIntegrationQueuedSwaps(c, ctx, k)
		c.Assert(swaps, HasLen, 2, Commentf("advanced=%d", advanced))
		var conversion, payout int
		for _, sw := range swaps {
			if strings.HasPrefix(sw.Tx.Memo, PreferredAssetSwapMemoPrefix) {
				payout++
				c.Assert(sw.TargetAsset.Equals(few.PreferredAsset), Equals, true)
			} else {
				conversion++
				c.Assert(sw.Destination.Equals(collectorAddr), Equals, true)
				c.Assert(sw.AffiliateAddress.IsEmpty(), Equals, false)
			}
		}
		c.Assert(conversion, Equals, 1)
		c.Assert(payout, Equals, 1)
		// accrued balance moved to asgard for the payout swap
		assertNamedAffiliateBalance(c, ctx, k, "few", 0)
	}
}

func (CovCCAffiliatesSuite) TestSkimNonRuneSwapWouldFail(c *C) {
	ctx, mgr := setupManagerForTest(c)
	// no ETH pool: the affiliate swap cannot succeed and the fee is skipped
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One)))
	memo := fmt.Sprintf("=:ETH.ETH:%s::%s:100", GetRandomETHAddress(), GetRandomTHORAddress())
	fee, err := skimAffiliateFees(ctx, mgr, tx, GetRandomBech32Addr(), common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One)), memo)
	c.Assert(err, IsNil)
	c.Assert(fee.IsZero(), Equals, true)
	c.Assert(affiliateIntegrationQueuedSwaps(c, ctx, mgr.Keeper()), HasLen, 0)
}

func (CovCCAffiliatesSuite) TestTriggerPreferredAssetSwapErrors(c *C) {
	ctx, mgr, few, _ := setupAffiliateCollectorPartners(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)

	// no alias for the preferred asset chain
	noAlias := few
	noAlias.PreferredAsset = common.ETHAsset
	err := triggerPreferredAssetSwap(ctx, mgr, noAlias, AffiliateFeeCollector{Name: "few", RuneAmount: cosmos.NewUint(10)}, 1)
	c.Assert(err, ErrorMatches, "no alias for preferred asset.*")

	// zero accrued
	err = triggerPreferredAssetSwap(ctx, mgr, few, AffiliateFeeCollector{Name: "few", RuneAmount: cosmos.ZeroUint()}, 1)
	c.Assert(err, ErrorMatches, ".*accrued RUNE amount is zero")

	// more than the module holds
	err = triggerPreferredAssetSwap(ctx, mgr, few, AffiliateFeeCollector{Name: "few", RuneAmount: cosmos.NewUint(10)}, 1)
	c.Assert(err, ErrorMatches, "rune amount greater than module balance.*")

	// first payout succeeds
	col := fundNamedCollector(c, ctx, k, "few", 1000)
	c.Assert(triggerPreferredAssetSwap(ctx, mgr, few, col, 1), IsNil)
	assertNamedAffiliateBalance(c, ctx, k, "few", 0)
	c.Assert(affiliateIntegrationQueuedSwaps(c, ctx, k), HasLen, 1)

	// same amount in the same block -> duplicate tx id
	col = fundNamedCollector(c, ctx, k, "few", 1000)
	err = triggerPreferredAssetSwap(ctx, mgr, few, col, 2)
	c.Assert(err, ErrorMatches, "preferred asset tx: .* already exists")
	assertNamedAffiliateBalance(c, ctx, k, "few", 1000)
}

func (CovCCAffiliatesSuite) TestCollectorHelpersErrors(c *C) {
	ctx, mgr, few, _ := setupAffiliateCollectorPartners(c)
	k := mgr.Keeper()

	// invalid restore memos
	c.Assert(restorePreferredAssetCollector(ctx, mgr, "garbage", cosmos.NewUint(1)), ErrorMatches, "invalid preferred asset swap memo.*")
	c.Assert(restorePreferredAssetCollector(ctx, mgr, PreferredAssetSwapMemoPrefix+"-", cosmos.NewUint(1)), ErrorMatches, "invalid preferred asset swap memo.*")
	// zero amount restore is a no-op
	c.Assert(restorePreferredAssetCollector(ctx, mgr, PreferredAssetSwapMemoPrefix+"-few", cosmos.ZeroUint()), IsNil)
	assertNamedAffiliateBalance(c, ctx, k, "few", 0)

	// owner validation
	c.Assert(validateAffiliateCollectorOwner(ctx, k, cosmos.AccAddress{}), ErrorMatches, "invalid affiliate collector owner.*")
	c.Assert(validateAffiliateCollectorOwner(ctx, k, k.GetModuleAccAddress(ReserveName)), ErrorMatches, ".*cannot be a module address")
	c.Assert(validateAffiliateCollectorOwner(ctx, k, GetRandomBech32Addr()), IsNil)

	// settle with nil / zero balances
	c.Assert(settleTHORNameCollector(ctx, k, AffiliateFeeCollector{Name: "few"}, GetRandomBech32Addr()), ErrorMatches, ".*uninitialized RUNE balance")
	c.Assert(settleTHORNameCollector(ctx, k, AffiliateFeeCollector{Name: "few", RuneAmount: cosmos.ZeroUint()}, GetRandomBech32Addr()), IsNil)

	// collector credit from an unfunded module fails without changing state
	swapIndex := 0
	err := addRuneToAffiliateCollectorFromModule(ctx, mgr, TCYStakeName, common.NewCoin(common.RuneAsset(), cosmos.NewUint(5)), &few, &swapIndex)
	c.Assert(err, ErrorMatches, "failed to send coin to affiliate collector.*")
	assertNamedAffiliateBalance(c, ctx, k, "few", 0)
	c.Assert(swapIndex, Equals, 0)
}

func (CovCCAffiliatesSuite) TestSkimTransferFailuresSkipFee(c *C) {
	ctx, mgr, _, _ := setupAffiliateCollectorPartners(c)
	k := mgr.Keeper()
	signer := GetRandomBech32Addr()

	// affiliate fee larger than asgard holds -> RUNE transfer fails, fee skipped
	aff := GetRandomTHORAddress()
	memo := fmt.Sprintf("=:THOR.RUNE:%s::%s:100", GetRandomTHORAddress(), aff)
	huge := common.NewCoin(common.RuneNative, cosmos.NewUint(100_000_000_000*common.One))
	fee, err := skimAffiliateFees(ctx, mgr, GetRandomTx(), signer, huge, memo)
	c.Assert(err, IsNil)
	c.Assert(fee.IsZero(), Equals, true)
	affAcc, err := aff.AccAddress()
	c.Assert(err, IsNil)
	c.Assert(k.GetBalance(ctx, affAcc).AmountOf(common.RuneNative.Native()).IsZero(), Equals, true)

	// collector credit failure for a preferred-asset thorname -> fee skipped
	mgr.K = &collectorFailKeeper{Keeper: k, failTransfer: true}
	memo = fmt.Sprintf("=:THOR.RUNE:%s::few:100", GetRandomTHORAddress())
	fee, err = skimAffiliateFees(ctx, mgr, GetRandomTx(), signer, common.NewCoin(common.RuneNative, cosmos.NewUint(5000)), memo)
	c.Assert(err, IsNil)
	c.Assert(fee.IsZero(), Equals, true)
	assertNamedAffiliateBalance(c, ctx, k, "few", 0)
}

func (CovCCAffiliatesSuite) TestSkimNonTHORChainAffiliateAddress(c *C) {
	ctx, mgr := setupManagerForTest(c)
	memo := fmt.Sprintf("=:THOR.RUNE:%s::%s:100", GetRandomTHORAddress(), GetRandomETHAddress())
	fee, err := skimAffiliateFees(ctx, mgr, GetRandomTx(), GetRandomBech32Addr(), common.NewCoin(common.RuneNative, cosmos.NewUint(5000)), memo)
	c.Assert(err, IsNil)
	c.Assert(fee.IsZero(), Equals, true)
}
