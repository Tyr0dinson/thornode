package substrate

import (
	"fmt"
	"maps"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestParamsRequiredIndexes(t *testing.T) {
	for _, chain := range []common.Chain{common.TAOChain, common.DOTChain} {
		t.Run(chain.String(), func(t *testing.T) {
			original := ChainParams[chain]
			if err := original.Validate(); err != nil {
				t.Fatalf("shipped parameters: %v", err)
			}
			for _, key := range []string{
				SystemPallet, SystemRemarkCall,
				BalancesPallet, BalancesTransferAllowDeathCall, BalancesTransferKeepAliveCall,
				UtilityPallet, UtilityBatchAllCall, TransactionPaymentPallet,
			} {
				t.Run(key, func(t *testing.T) {
					params := original
					params.Indexes = maps.Clone(original.Indexes)
					delete(params.Indexes, key)
					want := fmt.Sprintf("missing configured index for %q", key)
					if err := params.Validate(); err == nil || err.Error() != want {
						t.Fatalf("Validate() = %v, want %s", err, want)
					}
					params.Indexes[key] = 0
					if err := params.Validate(); err != nil {
						t.Fatalf("zero-valued index must count as present: %v", err)
					}
				})
			}
			for _, indexes := range []map[string]byte{nil, {}} {
				params := original
				params.Indexes = indexes
				if err := params.Validate(); err == nil {
					t.Fatal("Validate() accepted an empty index map")
				}
			}
		})
	}
}
