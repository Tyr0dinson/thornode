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

// UTXO fetch-ahead (opt-in per chain with utxo_ahead_ms; adapter layer only).
//
// Bifrost's UTXO scanners (BTC, LTC, BCH) speak bitcoind JSON-RPC and work one
// provider round trip at a time. Against a remote provider a new block costs:
// getblockhash, getblock at verbosity 2, the
// previous transaction of every transaction's first input in batches of 500,
// single lookups for every input of the vault transactions, and then
// getblockcount / getblockhash pairs while it checks the block is
// still canonical. A node with a local daemon does all of that in milliseconds
// and votes first.
//
// With this on, the router does that work before Bifrost asks:
//
//   - It polls getblockcount itself, twice per utxo_ahead_ms, for as long as
//     Bifrost has used the chain in the last utxoIdle. Bifrost's own
//     getblockcount and getblockhash [height] calls are answered from the
//     router's copy, which is never older than utxo_ahead_ms.
//   - When the tip moves it fetches the new block's hash, the block at
//     verbosity 2, and getrawtransaction [txid, true] for the first input of
//     every transaction, plus every input of transactions that carry an
//     OP_RETURN output (THORChain transactions do). It keeps those for
//     utxoKeep and answers Bifrost's getblock [hash, 2] and
//     getrawtransaction [txid, true] calls, single or batched, from them. It
//     also fetches getblockstats [hash] and getnetworkinfo, which Bifrost asks
//     for once per block for its fee report.
//
// Rules that keep this safe:
//   - Every answer is the provider's own result for that exact call. Blocks are
//     keyed by hash and transactions by txid, so a reorg cannot make one stand
//     in for another; a transaction lookup can be up to utxoKeep old, which
//     matters only for its confirmation count, and the scan path does not read
//     that from these lookups.
//   - A failed, null or oversized early call is never handed out. Bifrost's
//     request is forwarded and gets the provider's own reply or error.
//   - A batch is answered here only when every element is a plain call with a
//     distinct id and at least one element is held. Elements not held are sent
//     to the provider as one smaller batch under their original ids, and the
//     reply is assembled in request order. If that provider call fails in any
//     way the whole original batch is forwarded untouched.
//   - Everything else (wallet calls, broadcasts, mempool calls, any other
//     verbosity) is forwarded untouched.
//   - Daemons differ: Zebra rejects getrawtransaction [txid, true] (it wants 1)
//     and has no getblockstats, and Bifrost's Zcash client makes no lookup
//     batches. So the router only makes an early call in a form it has seen
//     Bifrost use on this backend since it started: lookups once Bifrost has
//     sent a lookup batch, in that batch's verbosity form; getblockstats and
//     getnetworkinfo once Bifrost has called them. The tip, block hash and
//     block calls are common to all of them and need no learning.
//
// Replies built here use bitcoind's own shape: {"result":…,"error":null,"id":…}.

const (
	utxoRequestLimit = 512 << 10 // A 500-element lookup batch is about 55 KiB.
	utxoBatch        = 500       // Lookups per early provider batch (Bifrost's own lookup batch size).
	utxoWorkers      = 4         // Early provider batches in flight per block.
	utxoMaxLookups   = 8000      // Per block; the rest are left to Bifrost.
	utxoMaxEntries   = 40000     // Hard bound on held lookups.
	utxoMaxInputs    = 32        // Inputs fetched for one OP_RETURN transaction.
	utxoMaxOutputs   = 12        // Bifrost ignores transactions with more outputs.
	utxoTxLimit      = 2 << 20   // One early lookup batch reply.
)

// Atomic so tests can shorten them while an earlier test's poller winds down.
var (
	utxoKeepNS atomic.Int64 // How long an early block or lookup is answerable.
	utxoIdleNS atomic.Int64 // Polling stops this long after Bifrost's last call.
)

func init() {
	utxoKeepNS.Store(int64(20 * time.Second))
	utxoIdleNS.Store(int64(60 * time.Second))
}

func utxoKeep() time.Duration { return time.Duration(utxoKeepNS.Load()) }
func utxoIdle() time.Duration { return time.Duration(utxoIdleNS.Load()) }

type utxoAhead struct {
	mu      sync.Mutex
	entries map[string]*aheadEntry // "block:<hash>" and "tx:<txid>"
	tip     int64                  // Highest getblockcount seen; 0 before the first.
	polling bool
	path    string

	interval atomic.Int64 // utxo_ahead_ms in nanoseconds as last requested; 0 stops the poller.
	// What Bifrost has been seen to use on this backend (see the rules above).
	form    atomic.Int32 // Verbosity of its lookup batches: 0 none seen, formTrue or formOne.
	stats   atomic.Bool
	netinfo atomic.Bool
	lastUse atomic.Int64 // Unix nanoseconds of Bifrost's last call.
}

type utxoCall struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	raw    json.RawMessage // The element as sent.
}

// parseUTXOCall accepts one plain call with a scalar id.
func parseUTXOCall(raw json.RawMessage) (c utxoCall, ok bool) {
	var in struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if strictJSON(raw, &in) != nil || in.Method == "" || len(in.ID) == 0 || !(in.ID[0] == '"' || in.ID[0] == '-' || in.ID[0] >= '0' && in.ID[0] <= '9') {
		return c, false
	}
	if in.JSONRPC != "" && in.JSONRPC != "1.0" && in.JSONRPC != "2.0" {
		return c, false
	}
	return utxoCall{ID: in.ID, Method: in.Method, Params: in.Params, raw: raw}, true
}

func (c utxoCall) params() []any {
	var p []any
	if len(c.Params) == 0 || json.Unmarshal(c.Params, &p) != nil {
		return nil
	}
	return p
}

func hexID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}

const (
	formTrue int32 = 1 // getrawtransaction [txid, true]
	formOne  int32 = 2 // getrawtransaction [txid, 1]
)

// lookup reports the held-entry key and verbosity form of a verbose
// getrawtransaction call. The two forms are kept apart: an answer is only ever
// given to the form it was fetched with.
func (c utxoCall) lookup() (key string, form int32, ok bool) {
	p := c.params()
	if c.Method != "getrawtransaction" || len(p) != 2 {
		return "", 0, false
	}
	switch p[1] {
	case true:
		form = formTrue
	case float64(1):
		form = formOne
	default:
		return "", 0, false
	}
	txid, _ := p[0].(string)
	return lookupKey(txid, form), form, hexID(txid)
}

func lookupKey(txid string, form int32) string {
	if form == formOne {
		return "tx1:" + txid
	}
	return "tx:" + txid
}

// block reports the hash of a getblock [hash, 2] call.
func (c utxoCall) block() (hash string, ok bool) {
	p := c.params()
	if c.Method != "getblock" || len(p) != 2 || p[1] != float64(2) {
		return "", false
	}
	hash, _ = p[0].(string)
	return hash, hexID(hash)
}

// extra reports the held-entry key of the two per-block fee calls.
func (c utxoCall) extra() (key string, ok bool) {
	p := c.params()
	switch {
	case c.Method == "getnetworkinfo" && len(p) == 0:
		return "netinfo", true
	case c.Method == "getblockstats" && len(p) == 1:
		hash, _ := p[0].(string)
		return "stats:" + hash, hexID(hash)
	}
	return "", false
}

// height reports the height of a getblockhash [height] call.
func (c utxoCall) height() (h int64, ok bool) {
	p := c.params()
	if c.Method != "getblockhash" || len(p) != 1 {
		return 0, false
	}
	f, isNumber := p[0].(float64)
	h = int64(f)
	return h, isNumber && h >= 0 && float64(h) == f && string(bytes.TrimSpace(c.Params)) == "["+strconv.FormatInt(h, 10)+"]"
}

func (c utxoCall) count() bool {
	if c.Method != "getblockcount" {
		return false
	}
	p := string(bytes.TrimSpace(c.Params))
	return p == "" || p == "null" || p == "[]"
}

func utxoReply(id, result json.RawMessage) []byte {
	return []byte(`{"result":` + string(result) + `,"error":null,"id":` + string(id) + `}`)
}

const (
	utxoCountKey = "utxo getblockcount"
	utxoHashKey  = "utxo getblockhash "
)

// put installs a result the caller has just fetched, as if get had fetched it.
func (c *headCache) put(key string, result json.RawMessage) {
	e := &headEntry{sent: time.Now(), done: make(chan struct{}), result: result, ok: true}
	close(e.done)
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]*headEntry{}
	}
	// Keep the entry this replaces answerable by number, as refresh does.
	if old := c.entries[key]; old != nil {
		select {
		case <-old.done:
			if old.ok {
				if c.replaced == nil {
					c.replaced = map[string][]*headEntry{}
				}
				kept := append([]*headEntry{old}, c.replaced[key]...)
				if len(kept) > 4 {
					kept = kept[:4]
				}
				c.replaced[key] = kept
			}
		default:
		}
	}
	c.entries[key] = e
	c.mu.Unlock()
}

// serveUTXO answers req from the router's own copy of the chain tip or from an
// early call, and reports whether it did. When it returns false req is
// unchanged apart from its body having been buffered.
func (u *upstream) serveUTXO(w http.ResponseWriter, req *http.Request, ch Chain) bool {
	ttl := time.Duration(ch.UTXOAheadMS) * time.Millisecond
	if req.Method != http.MethodPost || req.ContentLength <= 0 || req.ContentLength > utxoRequestLimit || req.URL.RawQuery != "" || req.Header.Get("Content-Encoding") != "" || req.URL.Path != u.endpoint.Path {
		return false
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "request body unreadable", http.StatusBadRequest)
		return true
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	u.utxo.use(u, req.URL.Path, ttl)

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		reply, ok := u.utxoBatch(req, trimmed)
		if !ok {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(reply)
		return true
	}
	c, ok := parseUTXOCall(trimmed)
	if !ok {
		return false
	}
	var result json.RawMessage
	switch {
	case c.count():
		result, ok = u.heads.get(req.Context(), utxoCountKey, ttl, func() (json.RawMessage, bool) { return u.utxoCount(req.URL.Path) })
	default:
		if h, isHash := c.height(); isHash {
			result, ok = u.utxoHash(req.Context(), req.URL.Path, h, ttl)
		} else if hash, isBlock := c.block(); isBlock {
			result, ok = u.utxo.held(req.Context(), "block:"+hash)
		} else if key, _, isLookup := c.lookup(); isLookup {
			result, ok = u.utxo.held(req.Context(), key)
		} else if key, isExtra := c.extra(); isExtra {
			if key == "netinfo" {
				u.utxo.netinfo.Store(true)
			} else {
				u.utxo.stats.Store(true)
			}
			result, ok = u.utxo.take(req.Context(), key)
		} else {
			return false
		}
		if ok && c.Method != "getblockhash" {
			u.ahead.hits.Add(1)
		}
	}
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(append(utxoReply(c.ID, result), '\n'))
	return true
}

// use records Bifrost's call and makes sure the tip poller is running.
func (a *utxoAhead) use(u *upstream, path string, ttl time.Duration) {
	a.lastUse.Store(time.Now().UnixNano())
	a.interval.Store(int64(ttl))
	a.mu.Lock()
	start := !a.polling
	a.polling, a.path = true, path
	a.mu.Unlock()
	if start {
		go u.utxoPoll()
	}
}

// utxoPoll keeps the router's tip current while Bifrost is using the chain.
func (u *upstream) utxoPoll() {
	for {
		ttl := time.Duration(u.utxo.interval.Load())
		u.utxo.mu.Lock()
		path := u.utxo.path
		if ttl <= 0 || time.Since(time.Unix(0, u.utxo.lastUse.Load())) > utxoIdle() {
			u.utxo.polling = false
			u.utxo.mu.Unlock()
			return
		}
		u.utxo.mu.Unlock()
		time.Sleep(ttl / 2)
		if result, ok := u.utxoCount(path); ok {
			u.heads.put(utxoCountKey, result)
		}
	}
}

// utxoCount asks the provider for the tip and starts the early calls for any
// block it has not seen.
func (u *upstream) utxoCount(path string) (json.RawMessage, bool) {
	result, ok := u.fetchRPC(&url.URL{Path: path}, nil, "getblockcount", "[]", headResponseLimit)
	if !ok {
		return nil, false
	}
	n, err := strconv.ParseInt(string(result), 10, 64)
	if err != nil || n <= 0 {
		return nil, false
	}
	u.utxo.mu.Lock()
	old := u.utxo.tip
	if n > old {
		u.utxo.tip = n
	}
	u.utxo.mu.Unlock()
	// The first tip seen is where Bifrost already is. After that, fetch each new
	// block, at most the last three if several arrived at once.
	if old > 0 {
		for h := max(old+1, n-2); h <= n; h++ {
			go u.utxoFetchBlock(path, h)
		}
	}
	return result, true
}

// utxoHash answers getblockhash [height] from one provider call per ttl.
func (u *upstream) utxoHash(ctx context.Context, path string, height int64, ttl time.Duration) (json.RawMessage, bool) {
	h := strconv.FormatInt(height, 10)
	return u.heads.get(ctx, utxoHashKey+h, ttl, func() (json.RawMessage, bool) {
		return u.fetchRPC(&url.URL{Path: path}, nil, "getblockhash", "["+h+"]", headResponseLimit)
	})
}

// held hands over an early answer, waiting for it if the call is in flight.
func (a *utxoAhead) held(ctx context.Context, key string) (json.RawMessage, bool) {
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
	if !e.ok || time.Since(e.at) > utxoKeep() {
		return nil, false
	}
	return e.result, true
}

// take is held for answers that are good once: the entry is dropped as it is
// handed over, so a second identical call reaches the provider.
func (a *utxoAhead) take(ctx context.Context, key string) (json.RawMessage, bool) {
	result, ok := a.held(ctx, key)
	a.mu.Lock()
	if e := a.entries[key]; e != nil {
		select {
		case <-e.done:
			delete(a.entries, key)
		default:
		}
	}
	a.mu.Unlock()
	return result, ok
}

// reserve registers in-flight entries for keys not already held or due and
// returns the ones it created.
func (a *utxoAhead) reserve(keys []string) map[string]*aheadEntry {
	created := map[string]*aheadEntry{}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.entries == nil {
		a.entries = map[string]*aheadEntry{}
	}
	for k, e := range a.entries {
		select {
		case <-e.done:
			if time.Since(e.at) > utxoKeep() {
				delete(a.entries, k)
			}
		default:
		}
	}
	for _, k := range keys {
		if a.entries[k] != nil || len(a.entries) >= utxoMaxEntries {
			continue
		}
		e := &aheadEntry{done: make(chan struct{})}
		a.entries[k], created[k] = e, e
	}
	return created
}

func finish(e *aheadEntry, result json.RawMessage, ok bool) {
	e.result, e.ok, e.at = result, ok, time.Now()
	close(e.done)
}

// utxoFetchBlock makes the early calls for the block at height.
func (u *upstream) utxoFetchBlock(path string, height int64) {
	ctx, cancel := context.WithTimeout(context.Background(), u.timeout)
	defer cancel()
	raw, ok := u.utxoHash(ctx, path, height, time.Duration(u.utxo.interval.Load()))
	var hash string
	if !ok || json.Unmarshal(raw, &hash) != nil || !hexID(hash) {
		return
	}
	want := []string{"block:" + hash}
	if u.utxo.stats.Load() {
		want = append(want, "stats:"+hash)
	}
	if u.utxo.netinfo.Load() {
		want = append(want, "netinfo")
	}
	first := u.utxo.reserve(want)
	entry := first["block:"+hash]
	if entry == nil {
		for _, e := range first {
			finish(e, nil, false)
		}
		return // Already fetched or being fetched.
	}
	for key, call := range map[string][2]string{"stats:" + hash: {"getblockstats", `["` + hash + `"]`}, "netinfo": {"getnetworkinfo", "[]"}} {
		if e := first[key]; e != nil {
			go func() {
				u.ahead.fetches.Add(1)
				result, ok := u.fetchRPC(&url.URL{Path: path}, nil, call[0], call[1], headResponseLimit)
				finish(e, result, ok)
			}()
		}
	}
	u.ahead.fetches.Add(1)
	block, ok := u.fetchRPC(&url.URL{Path: path}, nil, "getblock", `["`+hash+`",2]`, blockLimit)
	finish(entry, block, ok)
	if !ok {
		return
	}
	var parsed struct {
		Tx []struct {
			Vin []struct {
				Txid string `json:"txid"`
			} `json:"vin"`
			Vout []struct {
				ScriptPubKey struct {
					Type string `json:"type"`
				} `json:"scriptPubKey"`
			} `json:"vout"`
		} `json:"tx"`
	}
	if json.Unmarshal(block, &parsed) != nil {
		return
	}
	form := u.utxo.form.Load()
	if form == 0 {
		return // Bifrost has sent no lookup batch here: it does not make these calls.
	}
	verbose := "true"
	if form == formOne {
		verbose = "1"
	}
	seen := map[string]bool{}
	var keys []string
	txids := map[string]string{}
	add := func(txid string) {
		if hexID(txid) && !seen[txid] && len(keys) < utxoMaxLookups {
			seen[txid] = true
			keys = append(keys, lookupKey(txid, form))
			txids[lookupKey(txid, form)] = txid
		}
	}
	// First inputs first: Bifrost asks for those for nearly every transaction.
	for _, tx := range parsed.Tx {
		if len(tx.Vin) > 0 {
			add(tx.Vin[0].Txid)
		}
	}
	for _, tx := range parsed.Tx {
		if len(tx.Vin) > utxoMaxInputs || len(tx.Vout) > utxoMaxOutputs {
			continue
		}
		for _, out := range tx.Vout {
			if strings.EqualFold(out.ScriptPubKey.Type, "nulldata") {
				for _, in := range tx.Vin {
					add(in.Txid)
				}
				break
			}
		}
	}
	created := u.utxo.reserve(keys)
	var todo []string
	for _, k := range keys {
		if created[k] != nil {
			todo = append(todo, k)
		}
	}
	work := make(chan []string)
	var wg sync.WaitGroup
	for i := 0; i < utxoWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for part := range work {
				calls := make([]utxoCall, len(part))
				for i, k := range part {
					calls[i] = utxoCall{ID: json.RawMessage(strconv.Itoa(i)), raw: json.RawMessage(`{"jsonrpc":"2.0","id":` + strconv.Itoa(i) + `,"method":"getrawtransaction","params":["` + txids[k] + `",` + verbose + `]}`)}
				}
				u.ahead.fetches.Add(1)
				replies, _ := u.fetchBatch(path, calls)
				for i, k := range part {
					var r struct {
						Result json.RawMessage `json:"result"`
						Error  json.RawMessage `json:"error"`
					}
					reply := replies[strconv.Itoa(i)]
					usable := reply != nil && json.Unmarshal(reply, &r) == nil && len(r.Result) != 0 && string(r.Result) != "null" && (len(r.Error) == 0 || string(r.Error) == "null")
					finish(created[k], r.Result, usable)
				}
			}
		}()
	}
	for len(todo) > 0 {
		n := min(utxoBatch, len(todo))
		work <- todo[:n]
		todo = todo[n:]
	}
	close(work)
	wg.Wait()
}

// fetchBatch sends calls to the provider as one batch and returns each reply
// element by id. ok is false unless there is exactly one element per call.
func (u *upstream) fetchBatch(path string, calls []utxoCall) (map[string]json.RawMessage, bool) {
	var body bytes.Buffer
	body.WriteByte('[')
	for i, c := range calls {
		if i > 0 {
			body.WriteByte(',')
		}
		body.Write(c.raw)
	}
	body.WriteByte(']')
	raw, ok := u.fetchRaw(&url.URL{Path: path}, nil, body.String(), utxoTxLimit+int64(len(calls))*(64<<10))
	out := map[string]json.RawMessage{}
	var elems []json.RawMessage
	if !ok || json.Unmarshal(raw, &elems) != nil || len(elems) != len(calls) {
		return out, false
	}
	want := map[string]bool{}
	for _, c := range calls {
		want[string(c.ID)] = true
	}
	for _, e := range elems {
		var r struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(e, &r) != nil || !want[string(r.ID)] || out[string(r.ID)] != nil {
			return map[string]json.RawMessage{}, false
		}
		out[string(r.ID)] = e
	}
	return out, true
}

// utxoBatch answers a batch when at least one of its lookups is held.
func (u *upstream) utxoBatch(req *http.Request, body []byte) ([]byte, bool) {
	var elems []json.RawMessage
	if json.Unmarshal(body, &elems) != nil || len(elems) == 0 {
		return nil, false
	}
	calls := make([]utxoCall, len(elems))
	ids := map[string]bool{}
	forms := map[int32]bool{}
	held := false
	u.utxo.mu.Lock()
	for i, e := range elems {
		c, ok := parseUTXOCall(e)
		if !ok || ids[string(c.ID)] {
			u.utxo.mu.Unlock()
			return nil, false
		}
		ids[string(c.ID)] = true
		calls[i] = c
		if key, form, isLookup := c.lookup(); isLookup {
			forms[form] = true
			if u.utxo.entries[key] != nil {
				held = true
			}
		}
	}
	u.utxo.mu.Unlock()
	// A batch of lookups in one form is how Bifrost's BTC-family clients ask for
	// previous transactions: from now on those are fetched early, in that form.
	if len(forms) == 1 {
		for form := range forms {
			u.utxo.form.Store(form)
		}
	}
	if !held {
		return nil, false
	}
	parts := make([][]byte, len(calls))
	var missing []utxoCall
	for i, c := range calls {
		if key, _, isLookup := c.lookup(); isLookup {
			if result, ok := u.utxo.held(req.Context(), key); ok {
				parts[i] = utxoReply(c.ID, result)
				continue
			}
		}
		missing = append(missing, c)
	}
	if len(missing) > 0 {
		replies, ok := u.fetchBatch(req.URL.Path, missing)
		if !ok {
			return nil, false
		}
		for i, c := range calls {
			if parts[i] == nil {
				parts[i] = replies[string(c.ID)]
			}
		}
	}
	u.ahead.hits.Add(uint64(len(calls) - len(missing)))
	return append(append([]byte{'['}, bytes.Join(parts, []byte{','})...), ']', '\n'), true
}
