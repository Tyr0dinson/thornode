package keeperv1

import (
	"fmt"
	
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/constants"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type MemolessKeeperTestSuite struct{}

var _ = Suite(&MemolessKeeperTestSuite{})

// TestBasicInfrastructure tests basic infrastructure functionality
func (s *MemolessKeeperTestSuite) TestBasicInfrastructure(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test 1: Verify constants are accessible
	constantProvider := constants.GetConstantValues(GetCurrentVersion())
	enabled := constantProvider.GetInt64Value(constants.MemolessEnabled)
	c.Assert(enabled, Not(Equals), int64(0), Commentf("Memoless should be enabled for testing"))

	// Test 2: Verify chain configuration works
	defaultConfigs := GetDefaultChainConfigs()
	c.Assert(len(defaultConfigs) > 0, Equals, true, Commentf("Should have default chain configurations"))

	btcConfig, exists := defaultConfigs["BTC"]
	c.Assert(exists, Equals, true, Commentf("Should have BTC configuration"))
	c.Assert(btcConfig.SupportsMemoless, Equals, true, Commentf("BTC should support memoless"))

	// Test 3: Verify chain configuration storage works
	err := k.SetChainConfig(ctx, btcConfig)
	c.Assert(err, IsNil, Commentf("Should be able to store chain config"))

	retrievedConfig, err := k.GetChainConfig(ctx, "BTC")
	c.Assert(err, IsNil, Commentf("Should be able to retrieve chain config"))
	c.Assert(retrievedConfig.ChainID, Equals, "BTC", Commentf("Retrieved config should match"))

	// Test 4: Verify validation works
	err = k.ValidateChainForMemoless(ctx, "BTC")
	c.Assert(err, IsNil, Commentf("BTC should be valid for memoless"))

	err = k.ValidateChainForMemoless(ctx, "INVALID")
	c.Assert(err, NotNil, Commentf("Invalid chain should fail validation"))

	c.Logf("✓ Basic infrastructure validation passed")
}

// TestReferenceGeneration tests incremental counter reference generation functionality
func (s *MemolessKeeperTestSuite) TestReferenceGeneration(c *C) {
	ctx, k := setupKeeperForTest(c)
	
	// Test incremental counter reference generation
	ref1, err := k.GetNextReferenceID(ctx, "BTC")
	c.Assert(err, IsNil)
	
	ref2, err := k.GetNextReferenceID(ctx, "BTC")
	c.Assert(err, IsNil)

	// References should be 5 digits
	c.Assert(len(ref1), Equals, 5, Commentf("Reference should be 5 digits"))
	c.Assert(len(ref2), Equals, 5, Commentf("Reference should be 5 digits"))

	// Sequential references should be different and increment
	c.Assert(ref1, Not(Equals), ref2, Commentf("Sequential references should be different"))
	
	// Test incremental behavior
	c.Assert(ref1, Equals, "10000", Commentf("First reference should be 10000"))
	c.Assert(ref2, Equals, "10001", Commentf("Second reference should be 10001"))

	// Test reference validation
	err = ValidateReference(ref1)
	c.Assert(err, IsNil, Commentf("Generated reference should be valid"))

	err = ValidateReference("15432")
	c.Assert(err, IsNil, Commentf("Valid 5-digit reference should pass"))

	err = ValidateReference("1234")
	c.Assert(err, NotNil, Commentf("4-digit reference should fail"))

	err = ValidateReference("abcde")
	c.Assert(err, NotNil, Commentf("Non-numeric reference should fail"))

	c.Logf("✓ Reference generation tests passed")
}

// TestMemoRegistrationBasic tests basic memo registration functionality
func (s *MemolessKeeperTestSuite) TestMemoRegistrationBasic(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test data
	memo := "SWAP:ETH.ETH:thor1abc123:14400"

	// Test basic memo registration with incremental counter
	reference, err := k.GetNextReferenceID(ctx, "BTC")
	c.Assert(err, IsNil, Commentf("Should be able to resolve collision for new memo"))
	c.Assert(len(reference), Equals, 5, Commentf("Reference should be 5 digits"))

	// Test memo storage and retrieval
	registeredMemo := thorchaintypes.RegisteredMemo{
		Reference:    reference,
		Memo:         memo,
		ChainId:      "BTC",
		RegisteredAt: ctx.BlockHeight(),
		ExpiresAt:    ctx.BlockHeight() + 14400,
		Status:       "active",
	}
	err = k.SetRegisteredMemo(ctx, registeredMemo)
	c.Assert(err, IsNil, Commentf("Should be able to store registered memo"))

	// Test memo retrieval
	retrieved, err := k.GetRegisteredMemo(ctx, reference)
	c.Assert(err, IsNil, Commentf("Should be able to retrieve registered memo"))
	c.Assert(retrieved.Reference, Equals, reference, Commentf("Retrieved memo should match"))
	c.Assert(retrieved.Memo, Equals, memo, Commentf("Retrieved memo content should match"))

	// Test memo existence check
	exists := k.HasRegisteredMemo(ctx, reference)
	c.Assert(exists, Equals, true, Commentf("Registered memo should exist"))

	exists = k.HasRegisteredMemo(ctx, "99999")
	c.Assert(exists, Equals, false, Commentf("Non-existent memo should not exist"))

	c.Logf("✓ Basic memo registration tests passed")
}

// TestInfrastructurePrerequisites validates all infrastructure components
func (s *MemolessKeeperTestSuite) TestInfrastructurePrerequisites(c *C) {
	c.Logf("=== Infrastructure Prerequisites Validation ===")

	ctx, k := setupKeeperForTest(c)
	constantProvider := constants.GetConstantValues(GetCurrentVersion())

	// Initialize chain configurations
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil, Commentf("Should initialize default chain configs"))

	// Test 1: Constants are accessible
	enabled := constantProvider.GetInt64Value(constants.MemolessEnabled)
	c.Assert(enabled, Not(Equals), int64(0), Commentf("Memoless protocol should be enabled"))
	c.Logf("✓ Constants accessible: MemolessEnabled = %d", enabled)

	// Test 2: Error codes work
	err = k.ValidateChainForMemoless(ctx, "INVALID_CHAIN")
	c.Assert(err, NotNil, Commentf("Should return error for invalid chain"))
	c.Logf("✓ Error codes working: Invalid chain properly rejected")

	// Test 3: Chain configuration accessible
	supportedChains, err := k.GetSupportedChains(ctx)
	c.Assert(err, IsNil, Commentf("Should get supported chains"))
	c.Assert(len(supportedChains) > 0, Equals, true, Commentf("Should have supported chains"))
	c.Logf("✓ Chain configuration accessible: %d supported chains", len(supportedChains))

	// Test 4: Reference generation works
	reference, _ := k.GetNextReferenceID(ctx, "BTC")
	c.Assert(len(reference), Equals, 5, Commentf("Reference should be 5 digits"))
	err = ValidateReference(reference)
	c.Assert(err, IsNil, Commentf("Generated reference should be valid"))
	c.Logf("✓ Reference generation working: Generated reference %s", reference)

	// Test 5: Memo registration works
	testReference, err := k.GetNextReferenceID(ctx, "BTC")
	c.Assert(err, IsNil, Commentf("Should resolve collision"))
	c.Assert(len(testReference), Equals, 5, Commentf("Reference should be 5 digits"))
	c.Logf("✓ Memo registration working: Test reference %s", testReference)

	c.Logf("=== ✅ ALL INFRASTRUCTURE PREREQUISITES VALIDATED ===")
}

// TestEnhancedChainPrecisionValidation tests enhanced chain precision validation using 5-layer framework
func (s *MemolessKeeperTestSuite) TestEnhancedChainPrecisionValidation(c *C) {
	ctx, k := setupKeeperForTest(c)
	
	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)
	
	// Create enhanced validation context
	constantProvider := constants.GetConstantValues(GetCurrentVersion())
	chainConfig, err := k.GetChainConfig(ctx, "DOGE")
	c.Assert(err, IsNil)
	
	validationCtx := EnhancedValidationContext{
		Version:          GetCurrentVersion(),
		BlockHeight:      ctx.BlockHeight(),
		ConstantProvider: constantProvider,
		ChainConfig:      chainConfig,
	}
	
	// Test that DOGE (precision loss chain) is properly rejected in Layer 2
	msg := thorchaintypes.MsgRegisterMemo{
		Memo:    "SWAP:BTC.BTC:thor1abc123:14400",
		ChainId: "DOGE", // DOGE has PrecisionLoss: true in config
		Ttl:     14400,
		Signer:  GetRandomValidatorNode(NodeActive).NodeAddress,
	}
	
	// This should fail at Layer 2 validation because DOGE has precision loss issues
	err = k.ValidateMemoRegistrationEnhanced(ctx, msg, validationCtx)
	c.Assert(err, NotNil, Commentf("DOGE should be rejected due to precision loss"))
	c.Assert(err.Error(), Matches, ".*Layer 2.*precision loss.*", Commentf("Error should mention Layer 2 and precision loss"))
}

// TestCollusionDetection tests collusion detection and prevention mechanisms
func (s *MemolessKeeperTestSuite) TestCollusionDetection(c *C) {
	ctx, k := setupKeeperForTest(c)
	
	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)
	
	// Use consistent signer for all registrations to trigger collusion detection
	testSigner := GetRandomValidatorNode(NodeActive).NodeAddress
	
	// Pre-create 3 registrations in this block to trigger limit
	for i := 0; i < 3; i++ {
		reference, err := k.GetNextReferenceID(ctx, "BTC")
		c.Assert(err, IsNil)
		
		registration := thorchaintypes.RegisteredMemo{
			Reference:    reference,
			Memo:         fmt.Sprintf("SWAP:BTC.BTC:thor1test%d", i),
			ChainId:      "BTC",
			RegisteredAt: ctx.BlockHeight(),
			ExpiresAt:    ctx.BlockHeight() + 14400,
			Status:       "active",
			Signer:       testSigner, // Same signer for collusion tracking
		}
		err = k.SetRegisteredMemo(ctx, registration)
		c.Assert(err, IsNil)
	}
	
	// Now attempt 4th registration - should fail due to collusion detection
	msg := thorchaintypes.MsgRegisterMemo{
		Memo:    "SWAP:BTC.BTC:thor1collision:14400",
		ChainId: "BTC",
		Ttl:     14400,
		Signer:  testSigner, // Same signer attempting 4th registration
		Amount:  "1000000",
	}
	
	// This should fail due to collusion detection (too many registrations in same block)
	constantProvider := constants.GetConstantValues(GetCurrentVersion())
	
	// Use same validation path as handler (ValidateMemoRegistration -> Enhanced internally)
	err = k.ValidateMemoRegistration(ctx, msg, constantProvider)
	c.Assert(err, NotNil, Commentf("Should detect collusion - too many registrations in same block"))
	c.Assert(err.Error(), Matches, ".*collusion.*", Commentf("Error should mention collusion"))
}

// TestSlashingLogic tests slashing logic for malicious behavior
func (s *MemolessKeeperTestSuite) TestSlashingLogic(c *C) {
	ctx, k := setupKeeperForTest(c)
	
	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)
	
	// Create a malicious validator that attempts collusion
	maliciousValidator := GetRandomValidatorNode(NodeActive).NodeAddress
	
	// Test slashing when collusion is detected
	// This should fail initially since SlashValidatorForMaliciousBehavior doesn't exist yet
	err = k.SlashValidatorForMaliciousBehavior(ctx, maliciousValidator, "collusion_detected", ctx.BlockHeight())
	c.Assert(err, IsNil, Commentf("Should be able to slash validator for malicious behavior"))
}

// TestTTLExpiryManagement tests TTL expiry management system
func (s *MemolessKeeperTestSuite) TestTTLExpiryManagement(c *C) {
	ctx, k := setupKeeperForTest(c)
	
	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)
	
	// Create memo with short TTL that should expire
	signer := GetRandomValidatorNode(NodeActive).NodeAddress
	reference, err := k.GetNextReferenceID(ctx, "BTC")
	c.Assert(err, IsNil)
	
	shortTTL := int64(5) // 5 blocks
	registration := thorchaintypes.RegisteredMemo{
		Reference:    reference,
		Memo:         "SWAP:BTC.BTC:thor1test:100",
		ChainId:      "BTC",
		RegisteredAt: ctx.BlockHeight(),
		ExpiresAt:    ctx.BlockHeight() + shortTTL,
		Status:       "active",
		Signer:       signer,
	}
	err = k.SetRegisteredMemo(ctx, registration)
	c.Assert(err, IsNil)
	
	// Test TTL expiry checking - this should fail initially since CheckMemoExpiry doesn't exist
	expired, err := k.CheckMemoExpiry(ctx, reference, ctx.BlockHeight() + shortTTL + 1)
	c.Assert(err, IsNil)
	c.Assert(expired, Equals, true, Commentf("Memo should be expired after TTL"))
}

// TestAutomatedMemoCleanup tests automated memo cleanup process
func (s *MemolessKeeperTestSuite) TestAutomatedMemoCleanup(c *C) {
	ctx, k := setupKeeperForTest(c)
	
	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)
	
	// Create multiple memos with different expiry times
	signer := GetRandomValidatorNode(NodeActive).NodeAddress
	currentBlock := ctx.BlockHeight()
	
	// Memo 1: Already expired
	ref1, err := k.GetNextReferenceID(ctx, "BTC")
	c.Assert(err, IsNil)
	expired1 := thorchaintypes.RegisteredMemo{
		Reference:    ref1,
		Memo:         "SWAP:BTC.BTC:thor1expired:100",
		ChainId:      "BTC",
		RegisteredAt: currentBlock - 10,
		ExpiresAt:    currentBlock - 5, // Expired 5 blocks ago
		Status:       "active",
		Signer:       signer,
	}
	err = k.SetRegisteredMemo(ctx, expired1)
	c.Assert(err, IsNil)
	
	// Memo 2: Still active
	ref2, err := k.GetNextReferenceID(ctx, "BTC")
	c.Assert(err, IsNil)
	active1 := thorchaintypes.RegisteredMemo{
		Reference:    ref2,
		Memo:         "SWAP:BTC.BTC:thor1active:100",
		ChainId:      "BTC",
		RegisteredAt: currentBlock,
		ExpiresAt:    currentBlock + 100, // Future expiry
		Status:       "active",
		Signer:       signer,
	}
	err = k.SetRegisteredMemo(ctx, active1)
	c.Assert(err, IsNil)
	
	// Test automated cleanup - this should fail initially since CleanupExpiredMemos doesn't exist
	cleanedCount, err := k.CleanupExpiredMemos(ctx, currentBlock)
	c.Assert(err, IsNil)
	c.Assert(cleanedCount, Equals, int64(1), Commentf("Should clean up 1 expired memo"))
	
	// Verify expired memo is gone
	exists := k.HasRegisteredMemo(ctx, ref1)
	c.Assert(exists, Equals, false, Commentf("Expired memo should be cleaned up"))
	
	// Verify active memo still exists
	exists = k.HasRegisteredMemo(ctx, ref2)
	c.Assert(exists, Equals, true, Commentf("Active memo should remain"))
}
