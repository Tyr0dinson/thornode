package observer

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	shareTypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// BFT testing suite for memoless protocol under Byzantine conditions
type MemolessBFTTestSuite struct {
	suite.Suite
	validators []*MockValidator
	network    *MockBFTNetwork
}

func TestMemolessBFTSuite(t *testing.T) {
	suite.Run(t, new(MemolessBFTTestSuite))
}

func (suite *MemolessBFTTestSuite) SetupTest() {
	// Create a network of 7 validators (can tolerate 2 Byzantine)
	suite.validators = make([]*MockValidator, 7)
	for i := range suite.validators {
		suite.validators[i] = &MockValidator{
			id:        i,
			byzantine: false,
			responses: make(map[string]*types.RegisteredMemo),
		}
	}

	suite.network = &MockBFTNetwork{
		validators:    suite.validators,
		partitioned:   make(map[int]bool),
		latencyMap:    make(map[int]time.Duration),
		faultInjector: &FaultInjector{},
	}
}

func (suite *MemolessBFTTestSuite) TestByzantineValidatorResistance() {
	testCases := []struct {
		name              string
		byzantineCount    int
		byzantineBehavior ByzantineBehavior
		expectSuccess     bool
	}{
		{
			name:              "single_byzantine_false_memo",
			byzantineCount:    1,
			byzantineBehavior: FalseMemoContent,
			expectSuccess:     true,
		},
		{
			name:              "two_byzantine_conflicting_memos",
			byzantineCount:    2,
			byzantineBehavior: ConflictingMemos,
			expectSuccess:     true,
		},
		{
			name:              "threshold_byzantine_validators",
			byzantineCount:    2, // Just at threshold (2/7)
			byzantineBehavior: MemoNotFound,
			expectSuccess:     true,
		},
		{
			name:              "above_threshold_byzantine", // Should fail
			byzantineCount:    3,                           // Above threshold (3/7)
			byzantineBehavior: FalseMemoContent,
			expectSuccess:     false,
		},
	}

	for _, tc := range testCases {
		suite.Run(tc.name, func() {
			suite.SetupTest() // Reset for each test

			// Configure Byzantine validators
			for i := 0; i < tc.byzantineCount; i++ {
				suite.validators[i].byzantine = true
				suite.validators[i].behavior = tc.byzantineBehavior
			}

			// Setup honest validators with correct memo
			correctMemo := &types.RegisteredMemo{
				Reference:    "12345",
				Memo:         "SWAP:BTC.BTC",
				ChainId:      "BTC",
				RegisteredAt: 100,
				ExpiresAt:    144100,
				Status:       "active",
			}

			for i := tc.byzantineCount; i < len(suite.validators); i++ {
				suite.validators[i].responses["12345"] = correctMemo
			}

			// Create Byzantine memo resolver
			resolver := &ByzantineMemoResolver{
				network:   suite.network,
				threshold: len(suite.validators)/2 + 1, // Byzantine fault threshold
				timeout:   time.Second * 5,
			}

			// Attempt memo resolution
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
			defer cancel()

			resolvedMemo, err := resolver.ResolveMemo(ctx, "12345")

			if tc.expectSuccess {
				suite.Require().NoError(err, "Expected successful memo resolution")
				suite.Require().NotNil(resolvedMemo)
				suite.Require().Equal(correctMemo.Memo, resolvedMemo.Memo)
				suite.Require().Equal(correctMemo.Status, resolvedMemo.Status)
			} else {
				suite.Require().Error(err, "Expected memo resolution to fail with too many Byzantine validators")
			}
		})
	}
}

func (suite *MemolessBFTTestSuite) TestNetworkPartitionScenarios() {
	partitionTests := []struct {
		name             string
		partitionedNodes []int
		expectSuccess    bool
		description      string
	}{
		{
			name:             "minority_partition",
			partitionedNodes: []int{0, 1, 2}, // 3/7 partitioned
			expectSuccess:    true,
			description:      "Majority can still reach consensus",
		},
		{
			name:             "exactly_half_partition",
			partitionedNodes: []int{0, 1, 2}, // 3/7, leaving 4/7
			expectSuccess:    true,
			description:      "Majority side should succeed",
		},
		{
			name:             "majority_partition",
			partitionedNodes: []int{0, 1, 2, 3}, // 4/7 partitioned
			expectSuccess:    false,
			description:      "Minority cannot reach consensus",
		},
	}

	for _, tc := range partitionTests {
		suite.Run(tc.name, func() {
			suite.SetupTest()

			// Setup all validators with correct memo
			correctMemo := &types.RegisteredMemo{
				Reference:    "54321",
				Memo:         "ADD:ETH.ETH:tthor1test",
				ChainId:      "ETH",
				RegisteredAt: 200,
				ExpiresAt:    144200,
				Status:       "active",
			}

			for i := range suite.validators {
				suite.validators[i].responses["54321"] = correctMemo
			}

			// Create network partition
			for _, nodeId := range tc.partitionedNodes {
				suite.network.partitioned[nodeId] = true
			}

			resolver := &ByzantineMemoResolver{
				network:   suite.network,
				threshold: len(suite.validators)/2 + 1,
				timeout:   time.Second * 3,
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
			defer cancel()

			resolvedMemo, err := resolver.ResolveMemo(ctx, "54321")

			if tc.expectSuccess {
				suite.Require().NoError(err, "Expected success despite partition: %s", tc.description)
				suite.Require().NotNil(resolvedMemo)
				suite.Require().Equal(correctMemo.Memo, resolvedMemo.Memo)
			} else {
				suite.Require().Error(err, "Expected failure due to partition: %s", tc.description)
			}
		})
	}
}

func (suite *MemolessBFTTestSuite) TestConsensusTimingAttacks() {
	suite.Run("selective_timing_delays", func() {
		suite.SetupTest()

		// Setup validators with correct memo but different response times
		correctMemo := &types.RegisteredMemo{
			Reference:    "98765",
			Memo:         "WITHDRAW:BNB.BNB:1000",
			ChainId:      "BNB",
			RegisteredAt: 300,
			ExpiresAt:    144300,
			Status:       "active",
		}

		// Set up different latencies for validators
		for i := range suite.validators {
			suite.validators[i].responses["98765"] = correctMemo
			if i < 3 {
				// First 3 validators have high latency
				suite.network.latencyMap[i] = time.Second * 4
			} else {
				// Remaining validators respond quickly
				suite.network.latencyMap[i] = time.Millisecond * 100
			}
		}

		resolver := &ByzantineMemoResolver{
			network:   suite.network,
			threshold: len(suite.validators)/2 + 1,
			timeout:   time.Second * 2, // Short timeout to test timing
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()

		start := time.Now()
		resolvedMemo, err := resolver.ResolveMemo(ctx, "98765")
		elapsed := time.Since(start)

		// Should succeed with fast validators even if slow ones timeout
		suite.Require().NoError(err)
		suite.Require().NotNil(resolvedMemo)
		suite.Require().Equal(correctMemo.Memo, resolvedMemo.Memo)

		// Should complete reasonably quickly (before slow validators respond)
		suite.Require().Less(elapsed, time.Second*3, "Resolution took too long: %v", elapsed)
	})
}

func (suite *MemolessBFTTestSuite) TestForkResolutionScenarios() {
	suite.Run("competing_memo_versions", func() {
		suite.SetupTest()

		// Create two competing versions of the same memo
		memoV1 := &types.RegisteredMemo{
			Reference:    "11111",
			Memo:         "SWAP:BTC.BTC:v1",
			ChainId:      "BTC",
			RegisteredAt: 100,
			ExpiresAt:    144100,
			Status:       "active",
		}

		memoV2 := &types.RegisteredMemo{
			Reference:    "11111",
			Memo:         "SWAP:BTC.BTC:v2",
			ChainId:      "BTC",
			RegisteredAt: 101, // Later registration
			ExpiresAt:    144101,
			Status:       "active",
		}

		// First 3 validators have v1, last 4 have v2
		for i := 0; i < 3; i++ {
			suite.validators[i].responses["11111"] = memoV1
		}
		for i := 3; i < len(suite.validators); i++ {
			suite.validators[i].responses["11111"] = memoV2
		}

		resolver := &ByzantineMemoResolver{
			network:      suite.network,
			threshold:    len(suite.validators)/2 + 1,
			timeout:      time.Second * 5,
			forkResolver: NewTimestampForkResolver(), // Prefer later timestamp
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()

		resolvedMemo, err := resolver.ResolveMemo(ctx, "11111")

		suite.Require().NoError(err)
		suite.Require().NotNil(resolvedMemo)
		// Should choose v2 due to later timestamp
		suite.Require().Equal(memoV2.Memo, resolvedMemo.Memo)
		suite.Require().Equal(int64(101), resolvedMemo.RegisteredAt)
	})
}

func (suite *MemolessBFTTestSuite) TestCascadingFailureRecovery() {
	suite.Run("progressive_validator_failures", func() {
		suite.SetupTest()

		correctMemo := &types.RegisteredMemo{
			Reference:    "77777",
			Memo:         "SWAP:ETH.ETH:recovery_test",
			ChainId:      "ETH",
			RegisteredAt: 500,
			ExpiresAt:    144500,
			Status:       "active",
		}

		// All validators start with correct memo
		for i := range suite.validators {
			suite.validators[i].responses["77777"] = correctMemo
		}

		resolver := &ByzantineMemoResolver{
			network:   suite.network,
			threshold: len(suite.validators)/2 + 1,
			timeout:   time.Second * 2,
		}

		// Progressively fail validators and test recovery
		for failCount := 0; failCount <= 2; failCount++ { // Can tolerate 2 failures
			// Fail additional validator
			if failCount > 0 {
				suite.network.faultInjector.InjectFailure(failCount-1, NetworkError)
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)

			resolvedMemo, err := resolver.ResolveMemo(ctx, "77777")
			cancel()

			if failCount < 3 { // Should succeed with up to 2 failures
				suite.Require().NoError(err, "Should handle %d validator failures", failCount)
				suite.Require().NotNil(resolvedMemo)
				suite.Require().Equal(correctMemo.Memo, resolvedMemo.Memo)
			}
		}

		// Test recovery after failures are resolved
		suite.network.faultInjector.ClearFailures()

		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()

		resolvedMemo, err := resolver.ResolveMemo(ctx, "77777")
		suite.Require().NoError(err, "Should recover after failures cleared")
		suite.Require().NotNil(resolvedMemo)
		suite.Require().Equal(correctMemo.Memo, resolvedMemo.Memo)
	})
}

// Mock implementations for BFT testing

type MockValidator struct {
	id        int
	byzantine bool
	behavior  ByzantineBehavior
	responses map[string]*types.RegisteredMemo
	mu        sync.RWMutex
}

func (mv *MockValidator) QueryMemo(ctx context.Context, reference string) (*types.RegisteredMemo, error) {
	mv.mu.RLock()
	defer mv.mu.RUnlock()

	// Simulate Byzantine behavior
	if mv.byzantine {
		switch mv.behavior {
		case FalseMemoContent:
			fakeMemo := &types.RegisteredMemo{
				Reference:    reference,
				Memo:         "FAKE:MEMO:CONTENT",
				ChainId:      "FAKE",
				RegisteredAt: 999,
				ExpiresAt:    999999,
				Status:       "malicious",
			}
			return fakeMemo, nil

		case ConflictingMemos:
			conflictMemo := &types.RegisteredMemo{
				Reference:    reference,
				Memo:         fmt.Sprintf("CONFLICT:%d:MEMO", mv.id),
				ChainId:      "BTC",
				RegisteredAt: int64(mv.id * 100),
				ExpiresAt:    144000 + int64(mv.id*100),
				Status:       "active",
			}
			return conflictMemo, nil

		case MemoNotFound:
			return nil, errors.New("memo not found")

		case DelayedResponse:
			time.Sleep(time.Second * 10) // Excessive delay

		case CorruptedData:
			corruptMemo := &types.RegisteredMemo{
				Reference:    "XXXXX", // Corrupted reference
				Memo:         "",      // Empty memo
				ChainId:      "",
				RegisteredAt: -1, // Invalid timestamp
				ExpiresAt:    0,
				Status:       "corrupt",
			}
			return corruptMemo, nil
		}
	}

	// Honest behavior
	memo, exists := mv.responses[reference]
	if !exists {
		return nil, errors.New("memo not found")
	}

	return memo, nil
}

type MockBFTNetwork struct {
	validators    []*MockValidator
	partitioned   map[int]bool
	latencyMap    map[int]time.Duration
	faultInjector *FaultInjector
	mu            sync.RWMutex
}

func (mn *MockBFTNetwork) QueryValidators(ctx context.Context, reference string) ([]*types.RegisteredMemo, error) {
	mn.mu.RLock()
	defer mn.mu.RUnlock()

	results := make(chan *ValidatorResponse, len(mn.validators))

	// Query all available validators concurrently
	for i, validator := range mn.validators {
		go func(id int, v *MockValidator) {
			// Check if validator is partitioned
			if mn.partitioned[id] {
				results <- &ValidatorResponse{ValidatorID: id, Error: errors.New("network partition")}
				return
			}

			// Check for injected failures
			if mn.faultInjector.ShouldFail(id) {
				results <- &ValidatorResponse{ValidatorID: id, Error: errors.New("injected failure")}
				return
			}

			// Apply artificial latency
			if latency, exists := mn.latencyMap[id]; exists {
				time.Sleep(latency)
			}

			// Query validator
			memo, err := v.QueryMemo(ctx, reference)
			results <- &ValidatorResponse{
				ValidatorID: id,
				Memo:        memo,
				Error:       err,
			}
		}(i, validator)
	}

	// Collect results
	var responses []*types.RegisteredMemo
	var errors []error

	for i := 0; i < len(mn.validators); i++ {
		select {
		case response := <-results:
			if response.Error != nil {
				errors = append(errors, response.Error)
			} else if response.Memo != nil {
				responses = append(responses, response.Memo)
			}
		case <-ctx.Done():
			return responses, ctx.Err()
		}
	}

	return responses, nil
}

type ValidatorResponse struct {
	ValidatorID int
	Memo        *types.RegisteredMemo
	Error       error
}

// FaultType and other common types are defined in memoless_test_utils.go

type FaultInjector struct {
	failures map[int]FaultType
	mu       sync.RWMutex
}

func (fi *FaultInjector) InjectFailure(validatorID int, faultType FaultType) {
	fi.mu.Lock()
	defer fi.mu.Unlock()

	if fi.failures == nil {
		fi.failures = make(map[int]FaultType)
	}
	fi.failures[validatorID] = faultType
}

func (fi *FaultInjector) ShouldFail(validatorID int) bool {
	fi.mu.RLock()
	defer fi.mu.RUnlock()

	_, exists := fi.failures[validatorID]
	return exists
}

func (fi *FaultInjector) ClearFailures() {
	fi.mu.Lock()
	defer fi.mu.Unlock()

	fi.failures = make(map[int]FaultType)
}

// Byzantine fault tolerant memo resolver
type ByzantineMemoResolver struct {
	network      *MockBFTNetwork
	threshold    int
	timeout      time.Duration
	forkResolver ForkResolver
}

func (bmr *ByzantineMemoResolver) ResolveMemo(ctx context.Context, reference string) (*types.RegisteredMemo, error) {
	responses, err := bmr.network.QueryValidators(ctx, reference)
	if err != nil {
		return nil, fmt.Errorf("failed to query validators: %w", err)
	}

	if len(responses) < bmr.threshold {
		return nil, fmt.Errorf("insufficient responses: got %d, need %d", len(responses), bmr.threshold)
	}

	// Group responses by content hash to detect conflicts
	responseGroups := make(map[string][]*types.RegisteredMemo)
	for _, response := range responses {
		hash := bmr.hashMemo(response)
		responseGroups[hash] = append(responseGroups[hash], response)
	}

	// Find the group with most responses (Byzantine consensus)
	var majorityGroup []*types.RegisteredMemo
	maxCount := 0

	for _, group := range responseGroups {
		if len(group) > maxCount {
			maxCount = len(group)
			majorityGroup = group
		}
	}

	if maxCount < bmr.threshold {
		return nil, fmt.Errorf("no consensus reached: max agreement %d, need %d", maxCount, bmr.threshold)
	}

	// Resolve forks if multiple versions exist in majority group
	if bmr.forkResolver != nil && len(majorityGroup) > 1 {
		return bmr.forkResolver.ResolveFork(majorityGroup), nil
	}

	return majorityGroup[0], nil
}

func (bmr *ByzantineMemoResolver) hashMemo(memo *types.RegisteredMemo) string {
	return fmt.Sprintf("%s:%s:%s:%d:%d:%s",
		memo.Reference, memo.Memo, memo.ChainId,
		memo.RegisteredAt, memo.ExpiresAt, memo.Status)
}

type ForkResolver interface {
	ResolveFork(memos []*types.RegisteredMemo) *types.RegisteredMemo
}

type TimestampForkResolver struct{}

func NewTimestampForkResolver() *TimestampForkResolver {
	return &TimestampForkResolver{}
}

func (tfr *TimestampForkResolver) ResolveFork(memos []*types.RegisteredMemo) *types.RegisteredMemo {
	if len(memos) == 0 {
		return nil
	}

	// Choose memo with latest timestamp
	latest := memos[0]
	for _, memo := range memos[1:] {
		if memo.RegisteredAt > latest.RegisteredAt {
			latest = memo
		}
	}

	return latest
}

// MockThorClient and other common test utilities are defined in memoless_test_utils.go
