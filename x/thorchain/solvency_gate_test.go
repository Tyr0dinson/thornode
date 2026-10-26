package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type SolvencyGateSuite struct{}

var _ = Suite(&SolvencyGateSuite{})

type solvencyGateFixture struct {
	ctx   cosmos.Context
	mgr   *Mgrs
	nodes NodeAccounts
	vault Vault
}

func newSolvencyGateFixture(c *C, gated bool) *solvencyGateFixture {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	f := &solvencyGateFixture{ctx: ctx, mgr: mgr, nodes: make(NodeAccounts, 4)}
	for i := range f.nodes {
		f.nodes[i] = GetRandomValidatorNode(NodeActive)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, f.nodes[i]), IsNil)
	}
	f.vault = NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), []string{common.GAIAChain.String()}, nil)
	c.Assert(mgr.Keeper().SetVault(ctx, f.vault), IsNil)

	pool := NewPool()
	pool.Asset = common.ATOMAsset
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.BalanceRune = cosmos.NewUint(10000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.GAIAChain, NewNetworkFee(common.GAIAChain, 1, 100000)), IsNil)
	// $500 permitted solvency gap = 100 RUNE = 10 ATOM.
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", 5_00000000)
	if gated {
		mgr.Keeper().SetMimir(ctx, fmt.Sprintf(constants.MimirTemplateSolvencyGateInbound, common.GAIAChain), 1)
	}
	return f
}

// observe brings a finalised 100 ATOM inbound at the external height to consensus.
func (f *solvencyGateFixture) observe(c *C, memo string, height int64) common.ObservedTx {
	to, err := f.vault.PubKey.GetAddress(common.GAIAChain)
	c.Assert(err, IsNil)
	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.GAIAChain,
		FromAddress: GetRandomGAIAAddress(),
		ToAddress:   to,
		Coins:       common.NewCoins(common.NewCoin(common.ATOMAsset, cosmos.NewUint(100*common.One))),
		Gas:         common.Gas{common.NewCoin(common.ATOMAsset, cosmos.NewUint(common.One/100))},
		Memo:        memo,
	}, height, f.vault.PubKey, height)
	handler := NewObservedTxInHandler(f.mgr)
	for _, na := range f.nodes {
		_, err = handler.handle(f.ctx, *NewMsgObservedTxIn(common.ObservedTxs{tx}, na.NodeAddress))
		c.Assert(err, IsNil)
	}
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.FinalisedHeight, Equals, f.ctx.BlockHeight())
	return tx
}

// reportSolvency brings a solvency report of the vault's ATOM wallet balance at the
// external height to consensus.
func (f *solvencyGateFixture) reportSolvency(c *C, wallet uint64, height int64) {
	msg, err := NewMsgSolvency(common.GAIAChain, f.vault.PubKey,
		common.NewCoins(common.NewCoin(common.ATOMAsset, cosmos.NewUint(wallet))), height, f.nodes[0].NodeAddress)
	c.Assert(err, IsNil)
	handler := NewSolvencyHandler(f.mgr)
	for _, na := range f.nodes {
		msg.Signer = na.NodeAddress
		_, err = handler.Run(f.ctx, msg)
		c.Assert(err, IsNil)
	}
}

func (f *solvencyGateFixture) nextBlock() {
	f.ctx = f.ctx.WithBlockHeight(f.ctx.BlockHeight() + 1)
}

func (f *solvencyGateFixture) endBlock(c *C) {
	c.Assert(releaseSolvencyGatedInbounds(f.ctx, f.mgr), IsNil)
}

func (f *solvencyGateFixture) parked(c *C) []types.SolvencyGatedInbound {
	inbounds, err := f.mgr.Keeper().GetSolvencyGatedInbounds(f.ctx, common.GAIAChain, 100)
	c.Assert(err, IsNil)
	return inbounds
}

func (f *solvencyGateFixture) poolAsset(c *C) cosmos.Uint {
	pool, err := f.mgr.Keeper().GetPool(f.ctx, common.ATOMAsset)
	c.Assert(err, IsNil)
	return pool.BalanceAsset
}

func (f *solvencyGateFixture) vaultATOM(c *C) cosmos.Uint {
	vault, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	return vault.GetCoin(common.ATOMAsset).Amount
}

func (f *solvencyGateFixture) outbounds(c *C) []TxOutItem {
	items, err := f.mgr.TxOutStore().GetOutboundItems(f.ctx)
	c.Assert(err, IsNil)
	return items
}

func (s *SolvencyGateSuite) TestUngatedChainExecutesImmediately(c *C) {
	f := newSolvencyGateFixture(c, false)
	before := f.poolAsset(c)
	f.observe(c, "DONATE:GAIA.ATOM", 50)
	c.Assert(f.parked(c), HasLen, 0)
	c.Assert(f.poolAsset(c).Equal(before.Add(cosmos.NewUint(100*common.One))), Equals, true)
}

func (s *SolvencyGateSuite) TestParkedUntilSolventConsensusInLaterBlock(c *C) {
	f := newSolvencyGateFixture(c, true)
	before := f.poolAsset(c)
	tx := f.observe(c, "DONATE:GAIA.ATOM", 50)

	// Credited to the vault, but not executed.
	c.Assert(f.vaultATOM(c).Equal(cosmos.NewUint(100*common.One)), Equals, true)
	parked := f.parked(c)
	c.Assert(parked, HasLen, 1)
	c.Assert(parked[0].TxID.Equals(tx.Tx.ID), Equals, true)
	c.Assert(parked[0].PubKey.Equals(f.vault.PubKey), Equals, true)
	c.Assert(parked[0].Height, Equals, int64(50))
	c.Assert(parked[0].ParkedAt, Equals, f.ctx.BlockHeight())
	f.endBlock(c)
	c.Assert(f.parked(c), HasLen, 1)
	c.Assert(f.poolAsset(c).Equal(before), Equals, true)

	// A consensus in the block that parked the inbound may have been checked
	// against a vault balance without it, so it does not release the inbound.
	f.reportSolvency(c, 100*common.One, 50)
	f.endBlock(c)
	c.Assert(f.parked(c), HasLen, 1)
	f.nextBlock()
	f.endBlock(c)
	c.Assert(f.parked(c), HasLen, 1)
	c.Assert(f.poolAsset(c).Equal(before), Equals, true)

	// A solvent consensus in a later block releases it.
	f.reportSolvency(c, 100*common.One, 60)
	f.endBlock(c)
	c.Assert(f.parked(c), HasLen, 0)
	c.Assert(f.poolAsset(c).Equal(before.Add(cosmos.NewUint(100*common.One))), Equals, true)
	halt, err := f.mgr.Keeper().GetMimir(f.ctx, "SolvencyHaltGAIAChain")
	c.Assert(err, IsNil)
	c.Assert(halt <= 0, Equals, true)
}

func (s *SolvencyGateSuite) TestSolventReportBelowInboundHeightDoesNotRelease(c *C) {
	f := newSolvencyGateFixture(c, true)
	f.observe(c, "DONATE:GAIA.ATOM", 50)
	f.observe(c, "DONATE:GAIA.ATOM", 70)
	f.nextBlock()
	// Only the inbound at or below the confirmed height is released.
	c.Assert(f.mgr.Keeper().SetSolvencyConfirmation(f.ctx, common.GAIAChain, f.vault.PubKey, 60), IsNil)
	f.nextBlock()
	f.endBlock(c)
	parked := f.parked(c)
	c.Assert(parked, HasLen, 1)
	c.Assert(parked[0].Height, Equals, int64(70))
}

func (s *SolvencyGateSuite) TestInsolventReportHaltsBeforeRefund(c *C) {
	f := newSolvencyGateFixture(c, true)
	// An unparsable memo would be refunded at once on an ungated chain.
	f.observe(c, "not a memo", 50)
	c.Assert(f.outbounds(c), HasLen, 0)

	// The funds never reached the vault: the wallet holds nothing.
	f.nextBlock()
	f.reportSolvency(c, 0, 50)
	halt, err := f.mgr.Keeper().GetMimir(f.ctx, "SolvencyHaltGAIAChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, f.ctx.BlockHeight())

	f.endBlock(c)
	f.nextBlock()
	f.endBlock(c)
	c.Assert(f.parked(c), HasLen, 1)
	c.Assert(f.outbounds(c), HasLen, 0)
	confirmed, _, err := f.mgr.Keeper().GetSolvencyConfirmation(f.ctx, common.GAIAChain, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(confirmed, Equals, int64(0))
}

func (s *SolvencyGateSuite) TestSolventReleaseRefundsInvalidMemo(c *C) {
	f := newSolvencyGateFixture(c, true)
	f.observe(c, "not a memo", 50)
	f.nextBlock()
	f.reportSolvency(c, 100*common.One, 50)
	f.endBlock(c)
	c.Assert(f.parked(c), HasLen, 0)
	items := f.outbounds(c)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].Coin.Asset.Equals(common.ATOMAsset), Equals, true)
}

func (s *SolvencyGateSuite) TestGateOffReleasesParked(c *C) {
	f := newSolvencyGateFixture(c, true)
	before := f.poolAsset(c)
	f.observe(c, "DONATE:GAIA.ATOM", 50)
	c.Assert(f.parked(c), HasLen, 1)

	f.nextBlock()
	f.mgr.Keeper().SetMimir(f.ctx, fmt.Sprintf(constants.MimirTemplateSolvencyGateInbound, common.GAIAChain), 0)
	f.endBlock(c)
	c.Assert(f.parked(c), HasLen, 0)
	c.Assert(f.poolAsset(c).Equal(before.Add(cosmos.NewUint(100*common.One))), Equals, true)
}

func (s *SolvencyGateSuite) TestRevertedParkedInboundIsDropped(c *C) {
	f := newSolvencyGateFixture(c, true)
	before := f.poolAsset(c)
	tx := f.observe(c, "DONATE:GAIA.ATOM", 50)
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	voter.SetReverted()
	f.mgr.Keeper().SetObservedTxInVoter(f.ctx, voter)

	f.nextBlock()
	f.reportSolvency(c, 100*common.One, 50)
	f.endBlock(c)
	c.Assert(f.parked(c), HasLen, 0)
	c.Assert(f.poolAsset(c).Equal(before), Equals, true)
}

func (s *SolvencyGateSuite) TestReleaseBoundedPerBlock(c *C) {
	f := newSolvencyGateFixture(c, true)
	k := f.mgr.Keeper()
	for i := 0; i < solvencyGateMaxReleasesPerBlock+5; i++ {
		c.Assert(k.SetSolvencyGatedInbound(f.ctx, types.SolvencyGatedInbound{
			Chain: common.GAIAChain, PubKey: f.vault.PubKey, Height: int64(i), TxID: GetRandomTxHash(), ParkedAt: f.ctx.BlockHeight(),
		}), IsNil)
	}
	f.nextBlock()
	c.Assert(k.SetSolvencyConfirmation(f.ctx, common.GAIAChain, f.vault.PubKey, 1000), IsNil)
	f.nextBlock()
	f.endBlock(c)
	// No voters exist for these tx ids, so each is unparked without executing.
	c.Assert(f.parked(c), HasLen, 5)
	f.endBlock(c)
	c.Assert(f.parked(c), HasLen, 0)
}

// A parked inbound and its voter survive export/import.
func (s *SolvencyGateSuite) TestGenesisRoundTrip(c *C) {
	f := newSolvencyGateFixture(c, true)
	tx := f.observe(c, "DONATE:GAIA.ATOM", 50)
	parked := f.parked(c)
	c.Assert(parked, HasLen, 1)

	exported := ExportGenesis(f.ctx, f.mgr.Keeper())
	c.Assert(exported.SolvencyGatedInbounds, HasLen, 1)
	c.Assert(ValidateGenesis(exported), IsNil)
	bz, err := exported.Marshal()
	c.Assert(err, IsNil)
	var decoded GenesisState
	c.Assert(decoded.Unmarshal(bz), IsNil)

	g := newSolvencyGateFixture(c, true)
	g.ctx = g.ctx.WithBlockHeight(f.ctx.BlockHeight())
	InitGenesis(g.ctx, g.mgr.Keeper(), decoded)
	c.Assert(g.parked(c), DeepEquals, parked)
	voter, err := g.mgr.Keeper().GetObservedTxInVoter(g.ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Tx.Tx.ID.Equals(tx.Tx.ID), Equals, true)

	bad := decoded
	bad.SolvencyGatedInbounds = []types.SolvencyGatedInbound{{Chain: common.GAIAChain, TxID: tx.Tx.ID}}
	c.Assert(ValidateGenesis(bad), NotNil)
}
