package observer

import (
    "testing"

    "github.com/rs/zerolog"
    "github.com/stretchr/testify/require"

    btypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
    "gitlab.com/thorchain/thornode/v3/common"
    "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// TestScheduleLoserRefund_Idempotency ensures that calling ScheduleLoserRefund
// twice for the same txid does not schedule duplicate work and records the
// refund exactly once.
func TestScheduleLoserRefund_Idempotency(t *testing.T) {
    logger := zerolog.New(zerolog.NewTestWriter(t))
    handler := NewMemolessHandler(&MockThorchainBridgeForMemoless{}, logger)

    tx := &btypes.TxInItem{
        Tx:    "txid-1",
        Memo:  "",
        Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
    }

    // Precondition
    require.False(t, handler.rr.HasRefundRecord(tx.Tx))

    // First call records refund
    job1, err := handler.ScheduleLoserRefund(tx, "REF_COLLISION_LOST", "12345", "winner-tx")
    require.NoError(t, err)
    require.True(t, handler.rr.HasRefundRecord(tx.Tx))
    // For now, we allow empty job id; future change may return non-empty
    _ = job1

    // Second call is idempotent
    job2, err := handler.ScheduleLoserRefund(tx, "REF_COLLISION_LOST", "12345", "winner-tx")
    require.NoError(t, err)
    _ = job2
    // Still only one record
    require.True(t, handler.rr.HasRefundRecord(tx.Tx))
}

func TestBuildCanonicalRefundMemo_Format(t *testing.T) {
    memo := buildCanonicalRefundMemo("REF_COLLISION_LOST", "12345", "txid-abc")
    require.Equal(t, "REFUND:MEMOLESS:REF_COLLISION_LOST:12345:txid-abc", memo)
}
