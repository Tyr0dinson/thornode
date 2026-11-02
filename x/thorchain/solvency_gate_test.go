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
	return f.observeAs(c, memo, height, f.vault.PubKey)
}

// observeAs is observe with the vault observed under the given pubkey, as an
// EdDSA or FROST chain observes it.
func (f *solvencyGateFixture) observeAs(c *C, memo string, height int64, observedPubKey common.PubKey) common.ObservedTx {
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
	}, height, observedPubKey, height)
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

// parked returns every inbound parked on GAIA, across vaults.
func (f *solvencyGateFixture) parked(c *C) []types.SolvencyGatedInbound {
	iter := f.mgr.Keeper().GetSolvencyGatedInboundIterator(f.ctx)
	defer iter.Close()
	var inbounds []types.SolvencyGatedInbound
	for ; iter.Valid(); iter.Next() {
		var in types.SolvencyGatedInbound
		c.Assert(f.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &in), IsNil)
		if in.Chain.Equals(common.GAIAChain) {
			inbounds = append(inbounds, in)
		}
	}
	return inbounds
}

// parkWithVoter parks an inbound to the vault directly, with a finalised voter for it.
func (f *solvencyGateFixture) parkWithVoter(c *C, pubKey common.PubKey, height int64) types.SolvencyGatedInbound {
	to, err := pubKey.GetAddress(common.GAIAChain)
	c.Assert(err, IsNil)
	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.GAIAChain,
		FromAddress: GetRandomGAIAAddress(),
		ToAddress:   to,
		Coins:       common.NewCoins(common.NewCoin(common.ATOMAsset, cosmos.NewUint(common.One))),
		Gas:         common.Gas{common.NewCoin(common.ATOMAsset, cosmos.NewUint(common.One/100))},
		Memo:        "DONATE:GAIA.ATOM",
	}, height, pubKey, height)
	voter := NewObservedTxVoter(tx.Tx.ID, []common.ObservedTx{tx})
	voter.Tx = tx
	f.mgr.Keeper().SetObservedTxInVoter(f.ctx, voter)
	in := types.SolvencyGatedInbound{Chain: common.GAIAChain, PubKey: pubKey, Height: height, TxID: tx.Tx.ID, ParkedAt: f.ctx.BlockHeight()}
	c.Assert(f.mgr.Keeper().SetSolvencyGatedInbound(f.ctx, in), IsNil)
	return in
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

// A vault that can never be confirmed (here an inactive vault with more parked
// inbounds than one pass reads) does not hold back another vault's inbounds.
func (s *SolvencyGateSuite) TestUnconfirmedVaultDoesNotBlockOtherVaults(c *C) {
	f := newSolvencyGateFixture(c, true)
	k := f.mgr.Keeper()
	// The stuck vault's keys sort before the healthy vault's.
	var stuck common.PubKey
	for stuck.IsEmpty() || stuck.String() >= f.vault.PubKey.String() {
		stuck = GetRandomPubKey()
	}
	stuckVault := NewVault(1, InactiveVault, AsgardVault, stuck, []string{common.GAIAChain.String()}, nil)
	c.Assert(k.SetVault(f.ctx, stuckVault), IsNil)
	for i := 0; i < solvencyGateMaxVaultsPerChain+5; i++ {
		f.parkWithVoter(c, stuck, int64(i+1))
	}
	before := f.poolAsset(c)
	f.observe(c, "DONATE:GAIA.ATOM", 500)

	f.nextBlock()
	f.reportSolvency(c, 100*common.One, 500)
	f.endBlock(c)
	c.Assert(f.parked(c), HasLen, solvencyGateMaxVaultsPerChain+5)
	c.Assert(f.poolAsset(c).Equal(before.Add(cosmos.NewUint(100*common.One))), Equals, true)
	for _, in := range f.parked(c) {
		c.Assert(in.PubKey.Equals(stuck), Equals, true)
	}
}

// A reverted inbound is dropped even while its vault is unconfirmed, so it does not
// sit ahead of the vault's other inbounds.
func (s *SolvencyGateSuite) TestRevertedInboundDroppedWithoutConfirmation(c *C) {
	f := newSolvencyGateFixture(c, true)
	k := f.mgr.Keeper()
	in := f.parkWithVoter(c, f.vault.PubKey, 10)
	voter, err := k.GetObservedTxInVoter(f.ctx, in.TxID)
	c.Assert(err, IsNil)
	voter.SetReverted()
	k.SetObservedTxInVoter(f.ctx, voter)
	f.parkWithVoter(c, f.vault.PubKey, 20)

	f.nextBlock()
	f.endBlock(c)
	parked := f.parked(c)
	c.Assert(parked, HasLen, 1)
	c.Assert(parked[0].Height, Equals, int64(20))
	var dropped bool
	for _, e := range f.ctx.EventManager().Events() {
		if e.Type == eventSolvencyGatedInboundDropped {
			dropped = true
		}
	}
	c.Assert(dropped, Equals, true)
}

// An inactive vault with parked inbounds is listed for Bifrost, so its solvency is
// reported and the inbounds can be released.
func (s *SolvencyGateSuite) TestInactiveVaultWithParkedInboundIsListed(c *C) {
	f := newSolvencyGateFixture(c, true)
	k := f.mgr.Keeper()
	inactive := NewVault(1, InactiveVault, AsgardVault, GetRandomPubKey(), []string{common.GAIAChain.String()}, nil)
	inactive.AddFunds(common.NewCoins(common.NewCoin(common.ATOMAsset, cosmos.NewUint(common.One))))
	c.Assert(k.SetVault(f.ctx, inactive), IsNil)
	c.Assert(k.RemoveFromAsgardIndex(f.ctx, inactive.PubKey), IsNil)

	query := NewQueryServerImpl(f.mgr, nil, cosmos.KeybaseStore{})
	listed := func() bool {
		resp, err := query.AsgardVaults(f.ctx, &types.QueryAsgardVaultsRequest{})
		c.Assert(err, IsNil)
		for _, v := range resp.AsgardVaults {
			if v.PubKey == inactive.PubKey.String() {
				return true
			}
		}
		return false
	}
	c.Assert(listed(), Equals, false)
	in := f.parkWithVoter(c, inactive.PubKey, 10)
	c.Assert(listed(), Equals, true)
	k.DeleteSolvencyGatedInbound(f.ctx, in)
	c.Assert(listed(), Equals, false)
}

// The gate is confirmed only if the wallet covers the full ledger. Pending
// outbounds the wallet still holds do not count towards it, as they do for the
// halt check.
func (s *SolvencyGateSuite) TestPendingOutboundSurplusDoesNotConfirm(c *C) {
	f := newSolvencyGateFixture(c, true)
	k := f.mgr.Keeper()
	// 100 ATOM already held, then a spoofed 100 ATOM inbound credited and parked.
	vault, err := k.GetVault(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ATOMAsset, cosmos.NewUint(100*common.One))))
	c.Assert(k.SetVault(f.ctx, vault), IsNil)
	f.observe(c, "not a memo", 50)

	// An unbroadcast 100 ATOM outbound: the wallet still holds the real 100 ATOM,
	// and the halt check deducts the outbound from the 200 ATOM ledger.
	c.Assert(k.SetTxOut(f.ctx, &TxOut{Height: f.ctx.BlockHeight(), TxArray: []TxOutItem{{
		Chain:       common.GAIAChain,
		ToAddress:   GetRandomGAIAAddress(),
		VaultPubKey: f.vault.PubKey,
		Coin:        common.NewCoin(common.ATOMAsset, cosmos.NewUint(100*common.One)),
		Memo:        "OUT:" + GetRandomTxHash().String(),
		InHash:      GetRandomTxHash(),
	}}}), IsNil)

	f.nextBlock()
	f.reportSolvency(c, 100*common.One, 50)
	halt, err := k.GetMimir(f.ctx, "SolvencyHaltGAIAChain")
	c.Assert(err, IsNil)
	c.Assert(halt <= 0, Equals, true)
	confirmed, _, err := k.GetSolvencyConfirmation(f.ctx, common.GAIAChain, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(confirmed, Equals, int64(0))
	f.endBlock(c)
	c.Assert(f.parked(c), HasLen, 1)

	// Within the permitted gap ($500 = 10 ATOM) the wallet covers the ledger.
	vault, err = k.GetVault(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	wallet := common.NewCoins(common.NewCoin(common.ATOMAsset, cosmos.NewUint(195*common.One)))
	c.Assert(solventForSolvencyGate(f.ctx, f.mgr, vault, wallet, common.GAIAChain), Equals, true)
	wallet = common.NewCoins(common.NewCoin(common.ATOMAsset, cosmos.NewUint(185*common.One)))
	c.Assert(solventForSolvencyGate(f.ctx, f.mgr, vault, wallet, common.GAIAChain), Equals, false)
}

// Errata of a parked inbound debits the vault and unparks it, but leaves the pool
// alone: the inbound never added to it.
func (s *SolvencyGateSuite) TestErrataOfParkedInboundSkipsPoolUnwind(c *C) {
	f := newSolvencyGateFixture(c, true)
	before := f.poolAsset(c)
	tx := f.observe(c, "=:THOR.RUNE:"+GetRandomTHORAddress().String(), 50)
	c.Assert(f.parked(c), HasLen, 1)

	outcome, err := processErrataTx(f.ctx, f.mgr, &common.ErrataTx{Id: tx.Tx.ID, Chain: common.GAIAChain})
	c.Assert(err, IsNil)
	c.Assert(outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
	c.Assert(f.parked(c), HasLen, 0)
	c.Assert(f.poolAsset(c).Equal(before), Equals, true)
	c.Assert(f.vaultATOM(c).IsZero(), Equals, true)
}

// An inbound observed under the vault's EdDSA or FROST key, as on SOL and XMR, is
// parked under the primary pubkey that solvency confirms, so a solvent report
// releases it, and errata still finds it.
func (s *SolvencyGateSuite) TestInboundObservedUnderAlternateKeyParksUnderPrimary(c *C) {
	f := newSolvencyGateFixture(c, true)
	f.vault.PubKeyEddsa = GetRandomPubKey()
	c.Assert(f.mgr.Keeper().SetVault(f.ctx, f.vault), IsNil)
	before := f.poolAsset(c)
	f.observeAs(c, "DONATE:GAIA.ATOM", 50, f.vault.PubKeyEddsa)
	parked := f.parked(c)
	c.Assert(parked, HasLen, 1)
	c.Assert(parked[0].PubKey.Equals(f.vault.PubKey), Equals, true)
	c.Assert(hasSolvencyGatedInbounds(f.ctx, f.mgr.Keeper(), f.vault), Equals, true)

	f.nextBlock()
	f.reportSolvency(c, 100*common.One, 60)
	f.endBlock(c)
	c.Assert(f.parked(c), HasLen, 0)
	c.Assert(f.poolAsset(c).Equal(before.Add(cosmos.NewUint(100*common.One))), Equals, true)

	tx := f.observeAs(c, "DONATE:GAIA.ATOM", 70, f.vault.PubKeyEddsa)
	c.Assert(f.parked(c), HasLen, 1)
	outcome, err := processErrataTx(f.ctx, f.mgr, &common.ErrataTx{Id: tx.Tx.ID, Chain: common.GAIAChain})
	c.Assert(err, IsNil)
	c.Assert(outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
	c.Assert(f.parked(c), HasLen, 0)
}

// Parked principal is a liability, not network surplus.
func (s *SolvencyGateSuite) TestNetworkSolvencyExcludesParkedPrincipal(c *C) {
	f := newSolvencyGateFixture(c, true)
	before := f.networkSolvencyATOM(c)
	f.observe(c, "DONATE:GAIA.ATOM", 50)
	c.Assert(f.vaultATOM(c).Equal(cosmos.NewUint(100*common.One)), Equals, true)
	c.Assert(f.networkSolvencyATOM(c), Equals, before)
}

// A failed or panicking release commits nothing.
func (s *SolvencyGateSuite) TestRunSolvencyGatedReleaseCommitsOnlyOnSuccess(c *C) {
	f := newSolvencyGateFixture(c, true)
	k := f.mgr.Keeper()
	key := "SolvencyGateTest"
	set := func(fail, panics bool) error {
		return runSolvencyGatedRelease(f.ctx, func(ctx cosmos.Context) error {
			k.SetMimir(ctx, key, 1)
			if panics {
				panic("boom")
			}
			if fail {
				return fmt.Errorf("fail")
			}
			return nil
		})
	}
	value := func() int64 {
		v, err := k.GetMimir(f.ctx, key)
		c.Assert(err, IsNil)
		return v
	}
	err := set(false, true)
	c.Assert(err, ErrorMatches, "panic: boom")
	c.Assert(value(), Equals, int64(-1))
	c.Assert(set(true, false), NotNil)
	c.Assert(value(), Equals, int64(-1))
	c.Assert(set(false, false), IsNil)
	c.Assert(value(), Equals, int64(1))
}

// An inbound whose execution fails is refunded instead of dropped.
func (s *SolvencyGateSuite) TestFailedReleaseFallsBackToRefund(c *C) {
	f := newSolvencyGateFixture(c, true)
	tx := f.observe(c, "DONATE:GAIA.ATOM", 50)
	parked := f.parked(c)
	c.Assert(parked, HasLen, 1)
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	// The parked record names a vault that does not exist, so execution fails.
	in := parked[0]
	in.PubKey = GetRandomPubKey()
	releaseSolvencyGatedInbound(f.ctx, f.mgr, NewInternalHandler(f.mgr), in, voter)
	items := f.outbounds(c)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].InHash.Equals(tx.Tx.ID), Equals, true)
}

// A parked inbound's reference memo resolves as of its finalisation, and the use it
// recorded when parked does not count against it at release.
func (s *SolvencyGateSuite) TestParkedReferenceResolvesAsOfFinalisation(c *C) {
	f := newSolvencyGateFixture(c, true)
	k := f.mgr.Keeper()
	k.SetMimir(f.ctx, constants.MemolessTxnTTL.String(), 100)
	k.SetMimir(f.ctx, constants.MemolessTxnMaxUse.String(), 1)
	registered := "=:THOR.RUNE:" + GetRandomTHORAddress().String()
	k.SetReferenceMemo(f.ctx, NewReferenceMemo(common.ATOMAsset, registered, "40000", 10))
	tx := common.Tx{ID: GetRandomTxHash(), Chain: common.GAIAChain, Memo: "r:40000"}

	// Finalised at 100, released at 200: expired by the release height only.
	f.ctx = f.ctx.WithBlockHeight(200)
	c.Assert(lookupMemoFromReference(f.ctx, f.mgr, common.ATOMAsset, tx, 20, 100).memo, Equals, registered)
	c.Assert(lookupMemoFromReference(f.ctx, f.mgr, common.ATOMAsset, tx, 20, 200).memo, Equals, "")

	// Its own recorded use does not exhaust the reference for it, only for others.
	trackReferenceMemoUsage(f.ctx, f.mgr, common.ATOMAsset, "r:40000", tx.ID)
	c.Assert(lookupMemoFromReference(f.ctx, f.mgr, common.ATOMAsset, tx, 20, 100).memo, Equals, registered)
	other := tx
	other.ID = GetRandomTxHash()
	c.Assert(lookupMemoFromReference(f.ctx, f.mgr, common.ATOMAsset, other, 20, 100).memo, Equals, "")
}

// Parking an inbound with a valid reference memo records its use of the reference.
func (s *SolvencyGateSuite) TestParkingReservesReference(c *C) {
	f := newSolvencyGateFixture(c, true)
	k := f.mgr.Keeper()
	k.SetMimir(f.ctx, constants.MemolessTxnTTL.String(), 100)
	k.SetReferenceMemo(f.ctx, NewReferenceMemo(common.ATOMAsset, "DONATE:GAIA.ATOM", "40000", 10))
	tx := f.observe(c, "r:40000", 50)
	c.Assert(f.parked(c), HasLen, 1)
	ref, err := k.GetReferenceMemo(f.ctx, common.ATOMAsset, "40000")
	c.Assert(err, IsNil)
	c.Assert(ref.HasBeenUsedBy(tx.Tx.ID), Equals, true)
}

// networkSolvencyATOM returns the network's ATOM solvency: vault holdings less
// every liability.
func (f *solvencyGateFixture) networkSolvencyATOM(c *C) int64 {
	network := newNetworkMgr(f.mgr.Keeper(), f.mgr.TxOutStore(), f.mgr.EventMgr())
	amounts, err := network.calculateNetworkSolvency(f.ctx, f.mgr)
	c.Assert(err, IsNil)
	for _, a := range amounts {
		if a.Asset.Equals(common.ATOMAsset) {
			return a.Amount.Int64()
		}
	}
	return 0
}

// observeMigrate brings both sides of a scheduled migrate outbound, paying its
// full MaxGas, to consensus, and returns the gas paid.
func (f *solvencyGateFixture) observeMigrate(c *C, item TxOutItem, to Vault) cosmos.Uint {
	from, err := item.VaultPubKey.GetAddress(common.GAIAChain)
	c.Assert(err, IsNil)
	tx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.GAIAChain,
		FromAddress: from,
		ToAddress:   item.ToAddress,
		Coins:       common.Coins{item.Coin},
		Gas:         item.MaxGas,
		Memo:        item.Memo,
	}
	height := f.ctx.BlockHeight()
	out := common.NewObservedTx(tx, height, item.VaultPubKey, height)
	in := common.NewObservedTx(tx, height, to.PubKey, height)
	for _, na := range f.nodes {
		_, err = NewObservedTxOutHandler(f.mgr).handle(f.ctx, *NewMsgObservedTxOut(common.ObservedTxs{out}, na.NodeAddress))
		c.Assert(err, IsNil)
		_, err = NewObservedTxInHandler(f.mgr).handle(f.ctx, *NewMsgObservedTxIn(common.ObservedTxs{in}, na.NodeAddress))
		c.Assert(err, IsNil)
	}
	return item.MaxGas.ToCoins().GetCoin(common.ATOMAsset).Amount
}

// Churn migrates a vault's coins, parked principal included, into an active vault,
// while the inbound stays parked on the drained vault. The principal is still owed,
// so it is not network surplus that the over-solvency sweep could take.
func (s *SolvencyGateSuite) TestNetworkSolvencyKeepsParkedPrincipalAfterMigration(c *C) {
	f := newSolvencyGateFixture(c, true)
	k := f.mgr.Keeper()
	network := newNetworkMgr(k, f.mgr.TxOutStore(), f.mgr.EventMgr())
	k.SetMimir(f.ctx, constants.FundMigrationInterval.String(), 1)

	before := f.networkSolvencyATOM(c)
	f.observe(c, "DONATE:GAIA.ATOM", 50)
	c.Assert(f.parked(c), HasLen, 1)
	c.Assert(f.networkSolvencyATOM(c), Equals, before)

	// Churn: the parking vault retires and a new active vault takes over.
	next := NewVault(f.ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), []string{common.GAIAChain.String()}, nil)
	c.Assert(k.SetVault(f.ctx, next), IsNil)
	retiring, err := k.GetVault(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	retiring.UpdateStatus(RetiringVault, f.ctx.BlockHeight())
	c.Assert(k.SetVault(f.ctx, retiring), IsNil)

	gasPaid := cosmos.ZeroUint()
	for round := 0; round < 10; round++ {
		f.nextBlock()
		c.Assert(network.migrateFunds(f.ctx, f.mgr), IsNil)
		retiring, err = k.GetVault(f.ctx, f.vault.PubKey)
		c.Assert(err, IsNil)
		if retiring.Status == InactiveVault {
			break
		}
		for _, item := range f.outbounds(c) {
			if item.VaultPubKey.Equals(f.vault.PubKey) && item.OutHash.IsEmpty() {
				gasPaid = gasPaid.Add(f.observeMigrate(c, item, next))
			}
		}
	}
	c.Assert(retiring.Status, Equals, InactiveVault)
	c.Assert(retiring.HasFunds(), Equals, false)
	next, err = k.GetVault(f.ctx, next.PubKey)
	c.Assert(err, IsNil)
	c.Assert(next.GetCoin(common.ATOMAsset).Amount.Add(gasPaid).Equal(cosmos.NewUint(100*common.One)), Equals, true)
	parked := f.parked(c)
	c.Assert(parked, HasLen, 1)
	c.Assert(parked[0].PubKey.Equals(f.vault.PubKey), Equals, true)

	// The migrated principal is still owed. Only the gas the migration spent, not
	// yet reimbursed from the pool, leaves the network short.
	c.Assert(f.networkSolvencyATOM(c), Equals, before-int64(gasPaid.Uint64()))
}

// The part of a parked inbound's principal that its inactive vault still holds is
// not counted as vault holdings, so it is not subtracted as a liability either.
func (s *SolvencyGateSuite) TestNetworkSolvencyNetsParkedPrincipalHeldByInactiveVault(c *C) {
	f := newSolvencyGateFixture(c, true)
	k := f.mgr.Keeper()
	before := f.networkSolvencyATOM(c)

	// Two 1 ATOM inbounds parked on an inactive vault that still holds 1.5 ATOM.
	inactive := NewVault(1, InactiveVault, AsgardVault, GetRandomPubKey(), []string{common.GAIAChain.String()}, nil)
	inactive.AddFunds(common.NewCoins(common.NewCoin(common.ATOMAsset, cosmos.NewUint(common.One*3/2))))
	c.Assert(k.SetVault(f.ctx, inactive), IsNil)
	for _, in := range []types.SolvencyGatedInbound{f.parkWithVoter(c, inactive.PubKey, 10), f.parkWithVoter(c, inactive.PubKey, 11)} {
		voter, err := k.GetObservedTxInVoter(f.ctx, in.TxID)
		c.Assert(err, IsNil)
		voter.UpdatedVault = true
		k.SetObservedTxInVoter(f.ctx, voter)
	}

	// The 0.5 ATOM it no longer holds sits in an active vault, which counts it.
	vault, err := k.GetVault(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ATOMAsset, cosmos.NewUint(common.One/2))))
	c.Assert(k.SetVault(f.ctx, vault), IsNil)
	c.Assert(f.networkSolvencyATOM(c), Equals, before)
}
