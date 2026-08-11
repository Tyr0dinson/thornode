package common

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestAmountPrecisionAlignment(t *testing.T) {
	t.Parallel()

	amount := cosmos.NewUint(123_456_789)
	floored := FloorAmount(common.XECAsset, amount)
	ceiled := CeilAmount(common.XECAsset, amount)

	if !floored.Equal(cosmos.NewUint(123_000_000)) {
		t.Fatalf("expected floored XEC amount 123000000, got %s", floored)
	}
	if !ceiled.Equal(cosmos.NewUint(124_000_000)) {
		t.Fatalf("expected ceiled XEC amount 124000000, got %s", ceiled)
	}
	if _, err := common.XECChain.ThorchainToNativeGasExact(floored); err != nil {
		t.Fatalf("floored XEC amount is not externally representable: %v", err)
	}
	if _, err := common.XECChain.ThorchainToNativeGasExact(ceiled); err != nil {
		t.Fatalf("ceiled XEC amount is not externally representable: %v", err)
	}
}

func TestAmountPrecisionAlignmentLeavesFullPrecisionAssetsUnchanged(t *testing.T) {
	t.Parallel()

	amount := cosmos.NewUint(123_456_789)
	for _, asset := range []common.Asset{common.BTCAsset, common.RuneAsset()} {
		if got := FloorAmount(asset, amount); !got.Equal(amount) {
			t.Fatalf("expected floored %s amount to remain %s, got %s", asset, amount, got)
		}
		if got := CeilAmount(asset, amount); !got.Equal(amount) {
			t.Fatalf("expected ceiled %s amount to remain %s, got %s", asset, amount, got)
		}
	}
}
