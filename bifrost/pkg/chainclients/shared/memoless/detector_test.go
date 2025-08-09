package memoless

import (
	"fmt"
	"testing"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestPackage(t *testing.T) { TestingT(t) }

// MemolessDetectorTestSuite tests memoless reference detection in Bifrost
type MemolessDetectorTestSuite struct{}

var _ = Suite(&MemolessDetectorTestSuite{})

// TestMemolessReferenceDetection tests detection of 5-digit memoless references (RED PHASE)
func (s *MemolessDetectorTestSuite) TestMemolessReferenceDetection(c *C) {
	detector := NewMemolessDetector()
	
	// Test data with various reference patterns
	testCases := []struct {
		name           string
		memo           string
		expectedRef    string
		shouldDetect   bool
		description    string
	}{
		{
			name:         "valid_5digit_reference",
			memo:         "12345",
			expectedRef:  "12345",
			shouldDetect: true,
			description:  "Should detect standalone 5-digit reference",
		},
		{
			name:         "valid_reference_with_prefix",
			memo:         "REF:67890",
			expectedRef:  "67890",
			shouldDetect: true,
			description:  "Should detect 5-digit reference with REF: prefix",
		},
		{
			name:         "valid_reference_in_amount",
			memo:         "100012345",
			expectedRef:  "12345",
			shouldDetect: true,
			description:  "Should extract reference from last 5 digits of amount",
		},
		{
			name:         "invalid_4digit_number",
			memo:         "1234",
			expectedRef:  "",
			shouldDetect: false,
			description:  "Should not detect 4-digit numbers",
		},
		{
			name:         "invalid_6digit_number",
			memo:         "123456",
			expectedRef:  "",
			shouldDetect: false,
			description:  "Should not detect 6-digit numbers",
		},
		{
			name:         "invalid_swap_memo",
			memo:         "SWAP:BTC.BTC::thor1destination",
			expectedRef:  "",
			shouldDetect: false,
			description:  "Should not detect references in complex swap memos",
		},
		{
			name:         "invalid_empty_memo",
			memo:         "",
			expectedRef:  "",
			shouldDetect: false,
			description:  "Should handle empty memos",
		},
		{
			name:         "edge_case_leading_zeros",
			memo:         "00123",
			expectedRef:  "00123",
			shouldDetect: true,
			description:  "Should detect references with leading zeros",
		},
	}
	
	for _, tc := range testCases {
		c.Logf("Testing case: %s - %s", tc.name, tc.description)
		
		// RED: This should fail initially - DetectMemolessReference method doesn't exist yet
		reference, detected := detector.DetectMemolessReference(tc.memo)
		c.Assert(detected, Equals, tc.shouldDetect, Commentf("Case %s: Detection result mismatch", tc.name))
		c.Assert(reference, Equals, tc.expectedRef, Commentf("Case %s: Reference extraction mismatch", tc.name))
	}
}

// TestTxInItemMemolessProcessing tests processing TxInItem for memoless transactions (RED PHASE)
func (s *MemolessDetectorTestSuite) TestTxInItemMemolessProcessing(c *C) {
	detector := NewMemolessDetector()
	
	// Create test TxInItem with memoless reference
	txInItem := &types.TxInItem{
		BlockHeight:         12345,
		Tx:                  "test_tx_hash",
		Memo:                "54321", // 5-digit memoless reference
		Sender:              "bc1qsender123",
		To:                  "bc1qreceiver456",
		Coins:               common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100000000))}, // 1 BTC
		Gas:                 common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(10000))},
		ObservedVaultPubKey: common.EmptyPubKey,
	}
	
	// RED: This should fail initially - ProcessMemolessTransaction method doesn't exist yet
	result, err := detector.ProcessMemolessTransaction(txInItem)
	c.Assert(err, IsNil, Commentf("ProcessMemolessTransaction should be implemented"))
	c.Assert(result, NotNil, Commentf("Should return valid result"))
	c.Assert(result.Reference, Equals, "54321", Commentf("Should extract correct reference"))
	c.Assert(result.OriginalMemo, Equals, "54321", Commentf("Should preserve original memo"))
	c.Assert(result.IsMemoless, Equals, true, Commentf("Should identify as memoless transaction"))
}

// TestMemolessTransactionRouting tests routing of memoless transactions (RED PHASE)
func (s *MemolessDetectorTestSuite) TestMemolessTransactionRouting(c *C) {
	detector := NewMemolessDetector()
	
	// Create test transactions - mix of memoless and regular
	testTransactions := []*types.TxInItem{
		{
			Memo:   "12345", // Memoless
			Sender: "sender1",
			Coins:  common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(50000000))},
		},
		{
			Memo:   "SWAP:ETH.ETH::thor1dest", // Regular swap
			Sender: "sender2", 
			Coins:  common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000000000000000))},
		},
		{
			Memo:   "67890", // Memoless  
			Sender: "sender3",
			Coins:  common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(25000000))},
		},
	}
	
	// RED: This should fail initially - RouteMemolessTransactions method doesn't exist yet
	memolessCount, regularCount, err := detector.RouteMemolessTransactions(testTransactions)
	c.Assert(err, IsNil, Commentf("RouteMemolessTransactions should be implemented"))
	c.Assert(memolessCount, Equals, 2, Commentf("Should identify 2 memoless transactions"))
	c.Assert(regularCount, Equals, 1, Commentf("Should identify 1 regular transaction"))
}

// TestMemolessValidationIntegration tests integration with chain validation (RED PHASE)
func (s *MemolessDetectorTestSuite) TestMemolessValidationIntegration(c *C) {
	detector := NewMemolessDetector()
	
	// Test validation of memoless transactions for different chains
	testCases := []struct {
		chain       common.Chain
		amount      cosmos.Uint
		reference   string
		shouldPass  bool
		description string
	}{
		{
			chain:       common.BTCChain,
			amount:      cosmos.NewUint(100000000), // 1 BTC
			reference:   "12345",
			shouldPass:  true,
			description: "BTC transaction with sufficient amount should pass",
		},
		{
			chain:       common.BTCChain,
			amount:      cosmos.NewUint(50000), // 0.0005 BTC - too small
			reference:   "67890",
			shouldPass:  false,
			description: "BTC transaction with insufficient amount should fail",
		},
		{
			chain:       common.ETHChain,
			amount:      cosmos.NewUint(1000000000000000000), // 1 ETH
			reference:   "54321",
			shouldPass:  true,
			description: "ETH transaction with sufficient amount should pass",
		},
		{
			chain:       common.DOGEChain,
			amount:      cosmos.NewUint(100000000), // 1 DOGE
			reference:   "11111",
			shouldPass:  false,
			description: "DOGE transactions should fail due to precision issues",
		},
	}
	
	for _, tc := range testCases {
		c.Logf("Testing validation: %s", tc.description)
		
		// RED: This should fail initially - ValidateMemolessTransaction method doesn't exist yet
		valid, err := detector.ValidateMemolessTransaction(tc.chain, tc.amount, tc.reference)
		if tc.shouldPass {
			c.Assert(err, IsNil, Commentf("Validation should pass for: %s", tc.description))
			c.Assert(valid, Equals, true, Commentf("Should be valid: %s", tc.description))
		} else {
			c.Assert(valid, Equals, false, Commentf("Should be invalid: %s", tc.description))
		}
	}
}

// TestMemolessPerformanceMetrics tests performance monitoring for memoless processing (RED PHASE)
func (s *MemolessDetectorTestSuite) TestMemolessPerformanceMetrics(c *C) {
	detector := NewMemolessDetector()
	
	// Create batch of transactions for performance testing
	batchSize := 100
	transactions := make([]*types.TxInItem, batchSize)
	
	for i := 0; i < batchSize; i++ {
		memo := "12345" // All memoless for this test
		if i%10 == 0 {
			memo = "SWAP:BTC.BTC::thor1dest" // 10% regular transactions
		}
		
		transactions[i] = &types.TxInItem{
			Memo:   memo,
			Sender: fmt.Sprintf("sender%d", i),
			Coins:  common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100000000))},
		}
	}
	
	// RED: This should fail initially - ProcessBatchWithMetrics method doesn't exist yet
	metrics, err := detector.ProcessBatchWithMetrics(transactions)
	c.Assert(err, IsNil, Commentf("ProcessBatchWithMetrics should be implemented"))
	c.Assert(metrics, NotNil, Commentf("Should return metrics"))
	c.Assert(metrics.TotalProcessed, Equals, int64(batchSize), Commentf("Should process all transactions"))
	c.Assert(metrics.MemolessCount, Equals, int64(90), Commentf("Should detect 90 memoless transactions"))
	c.Assert(metrics.RegularCount, Equals, int64(10), Commentf("Should detect 10 regular transactions"))
	c.Assert(metrics.ProcessingTimeMs > 0, Equals, true, Commentf("Should measure processing time"))
}