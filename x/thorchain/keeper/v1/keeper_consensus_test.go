package keeperv1

import (
	"testing"

	. "gopkg.in/check.v1"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestKeeperConsensus(t *testing.T) { TestingT(t) }

type KeeperConsensusSuite struct{}

var _ = Suite(&KeeperConsensusSuite{})

// RED Phase Test - This should FAIL initially to drive Task 5C implementation
func (s *KeeperConsensusSuite) TestValidateCollusionConsensus_RequiresSuperMajority(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Setup: 100 total observers
	totalObservers := 100
	
	// Case 1: 70 observers report collusion (70% > 67% threshold) - should pass
	reportingObservers := make([]cosmos.AccAddress, 70)
	for i := 0; i < 70; i++ {
		reportingObservers[i] = GetRandomBech32Addr()
	}
	
	evidence := CollusionConsensusEvidence{
		CollusionEvidence: CollusionEvidence{
			NodeOperator:  GetRandomBech32Addr().String(),
			Reference:     "12345",
			CollusionType: "duplicate_reference",
			BlockHeight:   100,
		},
		ReportingObservers: reportingObservers,
		TotalObservers:     totalObservers,
	}
	
	// THIS WILL FAIL initially - method doesn't exist
	isValid, err := keeper.ValidateCollusionConsensus(ctx, evidence)
	c.Assert(err, IsNil, Commentf("Consensus validation should not error"))
	c.Assert(isValid, Equals, true, Commentf("70/100 observers (70%%) should pass 67%% threshold"))
}

func (s *KeeperConsensusSuite) TestValidateCollusionConsensus_RejectsBelowThreshold(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Setup: 100 total observers
	totalObservers := 100
	
	// Case 2: 60 observers report collusion (60% < 67% threshold) - should fail
	reportingObservers := make([]cosmos.AccAddress, 60)
	for i := 0; i < 60; i++ {
		reportingObservers[i] = GetRandomBech32Addr()
	}
	
	evidence := CollusionConsensusEvidence{
		CollusionEvidence: CollusionEvidence{
			NodeOperator:  GetRandomBech32Addr().String(),
			Reference:     "67890",
			CollusionType: "reference_hoarding",
			BlockHeight:   200,
		},
		ReportingObservers: reportingObservers,
		TotalObservers:     totalObservers,
	}
	
	// THIS WILL FAIL initially - method doesn't exist
	isValid, err := keeper.ValidateCollusionConsensus(ctx, evidence)
	c.Assert(err, IsNil, Commentf("Consensus validation should not error"))
	c.Assert(isValid, Equals, false, Commentf("60/100 observers (60%%) should fail 67%% threshold"))
}

func (s *KeeperConsensusSuite) TestValidateCollusionConsensus_ExactThreshold(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Setup: 99 total observers (to test exact 67% threshold)
	totalObservers := 99
	
	// Calculate exact 67%: 99 * 2 / 3 = 66, so need 67 to pass supermajority
	reportingObservers := make([]cosmos.AccAddress, 67)
	for i := 0; i < 67; i++ {
		reportingObservers[i] = GetRandomBech32Addr()
	}
	
	evidence := CollusionConsensusEvidence{
		CollusionEvidence: CollusionEvidence{
			NodeOperator:  GetRandomBech32Addr().String(),
			Reference:     "99999",
			CollusionType: "cross_node_collusion",
			BlockHeight:   300,
		},
		ReportingObservers: reportingObservers,
		TotalObservers:     totalObservers,
	}
	
	// THIS WILL FAIL initially - method doesn't exist
	isValid, err := keeper.ValidateCollusionConsensus(ctx, evidence)
	c.Assert(err, IsNil, Commentf("Consensus validation should not error"))
	c.Assert(isValid, Equals, true, Commentf("67/99 observers should pass exact supermajority threshold"))
}

func (s *KeeperConsensusSuite) TestValidateCollusionConsensus_SmallObserverSet(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Test edge case: 3 total observers
	totalObservers := 3
	
	// Need 2 out of 3 for supermajority (67%)
	reportingObservers := make([]cosmos.AccAddress, 2)
	for i := 0; i < 2; i++ {
		reportingObservers[i] = GetRandomBech32Addr()
	}
	
	evidence := CollusionConsensusEvidence{
		CollusionEvidence: CollusionEvidence{
			NodeOperator:  GetRandomBech32Addr().String(),
			Reference:     "11111",
			CollusionType: "duplicate_reference",
			BlockHeight:   400,
		},
		ReportingObservers: reportingObservers,
		TotalObservers:     totalObservers,
	}
	
	// THIS WILL FAIL initially - method doesn't exist
	isValid, err := keeper.ValidateCollusionConsensus(ctx, evidence)
	c.Assert(err, IsNil, Commentf("Consensus validation should not error"))
	c.Assert(isValid, Equals, true, Commentf("2/3 observers should pass supermajority"))
}

func (s *KeeperConsensusSuite) TestValidateCollusionConsensus_InvalidInput(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Test invalid cases
	testCases := []struct {
		name           string
		evidence       CollusionConsensusEvidence
		expectedValid  bool
		expectedError  bool
	}{
		{
			name: "zero_observers",
			evidence: CollusionConsensusEvidence{
				CollusionEvidence: CollusionEvidence{
					NodeOperator: GetRandomBech32Addr().String(),
					Reference:    "00000",
				},
				ReportingObservers: []cosmos.AccAddress{},
				TotalObservers:     0,
			},
			expectedValid: false,
			expectedError: true,
		},
		{
			name: "more_reporters_than_total",
			evidence: CollusionConsensusEvidence{
				CollusionEvidence: CollusionEvidence{
					NodeOperator: GetRandomBech32Addr().String(),
					Reference:    "22222",
				},
				ReportingObservers: []cosmos.AccAddress{GetRandomBech32Addr(), GetRandomBech32Addr()},
				TotalObservers:     1,
			},
			expectedValid: false,
			expectedError: true,
		},
	}
	
	for _, tc := range testCases {
		// THIS WILL FAIL initially - method doesn't exist
		isValid, err := keeper.ValidateCollusionConsensus(ctx, tc.evidence)
		
		if tc.expectedError {
			c.Assert(err, NotNil, Commentf("Case %s should return error", tc.name))
		} else {
			c.Assert(err, IsNil, Commentf("Case %s should not error", tc.name))
			c.Assert(isValid, Equals, tc.expectedValid, Commentf("Case %s validation result", tc.name))
		}
	}
}

func (s *KeeperConsensusSuite) TestRecordObserverConsensus_TracksReporting(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	evidence := CollusionConsensusEvidence{
		CollusionEvidence: CollusionEvidence{
			NodeOperator:  GetRandomBech32Addr().String(),
			Reference:     "33333",
			CollusionType: "duplicate_reference",
			BlockHeight:   500,
		},
		ReportingObservers: []cosmos.AccAddress{
			GetRandomBech32Addr(),
			GetRandomBech32Addr(),
		},
		TotalObservers: 3,
	}
	
	// THIS WILL FAIL initially - method doesn't exist
	err := keeper.RecordObserverConsensus(ctx, evidence)
	c.Assert(err, IsNil, Commentf("Recording consensus should not error"))
	
	// Verify the consensus was recorded (method to be implemented)
	// This verifies that observer evidence is stored for audit trail
	stored, err := keeper.GetCollusionConsensusRecord(ctx, evidence.Reference, evidence.NodeOperator)
	c.Assert(err, IsNil, Commentf("Retrieving consensus record should not error"))
	c.Assert(stored.TotalObservers, Equals, evidence.TotalObservers, 
		Commentf("Stored total observers should match"))
	c.Assert(len(stored.ReportingObservers), Equals, len(evidence.ReportingObservers),
		Commentf("Stored reporting observers count should match"))
}