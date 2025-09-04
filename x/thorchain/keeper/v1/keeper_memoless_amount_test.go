package keeperv1

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

// KeeperAmountSuite tests amount-based keeper functions
type KeeperAmountSuite struct{}

var _ = Suite(&KeeperAmountSuite{})

func TestKeeperAmount(t *testing.T) { TestingT(t) }

// TestValidateUserReference tests user reference validation with Genesis State checking
func (s *KeeperAmountSuite) TestValidateUserReference(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test 1: Valid unused reference
	err := k.ValidateUserReference(ctx, "12345")
	c.Assert(err, IsNil, Commentf("Valid unused reference should pass"))

	// Test 2: Invalid format should fail
	err = k.ValidateUserReference(ctx, "1234") // Too short
	c.Assert(err, NotNil, Commentf("Invalid format should fail"))

	// Test 3: Invalid range should fail
	err = k.ValidateUserReference(ctx, "09999") // Below minimum
	c.Assert(err, NotNil, Commentf("Invalid range should fail"))

	// Test 4: Valid format in valid range should pass (when available)
	err = k.ValidateUserReference(ctx, "54321")
	c.Assert(err, IsNil, Commentf("Valid available reference should pass"))

	// Test 5: Reference collision detection (requires mock registered memo)
	// Note: This would require setting up a registered memo to test collision detection
}

// TestExtractReferenceFromAmount tests stateless reference extraction
func (s *KeeperAmountSuite) TestExtractReferenceFromAmount(c *C) {
	_, k := setupKeeperForTest(c)

	testCases := []struct {
		name      string
		amount    cosmos.Uint
		expected  string
		shouldErr bool
	}{
		{
			name:      "valid_5_digit_amount",
			amount:    cosmos.NewUint(12345),
			expected:  "12345",
			shouldErr: false,
		},
		{
			name:      "valid_large_amount",
			amount:    cosmos.NewUint(100012345),
			expected:  "12345",
			shouldErr: false,
		},
		{
			name:      "amount_too_small",
			amount:    cosmos.NewUint(1234),
			expected:  "",
			shouldErr: true,
		},
		{
			name:      "valid_minimum_reference",
			amount:    cosmos.NewUint(10000),
			expected:  "10000",
			shouldErr: false,
		},
		{
			name:      "valid_maximum_reference",
			amount:    cosmos.NewUint(99999),
			expected:  "99999",
			shouldErr: false,
		},
		{
			name:      "invalid_reference_below_range",
			amount:    cosmos.NewUint(9999),
			expected:  "",
			shouldErr: true,
		},
	}

	for _, tc := range testCases {
		result, err := k.ExtractReferenceFromAmount(tc.amount)

		if tc.shouldErr {
			c.Assert(err, NotNil, Commentf("Test %s: expected error but got none", tc.name))
		} else {
			c.Assert(err, IsNil, Commentf("Test %s: unexpected error: %v", tc.name, err))
			c.Assert(result, Equals, tc.expected, Commentf("Test %s: got %s, expected %s", tc.name, result, tc.expected))
		}
	}
}

// TestEncodeAmountWithReference tests amount encoding with reference
func (s *KeeperAmountSuite) TestEncodeAmountWithReference(c *C) {
	_, k := setupKeeperForTest(c)

	testCases := []struct {
		name       string
		baseAmount cosmos.Uint
		reference  string
		expected   cosmos.Uint
		shouldErr  bool
	}{
		{
			name:       "encode_simple_amount",
			baseAmount: cosmos.NewUint(100000000), // 1.0 BTC in satoshis
			reference:  "12345",
			expected:   cosmos.NewUint(100012345),
			shouldErr:  false,
		},
		{
			name:       "encode_large_amount",
			baseAmount: cosmos.NewUint(1000000000000000000), // 1.0 ETH in wei
			reference:  "54321",
			expected:   cosmos.NewUint(1000000000000054321),
			shouldErr:  false,
		},
		{
			name:       "base_amount_too_small",
			baseAmount: cosmos.NewUint(1234), // <5 digits
			reference:  "12345",
			expected:   cosmos.ZeroUint(),
			shouldErr:  true,
		},
		{
			name:       "invalid_reference_format",
			baseAmount: cosmos.NewUint(100000000),
			reference:  "1234", // 4 digits
			expected:   cosmos.ZeroUint(),
			shouldErr:  true,
		},
		{
			name:       "invalid_reference_range",
			baseAmount: cosmos.NewUint(100000000),
			reference:  "09999", // Below minimum
			expected:   cosmos.ZeroUint(),
			shouldErr:  true,
		},
	}

	for _, tc := range testCases {
		result, err := k.EncodeAmountWithReference(tc.baseAmount, tc.reference)

		if tc.shouldErr {
			c.Assert(err, NotNil, Commentf("Test %s: expected error but got none", tc.name))
		} else {
			c.Assert(err, IsNil, Commentf("Test %s: unexpected error: %v", tc.name, err))
			c.Assert(result.Equal(tc.expected), Equals, true,
				Commentf("Test %s: got %s, expected %s", tc.name, result.String(), tc.expected.String()))
		}
	}
}

// TestHasActiveRegisteredMemo tests collision detection
func (s *KeeperAmountSuite) TestHasActiveRegisteredMemo(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test 1: Non-existent reference should return false
	exists := k.HasActiveRegisteredMemo(ctx, "12345")
	c.Assert(exists, Equals, false, Commentf("Non-existent reference should return false"))

	// Test 2: Test would require actually registering a memo to verify collision detection
	// This depends on the RegisteredMemo storage functions being available
}
