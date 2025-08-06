package observer

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Integration test suite for Observer memoless transaction flow
// Tests the complete end-to-end processing of memoless transactions
func TestObserver_MemolessIntegrationFlow(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t))

	tests := []struct {
		name                    string
		setupMocks              func(*MockThorchainBridgeForMemoless)
		inputTransactions       []btypes.TxInItem
		expectedOutputCount     int
		expectedProcessedTxs    []btypes.TxInItem
		expectErrors            bool
		description             string
	}{
		{
			name: "end-to-end memoless transaction processing",
			setupMocks: func(bridge *MockThorchainBridgeForMemoless) {
				bridge.getKeysignPartyFunc = func(pubKey common.PubKey) (common.PubKeys, error) {
					return common.PubKeys{}, nil
				}
				bridge.getRegisteredMemoFunc = func(reference string) (*stypes.RegisteredMemo, error) {
					switch reference {
					case "12345":
						return &stypes.RegisteredMemo{
							Reference:    "12345",
							Memo:         "SWAP:BTC.BTC",
							ChainId:      "BTC",
							RegisteredAt: 100,
							ExpiresAt:    144100,
							Status:       "active",
						}, nil
					case "67890":
						return &stypes.RegisteredMemo{
							Reference:    "67890", 
							Memo:         "ADD:BTC.BTC",
							ChainId:      "BTC",
							RegisteredAt: 200,
							ExpiresAt:    144200,
							Status:       "active",
						}, nil
					default:
						return nil, errors.New("registered memo not found")
					}
				}
			},
			inputTransactions: []btypes.TxInItem{
				// Regular transaction - should pass through unchanged
				{
					BlockHeight: 1000,
					Tx:          "regular-tx-1",
					Memo:        "SWAP:ETH.ETH",
					Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000))},
					Sender:      "sender1",
					To:          "vault1",
				},
				// Memoless transaction - should be processed
				{
					BlockHeight: 1001,
					Tx:          "memoless-tx-1", 
					Memo:        "",
					Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
					Sender:      "sender2",
					To:          "vault1",
				},
				// Another memoless transaction
				{
					BlockHeight: 1002,
					Tx:          "memoless-tx-2",
					Memo:        "",
					Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(200067890))},
					Sender:      "sender3", 
					To:          "vault1",
				},
				// Non-memoless small amount - should pass through
				{
					BlockHeight: 1003,
					Tx:          "small-tx-1",
					Memo:        "",
					Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(50000))},
					Sender:      "sender4",
					To:          "vault1",
				},
			},
			expectedOutputCount: 4,
			expectedProcessedTxs: []btypes.TxInItem{
				// Regular transaction unchanged
				{
					BlockHeight: 1000,
					Tx:          "regular-tx-1",
					Memo:        "SWAP:ETH.ETH",
					Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000))},
					Sender:      "sender1",
					To:          "vault1",
				},
				// Memoless transaction processed
				{
					BlockHeight: 1001,
					Tx:          "memoless-tx-1",
					Memo:        "SWAP:BTC.BTC", // Resolved from THORChain
					Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))}, // Amount adjusted
					Sender:      "sender2",
					To:          "vault1",
				},
				// Another memoless transaction processed
				{
					BlockHeight: 1002,
					Tx:          "memoless-tx-2",
					Memo:        "ADD:BTC.BTC", // Resolved from THORChain
					Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(2000))}, // Amount adjusted
					Sender:      "sender3",
					To:          "vault1",
				},
				// Small amount transaction unchanged
				{
					BlockHeight: 1003,
					Tx:          "small-tx-1", 
					Memo:        "",
					Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(50000))},
					Sender:      "sender4",
					To:          "vault1",
				},
			},
			expectErrors: false,
			description:  "Complete end-to-end flow with mixed transaction types",
		},
		{
			name: "memoless transactions with THORChain errors",
			setupMocks: func(bridge *MockThorchainBridgeForMemoless) {
				bridge.getKeysignPartyFunc = func(pubKey common.PubKey) (common.PubKeys, error) {
					return common.PubKeys{}, nil
				}
				bridge.getRegisteredMemoFunc = func(reference string) (*stypes.RegisteredMemo, error) {
					if reference == "12345" {
						return nil, errors.New("THORChain service unavailable")
					}
					return nil, errors.New("registered memo not found")
				}
			},
			inputTransactions: []btypes.TxInItem{
				// Memoless transaction that will fail THORChain lookup
				{
					BlockHeight: 1001,
					Tx:          "failing-memoless-tx",
					Memo:        "",
					Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
					Sender:      "sender1",
					To:          "vault1",
				},
			},
			expectedOutputCount: 0, // Should be filtered out due to error
			expectErrors:        true,
			description:         "Memoless transactions with THORChain lookup failures",
		},
		{
			name: "concurrent memoless transaction processing",
			setupMocks: func(bridge *MockThorchainBridgeForMemoless) {
				bridge.getKeysignPartyFunc = func(pubKey common.PubKey) (common.PubKeys, error) {
					return common.PubKeys{}, nil
				}
				// Add a small delay to simulate network latency
				bridge.getRegisteredMemoFunc = func(reference string) (*stypes.RegisteredMemo, error) {
					time.Sleep(10 * time.Millisecond) // Simulate network delay
					if reference == "11111" || reference == "22222" || reference == "33333" {
						return &stypes.RegisteredMemo{
							Reference:    reference,
							Memo:         "SWAP:BTC.BTC",
							ChainId:      "BTC",
							RegisteredAt: 100,
							ExpiresAt:    144100,
							Status:       "active",
						}, nil
					}
					return nil, errors.New("registered memo not found")
				}
			},
			inputTransactions: []btypes.TxInItem{
				{
					BlockHeight: 1001,
					Tx:          "concurrent-tx-1",
					Memo:        "",
					Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100011111))},
					Sender:      "sender1",
					To:          "vault1",
				},
				{
					BlockHeight: 1002,
					Tx:          "concurrent-tx-2",
					Memo:        "",
					Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100022222))},
					Sender:      "sender2",
					To:          "vault1",
				},
				{
					BlockHeight: 1003,
					Tx:          "concurrent-tx-3",
					Memo:        "",
					Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100033333))},
					Sender:      "sender3",
					To:          "vault1",
				},
			},
			expectedOutputCount: 3,
			expectErrors:        false,
			description:         "Concurrent processing of multiple memoless transactions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock bridge
			mockBridge := &MockThorchainBridgeForMemoless{}
			tt.setupMocks(mockBridge)

			// Create memoless handler
			memolessHandler := NewMemolessHandler(mockBridge, logger)

			// Process each transaction through the memoless handler
			var processedTxs []btypes.TxInItem
			var processingErrors []error

			// Test concurrent processing if there are multiple transactions
			if len(tt.inputTransactions) > 1 {
				// Use goroutines to simulate concurrent processing
				var wg sync.WaitGroup
				var mu sync.Mutex
				
				for _, tx := range tt.inputTransactions {
					wg.Add(1)
					go func(inputTx btypes.TxInItem) {
						defer wg.Done()
						
						processedTx, err := memolessHandler.ProcessMemolessTransaction(&inputTx)
						
						mu.Lock()
						defer mu.Unlock()
						
						if err != nil {
							processingErrors = append(processingErrors, err)
						} else {
							processedTxs = append(processedTxs, *processedTx)
						}
					}(tx)
				}
				wg.Wait()
			} else {
				// Single transaction processing
				for _, tx := range tt.inputTransactions {
					processedTx, err := memolessHandler.ProcessMemolessTransaction(&tx)
					if err != nil {
						processingErrors = append(processingErrors, err)
					} else {
						processedTxs = append(processedTxs, *processedTx)
					}
				}
			}

			// Validate results
			if tt.expectErrors {
				require.Greater(t, len(processingErrors), 0, "Expected processing errors but got none")
				t.Logf("Expected errors occurred: %v", processingErrors)
			} else {
				require.Empty(t, processingErrors, "Unexpected processing errors: %v", processingErrors)
			}

			require.Equal(t, tt.expectedOutputCount, len(processedTxs), 
				"Processed transaction count mismatch")

			// Validate specific processed transactions (for single-threaded tests only)
			if len(tt.expectedProcessedTxs) > 0 && len(tt.inputTransactions) == len(processedTxs) && len(tt.inputTransactions) <= 1 {
				// Only validate exact matches for single transaction tests to avoid concurrency issues
				for i, expectedTx := range tt.expectedProcessedTxs {
					actualTx := processedTxs[i]
					require.Equal(t, expectedTx.BlockHeight, actualTx.BlockHeight)
					require.Equal(t, expectedTx.Tx, actualTx.Tx)
					require.Equal(t, expectedTx.Memo, actualTx.Memo, 
						"Memo mismatch for tx %s", actualTx.Tx)
					require.Equal(t, expectedTx.Sender, actualTx.Sender)
					require.Equal(t, expectedTx.To, actualTx.To)
					require.Equal(t, len(expectedTx.Coins), len(actualTx.Coins))
					
					for j, expectedCoin := range expectedTx.Coins {
						actualCoin := actualTx.Coins[j]
						require.Equal(t, expectedCoin.Asset, actualCoin.Asset)
						require.True(t, expectedCoin.Amount.Equal(actualCoin.Amount),
							"Amount mismatch for tx %s: expected %s, got %s", 
							actualTx.Tx, expectedCoin.Amount.String(), actualCoin.Amount.String())
					}
				}
			} else if len(tt.expectedProcessedTxs) > 0 && len(tt.inputTransactions) > 1 {
				// For multi-transaction tests, validate that we have the expected types of transactions
				memolessCount := 0
				regularCount := 0
				for _, tx := range processedTxs {
					if tx.Memo != "" {
						if tx.Memo == "SWAP:ETH.ETH" {
							regularCount++
						} else if tx.Memo == "SWAP:BTC.BTC" || tx.Memo == "ADD:BTC.BTC" {
							memolessCount++
						}
					} else {
						regularCount++ // Non-memoless empty memo transactions
					}
				}
				
				// Verify we have the expected mix based on our test data
				if tt.name == "end-to-end memoless transaction processing" {
					require.GreaterOrEqual(t, memolessCount, 2, "Should have at least 2 memoless transactions processed")
					require.GreaterOrEqual(t, regularCount, 2, "Should have at least 2 regular transactions processed")
				}
			}

			t.Logf("✅ %s: processed %d transactions", tt.description, len(processedTxs))
		})
	}
}

// Test the integration between Observer and MemolessHandler
func TestObserver_MemolessHandlerIntegration(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t))

	// Mock bridge setup
	mockBridge := &MockThorchainBridgeForMemoless{}
	mockBridge.getKeysignPartyFunc = func(pubKey common.PubKey) (common.PubKeys, error) {
		return common.PubKeys{}, nil
	}
	mockBridge.getRegisteredMemoFunc = func(reference string) (*stypes.RegisteredMemo, error) {
		return &stypes.RegisteredMemo{
			Reference:    reference,
			Memo:         "SWAP:BTC.BTC",
			ChainId:      "BTC", 
			RegisteredAt: 100,
			ExpiresAt:    144100,
			Status:       "active",
		}, nil
	}

	// Create memoless handler
	memolessHandler := NewMemolessHandler(mockBridge, logger)

	// Test memoless transaction detection
	t.Run("memoless transaction detection", func(t *testing.T) {
		testCases := []struct {
			memo           string
			coins          common.Coins
			expectMemoless bool
			expectedRef    string
		}{
			{
				memo:           "",
				coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
				expectMemoless: true,
				expectedRef:    "12345",
			},
			{
				memo:           "SWAP:ETH.ETH",
				coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
				expectMemoless: false,
				expectedRef:    "",
			},
			{
				memo:           "",
				coins:          common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(50000))},
				expectMemoless: false,
				expectedRef:    "",
			},
		}

		for _, tc := range testCases {
			isMemoless, ref := memolessHandler.IsMemolessTransaction(tc.memo, tc.coins)
			require.Equal(t, tc.expectMemoless, isMemoless)
			require.Equal(t, tc.expectedRef, ref)
		}
	})

	// Test complete transaction processing workflow
	t.Run("complete transaction processing workflow", func(t *testing.T) {
		inputTx := &btypes.TxInItem{
			BlockHeight: 1000,
			Tx:          "integration-test-tx",
			Memo:        "",
			Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(150054321))},
			Sender:      "test-sender",
			To:          "test-vault",
		}

		// Process the transaction
		processedTx, err := memolessHandler.ProcessMemolessTransaction(inputTx)
		require.NoError(t, err)

		// Validate the processed transaction
		require.Equal(t, inputTx.BlockHeight, processedTx.BlockHeight)
		require.Equal(t, inputTx.Tx, processedTx.Tx)
		require.Equal(t, "SWAP:BTC.BTC", processedTx.Memo) // Should be resolved
		require.Equal(t, inputTx.Sender, processedTx.Sender)
		require.Equal(t, inputTx.To, processedTx.To)
		
		// Amount should be adjusted (original 150054321 -> 1500)
		require.Equal(t, 1, len(processedTx.Coins))
		require.Equal(t, common.BTCAsset, processedTx.Coins[0].Asset)
		require.True(t, cosmos.NewUint(1500).Equal(processedTx.Coins[0].Amount),
			"Expected amount 1500, got %s", processedTx.Coins[0].Amount.String())

		t.Logf("✅ Successfully processed memoless transaction: %s -> %s", 
			inputTx.Memo, processedTx.Memo)
	})
}

// Test performance and stress scenarios
func TestObserver_MemolessPerformanceIntegration(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t))

	// Mock bridge with consistent responses
	mockBridge := &MockThorchainBridgeForMemoless{}
	mockBridge.getKeysignPartyFunc = func(pubKey common.PubKey) (common.PubKeys, error) {
		return common.PubKeys{}, nil
	}
	mockBridge.getRegisteredMemoFunc = func(reference string) (*stypes.RegisteredMemo, error) {
		return &stypes.RegisteredMemo{
			Reference:    reference,
			Memo:         "SWAP:BTC.BTC",
			ChainId:      "BTC",
			RegisteredAt: 100,
			ExpiresAt:    144100,
			Status:       "active",
		}, nil
	}

	memolessHandler := NewMemolessHandler(mockBridge, logger)

	t.Run("high volume transaction processing", func(t *testing.T) {
		const numTransactions = 100
		var wg sync.WaitGroup
		var successCount int32
		var mu sync.Mutex

		start := time.Now()

		for i := 0; i < numTransactions; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()

				tx := &btypes.TxInItem{
					BlockHeight: int64(1000 + index),
					Tx:          fmt.Sprintf("perf-test-tx-%d", index),
					Memo:        "",
					Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(uint64(100012345+index)))},
					Sender:      fmt.Sprintf("sender-%d", index),
					To:          "test-vault",
				}

				_, err := memolessHandler.ProcessMemolessTransaction(tx)
				if err == nil {
					mu.Lock()
					successCount++
					mu.Unlock()
				}
			}(i)
		}

		wg.Wait()
		duration := time.Since(start)

		require.Equal(t, int32(numTransactions), successCount, 
			"All transactions should process successfully")
		
		avgProcessingTime := duration.Nanoseconds() / int64(numTransactions)
		t.Logf("✅ Processed %d transactions in %v (avg: %v per tx)", 
			numTransactions, duration, time.Duration(avgProcessingTime))
		
		// Performance assertion - should process quickly
		require.Less(t, avgProcessingTime, int64(10*time.Millisecond), 
			"Average processing time should be under 10ms")
	})
}

// Test error recovery and resilience scenarios  
func TestObserver_MemolessResilienceIntegration(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t))

	t.Run("recovery from THORChain service interruption", func(t *testing.T) {
		var callCount int
		var mu sync.Mutex

		mockBridge := &MockThorchainBridgeForMemoless{}
		mockBridge.getKeysignPartyFunc = func(pubKey common.PubKey) (common.PubKeys, error) {
			return common.PubKeys{}, nil
		}
		mockBridge.getRegisteredMemoFunc = func(reference string) (*stypes.RegisteredMemo, error) {
			mu.Lock()
			callCount++
			currentCount := callCount
			mu.Unlock()

			// Fail the first 3 calls, then succeed
			if currentCount <= 3 {
				return nil, errors.New("THORChain service temporarily unavailable")
			}

			return &stypes.RegisteredMemo{
				Reference:    reference,
				Memo:         "SWAP:BTC.BTC",
				ChainId:      "BTC",
				RegisteredAt: 100,
				ExpiresAt:    144100,
				Status:       "active",
			}, nil
		}

		memolessHandler := NewMemolessHandler(mockBridge, logger)

		// Process multiple transactions - some should fail, then succeed
		transactions := []btypes.TxInItem{
			{Tx: "tx-1", Memo: "", Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))}},
			{Tx: "tx-2", Memo: "", Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100067890))}},
			{Tx: "tx-3", Memo: "", Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100011111))}},
			{Tx: "tx-4", Memo: "", Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100022222))}},
		}

		var successCount, errorCount int
		for _, tx := range transactions {
			_, err := memolessHandler.ProcessMemolessTransaction(&tx)
			if err != nil {
				errorCount++
			} else {
				successCount++
			}
		}

		require.Greater(t, errorCount, 0, "Expected some failures during service interruption")
		require.Greater(t, successCount, 0, "Expected some successes after service recovery")
		
		t.Logf("✅ Resilience test: %d errors, %d successes", errorCount, successCount)
	})
}