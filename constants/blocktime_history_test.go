package constants

import "testing"

var testHistories = []BlockTimeHistory{
	nil,
	{{Height: 50, Ms: 2000}},  // faster
	{{Height: 50, Ms: 10000}}, // slower
	{{Height: 40, Ms: 2000}, {Height: 70, Ms: 6000}}, // faster, then back
	{{Height: 30, Ms: 1500}, {Height: 45, Ms: 4000}, {Height: 60, Ms: 1700}},
}

// naiveElapsedMs adds up the block times of blocks (from, to] one by one.
func naiveElapsedMs(h BlockTimeHistory, from, to, refMs int64) int64 {
	var ms int64
	for b := from + 1; b <= to; b++ {
		ms += h.MsAt(b, refMs)
	}
	return ms
}

func TestHeightAgoMatchesBlockByBlockSum(t *testing.T) {
	const refMs = 6000
	for _, h := range testHistories {
		for _, durMs := range []int64{1, 5999, 6000, 6001, 60_000, 90_500, 180_000} {
			for now := int64(1); now <= 120; now++ {
				got := h.HeightAgo(now, durMs, refMs)
				if naiveElapsedMs(h, got, now, refMs) < durMs {
					t.Fatalf("%v: HeightAgo(%d, %d) = %d is younger than %d ms", h, now, durMs, got, durMs)
				}
				if naiveElapsedMs(h, got+1, now, refMs) >= durMs {
					t.Fatalf("%v: HeightAgo(%d, %d) = %d is not the newest such height", h, now, durMs, got)
				}
			}
		}
	}
}

// Taking each block's (HeightAgo(now-1), HeightAgo(now)] checks every height
// exactly once, in the first block at which it is durMs old.
func TestHeightAgoRangesCoverEachHeightOnce(t *testing.T) {
	const refMs, durMs = 6000, 60_000
	for _, h := range testHistories {
		checked := make(map[int64]int64)
		for now := int64(1); now <= 200; now++ {
			for height := h.HeightAgo(now-1, durMs, refMs) + 1; height <= h.HeightAgo(now, durMs, refMs); height++ {
				if prev, ok := checked[height]; ok {
					t.Fatalf("%v: height %d checked at %d and %d", h, height, prev, now)
				}
				checked[height] = now
				if naiveElapsedMs(h, height, now, refMs) < durMs || naiveElapsedMs(h, height, now-1, refMs) >= durMs {
					t.Fatalf("%v: height %d checked at %d, not when it became %d ms old", h, height, now, durMs)
				}
			}
		}
		for height := int64(0); height <= 150; height++ {
			if _, ok := checked[height]; !ok {
				t.Fatalf("%v: height %d never checked", h, height)
			}
		}
	}
}

func TestBlockTimeHistoryIdentity(t *testing.T) {
	const refMs = 6000
	var h BlockTimeHistory
	for _, now := range []int64{1, 300, 1_000_000} {
		if got := h.HeightAgo(now, 300*refMs, refMs); got != now-300 {
			t.Errorf("HeightAgo(%d) = %d, want %d", now, got, now-300)
		}
		if got := h.Reach(now, 720, refMs); got != now+720 {
			t.Errorf("Reach(%d) = %d, want %d", now, got, now+720)
		}
	}
	if got := h.HeightAgo(10, 0, refMs); got != 10 {
		t.Errorf("HeightAgo with no duration = %d, want 10", got)
	}
}

// Reach is the furthest height a window could have scheduled to from any block
// so far.
func TestReachMatchesBlockByBlockMax(t *testing.T) {
	const refMs, refBlocks = 6000, 20
	for _, h := range testHistories {
		for now := int64(1); now <= 120; now++ {
			want := int64(0)
			for b := int64(0); b <= now; b++ {
				want = max(want, b+ScaleDuration(refBlocks, refMs, h.MsAt(b, refMs)))
			}
			if got := h.Reach(now, refBlocks, refMs); got != want {
				t.Fatalf("%v: Reach(%d) = %d, want %d", h, now, got, want)
			}
		}
	}
}

func TestElapsedMsAndHeightAfterMatchBlockByBlock(t *testing.T) {
	const refMs = 6000
	for _, h := range testHistories {
		for from := int64(0); from <= 90; from += 3 {
			for to := int64(0); to <= 120; to++ {
				want := naiveElapsedMs(h, from, to, refMs)
				if to < from {
					want = -naiveElapsedMs(h, to, from, refMs)
				}
				if got := h.ElapsedMs(from, to, refMs); got != want {
					t.Fatalf("%v: ElapsedMs(%d, %d) = %d, want %d", h, from, to, got, want)
				}
			}
			for _, durMs := range []int64{1, 5999, 6000, 6001, 60_000, 90_500, 1_000_000} {
				got := h.HeightAfter(from, durMs, refMs)
				if naiveElapsedMs(h, from, got, refMs) < durMs || (got > from && naiveElapsedMs(h, from, got-1, refMs) >= durMs) {
					t.Fatalf("%v: HeightAfter(%d, %d) = %d is not the first height that old", h, from, durMs, got)
				}
			}
		}
	}
	var none BlockTimeHistory
	if got := none.HeightAfter(100, 300*refMs, refMs); got != 400 {
		t.Errorf("HeightAfter without history = %d, want 400", got)
	}
	if got := none.ElapsedMs(100, 400, refMs); got != 300*refMs {
		t.Errorf("ElapsedMs without history = %d, want %d", got, 300*refMs)
	}
}
