package keeperv1

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// MemolessTestSuite provides test utilities for memoless protocol testing
type MemolessTestSuite struct {
	ctx    cosmos.Context
	keeper KVStore
}

// NewMemolessTestSuite creates a new test suite with initialized keeper
func NewMemolessTestSuite(c *C) *MemolessTestSuite {
	ctx, keeper := setupKeeperForTest(c)
	
	// Initialize default chain configurations
	err := keeper.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)
	
	// Set memoless constants for testing
	keeper.SetMimir(ctx, constants.MemolessEnabled.String(), 1)
	keeper.SetMimir(ctx, constants.MemolessTTL.String(), 14400) // 4 hours
	keeper.SetMimir(ctx, constants.MemolessMaxMemoLength.String(), 512)
	keeper.SetMimir(ctx, constants.MemolessCollisionLimit.String(), 100)
	keeper.SetMimir(ctx, constants.MemolessSlashAmount.String(), 1000000)
	keeper.SetMimir(ctx, constants.MemolessMinAmount.String(), 100000)
	keeper.SetMimir(ctx, constants.MemolessMaxTTL.String(), 1008000) // 1 week
	keeper.SetMimir(ctx, constants.MemolessGracePeriod.String(), 720) // 1 hour
	keeper.SetMimir(ctx, constants.MemolessMaxActiveRefs.String(), 100000)
	
	return &MemolessTestSuite{
		ctx:    ctx,
		keeper: keeper,
	}
}

// GetContext returns the test context
func (suite *MemolessTestSuite) GetContext() cosmos.Context {
	return suite.ctx
}

// GetKeeper returns the test keeper
func (suite *MemolessTestSuite) GetKeeper() KVStore {
	return suite.keeper
}

// AdvanceBlockHeight advances the block height by the specified amount
func (suite *MemolessTestSuite) AdvanceBlockHeight(blocks int64) {
	suite.ctx = suite.ctx.WithBlockHeight(suite.ctx.BlockHeight() + blocks)
}

// SetBlockTime sets the block time for temporal testing
func (suite *MemolessTestSuite) SetBlockTime(t time.Time) {
	suite.ctx = suite.ctx.WithBlockTime(t)
}

// CreateTestMemo creates a test registered memo with default values
func (suite *MemolessTestSuite) CreateTestMemo(reference, memo, chainID string) types.RegisteredMemo {
	currentHeight := suite.ctx.BlockHeight()
	ttl := int64(14400) // 4 hours in blocks
	
	return types.RegisteredMemo{
		Reference:    reference,
		Memo:         memo,
		RegisteredAt: currentHeight,
		ExpiresAt:    currentHeight + ttl,
		ChainId:      chainID,
		Status:       "active",
	}
}

// RegisterTestMemo registers a test memo and returns the reference
func (suite *MemolessTestSuite) RegisterTestMemo(c *C, memo, chainID string) string {
	constantProvider := constants.GetConstantValues(GetCurrentVersion())
	
	// Generate reference
	reference, err := suite.keeper.ResolveCollision(suite.ctx, memo, suite.ctx.BlockHeight(), constantProvider)
	c.Assert(err, IsNil)
	
	// Create and store memo
	registeredMemo := suite.CreateTestMemo(reference, memo, chainID)
	err = suite.keeper.SetRegisteredMemo(suite.ctx, registeredMemo)
	c.Assert(err, IsNil)
	
	// Update statistics
	err = suite.keeper.IncrementRegistrations(suite.ctx)
	c.Assert(err, IsNil)
	
	return reference
}

// CreateTestMsgRegisterMemo creates a test MsgRegisterMemo message
func (suite *MemolessTestSuite) CreateTestMsgRegisterMemo(memo, chainID string, ttl int64, signer cosmos.AccAddress) types.MsgRegisterMemo {
	msg := types.MsgRegisterMemo{
		Memo:    memo,
		Ttl:     ttl,
		ChainId: chainID,
		Signer:  signer,
	}
	return msg
}

// CreateExtendedTestMsgRegisterMemo creates a test message with additional validation fields
func (suite *MemolessTestSuite) CreateExtendedTestMsgRegisterMemo(memo, chainID, amount, destination string, ttl int64, signer cosmos.AccAddress, forceCollision bool, tags []string) types.MsgRegisterMemo {
	msg := types.MsgRegisterMemo{
		Memo:           memo,
		Ttl:            ttl,
		ChainId:        chainID,
		Signer:         signer,
		Amount:         amount,
		Destination:    destination,
		ForceCollision: forceCollision,
		Tags:           tags,
	}
	return msg
}

// GenerateRandomMemo generates a random memo for testing
func (suite *MemolessTestSuite) GenerateRandomMemo() string {
	memos := []string{
		"SWAP:BTC.BTC::thor123",
		"ADD:BTC.BTC:100000000",
		"WITHDRAW:BTC/BTC:5000:thor456",
		"LOAN+:ETH.ETH:thor789:100000000",
		"LOAN-:ETH.ETH:thor789",
		"SAVERS+:BTC/BTC:thor111",
		"SAVERS-:2000:thor222",
	}
	
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(memos))))
	return memos[n.Int64()]
}

// GenerateRandomChainID generates a random supported chain ID for testing
func (suite *MemolessTestSuite) GenerateRandomChainID() string {
	chains := []string{"BTC", "ETH", "BNB", "BCH", "LTC"}
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chains))))
	return chains[n.Int64()]
}

// GenerateRandomReference generates a random 5-digit reference
func (suite *MemolessTestSuite) GenerateRandomReference() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(90000))
	return fmt.Sprintf("%05d", n.Int64()+10000)
}

// CreateTestRegistrationBatch creates multiple test registrations for load testing
func (suite *MemolessTestSuite) CreateTestRegistrationBatch(c *C, count int) []string {
	references := make([]string, 0, count)
	
	for i := 0; i < count; i++ {
		memo := fmt.Sprintf("SWAP:BTC.BTC::%d", i)
		chainID := "BTC"
		reference := suite.RegisterTestMemo(c, memo, chainID)
		references = append(references, reference)
	}
	
	return references
}

// SimulateCollisionScenario simulates collision scenarios for testing
func (suite *MemolessTestSuite) SimulateCollisionScenario(c *C, memo string, attempts int) []string {
	constantProvider := constants.GetConstantValues(GetCurrentVersion())
	references := make([]string, 0, attempts)
	
	// Force collisions by using same memo and incrementing height
	baseHeight := suite.ctx.BlockHeight()
	
	for i := 0; i < attempts; i++ {
		reference, err := suite.keeper.ResolveCollision(suite.ctx, memo, baseHeight+int64(i), constantProvider)
		if err != nil {
			break // Collision limit reached
		}
		
		// Register memo to create collision for next iteration
		registeredMemo := suite.CreateTestMemo(reference, memo, "BTC")
		err = suite.keeper.SetRegisteredMemo(suite.ctx, registeredMemo)
		c.Assert(err, IsNil)
		
		references = append(references, reference)
	}
	
	return references
}

// ValidateCollisionRate validates that collision rate is within expected bounds
func (suite *MemolessTestSuite) ValidateCollisionRate(c *C, totalRegistrations int, observedCollisions int, expectedRate float64) {
	observedRate := float64(observedCollisions) / float64(totalRegistrations)
	tolerance := 0.005 // 0.5% tolerance
	
	c.Assert(observedRate, LessEqual, expectedRate+tolerance, 
		Commentf("Collision rate %.4f exceeds expected rate %.4f", observedRate, expectedRate))
	
	c.Logf("Collision validation: %d collisions out of %d registrations (%.4f%% rate)", 
		observedCollisions, totalRegistrations, observedRate*100)
}

// CreateExpiredMemos creates test memos that are already expired
func (suite *MemolessTestSuite) CreateExpiredMemos(c *C, count int) []string {
	references := make([]string, 0, count)
	currentHeight := suite.ctx.BlockHeight()
	
	for i := 0; i < count; i++ {
		memo := fmt.Sprintf("EXPIRED:MEMO:%d", i)
		reference := suite.GenerateRandomReference()
		
		// Create expired memo (registered in the past, already expired)
		registeredMemo := types.RegisteredMemo{
			Reference:    reference,
			Memo:         memo,
			RegisteredAt: currentHeight - 20000, // Registered well in the past
			ExpiresAt:    currentHeight - 1000,  // Expired 1000 blocks ago
			ChainId:      "BTC",
			Status:       "expired",
		}
		
		err := suite.keeper.SetRegisteredMemo(suite.ctx, registeredMemo)
		c.Assert(err, IsNil)
		
		references = append(references, reference)
	}
	
	return references
}

// ValidateMemoPrecision validates that amount precision is preserved
func (suite *MemolessTestSuite) ValidateMemoPrecision(c *C, chainID string, originalAmount cosmos.Uint, processedAmount cosmos.Uint) {
	// Amount should be exactly the same after processing
	c.Assert(originalAmount.Equal(processedAmount), Equals, true, 
		Commentf("Precision loss detected for chain %s: original=%s, processed=%s", 
			chainID, originalAmount.String(), processedAmount.String()))
}

// CreateTestAmountWithReference creates test amount with embedded 5-digit reference
func (suite *MemolessTestSuite) CreateTestAmountWithReference(baseAmount cosmos.Uint, reference string) cosmos.Uint {
	// Convert reference to integer
	refInt := big.NewInt(0)
	refInt.SetString(reference, 10)
	
	// Create amount with reference embedded in last 5 digits
	baseInt := baseAmount.BigInt()
	
	// Remove last 5 digits and add reference
	divisor := big.NewInt(100000) // 10^5
	quotient := big.NewInt(0).Div(baseInt, divisor)
	newAmount := big.NewInt(0).Mul(quotient, divisor)
	newAmount.Add(newAmount, refInt)
	
	return cosmos.NewUintFromBigInt(newAmount)
}

// ExtractReferenceFromAmount extracts 5-digit reference from amount
func (suite *MemolessTestSuite) ExtractReferenceFromAmount(amount cosmos.Uint) string {
	amountInt := amount.BigInt()
	divisor := big.NewInt(100000) // 10^5
	remainder := big.NewInt(0).Mod(amountInt, divisor)
	
	return fmt.Sprintf("%05d", remainder.Int64())
}

// MockConstantProvider creates a mock constant provider for testing
type MockConstantProvider struct {
	values map[constants.ConstantName]int64
}

// NewMockConstantProvider creates a new mock constant provider
func NewMockConstantProvider() *MockConstantProvider {
	return &MockConstantProvider{
		values: make(map[constants.ConstantName]int64),
	}
}

// SetValue sets a constant value
func (m *MockConstantProvider) SetValue(name constants.ConstantName, value int64) {
	m.values[name] = value
}

// GetInt64Value returns a constant value
func (m *MockConstantProvider) GetInt64Value(name constants.ConstantName) int64 {
	if value, exists := m.values[name]; exists {
		return value
	}
	// Return default values for testing
	switch name {
	case constants.MemolessEnabled:
		return 1
	case constants.MemolessTTL:
		return 14400
	case constants.MemolessMaxMemoLength:
		return 512
	case constants.MemolessCollisionLimit:
		return 100
	case constants.MemolessSlashAmount:
		return 1000000
	case constants.MemolessMinAmount:
		return 100000
	case constants.MemolessMaxTTL:
		return 1008000
	case constants.MemolessGracePeriod:
		return 720
	case constants.MemolessMaxActiveRefs:
		return 100000
	default:
		return 0
	}
}

// CreateChainConfigForTesting creates test chain configuration
func (suite *MemolessTestSuite) CreateChainConfigForTesting(chainID string, decimalPlaces int, supportsMemoless bool) ChainConfig {
	config := ChainConfig{
		ChainID:          chainID,
		DecimalPlaces:    decimalPlaces,
		MinAmount:        cosmos.NewUint(100000),
		MaxAmount:        cosmos.NewUint(1000000000000000),
		DustThreshold:    cosmos.NewUint(1000),
		SupportsMemoless: supportsMemoless,
		PrecisionLoss:    !supportsMemoless,
		GasMultiplier:    cosmos.NewDec(1),
	}
	return config
}

// SetTestChainConfig sets a test chain configuration
func (suite *MemolessTestSuite) SetTestChainConfig(c *C, config ChainConfig) {
	err := suite.keeper.SetChainConfig(suite.ctx, config)
	c.Assert(err, IsNil)
}

// ValidateTestResults validates test results against acceptance criteria
func (suite *MemolessTestSuite) ValidateTestResults(c *C, testName string, results map[string]interface{}) {
	c.Logf("=== %s Test Results ===", testName)
	
	for key, value := range results {
		c.Logf("%s: %v", key, value)
	}
	
	// Validate specific acceptance criteria based on test name
	switch testName {
	case "CollisionTest":
		if rate, ok := results["collision_rate"].(float64); ok {
			c.Assert(rate, LessEqual, 0.0001, Commentf("Collision rate %.6f exceeds 0.01%% threshold", rate*100))
		}
	case "PrecisionTest":
		if lossCount, ok := results["precision_loss_count"].(int); ok {
			c.Assert(lossCount, Equals, 0, Commentf("Precision loss detected: %d instances", lossCount))
		}
	case "PerformanceTest":
		if avgLatency, ok := results["avg_latency_ms"].(float64); ok {
			c.Assert(avgLatency, LessEqual, 1.0, Commentf("Average latency %.2fms exceeds 1ms threshold", avgLatency))
		}
	}
}

// BenchmarkOperation benchmarks a specific operation
func (suite *MemolessTestSuite) BenchmarkOperation(operation string, iterations int, fn func() error) (time.Duration, error) {
	start := time.Now()
	
	for i := 0; i < iterations; i++ {
		if err := fn(); err != nil {
			return 0, fmt.Errorf("operation failed at iteration %d: %w", i, err)
		}
	}
	
	duration := time.Since(start)
	avgDuration := duration / time.Duration(iterations)
	
	return avgDuration, nil
}

// CleanupTestData cleans up test data after tests
func (suite *MemolessTestSuite) CleanupTestData(c *C) {
	// Clean up expired memos
	err := suite.keeper.DeleteExpiredMemos(suite.ctx, suite.ctx.BlockHeight()+1000000)
	c.Assert(err, IsNil)
	
	c.Logf("Test cleanup completed")
}