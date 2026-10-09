package tss

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/blang/semver"
	"github.com/stretchr/testify/require"
)

type countingVersionBridge struct {
	mockBridgeForKeysign
	calls atomic.Int32
}

func (b *countingVersionBridge) GetThorchainVersion() (semver.Version, error) {
	b.calls.Add(1)
	return b.version, nil
}

// Keysign batches for different vaults call getVersion from concurrent
// goroutines (one toLocalTSSSigner per pool pubkey). The version cache must be
// safe under -race, and concurrent misses must collapse into one bridge query.
func TestGetVersionConcurrent(t *testing.T) {
	bridge := &countingVersionBridge{mockBridgeForKeysign: mockBridgeForKeysign{version: semver.MustParse("3.4.5")}}
	ks, err := NewKeySign(&mockTssServer{}, bridge)
	require.NoError(t, err)

	const callers = 32
	var wg sync.WaitGroup
	got := make([]string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = ks.getVersion().String()
		}(i)
	}
	wg.Wait()

	for i, v := range got {
		require.Equal(t, "3.4.5", v, "caller %d", i)
	}
	require.EqualValues(t, 1, bridge.calls.Load())
}
