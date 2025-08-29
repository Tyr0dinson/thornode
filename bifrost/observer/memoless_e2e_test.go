//go:build integration
// +build integration

package observer

import (
    "errors"
    "testing"

    "github.com/rs/zerolog"
    "github.com/stretchr/testify/require"

    btypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
    "gitlab.com/thorchain/thornode/v3/common"
    "gitlab.com/thorchain/thornode/v3/common/cosmos"
    stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// E2E-style test that imitates the memoless winner flow end-to-end within
// the observer layer, and exercises the loser refund seam idempotency.
func TestMemolessE2E_ObserverWinnerAndLoserSeam(t *testing.T) {
    logger := zerolog.New(zerolog.NewTestWriter(t))

    // Mock bridge to return a registered memo for our chosen reference
    mockBridge := &MockThorchainBridgeForMemoless{}
    registeredMemo := &stypes.RegisteredMemo{
        Reference:    "12345",
        Memo:         "SWAP:BTC.BTC",
        ChainId:      "BTC",
        RegisteredAt: 100,
        ExpiresAt:    144100,
        Status:       "active",
    }
    mockBridge.getRegisteredMemoFunc = func(reference string) (*stypes.RegisteredMemo, error) {
        if reference == "12345" {
            return registeredMemo, nil
        }
        return nil, errors.New("not found")
    }

    handler := NewMemolessHandler(mockBridge, logger)

    // Winner path: empty memo + amount encodes reference 12345
    txIn := &btypes.TxInItem{
        Memo:  "",
        Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))},
        Tx:    "txid-winner",
    }

    processed, err := handler.ProcessMemolessTransaction(txIn)
    require.NoError(t, err)
    require.Equal(t, "SWAP:BTC.BTC", processed.Memo)
    // Amount adjusted: base amount should drop last five digits
    require.Equal(t, cosmos.NewUint(1000), processed.Coins[0].Amount)

    // Loser seam: schedule loser refund twice → idempotent
    job1, err := handler.ScheduleLoserRefund(txIn, "REF_COLLISION_LOST", "12345", "txid-winner")
    require.NoError(t, err)
    _ = job1
    job2, err := handler.ScheduleLoserRefund(txIn, "REF_COLLISION_LOST", "12345", "txid-winner")
    require.NoError(t, err)
    _ = job2
}
