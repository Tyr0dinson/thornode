package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/constants"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type TTLConfigurationSuite struct{}

var _ = Suite(&TTLConfigurationSuite{})

func (s *TTLConfigurationSuite) TestMimirTTLConfiguration_UpdatesDynamically(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Setup: Initial TTL from constants
	constantProvider := constants.GetConstantValues(GetCurrentVersion())
	initialTTL := constantProvider.GetInt64Value(constants.MemolessTTL)
	c.Assert(initialTTL, Equals, int64(14400)) // 4 hours default

	// Test: Update TTL via Mimir (simulated with SetMimir)
	keeper.SetMimir(ctx, constants.MemolessTTL.String(), 7200) // 2 hours

	// Verify: New registrations use updated TTL
	newTTL := keeper.GetCurrentTTL(ctx)
	c.Assert(newTTL, Equals, int64(7200)) // THIS WILL FAIL initially
}

func (s *TTLConfigurationSuite) TestMimirTTLConfiguration_ValidatesMaximum(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Test: Attempt to set TTL beyond maximum
	err := keeper.SetTTL(ctx, 2000000) // Exceeds 1 week maximum (1008000)

	c.Assert(err, NotNil) // THIS WILL FAIL initially
	c.Check(err.Error(), Matches, ".*exceeds maximum TTL.*")

	// Verify: TTL remains at previous value
	currentTTL := keeper.GetCurrentTTL(ctx)
	c.Assert(currentTTL, Equals, int64(14400)) // Should be default
}

func (s *TTLConfigurationSuite) TestMimirTTLConfiguration_ExistingRegistrationsUnaffected(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Setup: Register memo with current TTL
	registration := createTestRegistration("12345", "SWAP:BTC.BTC:addr", ctx.BlockHeight())
	err := keeper.SetRegisteredMemo(ctx, registration)
	c.Assert(err, IsNil)

	// Update TTL via Mimir
	keeper.SetMimir(ctx, constants.MemolessTTL.String(), 7200) // 2 hours

	// Verify: Existing registration keeps original expiry
	retrieved, err := keeper.GetRegisteredMemo(ctx, "12345")
	c.Assert(err, IsNil)
	c.Assert(retrieved.ExpiresAt, Equals, registration.ExpiresAt) // Original expiry preserved
}

func (s *TTLConfigurationSuite) TestMimirTTLConfiguration_ValidatesMinimum(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Test: Attempt to set TTL below minimum (10 minutes)
	err := keeper.SetTTL(ctx, 500) // 500 blocks < 600 (10 minutes)

	c.Assert(err, NotNil) // Should fail for minimum TTL validation
	c.Assert(err.Error(), Equals, "TTL 500 too short: 500 blocks (minimum 600)")
}

// Helper function to create test registration
func createTestRegistration(reference string, memo string, blockHeight int64) thorchaintypes.RegisteredMemo {
	return thorchaintypes.RegisteredMemo{
		Reference:    reference,
		Memo:         memo,
		ChainId:      "BTC",
		RegisteredAt: blockHeight,
		ExpiresAt:    blockHeight + 14400, // 4 hours default
		Status:       "active",
	}
}
