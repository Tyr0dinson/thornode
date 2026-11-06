package constants

import "math"

// BlockTimeSegment says that from Height on, blocks are Ms apart.
type BlockTimeSegment struct {
	Height int64 `json:"height"`
	Ms     int64 `json:"ms"`
}

// BlockTimeHistory lists the effective block time's changes in ascending
// height order. Blocks before the first segment run at the reference block
// time (ThorchainBlockTime); an empty history means the block time never
// changed, and every method then reduces to plain reference-block arithmetic.
//
// THORNode and Bifrost both use it to measure an outbound's age in wall-clock
// time, so they agree on when it is rescheduled whatever the block time did in
// between.
type BlockTimeHistory []BlockTimeSegment

// MsAt returns the block time of the segment that holds height.
func (h BlockTimeHistory) MsAt(height, refMs int64) int64 {
	ms := refMs
	for _, seg := range h {
		if seg.Height > height {
			break
		}
		ms = seg.Ms
	}
	return ms
}

// HeightAgo returns the newest height that is at least durMs old at now: the
// largest h for which the block times of blocks h+1 through now add up to
// durMs or more. Without history it is now - ceil(durMs/refMs); durMs <= 0
// returns now. The result can be zero or negative near genesis.
func (h BlockTimeHistory) HeightAgo(now, durMs, refMs int64) int64 {
	if durMs <= 0 {
		return now
	}
	remaining, cur := durMs, now
	for i := len(h) - 1; i >= 0; i-- {
		seg := h[i]
		if seg.Height > cur {
			continue // a segment starting after cur holds none of these blocks
		}
		span := cur - seg.Height + 1
		if span*seg.Ms >= remaining {
			return cur - (remaining+seg.Ms-1)/seg.Ms
		}
		remaining -= span * seg.Ms
		cur = seg.Height - 1
	}
	return cur - (remaining+refMs-1)/refMs
}

// Reach returns the furthest height that a window of refBlocks reference
// blocks, applied at any block up to now, could have scheduled to: blocks
// scheduled under an earlier, faster block time can sit further ahead than
// the current window. Without history it is now + ScaleDuration(refBlocks).
func (h BlockTimeHistory) Reach(now, refBlocks, refMs int64) int64 {
	reach := now + ScaleDuration(refBlocks, refMs, h.MsAt(now, refMs))
	for i, seg := range h {
		// the last block before this segment ran at the previous block time
		last := seg.Height - 1
		if last > now {
			break
		}
		prevMs := refMs
		if i > 0 {
			prevMs = h[i-1].Ms
		}
		reach = max(reach, last+ScaleDuration(refBlocks, refMs, prevMs))
	}
	return reach
}

// ElapsedMs returns the block times of blocks from+1 through to added up: the
// wall-clock time from height from to height to. It is negative when to is
// below from. Without history it is (to - from) * refMs.
func (h BlockTimeHistory) ElapsedMs(from, to, refMs int64) int64 {
	if to < from {
		return -h.ElapsedMs(to, from, refMs)
	}
	var elapsed int64
	cur := to
	for i := len(h) - 1; i >= 0 && cur > from; i-- {
		start := max(h[i].Height, from+1)
		if start > cur {
			continue
		}
		elapsed += (cur - start + 1) * h[i].Ms
		cur = start - 1
	}
	return elapsed + (cur-from)*refMs
}

// HeightAfter returns the first height at which at least durMs has passed
// since from: the smallest h for which ElapsedMs(from, h) >= durMs. Without
// history it is from + ceil(durMs/refMs); durMs <= 0 returns from.
func (h BlockTimeHistory) HeightAfter(from, durMs, refMs int64) int64 {
	if durMs <= 0 {
		return from
	}
	remaining, cur := durMs, from
	for i := 0; i <= len(h); i++ {
		// blocks cur+1 .. end run at ms
		ms, end := refMs, int64(math.MaxInt64)
		if i > 0 {
			ms = h[i-1].Ms
		}
		if i < len(h) {
			end = h[i].Height - 1
		}
		if end <= cur {
			continue
		}
		if end == math.MaxInt64 || (end-cur)*ms >= remaining {
			return cur + (remaining+ms-1)/ms
		}
		remaining -= (end - cur) * ms
		cur = end
	}
	return cur
}

// ConvertHeight moves a deadline stored as a future height when the block time
// changes from prevMs to newMs at height, so it keeps the wall-clock time it had
// left: the first height at which (deadline - (height-1)) blocks of prevMs have
// passed since height-1. Deadlines at or before height are left alone.
func ConvertHeight(height, deadline, prevMs, newMs int64) int64 {
	if deadline <= height {
		return deadline
	}
	return height - 1 + ScaleDuration(deadline-(height-1), prevMs, newMs)
}

// CoalesceReach returns the furthest height a retry can sit at that was
// rescheduled at any block up to now to the next multiple of a coalesce
// interval of refBlocks reference blocks (as LackSigning does), after every
// block time change since moved it with ConvertHeight. A retry scheduled under
// an earlier block time can sit past the next multiple of the current
// interval: blocks before a change rounded up to the interval at the previous
// block time, and a speed-up stretches what was left of it over more blocks.
// Without history it is now rounded up to a multiple of refBlocks.
func (h BlockTimeHistory) CoalesceReach(now, refBlocks, refMs int64) int64 {
	roundUp := func(height, ms int64) int64 {
		if c := ScaleDuration(refBlocks, refMs, ms); c > 1 {
			if over := height % c; over != 0 {
				height += c - over
			}
		}
		return height
	}
	reach := int64(math.MinInt64)
	for i, seg := range h {
		if seg.Height > now {
			break
		}
		prevMs := refMs
		if i > 0 {
			prevMs = h[i-1].Ms
		}
		// the last retry scheduled before the change, or an earlier one already
		// moved past it, as the change moves it
		reach = ConvertHeight(seg.Height, max(reach, roundUp(seg.Height-1, prevMs)), prevMs, seg.Ms)
	}
	return max(reach, roundUp(now, h.MsAt(now, refMs)))
}
