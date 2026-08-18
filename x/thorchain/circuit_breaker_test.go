package thorchain

import (
	"math"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type CircuitBreakerRunSuite struct{}

var _ = Suite(&CircuitBreakerRunSuite{})

func circuitBreakerEventAttributes(ctx cosmos.Context, invariant string) map[string]string {
	for _, event := range ctx.EventManager().Events() {
		if event.Type != types.CircuitBreakerEventType {
			continue
		}
		attributes := make(map[string]string)
		for _, attribute := range event.Attributes {
			attributes[attribute.Key] = attribute.Value
		}
		if attributes["invariant"] == invariant {
			return attributes
		}
	}
	return nil
}

func (s *CircuitBreakerRunSuite) TestCalculateInsolvencyBps(c *C) {
	// no divergence
	c.Assert(common.CalculateInsolvencyBps(cosmos.NewUint(100), cosmos.NewUint(100)).IsZero(), Equals, true)

	// expected is zero — binary conditions are signalled via Broken instead
	c.Assert(common.CalculateInsolvencyBps(cosmos.NewUint(0), cosmos.ZeroUint()).IsZero(), Equals, true)
	c.Assert(common.CalculateInsolvencyBps(cosmos.NewUint(100), cosmos.ZeroUint()).IsZero(), Equals, true)

	// 10% insolvent = 1000 bps
	c.Assert(common.CalculateInsolvencyBps(cosmos.NewUint(90), cosmos.NewUint(100)).Equal(cosmos.NewUint(1000)), Equals, true)

	// 50% insolvent = 5000 bps
	c.Assert(common.CalculateInsolvencyBps(cosmos.NewUint(50), cosmos.NewUint(100)).Equal(cosmos.NewUint(5000)), Equals, true)

	// 100% insolvent = 10000 bps
	c.Assert(common.CalculateInsolvencyBps(cosmos.ZeroUint(), cosmos.NewUint(100)).Equal(cosmos.NewUint(10000)), Equals, true)

	// 1% insolvent = 100 bps
	c.Assert(common.CalculateInsolvencyBps(cosmos.NewUint(99), cosmos.NewUint(100)).Equal(cosmos.NewUint(100)), Equals, true)

	// Solvent and overfunded balances are not circuit-breaker findings.
	c.Assert(common.CalculateInsolvencyBps(cosmos.NewUint(110), cosmos.NewUint(100)).IsZero(), Equals, true)
	c.Assert(common.CalculateInsolvencyBps(cosmos.NewUint(200), cosmos.NewUint(100)).IsZero(), Equals, true)

	// Extreme overfunding must also be ignored without conversion or overflow.
	huge := cosmos.NewUintFromString("1208925819614629174706176") // 2^80
	bps := common.CalculateInsolvencyBps(huge, cosmos.NewUint(1))
	c.Assert(bps.IsZero(), Equals, true)

	// The intermediate shortfall * 10000 exceeds 256 bits, but the final
	// result is bounded to 10000 BPS and must not panic.
	maxUint := cosmos.NewUintFromString("115792089237316195423570985008687907853269984665640564039457584007913129639935")
	c.Assert(common.CalculateInsolvencyBps(cosmos.ZeroUint(), maxUint).Equal(cosmos.NewUint(10000)), Equals, true)

	// Fractional shortfalls round up so they cannot floor below a zero
	// tolerance: a 0.5 BPS shortage registers as 1 BPS.
	c.Assert(common.CalculateInsolvencyBps(cosmos.NewUint(19999), cosmos.NewUint(20000)).Equal(cosmos.NewUint(1)), Equals, true)

	// A one-unit shortage in a billion (0.00001 BPS) still registers.
	c.Assert(common.CalculateInsolvencyBps(cosmos.NewUint(999999999), cosmos.NewUint(1000000000)).Equal(cosmos.NewUint(1)), Equals, true)

	// Fractional excess above a whole-BPS threshold rounds up rather than
	// being forgiven: 5.33... BPS reads as 6.
	c.Assert(common.CalculateInsolvencyBps(cosmos.NewUint(29984), cosmos.NewUint(30000)).Equal(cosmos.NewUint(6)), Equals, true)
}

func (s *CircuitBreakerRunSuite) TestSaturatingAddUint(c *C) {
	c.Assert(common.SaturatingAddUint(cosmos.NewUint(2), cosmos.NewUint(3)).Equal(cosmos.NewUint(5)), Equals, true)
	c.Assert(common.SaturatingAddUint(cosmos.ZeroUint(), cosmos.ZeroUint()).IsZero(), Equals, true)

	// at and beyond the 256-bit ceiling the sum saturates instead of
	// panicking, so corrupt magnitudes cannot halt block production
	maxUint := cosmos.NewUintFromString("115792089237316195423570985008687907853269984665640564039457584007913129639935")
	c.Assert(common.SaturatingAddUint(maxUint, cosmos.ZeroUint()).Equal(maxUint), Equals, true)
	c.Assert(common.SaturatingAddUint(maxUint, cosmos.NewUint(1)).Equal(maxUint), Equals, true)
	c.Assert(common.SaturatingAddUint(maxUint, maxUint).Equal(maxUint), Equals, true)
}

func (s *CircuitBreakerRunSuite) TestSaturateInt64(c *C) {
	c.Assert(saturateInt64(cosmos.NewUint(100)), Equals, int64(100))
	c.Assert(saturateInt64(cosmos.NewUint(uint64(math.MaxInt64))), Equals, int64(math.MaxInt64))
	// one above MaxInt64 would wrap negative on a raw conversion
	c.Assert(saturateInt64(cosmos.NewUint(uint64(math.MaxInt64)).AddUint64(1)), Equals, int64(math.MaxInt64))
	// beyond uint64 would panic in Uint64()
	c.Assert(saturateInt64(cosmos.NewUintFromString("36893488147419103232")), Equals, int64(math.MaxInt64)) // 2^65
}

func (s *CircuitBreakerRunSuite) TestCalculateCircuitBreakerPauseUntil(c *C) {
	c.Assert(calculateCircuitBreakerPauseUntil(100, 1), Equals, int64(100)+constants.CircuitBreakerPauseBlocks)
	c.Assert(calculateCircuitBreakerPauseUntil(100, 1000), Equals, int64(1100))
	c.Assert(calculateCircuitBreakerPauseUntil(math.MaxInt64-10, 1000), Equals, int64(math.MaxInt64))
}

func (s *CircuitBreakerRunSuite) TestCircuitBreakerAction(c *C) {
	clean := common.CircuitBreakerCheck{}
	broken := common.CircuitBreakerCheck{Broken: true}
	indeterminate := common.CircuitBreakerCheck{Indeterminate: true}
	both := common.CircuitBreakerCheck{Broken: true, Indeterminate: true}

	// within tolerance, nothing wrong: no finding
	trigger, halt := circuitBreakerAction(clean, cosmos.NewUint(5), 5, 1)
	c.Check(trigger, Equals, false)
	c.Check(halt, Equals, false)

	// beyond tolerance: finding, halts when armed
	trigger, halt = circuitBreakerAction(clean, cosmos.NewUint(6), 5, 1)
	c.Check(trigger, Equals, true)
	c.Check(halt, Equals, true)
	trigger, halt = circuitBreakerAction(clean, cosmos.NewUint(6), 5, 0)
	c.Check(trigger, Equals, true)
	c.Check(halt, Equals, false)

	// a proven violation bypasses tolerance and halts when armed
	trigger, halt = circuitBreakerAction(broken, cosmos.ZeroUint(), 9999, 1)
	c.Check(trigger, Equals, true)
	c.Check(halt, Equals, true)
	trigger, halt = circuitBreakerAction(broken, cosmos.ZeroUint(), 9999, 0)
	c.Check(trigger, Equals, true)
	c.Check(halt, Equals, false)

	// indeterminate state always reports but never halts, even when armed —
	// the breaker must not be stricter than the system it protects
	trigger, halt = circuitBreakerAction(indeterminate, cosmos.ZeroUint(), 0, 1)
	c.Check(trigger, Equals, true)
	c.Check(halt, Equals, false)

	// indeterminate does not mask a coexisting proven violation
	trigger, halt = circuitBreakerAction(both, cosmos.ZeroUint(), 0, 1)
	c.Check(trigger, Equals, true)
	c.Check(halt, Equals, true)

	// indeterminate does not mask coexisting beyond-tolerance insolvency
	trigger, halt = circuitBreakerAction(indeterminate, cosmos.NewUint(6), 5, 1)
	c.Check(trigger, Equals, true)
	c.Check(halt, Equals, true)
}

// indeterminateRouteKeeper overrides the registered routes with one that
// reports indeterminate state, so the runner's halt decision can be
// exercised end to end.
type indeterminateRouteKeeper struct {
	keeper.Keeper
}

func (k indeterminateRouteKeeper) CircuitBreakerRoutes() []common.CircuitBreakerRoute {
	return []common.CircuitBreakerRoute{
		common.NewCircuitBreakerRoute("AsgardSolvency", func(ctx cosmos.Context) common.CircuitBreakerCheck {
			return common.CircuitBreakerCheck{
				Actual:        cosmos.ZeroUint(),
				Expected:      cosmos.ZeroUint(),
				Indeterminate: true,
				Msgs:          []string{"unreadable state"},
			}
		}),
	}
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersIndeterminateNeverHalts(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// an armed route reporting indeterminate state must alert without
	// halting — the breaker is not stricter than the system it protects
	mgr.K = indeterminateRouteKeeper{Keeper: mgr.K}
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	mgr.Keeper().SetMimir(ctx, "InvAsgardSolvencyAutoHalt", 1)

	runCircuitBreakers(ctx, mgr)

	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, int64(-1))
	attributes := circuitBreakerEventAttributes(ctx, "AsgardSolvency")
	c.Assert(attributes, NotNil)
	c.Assert(attributes["indeterminate"], Equals, "true")
	c.Assert(attributes["broken"], Equals, "false")
	c.Assert(attributes["halted"], Equals, "false")
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersMalformedQueueRecordPurge(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// plant an undecodable standard-queue record
	c.Assert(mgr.storeService.OpenKVStore(ctx).Set([]byte("swapitem/CORRUPT-0"), []byte("garbage")), IsNil)

	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	mgr.Keeper().SetMimir(ctx, "InvAsgardSolvencyAutoHalt", 1)

	// mirror the EndBlock order: the swap queue deletes the record before
	// the breaker runs, and the purge must still be surfaced on-chain
	c.Assert(mgr.SwapQ().EndBlock(ctx, mgr), IsNil)
	runCircuitBreakers(ctx, mgr)

	// alert-only: event fires with the purge message, no halt even armed
	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, int64(-1))
	attributes := circuitBreakerEventAttributes(ctx, "AsgardSolvency")
	c.Assert(attributes, NotNil)
	c.Assert(attributes["indeterminate"], Equals, "true")
	c.Assert(attributes["broken"], Equals, "false")
	c.Assert(attributes["halted"], Equals, "false")
	c.Assert(attributes["message_0"], Matches, "1 malformed swap queue records deleted.*")

	// the record is gone and the signal consumed: the next check is clean
	ctx = ctx.WithBlockHeight(101).WithEventManager(cosmos.NewEventManager())
	runCircuitBreakers(ctx, mgr)
	c.Assert(circuitBreakerEventAttributes(ctx, "AsgardSolvency"), IsNil)
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// By default InvCheckInterval is not set (-1), so circuit breakers should be disabled
	runCircuitBreakers(ctx, mgr)

	// Should not panic and should not set NodePauseChainGlobal
	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, int64(-1))
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersEnabled(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Enable circuit breakers with interval of 1 (every block)
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)

	// Run circuit breakers - should not panic even with default state
	runCircuitBreakers(ctx, mgr)

	// Without auto-halt enabled, chain should not be paused
	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, int64(-1))
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersAutoHalt(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Create a pool that expects rune in asgard
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Drain asgard of rune to create insolvency
	asgardBal := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	if !asgardBal.IsZero() {
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), asgardBal))
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)
	}

	// Enable circuit breakers
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	// Enable auto-halt for AsgardSolvency
	mgr.Keeper().SetMimir(ctx, "InvAsgardSolvencyAutoHalt", 1)

	// Run circuit breakers
	runCircuitBreakers(ctx, mgr)

	// Chain should be paused
	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause > ctx.BlockHeight(), Equals, true)
	c.Assert(pause, Equals, ctx.BlockHeight()+constants.CircuitBreakerPauseBlocks)

	// The node-pause key must remain untouched by the breaker
	nodePause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyNodePauseChainGlobal)
	c.Assert(err, IsNil)
	c.Assert(nodePause, Equals, int64(-1))
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersClearsStalePauseVotes(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	key := constants.MimirKeyCircuitBreakerPauseGlobal

	// Model a prior recovery: two of three active nodes still have stored
	// votes to clear the dedicated pause key.
	active := make([]cosmos.AccAddress, 0, 3)
	for i := 0; i < 3; i++ {
		node := GetRandomValidatorNode(NodeActive)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
		active = append(active, node.NodeAddress)
	}
	c.Assert(mgr.Keeper().SetNodeMimir(ctx, key, 0, active[0]), IsNil)
	c.Assert(mgr.Keeper().SetNodeMimir(ctx, key, 0, active[1]), IsNil)
	staleVotes, err := mgr.Keeper().GetNodeMimirs(ctx, key)
	c.Assert(err, IsNil)
	_, hasSuperMajority := staleVotes.HasSuperMajority(key, active)
	c.Assert(hasSuperMajority, Equals, true)

	// Trigger a new automated halt with structurally invalid vault state.
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10)))
	vault.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1)))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	mgr.Keeper().SetMimir(ctx, "InvVaultDeficitAutoHalt", 1)
	runCircuitBreakers(ctx, mgr)

	pause, err := mgr.Keeper().GetMimir(ctx, key)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, ctx.BlockHeight()+constants.CircuitBreakerPauseBlocks)

	// A new incident starts with no inherited recovery consensus. One fresh
	// vote is insufficient to clear the pause.
	votes, err := mgr.Keeper().GetNodeMimirs(ctx, key)
	c.Assert(err, IsNil)
	c.Assert(votes.Mimirs, HasLen, 0)
	c.Assert(mgr.Keeper().SetNodeMimir(ctx, key, 0, active[0]), IsNil)
	votes, err = mgr.Keeper().GetNodeMimirs(ctx, key)
	c.Assert(err, IsNil)
	_, hasSuperMajority = votes.HasSuperMajority(key, active)
	c.Assert(hasSuperMajority, Equals, false)
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersAutoHaltBond(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Create a node with bond but no rune in bond module
	node := GetRandomValidatorNode(NodeActive)
	node.Bond = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)

	// Enable circuit breakers with auto-halt for BondSolvency
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	mgr.Keeper().SetMimir(ctx, "InvBondSolvencyAutoHalt", 1)

	runCircuitBreakers(ctx, mgr)

	// Chain should be paused
	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, ctx.BlockHeight()+constants.CircuitBreakerPauseBlocks)
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersNormalVaultDeficitDoesNotTrigger(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100)))
	missed := vault.SubFundsWithDeficit(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(250))))
	c.Assert(missed, HasLen, 1)
	c.Assert(missed[0].Amount.Equal(cosmos.NewUint(150)), Equals, true)
	c.Assert(vault.ValidateDeficitInvariant(), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	mgr.Keeper().SetMimir(ctx, "InvVaultDeficitAutoHalt", 1)
	runCircuitBreakers(ctx, mgr)

	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, int64(-1))
	c.Assert(circuitBreakerEventAttributes(ctx, "VaultDeficit"), IsNil)
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersBrokenVaultDeficitAutoHalts(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10)))
	vault.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1)))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	mgr.Keeper().SetMimir(ctx, "InvVaultDeficitAutoHalt", 1)
	runCircuitBreakers(ctx, mgr)

	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, ctx.BlockHeight()+constants.CircuitBreakerPauseBlocks)
	attributes := circuitBreakerEventAttributes(ctx, "VaultDeficit")
	c.Assert(attributes, NotNil)
	c.Assert(attributes["halted"], Equals, "true")
	c.Assert(attributes["broken"], Equals, "true")
	c.Assert(attributes["indeterminate"], Equals, "false")
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersWithTolerance(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Create a pool that expects rune in asgard
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Drain asgard and send back most but not all
	asgardBal := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	if !asgardBal.IsZero() {
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), asgardBal))
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)
	}

	// Add back 99% of expected
	restock := common.NewCoin(common.RuneAsset(), cosmos.NewUint(99*common.One))
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, restock), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(restock)), IsNil)

	// Enable circuit breakers with auto-halt
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	mgr.Keeper().SetMimir(ctx, "InvAsgardSolvencyAutoHalt", 1)

	// Set tolerance to 200 bps (2%) - 1% insolvency should be within tolerance
	mgr.Keeper().SetMimir(ctx, "InvAsgardSolvencyTolerance", 200)

	// Run circuit breakers
	runCircuitBreakers(ctx, mgr)

	// Chain should NOT be paused because insolvency is within tolerance
	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, int64(-1))
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersToleranceBoundary(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Create a pool that expects rune in asgard
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Drain asgard
	asgardBal := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	if !asgardBal.IsZero() {
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), asgardBal))
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)
	}

	// Add back 99% of expected (1% = 100 bps insolvency)
	restock := common.NewCoin(common.RuneAsset(), cosmos.NewUint(99*common.One))
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, restock), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(restock)), IsNil)

	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	mgr.Keeper().SetMimir(ctx, "InvAsgardSolvencyAutoHalt", 1)

	// Set tolerance to exactly 100 bps — deviation equals tolerance, should NOT halt
	mgr.Keeper().SetMimir(ctx, "InvAsgardSolvencyTolerance", 100)
	runCircuitBreakers(ctx, mgr)

	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, int64(-1))

	// Set tolerance to 99 bps — deviation exceeds tolerance, should halt
	mgr.Keeper().SetMimir(ctx, "InvAsgardSolvencyTolerance", 99)
	runCircuitBreakers(ctx, mgr)

	pause, err = mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, ctx.BlockHeight()+constants.CircuitBreakerPauseBlocks)
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersSubBpsShortfallTriggers(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// pool expects 20000 rune in asgard
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(20000)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// asgard holds 19999 — a real 0.5 BPS shortage that must not floor
	// below the zero tolerance
	asgardBal := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	if !asgardBal.IsZero() {
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), asgardBal))
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)
	}
	restock := common.NewCoin(common.RuneAsset(), cosmos.NewUint(19999))
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, restock), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(restock)), IsNil)

	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	mgr.Keeper().SetMimir(ctx, "InvAsgardSolvencyAutoHalt", 1)

	runCircuitBreakers(ctx, mgr)

	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, ctx.BlockHeight()+constants.CircuitBreakerPauseBlocks)
	attributes := circuitBreakerEventAttributes(ctx, "AsgardSolvency")
	c.Assert(attributes, NotNil)
	c.Assert(attributes["deviation_bps"], Equals, "1")
	c.Assert(attributes["halted"], Equals, "true")
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersIntervalSkip(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(101)

	// Create insolvency
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	asgardBal := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	if !asgardBal.IsZero() {
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), asgardBal))
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)
	}

	// Enable circuit breakers with interval of 10
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 10)
	mgr.Keeper().SetMimir(ctx, "InvAsgardSolvencyAutoHalt", 1)

	// Run at block 101 - should skip since 101 % 10 != 0
	runCircuitBreakers(ctx, mgr)

	// Chain should NOT be paused
	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, int64(-1))

	// Run at block 110 - should trigger since 110 % 10 == 0
	ctx = ctx.WithBlockHeight(110)
	runCircuitBreakers(ctx, mgr)

	pause, err = mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, 110+constants.CircuitBreakerPauseBlocks)
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersPauseCoversLongInterval(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	asgardBal := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	if !asgardBal.IsZero() {
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), asgardBal))
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)
	}

	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	mgr.Keeper().SetMimir(ctx, "InvAsgardSolvencyInterval", 1000)
	mgr.Keeper().SetMimir(ctx, "InvAsgardSolvencyAutoHalt", 1)

	runCircuitBreakers(ctx, mgr)

	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	// The pause reaches the next route check, leaving no protection gap.
	c.Assert(pause, Equals, int64(2000))
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersPauseExtension(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Create insolvency
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	asgardBal := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	if !asgardBal.IsZero() {
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), asgardBal))
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)
	}

	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	mgr.Keeper().SetMimir(ctx, "InvAsgardSolvencyAutoHalt", 1)

	// First trigger at block 100
	runCircuitBreakers(ctx, mgr)
	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, 100+constants.CircuitBreakerPauseBlocks)

	// Second trigger at block 200 — should extend the pause
	ctx = ctx.WithBlockHeight(200)
	runCircuitBreakers(ctx, mgr)
	pause, err = mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, 200+constants.CircuitBreakerPauseBlocks)
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersOverSolvencyIgnored(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Bond module holds 10% more than bonds + pending rewards
	node := GetRandomValidatorNode(NodeActive)
	node.Bond = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)

	bondBal := mgr.Keeper().GetRuneBalanceOfModule(ctx, BondName)
	if !bondBal.IsZero() {
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), bondBal))
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, BondName, ReserveName, coins), IsNil)
	}
	restock := common.NewCoin(common.RuneAsset(), cosmos.NewUint(1100*common.One))
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, restock), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, BondName, common.NewCoins(restock)), IsNil)

	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	mgr.Keeper().SetMimir(ctx, "InvBondSolvencyAutoHalt", 1)

	// Overfunding is not insolvency and must not halt, even with zero tolerance.
	mgr.Keeper().SetMimir(ctx, "InvBondSolvencyTolerance", 0)
	runCircuitBreakers(ctx, mgr)

	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, int64(-1))
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersExtremeInflationIgnored(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Bond expected is tiny while the module balance is astronomically
	// inflated. It is still overfunding, not insolvency, and must be ignored.
	node := GetRandomValidatorNode(NodeActive)
	node.Bond = cosmos.NewUint(1)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)

	bondBal := mgr.Keeper().GetRuneBalanceOfModule(ctx, BondName)
	if !bondBal.IsZero() {
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), bondBal))
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, BondName, ReserveName, coins), IsNil)
	}
	huge := common.NewCoin(common.RuneAsset(), cosmos.NewUintFromString("1208925819614629174706176")) // 2^80
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, huge), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, BondName, common.NewCoins(huge)), IsNil)

	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	mgr.Keeper().SetMimir(ctx, "InvBondSolvencyAutoHalt", 1)
	mgr.Keeper().SetMimir(ctx, "InvBondSolvencyTolerance", 10000)

	runCircuitBreakers(ctx, mgr)

	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, int64(-1))
}

func (s *CircuitBreakerRunSuite) TestRunCircuitBreakersBrokenBypassesTolerance(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Simultaneous same-asset vault balance and deficit is a binary finding.
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10)))
	vault.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1)))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	mgr.Keeper().SetMimir(ctx, "InvVaultDeficitAutoHalt", 1)
	// Tolerance cannot mask broken findings
	mgr.Keeper().SetMimir(ctx, "InvVaultDeficitTolerance", 9999)

	runCircuitBreakers(ctx, mgr)

	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, ctx.BlockHeight()+constants.CircuitBreakerPauseBlocks)
}

func (s *CircuitBreakerRunSuite) TestCircuitBreakerHaltSurvivesNodeResume(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	node := GetRandomValidatorNode(NodeActive)
	node2 := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node2), IsNil)

	// A node pauses via the node-pause handler, and the breaker also halts
	handler := NewNodePauseChainHandler(mgr)
	c.Assert(handler.handle(ctx, *NewMsgNodePauseChain(1, node.NodeAddress)), IsNil)

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10)))
	vault.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1)))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyInvCheckInterval, 1)
	mgr.Keeper().SetMimir(ctx, "InvVaultDeficitAutoHalt", 1)
	runCircuitBreakers(ctx, mgr)
	c.Assert(mgr.Keeper().IsChainHalted(ctx, common.BTCChain), Equals, true)

	// Another node resumes: NodePauseChainGlobal drops to the current
	// height, but the circuit breaker pause must be unaffected
	c.Assert(handler.handle(ctx, *NewMsgNodePauseChain(-1, node2.NodeAddress)), IsNil)

	nodePause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyNodePauseChainGlobal)
	c.Assert(err, IsNil)
	c.Assert(nodePause <= ctx.BlockHeight(), Equals, true)

	pause, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal)
	c.Assert(err, IsNil)
	c.Assert(pause, Equals, ctx.BlockHeight()+constants.CircuitBreakerPauseBlocks)

	// From the next block the node-pause key no longer halts anything;
	// only the breaker key keeps the chain halted
	ctx = ctx.WithBlockHeight(101)
	c.Assert(mgr.Keeper().IsChainHalted(ctx, common.BTCChain), Equals, true)
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyCircuitBreakerPauseGlobal, 0)
	c.Assert(mgr.Keeper().IsChainHalted(ctx, common.BTCChain), Equals, false)
}
