//go:build !mocknet

package blocktime

import (
	"testing"
	"time"

	"cosmossdk.io/log"
	cmtcfg "github.com/cometbft/cometbft/config"
)

// On real networks the hook engages like on mocknet, within the mainnet
// bounds: a stored value below 1500ms runs at 1500ms.
func TestHookEngagesOnMainnet(t *testing.T) {
	var h Hook
	cfg := cmtcfg.DefaultConsensusConfig()
	cfg.TimeoutCommit = 5 * time.Second
	h.Attach(cfg)
	h.OnCommit(log.NewNopLogger(), 2000, time.Unix(1_700_000_000, 0))
	if cfg.TimeoutCommit != 900*time.Millisecond || !h.engaged {
		t.Fatalf("hook not engaged: TimeoutCommit %v", cfg.TimeoutCommit)
	}

	h.OnCommit(log.NewNopLogger(), 500, time.Unix(1_700_000_002, 0))
	if h.ctrl.target != 1500*time.Millisecond || cfg.TimeoutCommit != 400*time.Millisecond {
		t.Fatalf("stored 500: target %v, wait %v", h.ctrl.target, cfg.TimeoutCommit)
	}
	h.OnCommit(log.NewNopLogger(), 60_000, time.Unix(1_700_000_004, 0))
	if h.ctrl.target != 10*time.Second {
		t.Fatalf("stored 60000: target %v", h.ctrl.target)
	}

	h.OnCommit(log.NewNopLogger(), 0, time.Unix(1_700_000_014, 0))
	if cfg.TimeoutCommit != 5*time.Second || h.engaged {
		t.Fatalf("not restored: TimeoutCommit %v", cfg.TimeoutCommit)
	}
}
