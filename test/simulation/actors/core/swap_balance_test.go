package core

import (
	"testing"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestSwapSpendableBalanceLeavesGasReserve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		asset   common.Asset
		balance sdkmath.Uint
		want    sdkmath.Uint
	}{
		{
			name:    "non gas asset can use full balance",
			asset:   common.AVAXAsset.GetSyntheticAsset(), // force a non-gas asset? no, use token below
			balance: sdkmath.NewUint(12345),
			want:    sdkmath.NewUint(12345),
		},
		{
			name:    "sol leaves ten percent reserve",
			asset:   common.SOLAsset,
			balance: sdkmath.NewUint(1_000_000_000),
			want:    sdkmath.NewUint(900_000_000),
		},
		{
			name:    "xrp leaves at least base reserve",
			asset:   common.XRPAsset,
			balance: sdkmath.NewUint(2 * common.One),
			want:    sdkmath.NewUint(common.One),
		},
		{
			name:    "gas asset below reserve is unusable",
			asset:   common.XRPAsset,
			balance: sdkmath.NewUint(common.One),
			want:    sdkmath.ZeroUint(),
		},
	}

	token, err := common.NewAsset("ETH.TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25")
	if err != nil {
		t.Fatalf("failed to parse token asset: %v", err)
	}
	tests[0].asset = token

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := swapSpendableBalance(tc.asset, tc.balance)
			if !got.Equal(tc.want) {
				t.Fatalf("expected spendable balance %s, got %s", tc.want, got)
			}
		})
	}
}

func TestSwapAmountForBalanceCapsToSpendableBalance(t *testing.T) {
	t.Parallel()

	got := swapAmountForBalance(common.SOLAsset, sdkmath.NewUint(500_000_000), sdkmath.NewUint(100_000_000))
	want := sdkmath.NewUint(90_000_000)
	if !got.Equal(want) {
		t.Fatalf("expected capped swap amount %s, got %s", want, got)
	}

	got = swapAmountForBalance(common.BTCAsset, sdkmath.NewUint(500_000), sdkmath.NewUint(2_000_000))
	want = sdkmath.NewUint(500_000)
	if !got.Equal(want) {
		t.Fatalf("expected target swap amount %s, got %s", want, got)
	}
}
