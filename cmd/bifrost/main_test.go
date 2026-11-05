package main

import (
	"context"
	"errors"
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	btss "gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/config"
)

type LegacyRecoverySuite struct{}

var _ = Suite(&LegacyRecoverySuite{})

// fakeHeightBridge embeds the ThorchainBridge interface (nil) and overrides only
// GetBlockHeight — the one method the one-shot height trigger calls. Every other
// method would panic if called, which is intentional: these tests must not reach
// the real recovery path (that goes through the recoverLegacyKeysharesFn seam).
type fakeHeightBridge struct {
	thorclient.ThorchainBridge
	height    int64
	heightErr error
}

func (f *fakeHeightBridge) GetBlockHeight() (int64, error) { return f.height, f.heightErr }

// setLegacySeams overrides the package-level recovery seams and returns a restore
// func. Every test defers the restore so the global vars never leak between tests.
func setLegacySeams(fn func(config.Bifrost, thorclient.ThorchainBridge) error, tick, maxWait time.Duration) func() {
	origFn := recoverLegacyKeysharesFn
	origTick := legacyRecoveryTickInterval
	origWait := legacyRecoverySyncMaxWait
	recoverLegacyKeysharesFn = fn
	legacyRecoveryTickInterval = tick
	legacyRecoverySyncMaxWait = maxWait
	return func() {
		recoverLegacyKeysharesFn = origFn
		legacyRecoveryTickInterval = origTick
		legacyRecoverySyncMaxWait = origWait
	}
}

// --- recoverLegacyKeysharesBeforeStartup: error -> (start, degraded, recoverAtHeight) ---

func (s *LegacyRecoverySuite) TestBeforeStartupSuccessStartsCleanly(c *C) {
	calls := 0
	defer setLegacySeams(func(config.Bifrost, thorclient.ThorchainBridge) error {
		calls++
		return nil
	}, time.Millisecond, time.Hour)()

	start, degraded, recoverAt := recoverLegacyKeysharesBeforeStartup(config.Bifrost{}, &fakeHeightBridge{})
	c.Check(start, Equals, true)
	c.Check(degraded, Equals, false)
	c.Check(recoverAt, Equals, int64(0))
	c.Check(calls, Equals, 1)
}

func (s *LegacyRecoverySuite) TestBeforeStartupRetriesTransientThenStarts(c *C) {
	// A genuinely transient failure (RPC/disk — neither permanent nor
	// not-yet-synced) is retried in the foreground before TSS startup. The seam
	// returns one transient error, then nil, so the loop must retry exactly once
	// and start cleanly. The tiny tick keeps the retry wait sub-millisecond.
	calls := 0
	defer setLegacySeams(func(config.Bifrost, thorclient.ThorchainBridge) error {
		calls++
		if calls == 1 {
			return errors.New("fail to get block: transient rpc")
		}
		return nil
	}, time.Millisecond, time.Hour)()

	start, degraded, recoverAt := recoverLegacyKeysharesBeforeStartup(config.Bifrost{}, &fakeHeightBridge{})
	c.Check(start, Equals, true)
	c.Check(degraded, Equals, false)
	c.Check(recoverAt, Equals, int64(0))
	c.Check(calls, Equals, 2)
}

func (s *LegacyRecoverySuite) TestBeforeStartupPermanentWithTransientStartsDegraded(c *C) {
	// The core of the degraded-on-permanent fix: a permanently-unavailable vault joined
	// with a PERSISTENT transient one must start bifrost DEGRADED (hand recovery to the
	// background supervisor), NOT loop the pre-startup barrier forever waiting for the
	// transient to clear — that loop is the wedge (never starts TSS; the 3.20.0
	// signing-stall class). The seam returns that exact joined error on every call,
	// using the exported permanent sentinel so package main can build it.
	calls := 0
	defer setLegacySeams(func(config.Bifrost, thorclient.ThorchainBridge) error {
		calls++
		return errors.Join(btss.ErrRecoveryBackupUnrecoverable, errors.New("fail to get block: transient rpc"))
	}, time.Millisecond, time.Hour)()

	start, degraded, recoverAt := recoverLegacyKeysharesBeforeStartup(config.Bifrost{}, &fakeHeightBridge{})
	c.Check(start, Equals, true)
	c.Check(degraded, Equals, true)      // permanent member -> degraded start
	c.Check(recoverAt, Equals, int64(0)) // permanent subsumes the one-shot trigger
	// Exactly one attempt then degraded: a wedge would keep re-calling the seam while
	// the (permanent) error persists. One call proves the barrier did not loop.
	c.Check(calls, Equals, 1)
}

// NOTE: the permanent -> (true,true,0) routing is now exercised from package main via
// the exported btss.ErrRecoveryBackupUnrecoverable sentinel
// (TestBeforeStartupPermanentWithTransientStartsDegraded above). The not-yet-synced
// -> (true,false,height) mapping still pivots on the UNEXPORTED
// *recoveryBackupNotYetSyncedError, which package main cannot construct, so that value
// assertion is covered in package tss (TestFindRecoveryBackupsClassification /
// TestNotYetSyncedKeyshareHeight). Here we also cover the nil start path and the
// transient-retry loop.

// --- awaitHeightAndRecoverLegacyKeyshares: the one-shot height trigger ---

// runAwait runs the trigger in a goroutine and returns a channel closed when it
// returns, so a test can assert completion with a timeout rather than hang.
func runAwait(ctx context.Context, bridge thorclient.ThorchainBridge, recoverAt int64) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		awaitHeightAndRecoverLegacyKeyshares(ctx, config.Bifrost{}, bridge, recoverAt)
		close(done)
	}()
	return done
}

func (s *LegacyRecoverySuite) TestAwaitCompletesWhenHeightReachedAndRecoverSucceeds(c *C) {
	calls := make(chan struct{}, 4)
	defer setLegacySeams(func(config.Bifrost, thorclient.ThorchainBridge) error {
		calls <- struct{}{}
		return nil
	}, time.Millisecond, time.Hour)()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Height already at/above the backup height -> the first tick triggers recovery,
	// which succeeds -> the trigger returns.
	done := runAwait(ctx, &fakeHeightBridge{height: 200}, 100)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		c.Fatal("await did not complete after a successful recovery at height")
	}
	c.Check(len(calls), Equals, 1)
}

func (s *LegacyRecoverySuite) TestAwaitDeadlineHandsOffToSupervisor(c *C) {
	// The node never reaches the backup height (1 < 100), so the ticker branch NEVER
	// runs recovery. The only way the seam is invoked here is via the max-wait
	// deadline handing off to the background supervisor — so asserting the seam runs
	// (and the trigger then returns) proves the deadline branch fired. Shrink the
	// supervisor's first-attempt delay so its attempt lands within the test. Without
	// the deadline branch the seam would never be called and this test fails.
	origInitial := legacyRecoveryRetryInitial
	legacyRecoveryRetryInitial = time.Millisecond
	defer func() { legacyRecoveryRetryInitial = origInitial }()

	calls := make(chan struct{}, 4)
	defer setLegacySeams(func(config.Bifrost, thorclient.ThorchainBridge) error {
		select {
		case calls <- struct{}{}:
		default:
		}
		return nil // the supervisor's first attempt "recovers", so it returns
	}, time.Millisecond, 20*time.Millisecond)()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAwait(ctx, &fakeHeightBridge{height: 1}, 100)
	select {
	case <-calls:
	case <-time.After(5 * time.Second):
		c.Fatal("deadline never handed off: seam not invoked, but the height is never reached so only the deadline->supervisor path can call it")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		c.Fatal("await did not return after the deadline handoff completed")
	}
}

func (s *LegacyRecoverySuite) TestAwaitRetriesGenericFailureAtHeight(c *C) {
	// Height is reached and recovery runs but returns a generic (non-permanent,
	// non-not-yet-synced) failure. Such a leaf is HasRetryableKeyshareComponent, so
	// the trigger treats it as transient and RE-RUNS on the next tick — it does NOT
	// hand off to the supervisor here (handoff happens only for a permanent or a
	// not-yet-synced classification, neither of which a generic error carries; see
	// the NOTE below). Proven by the seam being invoked on MORE THAN ONE tick; the
	// trigger returns only on ctx cancel.
	calls := make(chan struct{}, 16)
	defer setLegacySeams(func(config.Bifrost, thorclient.ThorchainBridge) error {
		select {
		case calls <- struct{}{}:
		default:
		}
		return errors.New("ran at the synced height but did not complete")
	}, time.Millisecond, time.Hour)()

	ctx, cancel := context.WithCancel(context.Background())
	done := runAwait(ctx, &fakeHeightBridge{height: 200}, 100)
	// Two invocations across two ticks prove the per-tick retry (not a one-shot
	// handoff that would stop after a single failed attempt).
	for i := 0; i < 2; i++ {
		select {
		case <-calls:
		case <-time.After(5 * time.Second):
			c.Fatalf("recovery seam was not re-invoked on tick %d (expected per-tick retry)", i+1)
		}
	}
	// The trigger did not return on its own after the failures; only the cancel ends it.
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		c.Fatal("await did not return after context cancel")
	}
}

// NOTE: the await branch that ADVANCES the target on a not-yet-synced result still
// pivots on the unexported *recoveryBackupNotYetSyncedError, so it cannot be built
// from package main; it is covered in package tss (TestNotYetSyncedKeyshareHeight +
// TestFindRecoveryBackupsClassification). The PERMANENT-handoff branch is now
// constructible here via the exported btss.ErrRecoveryBackupUnrecoverable (see the
// barrier note above). This file covers the height-reached success, the deadline
// handoff, and the generic-failure per-tick RETRY (a generic error is retryable, so
// the trigger re-runs rather than handing off).
