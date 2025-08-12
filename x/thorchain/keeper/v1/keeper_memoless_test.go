package keeperv1

import (
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

// TestReferenceGeneration tests reference generation functionality
func (s *MemolessKeeperTestSuite) TestReferenceGeneration(c *C) {
	// Test reference generation
	memo1 := "SWAP:BTC.BTC::thor123"
	memo2 := "SWAP:ETH.ETH::thor456"
	height := int64(100)

	ref1 := GenerateReference(memo1, height)
	ref2 := GenerateReference(memo2, height)

	// References should be 5 digits
	c.Assert(len(ref1), Equals, 5, Commentf("Reference should be 5 digits"))
	c.Assert(len(ref2), Equals, 5, Commentf("Reference should be 5 digits"))

	// Different memos should generate different references
	c.Assert(ref1, Not(Equals), ref2, Commentf("Different memos should generate different references"))

	// Same memo and height should generate same reference (deterministic)
	ref1_repeat := GenerateReference(memo1, height)
	c.Assert(ref1, Equals, ref1_repeat, Commentf("Reference generation should be deterministic"))

	// Test reference validation
	err := ValidateReference(ref1)
	c.Assert(err, IsNil, Commentf("Generated reference should be valid"))

	err = ValidateReference("12345")
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
	constantProvider := constants.GetConstantValues(GetCurrentVersion())

	// Test basic memo registration
	memo := "SWAP:BTC.BTC::thor789"
	reference, err := k.ResolveCollision(ctx, memo, ctx.BlockHeight(), constantProvider)
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
	reference := GenerateReference("TEST:MEMO", ctx.BlockHeight())
	c.Assert(len(reference), Equals, 5, Commentf("Reference should be 5 digits"))
	err = ValidateReference(reference)
	c.Assert(err, IsNil, Commentf("Generated reference should be valid"))
	c.Logf("✓ Reference generation working: Generated reference %s", reference)

	// Test 5: Memo registration works
	testReference, err := k.ResolveCollision(ctx, "INFRA:TEST", ctx.BlockHeight(), constantProvider)
	c.Assert(err, IsNil, Commentf("Should resolve collision"))
	c.Assert(len(testReference), Equals, 5, Commentf("Reference should be 5 digits"))
	c.Logf("✓ Memo registration working: Test reference %s", testReference)

	c.Logf("=== ✅ ALL INFRASTRUCTURE PREREQUISITES VALIDATED ===")
}
