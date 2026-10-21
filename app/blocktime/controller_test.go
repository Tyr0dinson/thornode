package blocktime

import (
	"math"
	"math/rand"
	"slices"
	"testing"
	"time"
)

// mainnetOverhead draws consensus overhead (block interval minus the commit
// wait) shaped like 2000 mainnet blocks with 111 validators: lognormal with
// p50 1.08s and p90 1.59s (giving p99 ~2.2s), capped at the observed 5.85s
// max, plus 3s for the ~1% of blocks that commit in round 1.
func mainnetOverhead(r *rand.Rand) time.Duration {
	sigma := (math.Log(1.59) - math.Log(1.08)) / 1.2816
	o := min(time.Duration(1.08*math.Exp(sigma*r.NormFloat64())*float64(time.Second)), 5850*time.Millisecond)
	if r.Intn(100) == 0 {
		o += 3 * time.Second
	}
	return o
}

type simResult struct {
	mean, p50, p90, p99 time.Duration
	meanWait            time.Duration
}

// simulate runs the controller against a model of CometBFT: committing
// height n (header time = commit time of n-1) sets the wait that starts
// height n+1, so commit(n+1) = commit(n) + wait + overhead.
func simulate(target time.Duration, blocks, warmup int, overhead func(*rand.Rand) time.Duration) simResult {
	r := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic simulation RNG
	var c Controller
	base := time.Unix(1_700_000_000, 0)
	prevCommit, commit := base, base.Add(target)
	var intervals []time.Duration
	var sum, waitSum time.Duration
	for n := 0; n < blocks; n++ {
		wait, ok := c.Next(target, prevCommit)
		if !ok {
			wait = 5 * time.Second
		}
		next := commit.Add(wait + overhead(r))
		if n >= warmup {
			intervals = append(intervals, next.Sub(commit))
			sum += next.Sub(commit)
			waitSum += wait
		}
		prevCommit, commit = commit, next
	}
	slices.Sort(intervals)
	q := func(p int) time.Duration { return intervals[len(intervals)*p/100] }
	n := time.Duration(len(intervals))
	return simResult{mean: sum / n, p50: q(50), p90: q(90), p99: q(99), meanWait: waitSum / n}
}

func TestControllerConvergesOnMainnetOverhead(t *testing.T) {
	for _, ms := range []int64{6000, 3000, 2000, 1500} {
		target := time.Duration(ms) * time.Millisecond
		res := simulate(target, 22_000, 2_000, mainnetOverhead)
		t.Logf("target %5dms: mean %4dms p50 %4dms p90 %4dms p99 %4dms, mean wait %4dms",
			ms, res.mean.Milliseconds(), res.p50.Milliseconds(), res.p90.Milliseconds(),
			res.p99.Milliseconds(), res.meanWait.Milliseconds())
		// steady-state mean within 1% of target
		if d := res.mean - target; d > target/100 || d < -target/100 {
			t.Errorf("target %v: mean interval %v not within 1%%", target, res.mean)
		}
	}
}

func TestControllerConstantOverhead(t *testing.T) {
	// with no noise the wait settles at exactly target - overhead
	target := 3 * time.Second
	res := simulate(target, 400, 300, func(*rand.Rand) time.Duration { return 1300 * time.Millisecond })
	if (res.mean-target).Abs() > time.Millisecond || (res.meanWait-1700*time.Millisecond).Abs() > time.Millisecond {
		t.Fatalf("got mean %v wait %v", res.mean, res.meanWait)
	}
}

func TestControllerLowOverheadNoWindup(t *testing.T) {
	// a single-validator mocknet has ~25ms of overhead, far below the 1.1s
	// the wait starts from; the startup error must not wind up the integral
	// and pin the wait at the target
	for _, target := range []time.Duration{3 * time.Second, 2 * time.Second} {
		res := simulate(target, 130, 30, func(*rand.Rand) time.Duration { return 25 * time.Millisecond })
		if d := (res.mean - target).Abs(); d > target/500 {
			t.Errorf("target %v: mean %v over blocks 30-130", target, res.mean)
		}
	}
}

func TestControllerTargetChangeNoWindup(t *testing.T) {
	// live mocknet 3000ms -> 2000ms: the first interval after the change
	// still carries the old ~3s wait, then the wait climbs from 900ms
	var c Controller
	h := time.Unix(1_700_000_000, 0)
	wait := time.Duration(0)
	var sum time.Duration
	for n := 0; n < 330; n++ {
		target := 3 * time.Second
		if n >= 100 {
			target = 2 * time.Second
		}
		h = h.Add(wait + 33*time.Millisecond)
		if n >= 130 {
			sum += wait + 33*time.Millisecond
		}
		wait, _ = c.Next(target, h)
	}
	if mean := sum / 200; (mean - 2*time.Second).Abs() > 4*time.Millisecond {
		t.Fatalf("mean %v over 200 blocks after the change settled", mean)
	}
}

func TestControllerStepResponse(t *testing.T) {
	// overhead jumps from 1.0s to 1.5s (e.g. a larger validator set); the
	// wait must absorb it without overshooting past zero
	target := 3 * time.Second
	var c Controller
	h := time.Unix(1_700_000_000, 0)
	wait := 2 * time.Second
	settled := -1
	for n := 0; n < 200; n++ {
		overhead := time.Second
		if n >= 50 {
			overhead = 1500 * time.Millisecond
		}
		h = h.Add(wait + overhead)
		wait, _ = c.Next(target, h)
		if n > 50 && settled < 0 && (wait-1500*time.Millisecond).Abs() < 20*time.Millisecond {
			settled = n - 50
		}
	}
	t.Logf("settled within 20ms of the new wait after %d blocks", settled)
	if settled < 0 || settled > 60 || (wait-1500*time.Millisecond).Abs() > 10*time.Millisecond {
		t.Fatalf("settled %d, final wait %v", settled, wait)
	}
}

func TestControllerFallback(t *testing.T) {
	var c Controller
	target := 2 * time.Second
	h := time.Unix(1_700_000_000, 0)
	if w, ok := c.Next(target, h); !ok || w != 900*time.Millisecond {
		t.Fatalf("first block: %v %v", w, ok)
	}
	// header time going backwards
	if _, ok := c.Next(target, h.Add(-time.Second)); ok {
		t.Fatal("negative interval accepted")
	}
	// a long halt: interval > 10x target
	h = h.Add(time.Hour)
	if _, ok := c.Next(target, h); ok {
		t.Fatal("halt-length interval accepted")
	}
	// measurement resumes from the last header
	if _, ok := c.Next(target, h.Add(target)); !ok {
		t.Fatal("did not resume")
	}
	// a new target resets the wait
	if w, _ := c.Next(6*time.Second, h.Add(2*target)); w != 4900*time.Millisecond {
		t.Fatalf("reset wait %v", w)
	}
	// the wait never goes negative or above the target
	c.Reset(1500 * time.Millisecond)
	for i := 0; i < 50; i++ {
		h = h.Add(14 * time.Second)
		if w, _ := c.Next(1500*time.Millisecond, h); w < 0 || w > 1500*time.Millisecond {
			t.Fatalf("wait out of range: %v", w)
		}
	}
	if c.Wait() != 0 {
		t.Fatalf("wait %v after sustained overruns", c.Wait())
	}
}
