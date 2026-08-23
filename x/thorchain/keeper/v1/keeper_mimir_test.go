package keeperv1

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/constants"
	keeperpkg "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"

	. "gopkg.in/check.v1"
)

type KeeperMimirSuite struct{}

var _ = Suite(&KeeperMimirSuite{})

func (s *KeeperMimirSuite) TestMimir(c *C) {
	ctx, k := setupKeeperForTest(c)

	k.SetMimir(ctx, "foo", 14)

	val, err := k.GetMimir(ctx, "foo")
	c.Assert(err, IsNil)
	c.Assert(val, Equals, int64(14))

	val, err = k.GetMimir(ctx, "bogus")
	c.Assert(err, IsNil)
	c.Check(val, Equals, int64(-1))
}

func (s *KeeperMimirSuite) TestScopedMimirCacheIsSnapshot(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, "foo", 14)
	cachedCtx := keeperpkg.WithMimirCache(ctx)

	value, err := k.GetMimir(cachedCtx, "foo")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(14))

	// The scoped context retains its first read, while an uncached context sees
	// the updated store value.
	k.SetMimir(ctx, "foo", 15)
	value, err = k.GetMimir(cachedCtx, "foo")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(14))
	value, err = k.GetMimir(ctx, "foo")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(15))
}

func (s *KeeperMimirSuite) TestScopedMimirCacheDisabledByWrite(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, "HaltTrading", 0)
	k.SetMimir(ctx, "foo", 7)
	cachedCtx := keeperpkg.WithMimirCache(ctx)

	value, err := k.GetMimir(cachedCtx, "HaltTrading")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(0))

	// A write through a context carrying the cache (as the MaxRuneSupply circuit
	// breaker does mid-EndBlock) must be visible to every later read.
	k.SetMimir(cachedCtx, "HaltTrading", 1)
	value, err = k.GetMimir(cachedCtx, "HaltTrading")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(1))

	// A write inside a discarded cache context must not be snapshotted into the
	// shared cache and leak to the parent context.
	childCtx, _ := cachedCtx.CacheContext()
	k.SetMimir(childCtx, "foo", 8)
	value, err = k.GetMimir(childCtx, "foo")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(8))
	value, err = k.GetMimir(cachedCtx, "foo")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(7))

	// DeleteMimir disables a fresh cache too.
	cachedCtx = keeperpkg.WithMimirCache(ctx)
	value, err = k.GetMimir(cachedCtx, "foo")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(7))
	c.Assert(k.DeleteMimir(cachedCtx, "foo"), IsNil)
	value, err = k.GetMimir(cachedCtx, "foo")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(-1))
}

func (s *KeeperMimirSuite) TestIsOperationalMimirPOLReserveBlackList(c *C) {
	// Per-pool POL reserve blacklist must be operational so operators can
	// quickly disable a misbehaving pool without full governance consensus.
	c.Check(constants.IsOperationalMimir("POLReserveBlacklist-BTC-BTC"), Equals, true)
	c.Check(
		constants.IsOperationalMimir("POLReserveBlacklist-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48"),
		Equals, true,
	)

	// The partial match is case-insensitive (IsOperationalMimir uppercases the
	// key before comparing), so operator-set keys in any case still register.
	c.Check(constants.IsOperationalMimir("POLRESERVEBLACKLIST-BTC-BTC"), Equals, true)
	c.Check(constants.IsOperationalMimir("polreserveblacklist-btc-btc"), Equals, true)

	// Similarly-prefixed but unrelated keys must NOT be flagged operational.
	c.Check(constants.IsOperationalMimir("POLReserveFoo-BTC-BTC"), Equals, false)
	c.Check(constants.IsOperationalMimir("POLReserveDeposit-BTC-BTC"), Equals, false)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirPOLReserveSystemIncomeBps(c *C) {
	key := constants.POLReserveSystemIncomeBps.String()
	c.Check(constants.IsOperationalMimir(key), Equals, true)
	c.Check(constants.IsOperationalMimir(strings.ToUpper(key)), Equals, true)
	c.Check(constants.IsOperationalMimir(strings.ToLower(key)), Equals, true)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirPOLReserveWhitelist(c *C) {
	c.Check(constants.IsOperationalMimir("POLReserveWhitelist-BTC-BTC"), Equals, true)
	c.Check(
		constants.IsOperationalMimir("POLReserveWhitelist-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48"),
		Equals, true,
	)
	c.Check(constants.IsOperationalMimir("POLRESERVEWHITELIST-BTC-BTC"), Equals, true)
	c.Check(constants.IsOperationalMimir("polreservewhitelist-btc-btc"), Equals, true)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirPreflightChainSetDisabled(c *C) {
	// A kill switch for a check that holds nodes out of the Ready set must be
	// reachable at operational threshold if the check misfires during a churn.
	c.Check(constants.IsOperationalMimir(constants.MimirKeyPreflightChainSetDisabled), Equals, true)
	c.Check(constants.IsOperationalMimir(strings.ToUpper(constants.MimirKeyPreflightChainSetDisabled)), Equals, true)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirCompromisedVault(c *C) {
	c.Check(constants.IsOperationalMimir("CompromisedVault-tthorpub1addwnpepq0"), Equals, true)
	c.Check(constants.IsOperationalMimir("COMPROMISEDVAULT-TTHORPUB1ADDWNPEPQ0"), Equals, true)
	c.Check(constants.IsOperationalMimir("compromisedvault-tthorpub1addwnpepq0"), Equals, true)
	c.Check(constants.IsOperationalMimir("CompromisedVault"), Equals, false)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirOverSolvencyToTreasuryBps(c *C) {
	c.Check(constants.IsOperationalMimir("OverSolvencyToTreasuryBps-BTC-BTC"), Equals, true)
	c.Check(
		constants.IsOperationalMimir("OverSolvencyToTreasuryBps-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48"),
		Equals, true,
	)
	c.Check(constants.IsOperationalMimir("OVERSOLVENCYTOTREASURYBPS-ETH-ETH"), Equals, true)
	c.Check(constants.IsOperationalMimir("oversolvencytotreasurybps-btc-btc"), Equals, true)
	c.Check(constants.IsOperationalMimir("OverSolvencyToTreasuryBps"), Equals, true)
	c.Check(constants.IsOperationalMimir("OverSolvencyToTreasuryFoo-BTC-BTC"), Equals, false)
}

func (s *KeeperMimirSuite) TestDisableTSSSignerFilteringIsOperationalMimir(c *C) {
	key := constants.MimirKeyDisableTSSSignerFiltering
	c.Check(constants.IsOperationalMimir(key), Equals, true)
	c.Check(constants.IsOperationalMimir(strings.ToUpper(key)), Equals, true)
	c.Check(constants.IsOperationalMimir(strings.ToLower(key)), Equals, true)
	c.Check(constants.IsOperationalMimir(key+"-BTC"), Equals, false)
}

func (s *KeeperMimirSuite) TestCircuitBreakerControlsAreEconomicMimirs(c *C) {
	// Clearing an active automated halt or disabling a route's auto-halt must
	// require economic supermajority rather than the fixed operational minimum.
	c.Check(constants.IsOperationalMimir(constants.MimirKeyCircuitBreakerPauseGlobal), Equals, false)
	c.Check(constants.IsOperationalMimir(strings.ToLower(constants.MimirKeyCircuitBreakerPauseGlobal)), Equals, false)
	c.Check(constants.IsOperationalMimir("InvAsgardSolvencyAutoHalt"), Equals, false)
	c.Check(constants.IsOperationalMimir("invbondsolvencyautohalt"), Equals, false)

	// The tuning keys are deliberately economic as well: an operational
	// minority must not be able to disarm detection by zeroing an interval
	// or raising a tolerance.
	c.Check(constants.IsOperationalMimir(constants.MimirKeyInvCheckInterval), Equals, false)
	c.Check(constants.IsOperationalMimir("InvAsgardSolvencyInterval"), Equals, false)
	c.Check(constants.IsOperationalMimir("InvBondSolvencyInterval"), Equals, false)
	c.Check(constants.IsOperationalMimir("InvVaultDeficitInterval"), Equals, false)
	c.Check(constants.IsOperationalMimir("InvAsgardSolvencyTolerance"), Equals, false)
	c.Check(constants.IsOperationalMimir("InvBondSolvencyTolerance"), Equals, false)
	c.Check(constants.IsOperationalMimir("InvVaultDeficitTolerance"), Equals, false)

	// Unrelated halt keys retain their existing operational classification.
	c.Check(constants.IsOperationalMimir("HaltSigningBTC"), Equals, true)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirSkipSignerCache(c *C) {
	// SkipSignerCache<Chain> is the per-chain bifrost gate that authorizes the
	// expired-cache verification path (and a possible cache clear). Every node
	// in the network MUST observe the same gate value at the same height —
	// otherwise some bifrosts re-broadcast a tx while others don't, splitting
	// observation consensus. Marking it operational forces consensus storage,
	// closing the operator-misconfiguration divergence vector.
	c.Check(constants.IsOperationalMimir("SkipSignerCacheBTC"), Equals, true)
	c.Check(constants.IsOperationalMimir("SkipSignerCacheETH"), Equals, true)
	c.Check(constants.IsOperationalMimir("SkipSignerCacheXRP"), Equals, true)

	// Case-insensitive (partial match uppercases the key).
	c.Check(constants.IsOperationalMimir("skipsignercacheBTC"), Equals, true)
	c.Check(constants.IsOperationalMimir("SKIPSIGNERCACHEBTC"), Equals, true)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirAllowNoVault(c *C) {
	// AllowNoVault-<CHAIN> gates noop:novault debits for a short correction
	// window; operators must be able to open it by plurality, not 2/3. Derive
	// the key from the template so a renamed constant fails here, not on mainnet.
	c.Check(constants.IsOperationalMimir(fmt.Sprintf(constants.MimirTemplateAllowNoVault, "TRON")), Equals, true)
	c.Check(constants.IsOperationalMimir("ALLOWNOVAULT-BSC"), Equals, true)
	c.Check(constants.IsOperationalMimir("allownovault-eth"), Equals, true)

	// Unrelated keys stay non-operational.
	c.Check(constants.IsOperationalMimir("NoVaultAllowed-TRON"), Equals, false)
	c.Check(constants.IsOperationalMimir("AllowNoVault"), Equals, false)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirXMRConsolidation(c *C) {
	// Keys read by bifrost's Monero client (bifrost/pkg/chainclients/monero/client.go).
	// Consolidation locks vault liquidity for Monero's 10-block spend lock, so
	// operators must be able to tune or disable it by plurality, not 2/3.
	keys := []string{
		"XMRConsolidationEnabled",
		"XMRConsolidationCheckIntervalBlocks",
		"XMRConsolidationCooldownBlocks",
		"XMRConsolidationMinUnlockedOutputs",
		"XMRConsolidationTargetOutputs",
		"XMRConsolidationMinUnlockedAtomic",
		"XMRConsolidationReserveBps",
		"XMRConsolidationChurnLookaheadBlocks",
		"XMRConsolidationMaxRetries",
		"XMRConsolidationMaxInputs",
		"XMRConsolidationMaxLockBps",
	}
	for _, key := range keys {
		c.Check(constants.IsOperationalMimir(key), Equals, true, Commentf("key=%s", key))
		c.Check(constants.IsOperationalMimir(strings.ToUpper(key)), Equals, true, Commentf("key=%s", key))
		c.Check(constants.IsOperationalMimir(strings.ToLower(key)), Equals, true, Commentf("key=%s", key))
	}
	c.Check(constants.IsOperationalMimir("xmrCONSOLIDATIONmaxLOCKbps"), Equals, true)

	// The UTXO input cap shared with the other UTXO chains stays economic, as do
	// keys that merely mention XMR or consolidation.
	c.Check(constants.IsOperationalMimir("MaxUTXOsToSpend"), Equals, false)
	c.Check(constants.IsOperationalMimir("XMRConsolidatio"), Equals, false)
	c.Check(constants.IsOperationalMimir("ConsolidationXMREnabled"), Equals, false)
	c.Check(constants.IsOperationalMimir("BTCConsolidationEnabled"), Equals, false)
}
