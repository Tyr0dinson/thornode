package blocktime

import (
	"math/rand"
	"sync"
	"testing"
	"time"

	"cosmossdk.io/log"
	cmtcfg "github.com/cometbft/cometbft/config"

	"gitlab.com/thorchain/thornode/v3/constants"
)

func TestHookUnsetNeverTouchesTimeoutCommit(t *testing.T) {
	var h Hook
	cfg := cmtcfg.DefaultConsensusConfig()
	cfg.TimeoutCommit = 5 * time.Second
	h.Attach(cfg)
	hdr := time.Unix(1_700_000_000, 0)
	for i := 0; i < 10; i++ {
		hdr = hdr.Add(6 * time.Second)
		h.OnCommit(log.NewNopLogger(), -1, hdr)
		h.OnCommit(log.NewNopLogger(), 0, hdr)
		// a value written by someone else (operator, regtest) is left alone
		cfg.TimeoutCommit = 1234 * time.Millisecond
	}
	if cfg.TimeoutCommit != 1234*time.Millisecond {
		t.Fatalf("TimeoutCommit changed: %v", cfg.TimeoutCommit)
	}
}

func TestHookUnattachedIsInert(t *testing.T) {
	var h Hook
	h.OnCommit(log.NewNopLogger(), 3000, time.Unix(1_700_000_000, 0))
	if h.Attached() || h.engaged {
		t.Fatal("unattached hook engaged")
	}
}

func TestHookEngageAndRestore(t *testing.T) {
	var h Hook
	cfg := cmtcfg.DefaultConsensusConfig()
	cfg.TimeoutCommit = 5 * time.Second
	h.Attach(cfg)
	hdr := time.Unix(1_700_000_000, 0)

	h.OnCommit(log.NewNopLogger(), 3000, hdr)
	if cfg.TimeoutCommit != 1900*time.Millisecond {
		t.Fatalf("initial wait %v", cfg.TimeoutCommit)
	}
	// header time going backwards falls back to config.toml's value
	h.OnCommit(log.NewNopLogger(), 3000, hdr.Add(-time.Second))
	if cfg.TimeoutCommit != 5*time.Second {
		t.Fatalf("fallback wait %v", cfg.TimeoutCommit)
	}
	for i := 0; i < 20; i++ {
		hdr = hdr.Add(4 * time.Second)
		h.OnCommit(log.NewNopLogger(), 3000, hdr)
	}
	if cfg.TimeoutCommit >= 1900*time.Millisecond {
		t.Fatalf("slow blocks did not shorten the wait: %v", cfg.TimeoutCommit)
	}
	h.OnCommit(log.NewNopLogger(), 0, hdr.Add(3*time.Second))
	if cfg.TimeoutCommit != 5*time.Second {
		t.Fatalf("not restored: %v", cfg.TimeoutCommit)
	}
	// re-engaging starts over from the new target
	h.OnCommit(log.NewNopLogger(), 2000, hdr.Add(6*time.Second))
	if cfg.TimeoutCommit != 900*time.Millisecond {
		t.Fatalf("re-engaged wait %v", cfg.TimeoutCommit)
	}
}

func TestHookClampsStoredTarget(t *testing.T) {
	// out-of-range values that bypassed MsgMimir validation run at the bound
	for _, tc := range []struct{ stored, want int64 }{
		{1, constants.BlockTimeMsMin},
		{500, max(500, constants.BlockTimeMsMin)}, // 1500 on mainnet tags, 500 on mocknet
		{constants.BlockTimeMsMin, constants.BlockTimeMsMin},
		{3000, 3000},
		{constants.BlockTimeMsMax, constants.BlockTimeMsMax},
		{60_000, constants.BlockTimeMsMax},
	} {
		var h Hook
		cfg := cmtcfg.DefaultConsensusConfig()
		h.Attach(cfg)
		hdr := time.Unix(1_700_000_000, 0)
		for i := 0; i < 200; i++ {
			hdr = hdr.Add(time.Duration(tc.want)*time.Millisecond + 50*time.Millisecond)
			h.OnCommit(log.NewNopLogger(), tc.stored, hdr)
			if cfg.TimeoutCommit < 0 || cfg.TimeoutCommit > time.Duration(tc.want)*time.Millisecond {
				t.Fatalf("stored %d: wait %v outside [0, %dms]", tc.stored, cfg.TimeoutCommit, tc.want)
			}
		}
		if h.ctrl.target != time.Duration(tc.want)*time.Millisecond {
			t.Errorf("stored %d: target %v, want %dms", tc.stored, h.ctrl.target, tc.want)
		}
	}
}

func TestControllerWaitWithinTarget(t *testing.T) {
	// whatever intervals it sees, the wait stays in [0, target], so with no
	// overhead the interval cannot exceed BlockTimeMsMax
	r := rand.New(rand.NewSource(2)) //nolint:gosec // deterministic test RNG
	for _, ms := range []int64{constants.BlockTimeMsMin, 1500, 6000, constants.BlockTimeMsMax} {
		target := time.Duration(ms) * time.Millisecond
		var c Controller
		h := time.Unix(1_700_000_000, 0)
		for i := 0; i < 5000; i++ {
			h = h.Add(time.Duration(r.Int63n(int64(3 * target))))
			if w, _ := c.Next(target, h); w < 0 || w > target || w > constants.BlockTimeMsMax*time.Millisecond {
				t.Fatalf("target %v: wait %v", target, w)
			}
		}
	}
}

// TestHookRace mirrors the in-process wiring: the consensus goroutine runs
// the app's Commit (OnCommit) and then reads TimeoutCommit (updateToState),
// while PostSetup attaches the config from another goroutine after consensus
// has started. Run with -race.
func TestHookRace(t *testing.T) {
	var h Hook
	cfg := cmtcfg.DefaultConsensusConfig()
	cfg.TimeoutCommit = 5 * time.Second

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { // consensus
		defer wg.Done()
		hdr := time.Unix(1_700_000_000, 0)
		var startTime time.Time
		for i := 0; i < 5000; i++ {
			hdr = hdr.Add(2 * time.Second)
			h.OnCommit(log.NewNopLogger(), 2000, hdr)
			startTime = hdr.Add(cfg.TimeoutCommit) // cs.config.Commit(cs.CommitTime)
		}
		_ = startTime
	}()
	go func() { // PostSetup
		defer wg.Done()
		time.Sleep(time.Millisecond)
		h.Attach(cfg)
	}()
	go func() { // other readers of the hook
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			_ = h.Attached()
		}
	}()
	wg.Wait()
	if !h.Attached() {
		t.Fatal("not attached")
	}
}
