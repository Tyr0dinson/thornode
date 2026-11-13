package utxo

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
)

func TestInstantSendBaseFee(t *testing.T) {
	for _, tc := range []struct {
		fee     string
		size    int64
		rate    uint64
		allowed bool
	}{
		{"0.000001", 100, 0, false},
		{"0.00000799", 100, 0, false},
		{"0.000001", 100, 1, true},
		{"0.000008", 100, 8, true},
		{"0.00000799", 100, 8, false},
		{"0.000008001", 100, 8, false},
		{"8e-6", 100, 8, true},
		{"-1", 100, 8, false},
		{"NaN", 100, 8, false},
		{"", 100, 8, false},
		{"1", 0, 8, false},
		{"0.000008", 100, 0, true},
		{"0.000008", 100, 9, false},
	} {
		entry := rpc.GetRawMempoolVerboseResult{VSize: tc.size}
		entry.Fees.Base = json.Number(tc.fee)
		require.Equal(t, tc.allowed, sufficientInstantSendFee(entry, tc.rate), "%+v", tc)
	}
	var entry rpc.GetRawMempoolVerboseResult
	require.NoError(t, json.Unmarshal([]byte(`{"vsize":100,"fees":{"base":0.00000799,"modified":100}}`), &entry))
	require.False(t, sufficientInstantSendFee(entry, 8))
}
