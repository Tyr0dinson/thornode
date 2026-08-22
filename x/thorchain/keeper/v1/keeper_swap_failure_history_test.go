package keeperv1

import (
	"fmt"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const preUpgradeFailureCount = types.MaxSwapFailureHistory + 17

func preUpgradeFailureHistory() ([]uint64, []string) {
	failedSwaps := make([]uint64, preUpgradeFailureCount)
	// Include one unmatched old reason to prove read/write normalization takes
	// a shared suffix rather than independently truncating misaligned slices.
	failedReasons := make([]string, preUpgradeFailureCount+1)
	failedReasons[0] = "orphaned legacy reason"
	for i := range failedSwaps {
		failedSwaps[i] = uint64(i)
		failedReasons[i+1] = failureReason(uint64(i))
	}
	return failedSwaps, failedReasons
}

func failureReason(index uint64) string {
	return fmt.Sprintf("failure-%03d-%s", index, strings.Repeat("x", types.MaxSwapFailureReasonBytes))
}

func newPreUpgradeAdvSwap() *MsgSwap {
	tx := GetRandomTx()
	msg := types.NewMsgSwap(
		tx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		types.SwapType_limit,
		1,
		0,
		types.SwapVersion_v2,
		GetRandomBech32Addr(),
	)
	msg.State.Count = preUpgradeFailureCount
	msg.State.FailedSwaps, msg.State.FailedSwapReasons = preUpgradeFailureHistory()
	return msg
}

func assertCompactedFailureHistory(c *C, total uint64, failedSwaps []uint64, failedReasons []string) {
	c.Assert(total, Equals, uint64(preUpgradeFailureCount))
	c.Assert(failedSwaps, HasLen, types.MaxSwapFailureHistory)
	c.Assert(failedReasons, HasLen, types.MaxSwapFailureHistory)

	expectedFirst := uint64(preUpgradeFailureCount - types.MaxSwapFailureHistory)
	c.Assert(failedSwaps[0], Equals, expectedFirst)
	c.Assert(failedSwaps[len(failedSwaps)-1], Equals, uint64(preUpgradeFailureCount-1))
	for i, index := range failedSwaps {
		c.Assert(failedReasons[i], Matches, fmt.Sprintf("^failure-%03d-.*", index))
		c.Assert(len(failedReasons[i]) <= types.MaxSwapFailureReasonBytes, Equals, true)
	}
}

func (s *KeeperAdvSwapQueueSuite) TestGetAdvSwapQueueItemCompactsPreUpgradeFailureHistory(c *C) {
	ctx, k := setupKeeperForTest(c)
	msg := newPreUpgradeAdvSwap()
	key := k.GetKey(prefixAdvSwapQueueItem, formatSwapQueueItemKey(msg.Tx.ID, int(msg.Index)))

	// Bypass the public setter to reproduce an on-disk record written before
	// FailedSwapCount and bounded diagnostic tails existed.
	k.setMsgSwap(ctx, key, *msg)

	result, err := k.GetAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index))
	c.Assert(err, IsNil)
	assertCompactedFailureHistory(c, result.State.FailedSwapCount, result.State.FailedSwaps, result.State.FailedSwapReasons)
	c.Assert(result.FailCount(), Equals, uint64(preUpgradeFailureCount))

	// Read-time compatibility must not rewrite state implicitly.
	var raw MsgSwap
	ok, err := k.getMsgSwap(ctx, key, &raw)
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)
	c.Assert(raw.State.FailedSwapCount, Equals, uint64(0))
	c.Assert(raw.State.FailedSwaps, HasLen, preUpgradeFailureCount)
	c.Assert(raw.State.FailedSwapReasons, HasLen, preUpgradeFailureCount+1)
}

func (s *KeeperAdvSwapQueueSuite) TestSetAdvSwapQueueItemCompactsWithoutMutatingCaller(c *C) {
	ctx, k := setupKeeperForTest(c)
	msg := newPreUpgradeAdvSwap()
	originalFailedSwaps := append([]uint64(nil), msg.State.FailedSwaps...)
	originalFailedReasons := append([]string(nil), msg.State.FailedSwapReasons...)

	c.Assert(k.SetAdvSwapQueueItem(ctx, *msg), IsNil)

	// MsgSwap is passed by value, so the pointed-to State must be copied before
	// write-time compaction rather than mutating the caller's object graph.
	c.Assert(msg.State.FailedSwapCount, Equals, uint64(0))
	c.Assert(msg.State.FailedSwaps, DeepEquals, originalFailedSwaps)
	c.Assert(msg.State.FailedSwapReasons, DeepEquals, originalFailedReasons)

	key := k.GetKey(prefixAdvSwapQueueItem, formatSwapQueueItemKey(msg.Tx.ID, int(msg.Index)))
	var stored MsgSwap
	ok, err := k.getMsgSwap(ctx, key, &stored)
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)
	assertCompactedFailureHistory(c, stored.State.FailedSwapCount, stored.State.FailedSwaps, stored.State.FailedSwapReasons)
}

func (s *KeeperStreamingSwapSuite) TestGetStreamingSwapCompactsPreUpgradeFailureHistory(c *C) {
	ctx, k := setupKeeperForTest(c)
	swp := NewStreamingSwap(GetRandomTxHash(), preUpgradeFailureCount, 1, cosmos.NewUint(100), cosmos.NewUint(100))
	swp.Count = preUpgradeFailureCount
	swp.FailedSwaps, swp.FailedSwapReasons = preUpgradeFailureHistory()
	key := k.GetKey(prefixStreamingSwap, swp.TxID.String())

	// Bypass the public setter to reproduce pre-upgrade state exactly.
	k.setStreamingSwap(ctx, key, swp)

	result, err := k.GetStreamingSwap(ctx, swp.TxID)
	c.Assert(err, IsNil)
	assertCompactedFailureHistory(c, result.FailedSwapCount, result.FailedSwaps, result.FailedSwapReasons)
	c.Assert(result.FailureCount(), Equals, uint64(preUpgradeFailureCount))

	// Read-time compatibility must not rewrite state implicitly.
	var raw StreamingSwap
	ok, err := k.getStreamingSwap(ctx, key, &raw)
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)
	c.Assert(raw.FailedSwapCount, Equals, uint64(0))
	c.Assert(raw.FailedSwaps, HasLen, preUpgradeFailureCount)
	c.Assert(raw.FailedSwapReasons, HasLen, preUpgradeFailureCount+1)
}

func (s *KeeperStreamingSwapSuite) TestSetStreamingSwapPersistsCompactedFailureHistory(c *C) {
	ctx, k := setupKeeperForTest(c)
	swp := NewStreamingSwap(GetRandomTxHash(), preUpgradeFailureCount, 1, cosmos.NewUint(100), cosmos.NewUint(100))
	swp.Count = preUpgradeFailureCount
	swp.FailedSwaps, swp.FailedSwapReasons = preUpgradeFailureHistory()

	k.SetStreamingSwap(ctx, swp)

	key := k.GetKey(prefixStreamingSwap, swp.TxID.String())
	var stored StreamingSwap
	ok, err := k.getStreamingSwap(ctx, key, &stored)
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)
	assertCompactedFailureHistory(c, stored.FailedSwapCount, stored.FailedSwaps, stored.FailedSwapReasons)
}
