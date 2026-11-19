package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Tip fetch-ahead (opt-in per chain with tip_ahead; needs head_cache_ms;
// adapter layer only).
//
// Read-ahead (readahead.go) only works behind the safe head, so it does nothing
// for a scanner that is already at the tip. There, against a remote provider,
// every new block still costs the scanner a full-block call
// and a receipts call, one after the other, before it can vote.
//
// With this on, the router keeps the head current itself instead of waiting for
// the scanner to ask: while the scanner has used the chain in the last tipIdle
// it fetches the "latest" header twice per head_cache_ms and puts it in the head
// cache (eth_blockNumber is answered from that same header, so the two never
// disagree). When the head number moves it fetches
//
//	eth_getBlockByNumber [number, true]
//	eth_getBlockReceipts [hash]   or   [number]
//
// and answers the scanner's own calls from those for tipKeep.
//
// Rules:
//   - Every answer is the provider's own result for that exact call.
//   - Receipts are fetched early only in the form (by hash or by number) the
//     scanner has itself used on this backend since the router started.
//   - A block above the safe head can still be replaced. A held block is
//     dropped as soon as a later head shows a different hash at its height, or
//     names a different parent, and an early block whose hash is not the head's
//     is never held. Between such a change and the router's next poll the
//     scanner can be given the replaced block; its own canonical checks, which
//     ask for the header at that height and are not answered from here, then
//     see the new hash exactly as they would with a provider that was slow to
//     learn of the reorg.
//   - A null or failed early call is never handed out; the scanner's request is
//     forwarded.

// Atomic so tests can shorten them.
var (
	tipKeepNS atomic.Int64 // How long an early block or its receipts are answerable.
	tipIdleNS atomic.Int64 // Polling stops this long after the scanner's last call.
)

func init() {
	tipKeepNS.Store(int64(12 * time.Second))
	tipIdleNS.Store(int64(60 * time.Second))
}

const (
	receiptsByHash   int32 = 1
	receiptsByNumber int32 = 2
	tipEntries             = 64 // Two per block; a hold keeps several blocks at once.
)

type tipEntry struct {
	aheadEntry
	number uint64
	hash   string // Block hash the entry belongs to.
}

type tipAhead struct {
	mu      sync.Mutex
	entries map[string]*tipEntry // "block:<tag>" and "receipts:<param>"
	head    uint64               // Highest head number seen; 0 before the first.
	polling bool
	path    string

	interval atomic.Int64 // head_cache_ms in nanoseconds as last requested; 0 stops the poller.
	lastUse  atomic.Int64
	form     atomic.Int32 // How the scanner asks for receipts: 0 not seen yet.
}

type evmHeader struct {
	Number     string `json:"number"`
	Hash       string `json:"hash"`
	ParentHash string `json:"parentHash"`
}

func parseTag(tag string) (uint64, bool) {
	if !strings.HasPrefix(tag, "0x") {
		return 0, false
	}
	n, err := strconv.ParseUint(tag[2:], 16, 64)
	return n, err == nil && tag == blockTag(n)
}

func blockHashParam(s string) bool {
	if len(s) != 66 || s[:2] != "0x" {
		return false
	}
	for i := 2; i < len(s); i++ {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}

// receipts reports the single parameter of an eth_getBlockReceipts call and
// whether it names the block by hash or by canonical height.
func (q rpcQuery) receipts() (param string, form int32, ok bool) {
	if q.Method != "eth_getBlockReceipts" || len(q.params) != 1 {
		return "", 0, false
	}
	if blockHashParam(q.first) {
		return q.first, receiptsByHash, true
	}
	if _, isTag := parseTag(q.first); isTag {
		return q.first, receiptsByNumber, true
	}
	return "", 0, false
}

// use records the scanner's call and makes sure the head poller is running.
func (a *tipAhead) use(u *upstream, path string, ttl time.Duration) {
	a.lastUse.Store(time.Now().UnixNano())
	a.interval.Store(int64(ttl))
	a.mu.Lock()
	start := !a.polling
	a.polling, a.path = true, path
	a.mu.Unlock()
	if start {
		go u.tipPoll()
	}
}

// tipPoll keeps the head cache current while the scanner is using the chain.
func (u *upstream) tipPoll() {
	for {
		ttl := time.Duration(u.tip.interval.Load())
		u.tip.mu.Lock()
		path := u.tip.path
		if ttl <= 0 || time.Since(time.Unix(0, u.tip.lastUse.Load())) > time.Duration(tipIdleNS.Load()) {
			u.tip.polling = false
			u.tip.mu.Unlock()
			return
		}
		u.tip.mu.Unlock()
		time.Sleep(ttl / 2)
		header, ok := u.fetchRPC(&url.URL{Path: path}, nil, "eth_getBlockByNumber", `["latest",false]`, headResponseLimit)
		if ok {
			u.tipHead(path, header, true)
		}
	}
}

// tipHead reacts to a freshly fetched latest header: the router's own poll
// (install, which also puts it in the head cache) or the scanner's own query.
func (u *upstream) tipHead(path string, header json.RawMessage, install bool) {
	var h evmHeader
	if json.Unmarshal(header, &h) != nil {
		return
	}
	n, ok := parseTag(h.Number)
	if !ok || !blockHashParam(h.Hash) {
		return
	}
	holding := install && u.hold.n.Load() > 0
	if install && !holding {
		u.heads.put(path+` eth_getBlockByNumber["latest",false]`, header)
		u.heads.put(path+" eth_blockNumber[]", json.RawMessage(`"`+h.Number+`"`))
	}

	u.tip.mu.Lock()
	// Anything held for this height under another hash, or for the height below
	// under a hash that is not this head's parent, has been replaced.
	for key, e := range u.tip.entries {
		if e.number == n && e.hash != h.Hash || e.number+1 == n && e.hash != h.ParentHash {
			delete(u.tip.entries, key)
		}
	}
	old := u.tip.head
	if n > old {
		u.tip.head = n
	}
	u.tip.mu.Unlock()
	if holding {
		// The hold decides what the scanner sees and makes the early calls.
		u.holdHead(path, n, header, h.Hash, h.ParentHash)
		return
	}
	// The first head seen is where the scanner already is.
	if old > 0 && n > old {
		go u.tipFetch(path, n, h.Hash)
	}
}

// reserve registers an in-flight entry unless one is held or due for key.
func (a *tipAhead) reserve(key string, number uint64, hash string) *tipEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.entries == nil {
		a.entries = map[string]*tipEntry{}
	}
	keep := time.Duration(tipKeepNS.Load())
	for k, e := range a.entries {
		select {
		case <-e.done:
			if time.Since(e.at) > keep {
				delete(a.entries, k)
			}
		default:
		}
	}
	if a.entries[key] != nil || len(a.entries) >= tipEntries {
		return nil
	}
	e := &tipEntry{aheadEntry: aheadEntry{done: make(chan struct{})}, number: number, hash: hash}
	a.entries[key] = e
	return e
}

// tipFetch makes the early calls for the new head block.
func (u *upstream) tipFetch(path string, number uint64, hash string) {
	tag := blockTag(number)
	if e := u.tip.reserve("block:"+tag, number, hash); e != nil {
		go func() {
			u.ahead.fetches.Add(1)
			block, ok := u.fetchRPC(&url.URL{Path: path}, nil, "eth_getBlockByNumber", `["`+tag+`",true]`, blockLimit)
			var b evmHeader
			// The provider may already be on another block at this height.
			finish(&e.aheadEntry, block, ok && json.Unmarshal(block, &b) == nil && b.Hash == hash)
		}()
	}
	if !u.demand.wanted() {
		return // The scanner now skips most receipts; a hold fetches its own.
	}
	param := ""
	switch u.tip.form.Load() {
	case receiptsByHash:
		param = hash
	case receiptsByNumber:
		param = tag
	default:
		return // The scanner has not asked for block receipts here.
	}
	if e := u.tip.reserve("receipts:"+param, number, hash); e != nil {
		u.ahead.fetches.Add(1)
		receipts, ok := u.fetchRPC(&url.URL{Path: path}, nil, "eth_getBlockReceipts", `["`+param+`"]`, blockLimit)
		finish(&e.aheadEntry, receipts, ok)
	}
}

// held hands over an early answer, waiting for it if the call is in flight.
func (a *tipAhead) held(ctx context.Context, key string) (json.RawMessage, bool) {
	a.mu.Lock()
	e := a.entries[key]
	a.mu.Unlock()
	if e == nil {
		return nil, false
	}
	select {
	case <-e.done:
	case <-ctx.Done():
		return nil, false
	}
	a.mu.Lock()
	current := a.entries[key] == e // Not dropped as replaced while we waited.
	a.mu.Unlock()
	if !current || !e.ok || time.Since(e.at) > time.Duration(tipKeepNS.Load()) {
		return nil, false
	}
	return e.result, true
}

// tipTry answers a full-block or receipts query from an early call when
// tip_ahead is on, and notes how the scanner asks for receipts.
func (u *upstream) tipTry(ch Chain, req *http.Request, q rpcQuery) (json.RawMessage, bool) {
	if !ch.TipAhead {
		return nil, false
	}
	var result json.RawMessage
	var ok bool
	if param, form, isReceipts := q.receipts(); isReceipts {
		u.tip.form.Store(form)
		result, ok = u.tip.held(req.Context(), "receipts:"+param)
	} else if height, full, isBlock := q.block(); isBlock && full {
		result, ok = u.tip.held(req.Context(), "block:"+blockTag(height))
	}
	if ok {
		u.ahead.hits.Add(1)
	}
	return result, ok
}
