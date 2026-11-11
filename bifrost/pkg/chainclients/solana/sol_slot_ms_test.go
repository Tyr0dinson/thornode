package solana

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// slotMsBridge serves MimirKeySOLSlotMilliseconds per keysign height (every other key
// reads 0) and records the heights the value was read at, mirroring marginMimirBridge.
type slotMsBridge struct {
	*mockSOLThorchainBridge
	byHeight map[int64]int64
	err      error
	reads    []int64
}

func (b *slotMsBridge) GetMimirAtHeight(key string, height int64) (int64, error) {
	if key != constants.MimirKeySOLSlotMilliseconds {
		return 0, nil
	}
	b.reads = append(b.reads, height)
	if b.err != nil {
		return 0, b.err
	}
	return b.byHeight[height], nil
}

func newSlotMsClient(byHeight map[int64]int64, err error) (*SOLClient, *slotMsBridge) {
	b := &slotMsBridge{mockSOLThorchainBridge: &mockSOLThorchainBridge{}, byHeight: byHeight, err: err}
	return &SOLClient{bridge: b}, b
}

func TestClampSolSlotMilliseconds(t *testing.T) {
	for _, tc := range []struct {
		raw, want int64
	}{
		{solSlotMillisecondsMin - 1, solSlotMillisecondsMin},
		{solSlotMillisecondsMin, solSlotMillisecondsMin},
		{100, 100},
		{214, 214},
		{solSlotMillisecondsMax, solSlotMillisecondsMax},
		{solSlotMillisecondsMax + 1, solSlotMillisecondsMax},
		{1_000_000, solSlotMillisecondsMax},
	} {
		require.Equal(t, tc.want, clampSolSlotMilliseconds(tc.raw), "raw %d", tc.raw)
	}
}

func TestSolSlotMillisecondsAtHeight(t *testing.T) {
	fallback := common.SOLChain.ApproximateBlockMilliseconds()

	// A set value within range is read at the given height and used as-is; an unset
	// (0) height falls back to the code constant. Neither errors.
	c, b := newSlotMsClient(map[int64]int64{100: 180}, nil)
	ms, err := c.solSlotMillisecondsAtHeight(100)
	require.NoError(t, err)
	require.Equal(t, int64(180), ms)
	ms, err = c.solSlotMillisecondsAtHeight(200) // unset -> constant
	require.NoError(t, err)
	require.Equal(t, fallback, ms)
	require.Equal(t, []int64{100, 200}, b.reads) // read AT the keysign height

	// Out-of-range votes are clamped, not trusted raw.
	cLow, _ := newSlotMsClient(map[int64]int64{1: 1}, nil)
	ms, err = cLow.solSlotMillisecondsAtHeight(1)
	require.NoError(t, err)
	require.Equal(t, solSlotMillisecondsMin, ms)
	cHigh, _ := newSlotMsClient(map[int64]int64{1: 10_000}, nil)
	ms, err = cHigh.solSlotMillisecondsAtHeight(1)
	require.NoError(t, err)
	require.Equal(t, solSlotMillisecondsMax, ms)

	// A read error is PROPAGATED (not swallowed into the constant), so the caller fails
	// closed instead of diverging from peers whose read succeeded.
	cErr, _ := newSlotMsClient(nil, errors.New("rpc down"))
	_, err = cErr.solSlotMillisecondsAtHeight(1)
	require.Error(t, err)

	// A bridge without GetMimirAtHeight falls back to the live GetMimir (returns 0 in
	// the mock) -> the code constant, no error, never a panic.
	cLive := &SOLClient{bridge: &mockSOLThorchainBridge{}}
	ms, err = cLive.solSlotMillisecondsAtHeight(5)
	require.NoError(t, err)
	require.Equal(t, fallback, ms)
}

func TestSolSlotsPerThorchainBlock(t *testing.T) {
	thor := common.THORChain.ApproximateBlockMilliseconds() // 6000

	// 6000 / 214 = 28 (truncated).
	c, _ := newSlotMsClient(map[int64]int64{10: 214}, nil)
	slots, err := c.solSlotsPerThorchainBlock(10)
	require.NoError(t, err)
	require.Equal(t, uint64(thor/214), slots)
	require.Equal(t, uint64(28), slots)

	// The clamp caps the slot time at solSlotMillisecondsMax (2000ms), so the ratio is
	// never below 6000/2000 = 3 via the mimir path — the slots<1 floor in
	// solSlotsPerThorchainBlock is an unreachable-via-mimir safety net, not exercised here.
	cSlow, _ := newSlotMsClient(map[int64]int64{10: solSlotMillisecondsMax}, nil)
	slots, err = cSlow.solSlotsPerThorchainBlock(10)
	require.NoError(t, err)
	require.Equal(t, uint64(thor/solSlotMillisecondsMax), slots)
	require.Equal(t, uint64(3), slots)
	cHuge, _ := newSlotMsClient(map[int64]int64{10: 999_999}, nil) // clamped to max 2000 -> 3
	slots, err = cHuge.solSlotsPerThorchainBlock(10)
	require.NoError(t, err)
	require.Equal(t, uint64(3), slots, "clamp keeps the ratio at >= 3")

	// Unset mimir uses the code constant (214) -> 28.
	cDefault, _ := newSlotMsClient(nil, nil)
	slots, err = cDefault.solSlotsPerThorchainBlock(10)
	require.NoError(t, err)
	require.Equal(t, uint64(thor/common.SOLChain.ApproximateBlockMilliseconds()), slots)

	// A mimir read error propagates, so keepAliveCheck skips the boundary (fail closed).
	cErr, _ := newSlotMsClient(nil, errors.New("rpc down"))
	_, err = cErr.solSlotsPerThorchainBlock(10)
	require.Error(t, err)
}
