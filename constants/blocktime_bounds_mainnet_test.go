//go:build !mocknet

package constants

import "testing"

// the BLOCKTIMEMS hard limits on real networks: 1.5s to 10s
func TestBlockTimeMsBounds(t *testing.T) {
	if BlockTimeMsMin != 1500 || BlockTimeMsMax != 10_000 {
		t.Fatalf("BLOCKTIMEMS bounds [%d, %d], want [1500, 10000]", BlockTimeMsMin, BlockTimeMsMax)
	}
}
