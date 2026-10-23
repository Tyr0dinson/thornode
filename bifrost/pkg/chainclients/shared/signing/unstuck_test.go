package signing

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/constants"
)

func TestUnstuckWindow(t *testing.T) {
	ref := constants.ThorchainBlockTime.Milliseconds()
	// without a block time change: the old block-count checks, period 300 and
	// buffer 150
	for _, age := range []int64{149, 150} {
		waiting, _ := UnstuckWindow(nil, 1000-age, 1000-age, 1000, 300, ref, 150)
		if waiting != (age < 150) {
			t.Fatalf("age %d: waiting=%t", age, waiting)
		}
	}
	for _, phase := range []int64{149, 150, 299, 300} {
		_, outside := UnstuckWindow(nil, 1000-phase, 1000-phase, 1000, 300, ref, 150)
		if outside != (300-phase%300 > 150) {
			t.Fatalf("phase %d: outsideBuffer=%t", phase, outside)
		}
	}

	// the block time doubles at 1000: the period is 150 blocks and the buffer
	// 75. An outbound from 900 is 101 reference blocks old at 1000, still
	// waiting, where counting blocks (100 >= 150 - 75) would unstuck it while
	// the signer could still re-sign. It is due at 1025 (151 reference blocks).
	double := constants.BlockTimeHistory{{Height: 1000, Ms: 2 * ref}}
	if waiting, _ := UnstuckWindow(double, 900, 900, 1000, 150, 2*ref, 150); !waiting {
		t.Fatal("unstuck at 1000, 101 reference blocks after signing")
	}
	if waiting, _ := UnstuckWindow(double, 900, 900, 1024, 150, 2*ref, 150); !waiting {
		t.Fatal("unstuck at 1024, 149 reference blocks after signing")
	}
	waiting, outside := UnstuckWindow(double, 900, 900, 1025, 150, 2*ref, 150)
	if waiting || outside {
		t.Fatalf("not due at 1025: waiting=%t outsideBuffer=%t", waiting, outside)
	}
}
