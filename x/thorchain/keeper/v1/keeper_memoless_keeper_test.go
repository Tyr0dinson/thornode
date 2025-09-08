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
    // ttl removed from MsgRegisterMemo in v1 (global TTL applied)

	// Create enhanced message with extended fields
    msg := thorchaintypes.MsgRegisterMemo{
        Memo:      memo,
        ChainId:   chainID,
        Signer:    signer,
        Reference: "12000", // User-provided 5-digit reference
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
        Memo:      "SWAP:BTC.BTC::thor1valid",
        ChainId:   "BTC",
        Signer:    GetRandomBech32Addr(),
        Reference: "15000", // User-provided 5-digit reference
    }

	// RED: This should fail initially - ValidateMemoRegistrationComplete method doesn't exist yet
	err = k.ValidateMemoRegistrationComplete(ctx, validMsg, constantProvider)
	c.Assert(err, IsNil, Commentf("ValidateMemoRegistrationComplete should accept valid memo"))

	// Test invalid memo (empty)
    invalidMsg := thorchaintypes.MsgRegisterMemo{
        Memo:    "",
        ChainId: "BTC",
        Signer:  GetRandomBech32Addr(),
    }

	err = k.ValidateMemoRegistrationComplete(ctx, invalidMsg, constantProvider)
	c.Assert(err, NotNil, Commentf("ValidateMemoRegistrationComplete should reject empty memo"))
}

// TestCollisionResolutionFlow tests collision handling (RED PHASE)
func (s *MemolessKeeperImplementationTestSuite) TestCollisionResolutionFlow(c *C) {
	ctx, k := setupKeeperForTest(c)

	memo := "COLLISION:TEST"
	height := ctx.BlockHeight()

	// Test user reference collision detection
	reference1 := "25000"
	err := k.ValidateUserReference(ctx, reference1)
	c.Assert(err, IsNil, Commentf("First reference should be valid"))

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

	// Second attempt with same reference should fail
	err = k.ValidateUserReference(ctx, reference1)
	c.Assert(err, NotNil, Commentf("Duplicate reference should be rejected"))

	// Different reference should succeed
	reference2 := "25001"
	err = k.ValidateUserReference(ctx, reference2)
	c.Assert(err, IsNil, Commentf("Different reference should be valid"))
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

	// Test expiration detection
	retrievedMemo, err := k.GetRegisteredMemo(ctx, "99999")
	c.Assert(err, IsNil, Commentf("Should retrieve expired memo"))
	c.Assert(retrievedMemo.IsExpired(ctx.BlockHeight()), Equals, true, Commentf("Memo should be detected as expired"))

	// Test that expired memo lookup handles expiration properly
	lookupResult, err := k.LookupMemoByReference(ctx, "99999")
	if err == nil {
		c.Assert(lookupResult.IsExpired(ctx.BlockHeight()), Equals, true, Commentf("Lookup should detect expiration"))
	}
}
