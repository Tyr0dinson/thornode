package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/constants"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// MemolessKeeperImplementationTestSuite tests the core keeper implementation
type MemolessKeeperImplementationTestSuite struct{}

var _ = Suite(&MemolessKeeperImplementationTestSuite{})

// TestMemoRegistrationFlow tests the complete memo registration flow (RED PHASE)
func (s *MemolessKeeperImplementationTestSuite) TestMemoRegistrationFlow(c *C) {
	ctx, k := setupKeeperForTest(c)
	constantProvider := constants.GetConstantValues(GetCurrentVersion())

	// Initialize chain configurations
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Test data
	signer := GetRandomBech32Addr()
	memo := "SWAP:BTC.BTC::thor1test"
	chainID := "BTC"
	ttl := int64(14400)
	amount := "100000000" // 1 BTC in satoshis

	// Create enhanced message with extended fields
	msg := thorchaintypes.MsgRegisterMemo{
		Memo:           memo,
		Ttl:            ttl,
		ChainId:        chainID,
		Signer:         signer,
		Amount:         amount,
		Destination:    "thor1destination",
		ForceCollision: false,
		Tags:           []string{"test", "memoless", "keeper"},
	}

	// RED: This should fail initially - ProcessMemoRegistration method doesn't exist yet
	registration, err := k.ProcessMemoRegistration(ctx, msg, constantProvider)
	c.Assert(err, IsNil, Commentf("ProcessMemoRegistration should be implemented"))
	c.Assert(registration, NotNil, Commentf("Registration should return valid result"))
	c.Assert(len(registration.Reference), Equals, 5, Commentf("Reference should be 5 digits"))
	c.Assert(registration.Memo, Equals, memo, Commentf("Memo should match input"))
}

// TestMemoLookupFlow tests memo lookup functionality (RED PHASE)
func (s *MemolessKeeperImplementationTestSuite) TestMemoLookupFlow(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test data
	reference := "12345"
	expectedMemo := "SWAP:ETH.ETH::thor1lookup"

	// Set up test memo
	registeredMemo := thorchaintypes.RegisteredMemo{
		Reference:    reference,
		Memo:         expectedMemo,
		ChainId:      "ETH",
		RegisteredAt: ctx.BlockHeight(),
		ExpiresAt:    ctx.BlockHeight() + 14400,
		Status:       "active",
	}
	err := k.SetRegisteredMemo(ctx, registeredMemo)
	c.Assert(err, IsNil)

	// RED: This should fail initially - LookupMemoByReference method doesn't exist yet
	result, err := k.LookupMemoByReference(ctx, reference)
	c.Assert(err, IsNil, Commentf("LookupMemoByReference should be implemented"))
	c.Assert(result, NotNil, Commentf("Lookup should return valid result"))
	c.Assert(result.Memo, Equals, expectedMemo, Commentf("Looked up memo should match"))
	c.Assert(result.IsExpired(ctx.BlockHeight()), Equals, false, Commentf("Memo should not be expired"))
}

// TestMemoValidationFlow tests comprehensive memo validation (RED PHASE)
func (s *MemolessKeeperImplementationTestSuite) TestMemoValidationFlow(c *C) {
	ctx, k := setupKeeperForTest(c)
	constantProvider := constants.GetConstantValues(GetCurrentVersion())

	// Initialize chain configurations
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Test valid memo
	validMsg := thorchaintypes.MsgRegisterMemo{
		Memo:    "SWAP:BTC.BTC::thor1valid",
		Ttl:     14400,
		ChainId: "BTC",
		Signer:  GetRandomBech32Addr(),
		Amount:  "100000000", // 1 BTC - meets minimum for 5-digit reference encoding
	}

	// RED: This should fail initially - ValidateMemoRegistrationComplete method doesn't exist yet
	err = k.ValidateMemoRegistrationComplete(ctx, validMsg, constantProvider)
	c.Assert(err, IsNil, Commentf("ValidateMemoRegistrationComplete should accept valid memo"))

	// Test invalid memo (empty)
	invalidMsg := thorchaintypes.MsgRegisterMemo{
		Memo:    "",
		Ttl:     14400,
		ChainId: "BTC",
		Signer:  GetRandomBech32Addr(),
	}

	err = k.ValidateMemoRegistrationComplete(ctx, invalidMsg, constantProvider)
	c.Assert(err, NotNil, Commentf("ValidateMemoRegistrationComplete should reject empty memo"))
}

// TestCollisionResolutionFlow tests collision handling (RED PHASE)
func (s *MemolessKeeperImplementationTestSuite) TestCollisionResolutionFlow(c *C) {
	ctx, k := setupKeeperForTest(c)
	constantProvider := constants.GetConstantValues(GetCurrentVersion())

	memo := "COLLISION:TEST"
	height := ctx.BlockHeight()

	// RED: This should fail initially - HandleCollisionResolution method doesn't exist yet
	reference1, err := k.HandleCollisionResolution(ctx, memo, height, constantProvider)
	c.Assert(err, IsNil, Commentf("HandleCollisionResolution should be implemented"))
	c.Assert(len(reference1), Equals, 5, Commentf("First reference should be 5 digits"))

	// Create collision by storing first reference
	registeredMemo := thorchaintypes.RegisteredMemo{
		Reference:    reference1,
		Memo:         memo,
		ChainId:      "BTC",
		RegisteredAt: height,
		ExpiresAt:    height + 14400,
		Status:       "active",
	}
	err = k.SetRegisteredMemo(ctx, registeredMemo)
	c.Assert(err, IsNil)

	// Second attempt should generate different reference
	reference2, err := k.HandleCollisionResolution(ctx, memo, height, constantProvider)
	c.Assert(err, IsNil, Commentf("Second collision resolution should succeed"))
	c.Assert(len(reference2), Equals, 5, Commentf("Second reference should be 5 digits"))
	c.Assert(reference1, Not(Equals), reference2, Commentf("References should be different"))
}

// TestMemoExpirationFlow tests memo expiration handling (RED PHASE)
func (s *MemolessKeeperImplementationTestSuite) TestMemoExpirationFlow(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Create expired memo
	expiredMemo := thorchaintypes.RegisteredMemo{
		Reference:    "99999",
		Memo:         "EXPIRED:TEST",
		ChainId:      "BTC",
		RegisteredAt: ctx.BlockHeight() - 20000,
		ExpiresAt:    ctx.BlockHeight() - 1000, // Expired 1000 blocks ago
		Status:       "active",
	}
	err := k.SetRegisteredMemo(ctx, expiredMemo)
	c.Assert(err, IsNil)

	// RED: This should fail initially - CleanupExpiredMemos method doesn't exist yet
	cleanedCount, err := k.CleanupExpiredMemos(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil, Commentf("CleanupExpiredMemos should be implemented"))
	c.Assert(cleanedCount > 0, Equals, true, Commentf("Should clean up at least 1 expired memo"))

	// Verify memo is marked as expired
	retrievedMemo, err := k.GetRegisteredMemo(ctx, "99999")
	if err == nil {
		c.Assert(retrievedMemo.Status, Equals, "expired", Commentf("Memo should be marked as expired"))
	}
}
