package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// A halted chain blocks churn only while vaults still hold exposure on it. Without
// this, halting a newly activated chain would stop rotation network-wide.
type ChainHaltChurnTestSuite struct{}

var _ = Suite(&ChainHaltChurnTestSuite{})

func (s *ChainHaltChurnTestSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func newFundsCheckVault(status types.VaultStatus, coins common.Coins) Vault {
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = status
	vault.Chains = common.Chains{common.THORChain, common.BTCChain}.Strings()
	vault.Coins = coins
	return vault
}

func (s *ChainHaltChurnTestSuite) TestNoFundsAnywhereDoesNotBlock(c *C) {
	ctx, mgr := setupManagerForTest(c)
	c.Assert(mgr.Keeper().SetVault(ctx, newFundsCheckVault(ActiveVault, nil)), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, newFundsCheckVault(RetiringVault, nil)), IsNil)

	c.Assert(chainHasVaultFunds(ctx, mgr.Keeper(), common.BTCChain), Equals, false)
}

func (s *ChainHaltChurnTestSuite) TestActiveVaultFundsBlock(c *C) {
	ctx, mgr := setupManagerForTest(c)
	coins := common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100)))
	c.Assert(mgr.Keeper().SetVault(ctx, newFundsCheckVault(ActiveVault, coins)), IsNil)

	c.Assert(chainHasVaultFunds(ctx, mgr.Keeper(), common.BTCChain), Equals, true)
}

// The activeChains set the churn gate iterates is built from active vaults only, so
// a funded retiring vault would otherwise be invisible - yet migration flows out of it.
func (s *ChainHaltChurnTestSuite) TestRetiringVaultFundsBlock(c *C) {
	ctx, mgr := setupManagerForTest(c)
	coins := common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100)))
	c.Assert(mgr.Keeper().SetVault(ctx, newFundsCheckVault(ActiveVault, nil)), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, newFundsCheckVault(RetiringVault, coins)), IsNil)

	c.Assert(chainHasVaultFunds(ctx, mgr.Keeper(), common.BTCChain), Equals, true)
}

// An outstanding deficit is real exposure even with a zero balance.
func (s *ChainHaltChurnTestSuite) TestDeficitBlocks(c *C) {
	ctx, mgr := setupManagerForTest(c)
	vault := newFundsCheckVault(ActiveVault, nil)
	vault.Deficit = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(50)))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	c.Assert(chainHasVaultFunds(ctx, mgr.Keeper(), common.BTCChain), Equals, true)
}

// Funds on a different chain must not block this one.
func (s *ChainHaltChurnTestSuite) TestFundsOnOtherChainDoNotBlock(c *C) {
	ctx, mgr := setupManagerForTest(c)
	coins := common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100)))
	c.Assert(mgr.Keeper().SetVault(ctx, newFundsCheckVault(ActiveVault, coins)), IsNil)

	c.Assert(chainHasVaultFunds(ctx, mgr.Keeper(), common.LTCChain), Equals, false)
}

// Zero-amount coins are not exposure.
func (s *ChainHaltChurnTestSuite) TestZeroAmountCoinDoesNotBlock(c *C) {
	ctx, mgr := setupManagerForTest(c)
	coins := common.Coins{common.NewCoin(common.BTCAsset, cosmos.ZeroUint())}
	c.Assert(mgr.Keeper().SetVault(ctx, newFundsCheckVault(ActiveVault, coins)), IsNil)

	c.Assert(chainHasVaultFunds(ctx, mgr.Keeper(), common.BTCChain), Equals, false)
}

// End-to-end through the rotation gate: a halted chain with no funds must not stop
// BeginBlock from proceeding past the halt check.
func (s *ChainHaltChurnTestSuite) TestHaltedEmptyChainDoesNotStopRotation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	vault := newFundsCheckVault(ActiveVault, nil)
	vault.Chains = common.Chains{common.THORChain, common.BTCChain, common.LTCChain}.Strings()
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	mgr.Keeper().SetMimir(ctx, fmt.Sprintf("Halt%sChain", common.LTCChain), 1)

	c.Assert(mgr.Keeper().IsChainHalted(ctx, common.LTCChain), Equals, true)
	c.Assert(chainHasVaultFunds(ctx, mgr.Keeper(), common.LTCChain), Equals, false)
}
