package keeperv1

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

func TestKeeperProgressive(t *testing.T) { TestingT(t) }

type KeeperProgressiveSuite struct{}

var _ = Suite(&KeeperProgressiveSuite{})

// RED Phase Test - This should FAIL initially to drive Task 5D implementation
func (s *KeeperProgressiveSuite) TestCalculateProgressivePenalty_EscalatesWithHistory(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Setup node with different slash point histories
	nodeAddr := GetRandomBech32Addr()

	testCases := []struct {
		name             string
		existingPoints   int64
		collusionType    string
		expectedMinBonus float64 // Minimum escalation factor
		expectedMaxBonus float64 // Maximum escalation factor
	}{
		{
			name:             "first_offense_no_escalation",
			existingPoints:   0,
			collusionType:    "duplicate_reference",
			expectedMinBonus: 1.0,
			expectedMaxBonus: 1.0,
		},
		{
			name:             "moderate_history_small_escalation",
			existingPoints:   5000, // 5000 slash points accumulated
			collusionType:    "duplicate_reference",
			expectedMinBonus: 1.5, // 50% escalation
			expectedMaxBonus: 2.0,
		},
		{
			name:             "high_history_significant_escalation",
			existingPoints:   15000, // 15000 slash points accumulated
			collusionType:    "duplicate_reference",
			expectedMinBonus: 2.5, // 150% escalation
			expectedMaxBonus: 3.0,
		},
		{
			name:             "severe_offense_with_history",
			existingPoints:   10000, // 10000 slash points
			collusionType:    "cross_node_collusion",
			expectedMinBonus: 2.0, // More severe offense gets higher escalation
			expectedMaxBonus: 4.0,
		},
	}

	for _, tc := range testCases {
		// Setup existing slash points
		keeper.SetNodeAccountSlashPoints(ctx, nodeAddr, tc.existingPoints)

		// THIS WILL FAIL initially - method doesn't exist
		result, err := keeper.CalculateProgressivePenalty(ctx, nodeAddr, tc.collusionType)
		c.Assert(err, IsNil, Commentf("Case %s should not error", tc.name))

		// Verify escalation factor is within expected range
		c.Assert(result.EscalationFactor >= tc.expectedMinBonus, Equals, true,
			Commentf("Case %s: escalation factor %f should be >= %f",
				tc.name, result.EscalationFactor, tc.expectedMinBonus))
		c.Assert(result.EscalationFactor <= tc.expectedMaxBonus, Equals, true,
			Commentf("Case %s: escalation factor %f should be <= %f",
				tc.name, result.EscalationFactor, tc.expectedMaxBonus))

		// Verify final penalty is greater than base penalty (unless first offense)
		if tc.existingPoints > 0 {
			c.Assert(result.FinalPenalty.GT(result.BasePenalty), Equals, true,
				Commentf("Case %s: final penalty should exceed base penalty for repeat offenses", tc.name))
		} else {
			c.Assert(result.FinalPenalty.Equal(result.BasePenalty), Equals, true,
				Commentf("Case %s: first offense should have no escalation", tc.name))
		}

		// Verify slash points used matches existing points
		c.Assert(result.SlashPointsUsed, Equals, tc.existingPoints,
			Commentf("Case %s: should use existing slash points for calculation", tc.name))
	}
}

func (s *KeeperProgressiveSuite) TestCalculateProgressivePenalty_RespectsMaximumCaps(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	nodeAddr := GetRandomBech32Addr()

	// Test extreme slash point scenarios
	extremeCases := []struct {
		name           string
		existingPoints int64
		collusionType  string
		maxFactor      float64
	}{
		{
			name:           "extreme_slash_points_capped",
			existingPoints: 100000, // Very high slash points
			collusionType:  "duplicate_reference",
			maxFactor:      5.0, // Should be capped at reasonable maximum
		},
		{
			name:           "negative_slash_points_handled",
			existingPoints: -1000, // Edge case: negative points
			collusionType:  "duplicate_reference",
			maxFactor:      1.0, // Should default to no escalation
		},
	}

	for _, tc := range extremeCases {
		keeper.SetNodeAccountSlashPoints(ctx, nodeAddr, tc.existingPoints)

		// THIS WILL FAIL initially - method doesn't exist
		result, err := keeper.CalculateProgressivePenalty(ctx, nodeAddr, tc.collusionType)
		c.Assert(err, IsNil, Commentf("Case %s should not error", tc.name))

		// Verify escalation factor doesn't exceed reasonable maximum
		c.Assert(result.EscalationFactor <= tc.maxFactor, Equals, true,
			Commentf("Case %s: escalation factor %f should not exceed maximum %f",
				tc.name, result.EscalationFactor, tc.maxFactor))
	}
}

func (s *KeeperProgressiveSuite) TestApplyProgressivePenaltyToSlashing_IntegratesWithExisting(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Setup node with bond and slash point history
	nodeAddr := GetRandomBech32Addr()
	originalBond := cosmos.NewUint(10000000000) // 100 RUNE
	existingSlashPoints := int64(8000)          // 8000 slash points history

	// Setup node and history
	FundModule(c, ctx, keeper, ModuleName, originalBond.Uint64())
	err := setupNodeWithBond(ctx, keeper, nodeAddr, originalBond)
	c.Assert(err, IsNil)
	keeper.SetNodeAccountSlashPoints(ctx, nodeAddr, existingSlashPoints)

	// Create evidence
	evidence := CollusionEvidence{
		NodeOperator:  nodeAddr.String(),
		Reference:     "progressive-test",
		CollusionType: "duplicate_reference",
		BlockHeight:   100,
	}

	// THIS WILL FAIL initially - method doesn't exist
	err = keeper.SlashNodeForCollusionWithProgression(ctx, nodeAddr, evidence)
	c.Assert(err, IsNil, Commentf("Progressive slashing should not error"))

	// Verify the penalty was escalated based on history
	nodeAccountAfter, err := keeper.GetNodeAccount(ctx, nodeAddr)
	c.Assert(err, IsNil)

	// Calculate what base penalty would be
	basePenalty := keeper.GetBasePenaltyForCollusionType(evidence.CollusionType)
	slashedAmount := originalBond.Sub(nodeAccountAfter.Bond)

	// With 8000 slash points, penalty should be escalated
	c.Assert(slashedAmount.GT(basePenalty), Equals, true,
		Commentf("Slashed amount %s should exceed base penalty %s due to progression",
			slashedAmount, basePenalty))
}

func (s *KeeperProgressiveSuite) TestProgressivePenaltyFormula_FollowsSpecification(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	nodeAddr := GetRandomBech32Addr()

	// Test specific formula cases based on spec
	formulaCases := []struct {
		name              string
		slashPoints       int64
		expectedMinFactor float64
		expectedMaxFactor float64
		description       string
	}{
		{
			name:              "low_points_minimal_escalation",
			slashPoints:       1000,
			expectedMinFactor: 1.0,
			expectedMaxFactor: 1.2,
			description:       "1000 points should have minimal escalation",
		},
		{
			name:              "medium_points_moderate_escalation",
			slashPoints:       5000,
			expectedMinFactor: 1.4,
			expectedMaxFactor: 2.0,
			description:       "5000 points should have moderate escalation",
		},
		{
			name:              "high_points_significant_escalation",
			slashPoints:       10000,
			expectedMinFactor: 2.0,
			expectedMaxFactor: 3.0,
			description:       "10000 points should have significant escalation",
		},
	}

	for _, tc := range formulaCases {
		keeper.SetNodeAccountSlashPoints(ctx, nodeAddr, tc.slashPoints)

		// THIS WILL FAIL initially - method doesn't exist
		result, err := keeper.CalculateProgressivePenalty(ctx, nodeAddr, "duplicate_reference")
		c.Assert(err, IsNil, Commentf("Formula case %s should not error", tc.name))

		c.Assert(result.EscalationFactor >= tc.expectedMinFactor, Equals, true,
			Commentf("%s: factor %f should be >= %f", tc.description, result.EscalationFactor, tc.expectedMinFactor))
		c.Assert(result.EscalationFactor <= tc.expectedMaxFactor, Equals, true,
			Commentf("%s: factor %f should be <= %f", tc.description, result.EscalationFactor, tc.expectedMaxFactor))
	}
}

func (s *KeeperProgressiveSuite) TestGetSlashPointsEscalationFactor_PureMath(c *C) {
	_, keeper := setupKeeperForTest(c)

	// Test the pure mathematical escalation formula
	mathCases := []struct {
		slashPoints    int64
		expectedFactor float64
	}{
		{0, 1.0},     // No history = no escalation
		{1000, 1.1},  // Small escalation
		{5000, 1.5},  // Medium escalation
		{10000, 2.0}, // Significant escalation
		{20000, 3.0}, // High escalation
	}

	for _, tc := range mathCases {
		// THIS WILL FAIL initially - method doesn't exist
		factor := keeper.GetSlashPointsEscalationFactor(tc.slashPoints)

		// Allow small floating point differences
		diff := factor - tc.expectedFactor
		if diff < 0 {
			diff = -diff
		}
		c.Assert(diff < 0.1, Equals, true,
			Commentf("Escalation factor for %d points should be ~%f, got %f",
				tc.slashPoints, tc.expectedFactor, factor))
	}
}
