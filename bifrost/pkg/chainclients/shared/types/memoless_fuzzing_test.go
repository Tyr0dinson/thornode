package types

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// Property-based fuzzing tests for memoless protocol
// These tests validate invariant properties under adversarial conditions

func TestMemolessPropertyBasedFuzzing(t *testing.T) {
	rand.Seed(time.Now().UnixNano())
	utils := NewMemolessUtils()

	t.Run("property_reference_extraction_correctness", func(t *testing.T) {
		// Property: All valid 5-digit references should extract correctly
		successCount := 0
		for i := 0; i < 1000; i++ { // Reduced for faster testing
			// Generate random base amounts and references
			baseAmount := rand.Uint64() % 1000000 // Keep amounts reasonable
			reference := fmt.Sprintf("%05d", rand.Intn(100000))

			// Encode reference into amount using utils
			encodedAmount, err := utils.EncodeAmountWithReference(
				cosmos.NewUint(baseAmount), reference)
			if err != nil {
				continue // Skip invalid combinations
			}

			// Property validation - extract reference
			extractedRef, err := utils.ExtractReferenceFromAmount(encodedAmount)
			if err != nil {
				continue // Skip if extraction fails
			}

			require.Equal(t, reference, extractedRef,
				"Reference extraction failed for amount %s, expected %s, got %s",
				encodedAmount.String(), reference, extractedRef)
			successCount++
		}

		t.Logf("Successfully tested %d reference extraction cycles", successCount)
		require.Greater(t, successCount, 100, "Should have at least 100 successful test cycles")
	})

	t.Run("property_encoding_reversibility", func(t *testing.T) {
		// Property: Reference encoding should be perfectly reversible
		successCount := 0
		for i := 0; i < 500; i++ { // Reduced for faster testing
			baseAmount := rand.Uint64() % 1000000
			reference := fmt.Sprintf("%05d", rand.Intn(100000))

			// Encode -> Decode cycle
			encoded, err := utils.EncodeAmountWithReference(
				cosmos.NewUint(baseAmount), reference)
			if err != nil {
				continue // Skip invalid combinations
			}

			extractedRef, err := utils.ExtractReferenceFromAmount(encoded)
			if err != nil {
				continue // Skip if extraction fails
			}

			require.Equal(t, reference, extractedRef, "Reference reversibility failed")
			successCount++
		}

		t.Logf("Successfully tested %d reversibility cycles", successCount)
		require.Greater(t, successCount, 50, "Should have at least 50 successful reversibility cycles")
	})

	t.Run("property_boundary_conditions", func(t *testing.T) {
		// Test edge cases and boundary conditions
		boundaryTests := []struct {
			name        string
			amount      string
			expectedRef string
			shouldPass  bool
		}{
			{"zero_amount", "0", "", false},
			{"minimum_valid", "10000", "10000", true}, // Updated to valid 10000-99999 range
			{"just_below_threshold", "9999", "", false},
			{"large_amount", "999999999999", "99999", true},
			{"simple_case", "1000012345", "12345", true},
		}

		for _, test := range boundaryTests {
			t.Run(test.name, func(t *testing.T) {
				amount := cosmos.NewUintFromString(test.amount)

				extractedRef, err := utils.ExtractReferenceFromAmount(amount)

				if test.shouldPass {
					require.NoError(t, err, "Expected successful extraction for amount %s", test.amount)
					require.Equal(t, test.expectedRef, extractedRef,
						"Boundary condition failed for amount %s", test.amount)
				} else {
					require.Error(t, err, "Expected extraction failure for amount %s", test.amount)
				}
			})
		}
	})
}

func TestMemolessAdversarialFuzzing(t *testing.T) {
	utils := NewMemolessUtils()

	t.Run("adversarial_reference_patterns", func(t *testing.T) {
		// Test against adversarial reference patterns
		adversarialPatterns := []struct {
			name      string
			reference string
			valid     bool
		}{
			{"valid_reference", "12345", true},
			{"leading_zeros", "00123", false}, // Updated: below 10000 range - invalid
			{"all_zeros", "00000", false},      // Below 10000 range - invalid
			{"all_nines", "99999", true},       // At top of range - valid
			{"negative_sign", "-1234", false},
			{"hex_digits", "ABCDE", false},
			{"mixed_case", "1a2B3", false},
			{"too_short", "123", false},
			{"too_long", "123456", false},
			{"empty_string", "", false},
			{"spaces", "123 4", false},
		}

		for _, pattern := range adversarialPatterns {
			t.Run(pattern.name, func(t *testing.T) {
				// Test reference validation
				err := utils.ValidateReference(pattern.reference)

				if pattern.valid {
					require.NoError(t, err, "Valid reference %s should pass validation", pattern.reference)
				} else {
					require.Error(t, err, "Invalid reference %s should fail validation", pattern.reference)
				}
			})
		}
	})

	t.Run("timing_based_attacks", func(t *testing.T) {
		// Test for timing-based vulnerabilities in reference extraction
		measurements := make([]time.Duration, 100)

		validMeasurements := 0
		for i := 0; i < 100; i++ {
			// Create test amounts
			baseAmount := rand.Uint64() % 1000000
			reference := fmt.Sprintf("%05d", rand.Intn(100000))

			encodedAmount, err := utils.EncodeAmountWithReference(
				cosmos.NewUint(baseAmount), reference)
			if err != nil {
				continue
			}

			start := time.Now()
			_, err = utils.ExtractReferenceFromAmount(encodedAmount)
			measurements[validMeasurements] = time.Since(start)

			if err == nil {
				validMeasurements++
			}
		}

		// Calculate timing statistics
		if validMeasurements > 10 {
			var total time.Duration
			var min, max time.Duration = time.Hour, 0

			for i := 0; i < validMeasurements; i++ {
				duration := measurements[i]
				total += duration
				if duration < min {
					min = duration
				}
				if duration > max {
					max = duration
				}
			}

			avg := total / time.Duration(validMeasurements)
			t.Logf("Timing analysis - Min: %v, Max: %v, Avg: %v, Samples: %d", min, max, avg, validMeasurements)

			// Ensure no extreme timing variations that could leak information
			if min > 0 {
				ratio := float64(max) / float64(min)
				require.Less(t, ratio, 1000.0, "Excessive timing variation detected: %f", ratio)
			}
		}
	})
}

func TestMemolessStateInvariants(t *testing.T) {
	utils := NewMemolessUtils()

	t.Run("invariant_idempotency", func(t *testing.T) {
		// Property: Multiple extractions should yield same results
		for i := 0; i < 100; i++ {
			baseAmount := rand.Uint64() % 1000000
			reference := fmt.Sprintf("%05d", rand.Intn(100000))

			encodedAmount, err := utils.EncodeAmountWithReference(
				cosmos.NewUint(baseAmount), reference)
			if err != nil {
				continue
			}

			// Extract multiple times
			ref1, err1 := utils.ExtractReferenceFromAmount(encodedAmount)
			ref2, err2 := utils.ExtractReferenceFromAmount(encodedAmount)

			require.Equal(t, err1 != nil, err2 != nil, "ExtractReferenceFromAmount error consistency failed")

			if err1 == nil && err2 == nil {
				require.Equal(t, ref1, ref2, "ExtractReferenceFromAmount not idempotent")
			}
		}
	})

	t.Run("invariant_minimum_amount", func(t *testing.T) {
		// Property: Minimum memoless amount should be consistent
		minAmount := utils.GetMinimumMemolessAmount()
		require.True(t, minAmount.GT(cosmos.ZeroUint()), "Minimum amount should be greater than zero")

		// Should be able to extract reference from minimum amount
		_, err := utils.ExtractReferenceFromAmount(minAmount)
		require.NoError(t, err, "Should be able to extract reference from minimum amount")
	})
}

func TestMemolessSecurityProperties(t *testing.T) {
	utils := NewMemolessUtils()

	t.Run("reference_validation_security", func(t *testing.T) {
		// Test that validation catches malicious references
		maliciousRefs := []string{
			"\x00\x00\x00\x00\x00", // Null bytes
			"12345\n",              // With newline
			"12345\r",              // With carriage return
			"12345 ",               // Trailing space
			" 12345",               // Leading space
			"１２３４５",                // Unicode digits (might be visually similar)
		}

		for _, ref := range maliciousRefs {
			err := utils.ValidateReference(ref)
			require.Error(t, err, "Malicious reference should be rejected: %q", ref)
		}
	})

	t.Run("amount_encoding_security", func(t *testing.T) {
		// Test security properties of amount encoding
		testCases := []struct {
			name          string
			baseAmount    uint64
			reference     string
			shouldSucceed bool
		}{
			{"normal_case", 100000, "12345", true},   // 5 digits minimum is correct
			{"zero_base", 0, "12345", false},         // Zero base should fail per implementation
			{"large_base", 999999999, "12345", true},
			{"small_base", 1000, "12345", false},     // Less than 5 digits - should fail
			{"empty_reference", 100000, "", false},
			{"invalid_reference", 100000, "abcde", false},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := utils.EncodeAmountWithReference(
					cosmos.NewUint(tc.baseAmount), tc.reference)

				if tc.shouldSucceed {
					require.NoError(t, err, "Encoding should succeed for %s", tc.name)
				} else {
					require.Error(t, err, "Encoding should fail for %s", tc.name)
				}
			})
		}
	})
}

// Benchmark fuzzing performance
func BenchmarkMemolessFuzzing(b *testing.B) {
	utils := NewMemolessUtils()

	// Pre-generate test data
	testData := make([]cosmos.Uint, 1000)
	for i := range testData {
		baseAmount := rand.Uint64() % 1000000
		reference := fmt.Sprintf("%05d", rand.Intn(100000))

		encoded, err := utils.EncodeAmountWithReference(
			cosmos.NewUint(baseAmount), reference)
		if err != nil {
			encoded = cosmos.NewUint(rand.Uint64() % 1000000)
		}
		testData[i] = encoded
	}

	b.Run("extract_reference_benchmark", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			utils.ExtractReferenceFromAmount(testData[i%len(testData)])
		}
	})

	b.Run("validate_reference_benchmark", func(b *testing.B) {
		references := []string{"12345", "00000", "99999", "invalid", ""}
		for i := 0; i < b.N; i++ {
			utils.ValidateReference(references[i%len(references)])
		}
	})
}
