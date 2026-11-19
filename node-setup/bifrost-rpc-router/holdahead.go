package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Confirmation hold (opt-in per chain with hold_blocks; needs tip_ahead and
// call_ahead; adapter layer only).
//
// On a chain whose confirmation count passes in about a second, Bifrost sends
// an unfinalised observation whenever its scanner is fewer than that many
// blocks past a transaction's block at the moment it flushes its observations,
// and the final observation soon after. Only part of the network sends the
// unfinalised one, so it does not reach consensus and each node that sent it
// keeps the pre-consensus slash point.
//
// With this on, the router decides which head the scanner sees. It already
// fetches every new head block and its receipts (tip_ahead). A block that
// touches a vault or the router contract is not shown until hold_blocks later
// blocks exist; then the head moves past all of them at once, the scanner reads
// them from the early replies in one pass, and by the time it flushes it is
// hold_blocks past the transaction and sends only the final observation. Every
// other block is shown as soon as it has been fetched.
//
// Rules:
//   - Nothing is invented. The head the scanner is given is a header the
//     provider returned earlier: its "latest" reply for the full-header form,
//     and its reply for that exact height for eth_getHeaderByNumber. Blocks and
//     receipts are the provider's own replies, as with tip_ahead.
//   - The addresses come from the scanner itself: the vaults are the addresses
//     of the eth_getBalance queries in the solvency run call_ahead remembers,
//     and the router contract is the target of the eth_call queries a vault
//     makes in that run. Until a run has been seen nothing is held.
//   - A block is a vault block when one of those addresses appears, as 40 hex
//     digits, anywhere in its full-block reply or its receipts. That is
//     deliberately loose (calldata and log topics count): a false match costs
//     hold_blocks block times, a miss leaves today's behaviour.
//   - It fails open. A block that cannot be fetched within holdInspect, a hold
//     older than holdMax, a poller that has stopped, or a head that moved far
//     backwards or ahead all show the provider's head. A poll that comes back a
//     few blocks behind the shown head is ignored.
//   - A held block replaced by a reorg is dropped by tip_ahead as usual; the
//     replacement is not inspected again and may be shown without a hold.

// Atomic so tests can shorten them.
var (
	holdInspectNS atomic.Int64 // A block not fetched within this is shown uninspected.
	holdMaxNS     atomic.Int64 // A vault block is held no longer than this.
)

func init() {
	holdInspectNS.Store(int64(1500 * time.Millisecond))
	holdMaxNS.Store(int64(4 * time.Second))
}

const (
	holdMaxBlocks = 8   // Upper bound of hold_blocks.
	holdMaxGap    = 128 // A head this far past the last one seen restarts the hold.
	holdBehind    = 16  // A polled head up to this far below the shown one is ignored.
)

type holdBlock struct {
	since    time.Time
	resolved bool
	relevant bool
}

// holdHeader is the provider's eth_getHeaderByNumber reply for one height.
type holdHeader struct {
	raw  json.RawMessage
	hash string
	at   time.Time
}

type holdAhead struct {
	mu      sync.Mutex
	visible uint64          // Head the scanner is shown; 0 before the first poll.
	header  json.RawMessage // The provider's ["latest",false] reply for visible.
	heads   map[uint64]json.RawMessage
	blocks  map[uint64]*holdBlock  // Heights above visible.
	headers map[uint64]*holdHeader // Recent heights, shown or not.
	top     uint64                 // Highest head seen.
	polled  time.Time

	n     atomic.Int32  // hold_blocks as last requested; 0 is off.
	holds atomic.Uint64 // Vault blocks shown only after their hold.
	// Times the hold started over: stale poller, or a head far behind or ahead.
	resets atomic.Uint64
	seen   atomic.Uint64 // Blocks inspected.
}

// watched returns the vault addresses and router contracts of the remembered
// solvency run, as 40 lowercase hex digits each.
func (a *callAhead) watched() [][]byte {
	a.mu.Lock()
	known := a.known
	a.mu.Unlock()
	vaults := map[string]bool{}
	for _, t := range known {
		var address string
		if t.method == "eth_getBalance" && json.Unmarshal([]byte(t.first), &address) == nil && blockAddress(address) {
			vaults[strings.ToLower(address)] = true
		}
	}
	if len(vaults) == 0 {
		return nil
	}
	set := map[string]bool{}
	for v := range vaults {
		set[v[2:]] = true
	}
	for _, t := range known {
		var call struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if t.method == "eth_call" && json.Unmarshal([]byte(t.first), &call) == nil && vaults[strings.ToLower(call.From)] && blockAddress(call.To) {
			set[strings.ToLower(call.To)[2:]] = true
		}
	}
	out := make([][]byte, 0, len(set))
	for s := range set {
		out = append(out, []byte(s))
	}
	return out
}

func blockAddress(s string) bool {
	if len(s) != 42 || s[:2] != "0x" {
		return false
	}
	for i := 2; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func containsAny(reply []byte, needles [][]byte) bool {
	for _, n := range needles {
		if bytes.Contains(reply, n) {
			return true
		}
	}
	return false
}

// stale is how long after the last poll the held head is still answerable.
func (u *upstream) holdStale() time.Duration {
	return 2*time.Duration(u.tip.interval.Load()) + time.Second
}

// holdHead takes over from the head cache for a freshly polled latest header.
func (u *upstream) holdHead(path string, n uint64, header json.RawMessage, hash, parent string) {
	h := &u.hold
	now := time.Now()
	h.mu.Lock()
	if n < h.visible && n+holdBehind >= h.visible && now.Sub(h.polled) <= u.holdStale() {
		// A provider behind a load balancer often answers one poll from a node
		// a block or two behind. That is not the chain going backwards.
		h.polled = now
		h.release(now)
		h.mu.Unlock()
		return
	}
	if h.visible == 0 || now.Sub(h.polled) > u.holdStale() || n < h.visible || n > h.top+holdMaxGap {
		if h.visible != 0 {
			h.resets.Add(1)
		}
		h.visible, h.header, h.top, h.polled = n, header, n, now
		h.heads, h.blocks, h.headers = map[uint64]json.RawMessage{}, map[uint64]*holdBlock{}, map[uint64]*holdHeader{}
		h.mu.Unlock()
		go u.holdHeaderFetch(path, n, hash)
		return
	}
	h.polled = now
	// A header held for this height under another hash, or for the height below
	// under a hash that is not this head's parent, has been replaced.
	for k, e := range h.headers {
		if k == n && e.hash != hash || k+1 == n && e.hash != parent || k+64 < h.visible {
			delete(h.headers, k)
		}
	}
	var inspect []uint64
	if n > h.visible {
		h.heads[n] = header
	}
	for k := h.top + 1; k <= n; k++ {
		h.blocks[k] = &holdBlock{since: now}
		inspect = append(inspect, k)
	}
	if n > h.top {
		h.top = n
	}
	h.release(now)
	h.mu.Unlock()
	for _, k := range inspect {
		expected := ""
		if k == n {
			expected = hash
		}
		go u.holdInspect(path, k, expected)
	}
}

// release moves the visible head to the highest polled head that leaves no
// uninspected block and no vault block with fewer than hold_blocks after it.
// The caller holds mu.
func (h *holdAhead) release(now time.Time) {
	need := uint64(h.n.Load())
	inspect, limit := time.Duration(holdInspectNS.Load()), time.Duration(holdMaxNS.Load())
	polled := make([]uint64, 0, len(h.heads))
	for n := range h.heads {
		polled = append(polled, n)
	}
	sort.Slice(polled, func(i, j int) bool { return polled[i] < polled[j] })
	best, shown := uint64(0), uint64(0)
	for _, candidate := range polled {
		// The scanner asks for the head as a bare header too, so that reply must
		// be in hand before the head moves there.
		ok, vaultBlocks := h.headers[candidate] != nil || h.blocks[candidate] == nil || now.Sub(h.blocks[candidate].since) > inspect, uint64(0)
		for k := h.visible + 1; k <= candidate && ok; k++ {
			b := h.blocks[k]
			switch {
			case b == nil:
			case !b.resolved:
				ok = now.Sub(b.since) > inspect
			case b.relevant:
				vaultBlocks++
				ok = candidate-k >= need || now.Sub(b.since) > limit
			}
		}
		if ok {
			best, shown = candidate, vaultBlocks
		}
	}
	if best == 0 {
		return
	}
	h.holds.Add(shown)
	h.visible, h.header = best, h.heads[best]
	for n := range h.heads {
		if n <= best {
			delete(h.heads, n)
		}
	}
	for n := range h.blocks {
		if n <= best {
			delete(h.blocks, n)
		}
	}
}

// holdInspect fetches block k and its receipts, as tip_ahead would, and
// records whether it is a vault block. expected is the head hash when k was
// the polled head, empty for a height the poll skipped.
func (u *upstream) holdInspect(path string, k uint64, expected string) {
	tag := blockTag(k)
	bare := make(chan struct{})
	go func() {
		u.holdHeaderFetch(path, k, expected)
		close(bare)
	}()
	entry := u.tip.reserve("block:"+tag, k, expected)
	u.ahead.fetches.Add(1)
	block, ok := u.fetchRPC(&url.URL{Path: path}, nil, "eth_getBlockByNumber", `["`+tag+`",true]`, blockLimit)
	var b evmHeader
	ok = ok && json.Unmarshal(block, &b) == nil && blockHashParam(b.Hash) && (expected == "" || b.Hash == expected)
	if entry != nil {
		if ok {
			u.tip.mu.Lock()
			entry.hash = b.Hash
			u.tip.mu.Unlock()
		}
		finish(&entry.aheadEntry, block, ok)
	}
	var receipts json.RawMessage
	if ok {
		param := ""
		switch u.tip.form.Load() {
		case receiptsByHash:
			param = b.Hash
		case receiptsByNumber:
			param = tag
		}
		if param != "" {
			held := u.tip.reserve("receipts:"+param, k, b.Hash)
			u.ahead.fetches.Add(1)
			var got bool
			receipts, got = u.fetchRPC(&url.URL{Path: path}, nil, "eth_getBlockReceipts", `["`+param+`"]`, blockLimit)
			if held != nil {
				finish(&held.aheadEntry, receipts, got)
			}
		}
	}
	relevant := false
	if ok {
		if watched := u.calls.watched(); len(watched) > 0 {
			relevant = containsAny(block, watched) || containsAny(receipts, watched)
		}
	}
	<-bare
	if ok {
		u.hold.seen.Add(1)
	}
	h := &u.hold
	h.mu.Lock()
	if b := h.blocks[k]; b != nil {
		// A block that could not be fetched stays unresolved and is shown after
		// holdInspect, so the scanner asks the provider for it itself.
		b.resolved, b.relevant = ok, relevant
		h.release(time.Now())
	}
	h.mu.Unlock()
}

// holdAnswer answers the scanner's own latest-head queries from the held head.
func (u *upstream) holdAnswer(ch Chain, method, params string) (json.RawMessage, bool) {
	if ch.HoldBlocks == 0 || !ch.TipAhead {
		return nil, false
	}
	h := &u.hold
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.visible == 0 || time.Since(h.polled) > u.holdStale() {
		return nil, false
	}
	switch {
	case method == "eth_blockNumber":
		return json.RawMessage(`"` + blockTag(h.visible) + `"`), true
	case method == "eth_getBlockByNumber" && params == `["latest",false]`:
		return h.header, true
	case method == "eth_getHeaderByNumber" && params == `["latest"]`:
		if e := h.headers[h.visible]; e != nil && time.Since(e.at) < time.Duration(tipKeepNS.Load()) {
			return e.raw, true
		}
	}
	return nil, false
}

// holdHeaderFetch asks the provider for the bare header at height k and keeps
// it, so that the scanner's own query for that header, by height or as the
// held head, gets the provider's reply to that call.
func (u *upstream) holdHeaderFetch(path string, k uint64, expected string) {
	u.ahead.fetches.Add(1)
	raw, ok := u.fetchRPC(&url.URL{Path: path}, nil, "eth_getHeaderByNumber", `["`+blockTag(k)+`"]`, headResponseLimit)
	var b evmHeader
	if !ok || json.Unmarshal(raw, &b) != nil || !blockHashParam(b.Hash) || expected != "" && b.Hash != expected {
		return
	}
	h := &u.hold
	h.mu.Lock()
	if h.headers != nil {
		h.headers[k] = &holdHeader{raw: raw, hash: b.Hash, at: time.Now()}
	}
	h.mu.Unlock()
}

// holdByHeight answers the scanner's bare-header query for a height it has
// been shown, from the provider's reply to that same call.
func (u *upstream) holdByHeight(ch Chain, q rpcQuery) (json.RawMessage, bool) {
	if ch.HoldBlocks == 0 || !ch.TipAhead || q.Method != "eth_getHeaderByNumber" || len(q.params) != 1 {
		return nil, false
	}
	k, ok := parseTag(q.first)
	if !ok {
		return nil, false
	}
	h := &u.hold
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := h.headers[k]; e != nil && k <= h.visible && time.Since(e.at) < time.Duration(tipKeepNS.Load()) {
		return e.raw, true
	}
	return nil, false
}
