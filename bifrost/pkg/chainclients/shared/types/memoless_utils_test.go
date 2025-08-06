package types

import (
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestExtractReferenceFromAmount(t *testing.T) {
	tests := []struct {
		name     string
		amount   cosmos.Uint
		expected string
		wantErr  bool
	}{
		{
			name:     "valid reference extraction",
			amount:   cosmos.NewUint(100012345),
			expected: "12345",
			wantErr:  false,
		},
		{
			name:     "minimum valid amount",
			amount:   cosmos.NewUint(100000),
			expected: "00000", // Will be rejected by ValidateReference later
			wantErr:  false,
		},
		{
			name:    "amount too small",
			amount:  cosmos.NewUint(9999),
			wantErr: true,
		},
		{
			name:    "zero amount",
			amount:  cosmos.ZeroUint(),
			wantErr: true,
		},
		{
			name:     "large amount",
			amount:   cosmos.NewUint(999999999912345),
			expected: "12345",
			wantErr:  false,
		},
		{
			name:     "reference with leading zeros",
			amount:   cosmos.NewUint(100000123),
			expected: "00123",
			wantErr:  false,
		},
		{
			name:     "exactly 5 digits",
			amount:   cosmos.NewUint(12345),
			expected: "12345",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			utils := NewMemolessUtils()
			result, err := utils.ExtractReferenceFromAmount(tt.amount)

			if tt.wantErr {
				require.Error(t, err)
				if tt.amount.IsZero() {
					require.Contains(t, err.Error(), "amount cannot be zero", 
						"Error should mention zero amount")
				} else {
					require.Contains(t, err.Error(), "amount too small for memoless reference", 
						"Error should mention amount too small")
				}
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expected, result)
			require.Len(t, result, 5, "Reference should always be 5 characters")
		})
	}
}

func TestIsMemolessTransaction(t *testing.T) {
	tests := []struct {
		name           string
		memo           string
		coins          common.Coins
		expectMemoless bool
		expectedRef    string
		wantErr        bool
	}{
		{
			name:           "valid memoless transaction",
			memo:           "",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
			expectMemoless: true,
			expectedRef:    "12345",
			wantErr:        false,
		},
		{
			name:           "short memo acceptable",
			memo:           "s",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
			expectMemoless: true,
			expectedRef:    "12345",
			wantErr:        false,
		},
		{
			name:           "two character memo acceptable",
			memo:           "sw",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
			expectMemoless: true,
			expectedRef:    "12345",
			wantErr:        false,
		},
		{
			name:           "memo too long",
			memo:           "SWAP:BTC",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
			expectMemoless: false,
			wantErr:        false,
		},
		{
			name:           "multiple coins",
			memo:           "",
			coins: common.Coins{
				common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345)),
				common.NewCoin(common.RuneNative, cosmos.NewUint(1000)),
			},
			expectMemoless: false,
			wantErr:        false,
		},
		{
			name:           "amount too small",
			memo:           "",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(99999))},
			expectMemoless: false,
			wantErr:        false,
		},
		{
			name:           "no coins",
			memo:           "",
			coins:          common.Coins{},
			wantErr:        false,
			expectMemoless: false,
		},
		{
			name:           "whitespace memo",
			memo:           "  ",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
			expectMemoless: true,
			expectedRef:    "12345",
			wantErr:        false,
		},
		{
			name:           "reference all zeros (should still detect but fail validation later)",
			memo:           "",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100000000))},
			expectMemoless: true, // Should be true, validation happens separately
			expectedRef:    "00000",
			wantErr:        false,
		},
		{
			name:           "different asset",
			memo:           "",
			coins:          common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100012345))},
			expectMemoless: true,
			expectedRef:    "12345",
			wantErr:        false,
		},
		{
			name:           "exactly minimum amount",
			memo:           "",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100000))},
			expectMemoless: true, // Should detect as memoless, validation separate
			expectedRef:    "00000",
			wantErr:        false,
		},
		{
			name:           "just above minimum with valid reference",
			memo:           "",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100001))},
			expectMemoless: true,
			expectedRef:    "00001",
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			utils := NewMemolessUtils()
			isMemoless, ref, err := utils.IsMemolessTransaction(tt.memo, tt.coins)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expectMemoless, isMemoless, 
				"Memoless detection result should match expected")
			
			if tt.expectMemoless {
				require.Equal(t, tt.expectedRef, ref, 
					"Reference should match expected value")
				require.Len(t, ref, 5, "Reference should be 5 characters")
				require.Regexp(t, `^\d{5}$`, ref, "Reference should be 5 digits")
			} else {
				require.Empty(t, ref, "Reference should be empty for non-memoless transactions")
			}
		})
	}
}

func TestValidateReference(t *testing.T) {
	tests := []struct {
		name      string
		reference string
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "valid reference",
			reference: "12345",
			wantErr:   false,
		},
		{
			name:      "valid reference with leading zeros",
			reference: "00123",
			wantErr:   false,
		},
		{
			name:      "valid reference all nines",
			reference: "99999",
			wantErr:   false,
		},
		{
			name:      "all zeros - invalid",
			reference: "00000",
			wantErr:   true,
			errMsg:    "reference cannot be all zeros",
		},
		{
			name:      "too short",
			reference: "1234",
			wantErr:   true,
			errMsg:    "reference must be exactly 5 digits",
		},
		{
			name:      "too long",
			reference: "123456",
			wantErr:   true,
			errMsg:    "reference must be exactly 5 digits",
		},
		{
			name:      "non-numeric",
			reference: "1234a",
			wantErr:   true,
			errMsg:    "reference must be numeric",
		},
		{
			name:      "empty",
			reference: "",
			wantErr:   true,
			errMsg:    "reference must be exactly 5 digits",
		},
		{
			name:      "special characters",
			reference: "12-45",
			wantErr:   true,
			errMsg:    "reference must be numeric",
		},
		{
			name:      "spaces",
			reference: "1 345",
			wantErr:   true,
			errMsg:    "reference must be numeric",
		},
		{
			name:      "negative sign",
			reference: "-1234",
			wantErr:   true,
			errMsg:    "reference must be numeric",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			utils := NewMemolessUtils()
			err := utils.ValidateReference(tt.reference)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					require.Contains(t, err.Error(), tt.errMsg, 
						"Error message should contain expected text")
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestEncodeAmountWithReference(t *testing.T) {
	tests := []struct {
		name       string
		baseAmount cosmos.Uint
		reference  string
		expected   cosmos.Uint
		wantErr    bool
		errMsg     string
	}{
		{
			name:       "encode with valid reference",
			baseAmount: cosmos.NewUint(1000),
			reference:  "12345",
			expected:   cosmos.NewUint(100012345),
			wantErr:    false,
		},
		{
			name:       "replace existing reference",
			baseAmount: cosmos.NewUint(100099999),
			reference:  "12345",
			expected:   cosmos.NewUint(100012345),
			wantErr:    false,
		},
		{
			name:       "small base amount",
			baseAmount: cosmos.NewUint(1),
			reference:  "12345",
			expected:   cosmos.NewUint(112345),
			wantErr:    false,
		},
		{
			name:       "large base amount",
			baseAmount: cosmos.NewUint(999999999900000),
			reference:  "12345",
			expected:   cosmos.NewUint(999999999912345),
			wantErr:    false,
		},
		{
			name:       "base amount exactly 5 digits",
			baseAmount: cosmos.NewUint(12345),
			reference:  "67890",
			expected:   cosmos.NewUint(67890), // All base digits removed, only reference remains
			wantErr:    false,
		},
		{
			name:       "zero base amount",
			baseAmount: cosmos.ZeroUint(),
			reference:  "12345",
			wantErr:    true,
			errMsg:     "base amount cannot be zero",
		},
		{
			name:       "invalid reference - all zeros",
			baseAmount: cosmos.NewUint(1000),
			reference:  "00000",
			wantErr:    true,
			errMsg:     "invalid reference",
		},
		{
			name:       "invalid reference - too short",
			baseAmount: cosmos.NewUint(1000),
			reference:  "1234",
			wantErr:    true,
			errMsg:     "invalid reference",
		},
		{
			name:       "invalid reference - too long",
			baseAmount: cosmos.NewUint(1000),
			reference:  "123456",
			wantErr:    true,
			errMsg:     "invalid reference",
		},
		{
			name:       "invalid reference - non-numeric",
			baseAmount: cosmos.NewUint(1000),
			reference:  "1234a",
			wantErr:    true,
			errMsg:     "invalid reference",
		},
		{
			name:       "reference with leading zeros",
			baseAmount: cosmos.NewUint(1000),
			reference:  "00123",
			expected:   cosmos.NewUint(100000123),
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			utils := NewMemolessUtils()
			result, err := utils.EncodeAmountWithReference(tt.baseAmount, tt.reference)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					require.Contains(t, err.Error(), tt.errMsg,
						"Error message should contain expected text")
				}
				require.True(t, result.IsZero(), 
					"Result should be zero on error")
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expected, result)
			
			// Verify we can extract the reference back
			extractedRef, err := utils.ExtractReferenceFromAmount(result)
			require.NoError(t, err)
			require.Equal(t, tt.reference, extractedRef,
				"Should be able to extract the same reference back")
		})
	}
}

func TestGetMinimumMemolessAmount(t *testing.T) {
	utils := NewMemolessUtils()
	minAmount := utils.GetMinimumMemolessAmount()
	
	require.Equal(t, cosmos.NewUint(100000), minAmount,
		"Minimum amount should be 100,000 base units")
	require.False(t, minAmount.IsZero(), 
		"Minimum amount should not be zero")
	require.True(t, minAmount.GT(cosmos.NewUint(99999)),
		"Minimum amount should be greater than 99,999")
}

func TestMemolessUtilsIntegration(t *testing.T) {
	// Test the complete flow of memoless utilities working together
	utils := NewMemolessUtils()
	
	// Test data
	baseAmount := cosmos.NewUint(1500000)
	reference := "54321"
	
	// Step 1: Encode amount with reference
	encodedAmount, err := utils.EncodeAmountWithReference(baseAmount, reference)
	require.NoError(t, err)
	
	// Step 2: Create coins for transaction
	coins := common.Coins{common.NewCoin(common.BTCAsset, encodedAmount)}
	
	// Step 3: Detect memoless transaction
	isMemoless, extractedRef, err := utils.IsMemolessTransaction("", coins)
	require.NoError(t, err)
	require.True(t, isMemoless, "Should detect as memoless transaction")
	require.Equal(t, reference, extractedRef, "Should extract correct reference")
	
	// Step 4: Validate the reference
	err = utils.ValidateReference(extractedRef)
	require.NoError(t, err, "Reference should be valid")
	
	// Step 5: Extract reference directly from amount
	directRef, err := utils.ExtractReferenceFromAmount(encodedAmount)
	require.NoError(t, err)
	require.Equal(t, reference, directRef, "Direct extraction should match")
	
	// Verify the full cycle works
	t.Logf("Integration test successful:")
	t.Logf("  Base amount: %s", baseAmount.String())
	t.Logf("  Reference: %s", reference) 
	t.Logf("  Encoded amount: %s", encodedAmount.String())
	t.Logf("  Extracted reference: %s", extractedRef)
}

func TestBoundaryConditions(t *testing.T) {
	utils := NewMemolessUtils()
	
	tests := []struct {
		name        string
		memo        string
		amount      cosmos.Uint
		expectMemoless bool
		description string
	}{
		{
			name:        "minimum valid amount",
			memo:        "",
			amount:      cosmos.NewUint(100000), // Exactly minimum
			expectMemoless: true, // Should detect as memoless, validation separate
			description: "Amount at minimum threshold with invalid reference",
		},
		{
			name:        "just above minimum",
			memo:        "",
			amount:      cosmos.NewUint(100001),
			expectMemoless: true,
			description: "Just above minimum with valid reference",
		},
		{
			name:        "maximum memo length",
			memo:        "ab", // Exactly 2 characters (max for memoless)
			amount:      cosmos.NewUint(100012345),
			expectMemoless: true,
			description: "Maximum allowed memo length for memoless",
		},
		{
			name:        "memo too long by one char",
			memo:        "abc", // 3 characters (too long)
			amount:      cosmos.NewUint(100012345),
			expectMemoless: false,
			description: "Memo one character too long",
		},
		{
			name:        "very large amount",
			memo:        "",
			amount:      cosmos.NewUintFromString("999999999999999999999999999912345"),
			expectMemoless: true,
			description: "Very large amount with valid reference",
		},
		{
			name:        "amount with leading zeros in reference",
			memo:        "",
			amount:      cosmos.NewUint(100000123), // Reference: "00123"
			expectMemoless: true,
			description: "Reference with leading zeros",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coins := common.Coins{common.NewCoin(common.BTCAsset, tt.amount)}
			isMemoless, _, err := utils.IsMemolessTransaction(tt.memo, coins)
			
			require.NoError(t, err)
			require.Equal(t, tt.expectMemoless, isMemoless, tt.description)
		})
	}
}

func TestEdgeCasesAndErrorHandling(t *testing.T) {
	utils := NewMemolessUtils()
	
	t.Run("nil coins slice", func(t *testing.T) {
		isMemoless, ref, err := utils.IsMemolessTransaction("", nil)
		require.NoError(t, err)
		require.False(t, isMemoless)
		require.Empty(t, ref)
	})
	
	t.Run("empty coins slice", func(t *testing.T) {
		isMemoless, ref, err := utils.IsMemolessTransaction("", common.Coins{})
		require.NoError(t, err)
		require.False(t, isMemoless)
		require.Empty(t, ref)
	})
	
	t.Run("very long memo", func(t *testing.T) {
		longMemo := string(make([]byte, 1000))
		for i := range longMemo {
			longMemo = longMemo[:i] + "a" + longMemo[i+1:]
		}
		coins := common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))}
		
		isMemoless, ref, err := utils.IsMemolessTransaction(longMemo, coins)
		require.NoError(t, err)
		require.False(t, isMemoless, "Very long memo should not be memoless")
		require.Empty(t, ref)
	})
	
	t.Run("unicode in memo", func(t *testing.T) {
		unicodeMemo := "🚀💎"
		coins := common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))}
		
		isMemoless, ref, err := utils.IsMemolessTransaction(unicodeMemo, coins)
		require.NoError(t, err)
		require.False(t, isMemoless, "Unicode memo should not be memoless")
		require.Empty(t, ref)
	})
}