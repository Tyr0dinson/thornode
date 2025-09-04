package keeperv1

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/constants"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type MemolessChaosTestSuite struct{}

var _ = Suite(&MemolessChaosTestSuite{})

// TestUserReferenceCollisionRecovery tests collision recovery with user-provided references
func (s *MemolessChaosTestSuite) TestUserReferenceCollisionRecovery(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Test collision detection with user references
	signer := GetRandomValidatorNode(NodeActive).NodeAddress

	// Register first memo with reference "15432"
	testMemo := thorchaintypes.RegisteredMemo{
		Reference:    "15432",
		Memo:         "SWAP:BTC.BTC:thor1test:14400",
		ChainId:      "BTC",
		RegisteredAt: ctx.BlockHeight(),
		ExpiresAt:    ctx.BlockHeight() + 14400,
		Status:       "active",
		Signer:       signer,
	}
	err = k.SetRegisteredMemo(ctx, testMemo)
	c.Assert(err, IsNil)

	// Test that user cannot reuse same reference
	err = k.ValidateUserReference(ctx, "15432")
	c.Assert(err, NotNil, Commentf("Should reject duplicate user reference"))

	// Test that user can use different reference
	err = k.ValidateUserReference(ctx, "15433")
	c.Assert(err, IsNil, Commentf("Should accept different user reference"))
}

// TestUserReferenceLoadTesting tests user reference validation under load
func (s *MemolessChaosTestSuite) TestUserReferenceLoadTesting(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Test multiple user reference validations
	successCount := 0
	totalAttempts := 50

	for i := 0; i < totalAttempts; i++ {
		// Generate unique reference for each attempt
		reference := fmt.Sprintf("%05d", 10000+i)
		err := k.ValidateUserReference(ctx, reference)
		if err == nil {
			successCount++
		}
	}

	c.Assert(successCount > 0, Equals, true, Commentf("Should have some successful validations"))
	c.Assert(successCount <= totalAttempts, Equals, true, Commentf("Success count should not exceed total attempts"))
}

// TestUserReferenceValidationIntegrity tests reference validation consistency
func (s *MemolessChaosTestSuite) TestUserReferenceValidationIntegrity(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Test validation consistency with edge cases
	testCases := []struct {
		reference  string
		shouldPass bool
		reason     string
	}{
		{"10000", true, "minimum valid reference"},
		{"99999", true, "maximum valid reference"},
		{"09999", false, "below minimum range"},
		{"100000", false, "above maximum range"},
		{"1234", false, "too short"},
		{"123456", false, "too long"},
		{"abcde", false, "non-numeric"},
	}

	for _, tc := range testCases {
		err := k.ValidateUserReference(ctx, tc.reference)
		if tc.shouldPass {
			c.Assert(err, IsNil, Commentf("Reference %s should pass: %s", tc.reference, tc.reason))
		} else {
			c.Assert(err, NotNil, Commentf("Reference %s should fail: %s", tc.reference, tc.reason))
		}
	}
}

// TestUserReferenceBoundaryConditions tests boundary conditions for user references
func (s *MemolessChaosTestSuite) TestUserReferenceBoundaryConditions(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Test exact boundaries of valid reference range
	boundaryTests := []string{
		"09999", // Just below minimum (should fail)
		"10000", // Minimum valid (should pass)
		"10001", // Just above minimum (should pass)
		"99998", // Just below maximum (should pass)
		"99999", // Maximum valid (should pass)
	}

	expectedResults := []bool{false, true, true, true, true}

	for i, ref := range boundaryTests {
		err := k.ValidateUserReference(ctx, ref)
		if expectedResults[i] {
			c.Assert(err, IsNil, Commentf("Boundary reference %s should be valid", ref))
		} else {
			c.Assert(err, NotNil, Commentf("Boundary reference %s should be invalid", ref))
		}
	}
}

// TestMemoStorageConsistency tests storage consistency with user references
func (s *MemolessChaosTestSuite) TestMemoStorageConsistency(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	signer := GetRandomValidatorNode(NodeActive).NodeAddress

	// Test storage and retrieval consistency
	testReference := "25000"
	testMemo := thorchaintypes.RegisteredMemo{
		Reference:    testReference,
		Memo:         "SWAP:BTC.BTC:thor1storage:14400",
		ChainId:      "BTC",
		RegisteredAt: ctx.BlockHeight(),
		ExpiresAt:    ctx.BlockHeight() + 14400,
		Status:       "active",
		Signer:       signer,
	}

	// Store memo
	err = k.SetRegisteredMemo(ctx, testMemo)
	c.Assert(err, IsNil, Commentf("Should store memo successfully"))

	// Retrieve and verify
	retrieved, err := k.GetRegisteredMemo(ctx, testReference)
	c.Assert(err, IsNil, Commentf("Should retrieve memo successfully"))
	c.Assert(retrieved.Reference, Equals, testReference, Commentf("Retrieved reference should match"))
	c.Assert(retrieved.Memo, Equals, testMemo.Memo, Commentf("Retrieved memo should match"))
}

// TestMemoRetrievalResilience tests memo retrieval under various conditions
func (s *MemolessChaosTestSuite) TestMemoRetrievalResilience(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	constantProvider := constants.GetConstantValues(GetCurrentVersion())
	signer := GetRandomValidatorNode(NodeActive).NodeAddress

	// Step 1: Register memo with user reference
	msg := thorchaintypes.MsgRegisterMemo{
		Memo:      "SWAP:BTC.BTC:thor1resilience:14400",
		ChainId:   "BTC",
		Ttl:       14400,
		Signer:    signer,
		Reference: "35000", // User-provided reference
	}

	result, err := k.ProcessMemoRegistration(ctx, msg, constantProvider)
	c.Assert(err, IsNil, Commentf("Should register memo successfully"))

	// Step 2: Verify memo can be retrieved multiple times consistently
	for i := 0; i < 5; i++ {
		retrieved, err := k.LookupMemoByReference(ctx, result.Reference)
		c.Assert(err, IsNil, Commentf("Should be able to lookup memo consistently (attempt %d)", i+1))
		c.Assert(retrieved.Status, Equals, "active", Commentf("Memo should remain active"))
		c.Assert(retrieved.Reference, Equals, "35000", Commentf("Reference should remain consistent"))
	}
}

// TestUserReferenceRobustness tests robustness of user reference handling
func (s *MemolessChaosTestSuite) TestUserReferenceRobustness(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Test various edge cases for user references
	signer := GetRandomValidatorNode(NodeActive).NodeAddress

	// Test 1: Register memo with user reference
	testReference := "45000"
	validMemo := thorchaintypes.RegisteredMemo{
		Reference:    testReference,
		Memo:         "SWAP:BTC.BTC:thor1robust:14400",
		ChainId:      "BTC",
		RegisteredAt: ctx.BlockHeight(),
		ExpiresAt:    ctx.BlockHeight() + 14400,
		Status:       "active",
		Signer:       signer,
	}
	err = k.SetRegisteredMemo(ctx, validMemo)
	c.Assert(err, IsNil, Commentf("Should register memo with user reference"))

	// Test 2: Verify reference collision detection
	err = k.ValidateUserReference(ctx, testReference)
	c.Assert(err, NotNil, Commentf("Should detect reference collision"))

	// Test 3: Verify different reference works
	err = k.ValidateUserReference(ctx, "45001")
	c.Assert(err, IsNil, Commentf("Different reference should be valid"))

	// Test 4: Verify retrieval consistency
	retrieved, err := k.GetRegisteredMemo(ctx, testReference)
	c.Assert(err, IsNil, Commentf("Should retrieve memo by user reference"))
	c.Assert(retrieved.Reference, Equals, testReference, Commentf("Retrieved reference should match"))
}
