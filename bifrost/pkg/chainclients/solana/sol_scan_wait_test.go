package solana

import (
	"testing"
	"time"
)

func TestScanCoalesceWait(t *testing.T) {
	interval := 15 * time.Second
	for _, tc := range []struct {
		lag, want time.Duration
	}{
		{0, 15 * time.Second},
		{10 * time.Second, 5 * time.Second},
		{15 * time.Second, 0},
		{40 * time.Second, 0},
	} {
		if got := scanCoalesceWait(tc.lag, interval); got != tc.want {
			t.Errorf("scanCoalesceWait(%v, %v) = %v, want %v", tc.lag, interval, got, tc.want)
		}
	}
}
