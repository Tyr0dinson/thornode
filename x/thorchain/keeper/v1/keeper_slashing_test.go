package keeperv1

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

func TestKeeperSlashing(t *testing.T) { TestingT(t) }

type KeeperSlashingSuite struct{}

var _ = Suite(&KeeperSlashingSuite{})

// RED Phase Test - This should FAIL initially to drive collusion slashing implementation
func (s *KeeperSlashingSuite) TestSlashNodeForCollusion_ReducesBond(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Setup node with bond
	nodeAddr := GetRandomBech32Addr()
	originalBond := cosmos.NewUint(1000000000) // 10 RUNE

	// Fund and setup node account with bond
	FundModule(c, ctx, keeper, ModuleName, originalBond.Uint64())
	err := setupNodeWithBond(ctx, keeper, nodeAddr, originalBond)
	c.Assert(err, IsNil)

	// Verify initial bond amount
	nodeAccount, err := keeper.GetNodeAccount(ctx, nodeAddr)
	c.Assert(err, IsNil)
	c.Assert(nodeAccount.Bond.Equal(originalBond), Equals, true, Commentf("Initial bond should be %s", originalBond))

	// Get initial slash points
	slashPointsBefore, err := keeper.GetNodeAccountSlashPoints(ctx, nodeAddr)
	c.Assert(err, IsNil)

	// Evidence of collusion
	evidence := CollusionEvidence{
		NodeOperator:  nodeAddr.String(),
		Reference:     "12345",
		CollusionType: "duplicate_reference",
		BlockHeight:   100,
		EvidenceHash:  "0x1234567890abcdef",
	}

	// Execute slashing - THIS WILL FAIL initially
	err = keeper.SlashNodeForCollusion(ctx, nodeAddr, evidence)
	c.Assert(err, IsNil, Commentf("Slashing execution should not error"))

	// Verify bond reduced
	nodeAccountAfter, err := keeper.GetNodeAccount(ctx, nodeAddr)
	c.Assert(err, IsNil)
	c.Assert(nodeAccountAfter.Bond.LT(originalBond), Equals, true,
		Commentf("Bond should be reduced from %s to %s", originalBond, nodeAccountAfter.Bond))

	// Verify slash points increased
	slashPointsAfter, err := keeper.GetNodeAccountSlashPoints(ctx, nodeAddr)
	c.Assert(err, IsNil)
	c.Assert(slashPointsAfter > slashPointsBefore, Equals, true,
		Commentf("Slash points should increase from %d to %d", slashPointsBefore, slashPointsAfter))
}

func (s *KeeperSlashingSuite) TestSlashNodeForCollusion_CalculatesCorrectPenalty(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	nodeAddr := GetRandomBech32Addr()
	originalBond := cosmos.NewUint(10000000000) // 100 RUNE

	// Setup node
	FundModule(c, ctx, keeper, ModuleName, originalBond.Uint64())
	err := setupNodeWithBond(ctx, keeper, nodeAddr, originalBond)
	c.Assert(err, IsNil)

	evidence := CollusionEvidence{
		NodeOperator:  nodeAddr.String(),
		Reference:     "67890",
		CollusionType: "duplicate_reference",
		BlockHeight:   200,
	}

	// Execute slashing - THIS WILL FAIL initially
	err = keeper.SlashNodeForCollusion(ctx, nodeAddr, evidence)
	c.Assert(err, IsNil)

	// Verify penalty amount follows expected formula
	nodeAccountAfter, err := keeper.GetNodeAccount(ctx, nodeAddr)
	c.Assert(err, IsNil)

	slashedAmount := originalBond.Sub(nodeAccountAfter.Bond)
	expectedMinSlash := cosmos.NewUint(100000000) // 1 RUNE minimum for duplicate_reference

	c.Assert(slashedAmount.GTE(expectedMinSlash), Equals, true,
		Commentf("Slashed amount %s should be at least %s", slashedAmount, expectedMinSlash))
}

func (s *KeeperSlashingSuite) TestSlashNodeForCollusion_HandlesInsufficientBond(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	nodeAddr := GetRandomBech32Addr()
	smallBond := cosmos.NewUint(50000000) // 0.5 RUNE - very small bond

	// Setup node with small bond
	FundModule(c, ctx, keeper, ModuleName, smallBond.Uint64())
	err := setupNodeWithBond(ctx, keeper, nodeAddr, smallBond)
	c.Assert(err, IsNil)

	evidence := CollusionEvidence{
		NodeOperator:  nodeAddr.String(),
		Reference:     "99999",
		CollusionType: "duplicate_reference",
		BlockHeight:   300,
	}

	// Execute slashing - should handle gracefully
	err = keeper.SlashNodeForCollusion(ctx, nodeAddr, evidence)
	c.Assert(err, IsNil, Commentf("Should handle insufficient bond gracefully"))

	// Verify node still exists but bond reduced
	nodeAccountAfter, err := keeper.GetNodeAccount(ctx, nodeAddr)
	c.Assert(err, IsNil)
	c.Assert(nodeAccountAfter.Bond.LTE(smallBond), Equals, true,
		Commentf("Bond should be reduced or stay same if insufficient"))
}

func (s *KeeperSlashingSuite) TestGetBasePenaltyForCollusionType_ReturnsCorrectAmounts(c *C) {
	_, keeper := setupKeeperForTest(c)

	testCases := []struct {
		collusionType    string
		expectedMinSlash cosmos.Uint
	}{
		{"duplicate_reference", cosmos.NewUint(100000000)},   // 1 RUNE
		{"reference_hoarding", cosmos.NewUint(500000000)},    // 5 RUNE
		{"cross_node_collusion", cosmos.NewUint(2000000000)}, // 20 RUNE
	}

	for _, tc := range testCases {
		// THIS WILL FAIL initially - method doesn't exist
		penalty := keeper.GetBasePenaltyForCollusionType(tc.collusionType)
		c.Assert(penalty.GTE(tc.expectedMinSlash), Equals, true,
			Commentf("Penalty for %s should be at least %s, got %s",
				tc.collusionType, tc.expectedMinSlash, penalty))
	}
}

// Helper function to setup a node account with bond
func setupNodeWithBond(ctx cosmos.Context, keeper KVStore, nodeAddr cosmos.AccAddress, bondAmount cosmos.Uint) error {
	// Create node account with proper parameters
	na := NewNodeAccount(
		nodeAddr,
		NodeActive,
		common.PubKeySet{}, // Empty pubkey set for test
		"",                 // Empty validator consensus pubkey
		bondAmount,
		common.NoAddress, // No bond address for test
		ctx.BlockHeight(),
	)
	na.Type = NodeTypeValidator
	na.Version = GetCurrentVersion().String()

	// Set the node account
	return keeper.SetNodeAccount(ctx, na)
}
