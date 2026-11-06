package utxo

import (
	"testing"
	"time"
)

func TestFinalityHealthBoundaries(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name     string
		health   *finalitySnapshot
		scanned  int64
		wantDASH bool
		wantFIRO bool
	}{
		{"no successful read", nil, 100, false, false},
		{"missing timestamp", &finalitySnapshot{blockHeight: 100, chainLockHeight: 100}, 100, false, false},
		{"caught up", &finalitySnapshot{blockHeight: 100, chainLockHeight: 100, checkedAt: now}, 100, true, true},
		{"one block finality gap", &finalitySnapshot{blockHeight: 101, chainLockHeight: 100, checkedAt: now}, 100, true, true},
		{"two block finality gap", &finalitySnapshot{blockHeight: 102, chainLockHeight: 100, checkedAt: now}, 100, false, false},
		{"just before expiry", &finalitySnapshot{blockHeight: 100, chainLockHeight: 100, checkedAt: now.Add(-150*time.Second + time.Nanosecond)}, 100, true, true},
		{"at expiry", &finalitySnapshot{blockHeight: 100, chainLockHeight: 100, checkedAt: now.Add(-150 * time.Second)}, 100, false, false},
		{"after expiry", &finalitySnapshot{blockHeight: 100, chainLockHeight: 100, checkedAt: now.Add(-time.Hour)}, 100, false, false},
		{"locks recovered but scanner behind", &finalitySnapshot{blockHeight: 110, chainLockHeight: 110, checkedAt: now}, 100, false, false},
		{"one block scanner gap", &finalitySnapshot{blockHeight: 100, chainLockHeight: 100, checkedAt: now}, 99, true, false},
		{"two block scanner gap", &finalitySnapshot{blockHeight: 110, chainLockHeight: 110, checkedAt: now}, 108, false, false},
		{"combined gap exceeds limit", &finalitySnapshot{blockHeight: 110, chainLockHeight: 109, checkedAt: now}, 108, false, false},
		{"daemon behind ChainLock", &finalitySnapshot{blockHeight: 980, chainLockHeight: 1000, checkedAt: now}, 980, false, false},
		{"daemon one block behind ChainLock", &finalitySnapshot{blockHeight: 999, chainLockHeight: 1000, checkedAt: now}, 999, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.health.isHealthyDASH(now, tc.scanned); got != tc.wantDASH {
				t.Errorf("DASH health = %v, want %v", got, tc.wantDASH)
			}
			if got := tc.health.isHealthyFIRO(now, tc.scanned); got != tc.wantFIRO {
				t.Errorf("FIRO health = %v, want %v", got, tc.wantFIRO)
			}
		})
	}
}
