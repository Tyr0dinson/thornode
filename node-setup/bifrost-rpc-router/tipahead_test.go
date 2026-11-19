package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// evmTip stands in for an EVM provider whose head the test can move, and whose
// chain the test can replace from a given height (fork changes every hash from
// forkFrom up).
type evmTip struct {
	*httptest.Server
	mu       sync.Mutex
	head     uint64
	fork     int
	forkFrom uint64
	seen     map[string]int
	missing  map[string]bool
	delay    time.Duration
}

func (p *evmTip) hash(n uint64) string {
	if p.fork != 0 && n >= p.forkFrom {
		return fmt.Sprintf("0x%062x%02x", n, p.fork)
	}
	return fmt.Sprintf("0x%064x", n)
}

func newEVMTip(t *testing.T, head uint64) *evmTip {
	p := &evmTip{head: head, seen: map[string]int{}, missing: map[string]bool{}}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		var q struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(b, &q)
		var params []any
		_ = json.Unmarshal(q.Params, &params)
		first := ""
		if len(params) > 0 {
			first, _ = params[0].(string)
		}
		key := q.Method + " " + string(q.Params)
		p.mu.Lock()
		p.seen[key]++
		head, null, delay := p.head, p.missing[key], p.delay
		number := head
		if n, ok := parseTag(first); ok {
			number = n
		}
		header := fmt.Sprintf(`"number":"0x%x","hash":%q,"parentHash":%q`, number, p.hash(number), p.hash(number-1))
		result := "null"
		switch {
		case null || number > head:
		case q.Method == "eth_blockNumber":
			result = fmt.Sprintf(`"0x%x"`, head)
		case q.Method == "eth_getBlockByNumber" && len(params) == 2 && params[1] == true:
			result = "{" + header + `,"transactions":[{"hash":"0xtx"}]}`
		case q.Method == "eth_getBlockByNumber":
			result = "{" + header + "}"
		case q.Method == "eth_getBlockReceipts":
			result = fmt.Sprintf(`[{"for":%q,"status":"0x1"}]`, first)
		}
		p.mu.Unlock()
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, q.ID, result)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *evmTip) count(method, params string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[method+" "+params]
}

func (p *evmTip) set(fn func()) {
	p.mu.Lock()
	fn()
	p.mu.Unlock()
}

func (p *evmTip) waitFor(t *testing.T, method, params string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for p.count(method, params) < want {
		if time.Now().After(deadline) {
			t.Fatalf("provider never saw %s %s", method, params)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond)
}

func tipRouter(t *testing.T, p *evmTip, ms int, tip bool) *httptest.Server {
	t.Helper()
	c, file := fixtureConfig(t, map[string]map[string]string{"base": {"normal": p.URL}})
	ch := c.Chains["base"]
	ch.HeadCacheMS, ch.TipAhead = ms, tip
	ch.Endpoints = map[string]Endpoint{"rpc": {Path: "/base"}}
	c.Chains["base"] = ch
	writeJSON(t, file, c)
	r, err := newRouter(file, true)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(r)
	t.Cleanup(func() { s.Close(); r.close() })
	return s
}

const latestQuery = `["latest",false]`

func full(n uint64) string { return fmt.Sprintf(`["0x%x",true]`, n) }

func TestTipAheadIsOffWithoutTheOption(t *testing.T) {
	p := newEVMTip(t, 100)
	s := tipRouter(t, p, 200, false)
	mustPost(t, s, rpcBody("1", "eth_getBlockByNumber", latestQuery))
	p.set(func() { p.head = 101 })
	time.Sleep(400 * time.Millisecond)
	if n := p.count("eth_getBlockByNumber", latestQuery); n != 1 {
		t.Fatalf("the router polled the head %d times with tip_ahead off", n)
	}
	for i := 0; i < 2; i++ {
		mustPost(t, s, rpcBody("2", "eth_getBlockByNumber", full(101)))
	}
	if n := p.count("eth_getBlockByNumber", full(101)); n != 2 {
		t.Fatalf("block requests reached the provider %d times, want both forwarded", n)
	}
}

func TestTipAheadFetchesTheNewHeadBlockBeforeTheScannerAsks(t *testing.T) {
	p := newEVMTip(t, 100)
	s := tipRouter(t, p, 200, true)
	// The scanner asks for receipts by hash, and learns the head.
	mustPost(t, s, rpcBody("1", "eth_getBlockReceipts", `["`+p.hash(99)+`"]`))
	mustPost(t, s, rpcBody("2", "eth_getBlockByNumber", latestQuery))
	time.Sleep(250 * time.Millisecond)
	if n := p.count("eth_getBlockByNumber", full(100)); n != 0 {
		t.Fatalf("the head the scanner already had was fetched early (%d calls)", n)
	}

	p.set(func() { p.head = 101 })
	hash101 := p.hash(101)
	p.waitFor(t, "eth_getBlockReceipts", `["`+hash101+`"]`, 1)
	p.waitFor(t, "eth_getBlockByNumber", full(101), 1)

	if _, got := result(t, mustPostReply(t, s, rpcBody("3", "eth_blockNumber", "[]"))); got != `"0x65"` {
		t.Fatalf("eth_blockNumber %s, want the router's own head", got)
	}
	id, block := result(t, mustPostReply(t, s, rpcBody("4", "eth_getBlockByNumber", full(101))))
	if id != "4" || !strings.Contains(block, hash101) || !strings.Contains(block, `"transactions"`) {
		t.Fatalf("block reply id %s: %s", id, block)
	}
	for i := 0; i < 2; i++ { // The scanner may ask again; it is still held.
		if _, got := result(t, mustPostReply(t, s, rpcBody("5", "eth_getBlockReceipts", `["`+hash101+`"]`))); got != `[{"for":"`+hash101+`","status":"0x1"}]` {
			t.Fatalf("receipts %s", got)
		}
	}
	if a, b, c := p.count("eth_getBlockByNumber", full(101)), p.count("eth_getBlockReceipts", `["`+hash101+`"]`), p.count("eth_blockNumber", "[]"); a != 1 || b != 1 || c != 0 {
		t.Fatalf("provider calls: block %d, receipts %d, eth_blockNumber %d; want 1, 1, 0", a, b, c)
	}
	// By-number receipts were never the scanner's form here: not fetched.
	if n := p.count("eth_getBlockReceipts", `["0x65"]`); n != 0 {
		t.Fatalf("receipts fetched in a form the scanner did not use (%d)", n)
	}
}

func TestTipAheadFetchesReceiptsOnlyInTheFormTheScannerUses(t *testing.T) {
	p := newEVMTip(t, 100)
	s := tipRouter(t, p, 200, true)
	mustPost(t, s, rpcBody("1", "eth_getBlockByNumber", latestQuery))
	p.set(func() { p.head = 101 })
	p.waitFor(t, "eth_getBlockByNumber", full(101), 1)
	if a, b := p.count("eth_getBlockReceipts", `["`+p.hash(101)+`"]`), p.count("eth_getBlockReceipts", `["0x65"]`); a != 0 || b != 0 {
		t.Fatalf("receipts fetched before the scanner ever asked for any: %d by hash, %d by number", a, b)
	}
	mustPost(t, s, rpcBody("2", "eth_getBlockReceipts", `["0x65"]`)) // By number, as on BASE.
	p.set(func() { p.head = 102 })
	p.waitFor(t, "eth_getBlockReceipts", `["0x66"]`, 1)
	if _, got := result(t, mustPostReply(t, s, rpcBody("3", "eth_getBlockReceipts", `["0x66"]`))); !strings.Contains(got, `"0x66"`) {
		t.Fatalf("receipts %s", got)
	}
	if a, b := p.count("eth_getBlockReceipts", `["0x66"]`), p.count("eth_getBlockReceipts", `["`+p.hash(102)+`"]`); a != 1 || b != 0 {
		t.Fatalf("provider receipts calls: by number %d, by hash %d; want 1, 0", a, b)
	}
}

func TestTipAheadDropsABlockThatWasReplaced(t *testing.T) {
	p := newEVMTip(t, 100)
	s := tipRouter(t, p, 200, true)
	mustPost(t, s, rpcBody("1", "eth_getBlockByNumber", latestQuery))
	p.set(func() { p.head = 101 })
	p.waitFor(t, "eth_getBlockByNumber", full(101), 1)
	old := p.hash(101)

	// Same height, different block: the next poll sees another hash at 101.
	p.set(func() { p.fork, p.forkFrom = 1, 101 })
	time.Sleep(300 * time.Millisecond)
	_, block := result(t, mustPostReply(t, s, rpcBody("2", "eth_getBlockByNumber", full(101))))
	if strings.Contains(block, old) || !strings.Contains(block, p.hash(101)) {
		t.Fatalf("the replaced block was handed out: %s", block)
	}
	if n := p.count("eth_getBlockByNumber", full(101)); n != 2 {
		t.Fatalf("provider saw the block request %d times, want the early call and the forwarded one", n)
	}

	// A new head whose parent is not the held block: the held block goes too.
	p.set(func() { p.head = 102 })
	p.waitFor(t, "eth_getBlockByNumber", full(102), 1)
	held := p.hash(102)
	p.set(func() { p.fork, p.forkFrom, p.head = 2, 102, 103 })
	p.waitFor(t, "eth_getBlockByNumber", full(103), 1)
	_, block = result(t, mustPostReply(t, s, rpcBody("3", "eth_getBlockByNumber", full(102))))
	if strings.Contains(block, held) {
		t.Fatalf("a block that is no longer the head's parent was handed out: %s", block)
	}
}

func TestTipAheadNeverHoldsABlockThatIsNotTheHeadItSaw(t *testing.T) {
	p := newEVMTip(t, 100)
	s := tipRouter(t, p, 200, true)
	mustPost(t, s, rpcBody("1", "eth_getBlockByNumber", latestQuery))
	// The provider answers the head with one block and the full-block call,
	// 150 ms later, with its replacement.
	p.set(func() { p.head, p.delay = 101, 150*time.Millisecond })
	p.waitFor(t, "eth_getBlockByNumber", latestQuery, 2)
	p.set(func() { p.fork, p.forkFrom, p.delay = 1, 101, 0 })
	p.waitFor(t, "eth_getBlockByNumber", full(101), 1)
	time.Sleep(200 * time.Millisecond)
	before := p.count("eth_getBlockByNumber", full(101))
	mustPost(t, s, rpcBody("2", "eth_getBlockByNumber", full(101)))
	if n := p.count("eth_getBlockByNumber", full(101)); n != before+1 {
		t.Fatalf("a block fetched under a different hash than the head was held (%d then %d provider calls)", before, n)
	}
}

func TestTipAheadNeverHandsOutAFailedEarlyCallAndExpires(t *testing.T) {
	keep, idle := tipKeepNS.Load(), tipIdleNS.Load()
	tipKeepNS.Store(int64(250 * time.Millisecond))
	tipIdleNS.Store(int64(500 * time.Millisecond))
	t.Cleanup(func() { tipKeepNS.Store(keep); tipIdleNS.Store(idle) })
	p := newEVMTip(t, 100)
	p.missing["eth_getBlockByNumber "+full(101)] = true
	s := tipRouter(t, p, 200, true)
	mustPost(t, s, rpcBody("1", "eth_getBlockByNumber", latestQuery))
	p.set(func() { p.head = 101 })
	p.waitFor(t, "eth_getBlockByNumber", full(101), 1)
	if got := mustPostReply(t, s, rpcBody("2", "eth_getBlockByNumber", full(101))); !strings.Contains(got, `"result":null`) || p.count("eth_getBlockByNumber", full(101)) != 2 {
		t.Fatalf("a failed early call must be forwarded, got %s", got)
	}

	p.set(func() { p.head = 102 })
	p.waitFor(t, "eth_getBlockByNumber", full(102), 1)
	time.Sleep(300 * time.Millisecond) // past tipKeep
	mustPost(t, s, rpcBody("3", "eth_getBlockByNumber", full(102)))
	if n := p.count("eth_getBlockByNumber", full(102)); n != 2 {
		t.Fatalf("an expired early block was handed out (%d provider calls)", n)
	}

	time.Sleep(800 * time.Millisecond) // past tipIdle with no scanner calls
	quiet := p.count("eth_getBlockByNumber", latestQuery)
	time.Sleep(400 * time.Millisecond)
	if n := p.count("eth_getBlockByNumber", latestQuery); n != quiet {
		t.Fatalf("the router kept polling an unused chain: %d then %d", quiet, n)
	}
}

func TestTipAheadConfig(t *testing.T) {
	p := newEVMTip(t, 100)
	for _, tc := range []struct {
		head int
		tip  bool
		ok   bool
	}{{0, false, true}, {1000, true, true}, {0, true, false}} {
		c, file := fixtureConfig(t, map[string]map[string]string{"base": {"normal": p.URL}})
		ch := c.Chains["base"]
		ch.HeadCacheMS, ch.TipAhead = tc.head, tc.tip
		c.Chains["base"] = ch
		writeJSON(t, file, c)
		r, err := newRouter(file, true)
		if (err == nil) != tc.ok {
			t.Errorf("head_cache_ms=%d tip_ahead=%v: err=%v, want ok=%v", tc.head, tc.tip, err, tc.ok)
		}
		if r != nil {
			r.close()
		}
	}
	on, off := Config{Chains: map[string]Chain{"eth": {TipAhead: true}}}, Config{Chains: map[string]Chain{"eth": {}}}
	if !compatible(off, on) || !compatible(on, off) {
		t.Error("tip_ahead must be reloadable")
	}
}
