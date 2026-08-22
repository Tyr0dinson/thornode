package types

import (
	"encoding/json"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/openapi/gen"
)

func (MsgSwapSuite) TestSwapQueueJSONIncludesBoundedFailureState(c *C) {
	const failures = MaxSwapFailureHistory + 1
	failedSwaps := make([]uint64, failures)
	failedReasons := make([]string, failures)
	for i := range failedSwaps {
		failedSwaps[i] = uint64(i)
		failedReasons[i] = "failed"
	}

	state := &SwapState{
		Count:             failures,
		FailedSwaps:       failedSwaps,
		FailedSwapReasons: failedReasons,
	}
	response := QuerySwapQueueResponse{
		SwapQueue: []*MsgSwap{{
			InitialBlockHeight: 123,
			State:              state,
		}},
	}

	bz, err := response.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	var decoded []openapi.MsgSwap
	c.Assert(json.Unmarshal(bz, &decoded), IsNil)
	c.Assert(decoded, HasLen, 1)
	c.Assert(decoded[0].InitialBlockHeight, NotNil)
	c.Assert(*decoded[0].InitialBlockHeight, Equals, int64(123))
	c.Assert(decoded[0].State, NotNil)
	c.Assert(decoded[0].State.FailedSwapCount, NotNil)
	c.Assert(*decoded[0].State.FailedSwapCount, Equals, int64(failures))
	c.Assert(decoded[0].State.FailedSwaps, HasLen, MaxSwapFailureHistory)
	c.Assert(decoded[0].State.FailedSwapReasons, HasLen, MaxSwapFailureHistory)

	// JSON serialization must not mutate the raw caller-owned pre-upgrade state.
	c.Assert(state.FailedSwapCount, Equals, uint64(0))
	c.Assert(state.FailedSwaps, HasLen, failures)
	c.Assert(state.FailedSwapReasons, HasLen, failures)
}
