package types

import (
	"bytes"
	"encoding/json"

	"github.com/cosmos/gogoproto/jsonpb"
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

func (MsgSwapSuite) TestLimitSwapJSONUsesOpenAPINumericTypes(c *C) {
	response := QueryLimitSwapsResponse{
		LimitSwaps: []*LimitSwapWithDetails{{
			Swap: &MsgSwap{
				InitialBlockHeight: 123,
				State: &SwapState{
					Count:             2,
					FailedSwaps:       []uint64{0, 1},
					FailedSwapReasons: []string{"first", "second"},
					FailedSwapCount:   2,
				},
			},
			Ratio:              "1.5",
			BlocksSinceCreated: 10,
			TimeToExpiryBlocks: 20,
			CreatedTimestamp:   30,
		}},
		Pagination: &PaginationMeta{
			Offset:  1,
			Limit:   2,
			Total:   3,
			HasNext: true,
		},
	}

	var buf bytes.Buffer
	marshaler := jsonpb.Marshaler{}
	c.Assert(marshaler.Marshal(&buf, &response), IsNil)

	var decoded openapi.LimitSwapsResponse
	c.Assert(json.Unmarshal(buf.Bytes(), &decoded), IsNil)
	c.Assert(decoded.LimitSwaps, HasLen, 1)
	c.Assert(decoded.LimitSwaps[0].Swap, NotNil)
	c.Assert(decoded.LimitSwaps[0].Swap.State, NotNil)
	c.Assert(decoded.LimitSwaps[0].Swap.State.FailedSwapCount, NotNil)
	c.Assert(*decoded.LimitSwaps[0].Swap.State.FailedSwapCount, Equals, int64(2))
	c.Assert(decoded.LimitSwaps[0].Swap.State.FailedSwaps, DeepEquals, []int64{0, 1})
	c.Assert(decoded.LimitSwaps[0].BlocksSinceCreated, NotNil)
	c.Assert(*decoded.LimitSwaps[0].BlocksSinceCreated, Equals, int64(10))
	c.Assert(decoded.Pagination, NotNil)
	c.Assert(decoded.Pagination.Total, NotNil)
	c.Assert(*decoded.Pagination.Total, Equals, int64(3))
}
