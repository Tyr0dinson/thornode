package constants

import (
	"math"
	"strings"
	"testing"
)

// every ConstantName needs a unit decision before it can ship
func TestEveryConstantHasUnit(t *testing.T) {
	n := len(_ConstantName_index) - 1
	if n != len(constantUnits) {
		t.Errorf("%d ConstantNames, %d unit tags", n, len(constantUnits))
	}
	for i := 0; i < n; i++ {
		cn := ConstantName(i)
		if cn.Unit() == 0 {
			t.Errorf("%s has no unit tag in constantUnits", cn)
		}
	}
}

func TestMimirKeyUnit(t *testing.T) {
	for key, want := range map[string]Unit{
		"CHURNINTERVAL":          UnitDuration,
		"churnInterval":          UnitDuration,
		"TXOUTDELAYRATE":         UnitPerBlockRate,
		"DesiredValidatorSet":    UnitCount,
		"SIGNATUREREFRESHBLOCKS": UnitDuration,
		"HaltChurning":           UnitHeight,
		"OracleUpdateInterval":   UnitWallClock,
		"MaxConfirmations-BTC":   0,
		"NotAMimir":              0,
	} {
		if got := MimirKeyUnit(key); got != want {
			t.Errorf("MimirKeyUnit(%q) = %s, want %s", key, got, want)
		}
	}
}

func TestScaleDuration(t *testing.T) {
	for _, tc := range []struct{ v, ref, blk, want int64 }{
		{43200, 6000, 6000, 43200}, // identity
		{43200, 6000, 2000, 129600},
		{43200, 6000, 3000, 86400},
		{60, 1000, 500, 120},
		{7, 6000, 4000, 11},     // 10.5 rounds up
		{1, 6000, 10000, 1},     // never below one block
		{300, 6000, 10000, 180}, // longer blocks shrink it
		{0, 6000, 2000, 0},      // off stays off
		{-1, 6000, 2000, -1},    // unset stays unset
		{5, 0, 2000, 5},
		{5, 6000, 0, 5},
		{math.MaxInt64, 6000, 2000, math.MaxInt64}, // saturates
		{math.MaxInt64 / 2, 6000, 2000, math.MaxInt64},
	} {
		if got := ScaleDuration(tc.v, tc.ref, tc.blk); got != tc.want {
			t.Errorf("ScaleDuration(%d, %d, %d) = %d, want %d", tc.v, tc.ref, tc.blk, got, tc.want)
		}
	}
}

func TestScalePerBlockRate(t *testing.T) {
	for _, tc := range []struct{ v, ref, blk, want int64 }{
		{25_00000000, 6000, 6000, 25_00000000}, // identity
		{25_00000000, 6000, 2000, 8_33333333},
		{20, 6000, 2000, 7}, // 6.67 rounds half up
		{3, 6000, 4000, 2},  // 2.0
		{3, 6000, 1000, 1},  // 0.5 rounds up
		{2, 6000, 1000, 0},  // 0.33 rounds down
		{100, 1000, 500, 50},
		{100, 6000, 9000, 150}, // longer blocks raise it
		{0, 6000, 2000, 0},
		{-1, 6000, 2000, -1},
		{math.MaxInt64, 1000, 10000, math.MaxInt64},
	} {
		if got := ScalePerBlockRate(tc.v, tc.ref, tc.blk); got != tc.want {
			t.Errorf("ScalePerBlockRate(%d, %d, %d) = %d, want %d", tc.v, tc.ref, tc.blk, got, tc.want)
		}
	}
}

// at the reference block time every unit is the identity
func TestUnitScaleIdentity(t *testing.T) {
	ref := ThorchainBlockTime.Milliseconds()
	for _, u := range []Unit{0, UnitCount, UnitDuration, UnitPerBlockRate, UnitHeight, UnitWallClock} {
		for _, v := range []int64{math.MinInt64, -1, 0, 1, 7, 43200, math.MaxInt64} {
			if got := u.Scale(v, ref, ref); got != v {
				t.Errorf("%s.Scale(%d) at the reference = %d", u, v, got)
			}
		}
	}
	// counts and heights never scale
	for _, u := range []Unit{UnitCount, UnitHeight, UnitWallClock} {
		if got := u.Scale(1000, 6000, 2000); got != 1000 {
			t.Errorf("%s.Scale = %d, want 1000", u, got)
		}
		if u.Scales() {
			t.Errorf("%s.Scales() = true", u)
		}
	}
	if !strings.Contains(Unit(0).String(), "untagged") {
		t.Errorf("Unit(0) = %s", Unit(0))
	}
}
