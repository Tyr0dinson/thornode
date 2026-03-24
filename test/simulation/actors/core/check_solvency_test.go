package core

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestSolvencyAmountsMatch(t *testing.T) {
	tests := []struct {
		name       string
		asset      common.Asset
		vault      cosmos.Uint
		l1         cosmos.Uint
		match      bool
		difference cosmos.Uint
		tolerance  cosmos.Uint
	}{
		{
			name:       "exact match for non XMR asset",
			asset:      common.BTCAsset,
			vault:      cosmos.NewUint(100),
			l1:         cosmos.NewUint(100),
			match:      true,
			difference: cosmos.ZeroUint(),
			tolerance:  cosmos.ZeroUint(),
		},
		{
			name:       "non XMR asset still requires exact match",
			asset:      common.BTCAsset,
			vault:      cosmos.NewUint(100),
			l1:         cosmos.NewUint(99),
			match:      false,
			difference: cosmos.NewUint(1),
			tolerance:  cosmos.ZeroUint(),
		},
		{
			name:       "xmr allows small dust difference",
			asset:      common.XMRAsset,
			vault:      cosmos.NewUint(100),
			l1:         cosmos.NewUint(97),
			match:      true,
			difference: cosmos.NewUint(3),
			tolerance:  xmrSolvencyTolerance,
		},
		{
			name:       "xmr still fails beyond tolerance",
			asset:      common.XMRAsset,
			vault:      cosmos.NewUint(100),
			l1:         cosmos.NewUint(70),
			match:      false,
			difference: cosmos.NewUint(30),
			tolerance:  xmrSolvencyTolerance,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			match, difference, tolerance := solvencyAmountsMatch(tc.asset, tc.vault, tc.l1)
			if match != tc.match {
				t.Fatalf("expected match=%t, got %t", tc.match, match)
			}
			if !difference.Equal(tc.difference) {
				t.Fatalf("expected difference %s, got %s", tc.difference, difference)
			}
			if !tolerance.Equal(tc.tolerance) {
				t.Fatalf("expected tolerance %s, got %s", tc.tolerance, tolerance)
			}
		})
	}
}
