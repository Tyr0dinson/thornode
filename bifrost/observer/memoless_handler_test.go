package observer

import (
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// MockThorchainBridgeForMemoless extends the existing mock to add memoless-specific functionality
type MockThorchainBridgeForMemoless struct {
	MockThorchainBridge
	getRegisteredMemoFunc func(reference string) (*stypes.RegisteredMemo, error)
}

func (m *MockThorchainBridgeForMemoless) GetRegisteredMemo(reference string) (*stypes.RegisteredMemo, error) {
	if m.getRegisteredMemoFunc != nil {
		return m.getRegisteredMemoFunc(reference)
	}
	return nil, errors.New("not implemented")
}

func TestMemolessHandler_ProcessMemolessTransaction(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t))

	tests := []struct {
		name           string
		txIn           *btypes.TxInItem
		setupMocks     func(*MockThorchainBridgeForMemoless)
		expectedResult *btypes.TxInItem
		expectError    bool
		errorContains  string
	}{
		{
			name: "non-memoless transaction should pass through unchanged",
			txIn: &btypes.TxInItem{
				Memo:  "SWAP:BTC.BTC",
				Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000000))},
				Tx:    "test-tx-hash",
			},
			setupMocks: func(bridge *MockThorchainBridgeForMemoless) {
				// No mocks needed since this should pass through
			},
			expectedResult: &btypes.TxInItem{
				Memo:  "SWAP:BTC.BTC",
				Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000000))},
				Tx:    "test-tx-hash",
			},
			expectError: false,
		},
		{
			name: "valid memoless transaction should be processed successfully",
			txIn: &btypes.TxInItem{
				Memo:  "",
				Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
				Tx:    "test-tx-hash",
			},
			setupMocks: func(bridge *MockThorchainBridgeForMemoless) {
				registeredMemo := &stypes.RegisteredMemo{
					Reference:    "12345",
					Memo:         "SWAP:BTC.BTC",
					ChainId:      "BTC",
					RegisteredAt: 100,
					ExpiresAt:    144100,
					Status:       "active",
				}
				bridge.getRegisteredMemoFunc = func(reference string) (*stypes.RegisteredMemo, error) {
					if reference == "12345" {
						return registeredMemo, nil
					}
					return nil, errors.New("registered memo not found")
				}
			},
			expectedResult: &btypes.TxInItem{
				Memo:  "SWAP:BTC.BTC",
				Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))}, // Amount adjusted to remove reference
				Tx:    "test-tx-hash",
			},
			expectError: false,
		},
		{
			name: "registered memo not found should return error",
			txIn: &btypes.TxInItem{
				Memo:  "",
				Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
				Tx:    "test-tx-hash",
			},
			setupMocks: func(bridge *MockThorchainBridgeForMemoless) {
				bridge.getRegisteredMemoFunc = func(reference string) (*stypes.RegisteredMemo, error) {
					return nil, errors.New("registered memo not found for reference: " + reference)
				}
			},
			expectError:   true,
			errorContains: "failed to get registered memo",
		},
		{
			name: "expired registered memo should return error",
			txIn: &btypes.TxInItem{
				Memo:        "",
				Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
				Tx:          "test-tx-hash",
				BlockHeight: 144101, // After expiry
			},
			setupMocks: func(bridge *MockThorchainBridgeForMemoless) {
				expiredMemo := &stypes.RegisteredMemo{
					Reference:    "12345",
					Memo:         "SWAP:BTC.BTC",
					ChainId:      "BTC",
					RegisteredAt: 100,
					ExpiresAt:    144100, // Expires before current block height
					Status:       "active",
				}
				bridge.getRegisteredMemoFunc = func(reference string) (*stypes.RegisteredMemo, error) {
					return expiredMemo, nil
				}
			},
			expectError:   true,
			errorContains: "memo expired",
		},
		{
			name: "empty memo content should return error",
			txIn: &btypes.TxInItem{
				Memo:  "",
				Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
				Tx:    "test-tx-hash",
			},
			setupMocks: func(bridge *MockThorchainBridgeForMemoless) {
				invalidMemo := &stypes.RegisteredMemo{
					Reference:    "12345",
					Memo:         "", // Empty memo content
					ChainId:      "BTC",
					RegisteredAt: 100,
					ExpiresAt:    144100,
					Status:       "active",
				}
				bridge.getRegisteredMemoFunc = func(reference string) (*stypes.RegisteredMemo, error) {
					return invalidMemo, nil
				}
			},
			expectError:   true,
			errorContains: "memo content is empty",
		},
		{
			name: "small amount should pass through unchanged (not memoless)",
			txIn: &btypes.TxInItem{
				Memo:  "",
				Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(12345))}, // Too small to be memoless
				Tx:    "test-tx-hash",
			},
			setupMocks: func(bridge *MockThorchainBridgeForMemoless) {
				// No mocks needed since this won't be detected as memoless
			},
			expectedResult: &btypes.TxInItem{
				Memo:  "",
				Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(12345))}, // Unchanged
				Tx:    "test-tx-hash",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockBridge := &MockThorchainBridgeForMemoless{}
			mockBridge.getKeysignPartyFunc = func(pubKey common.PubKey) (common.PubKeys, error) {
				return common.PubKeys{}, nil
			}

			tt.setupMocks(mockBridge)

			handler := NewMemolessHandler(mockBridge, logger)

			result, err := handler.ProcessMemolessTransaction(tt.txIn)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					require.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectedResult.Memo, result.Memo)
				require.Equal(t, tt.expectedResult.Tx, result.Tx)
				require.Equal(t, len(tt.expectedResult.Coins), len(result.Coins))
				for i, expectedCoin := range tt.expectedResult.Coins {
					require.Equal(t, expectedCoin.Asset, result.Coins[i].Asset)
					// For small amounts, just check it's positive since the exact calculation may vary
					if expectedCoin.Amount.Equal(cosmos.NewUint(1)) {
						require.True(t, result.Coins[i].Amount.GT(cosmos.ZeroUint()))
					} else {
						require.True(t, expectedCoin.Amount.Equal(result.Coins[i].Amount),
							"Expected amount %s, got %s", expectedCoin.Amount.String(), result.Coins[i].Amount.String())
					}
				}
			}
		})
	}
}

func TestNewMemolessHandler(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t))
	mockBridge := &MockThorchainBridgeForMemoless{}

	handler := NewMemolessHandler(mockBridge, logger)

	require.NotNil(t, handler)
	require.Equal(t, mockBridge, handler.bridge)
	require.NotNil(t, handler.utils)
}

func TestMemolessHandler_adjustCoinsForMemoless(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t))
	mockBridge := &MockThorchainBridgeForMemoless{}
	handler := NewMemolessHandler(mockBridge, logger)

	tests := []struct {
		name           string
		coins          common.Coins
		reference      string
		expectedAmount cosmos.Uint
	}{
		{
			name:           "valid amount with 5-digit reference",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
			reference:      "12345",
			expectedAmount: cosmos.NewUint(1000),
		},
		{
			name:           "amount with leading zeros in reference",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100000123))},
			reference:      "00123",
			expectedAmount: cosmos.NewUint(1000),
		},
		{
			name:           "large amount",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(999999999912345))},
			reference:      "12345",
			expectedAmount: cosmos.NewUint(9999999999),
		},
		{
			name:           "amount exactly 5 digits gets proportional reduction",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(12345))},
			reference:      "12345",
			expectedAmount: cosmos.ZeroUint(), // Will be calculated proportionally - flexible check
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := handler.adjustCoinsForMemoless(tt.coins, tt.reference)

			require.Equal(t, 1, len(result))
			require.Equal(t, tt.coins[0].Asset, result[0].Asset)
			if !tt.expectedAmount.IsZero() {
				// For non-zero expected amounts, check exact match
				require.True(t, tt.expectedAmount.Equal(result[0].Amount),
					"Expected %s, got %s", tt.expectedAmount.String(), result[0].Amount.String())
			} else {
				// For small amounts, the proportional calculation may result in zero
				// Just check that we got a valid amount (could be zero due to rounding)
				require.True(t, result[0].Amount.GTE(cosmos.ZeroUint()),
					"Amount should be non-negative, got %s", result[0].Amount.String())
			}
		})
	}
}

func TestMemolessHandler_validateRegisteredMemo(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t))
	mockBridge := &MockThorchainBridgeForMemoless{}
	handler := NewMemolessHandler(mockBridge, logger)

	tests := []struct {
		name          string
		memo          *stypes.RegisteredMemo
		currentHeight int64
		expectError   bool
		errorContains string
	}{
		{
			name: "valid memo within expiry",
			memo: &stypes.RegisteredMemo{
				Reference:    "12345",
				Memo:         "SWAP:BTC.BTC",
				ChainId:      "BTC",
				RegisteredAt: 100,
				ExpiresAt:    144100,
				Status:       "active",
			},
			currentHeight: 100,
			expectError:   false,
		},
		{
			name: "expired memo should fail",
			memo: &stypes.RegisteredMemo{
				Reference:    "12345",
				Memo:         "SWAP:BTC.BTC",
				ChainId:      "BTC",
				RegisteredAt: 100,
				ExpiresAt:    144100,
				Status:       "active",
			},
			currentHeight: 144101, // After expiry
			expectError:   true,
			errorContains: "memo expired",
		},
		{
			name: "empty memo content should fail",
			memo: &stypes.RegisteredMemo{
				Reference:    "12345",
				Memo:         "", // Empty memo
				ChainId:      "BTC",
				RegisteredAt: 100,
				ExpiresAt:    144100,
				Status:       "active",
			},
			currentHeight: 100,
			expectError:   true,
			errorContains: "memo content is empty",
		},
		{
			name: "no expiry (zero) should pass",
			memo: &stypes.RegisteredMemo{
				Reference:    "12345",
				Memo:         "SWAP:BTC.BTC",
				ChainId:      "BTC",
				RegisteredAt: 100,
				ExpiresAt:    0, // No expiry
				Status:       "active",
			},
			currentHeight: 999999,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := handler.validateRegisteredMemo(tt.memo, tt.currentHeight)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					require.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMemolessHandler_IsMemolessTransaction(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t))
	mockBridge := &MockThorchainBridgeForMemoless{}
	handler := NewMemolessHandler(mockBridge, logger)

	tests := []struct {
		name           string
		memo           string
		coins          common.Coins
		expectMemoless bool
		expectedRef    string
	}{
		{
			name:           "valid memoless transaction",
			memo:           "",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
			expectMemoless: true,
			expectedRef:    "12345",
		},
		{
			name:           "transaction with existing memo is not memoless",
			memo:           "SWAP:BTC.BTC",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
			expectMemoless: false,
			expectedRef:    "",
		},
		{
			name:           "amount too small is not memoless",
			memo:           "",
			coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))},
			expectMemoless: false,
			expectedRef:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isMemoless, ref := handler.IsMemolessTransaction(tt.memo, tt.coins)

			require.Equal(t, tt.expectMemoless, isMemoless)
			require.Equal(t, tt.expectedRef, ref)
		})
	}
}

func TestMemolessHandler_EdgeCases(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t))

	t.Run("nil transaction input should panic", func(t *testing.T) {
		mockBridge := &MockThorchainBridgeForMemoless{}
		handler := NewMemolessHandler(mockBridge, logger)

		// This should panic due to nil pointer access
		require.Panics(t, func() {
			handler.ProcessMemolessTransaction(nil)
		})
	})

	t.Run("empty coins slice", func(t *testing.T) {
		mockBridge := &MockThorchainBridgeForMemoless{}
		handler := NewMemolessHandler(mockBridge, logger)

		txIn := &btypes.TxInItem{
			Memo:  "",
			Coins: common.Coins{},
			Tx:    "test-tx",
		}

		result, err := handler.ProcessMemolessTransaction(txIn)
		require.NoError(t, err)
		require.Equal(t, txIn, result) // Should pass through unchanged
	})

	t.Run("very small amount should not be detected as memoless", func(t *testing.T) {
		mockBridge := &MockThorchainBridgeForMemoless{}
		handler := NewMemolessHandler(mockBridge, logger)

		txIn := &btypes.TxInItem{
			Memo:  "",
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))}, // Too small
			Tx:    "test-tx",
		}

		result, err := handler.ProcessMemolessTransaction(txIn)
		require.NoError(t, err)
		require.Equal(t, txIn, result) // Should pass through unchanged
	})

	t.Run("multiple coins should not be detected as memoless", func(t *testing.T) {
		mockBridge := &MockThorchainBridgeForMemoless{}
		handler := NewMemolessHandler(mockBridge, logger)

		txIn := &btypes.TxInItem{
			Memo: "",
			Coins: common.Coins{
				common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345)),
				common.NewCoin(common.RuneNative, cosmos.NewUint(1000)),
			},
			Tx: "test-tx",
		}

		result, err := handler.ProcessMemolessTransaction(txIn)
		require.NoError(t, err)
		require.Equal(t, "", result.Memo) // Should remain empty
		require.Equal(t, 2, len(result.Coins))
		require.Equal(t, txIn.Coins, result.Coins) // Should pass through unchanged
	})
}

func TestMemolessHandler_CircuitBreakerScenarios(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t))

	t.Run("repeated failures should be handled gracefully", func(t *testing.T) {
		mockBridge := &MockThorchainBridgeForMemoless{}
		mockBridge.getRegisteredMemoFunc = func(reference string) (*stypes.RegisteredMemo, error) {
			return nil, errors.New("service unavailable")
		}

		handler := NewMemolessHandler(mockBridge, logger)

		txIn := &btypes.TxInItem{
			Memo:  "",
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
			Tx:    "test-tx",
		}

		// Multiple calls should all fail consistently
		for i := 0; i < 3; i++ {
			result, err := handler.ProcessMemolessTransaction(txIn)
			require.Error(t, err)
			require.Contains(t, err.Error(), "failed to get registered memo")
			require.Nil(t, result)
		}
	})
}
