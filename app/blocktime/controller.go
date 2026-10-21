// Package blocktime drives CometBFT's commit wait (consensus.timeout_commit)
// from the BLOCKTIMEMS mimir so the network's mean block interval tracks the
// target instead of timeout_commit plus consensus overhead.
//
// None of this touches state: the controller runs on each node with its own
// view and only changes how long that node waits before proposing the next
// height, so it may use node-local, non-deterministic inputs.
package blocktime

import "time"

const (
	// initialOverhead is the consensus overhead assumed before the first
	// measurement: mainnet's p50 of interval minus timeout_commit is ~1.08s.
	initialOverhead = 1100 * time.Millisecond

	// fallbackFactor rejects an interval longer than this many targets (a
	// halt, an outage, a node catching up) as a measurement.
	fallbackFactor = 10

	// Gains, as fractions over gainDen. The wait set at height H first shows
	// in the interval between headers H+1 and H+2, so the loop has two blocks
	// of dead time; larger gains oscillate.
	gainDen = 1000
	kNum    = 100 // proportional on this block's error
	kiNum   = 2   // on the accumulated error
	// The integral starts accumulating once settleBlocks consecutive
	// intervals come within target/settleBand (5%) of the target.
	settleBand   = 20
	settleBlocks = 3
	// integralMax bounds the accumulated error (anti-windup) to this many
	// targets.
	integralMax = 3
)

// Controller computes the commit wait for the next height from BFT header
// times. It is not safe for concurrent use.
type Controller struct {
	target   time.Duration
	wait     time.Duration
	integral time.Duration
	interval time.Duration
	prev     time.Time
	inBand   int // consecutive intervals within the settle band, up to settleBlocks
}

// Next records the header time of the block being committed and returns the
// commit wait to use before the next height. ok is false when the measured
// interval is not credible, in which case the caller should fall back to its
// configured wait; the controller then restarts its measurement from header.
func (c *Controller) Next(target time.Duration, header time.Time) (wait time.Duration, ok bool) {
	if target != c.target {
		c.Reset(target)
	}
	prev := c.prev
	c.prev = header
	if prev.IsZero() {
		return c.wait, true
	}
	interval := header.Sub(prev)
	c.interval = interval
	if interval <= 0 || interval > fallbackFactor*target {
		c.integral = 0
		return c.wait, false
	}

	err := interval - target
	// Anti-windup: the integral only accumulates once the intervals have
	// settled near the target (several in a row, so one stale interval after
	// a target change that happens to land on it does not count). Otherwise the error of the initial climb (overhead
	// far from initialOverhead, e.g. ~25ms on a one-node mocknet, or the old
	// wait after a target change) holds the wait at the target for hundreds
	// of blocks. Saturating at zero wait still accumulates: that is the debt
	// from slow rounds the integral pays back to keep the mean on target.
	if c.inBand < settleBlocks {
		if err.Abs() <= target/settleBand {
			c.inBand++
		} else {
			c.inBand = 0
		}
	}
	if c.inBand >= settleBlocks {
		c.integral = clamp(c.integral+err, -integralMax*target, integralMax*target)
	}
	c.wait = clamp(c.wait-err*kNum/gainDen-c.integral*kiNum/gainDen, 0, target)
	return c.wait, true
}

// Reset forgets all measurements and restarts at target.
func (c *Controller) Reset(target time.Duration) {
	*c = Controller{target: target, wait: clamp(target-initialOverhead, 0, target)}
}

// Interval returns the last measured header interval.
func (c *Controller) Interval() time.Duration { return c.interval }

// Wait returns the current commit wait.
func (c *Controller) Wait() time.Duration { return c.wait }

func clamp(v, lo, hi time.Duration) time.Duration {
	return min(max(v, lo), hi)
}
