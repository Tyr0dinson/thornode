package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type MigrationHelpersCovCSuite struct{}

var _ = Suite(&MigrationHelpersCovCSuite{})

func (s *MigrationHelpersCovCSuite) TestUnsafeAddRefundOutboundErrorsCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	txID := GetRandomTxHash().String()
	dest := GetRandomETHAddress().String()
	ethCoin := common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))

	c.Check(unsafeAddRefundOutbound(ctx, mgr, txID, dest, common.NoCoin, 1), ErrorMatches, "coin must be an external asset")
	c.Check(unsafeAddRefundOutbound(ctx, mgr, txID, dest, common.NewCoin(common.RuneNative, cosmos.OneUint()), 1),
		ErrorMatches, "coin must be an external asset")
	c.Check(unsafeAddRefundOutbound(ctx, mgr, txID, "not an address", ethCoin, 1), NotNil)
	c.Check(unsafeAddRefundOutbound(ctx, mgr, "bad-hash", dest, ethCoin, 1), NotNil)
	// no active vaults at all
	c.Check(unsafeAddRefundOutbound(ctx, mgr, txID, dest, ethCoin, 1), ErrorMatches, "fail to get active asgard vaults.*")

	// an active vault without enough balance
	v := GetRandomVault()
	v.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10)))
	c.Assert(mgr.Keeper().SetVault(ctx, v), IsNil)
	c.Check(unsafeAddRefundOutbound(ctx, mgr, txID, dest, ethCoin, 1), ErrorMatches, "no asgard with enough balance")

	// gas lookup failure on a chain without a network fee
	v.Coins = common.NewCoins(common.NewCoin(common.AVAXAsset, cosmos.NewUint(10*common.One)))
	c.Assert(mgr.Keeper().SetVault(ctx, v), IsNil)
	err := unsafeAddRefundOutbound(ctx, mgr, txID, dest, common.NewCoin(common.AVAXAsset, cosmos.NewUint(common.One)), 1)
	c.Check(err, ErrorMatches, "fail to get max gas.*")

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 0)
}

func (s *MigrationHelpersCovCSuite) TestRequeueDanglingSettlementValidationCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	txID := GetRandomTxHash()
	amount := cosmos.NewUint(common.One)
	good := danglingSettlementSpec{TxID: txID, Chain: common.ETHChain, Asset: common.ETHAsset, Amount: amount}

	spec := good
	spec.TxID = ""
	c.Check(requeueDanglingSettlementOutbound(ctx, mgr, spec, 10), ErrorMatches, "tx id cannot be empty")
	spec = good
	spec.Chain = common.EmptyChain
	c.Check(requeueDanglingSettlementOutbound(ctx, mgr, spec, 10), ErrorMatches, "chain cannot be empty")
	spec = good
	spec.Asset = common.EmptyAsset
	c.Check(requeueDanglingSettlementOutbound(ctx, mgr, spec, 10), ErrorMatches, "asset cannot be empty")
	spec = good
	spec.Amount = cosmos.ZeroUint()
	c.Check(requeueDanglingSettlementOutbound(ctx, mgr, spec, 10), ErrorMatches, "amount cannot be zero")

	// no voter -> no action
	c.Check(requeueDanglingSettlementOutbound(ctx, mgr, good, 10), ErrorMatches, "matching action not found")

	action := TxOutItem{
		Chain:     common.ETHChain,
		InHash:    txID,
		ToAddress: GetRandomETHAddress(),
		Coin:      common.NewCoin(common.ETHAsset, amount),
		Memo:      "OUT:" + txID.String(),
	}

	// action already has an outbound hash
	voter := NewObservedTxVoter(txID, nil)
	withHash := action
	withHash.OutHash = GetRandomTxHash()
	voter.Actions = []TxOutItem{withHash}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	c.Check(requeueDanglingSettlementOutbound(ctx, mgr, good, 10), ErrorMatches, "matching action already has an outbound hash")

	// no active vault to select
	voter.Actions = []TxOutItem{action}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	c.Check(requeueDanglingSettlementOutbound(ctx, mgr, good, 10), ErrorMatches, "no active asgard vaults")

	// gas lookup failure for a chain without network fees
	avaxSpec := danglingSettlementSpec{TxID: txID, Chain: common.AVAXChain, Asset: common.AVAXAsset, Amount: amount}
	avaxAction := action
	avaxAction.Chain = common.AVAXChain
	avaxAction.Coin = common.NewCoin(common.AVAXAsset, amount)
	voter.Actions = []TxOutItem{avaxAction}
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	c.Check(requeueDanglingSettlementOutbound(ctx, mgr, avaxSpec, 10), ErrorMatches, "fail to get max gas.*")
}

func (s *MigrationHelpersCovCSuite) TestFindDanglingSettlementActionCovC(c *C) {
	txID := GetRandomTxHash()
	amount := cosmos.NewUint(common.One)
	spec := danglingSettlementSpec{TxID: txID, Chain: common.ETHChain, Asset: common.ETHAsset, Amount: amount}
	match := TxOutItem{Chain: common.ETHChain, InHash: txID, Coin: common.NewCoin(common.ETHAsset, amount)}

	otherHash := match
	otherHash.InHash = GetRandomTxHash()
	otherChain := match
	otherChain.Chain = common.BTCChain
	otherAsset := match
	otherAsset.Coin.Asset = common.BTCAsset
	otherAmount := match
	otherAmount.Coin.Amount = amount.Add(cosmos.OneUint())

	voter := NewObservedTxVoter(txID, nil)
	voter.Actions = []TxOutItem{otherHash, otherChain, otherAsset, otherAmount, match}
	idx, got, err := findDanglingSettlementAction(voter, spec)
	c.Assert(err, IsNil)
	c.Check(idx, Equals, 4)
	c.Check(got.Equals(match), Equals, true)

	voter.Actions = []TxOutItem{otherHash, otherChain, otherAsset, otherAmount}
	idx, _, err = findDanglingSettlementAction(voter, spec)
	c.Check(err, ErrorMatches, "matching action not found")
	c.Check(idx, Equals, -1)

	voter.Actions = []TxOutItem{match, match}
	idx, _, err = findDanglingSettlementAction(voter, spec)
	c.Check(err, ErrorMatches, "multiple matching actions found")
	c.Check(idx, Equals, -1)
}

func (s *MigrationHelpersCovCSuite) TestFixVaultClampsSkipsInvalidCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	v := GetRandomVault()
	v.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000)))
	c.Assert(mgr.Keeper().SetVault(ctx, v), IsNil)

	fixVaultClamps_v3_18_0(ctx, mgr, []vaultClampFix{
		{pubKey: "not-a-pubkey", amount: 1},
		{pubKey: GetRandomPubKey().String(), amount: 1}, // unknown vault
		{pubKey: v.PubKey.String(), amount: 100},
	})

	got, err := mgr.Keeper().GetVault(ctx, v.PubKey)
	c.Assert(err, IsNil)
	c.Check(got.GetCoin(common.ETHAsset).Amount.Uint64(), Equals, uint64(900))
}
