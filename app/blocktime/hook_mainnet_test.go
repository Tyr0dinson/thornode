//go:build !mocknet

package blocktime

import (
	"testing"
	"time"

	"cosmossdk.io/log"
	cmtcfg "github.com/cometbft/cometbft/config"
)

// BLOCKTIMEMS is not enabled on real networks until block-count settings
// scale with it: even a stored, in-range value never touches TimeoutCommit.
func TestHookDisabledOnMainnet(t *testing.T) {
	var h Hook
	cfg := cmtcfg.DefaultConsensusConfig()
	cfg.TimeoutCommit = 5 * time.Second
	h.Attach(cfg)
	hdr := time.Unix(1_700_000_000, 0)
	for i := 0; i < 20; i++ {
		hdr = hdr.Add(6 * time.Second)
		h.OnCommit(log.NewNopLogger(), 2000, hdr)
	}
	if cfg.TimeoutCommit != 5*time.Second || h.engaged {
		t.Fatalf("hook engaged on mainnet: TimeoutCommit %v", cfg.TimeoutCommit)
	}
	Attach(cfg)
	if Active() {
		t.Fatal("process-wide hook active on mainnet")
	}
}
