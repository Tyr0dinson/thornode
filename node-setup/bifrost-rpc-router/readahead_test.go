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

// chainProvider answers block, receipt and head queries with height-specific
// content and records every call, so a test can tell who asked for what.
type chainProvider struct {
	*httptest.Server
	mu      sync.Mutex
	seen    map[string]int // "method params" -> provider calls
	safe    uint64
	missing map[string]bool // "method params" answered with a null result
}

func newChainProvider(t *testing.T, safe uint64) *chainProvider {
	p := &chainProvider{seen: map[string]int{}, safe: safe, missing: map[string]bool{}}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		var q struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(b, &q)
		key := q.Method + " " + string(q.Params)
		p.mu.Lock()
		p.seen[key]++
		null := p.missing[key]
		safe := p.safe
		p.mu.Unlock()
		var params []any
		_ = json.Unmarshal(q.Params, &params)
		first := ""
		if len(params) > 0 {
			first, _ = params[0].(string)
		}
		w.Header().Set("Content-Type", "application/json")
		result := "null"
		switch {
		case null:
		case q.Method == "eth_getBlockByNumber" && first == "safe":
			result = fmt.Sprintf(`{"number":"0x%x","hash":"0xsafe"}`, safe)
		case q.Method == "eth_getBlockByNumber" && len(params) == 2 && params[1] == true:
			result = fmt.Sprintf(`{"number":%q,"hash":"0xfull%s","transactions":[{"hash":"0xtx%s"}]}`, first, first[2:], first[2:])
		case q.Method == "eth_getBlockByNumber":
			result = fmt.Sprintf(`{"number":%q,"hash":"0xhdr%s"}`, first, first[2:])
		case q.Method == "eth_getBlockReceipts":
			result = fmt.Sprintf(`[{"blockNumber":%q,"status":"0x1"}]`, first)
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, q.ID, result)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *chainProvider) count(method, params string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[method+" "+params]
}

func (p *chainProvider) total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, v := range p.seen {
		n += v
	}
	return n
}

// waitFor polls until the provider has seen want calls for a query.
func (p *chainProvider) waitFor(t *testing.T, method, params string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for p.count(method, params) < want {
		if time.Now().After(deadline) {
			t.Fatalf("provider never saw %s %s (%d of %d)", method, params, p.count(method, params), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the reply land in the router
}

func readAheadRouter(t *testing.T, p *chainProvider, blocks int) (*router, *httptest.Server) {
	return readAheadRouterWith(t, p, blocks, false)
}

func readAheadRouterWith(t *testing.T, p *chainProvider, blocks int, headers bool) (*router, *httptest.Server) {
	t.Helper()
	c, file := fixtureConfig(t, map[string]map[string]string{"base": {"normal": p.URL}})
	ch := c.Chains["base"]
	ch.HeadCacheMS, ch.ReadAheadBlocks, ch.ReadAheadHeaders = 1000, blocks, headers
	ch.Endpoints = map[string]Endpoint{"rpc": {Path: "/base"}}
	c.Chains["base"] = ch
	writeJSON(t, file, c)
	r, err := newRouter(file, true)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(r)
	t.Cleanup(func() { s.Close(); r.close() })
	return r, s
}

const safeQuery = `["safe",false]`

// The scanner asks for a block, then its receipts, then the next block. With
// read-ahead on, the receipts and the next blocks are already at the router
// when asked for, and the provider is asked for each exactly once.
func TestReadAheadServesReceiptsAndNextBlocksFromOneProviderCallEach(t *testing.T) {
	p := newChainProvider(t, 0x64)
	r, s := readAheadRouter(t, p, 2)
	mustPost(t, s, rpcBody("1", "eth_getBlockByNumber", safeQuery)) // the scanner's own safe-head query teaches the router the limit

	_, res := result(t, mustPostReply(t, s, rpcBody("2", "eth_getBlockByNumber", `["0x10",true]`)))
	if !strings.Contains(res, `"hash":"0xfull10"`) {
		t.Fatalf("first block: %s", res)
	}
	p.waitFor(t, "eth_getBlockReceipts", `["0x10"]`, 1)
	p.waitFor(t, "eth_getBlockByNumber", `["0x12",true]`, 1)
	p.waitFor(t, "eth_getBlockReceipts", `["0x12"]`, 1)
	before := p.total()

	id, res := result(t, mustPostReply(t, s, rpcBody(`"r"`, "eth_getBlockReceipts", `["0x10"]`)))
	if id != `"r"` || res != `[{"blockNumber":"0x10","status":"0x1"}]` {
		t.Fatalf("receipts: id=%s %s", id, res)
	}
	id, res = result(t, mustPostReply(t, s, rpcBody("77", "eth_getBlockByNumber", `["0x11",true]`)))
	if id != "77" || !strings.Contains(res, `"hash":"0xfull11"`) || !strings.Contains(res, `"0xtx11"`) {
		t.Fatalf("next block: id=%s %s", id, res)
	}
	_, res = result(t, mustPostReply(t, s, rpcBody("78", "eth_getBlockReceipts", `["0x11"]`)))
	if res != `[{"blockNumber":"0x11","status":"0x1"}]` {
		t.Fatalf("next receipts: %s", res)
	}
	// Serving block 0x11 extends the window by one block (0x13); nothing is refetched.
	p.waitFor(t, "eth_getBlockReceipts", `["0x13"]`, 1)
	if n := p.total() - before; n != 2 {
		t.Fatalf("three scanner requests caused %d provider calls, want 2 (block and receipts 0x13)", n)
	}
	for _, q := range [][2]string{{"eth_getBlockByNumber", `["0x11",true]`}, {"eth_getBlockByNumber", `["0x12",true]`}, {"eth_getBlockReceipts", `["0x10"]`}, {"eth_getBlockReceipts", `["0x11"]`}} {
		if n := p.count(q[0], q[1]); n != 1 {
			t.Fatalf("provider saw %s %s %d times", q[0], q[1], n)
		}
	}
	if n := metricSum(t, r, "rpc_router_read_ahead_hits_total", `chain="base"`); n != 3 {
		t.Fatalf("read-ahead hits metric = %v, want 3", n)
	}
	// An answer is handed out once: a repeat goes to the provider.
	mustPost(t, s, rpcBody("79", "eth_getBlockReceipts", `["0x10"]`))
	if n := p.count("eth_getBlockReceipts", `["0x10"]`); n != 2 {
		t.Fatalf("repeat receipts request: provider saw %d calls, want 2", n)
	}
}

// Blocks above the provider's safe head can still change, so the router neither
// fetches them early nor answers for them.
func TestReadAheadNeverReachesPastTheSafeHead(t *testing.T) {
	p := newChainProvider(t, 0x64)
	_, s := readAheadRouter(t, p, 4)

	// Before the safe head is known nothing is fetched early.
	mustPost(t, s, rpcBody("1", "eth_getBlockByNumber", `["0x10",true]`))
	time.Sleep(100 * time.Millisecond)
	if n := p.total(); n != 1 {
		t.Fatalf("read-ahead ran without a known safe head: %d provider calls", n)
	}

	mustPost(t, s, rpcBody("2", "eth_getBlockByNumber", safeQuery))
	mustPost(t, s, rpcBody("3", "eth_getBlockByNumber", `["0x63",true]`))
	p.waitFor(t, "eth_getBlockByNumber", `["0x64",true]`, 1)
	p.waitFor(t, "eth_getBlockReceipts", `["0x64"]`, 1)
	time.Sleep(100 * time.Millisecond)
	for _, h := range []string{"0x65", "0x66", "0x67"} {
		if p.count("eth_getBlockByNumber", `["`+h+`",true]`)+p.count("eth_getBlockReceipts", `["`+h+`"]`) != 0 {
			t.Fatalf("router fetched %s, which is above the safe head", h)
		}
	}
	// A request above the safe head is forwarded as is and triggers nothing.
	before := p.total()
	mustPost(t, s, rpcBody("4", "eth_getBlockByNumber", `["0x65",true]`))
	mustPost(t, s, rpcBody("5", "eth_getBlockReceipts", `["0x65"]`))
	time.Sleep(100 * time.Millisecond)
	if n := p.total() - before; n != 2 {
		t.Fatalf("requests above the safe head caused %d provider calls, want 2", n)
	}
}

// A block the provider does not have yet is not remembered as missing: the
// scanner's own request goes to the provider and gets the provider's reply.
func TestReadAheadDoesNotServeMissingBlocks(t *testing.T) {
	p := newChainProvider(t, 0x64)
	p.missing[`eth_getBlockByNumber ["0x11",true]`] = true
	_, s := readAheadRouter(t, p, 1)
	mustPost(t, s, rpcBody("1", "eth_getBlockByNumber", safeQuery))
	mustPost(t, s, rpcBody("2", "eth_getBlockByNumber", `["0x10",true]`))
	p.waitFor(t, "eth_getBlockByNumber", `["0x11",true]`, 1)
	code, reply := mustPost(t, s, rpcBody("3", "eth_getBlockByNumber", `["0x11",true]`))
	if code != 200 || !strings.Contains(reply, `"id":3`) || !strings.Contains(reply, `"result":null`) {
		t.Fatalf("provider's null reply not passed through: %d %q", code, reply)
	}
	if n := p.count("eth_getBlockByNumber", `["0x11",true]`); n != 2 {
		t.Fatalf("provider saw %d calls for the missing block, want 2", n)
	}
}

// Header-by-height queries are the scanner's re-check that a block is still
// canonical. They always reach the provider, as does anything not asked by
// canonical height.
func TestReadAheadLeavesRechecksAndOtherFormsOpaque(t *testing.T) {
	p := newChainProvider(t, 0x64)
	_, s := readAheadRouter(t, p, 2)
	mustPost(t, s, rpcBody("1", "eth_getBlockByNumber", safeQuery))
	mustPost(t, s, rpcBody("2", "eth_getBlockByNumber", `["0x10",true]`))
	p.waitFor(t, "eth_getBlockReceipts", `["0x12"]`, 1)
	for _, q := range [][2]string{
		{"eth_getBlockByNumber", `["0x10",false]`},
		{"eth_getBlockByNumber", `["0x11",false]`},
		{"eth_getBlockByNumber", `["0x011",true]`},
		{"eth_getBlockByNumber", `["0X11",true]`},
		{"eth_getBlockReceipts", `["0xabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"]`},
		{"eth_getBlockReceipts", `["0x11","x"]`},
		{"eth_getBlockByHash", `["0x11",true]`},
	} {
		for i := 1; i <= 2; i++ {
			mustPost(t, s, rpcBody("9", q[0], q[1]))
			if n := p.count(q[0], q[1]); n != i {
				t.Fatalf("%s %s: provider saw %d calls after %d requests", q[0], q[1], n, i)
			}
		}
	}
}

func TestReadAheadConfig(t *testing.T) {
	p := newChainProvider(t, 0x64)
	c, file := fixtureConfig(t, map[string]map[string]string{"base": {"normal": p.URL}})
	for _, bad := range [][2]int{{0, 2}, {1000, -1}, {1000, 9}} {
		ch := c.Chains["base"]
		ch.HeadCacheMS, ch.ReadAheadBlocks = bad[0], bad[1]
		c.Chains["base"] = ch
		writeJSON(t, file, c)
		if _, err := loadCatalog(file); err == nil {
			t.Fatalf("head_cache_ms=%d read_ahead_blocks=%d accepted", bad[0], bad[1])
		}
	}
	// Off unless configured, even with the head cache on.
	_, s := readAheadRouter(t, p, 0)
	mustPost(t, s, rpcBody("1", "eth_getBlockByNumber", safeQuery))
	mustPost(t, s, rpcBody("2", "eth_getBlockByNumber", `["0x10",true]`))
	time.Sleep(100 * time.Millisecond)
	if n := p.total(); n != 2 {
		t.Fatalf("read-ahead ran without read_ahead_blocks: %d provider calls", n)
	}
}

// After fetching the safe head the scanner asks for the same header again by
// its number. That is the cached head itself; any other height is a re-check of
// a scanned block and reaches the provider every time.
func TestHeadCacheAnswersHeaderByNumberOnlyForACurrentHead(t *testing.T) {
	p := newChainProvider(t, 0x64)
	_, s := readAheadRouter(t, p, 0)
	_, head := result(t, mustPostReply(t, s, rpcBody("1", "eth_getBlockByNumber", safeQuery)))
	id, same := result(t, mustPostReply(t, s, rpcBody("2", "eth_getBlockByNumber", `["0x64",false]`)))
	if id != "2" || same != head || p.count("eth_getBlockByNumber", `["0x64",false]`) != 0 {
		t.Fatalf("header at the safe head: id=%s %s (head %s), provider calls %d", id, same, head, p.count("eth_getBlockByNumber", `["0x64",false]`))
	}
	for _, q := range []string{`["0x63",false]`, `["0x65",false]`, `["0x64",true]`, `["0x064",false]`} {
		for i := 1; i <= 2; i++ {
			mustPost(t, s, rpcBody("3", "eth_getBlockByNumber", q))
			if n := p.count("eth_getBlockByNumber", q); n != i {
				t.Fatalf("%s: provider saw %d calls after %d requests", q, n, i)
			}
		}
	}
	// Once the head entry has expired the by-number query goes to the provider.
	time.Sleep(1100 * time.Millisecond)
	mustPost(t, s, rpcBody("4", "eth_getBlockByNumber", `["0x64",false]`))
	if n := p.count("eth_getBlockByNumber", `["0x64",false]`); n != 1 {
		t.Fatalf("expired head served by number: provider calls %d", n)
	}
}

// With read_ahead_headers the two per-block header streams (this height, and a
// fixed distance behind it) are also fetched ahead, each height once per
// stream, and never above the safe head.
func TestReadAheadHeadersFollowsSequentialHeaderStreams(t *testing.T) {
	p := newChainProvider(t, 0x64)
	r, s := readAheadRouterWith(t, p, 2, true)
	mustPost(t, s, rpcBody("1", "eth_getBlockByNumber", safeQuery))
	hdr := func(h string) string { return `["` + h + `",false]` }

	// First query of a stream goes to the provider and starts the next two.
	_, res := result(t, mustPostReply(t, s, rpcBody("2", "eth_getBlockByNumber", hdr("0x20"))))
	if !strings.Contains(res, `"hash":"0xhdr20"`) {
		t.Fatalf("first header: %s", res)
	}
	p.waitFor(t, "eth_getBlockByNumber", hdr("0x22"), 1)
	before := p.total()
	id, res := result(t, mustPostReply(t, s, rpcBody(`"h"`, "eth_getBlockByNumber", hdr("0x21"))))
	if id != `"h"` || !strings.Contains(res, `"hash":"0xhdr21"`) || p.count("eth_getBlockByNumber", hdr("0x21")) != 1 {
		t.Fatalf("next header: id=%s %s, provider calls %d", id, res, p.count("eth_getBlockByNumber", hdr("0x21")))
	}
	p.waitFor(t, "eth_getBlockByNumber", hdr("0x23"), 1)
	if n := p.total() - before; n != 1 {
		t.Fatalf("serving one header caused %d provider calls, want 1 (the next height)", n)
	}
	if n := metricSum(t, r, "rpc_router_read_ahead_hits_total", `chain="base"`); n != 1 {
		t.Fatalf("read-ahead hits = %v, want 1", n)
	}
	// The trailing stream asks for the same height later and gets its own provider call.
	mustPost(t, s, rpcBody("3", "eth_getBlockByNumber", hdr("0x21")))
	if n := p.count("eth_getBlockByNumber", hdr("0x21")); n != 2 {
		t.Fatalf("second stream reused a handed-out header: provider calls %d", n)
	}
	// At and above the safe head.
	mustPost(t, s, rpcBody("4", "eth_getBlockByNumber", hdr("0x63")))
	mustPost(t, s, rpcBody("5", "eth_getBlockByNumber", hdr("0x65")))
	time.Sleep(100 * time.Millisecond)
	for _, h := range []string{"0x66", "0x67"} {
		if p.count("eth_getBlockByNumber", hdr(h)) != 0 {
			t.Fatalf("router fetched header %s above the safe head", h)
		}
	}
	if n := p.count("eth_getBlockByNumber", hdr("0x65")); n != 1 {
		t.Fatalf("header above the safe head: provider calls %d, want 1", n)
	}
}

func TestReadAheadHeadersNeedsReadAhead(t *testing.T) {
	p := newChainProvider(t, 0x64)
	c, file := fixtureConfig(t, map[string]map[string]string{"base": {"normal": p.URL}})
	ch := c.Chains["base"]
	ch.HeadCacheMS, ch.ReadAheadHeaders = 1000, true
	c.Chains["base"] = ch
	writeJSON(t, file, c)
	if _, err := loadCatalog(file); err == nil {
		t.Fatal("read_ahead_headers accepted without read_ahead_blocks")
	}
}
