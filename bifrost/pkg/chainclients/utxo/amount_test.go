package utxo

import (
	"math"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestToUint64(t *testing.T) {
	for _, tc := range []struct {
		name       string
		amount     cosmos.Uint
		want       uint64
		wantErrSub string
	}{
		{name: "nil", amount: cosmos.Uint{}, wantErrSub: "amount is nil"},
		{name: "ordinary", amount: cosmos.NewUint(42), want: 42},
		{name: "maximum", amount: cosmos.NewUint(math.MaxUint64), want: math.MaxUint64},
		{
			name:       "overflow",
			amount:     cosmos.NewUintFromString("18446744073709551616"),
			wantErrSub: "amount does not fit uint64",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := toUint64(tc.amount)
			if tc.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErrSub, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}
