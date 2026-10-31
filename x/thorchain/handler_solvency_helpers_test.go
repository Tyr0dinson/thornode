package thorchain

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

func TestSolvencyHeightTelemetryNormalizesChainLabels(t *testing.T) {
	ctx, mgr := setupBenchmarkManager(t)
	ctx = ctx.WithBlockHeight(1024)
	vault := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{common.TRONChain.String()}, nil)
	if err := mgr.Keeper().SetVault(ctx, vault); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Keeper().SetLastChainHeight(ctx, common.TRONChain, 2000); err != nil {
		t.Fatal(err)
	}
	active := NodeAccounts{
		GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive),
	}
	sink := installRecordingSink(t)
	for _, chain := range []common.Chain{common.TRONChain, "tron", "TrOn"} {
		voter := NewSolvencyVoter(GetRandomTxHash(), chain, vault.PubKey, nil, 1900)
		solvency := &common.Solvency{
			Id: voter.Id, Chain: chain, PubKey: vault.PubKey, Height: voter.Height,
		}
		for _, node := range active[:2] {
			if err := processSolvencyAttestation(ctx, mgr, &voter, node.NodeAddress, active, solvency, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := sink.gauges["thornode.solvency.last_chain_height_delta{chain=TRON}"]; got != 100 {
		t.Fatalf("expected normalized height delta of 100, got %v", got)
	}
	if got := sink.gaugesWithPrefix("thornode.solvency.last_chain_height_delta"); got != 1 {
		t.Fatalf("chain casing must share one height-delta series, got %d", got)
	}
	if got := sink.counters["thornode.solvency.height_check_total{chain=TRON,result=stale}"]; got != 3 {
		t.Fatalf("expected three stale checks in one normalized series, got %v", got)
	}
	if got := countersWithPrefix(sink, "thornode.solvency.height_check_total"); got != 1 {
		t.Fatalf("chain casing must share one height-check series, got %d", got)
	}
	if got := sink.counters["thornode.solvency.report_stale{chain=TRON}"]; got != 3 {
		t.Fatalf("existing stale-report counter must still record all three reports, got %v", got)
	}
}

type HandlerSolvencyHelpersSuite struct{}

var _ = Suite(&HandlerSolvencyHelpersSuite{})

func (s *HandlerSolvencyHelpersSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

// setupSolvencyHelpersTest creates common test fixtures for solvency helper tests.
func setupSolvencyHelpersTest(c *C) (cosmos.Context, Manager, NodeAccounts, Vault) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1024)

	// 4 active nodes, consensus needs 2 (1/3 of 4 = 1.33, rounded up = 2)
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}
	for _, na := range nas {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}

	vault := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{
		common.ETHChain.String(),
	}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Set up ETH pool: 1000 ETH = 10000 RUNE (1 ETH = 10 RUNE)
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceRune = cosmos.NewUint(10000 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	// Set RUNE price ($5/RUNE)
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", 5_00000000)

	return ctx, mgr, nas, vault
}

// bringVoterToConsensus signs a solvency voter with enough nodes to reach consensus.
func bringVoterToConsensus(voter *keeper.SolvencyVoter, nas NodeAccounts) {
	voter.Sign(nas[0].NodeAddress)
	voter.Sign(nas[1].NodeAddress)
}

func setupTRONSolvencyHelpersTest(c *C) (cosmos.Context, Manager, NodeAccounts, Vault, common.Coins) {
	ctx, mgr, nas, _ := setupSolvencyHelpersTest(c)

	usdt, err := common.NewAsset("TRON.USDT-TR7NHQJEKQXGTCI8Q8ZY4PL8OTSZGJLJ6T")
	c.Assert(err, IsNil)
	coins := common.NewCoins(
		common.NewCoin(common.TRXAsset, cosmos.NewUint(1_000_000_000)),
		common.NewCoin(usdt, cosmos.NewUint(10_000_000_000)),
	)
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), []string{
		common.TRONChain.String(),
	}, nil)
	vault.AddFunds(coins)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.TRONChain, ctx.BlockHeight()), IsNil)

	return ctx, mgr, nas, vault, coins
}

func assertSlashPoints(c *C, ctx cosmos.Context, mgr Manager, nas NodeAccounts, expected int64) {
	for _, na := range nas {
		points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		c.Assert(points, Equals, expected)
	}
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_DuplicateSigner(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	// First sign succeeds
	err := processSolvencyAttestation(ctx, mgr, &voter, nas[0].NodeAddress, nas, sol, true)
	c.Assert(err, IsNil)

	// Duplicate sign with shouldSlashForDuplicate=true should slash but not error
	err = processSolvencyAttestation(ctx, mgr, &voter, nas[0].NodeAddress, nas, sol, true)
	c.Assert(err, IsNil)
	pts, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nas[0].NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(pts > 1, Equals, true) // extra slash for duplicate
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_TRONFragmentationDoesNotSlash(c *C) {
	ctx, mgr, nas, vault, coins := setupTRONSolvencyHelpersTest(c)
	const initialSlashPoints = int64(100)
	for _, na := range nas {
		mgr.Keeper().SetNodeAccountSlashPoints(ctx, na.NodeAddress, initialSlashPoints)
	}

	// A different current-state TRC-20 balance produces a competing voter for
	// the same canonical height. Its signer must not retain a pre-consensus
	// observation penalty.
	fragmentedCoins := coins.Copy()
	fragmentedCoins[1].Amount = fragmentedCoins[1].Amount.Sub(cosmos.OneUint())
	fragmentedVoter := NewSolvencyVoter(
		GetRandomTxHash(), common.TRONChain, vault.PubKey, fragmentedCoins, ctx.BlockHeight(),
	)
	fragmented := &common.Solvency{
		Id: fragmentedVoter.Id, Chain: fragmentedVoter.Chain, PubKey: fragmentedVoter.PubKey,
		Coins: fragmentedVoter.Coins, Height: fragmentedVoter.Height,
	}
	c.Assert(processSolvencyAttestation(
		ctx, mgr, &fragmentedVoter, nas[0].NodeAddress, nas, fragmented, false,
	), IsNil)
	assertSlashPoints(c, ctx, mgr, nas, initialSlashPoints)

	// A second payload reaches the 1/3 solvency threshold. Neither its signers
	// nor the fragmented signer and other non-signers receive slash points.
	winnerVoter := NewSolvencyVoter(
		GetRandomTxHash(), common.TRONChain, vault.PubKey, coins, ctx.BlockHeight(),
	)
	winner := &common.Solvency{
		Id: winnerVoter.Id, Chain: winnerVoter.Chain, PubKey: winnerVoter.PubKey,
		Coins: winnerVoter.Coins, Height: winnerVoter.Height,
	}
	c.Assert(processSolvencyAttestation(
		ctx, mgr, &winnerVoter, nas[1].NodeAddress, nas, winner, false,
	), IsNil)
	c.Assert(processSolvencyAttestation(
		ctx, mgr, &winnerVoter, nas[2].NodeAddress, nas, winner, false,
	), IsNil)
	c.Assert(winnerVoter.ConsensusBlockHeight, Equals, ctx.BlockHeight())
	assertSlashPoints(c, ctx, mgr, nas, initialSlashPoints)

	flex := mgr.Keeper().GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
	entries, err := mgr.Keeper().PopObservationTallies(ctx, ctx.BlockHeight()+flex)
	c.Assert(err, IsNil)
	c.Assert(entries, HasLen, 0)
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_TRONStaleConsensusDoesNotSlashOrAct(c *C) {
	ctx, mgr, nas, vault, coins := setupTRONSolvencyHelpersTest(c)
	const initialSlashPoints = int64(100)
	for _, na := range nas {
		mgr.Keeper().SetNodeAccountSlashPoints(ctx, na.NodeAddress, initialSlashPoints)
	}

	// Make the report both insolvent and stale. The consensus is recorded, but
	// it must neither change slash accounting nor halt TRON.
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.TRONChain, ctx.BlockHeight()+1), IsNil)
	reportedCoins := coins.Copy()
	reportedCoins[1].Amount = reportedCoins[1].Amount.QuoUint64(2)
	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.TRONChain, vault.PubKey, reportedCoins, ctx.BlockHeight(),
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	c.Assert(processSolvencyAttestation(ctx, mgr, &voter, nas[0].NodeAddress, nas, sol, false), IsNil)
	c.Assert(processSolvencyAttestation(ctx, mgr, &voter, nas[1].NodeAddress, nas, sol, false), IsNil)
	c.Assert(voter.ConsensusBlockHeight, Equals, ctx.BlockHeight())
	assertSlashPoints(c, ctx, mgr, nas, initialSlashPoints)

	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltTRONChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(-1))
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_TRONLateSignerDoesNotReceivePhantomRefund(c *C) {
	ctx, mgr, nas, vault, coins := setupTRONSolvencyHelpersTest(c)
	const initialSlashPoints = int64(100)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, nas[2].NodeAddress, initialSlashPoints)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.TRONChain, vault.PubKey, coins, ctx.BlockHeight(),
	)
	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = ctx.BlockHeight()
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	c.Assert(processSolvencyAttestation(
		ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false,
	), IsNil)
	points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nas[2].NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(points, Equals, initialSlashPoints)
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_TRONDuplicateStillSlashes(c *C) {
	ctx, mgr, nas, vault, coins := setupTRONSolvencyHelpersTest(c)
	const initialSlashPoints = int64(100)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, nas[0].NodeAddress, initialSlashPoints)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.TRONChain, vault.PubKey, coins, ctx.BlockHeight(),
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	c.Assert(processSolvencyAttestation(ctx, mgr, &voter, nas[0].NodeAddress, nas, sol, true), IsNil)
	c.Assert(processSolvencyAttestation(ctx, mgr, &voter, nas[0].NodeAddress, nas, sol, true), IsNil)
	points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nas[0].NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(points, Equals, initialSlashPoints+1)
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_StopSolvencyCheckMimir(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Set StopSolvencyCheck to a height before current block (active stop)
	mgr.Keeper().SetMimir(ctx, "StopSolvencyCheck", ctx.BlockHeight()-1)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	// Bring to consensus
	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Even though vault is insolvent, StopSolvencyCheck prevents halting
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(-1))

	flex := mgr.Keeper().GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
	entries, err := mgr.Keeper().PopObservationTallies(ctx, ctx.BlockHeight()+flex)
	c.Assert(err, IsNil)
	c.Assert(entries, HasLen, 1)
	c.Assert(entries[0].Kind, Equals, observationTallyKindSolvency)
	c.Assert(entries[0].Chain, Equals, common.ETHChain)
	c.Assert(entries[0].Ref, DeepEquals, []string{voter.Id.String()})
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_StopSolvencyCheckChainMimir(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Set chain-specific StopSolvencyCheck
	mgr.Keeper().SetMimir(ctx, "StopSolvencyCheckETH", ctx.BlockHeight()-1)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(-1))
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_HaltChainAlreadySet(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Set SolvencyHaltETHChain to current block height (already halted)
	mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", ctx.BlockHeight())

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Halt value should not have changed
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, ctx.BlockHeight())
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_HaltChainManuallyHalted(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Set SolvencyHaltETHChain to 1 (indefinite manual halt)
	mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", 1)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Should stay at 1 (manually halted)
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(1))
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_SolvencyHeightBelowLastChainHeight(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Set last chain height higher than solvency msg height
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.ETHChain, 2000), IsNil)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024, // lower than lastChainHeight=2000
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Should not halt because solvency height is stale
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(-1))
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_GetVaultError(c *C) {
	ctx, mgr, nas, _ := setupSolvencyHelpersTest(c)

	// Use a pubkey that has no vault stored
	unknownPK := GetRandomPubKey()
	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, unknownPK,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*fail to get vault.*")
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_AutoUnhalt(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Chain was previously halted at block 500 (earlier than current 1024)
	mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", 500)

	// Vault has 1000 ETH, wallet also reports 1000 ETH → solvent
	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Chain should be auto-unhalted (mimir set to 0)
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(0))
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_FlagsVaultInsolvent(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Vault holds 1000 ETH but wallet only reports 100 ETH → insolvent.
	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Chain halted and the specific vault flagged insolvent for its chain.
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, ctx.BlockHeight())

	flag, err := mgr.Keeper().GetMimir(ctx, solvencyHaltVaultKey(vault.PubKey, common.ETHChain))
	c.Assert(err, IsNil)
	c.Assert(flag, Equals, ctx.BlockHeight())

	insolvent, err := isVaultInsolvent(ctx, mgr.Keeper(), vault)
	c.Assert(err, IsNil)
	c.Assert(insolvent, Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_ClearsVaultInsolventFlag(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Chain previously halted and vault previously flagged insolvent.
	mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", 500)
	flagVaultInsolvent(ctx.WithBlockHeight(500), mgr.Keeper(), vault.PubKey, common.ETHChain)

	// Vault has 1000 ETH and wallet also reports 1000 ETH → solvent again.
	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Both the chain halt and the vault flag are cleared.
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(0))

	insolvent, err := isVaultInsolvent(ctx, mgr.Keeper(), vault)
	c.Assert(err, IsNil)
	c.Assert(insolvent, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_ClearsVaultInsolventFlagWithoutUnhaltingChain(c *C) {
	testCases := []struct {
		name             string
		halt             int64
		haltCurrentBlock bool
	}{
		{name: "halted this block", haltCurrentBlock: true},
		{name: "manual halt", halt: 1},
	}

	for _, tc := range testCases {
		ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

		expectedHalt := tc.halt
		if tc.haltCurrentBlock {
			expectedHalt = ctx.BlockHeight()
		}
		mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", expectedHalt)
		flagVaultInsolvent(ctx.WithBlockHeight(500), mgr.Keeper(), vault.PubKey, common.ETHChain)

		// Vault has 1000 ETH and wallet also reports 1000 ETH -> solvent again.
		voter := NewSolvencyVoter(
			GetRandomTxHash(), common.ETHChain, vault.PubKey,
			common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))),
			1024,
		)
		sol := &common.Solvency{
			Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
		}

		bringVoterToConsensus(&voter, nas)
		voter.ConsensusBlockHeight = 0

		err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
		c.Assert(err, IsNil, Commentf(tc.name))

		// Chain-wide halt state is preserved, but the recovered vault's exclusion
		// flag is cleared so unrelated healthy chains can elect it again.
		halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
		c.Assert(err, IsNil, Commentf(tc.name))
		c.Assert(halt, Equals, expectedHalt, Commentf(tc.name))

		flag, err := mgr.Keeper().GetMimir(ctx, solvencyHaltVaultKey(vault.PubKey, common.ETHChain))
		c.Assert(err, IsNil, Commentf(tc.name))
		c.Assert(flag, Equals, int64(0), Commentf(tc.name))

		insolvent, err := isVaultInsolvent(ctx, mgr.Keeper(), vault)
		c.Assert(err, IsNil, Commentf(tc.name))
		c.Assert(insolvent, Equals, false, Commentf(tc.name))
	}
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_DoesNotClearVaultInsolventFlagForStaleHeight(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", ctx.BlockHeight())
	flagVaultInsolvent(ctx.WithBlockHeight(500), mgr.Keeper(), vault.PubKey, common.ETHChain)
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.ETHChain, 2000), IsNil)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	flag, err := mgr.Keeper().GetMimir(ctx, solvencyHaltVaultKey(vault.PubKey, common.ETHChain))
	c.Assert(err, IsNil)
	c.Assert(flag, Equals, int64(500))

	insolvent, err := isVaultInsolvent(ctx, mgr.Keeper(), vault)
	c.Assert(err, IsNil)
	c.Assert(insolvent, Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_EmptyCoin(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	// Add an empty coin to vault
	vault.Coins = append(vault.Coins, common.NewCoin(common.BTCAsset, cosmos.ZeroUint()))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	walletCoins := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One)))

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_WalletCoinEmpty(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	// Wallet has no ETH at all → gap = vault amount
	walletCoins := common.Coins{}

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_PendingOutboundCoversWalletGap(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	prevHeight := ctx.BlockHeight() - 1
	txOut := NewTxOut(prevHeight)
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.ETHChain,
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(20*common.One)),
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	// Vault has 1000 ETH, wallet has 980 ETH, and the remaining 20 ETH is
	// already committed to an outstanding outbound.
	walletCoins := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(980*common.One)))

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_PendingOutboundCanExceedWalletBalance(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	prevHeight := ctx.BlockHeight() - 1
	txOut := NewTxOut(prevHeight)
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.ETHChain,
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One)),
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	// The wallet can be empty without being insolvent if the full vault amount
	// is already represented by pending outbounds.
	walletCoins := common.Coins{}

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_PendingOutboundExceedsVaultBalance(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	prevHeight := ctx.BlockHeight() - 1
	txOut := NewTxOut(prevHeight)
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.ETHChain,
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(1500*common.One)),
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	// Over-scheduled pending outbounds are handled by outbound recovery and
	// should not make solvency fall back to checking the full vault balance.
	walletCoins := common.Coins{}

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_GapWithinPermittedRange(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	// Vault has 1000 ETH, wallet has 999.99 ETH → small gap
	walletCoins := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(999_99000000)))

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_NoPoolLiquidity(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	// Replace ETH pool with empty pool
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceAsset = cosmos.ZeroUint()
	ethPool.BalanceRune = cosmos.ZeroUint()
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	// Wallet has less ETH than vault
	walletCoins := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(500*common.One)))

	// Pool has no liquidity → gap can't be valued in RUNE → treat as insolvent (conservative)
	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_WalletMoreThanVault(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	// Wallet has more than vault → no gap
	walletCoins := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(2000*common.One)))

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_DifferentChainCoinSkipped(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	// Check BTC chain insolvency - vault has no BTC coins
	walletCoins := common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)))

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.BTCChain)
	c.Assert(result, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestDeductVaultBlockPendingOutbound_NoMatchingVault(c *C) {
	ctx, mgr, _, _ := setupSolvencyHelpersTest(c)

	vault := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{
		common.ETHChain.String(),
	}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))

	block := &TxOut{
		Height: 1024,
		TxArray: []TxOutItem{
			{
				Chain:       common.ETHChain,
				VaultPubKey: GetRandomPubKey(), // different vault
				Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
			},
		},
	}

	signingPeriod := mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	result, _ := deductVaultBlockPendingOutbound(ctx, mgr, vault, block, make(map[string]cosmos.Uint), common.ETHChain, signingPeriod)
	// Balance should be unchanged since VaultPubKey doesn't match
	ethCoin := result.Coins.GetCoin(common.ETHAsset)
	c.Assert(ethCoin.Amount.Equal(cosmos.NewUint(1000*common.One)), Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestDeductVaultBlockPendingOutbound_AlreadySent(c *C) {
	ctx, mgr, _, _ := setupSolvencyHelpersTest(c)

	vault := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{
		common.ETHChain.String(),
	}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))

	block := &TxOut{
		Height: 1024,
		TxArray: []TxOutItem{
			{
				Chain:       common.ETHChain,
				VaultPubKey: vault.PubKey,
				Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
				OutHash:     GetRandomTxHash(), // already sent
			},
		},
	}

	signingPeriod := mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	result, _ := deductVaultBlockPendingOutbound(ctx, mgr, vault, block, make(map[string]cosmos.Uint), common.ETHChain, signingPeriod)
	// Balance should be unchanged since tx already has OutHash
	ethCoin := result.Coins.GetCoin(common.ETHAsset)
	c.Assert(ethCoin.Amount.Equal(cosmos.NewUint(1000*common.One)), Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestDeductVaultBlockPendingOutbound_WithGas(c *C) {
	ctx, mgr, _, _ := setupSolvencyHelpersTest(c)

	vault := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{
		common.ETHChain.String(),
	}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))

	block := &TxOut{
		Height: 1024,
		TxArray: []TxOutItem{
			{
				Chain:       common.ETHChain,
				VaultPubKey: vault.PubKey,
				Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
				MaxGas: common.Gas{
					common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One)),
				},
			},
		},
	}

	signingPeriod := mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	result, _ := deductVaultBlockPendingOutbound(ctx, mgr, vault, block, make(map[string]cosmos.Uint), common.ETHChain, signingPeriod)
	// Should deduct both the coin amount and gas
	ethCoin := result.Coins.GetCoin(common.ETHAsset)
	expected := cosmos.NewUint(899 * common.One) // 1000 - 100 - 1
	c.Assert(ethCoin.Amount.Equal(expected), Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestExcludePendingOutboundFromVault_Success(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	// Set a pending outbound at a height within the signing period (before current block)
	prevHeight := ctx.BlockHeight() - 1
	txOut := NewTxOut(prevHeight)
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.ETHChain,
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)),
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	adjusted, _, err := excludePendingOutboundFromVault(ctx, mgr, vault, common.ETHChain)
	c.Assert(err, IsNil)
	ethCoin := adjusted.Coins.GetCoin(common.ETHAsset)
	expected := cosmos.NewUint(950 * common.One) // 1000 - 50
	c.Assert(ethCoin.Amount.Equal(expected), Equals, true)

	// Original vault coins should not be mutated
	originalETH := vault.Coins.GetCoin(common.ETHAsset)
	c.Assert(originalETH.Amount.Equal(cosmos.NewUint(1000*common.One)), Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestExcludePendingOutboundFromVaultScopesToReportedChain(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1000*common.One))))

	prevHeight := ctx.BlockHeight() - 1
	txOut := NewTxOut(prevHeight)
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.BTCChain,
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One)),
		},
		{
			Chain:       common.ETHChain,
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(75*common.One)),
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	adjusted, pending, err := excludePendingOutboundFromVault(ctx, mgr, vault, common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(adjusted.Coins.GetCoin(common.BTCAsset).Amount.Equal(cosmos.NewUint(950*common.One)), Equals, true)
	c.Assert(adjusted.Coins.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(1000*common.One)), Equals, true)
	c.Assert(pending[common.BTCAsset.String()].Equal(cosmos.NewUint(50*common.One)), Equals, true)
	_, hasETHPending := pending[common.ETHAsset.String()]
	c.Assert(hasETHPending, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestExcludePendingOutboundFromVaultRetainsExtendedEVMClasses(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)
	defaultSigningPeriod := mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	overrideSigningPeriod := defaultSigningPeriod * 2
	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), overrideSigningPeriod)
	signingPeriod := mgr.Keeper().GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	c.Assert(signingPeriod, Equals, overrideSigningPeriod)
	c.Assert(signingPeriod > 0, Equals, true)
	ctx = ctx.WithBlockHeight(signingPeriod*4 + 10)
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	vault.AddFunds(common.NewCoins(common.NewCoin(usdc, cosmos.NewUint(1000*common.One))))

	addTxOut := func(height int64, items ...TxOutItem) {
		txOut := NewTxOut(height)
		txOut.TxArray = items
		c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)
	}
	justPastStandard := ctx.BlockHeight() - signingPeriod - 1
	justInsideExtended := ctx.BlockHeight() - signingPeriod*3 + 1
	addTxOut(justPastStandard,
		TxOutItem{Chain: common.ETHChain, VaultPubKey: vault.PubKey, Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)), Aggregator: "0xaggregator"},
		TxOutItem{Chain: common.ETHChain, VaultPubKey: vault.PubKey, Coin: common.NewCoin(usdc, cosmos.NewUint(70*common.One))},
		TxOutItem{Chain: common.ETHChain, VaultPubKey: vault.PubKey, Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One))},
	)
	addTxOut(justInsideExtended,
		TxOutItem{Chain: common.ETHChain, VaultPubKey: vault.PubKey, Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)), Aggregator: "0xaggregator"},
		TxOutItem{Chain: common.ETHChain, VaultPubKey: vault.PubKey, Coin: common.NewCoin(usdc, cosmos.NewUint(80*common.One))},
	)

	adjusted, pending, err := excludePendingOutboundFromVault(ctx, mgr, vault, common.ETHChain)
	c.Assert(err, IsNil)
	c.Assert(adjusted.Coins.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(900*common.One)), Equals, true)
	c.Assert(adjusted.Coins.GetCoin(usdc).Amount.Equal(cosmos.NewUint(850*common.One)), Equals, true)
	c.Assert(pending[common.ETHAsset.String()].Equal(cosmos.NewUint(100*common.One)), Equals, true)
	c.Assert(pending[usdc.String()].Equal(cosmos.NewUint(150*common.One)), Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_InsolventButAlreadyHalted(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Chain was previously halted at block 500 (haltChain > 0, but < blockHeight)
	mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", 500)

	// Vault has 1000 ETH, wallet only has 100 ETH → insolvent
	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// The halt keeps the height it began at: insolvencyCheck dates its cutoff from
	// it, and other vaults' flags, not a refreshed height, keep it from lifting.
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(500))
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_InsolventAndUnhalted(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// No halt set (default -1)

	// Vault has 1000 ETH, wallet only has 100 ETH → insolvent
	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Should halt the chain
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, ctx.BlockHeight())
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_ConsensusAlreadyReached(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	// Mark consensus already reached
	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 100

	// Late observer within flex period should get slash points decremented
	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Late observer beyond flex period
	ctx = ctx.WithBlockHeight(voter.ConsensusBlockHeight + 100)
	err = processSolvencyAttestation(ctx, mgr, &voter, nas[3].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)
}

// queuePendingOutbound adds an unsent ETH outbound of amount from the vault to the
// block at height and returns it.
func queuePendingOutbound(c *C, ctx cosmos.Context, mgr Manager, vault Vault, height int64, amount uint64) TxOutItem {
	inHash := GetRandomTxHash()
	item := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: vault.PubKey,
		InHash:      inHash,
		Memo:        "OUT:" + inHash.String(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(amount*common.One)),
	}
	appendTxOutItem(c, ctx, mgr, height, item)
	return item
}

func appendTxOutItem(c *C, ctx cosmos.Context, mgr Manager, height int64, item TxOutItem) {
	txOut, err := mgr.Keeper().GetTxOut(ctx, height)
	c.Assert(err, IsNil)
	txOut.TxArray = append(txOut.TxArray, item)
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)
}

// replaceOutbound marks the unsent item in the block at from as replaced and, when
// to is positive, adds its copy for vault to the block at to, as LackSigning does.
func replaceOutbound(c *C, ctx cosmos.Context, mgr Manager, item TxOutItem, from, to int64, vault common.PubKey) {
	txOut, err := mgr.Keeper().GetTxOut(ctx, from)
	c.Assert(err, IsNil)
	found := false
	for i := range txOut.TxArray {
		if txOut.TxArray[i].InHash.Equals(item.InHash) && txOut.TxArray[i].Coin.Equals(item.Coin) && txOut.TxArray[i].OutHash.IsEmpty() {
			txOut.TxArray[i].OutHash = common.BlankTxID
			found = true
			break
		}
	}
	c.Assert(found, Equals, true)
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)
	if to > 0 {
		item.VaultPubKey = vault
		appendTxOutItem(c, ctx, mgr, to, item)
	}
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_SolvencyHaltNetsOutOnlyEarlierOutbounds(c *C) {
	// The vault books 1000 ETH and its wallet holds 900 ETH. A 150 ETH outbound is
	// queued; whether it may net out the 100 ETH gap depends on whether Bifrost could
	// have sent it before the halt stopped signing.
	testCases := []struct {
		name      string
		halt      int64 // SolvencyHaltETHChain; 0 leaves it unset
		lag       int64 // SolvencyHaltSigningLagBlocks Mimir; 0 keeps the default
		block     int64 // block holding the outbound
		insolvent bool
	}{
		{name: "not halted: nets out", block: 1010},
		{name: "halted at 1000: released after the halt, still in the wallet", halt: 1000, block: 1010, insolvent: true},
		{name: "halted at 1000: released before the halt nets out", halt: 1000, block: 999},
		{name: "halted at 1000: released within the signing lag nets out", halt: 1000, block: 1002},
		{name: "halted at 1000: released once the signing lag ends, still in the wallet", halt: 1000, block: 1003, insolvent: true},
		{name: "halted at 1000, lag set to 20 by Mimir: nets out", halt: 1000, lag: 20, block: 1010},
		{name: "halted at 1022: the signing lag hasn't passed, nets out", halt: 1022, block: 1023},
		{name: "halted manually (1), with no height: nets out", halt: 1, block: 1010},
		{name: "halt scheduled for 2000: nets out", halt: 2000, block: 1010},
	}
	for _, tc := range testCases {
		c.Logf("case: %s", tc.name)
		ctx, mgr, _, vault := setupSolvencyHelpersTest(c)
		if tc.halt != 0 {
			mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", tc.halt)
		}
		if tc.lag != 0 {
			mgr.Keeper().SetMimir(ctx, constants.SolvencyHaltSigningLagBlocks.String(), tc.lag)
		}
		queuePendingOutbound(c, ctx, mgr, vault, tc.block, 150)

		walletCoins := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(900*common.One)))
		c.Check(insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain), Equals, tc.insolvent, Commentf(tc.name))
	}
}

// markCopyObserved sets an observed hash on the copy of item in the block at height,
// as the outbound handler does once the payment is observed.
func markCopyObserved(c *C, ctx cosmos.Context, mgr Manager, item TxOutItem, height int64) {
	txOut, err := mgr.Keeper().GetTxOut(ctx, height)
	c.Assert(err, IsNil)
	for i := range txOut.TxArray {
		if txOut.TxArray[i].InHash.Equals(item.InHash) && txOut.TxArray[i].Coin.Equals(item.Coin) && txOut.TxArray[i].OutHash.IsEmpty() {
			txOut.TxArray[i].OutHash = GetRandomTxHash()
			c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)
			return
		}
	}
	c.Fatalf("no unsent copy at %d", height)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_SolvencyHaltCountsReplacedOutbounds(c *C) {
	// The chain halted at 1000, so the cutoff is 1003. LackSigning replaces an item
	// 300 blocks after its block (the signing period) and adds the copy there, so an
	// item from 710 is replaced at 1010, after the cutoff. Each vault books 1000 ETH
	// unless book says otherwise; payouts are 150 ETH. The check runs at 1024 unless
	// at says otherwise.
	testCases := []struct {
		name       string
		coalesce   int64 // RescheduleCoalesceBlocks Mimir; 0 leaves it unset
		at         int64
		setup      func(c *C, ctx cosmos.Context, mgr Manager, vault, other Vault)
		checkOther bool // check the other vault instead of this one
		book       uint64
		wallet     uint64
		insolvent  bool
	}{
		{
			name: "replaced after the cutoff, copy unsent: the original nets out",
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, _ Vault) {
				item := queuePendingOutbound(c, ctx, mgr, vault, 710, 150)
				replaceOutbound(c, ctx, mgr, item, 710, 1010, vault.PubKey)
			},
			wallet: 850,
		},
		{
			name: "replaced before the cutoff: only the copy nets out, not the original too",
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, _ Vault) {
				item := queuePendingOutbound(c, ctx, mgr, vault, 690, 150)
				replaceOutbound(c, ctx, mgr, item, 690, 990, vault.PubKey)
			},
			wallet:    700,
			insolvent: true,
		},
		{
			name:     "copy coalesced past the cutoff: the original nets out",
			coalesce: 20,
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, _ Vault) {
				// Replaced at 1001, before the cutoff, but the copy landed at 1020.
				item := queuePendingOutbound(c, ctx, mgr, vault, 701, 150)
				replaceOutbound(c, ctx, mgr, item, 701, 1020, vault.PubKey)
			},
			wallet: 850,
		},
		{
			name: "copy observed after the cutoff: already debited, so the original doesn't count again",
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, _ Vault) {
				item := queuePendingOutbound(c, ctx, mgr, vault, 703, 150)
				replaceOutbound(c, ctx, mgr, item, 703, 1003, vault.PubKey)
				markCopyObserved(c, ctx, mgr, item, 1003)
			},
			book:      850,
			wallet:    750,
			insolvent: true,
		},
		{
			name: "replaced without a copy (treasury recovery): doesn't count",
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, _ Vault) {
				item := queuePendingOutbound(c, ctx, mgr, vault, 701, 150)
				replaceOutbound(c, ctx, mgr, item, 701, 0, vault.PubKey)
			},
			wallet:    850,
			insolvent: true,
		},
		{
			name: "copy at another height (a SOL reissue): doesn't count",
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, _ Vault) {
				item := queuePendingOutbound(c, ctx, mgr, vault, 710, 150)
				replaceOutbound(c, ctx, mgr, item, 710, 1015, vault.PubKey)
			},
			wallet:    850,
			insolvent: true,
		},
		{
			name: "replaced migration: doesn't count",
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, _ Vault) {
				item := TxOutItem{
					Chain:       common.ETHChain,
					ToAddress:   GetRandomETHAddress(),
					VaultPubKey: vault.PubKey,
					InHash:      common.BlankTxID,
					Memo:        NewMigrateMemo(500).String(),
					Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(150*common.One)),
				}
				appendTxOutItem(c, ctx, mgr, 710, item)
				replaceOutbound(c, ctx, mgr, item, 710, 1010, vault.PubKey)
			},
			wallet:    850,
			insolvent: true,
		},
		{
			name: "reassigned after the cutoff: nets out for the original vault",
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, other Vault) {
				item := queuePendingOutbound(c, ctx, mgr, vault, 710, 150)
				replaceOutbound(c, ctx, mgr, item, 710, 1010, other.PubKey)
			},
			wallet: 850,
		},
		{
			name: "reassigned after the cutoff: doesn't net out for the new vault",
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, other Vault) {
				item := queuePendingOutbound(c, ctx, mgr, vault, 710, 150)
				replaceOutbound(c, ctx, mgr, item, 710, 1010, other.PubKey)
			},
			checkOther: true,
			wallet:     850,
			insolvent:  true,
		},
		{
			name: "another vault's replaced original doesn't count",
			setup: func(c *C, ctx cosmos.Context, mgr Manager, _, other Vault) {
				item := queuePendingOutbound(c, ctx, mgr, other, 710, 150)
				replaceOutbound(c, ctx, mgr, item, 710, 1010, other.PubKey)
			},
			wallet:    850,
			insolvent: true,
		},
		{
			name: "a sibling released after the cutoff doesn't drop the earlier payout",
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, _ Vault) {
				payout := queuePendingOutbound(c, ctx, mgr, vault, 710, 150)
				replaceOutbound(c, ctx, mgr, payout, 710, 1010, vault.PubKey)
				refund := payout
				refund.ToAddress = GetRandomETHAddress()
				refund.Coin = common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))
				appendTxOutItem(c, ctx, mgr, 1005, refund)
			},
			wallet: 850,
		},
		{
			name: "a sibling released after the cutoff doesn't net out itself",
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, _ Vault) {
				payout := queuePendingOutbound(c, ctx, mgr, vault, 710, 150)
				replaceOutbound(c, ctx, mgr, payout, 710, 1010, vault.PubKey)
				refund := payout
				refund.ToAddress = GetRandomETHAddress()
				refund.Coin = common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))
				appendTxOutItem(c, ctx, mgr, 1005, refund)
			},
			wallet:    750,
			insolvent: true,
		},
		{
			name: "replaced twice, latest copy unsent: the original nets out",
			at:   1320,
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, _ Vault) {
				item := queuePendingOutbound(c, ctx, mgr, vault, 710, 150)
				replaceOutbound(c, ctx, mgr, item, 710, 1010, vault.PubKey)
				replaceOutbound(c, ctx, mgr, item, 1010, 1310, vault.PubKey)
			},
			wallet: 850,
		},
		{
			name: "replaced twice, latest copy observed: doesn't count again",
			at:   1320,
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, _ Vault) {
				item := queuePendingOutbound(c, ctx, mgr, vault, 710, 150)
				replaceOutbound(c, ctx, mgr, item, 710, 1010, vault.PubKey)
				replaceOutbound(c, ctx, mgr, item, 1010, 1310, vault.PubKey)
				markCopyObserved(c, ctx, mgr, item, 1310)
			},
			book:      850,
			wallet:    750,
			insolvent: true,
		},
		{
			name: "two identical originals with two copies: both net out",
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, _ Vault) {
				item := queuePendingOutbound(c, ctx, mgr, vault, 710, 150)
				appendTxOutItem(c, ctx, mgr, 710, item)
				replaceOutbound(c, ctx, mgr, item, 710, 1010, vault.PubKey)
				replaceOutbound(c, ctx, mgr, item, 710, 1010, vault.PubKey)
			},
			wallet: 700,
		},
		{
			name: "two identical originals with one copy: only one nets out",
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, _ Vault) {
				item := queuePendingOutbound(c, ctx, mgr, vault, 710, 150)
				appendTxOutItem(c, ctx, mgr, 710, item)
				replaceOutbound(c, ctx, mgr, item, 710, 1010, vault.PubKey)
				replaceOutbound(c, ctx, mgr, item, 710, 0, vault.PubKey)
			},
			wallet:    700,
			insolvent: true,
		},
		{
			name: "an unsent outbound within its signing period at the cutoff counts after it runs out",
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, _ Vault) {
				// 293 blocks old at the cutoff, 314 at the check.
				queuePendingOutbound(c, ctx, mgr, vault, 710, 150)
			},
			wallet: 850,
		},
		{
			name: "an unsent outbound past its signing period at the cutoff doesn't count",
			setup: func(c *C, ctx cosmos.Context, mgr Manager, vault, _ Vault) {
				queuePendingOutbound(c, ctx, mgr, vault, 650, 150)
			},
			wallet:    850,
			insolvent: true,
		},
	}
	for _, tc := range testCases {
		c.Logf("case: %s", tc.name)
		ctx, mgr, _, vault := setupSolvencyHelpersTest(c)
		if tc.at != 0 {
			ctx = ctx.WithBlockHeight(tc.at)
		}
		other := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{common.ETHChain.String()}, nil)
		other.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))
		c.Assert(mgr.Keeper().SetVault(ctx, other), IsNil)
		mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", 1000)
		if tc.coalesce != 0 {
			mgr.Keeper().SetMimir(ctx, constants.RescheduleCoalesceBlocks.String(), tc.coalesce)
		}
		tc.setup(c, ctx, mgr, vault, other)

		checked := vault
		if tc.checkOther {
			checked = other
		}
		if tc.book != 0 {
			checked.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(tc.book*common.One)))
		}
		walletCoins := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(tc.wallet*common.One)))
		c.Check(insolvencyCheck(ctx, mgr, checked, walletCoins, common.ETHChain), Equals, tc.insolvent, Commentf(tc.name))
	}
}

// reportSolvency brings a solvency report for the vault with the wallet coins to
// consensus at ctx's height.
func reportSolvency(c *C, ctx cosmos.Context, mgr Manager, nas NodeAccounts, pubKey common.PubKey, wallet uint64, height int64) {
	coins := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(wallet*common.One)))
	voter := NewSolvencyVoter(GetRandomTxHash(), common.ETHChain, pubKey, coins, height)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}
	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0
	c.Assert(processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false), IsNil)
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_HaltBacklogDoesNotLiftHalt(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// The vault books 1000 ETH but its wallet holds 900 ETH: the first report halts the chain.
	reportSolvency(c, ctx.WithBlockHeight(1000), mgr, nas, vault.PubKey, 900, 100)
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(1000))

	// While halted, swaps keep scheduling ETH outbounds from the vault that nobody
	// signs. The queue soon exceeds the gap.
	queuePendingOutbound(c, ctx, mgr, vault, 1010, 80)
	queuePendingOutbound(c, ctx, mgr, vault, 1015, 70)

	// The wallet is unchanged at the next report, so the vault is still short. The
	// chain stays halted at the height it began, and the vault's flag now carries
	// this latest insolvent report.
	reportSolvency(c, ctx, mgr, nas, vault.PubKey, 900, 101)
	halt, err = mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(1000))
	flag, err := mgr.Keeper().GetMimir(ctx, solvencyHaltVaultKey(vault.PubKey, common.ETHChain))
	c.Assert(err, IsNil)
	c.Assert(flag, Equals, ctx.BlockHeight())
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_ScheduledHaltDatesTheCutoff(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// A halt is scheduled for 1000. A report at 900 finds the vault 100 ETH short and
	// flags it; the halt isn't active yet, so it stays as scheduled.
	mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", 1000)
	reportSolvency(c, ctx.WithBlockHeight(900), mgr, nas, vault.PubKey, 900, 100)
	flag, err := mgr.Keeper().GetMimir(ctx, solvencyHaltVaultKey(vault.PubKey, common.ETHChain))
	c.Assert(err, IsNil)
	c.Assert(flag, Equals, int64(900))

	// The gap is covered, and a 100 ETH outbound released at 998 is sent before
	// signing stops. The halted scanner never observes it, so the wallet holds 900
	// ETH against the 1000 booked.
	queuePendingOutbound(c, ctx, mgr, vault, 998, 100)

	// The cutoff comes from the halt, not the earlier flag: the outbound nets out,
	// the vault is solvent and the halt lifts.
	reportSolvency(c, ctx, mgr, nas, vault.PubKey, 900, 101)
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(0))
	insolvent, err := isVaultInsolvent(ctx, mgr.Keeper(), vault)
	c.Assert(err, IsNil)
	c.Assert(insolvent, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_FlaggedVaultKeepsHalt(c *C) {
	for _, status := range []VaultStatus{ActiveVault, RetiringVault} {
		c.Logf("flagged vault status: %s", status)
		ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

		// Another vault, still holding ETH, is flagged insolvent from the halt at 500.
		flaggedVault := NewVault(500, status, AsgardVault, GetRandomPubKey(), []string{common.ETHChain.String()}, nil)
		flaggedVault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))
		c.Assert(mgr.Keeper().SetVault(ctx, flaggedVault), IsNil)
		mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", 500)
		flagVaultInsolvent(ctx.WithBlockHeight(500), mgr.Keeper(), flaggedVault.PubKey, common.ETHChain)

		// A solvent report from this vault in a later block must not lift the halt.
		reportSolvency(c, ctx, mgr, nas, vault.PubKey, 1000, 1024)
		halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
		c.Assert(err, IsNil)
		c.Assert(halt, Equals, int64(500))

		// Once the flagged vault reports solvent too, the halt lifts.
		reportSolvency(c, ctx, mgr, nas, flaggedVault.PubKey, 1000, 1024)
		halt, err = mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
		c.Assert(err, IsNil)
		c.Assert(halt, Equals, int64(0))
		insolvent, err := isVaultInsolvent(ctx, mgr.Keeper(), flaggedVault)
		c.Assert(err, IsNil)
		c.Assert(insolvent, Equals, false)
	}
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_StaleFlagsDoNotKeepHalt(c *C) {
	// Bifrost no longer reports either vault, so their flags can't clear.
	testCases := []struct {
		name   string
		status VaultStatus
		funds  uint64
	}{
		{name: "Active vault written down to no ETH", status: ActiveVault},
		{name: "Inactive vault", status: InactiveVault, funds: 1000},
	}
	for _, tc := range testCases {
		c.Logf("case: %s", tc.name)
		ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)
		stale := NewVault(400, tc.status, AsgardVault, GetRandomPubKey(), []string{common.ETHChain.String()}, nil)
		if tc.funds > 0 {
			stale.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(tc.funds*common.One))))
		}
		c.Assert(mgr.Keeper().SetVault(ctx, stale), IsNil)
		mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", 500)
		flagVaultInsolvent(ctx.WithBlockHeight(400), mgr.Keeper(), stale.PubKey, common.ETHChain)

		reportSolvency(c, ctx, mgr, nas, vault.PubKey, 1000, 1024)
		halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
		c.Assert(err, IsNil)
		c.Check(halt, Equals, int64(0), Commentf(tc.name))
	}
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_LackSigningRescheduleStillNetsOut(c *C) {
	testCases := []struct {
		name     string
		coalesce int64
		original int64 // block of the outbound, released before the halt at 1000
	}{
		{name: "copy in the current block", original: 900},
		{name: "copy coalesced into a later block, not yet reached", coalesce: 20, original: 901},
	}
	for _, tc := range testCases {
		c.Logf("case: %s", tc.name)
		ctx, mgr, _, vault := setupSolvencyHelpersTest(c)
		if tc.coalesce != 0 {
			mgr.Keeper().SetMimir(ctx, constants.RescheduleCoalesceBlocks.String(), tc.coalesce)
		}
		signingPeriod := mgr.Keeper().GetConfigInt64(ctx, constants.SigningTransactionPeriod)

		// A 150 ETH outbound was broadcast, but its observation is held up: the
		// chain halted at 1000. The wallet holds 850 ETH against the 1000 booked.
		inHash := GetRandomTxHash()
		item := TxOutItem{
			Chain:       common.ETHChain,
			ToAddress:   GetRandomETHAddress(),
			VaultPubKey: vault.PubKey,
			InHash:      inHash,
			Memo:        "OUT:" + inHash.String(),
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(150*common.One)),
		}
		voter := NewObservedTxVoter(inHash, nil)
		voter.FinalisedHeight = tc.original
		voter.Actions = []TxOutItem{item}
		mgr.Keeper().SetObservedTxInVoter(ctx, voter)
		appendTxOutItem(c, ctx, mgr, tc.original, item)
		mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", 1000)

		// After the signing period LackSigning marks the original replaced and adds
		// the copy, rounded up to the coalescing interval.
		rescheduled := ctx.WithBlockHeight(tc.original + signingPeriod)
		c.Assert(mgr.Slasher().LackSigning(rescheduled, mgr), IsNil)
		copyHeight := rescheduled.BlockHeight()
		if tc.coalesce > 1 && copyHeight%tc.coalesce != 0 {
			copyHeight += tc.coalesce - copyHeight%tc.coalesce
		}
		moved, err := mgr.Keeper().GetTxOut(rescheduled, copyHeight)
		c.Assert(err, IsNil)
		c.Assert(moved.TxArray, HasLen, 1)
		c.Assert(moved.TxArray[0].InHash.Equals(inHash), Equals, true)
		original, err := mgr.Keeper().GetTxOut(rescheduled, tc.original)
		c.Assert(err, IsNil)
		c.Assert(original.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true)

		// The outbound still nets out, so the vault is not reported short.
		check := rescheduled.WithBlockHeight(rescheduled.BlockHeight() + 1)
		walletCoins := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(850*common.One)))
		c.Check(insolvencyCheck(check, mgr, vault, walletCoins, common.ETHChain), Equals, false, Commentf(tc.name))
	}
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_SolvencyHaltCoalescedCopyOnShortWindowChain(c *C) {
	// BTC's signing window is the plain SigningTransactionPeriod, so the original
	// of a copy coalesced past the cutoff sits before the window and is reached only
	// through the coalescing allowance.
	ctx, mgr, _, _ := setupSolvencyHelpersTest(c)
	signingPeriod := mgr.Keeper().GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	c.Assert(thortypes.MaxTxOutSigningTransactionPeriodForChain(common.BTCChain, signingPeriod), Equals, signingPeriod)

	vault := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{common.BTCChain.String()}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1000*common.One))))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(10000 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)
	mgr.Keeper().SetMimir(ctx, "SolvencyHaltBTCChain", 1000)
	mgr.Keeper().SetMimir(ctx, constants.RescheduleCoalesceBlocks.String(), 20)

	// Released at 701 and broadcast; LackSigning ran at 1001, before the cutoff at
	// 1003, and its copy landed at 1020.
	inHash := GetRandomTxHash()
	item := TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   GetRandomBTCAddress(),
		VaultPubKey: vault.PubKey,
		InHash:      inHash,
		Memo:        "OUT:" + inHash.String(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(150*common.One)),
	}
	appendTxOutItem(c, ctx, mgr, 701, item)
	replaceOutbound(c, ctx, mgr, item, 701, 1020, vault.PubKey)

	walletCoins := common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(850*common.One)))
	c.Assert(insolvencyCheck(ctx, mgr, vault, walletCoins, common.BTCChain), Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_SilentFlaggedVaultStopsHoldingHalt(c *C) {
	// Vault B was found short and halted the chain at 1000. Vault A sent a 50 ETH
	// outbound just before the halt that the paused scanner never observed, so its
	// wallet holds 950 ETH against the 1000 booked; Bifrost's own check fails for A,
	// and it reports A alone. THORNode nets that outbound out and finds A solvent.
	// B's gap was then fixed, so Bifrost stopped reporting B.
	testCases := []struct {
		name     string
		at       int64 // height of A's report
		bAgainAt int64 // height of a later insolvent report for B; 0 for none
		noLimit  bool  // set SolvencyHaltHoldBlocks to 0 by Mimir
		lifted   bool
	}{
		{name: "B reported 500 blocks ago: still holds the halt", at: 1500},
		{name: "B reported more than 1200 blocks ago: no longer holds it", at: 2300, lifted: true},
		{name: "B still being reported: holds the halt", at: 2300, bAgainAt: 2000},
		{name: "hold window set to 0 by Mimir: holds without limit", at: 2300, noLimit: true},
	}
	for _, tc := range testCases {
		c.Logf("case: %s", tc.name)
		ctx, mgr, nas, vaultA := setupSolvencyHelpersTest(c)
		vaultB := NewVault(500, ActiveVault, AsgardVault, GetRandomPubKey(), []string{common.ETHChain.String()}, nil)
		vaultB.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))
		c.Assert(mgr.Keeper().SetVault(ctx, vaultB), IsNil)
		if tc.noLimit {
			mgr.Keeper().SetMimir(ctx, constants.SolvencyHaltHoldBlocks.String(), 0)
		}

		reportSolvency(c, ctx.WithBlockHeight(1000), mgr, nas, vaultB.PubKey, 900, 100)
		queuePendingOutbound(c, ctx, mgr, vaultA, 995, 50)
		if tc.bAgainAt != 0 {
			reportSolvency(c, ctx.WithBlockHeight(tc.bAgainAt), mgr, nas, vaultB.PubKey, 900, 200)
		}
		reportSolvency(c, ctx.WithBlockHeight(tc.at), mgr, nas, vaultA.PubKey, 950, 300)

		halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
		c.Assert(err, IsNil)
		if tc.lifted {
			c.Check(halt, Equals, int64(0), Commentf(tc.name))
		} else {
			c.Check(halt, Equals, int64(1000), Commentf(tc.name))
		}
	}
}

func (s *HandlerSolvencyHelpersSuite) TestFlagVaultInsolvent_TracksLatestInsolventReport(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)
	key := solvencyHaltVaultKey(vault.PubKey, common.ETHChain)
	for _, height := range []int64{1000, 1100, 1100} {
		flagVaultInsolvent(ctx.WithBlockHeight(height), mgr.Keeper(), vault.PubKey, common.ETHChain)
	}
	flag, err := mgr.Keeper().GetMimir(ctx, key)
	c.Assert(err, IsNil)
	c.Assert(flag, Equals, int64(1100))
}
