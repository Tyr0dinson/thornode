//go:build mocknet

package blocktime

import (
	"testing"
	"time"

	"cosmossdk.io/log"
	cmtcfg "github.com/cometbft/cometbft/config"
)

func TestHookEnabledOnMocknet(t *testing.T) {
	var h Hook
	cfg := cmtcfg.DefaultConsensusConfig()
	cfg.TimeoutCommit = 5 * time.Second
	h.Attach(cfg)
	h.OnCommit(log.NewNopLogger(), 2000, time.Unix(1_700_000_000, 0))
	if cfg.TimeoutCommit != 900*time.Millisecond || !h.engaged {
		t.Fatalf("hook not engaged on mocknet: TimeoutCommit %v", cfg.TimeoutCommit)
	}
	Attach(cfg)
	if !Active() {
		t.Fatal("process-wide hook inactive on mocknet")
	}
}
