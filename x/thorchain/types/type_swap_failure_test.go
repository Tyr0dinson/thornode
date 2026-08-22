package types

import (
	"unicode/utf8"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (MsgSwapSuite) TestLegacyFailureHistoryCompactionPreservesProgress(c *C) {
	const legacyFailures = 65
	failedSwaps := make([]uint64, legacyFailures)
	failedReasons := make([]string, legacyFailures)
	for i := range failedSwaps {
		failedSwaps[i] = uint64(i)
		failedReasons[i] = "legacy failure"
	}

	swap := &MsgSwap{
		TradeTarget: cosmos.NewUint(222),
		State: &SwapState{
			Quantity:          20,
			Count:             75,
			Deposit:           cosmos.NewUint(111),
			In:                cosmos.ZeroUint(),
			Out:               cosmos.ZeroUint(),
			FailedSwaps:       failedSwaps,
			FailedSwapReasons: failedReasons,
		},
	}

	c.Assert(swap.FailCount(), Equals, uint64(legacyFailures))
	c.Assert(swap.SuccessCount(), Equals, uint64(10))
	legacySize, legacyTarget := swap.NextSize()

	swap.State.CompactFailureHistory()

	compactedSize, compactedTarget := swap.NextSize()
	c.Assert(swap.State.FailedSwapCount, Equals, uint64(legacyFailures))
	c.Assert(swap.FailCount(), Equals, uint64(legacyFailures))
	c.Assert(swap.SuccessCount(), Equals, uint64(10))
	c.Assert(compactedSize.Equal(legacySize), Equals, true)
	c.Assert(compactedTarget.Equal(legacyTarget), Equals, true)
	c.Assert(swap.State.FailedSwaps, DeepEquals, failedSwaps[1:])
	c.Assert(swap.State.FailedSwapReasons, DeepEquals, failedReasons[1:])
}

func (MsgSwapSuite) TestCorruptFailureCountSaturatesSuccessCount(c *C) {
	swap := &MsgSwap{
		State: &SwapState{
			Count:           3,
			FailedSwapCount: 5,
			FailedSwaps:     []uint64{1, 2},
		},
	}

	c.Assert(swap.FailCount(), Equals, uint64(5))
	c.Assert(swap.SuccessCount(), Equals, uint64(0))
}

func (MsgSwapSuite) TestMismatchedFailureHistoriesCompactToPairedSuffix(c *C) {
	tests := []struct {
		name             string
		state            SwapState
		expectedFailures uint64
		expectedSwaps    []uint64
		expectedReasons  []string
	}{
		{
			name: "missing older reasons",
			state: SwapState{
				FailedSwaps:       []uint64{10, 11, 12, 13, 14},
				FailedSwapReasons: []string{"failure 12", "failure 13", "failure 14"},
			},
			expectedFailures: 5,
			expectedSwaps:    []uint64{12, 13, 14},
			expectedReasons:  []string{"failure 12", "failure 13", "failure 14"},
		},
		{
			name: "missing older indexes",
			state: SwapState{
				FailedSwapCount:   7,
				FailedSwaps:       []uint64{12, 13, 14},
				FailedSwapReasons: []string{"failure 10", "failure 11", "failure 12", "failure 13", "failure 14"},
			},
			expectedFailures: 7,
			expectedSwaps:    []uint64{12, 13, 14},
			expectedReasons:  []string{"failure 12", "failure 13", "failure 14"},
		},
	}

	for _, test := range tests {
		test.state.CompactFailureHistory()
		c.Check(test.state.FailureCount(), Equals, test.expectedFailures, Commentf(test.name))
		c.Check(test.state.FailedSwaps, DeepEquals, test.expectedSwaps, Commentf(test.name))
		c.Check(test.state.FailedSwapReasons, DeepEquals, test.expectedReasons, Commentf(test.name))
	}
}

func (MsgSwapSuite) TestShortInvalidFailureReasonsAreNormalized(c *C) {
	invalidReason := string([]byte{'f', 'a', 'i', 'l', ':', 0xff, 'x'})
	c.Assert(len(invalidReason) < MaxSwapFailureReasonBytes, Equals, true)
	c.Assert(utf8.ValidString(invalidReason), Equals, false)

	legacy := SwapState{
		FailedSwaps:       []uint64{1},
		FailedSwapReasons: []string{invalidReason},
	}
	legacy.CompactFailureHistory()
	c.Assert(len(legacy.FailedSwapReasons), Equals, 1)
	c.Assert(utf8.ValidString(legacy.FailedSwapReasons[0]), Equals, true)
	c.Assert(len(legacy.FailedSwapReasons[0]) <= MaxSwapFailureReasonBytes, Equals, true)

	var recorded SwapState
	recorded.RecordFailure(2, invalidReason)
	c.Assert(len(recorded.FailedSwapReasons), Equals, 1)
	c.Assert(utf8.ValidString(recorded.FailedSwapReasons[0]), Equals, true)
	c.Assert(len(recorded.FailedSwapReasons[0]) <= MaxSwapFailureReasonBytes, Equals, true)
}
