package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Read-ahead (opt-in per chain with read_ahead_blocks; adapter layer only).
//
// A scanner that is behind asks for block N, then N's receipts, then block N+1,
// one provider round trip at a time. Every one of those calls is certain to
// come, so when the router sees the request for block N it asks the provider
// for N's receipts and for the next few blocks and their receipts straight
// away, and hands each answer over when the scanner asks for it. Receipts are
// only fetched early while the scanner is asking for them (see receiptDemand).
//
// Rules that keep this invisible to the scanner:
//   - Only heights at or below the provider's safe head are fetched early or
//     answered from here. The safe head is learned from the scanner's own
//     "safe" query; until one has been seen nothing happens. Blocks above it,
//     which can still be replaced, always go to the provider when asked.
//   - Only eth_getBlockByNumber [height, true] and eth_getBlockReceipts [height]
//     are involved by default. Header-by-height queries always reach the
//     provider unless read_ahead_headers is also set (see readAheadHeader).
//   - An answer is the provider's own result for that exact call, is handed out
//     once, and is dropped after readAheadKeep if nobody asks.
//   - A null or failed early call is never handed out: the scanner's request is
//     forwarded and gets the provider's own reply.
//
// The provider sees the same calls it would have seen anyway, a little earlier.

const (
	readAheadKeep    = 5 * time.Second
	readAheadEntries = 64       // Hard bound; normal use holds 2 x read_ahead_blocks.
	blockLimit       = 32 << 20 // Same order as the proxy's own response bound.
)

type aheadEntry struct {
	done   chan struct{}
	at     time.Time // Set before done is closed.
	result json.RawMessage
	ok     bool
}

type readAhead struct {
	mu      sync.Mutex
	entries map[string]*aheadEntry
	safe    atomic.Uint64
	fetches atomic.Uint64
	hits    atomic.Uint64
}

// receiptDemand tracks whether the scanner still asks for block receipts.
// Recent Bifrost releases decide from the block itself whether they need them
// and skip most; earlier versions asked for every block. Early receipt calls are made
// only while the scanner asked for receipts for at least half of the last
// receiptWindow blocks it fetched, so an early call is still one the provider
// would have received anyway. Until the first window is complete they are made.
type receiptDemand struct {
	mu               sync.Mutex
	blocks, receipts int
	off              bool
}

const receiptWindow = 32

func (d *receiptDemand) note(q rpcQuery) {
	_, full, isBlock := q.block()
	_, _, isReceipts := q.receipts()
	if !(isBlock && full) && !isReceipts {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if isReceipts {
		d.receipts++
		return
	}
	if d.blocks++; d.blocks >= receiptWindow {
		d.off = d.receipts*2 < d.blocks
		d.blocks, d.receipts = 0, 0
	}
}

func (d *receiptDemand) wanted() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.off
}

// learnSafe records the height of a safe-head header. It only moves forward.
func (a *readAhead) learnSafe(header json.RawMessage) {
	var h struct {
		Number string `json:"number"`
	}
	if json.Unmarshal(header, &h) != nil || len(h.Number) < 3 {
		return
	}
	n, err := strconv.ParseUint(h.Number[2:], 16, 64)
	if err != nil {
		return
	}
	for {
		old := a.safe.Load()
		if n <= old || a.safe.CompareAndSwap(old, n) {
			return
		}
	}
}

// start makes fetch run once for key unless an answer is already held or due.
func (a *readAhead) start(key string, fetch func() (json.RawMessage, bool)) {
	a.mu.Lock()
	if a.entries == nil {
		a.entries = map[string]*aheadEntry{}
	}
	for k, e := range a.entries {
		select {
		case <-e.done:
			if time.Since(e.at) > readAheadKeep {
				delete(a.entries, k)
			}
		default:
		}
	}
	if a.entries[key] != nil || len(a.entries) >= readAheadEntries {
		a.mu.Unlock()
		return
	}
	e := &aheadEntry{done: make(chan struct{})}
	a.entries[key] = e
	a.mu.Unlock()
	a.fetches.Add(1)
	go func() {
		e.result, e.ok = fetch()
		e.at = time.Now()
		close(e.done)
	}()
}

// take hands over the answer for key, waiting for it if the call is in flight.
// ok is false when there is no usable answer; the caller forwards its request.
func (a *readAhead) take(req *http.Request, key string) (json.RawMessage, bool) {
	a.mu.Lock()
	e := a.entries[key]
	a.mu.Unlock()
	if e == nil {
		return nil, false
	}
	select {
	case <-e.done:
	case <-req.Context().Done():
		return nil, false
	}
	a.mu.Lock()
	if a.entries[key] == e {
		delete(a.entries, key)
	}
	a.mu.Unlock()
	if !e.ok || time.Since(e.at) > readAheadKeep {
		return nil, false
	}
	a.hits.Add(1)
	return e.result, true
}

// readAhead serves a block or receipts query from an early call if one was
// made, and on a block query starts the early calls the scanner will need next.
func (u *upstream) readAhead(req *http.Request, height uint64, full bool, ahead int) (json.RawMessage, bool) {
	safe := u.ahead.safe.Load()
	if height > safe {
		return nil, false
	}
	path, header := req.URL.Path, req.Header.Clone()
	target := *req.URL
	key := func(method string, h uint64) string { return path + " " + method + " " + blockTag(h) }
	early := func(method string, h uint64, params string) {
		u.ahead.start(key(method, h), func() (json.RawMessage, bool) {
			return u.fetchRPC(&target, header, method, params, blockLimit)
		})
	}
	if full {
		for h := height; h <= safe && h <= height+uint64(ahead); h++ {
			if h > height {
				early("eth_getBlockByNumber", h, `["`+blockTag(h)+`",true]`)
			}
			if u.demand.wanted() {
				early("eth_getBlockReceipts", h, `["`+blockTag(h)+`"]`)
			}
		}
		return u.ahead.take(req, key("eth_getBlockByNumber", height))
	}
	return u.ahead.take(req, key("eth_getBlockReceipts", height))
}

// readAheadHeader (read_ahead_headers) extends read-ahead to header-by-height
// queries. An EVM scanner can send header queries with every block, at the same
// instant as the block request, for that height and for a fixed distance
// behind it. Both streams advance by one per block, so a header query for h
// predicts queries for h+1, h+2, ... and those are fetched now.
//
// Each answer is still the provider's own reply for that exact height, fetched
// at most readAheadKeep earlier than the scanner would have asked, and only for
// heights at or below the safe head, where a header no longer changes. Heights
// above the safe head are forwarded untouched and fetch nothing.
func (u *upstream) readAheadHeader(req *http.Request, q rpcQuery, suffix string, ahead int) (json.RawMessage, bool) {
	height, err := strconv.ParseUint(q.first[2:], 16, 64)
	safe := u.ahead.safe.Load()
	if err != nil || height > safe {
		return nil, false
	}
	path, header, method := req.URL.Path, req.Header.Clone(), q.Method
	target := *req.URL
	key := func(h uint64) string { return path + " header " + method + " " + blockTag(h) }
	for h := height + 1; h <= safe && h <= height+uint64(ahead); h++ {
		params := `["` + blockTag(h) + `"` + suffix
		u.ahead.start(key(h), func() (json.RawMessage, bool) {
			return u.fetchRPC(&target, header, method, params, headResponseLimit)
		})
	}
	return u.ahead.take(req, key(height))
}
