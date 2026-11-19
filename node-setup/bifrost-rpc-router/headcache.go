package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Head cache (opt-in per chain with head_cache_ms; meant for the adapter layer).
//
// Bifrost's EVM scanners ask for the chain head several times for every block
// they scan, as "latest" and "safe" header queries. Those queries carry no height, so within a short
// window they all have the same answer. With the cache on, exactly these
// requests are answered from one provider call:
//
//	eth_blockNumber        []
//	eth_getBlockByNumber   ["latest"|"safe"|"finalized", false]
//	eth_getHeaderByNumber  ["latest"|"safe"|"finalized"]
//
// The caller sees a head that is at most head_cache_ms older than a fresh call
// would return, which is indistinguishable from a provider that is that much
// slower to learn of a block. Everything else, including any request that names
// a height or hash, any batch that is not only header queries (see headerBatch),
// and any failed head query, is forwarded untouched.
// Entries belong to one backend's upstream, so a route switch or reload never
// serves another provider's head.

const (
	headRequestLimit  = 4 << 10 // Head queries are about 80 bytes, a pinned eth_call about 350; larger bodies are never read here.
	headResponseLimit = 1 << 20 // A header is about 1 KiB.
)

var headTags = map[string]bool{"latest": true, "safe": true, "finalized": true}

type rpcQuery struct {
	ID     json.RawMessage
	Method string
	raw    json.RawMessage // params as sent
	params []any
	first  string // params[0] when it is a string
}

// parseRPC accepts one plain JSON-RPC call with a scalar id. Anything unexpected
// (unknown field, duplicate key, batch, notification) is left to the provider.
func parseRPC(body []byte) (q rpcQuery, ok bool) {
	var in struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if strictJSON(body, &in) != nil || in.JSONRPC != "2.0" || len(in.ID) == 0 || !(in.ID[0] == '"' || in.ID[0] == '-' || in.ID[0] >= '0' && in.ID[0] <= '9') {
		return q, false
	}
	q = rpcQuery{ID: in.ID, Method: in.Method, raw: in.Params}
	if len(in.Params) != 0 && json.Unmarshal(in.Params, &q.params) != nil {
		return q, false
	}
	if len(q.params) > 0 {
		q.first, _ = q.params[0].(string)
	}
	return q, true
}

// head reports the canonical params of a cacheable head query.
func (q rpcQuery) head() (params string, ok bool) {
	switch {
	case q.Method == "eth_blockNumber" && len(q.params) == 0 && (len(q.raw) == 0 || q.raw[0] == '['):
		return "[]", true
	case q.Method == "eth_getBlockByNumber" && len(q.params) == 2 && headTags[q.first] && q.params[1] == false:
		return `["` + q.first + `",false]`, true
	case q.Method == "eth_getHeaderByNumber" && len(q.params) == 1 && headTags[q.first]:
		return `["` + q.first + `"]`, true
	}
	return "", false
}

// block reports the height of a full-block or block-receipts query made by
// canonical height, the two calls the scanner makes once for every block.
func (q rpcQuery) block() (height uint64, full, ok bool) {
	switch {
	case q.Method == "eth_getBlockByNumber" && len(q.params) == 2 && q.params[1] == true:
		full = true
	case q.Method == "eth_getBlockReceipts" && len(q.params) == 1:
	default:
		return 0, false, false
	}
	if !strings.HasPrefix(q.first, "0x") {
		return 0, false, false
	}
	height, err := strconv.ParseUint(q.first[2:], 16, 64)
	if err != nil || q.first != blockTag(height) {
		return 0, false, false
	}
	return height, full, true
}

// headerByHeight reports a header query made by canonical height, and the
// params suffix of the matching head query form.
func (q rpcQuery) headerByHeight() (suffix string, ok bool) {
	switch {
	case q.Method == "eth_getBlockByNumber" && len(q.params) == 2 && q.params[1] == false:
		suffix = ",false]"
	case q.Method == "eth_getHeaderByNumber" && len(q.params) == 1:
		suffix = "]"
	default:
		return "", false
	}
	if !strings.HasPrefix(q.first, "0x") {
		return "", false
	}
	height, err := strconv.ParseUint(q.first[2:], 16, 64)
	return suffix, err == nil && q.first == blockTag(height)
}

func blockTag(height uint64) string { return "0x" + strconv.FormatUint(height, 16) }

type headEntry struct {
	sent   time.Time     // When the provider call was sent: the answer is at least this fresh.
	done   chan struct{} // Closed when the call finished.
	result json.RawMessage
	ok     bool
	// Set once a replacement call has been started for this entry.
	refreshing bool
}

// number is the height of a cached head header ("" for eth_blockNumber).
func (e *headEntry) number() string {
	var h struct {
		Number string `json:"number"`
	}
	if len(e.result) == 0 || e.result[0] != '{' || json.Unmarshal(e.result, &h) != nil {
		return ""
	}
	return h.Number
}

// byNumber answers a header-by-height query when the height is exactly that of
// a head header this cache handed out recently: the scanner fetches the head,
// then keeps asking for that same header by its number until it takes a newer
// head. The
// answer is the head reply it was given. Because the scanner goes on using a
// head after the cache has moved to a newer one, replies replaced by a
// background refresh stay answerable here until they are 2 x ttl old. Heights
// that were not recently a head, which includes every re-check of an already
// scanned block behind the head, are never answered here.
func (c *headCache) byNumber(prefix, suffix, height string, ttl time.Duration) (json.RawMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for tag := range headTags {
		key := prefix + `["` + tag + `"` + suffix
		if e := c.entries[key]; e != nil {
			select {
			case <-e.done:
				if e.ok && time.Since(e.sent) < ttl && e.number() == height {
					c.hits.Add(1)
					return e.result, true
				}
			default:
			}
		}
		for _, e := range c.replaced[key] {
			if time.Since(e.sent) < 2*ttl && e.number() == height {
				c.hits.Add(1)
				return e.result, true
			}
		}
	}
	return nil, false
}

type headCache struct {
	mu      sync.Mutex
	entries map[string]*headEntry // At most one per cacheable query form and path.
	// The last few successful entries a refresh replaced, newest first, for byNumber only.
	replaced map[string][]*headEntry
	hits     atomic.Uint64
}

// get returns a result no older than ttl, making at most one provider call per
// key at a time. ok is false when no usable result exists; the caller then
// forwards its own request so the provider's exact reply reaches it.
//
// An entry that is still being asked for is replaced before it expires: the
// first request after half its life starts the next provider call in the
// background and is answered from the current entry. A steadily polling scanner
// therefore never waits a provider round trip for a head, and the provider is
// asked about twice per ttl instead of once per request.
func (c *headCache) get(ctx context.Context, key string, ttl time.Duration, fetch func() (json.RawMessage, bool)) (json.RawMessage, bool) {
	c.mu.Lock()
	e := c.entries[key]
	if e != nil {
		select {
		case <-e.done:
			if age := time.Since(e.sent); e.ok && age < ttl {
				if age > ttl/2 && !e.refreshing {
					e.refreshing = true
					go c.refresh(key, e, fetch)
				}
				c.mu.Unlock()
				c.hits.Add(1)
				return e.result, true
			}
			e = nil
		default:
		}
	}
	if e != nil { // In flight: share it.
		c.mu.Unlock()
		select {
		case <-e.done:
		case <-ctx.Done():
			return nil, false
		}
		if e.ok {
			c.hits.Add(1)
		}
		return e.result, e.ok
	}
	e = &headEntry{sent: time.Now(), done: make(chan struct{})}
	if c.entries == nil {
		c.entries = map[string]*headEntry{}
	}
	c.entries[key] = e
	c.mu.Unlock()
	e.result, e.ok = fetch()
	close(e.done)
	return e.result, e.ok
}

// refresh replaces old with a fresh result, leaving it in place on failure so
// the next request past expiry makes an ordinary foreground call.
func (c *headCache) refresh(key string, old *headEntry, fetch func() (json.RawMessage, bool)) {
	next := &headEntry{sent: time.Now(), done: make(chan struct{})}
	next.result, next.ok = fetch()
	close(next.done)
	c.mu.Lock()
	if next.ok && c.entries[key] == old {
		c.entries[key] = next
		if c.replaced == nil {
			c.replaced = map[string][]*headEntry{}
		}
		kept := append([]*headEntry{old}, c.replaced[key]...)
		if len(kept) > 4 {
			kept = kept[:4]
		}
		c.replaced[key] = kept
	}
	c.mu.Unlock()
}

// serveCached answers req from the head cache or the read-ahead store and
// reports whether it did. When it returns false req is unchanged apart from its
// body having been buffered.
func (u *upstream) serveCached(w http.ResponseWriter, req *http.Request, ch Chain) bool {
	if req.Method != http.MethodPost || req.ContentLength <= 0 || req.ContentLength > headRequestLimit || req.URL.RawQuery != "" || req.Header.Get("Content-Encoding") != "" {
		return false
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "request body unreadable", http.StatusBadRequest)
		return true
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	if ch.TipAhead {
		u.hold.n.Store(int32(ch.HoldBlocks))
	}
	q, ok := parseRPC(body)
	if !ok {
		reply, ok := u.headerBatch(req, ch, body)
		if ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(reply)
		}
		return ok
	}
	u.demand.note(q)
	result, ok := u.answer(req, ch, q)
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(q.ID) + `,"result":` + string(result) + "}\n"))
	return true
}

// answer returns the result for one call when the head cache, an early call or
// the hold has it, exactly as it would be given to a single request.
func (u *upstream) answer(req *http.Request, ch Chain, q rpcQuery) (result json.RawMessage, ok bool) {
	ttl, ahead := time.Duration(ch.HeadCacheMS)*time.Millisecond, ch.ReadAheadBlocks
	if ch.TipAhead {
		u.tip.use(u, req.URL.Path, ttl)
	}
	params, isHead := q.head()
	if held, isHeld := u.holdAnswer(ch, q.Method, params); isHead && isHeld {
		u.heads.hits.Add(1)
		return held, true
	} else if isHead {
		target, header := *req.URL, req.Header.Clone() // The call may outlive this request.
		return u.heads.get(req.Context(), req.URL.Path+" "+q.Method+params, ttl, func() (json.RawMessage, bool) {
			result, ok := u.fetchRPC(&target, header, q.Method, params, headResponseLimit)
			if ok && strings.HasPrefix(params, `["safe"`) {
				u.ahead.learnSafe(result)
			}
			if ok && ch.TipAhead && q.Method == "eth_getBlockByNumber" && params == `["latest",false]` {
				u.tipHead(target.Path, result, false)
			}
			return result, ok
		})
	} else if early, held := u.tipTry(ch, req, q); held {
		return early, true
	} else if early, held := u.callTry(ch, req, q); held {
		return early, true
	} else if height, full, isBlock := q.block(); isBlock && ahead > 0 {
		return u.readAhead(req, height, full, ahead)
	} else if suffix, isHeader := q.headerByHeight(); isHeader {
		if result, ok = u.holdByHeight(ch, q); ok {
			u.heads.hits.Add(1)
		} else if result, ok = u.heads.byNumber(req.URL.Path+" "+q.Method, suffix, q.first, ttl); !ok && ch.ReadAheadHeaders {
			result, ok = u.readAheadHeader(req, q, suffix, ahead)
		}
		return result, ok
	}
	return nil, false
}

// headerBatchMax bounds a header batch answered here; Bifrost sends two or three calls.
const headerBatchMax = 8

// headerBatch answers a JSON-RPC batch made only of header queries: a header
// by head tag, as the cache serves, or by height. Recent Bifrost releases read
// the tip header and the header of the block it is scanning in one batch, and
// reads them one at a time when a provider rejects batches, so each element is
// answered here as that single call would be: from the head cache, the hold or
// an early call when one of them has it, and otherwise by the router making
// that exact call. The reply holds one result per call, in the order sent.
//
// A batch with any other call, a repeated id, or an element the provider did
// not answer with a plain result is forwarded untouched and gets the
// provider's own reply to the whole batch.
func (u *upstream) headerBatch(req *http.Request, ch Chain, body []byte) ([]byte, bool) {
	if trimmed := bytes.TrimLeft(body, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, false
	}
	var raw []json.RawMessage
	if json.Unmarshal(body, &raw) != nil || len(raw) < 2 || len(raw) > headerBatchMax {
		return nil, false
	}
	calls, ids := make([]rpcQuery, len(raw)), map[string]bool{}
	for i, r := range raw {
		q, ok := parseRPC(r)
		_, isHead := q.head()
		_, isHeader := q.headerByHeight()
		if !ok || !isHead && !isHeader || q.Method == "eth_blockNumber" || ids[string(q.ID)] {
			return nil, false
		}
		calls[i], ids[string(q.ID)] = q, true
	}
	results, failed := make([]json.RawMessage, len(calls)), atomic.Bool{}
	target, header := *req.URL, req.Header.Clone()
	var wg sync.WaitGroup
	for i, q := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, ok := u.answer(req, ch, q)
			if _, isHead := q.head(); !ok && !isHead {
				result, ok = u.fetchRPC(&target, header, q.Method, string(q.raw), headResponseLimit)
			}
			if !ok {
				failed.Store(true)
			}
			results[i] = result
		}()
	}
	wg.Wait()
	if failed.Load() {
		return nil, false
	}
	reply := []byte{'['}
	for i, q := range calls {
		if i > 0 {
			reply = append(reply, ',')
		}
		reply = append(reply, `{"jsonrpc":"2.0","id":`+string(q.ID)+`,"result":`+string(results[i])+`}`...)
	}
	u.batches.Add(1)
	return append(reply, ']', '\n'), true
}

// fetchRPC makes the router's own provider call behind a cache entry. It is not
// tied to a caller's connection, because other callers may be waiting on it.
// Any reply other than a plain, non-null JSON-RPC result is reported as unusable.
func (u *upstream) fetchRPC(inURL *url.URL, inHeader http.Header, method, params string, limit int64) (json.RawMessage, bool) {
	body, ok := u.fetchRaw(inURL, inHeader, `{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`, limit)
	var reply struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if !ok || json.Unmarshal(body, &reply) != nil || len(reply.Result) == 0 || string(reply.Result) == "null" || len(reply.Error) != 0 && string(reply.Error) != "null" {
		return nil, false
	}
	return reply.Result, true
}

// fetchRaw posts body to the provider and returns a 200 reply of at most limit bytes.
func (u *upstream) fetchRaw(inURL *url.URL, inHeader http.Header, body string, limit int64) ([]byte, bool) {
	in := &http.Request{URL: inURL, Header: inHeader}
	ctx, cancel := context.WithTimeout(context.Background(), u.timeout)
	defer cancel()
	out, err := http.NewRequestWithContext(ctx, http.MethodPost, u.target.String(), strings.NewReader(body))
	if err != nil {
		return nil, false
	}
	rewriteUpstream(out, in, u.target, u.t, u.endpoint)
	out.Header.Set("Content-Type", "application/json")
	out.Header.Set("Accept", "application/json")
	out.Header.Del("Accept-Encoding") // Let the transport negotiate and decode compression.
	resp, err := u.transport.RoundTrip(out)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	reply, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(reply)) > limit {
		return nil, false
	}
	return reply, true
}
