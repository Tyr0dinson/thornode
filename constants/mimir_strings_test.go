package constants

import (
	"strings"

	. "gopkg.in/check.v1"
)

func (s *ConstantsTestSuite) TestIsOperationalMimir(c *C) {
	testCases := []struct {
		key      string
		expected bool
		reason   string
	}{
		// ===== EXACT OPERATIONAL MATCHES =====
		// Test the exact matches with case variations
		{"MintSynths", true, "exact operational match"},
		{"mintsynths", true, "exact operational match (lowercase)"},
		{"MINTSYNTHS", true, "exact operational match (uppercase)"},
		{"MiNtSyNtHs", true, "exact operational match (mixed case)"},

		{"TradeAccountsEnabled", true, "exact operational match"},
		{"tradeaccountsenabled", true, "exact operational match (lowercase)"},
		{"TRADEACCOUNTSENABLED", true, "exact operational match (uppercase)"},

		{"RUNEPoolEnabled", true, "exact operational match"},
		{"runepoolenabled", true, "exact operational match (lowercase)"},
		{"RUNEPOOLENABLED", true, "exact operational match (uppercase)"},

		{"EVMDisableContractWhitelist", true, "exact operational match"},
		{"evmdisablecontractwhitelist", true, "exact operational match (lowercase)"},
		{"EVMDISABLECONTRACTWHITELIST", true, "exact operational match (uppercase)"},

		{"MaxOutboundAttempts", true, "exact operational match"},
		{"maxoutboundattempts", true, "exact operational match (lowercase)"},
		{"MAXOUTBOUNDATTEMPTS", true, "exact operational match (uppercase)"},

		{"AdvSwapQueueRapidSwapMax", true, "exact operational match"},
		{"advswapqueuerapidswapmax", true, "exact operational match (lowercase)"},
		{"ADVSWAPQUEUERAPIDSWAPMAX", true, "exact operational match (uppercase)"},

		{"EnableADVSwapQueue", true, "exact operational match"},
		{"enableadvswapqueue", true, "exact operational match (lowercase)"},
		{"ENABLEADVSWAPQUEUE", true, "exact operational match (uppercase)"},

		// ===== ECONOMIC EXCEPTIONS (OVERRIDE PATTERNS) =====
		// These should return false even though they contain operational keywords
		{"NodePauseChainBlocks", false, "economic exception"},
		{"nodepausechainblocks", false, "economic exception (lowercase)"},
		{"NODEPAUSECHAINBLOCKS", false, "economic exception (uppercase)"},

		// PauseLoans is no longer an exception (loans were removed), so it now
		// falls through to the PAUSE pattern.
		{"PauseLoans", true, "contains PAUSE (no longer an exception)"},
		{"pauseloans", true, "contains PAUSE (lowercase)"},

		{"PauseOnSlashThreshold", false, "economic exception (contains PAUSE but exception)"},
		{"pauseonslashthreshold", false, "economic exception (lowercase)"},
		{"PAUSEONSLASHTHRESHOLD", false, "economic exception (uppercase)"},

		// ===== PATTERN MATCHES: HALT =====
		{"HaltBTCChain", true, "contains HALT"},
		{"haltbtcchain", true, "contains HALT (lowercase)"},
		{"HALTBTCCHAIN", true, "contains HALT (uppercase)"},
		{"SomeHaltSomething", true, "contains HALT in middle"},
		{"TestHalt", true, "contains HALT at end"},
		{"HaltTest", true, "contains HALT at start"},
		{"MyCustomHaltFeature", true, "contains HALT"},

		// ===== PATTERN MATCHES: PAUSE =====
		{"PauseETH", true, "contains PAUSE"},
		{"pauseeth", true, "contains PAUSE (lowercase)"},
		{"PAUSEETH", true, "contains PAUSE (uppercase)"},
		{"StreamingSwapPause", true, "contains PAUSE at end"},
		{"PauseStreamingSwap", true, "contains PAUSE at start"},
		{"SomePauseSomething", true, "contains PAUSE in middle"},
		{"MyCustomPauseFeature", true, "contains PAUSE"},

		// ===== PATTERN MATCHES: STOPSOLVENCYCHECK =====
		{"StopSolvencyCheckETH", true, "contains STOPSOLVENCYCHECK"},
		{"stopsolvencychecketh", true, "contains STOPSOLVENCYCHECK (lowercase)"},
		{"STOPSOLVENCYCHECKETH", true, "contains STOPSOLVENCYCHECK (uppercase)"},
		{"MyStopSolvencyCheckFeature", true, "contains STOPSOLVENCYCHECK"},
		{"TestStopSolvencyCheck", true, "contains STOPSOLVENCYCHECK at end"},

		// ===== SUFFIX MATCHES: SLIPMINBPS =====
		// Note: This is case-insensitive because key is converted to uppercase first
		{"SomeSlipMinBps", true, "ends with SLIPMINBPS"},
		{"SwapSlipBasisPointsMinSLIPMINBPS", true, "ends with SLIPMINBPS"},
		{"TestSLIPMINBPS", true, "ends with SLIPMINBPS"},
		{"SLIPMINBPS", true, "exact SLIPMINBPS"},

		// These should also work due to case insensitivity
		{"Someslipminbps", true, "ends with slipminbps (case insensitive)"},
		{"SomeSlipMinbps", true, "ends with slipminbps (mixed case)"},
		{"someslipminbps", true, "ends with slipminbps (all lowercase)"},

		// ===== ECONOMIC MIMIRS =====
		{"MaxLiquidityRune", false, "economic mimir"},
		{"IncentiveCurve", false, "economic mimir"},
		{"PoolCycle", false, "economic mimir"},
		{"MinimumNodesForBFT", false, "economic mimir"},
		{"NetworkFeePercent", false, "economic mimir"},
		{"SwapFeeRate", false, "economic mimir"},
		{"LiquidityProviderFee", false, "economic mimir"},

		// ===== EDGE CASES =====
		{"", false, "empty string"},
		{"HALT", true, "single keyword HALT"},
		{"halt", true, "single keyword HALT (lowercase)"},
		{"PAUSE", true, "single keyword PAUSE"},
		{"pause", true, "single keyword PAUSE (lowercase)"},
		{"STOPSOLVENCYCHECK", true, "single keyword STOPSOLVENCYCHECK"},
		{"stopsolvencycheck", true, "single keyword STOPSOLVENCYCHECK (lowercase)"},

		// ===== SUBSTRING EDGE CASES =====
		// These should NOT match because they don't contain the full keywords
		{"HAL", false, "partial HALT"},
		{"HALTING", true, "contains HALT (even though has -ING suffix)"},
		{"PAUSED", true, "contains PAUSE (even though has -D suffix)"},
		{"PAUSING", false, "contains PAUS but not PAUSE"},

		// ===== COMPLEX CASES =====
		{"HaltAndPause", true, "contains both HALT and PAUSE"},
		{"PauseHaltTest", true, "contains both PAUSE and HALT"},
		{"NotOperational", false, "no operational keywords"},
		{"JustARandomMimir", false, "no operational keywords"},

		// ===== BOUNDARY CASES =====
		{"A", false, "single character"},
		{"AB", false, "two characters"},
		{"ABC", false, "three characters"},
		{"HaltX", true, "minimal HALT match"},
		{"XHalt", true, "minimal HALT match at end"},
		{"PauseX", true, "minimal PAUSE match"},
		{"XPause", true, "minimal PAUSE match at end"},
	}

	for _, tc := range testCases {
		result := IsOperationalMimir(tc.key)
		c.Assert(result, Equals, tc.expected, Commentf("Key: '%s', Reason: %s", tc.key, tc.reason))
	}
}

func (s *ConstantsTestSuite) TestIsOperationalMimir_CompleteCoverage(c *C) {
	// ===== VERIFY ALL EXACT OPERATIONAL MATCHES =====
	// This test ensures we have coverage for every exact operational mimir
	exactOperationalMimirs := []string{
		"MintSynths",
		"TradeAccountsEnabled",
		"RUNEPoolEnabled",
		"EVMDisableContractWhitelist",
		"MaxOutboundAttempts",
		"AdvSwapQueueRapidSwapMax",
		"EnableADVSwapQueue",
		"StreamingLimitSwapMaxAge",
		OverSolvencyCheckInterval.String(),
		OverSolvencyToTreasuryBps.String(),
		MimirKeyScheduledMigration,
		MimirKeyDisableTSSSignerFiltering,
		MaxRetiredVaultRecoveryAttempts.String(),
		P2PGateDisabled.String(),
		EnableMemolessOutbound.String(),
		MinNodeChainSupport.String(),
		ChainLiquidityRunePerValidator.String(),
		POLReserveSystemIncomeBps.String(),
		StableReserveOraclePricing.String(),
		MimirKeyPreflightChainSetDisabled,
	}

	for _, mimir := range exactOperationalMimirs {
		// Test exact case
		c.Assert(IsOperationalMimir(mimir), Equals, true, Commentf("Exact operational mimir should return true: %s", mimir))
		// Test lowercase
		c.Assert(IsOperationalMimir(strings.ToLower(mimir)), Equals, true, Commentf("Lowercase operational mimir should return true: %s", strings.ToLower(mimir)))
		// Test uppercase
		c.Assert(IsOperationalMimir(strings.ToUpper(mimir)), Equals, true, Commentf("Uppercase operational mimir should return true: %s", strings.ToUpper(mimir)))
	}

	// ===== VERIFY ALL ECONOMIC EXCEPTIONS =====
	// This test ensures we have coverage for every economic exception
	economicExceptions := []string{
		"NodePauseChainBlocks",
		"PauseOnSlashThreshold",
	}

	for _, mimir := range economicExceptions {
		// Test exact case
		c.Assert(IsOperationalMimir(mimir), Equals, false, Commentf("Economic exception should return false: %s", mimir))
		// Test lowercase
		c.Assert(IsOperationalMimir(strings.ToLower(mimir)), Equals, false, Commentf("Lowercase economic exception should return false: %s", strings.ToLower(mimir)))
		// Test uppercase
		c.Assert(IsOperationalMimir(strings.ToUpper(mimir)), Equals, false, Commentf("Uppercase economic exception should return false: %s", strings.ToUpper(mimir)))
	}

	// ===== VERIFY ALL PATTERN MATCHES =====
	// This test ensures we have coverage for every pattern
	operationalPatterns := []string{
		"HALT",
		"PAUSE",
		"STOPSOLVENCYCHECK",
		"MIMIRUPGRADECONTRACT",
		"EVMALLOWANCECHECK",
		"POLRESERVEBLACKLIST",
		"POLRESERVEWHITELIST",
		"DYNAMICFEE-WHITELIST",
		"REVSHARE",
		"EVMDIRECTERC20INBOUND",
		"SKIPSIGNERCACHE",
	}

	for _, pattern := range operationalPatterns {
		// Test pattern by itself
		c.Assert(IsOperationalMimir(pattern), Equals, true, Commentf("Pattern should return true: %s", pattern))
		c.Assert(IsOperationalMimir(strings.ToLower(pattern)), Equals, true, Commentf("Lowercase pattern should return true: %s", strings.ToLower(pattern)))

		// Test pattern with prefix
		c.Assert(IsOperationalMimir("Test"+pattern), Equals, true, Commentf("Pattern with prefix should return true: Test%s", pattern))

		// Test pattern with suffix
		c.Assert(IsOperationalMimir(pattern+"Test"), Equals, true, Commentf("Pattern with suffix should return true: %sTest", pattern))

		// Test pattern in middle
		c.Assert(IsOperationalMimir("Pre"+pattern+"Post"), Equals, true, Commentf("Pattern in middle should return true: Pre%sPost", pattern))
	}

	// ===== VERIFY SUFFIX MATCH =====
	// This test ensures SLIPMINBPS suffix matching works
	slipMinBpsTests := []string{
		"SLIPMINBPS",
		"SomeSLIPMINBPS",
		"TestSlipMinBps",
		"slipminbps",
		"someslipminbps",
	}

	for _, test := range slipMinBpsTests {
		c.Assert(IsOperationalMimir(test), Equals, true, Commentf("SLIPMINBPS suffix should return true: %s", test))
	}
}

func (s *ConstantsTestSuite) TestIsOperationalMimir_EdgeCasesAndDocumentation(c *C) {
	// Additional edge cases to ensure complete coverage

	// Test that exceptions truly override patterns
	c.Assert(IsOperationalMimir("NodePauseChainBlocks"), Equals, false, Commentf("NodePauseChainBlocks should be economic despite containing 'PAUSE'"))
	c.Assert(IsOperationalMimir("PauseOnSlashThreshold"), Equals, false, Commentf("PauseOnSlashThreshold should be economic despite containing 'PAUSE'"))

	// Test case sensitivity behavior (suffix matching is actually case-insensitive)
	c.Assert(IsOperationalMimir("SLIPMINBPS"), Equals, true, Commentf("SLIPMINBPS suffix should work"))
	c.Assert(IsOperationalMimir("slipminbps"), Equals, true, Commentf("slipminbps lowercase suffix should work (case insensitive)"))

	// Test real-world examples that might exist in the codebase
	realWorldTests := []struct {
		key      string
		expected bool
		reason   string
	}{
		{"HaltSecuredGlobal", true, "real mimir from constants"},
		{"HaltWasmGlobal", true, "real mimir from constants"},
		{"HaltSecuredDeposit-BTC", true, "templated mimir with HALT"},
		{"HaltSecuredWithdraw-ETH", true, "templated mimir with HALT"},
		{"PauseLPDeposit-BTC.BTC", true, "templated mimir with PAUSE"},
		{"SwapSlipBasisPointsMin-ETH", false, "should not match without proper suffix"},
		{"AttestationMaxBatchSize", false, "real economic mimir"},
		{"AttestationPeerConcurrentSends", false, "real economic mimir"},
	}

	for _, tc := range realWorldTests {
		result := IsOperationalMimir(tc.key)
		c.Assert(result, Equals, tc.expected, Commentf("Real-world test - Key: '%s', Reason: %s", tc.key, tc.reason))
	}
}
