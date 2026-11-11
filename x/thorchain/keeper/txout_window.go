package keeper

import (
	"sync"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type txOutWindowKey struct{}

// TxOutWindow is a read-through snapshot of the stored TxOut bytes of a range
// of heights, scoped by a cosmos.Context. EndBlock walks the pending txout
// window (the last SigningTransactionPeriod heights) many times: every vault
// security sort for an outbound, every pending outbound check, fund
// migration. Most of those heights hold no TxOut, and the first Has of each
// absent height in a block reaches the app db, as the inter-block cache only
// keeps values that exist. The window loads the whole range with one store
// iteration on its first read instead.
//
// A write to a covered height (or to a txout key that is not a height)
// disables it for good, so the snapshot always holds the range as it was
// when the cache was attached: the pointer is shared with cache contexts that
// may be discarded, and a load in one of them after such a write could
// snapshot an uncommitted value. Writes to other heights (e.g. the current
// block's) leave it alone.
//
// Windows attached over each other (past and scheduled heights) stay
// independent: a read uses the innermost window covering its height, and a
// write disables only the windows covering it, so a delayed outbound disables
// the scheduled window and leaves the past one.
//
// Only attach it outside transactions (EndBlock): skipping store reads inside
// a tx changes gas_used, which is consensus-visible.
type TxOutWindow struct {
	mu       sync.Mutex
	lo, hi   int64
	entries  map[int64][]byte // present heights only
	loaded   bool
	disabled bool
	outer    *TxOutWindow // the window on ctx when this one was attached
}

// WithTxOutWindow attaches a fresh window covering heights lo..hi (from height
// 1 at most) to ctx.
//
// Invariant: every txout write made while this window is live must go through
// a ctx derived from the returned one, or the window will not see it and reads
// will keep returning the stale snapshot.
func WithTxOutWindow(ctx cosmos.Context, lo, hi int64) cosmos.Context {
	lo = max(lo, 1)
	if hi < lo {
		return ctx
	}
	return ctx.WithValue(txOutWindowKey{}, &TxOutWindow{lo: lo, hi: hi, outer: TxOutWindowFromContext(ctx)})
}

func TxOutWindowFromContext(ctx cosmos.Context) *TxOutWindow {
	window, _ := ctx.Value(txOutWindowKey{}).(*TxOutWindow)
	return window
}

// Covering returns the innermost enabled window of w and the windows it was
// attached over that covers height, or nil.
func (w *TxOutWindow) Covering(height int64) *TxOutWindow {
	for ; w != nil; w = w.outer {
		if _, _, ok := w.Range(height); ok {
			return w
		}
	}
	return nil
}

// Range returns the covered heights. ok is false if the window does not serve
// height, in which case the caller reads the store.
func (w *TxOutWindow) Range(height int64) (lo, hi int64, ok bool) {
	if w == nil {
		return 0, 0, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.disabled || height < w.lo || height > w.hi {
		return 0, 0, false
	}
	return w.lo, w.hi, true
}

// Get returns the stored bytes of height (present=false: no TxOut stored) and
// whether the window could answer, i.e. it is loaded, enabled and covers
// height. The bytes must not be modified.
func (w *TxOutWindow) Get(height int64) (bz []byte, present, ok bool) {
	if w == nil {
		return nil, false, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.disabled || !w.loaded || height < w.lo || height > w.hi {
		return nil, false, false
	}
	bz, present = w.entries[height]
	return bz, present, true
}

// Load stores the result of reading every covered height: entries holds the
// bytes of each present height. The window takes ownership of entries and its
// slices; the caller must not modify them afterwards.
func (w *TxOutWindow) Load(entries map[int64][]byte) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.disabled || w.loaded {
		return
	}
	w.entries = entries
	w.loaded = true
}

// NoteWrite disables each of w and the windows it was attached over that
// covers height. ok=false (a txout key that is not a height) disables them
// all.
func (w *TxOutWindow) NoteWrite(height int64, ok bool) {
	for ; w != nil; w = w.outer {
		w.mu.Lock()
		if !ok || (height >= w.lo && height <= w.hi) {
			w.disable()
		}
		w.mu.Unlock()
	}
}

// Disable drops the snapshot and makes every later read go to the store.
func (w *TxOutWindow) Disable() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.disable()
	w.mu.Unlock()
}

func (w *TxOutWindow) disable() {
	w.disabled = true
	w.entries = nil
}
