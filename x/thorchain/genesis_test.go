package thorchain

import (
	"os"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type GenesisTestSuite struct{}

var _ = Suite(&GenesisTestSuite{})

func (GenesisTestSuite) TestGenesis(c *C) {
	SetupConfigForTest()
	genesisState := DefaultGenesisState()
	c.Assert(ValidateGenesis(genesisState), IsNil)
	ctx, mgr := setupManagerForTest(c)
	gs := ExportGenesis(ctx, mgr.Keeper())
	c.Assert(ValidateGenesis(gs), IsNil)
	content, err := os.ReadFile("../../test/fixtures/genesis/genesis.json")
	c.Assert(err, IsNil)
	c.Assert(content, NotNil)
	ctx, mgr = setupManagerForTest(c)
	var state GenesisState
	c.Assert(ModuleCdc.UnmarshalJSON(content, &state), IsNil)
	result := InitGenesis(ctx, mgr.Keeper(), state)
	c.Assert(result, NotNil)
	gs1 := ExportGenesis(ctx, mgr.Keeper())
	c.Assert(len(gs1.Pools) > 0, Equals, true)
}

func (GenesisTestSuite) TestGenesisSupportedChainsRoundTrip(c *C) {
	SetupConfigForTest()
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	na := GetRandomValidatorNode(NodeActive)
	na.SupportedChains = []string{common.BTCChain.String(), common.ETHChain.String()}
	na.Bond = cosmos.NewUint(1000 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	naNoChains := GetRandomValidatorNode(NodeActive)
	naNoChains.Bond = cosmos.NewUint(1000 * common.One)
	naNoChains.SupportedChains = nil
	c.Assert(k.SetNodeAccount(ctx, naNoChains), IsNil)

	gs := ExportGenesis(ctx, k)
	c.Assert(ValidateGenesis(gs), IsNil)

	var foundWithChains, foundWithoutChains bool
	for _, exported := range gs.NodeAccounts {
		if exported.NodeAddress.Equals(na.NodeAddress) {
			c.Assert(exported.SupportedChains, HasLen, 2)
			c.Assert(exported.SupportedChains[0], Equals, common.BTCChain.String())
			c.Assert(exported.SupportedChains[1], Equals, common.ETHChain.String())
			foundWithChains = true
		}
		if exported.NodeAddress.Equals(naNoChains.NodeAddress) {
			c.Assert(exported.SupportedChains, HasLen, 0)
			foundWithoutChains = true
		}
	}
	c.Assert(foundWithChains, Equals, true)
	c.Assert(foundWithoutChains, Equals, true)

	ctx2, mgr2 := setupManagerForTest(c)
	k2 := mgr2.Keeper()
	InitGenesis(ctx2, k2, gs)

	imported, err := k2.GetNodeAccount(ctx2, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(imported.SupportedChains, HasLen, 2)
	c.Assert(imported.SupportedChains[0], Equals, common.BTCChain.String())
	c.Assert(imported.SupportedChains[1], Equals, common.ETHChain.String())

	importedNil, err := k2.GetNodeAccount(ctx2, naNoChains.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(importedNil.SupportedChains, HasLen, 0)
}

func (GenesisTestSuite) TestGenesisPOLReserveDepositRoundTrip(c *C) {
	SetupConfigForTest()

	// Seed a source keeper with two POL reserve deposits whose amounts must
	// survive an export/import round trip.
	srcCtx, srcMgr := setupManagerForTest(c)
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)

	btcDeposit := NewPOLReserveDeposit(common.BTCAsset)
	btcDeposit.RuneDeposited = cosmos.NewUint(12345 * common.One)
	c.Assert(srcMgr.Keeper().SetPOLReserveDeposit(srcCtx, btcDeposit), IsNil)

	usdcDeposit := NewPOLReserveDeposit(usdc)
	usdcDeposit.RuneDeposited = cosmos.NewUint(678 * common.One)
	c.Assert(srcMgr.Keeper().SetPOLReserveDeposit(srcCtx, usdcDeposit), IsNil)

	// Export must contain both records.
	exported := ExportGenesis(srcCtx, srcMgr.Keeper())
	found := map[string]cosmos.Uint{}
	for _, prd := range exported.PolReserveDeposits {
		found[prd.Asset.String()] = prd.RuneDeposited
	}
	c.Check(found[common.BTCAsset.String()].String(), Equals, btcDeposit.RuneDeposited.String())
	c.Check(found[usdc.String()].String(), Equals, usdcDeposit.RuneDeposited.String())

	// Import into a fresh keeper and confirm each per-asset record round-trips.
	dstCtx, dstMgr := setupManagerForTest(c)
	InitGenesis(dstCtx, dstMgr.Keeper(), exported)

	gotBTC, err := dstMgr.Keeper().GetPOLReserveDeposit(dstCtx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(gotBTC.Asset.Equals(common.BTCAsset), Equals, true)
	c.Check(gotBTC.RuneDeposited.String(), Equals, btcDeposit.RuneDeposited.String())

	gotUSDC, err := dstMgr.Keeper().GetPOLReserveDeposit(dstCtx, usdc)
	c.Assert(err, IsNil)
	c.Check(gotUSDC.Asset.Equals(usdc), Equals, true)
	c.Check(gotUSDC.RuneDeposited.String(), Equals, usdcDeposit.RuneDeposited.String())
}
