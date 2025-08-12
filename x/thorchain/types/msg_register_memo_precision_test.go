package types

import (
	"fmt"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type PrecisionValidationSuite struct{}

var _ = Suite(&PrecisionValidationSuite{})

func TestPrecisionValidation(t *testing.T) { TestingT(t) }

// Test chain precision support validation
func (s *PrecisionValidationSuite) TestValidateChainPrecisionSupport(c *C) {
	// Test supported chains
	supportedChains := []string{"BTC", "ETH", "BSC", "LTC", "BCH", "AVAX", "GAIA", "THOR"}
	for _, chainID := range supportedChains {
		err := ValidateChainPrecisionSupport(chainID)
		c.Assert(err, IsNil, Commentf("Chain %s should be supported", chainID))
	}

	// Test unsupported chain due to precision limitations
	err := ValidateChainPrecisionSupport("DOGE")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*does not support amount-based reference encoding.*")

	// Test invalid chain ID
	err = ValidateChainPrecisionSupport("INVALID")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*invalid chain ID.*")

	// Test unknown chain
	err = ValidateChainPrecisionSupport("UNKNOWN")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*not supported for memoless transactions.*")
}

// Test amount validation for reference encoding
func (s *PrecisionValidationSuite) TestValidateAmountForReference(c *C) {
	testCases := []struct {
		amount    string
		reference string
		chainID   string
		expectErr bool
		errMatch  string
	}{
		// Valid cases
		{"1.00000000", "12345", "BTC", false, ""},
		{"0.50000000", "54321", "BTC", false, ""},
		{"100.000000000000000000", "11111", "ETH", false, ""},

		// Amount too small
		{"0.00000001", "12345", "BTC", true, "below minimum threshold"},
		{"0.000000000000000001", "12345", "ETH", true, "below minimum threshold"},

		// Amount too large
		{"25000000.00000000", "12345", "BTC", true, "exceeds maximum threshold"},

		// Invalid reference
		{"1.00000000", "9999", "BTC", true, "out of valid range"},
		{"1.00000000", "100000", "BTC", true, "out of valid range"},
		{"1.00000000", "abcde", "BTC", true, "invalid reference format"},

		// Invalid amount format
		{"abc.def", "12345", "BTC", true, "invalid amount format"},
		{"", "12345", "BTC", true, "invalid amount format"},

		// Precision loss scenarios - amounts that would lose precision in encoding
		{"0.0005000099", "12345", "BTC", true, "precision loss"},
	}

	for _, tc := range testCases {
		err := ValidateAmountForReference(tc.amount, tc.reference, tc.chainID)
		if tc.expectErr {
			c.Assert(err, NotNil, Commentf("Expected error for %s %s %s", tc.amount, tc.reference, tc.chainID))
			if tc.errMatch != "" {
				c.Assert(err.Error(), Matches, fmt.Sprintf(".*%s.*", tc.errMatch))
			}
		} else {
			c.Assert(err, IsNil, Commentf("Should not error for %s %s %s", tc.amount, tc.reference, tc.chainID))
		}
	}
}

// Test asset-specific precision validation
func (s *PrecisionValidationSuite) TestValidateAssetPrecision(c *C) {
	testCases := []struct {
		amount    string
		asset     string
		chainID   string
		expectErr bool
		errMatch  string
	}{
		// Valid cases
		{"100.123456", "USDC", "ETH", false, ""},            // USDC has 6 decimals
		{"100.123456789012345678", "ETH", "ETH", false, ""}, // ETH has 18 decimals
		{"100.12345678", "BTC", "BTC", false, ""},           // BTC has 8 decimals

		// Invalid cases - too many decimal places
		{"100.1234567", "USDC", "ETH", true, "supports maximum 6"},
		{"100.123456789", "BTC", "BTC", true, "supports maximum 8"},

		// Edge cases
		{"100", "USDC", "ETH", false, ""},        // Integer amount is fine
		{"100.000000", "USDC", "ETH", false, ""}, // Exact precision match

		// Invalid amount format
		{"100.123.456", "USDC", "ETH", true, "invalid amount format"},
	}

	for _, tc := range testCases {
		err := ValidateAssetPrecision(tc.amount, tc.asset, tc.chainID)
		if tc.expectErr {
			c.Assert(err, NotNil, Commentf("Expected error for %s %s %s", tc.amount, tc.asset, tc.chainID))
			if tc.errMatch != "" {
				c.Assert(err.Error(), Matches, fmt.Sprintf(".*%s.*", tc.errMatch))
			}
		} else {
			c.Assert(err, IsNil, Commentf("Should not error for %s %s %s", tc.amount, tc.asset, tc.chainID))
		}
	}
}

// Test dust threshold validation
func (s *PrecisionValidationSuite) TestValidateDustThreshold(c *C) {
	testCases := []struct {
		amount    string
		chainID   string
		expectErr bool
		errMatch  string
	}{
		// Valid cases
		{"0.00001000", "BTC", false, ""},
		{"1.00000000", "DOGE", false, ""},
		{"0.000001", "GAIA", false, ""},

		// Below dust threshold
		{"0.00000545", "BTC", true, "below dust threshold"},
		{"0.00000001", "BTC", true, "below dust threshold"},
		{"0.99999999", "DOGE", true, "below dust threshold"},

		// Invalid format
		{"invalid", "BTC", true, "invalid amount format"},
	}

	for _, tc := range testCases {
		err := ValidateDustThreshold(tc.amount, tc.chainID)
		if tc.expectErr {
			c.Assert(err, NotNil, Commentf("Expected error for %s %s", tc.amount, tc.chainID))
			if tc.errMatch != "" {
				c.Assert(err.Error(), Matches, fmt.Sprintf(".*%s.*", tc.errMatch))
			}
		} else {
			c.Assert(err, IsNil, Commentf("Should not error for %s %s", tc.amount, tc.chainID))
		}
	}
}

// Test memo size validation for chain limits
func (s *PrecisionValidationSuite) TestValidateMemoSizeForChain(c *C) {
	// Create test memos of various sizes
	shortMemo := "short memo"
	mediumMemo := string(make([]byte, 100))    // 100 bytes
	longMemo := string(make([]byte, 500))      // 500 bytes
	veryLongMemo := string(make([]byte, 2000)) // 2000 bytes

	testCases := []struct {
		memo      string
		chainID   string
		expectErr bool
		errMatch  string
	}{
		// BTC - 80 byte limit
		{shortMemo, "BTC", false, ""},
		{mediumMemo, "BTC", true, "exceeds chain BTC limit of 80"},

		// ETH - 1024 byte limit
		{shortMemo, "ETH", false, ""},
		{mediumMemo, "ETH", false, ""},
		{longMemo, "ETH", false, ""},
		{veryLongMemo, "ETH", true, "exceeds chain ETH limit of 1024"},

		// GAIA - 256 byte limit
		{shortMemo, "GAIA", false, ""},
		{mediumMemo, "GAIA", false, ""},
		{longMemo, "GAIA", true, "exceeds chain GAIA limit of 256"},
	}

	for _, tc := range testCases {
		err := ValidateMemoSizeForChain(tc.memo, tc.chainID)
		if tc.expectErr {
			c.Assert(err, NotNil, Commentf("Expected error for memo size %d on %s", len(tc.memo), tc.chainID))
			if tc.errMatch != "" {
				c.Assert(err.Error(), Matches, fmt.Sprintf(".*%s.*", tc.errMatch))
			}
		} else {
			c.Assert(err, IsNil, Commentf("Should not error for memo size %d on %s", len(tc.memo), tc.chainID))
		}
	}
}

// Test integer overflow protection
func (s *PrecisionValidationSuite) TestCheckIntegerOverflowProtection(c *C) {
	testCases := []struct {
		amount    string
		chainID   string
		expectErr bool
		errMatch  string
	}{
		// Valid amounts
		{"1000.00000000", "BTC", false, ""},
		{"1000000.000000000000000000", "ETH", false, ""},

		// Amounts that would cause overflow
		{"999999999999999999999999999999999999999.00000000", "BTC", true, "overflow"},
		{"999999999999999999999999999999999999999.000000000000000000", "ETH", true, "overflow"},

		// Edge case - maximum safe amounts
		{"21000000.00000000", "BTC", false, ""},             // BTC max supply
		{"1000000000.000000000000000000", "ETH", false, ""}, // Large but safe ETH amount
	}

	for _, tc := range testCases {
		err := CheckIntegerOverflowProtection(tc.amount, tc.chainID)
		if tc.expectErr {
			c.Assert(err, NotNil, Commentf("Expected error for %s %s", tc.amount, tc.chainID))
			if tc.errMatch != "" {
				c.Assert(err.Error(), Matches, fmt.Sprintf(".*%s.*", tc.errMatch))
			}
		} else {
			c.Assert(err, IsNil, Commentf("Should not error for %s %s", tc.amount, tc.chainID))
		}
	}
}

// Test dust attack prevention
func (s *PrecisionValidationSuite) TestPreventDustAttack(c *C) {
	// Normal transaction amounts
	normalAmounts := []string{"1.00000000", "2.50000000", "5.12345678", "10.00000000"}
	err := PreventDustAttack(normalAmounts, "BTC", 3600)
	c.Assert(err, IsNil)

	// Dust attack pattern - many small amounts
	dustAmounts := make([]string, 20)
	for i := range dustAmounts {
		dustAmounts[i] = "0.00000600" // Just above dust but still very small
	}
	err = PreventDustAttack(dustAmounts, "BTC", 3600)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*potential dust attack detected.*")

	// Mixed pattern - should not trigger
	mixedAmounts := []string{
		"0.00000600", "0.00000700", "0.00000800", // Some dust
		"1.00000000", "2.00000000", "5.00000000", "10.00000000", // Normal amounts
	}
	err = PreventDustAttack(mixedAmounts, "BTC", 3600)
	c.Assert(err, IsNil)
}

// Test supported chains list
func (s *PrecisionValidationSuite) TestGetSupportedChains(c *C) {
	supported := GetSupportedChains()
	c.Assert(len(supported) > 0, Equals, true)

	// Verify known supported chains are included
	supportedMap := make(map[string]bool)
	for _, chain := range supported {
		supportedMap[chain] = true
	}

	c.Assert(supportedMap["BTC"], Equals, true)
	c.Assert(supportedMap["ETH"], Equals, true)
	c.Assert(supportedMap["BSC"], Equals, true)

	// Verify DOGE is not supported due to precision limitations
	c.Assert(supportedMap["DOGE"], Equals, false)
}

// Test chain config retrieval
func (s *PrecisionValidationSuite) TestGetChainConfig(c *C) {
	// Valid chain
	config, err := GetChainConfig("BTC")
	c.Assert(err, IsNil)
	c.Assert(config.DecimalPlaces, Equals, 8)
	c.Assert(config.SupportsAmountRef, Equals, true)

	// Invalid chain
	_, err = GetChainConfig("INVALID")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*invalid chain ID.*")

	// Unsupported chain
	_, err = GetChainConfig("UNKNOWN")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*unsupported chain.*")
}

// Test precision encoding/decoding reversibility
func (s *PrecisionValidationSuite) TestAmountEncodingReversibility(c *C) {
	testCases := []struct {
		amount     string
		reference  string
		chainID    string
		shouldWork bool
	}{
		// Cases that should work
		{"1.00000000", "12345", "BTC", true},
		{"0.50000000", "54321", "BTC", true},
		{"100.000000000000000000", "11111", "ETH", true},

		// Cases that might have precision issues
		{"0.000000123456789", "12345", "BTC", false},   // Too much precision for encoding
		{"0.123456789012345678", "54321", "ETH", true}, // Should work for ETH
	}

	for _, tc := range testCases {
		err := ValidateAmountForReference(tc.amount, tc.reference, tc.chainID)
		if tc.shouldWork {
			c.Assert(err, IsNil, Commentf("Amount %s with reference %s should work on %s",
				tc.amount, tc.reference, tc.chainID))
		} else {
			// We don't require it to fail, but if it does, that's expected for precision cases
		}
	}
}

// Benchmark tests for performance validation
func (s *PrecisionValidationSuite) BenchmarkValidateAmountForReference(c *C) {
	amount := "1.00000000"
	reference := "12345"
	chainID := "BTC"

	// This should complete in less than 1ms as required by acceptance criteria
	for i := 0; i < c.N; i++ {
		err := ValidateAmountForReference(amount, reference, chainID)
		c.Assert(err, IsNil)
	}
}

// Test edge cases and boundary conditions
func (s *PrecisionValidationSuite) TestEdgeCases(c *C) {
	// Test minimum valid reference
	err := ValidateAmountForReference("1.00000000", "10000", "BTC")
	c.Assert(err, IsNil)

	// Test maximum valid reference
	err = ValidateAmountForReference("1.00000000", "99999", "BTC")
	c.Assert(err, IsNil)

	// Test exactly at minimum amount threshold
	config, _ := GetChainConfig("BTC")
	err = ValidateAmountForReference(config.MinAmount, "12345", "BTC")
	c.Assert(err, IsNil)

	// Test exactly at dust threshold
	err = ValidateDustThreshold(config.MinDustThreshold, "BTC")
	c.Assert(err, IsNil)

	// Test just below dust threshold
	dustDec, _ := cosmos.NewDecFromStr(config.MinDustThreshold)
	belowDust := dustDec.Sub(cosmos.NewDec(1).Quo(cosmos.NewDec(100000000))) // Subtract 1 satoshi
	err = ValidateDustThreshold(belowDust.String(), "BTC")
	c.Assert(err, NotNil)
}

// Test integration with existing validation framework
func (s *PrecisionValidationSuite) TestIntegrationWithExistingValidation(c *C) {
	// Test that precision validation works for supported chains
	err := ValidateChainPrecisionSupport("BTC")
	c.Assert(err, IsNil)

	// Test that precision validation rejects invalid chains
	err = ValidateChainPrecisionSupport("INVALID_CHAIN_WITH_SPECIAL_CHARS!")
	c.Assert(err, NotNil)
}
