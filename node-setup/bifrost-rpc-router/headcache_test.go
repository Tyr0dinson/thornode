package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// headProvider is a JSON-RPC provider whose head advances on every head query,
// so a repeated answer can only have come from the router's cache.
type headProvider struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
	head   atomic.Int64
	delay  time.Duration
	fail   atomic.Value // "", "rpcerror", "503", "null"
}

func newHeadProvider(t *testing.T) *headProvider {
	p := &headProvider{}
	p.fail.Store("")
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		p.mu.Lock()
		p.bodies = append(p.bodies, string(b))
		p.mu.Unlock()
		time.Sleep(p.delay)
		var q struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(b, &q) != nil { // batch or malformed: echo, as an opaque provider reply
			fmt.Fprintf(w, `{"echo":%q}`, b)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch p.fail.Load().(string) {
		case "rpcerror":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"header not found"}}`, q.ID)
			return
		case "503":
			w.WriteHeader(503)
			fmt.Fprint(w, "busy")
			return
		case "null":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":null}`, q.ID)
			return
		}
		n := p.head.Add(1)
		if q.Method == "eth_blockNumber" {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":"0x%x"}`, q.ID, n)
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"number":"0x%x","hash":"0xabc"}}`, q.ID, n)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *headProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

// headRouter serves chain "base" with a head cache of ttl (0 leaves it off).
func headRouter(t *testing.T, ttl time.Duration, backends map[string]string) (*router, *httptest.Server) {
	t.Helper()
	c, file := fixtureConfig(t, map[string]map[string]string{"base": backends})
	ch := c.Chains["base"]
	ch.HeadCacheMS = int(ttl / time.Millisecond)
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

func rpcBody(id, method, params string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":%q,"params":%s}`, id, method, params)
}

func mustPost(t *testing.T, s *httptest.Server, body string) (int, string) {
	t.Helper()
	code, reply, err := post(s.Client(), s.URL+"/base", body)
	if err != nil {
		t.Fatal(err)
	}
	return code, reply
}

func result(t *testing.T, reply string) (id, res string) {
	t.Helper()
	var v struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(reply), &v); err != nil || v.JSONRPC != "2.0" || len(v.Error) != 0 {
		t.Fatalf("not a JSON-RPC result: %q", reply)
	}
	return string(v.ID), string(v.Result)
}

// Bifrost asks for the latest header four times for every block it scans. With
// the cache on, one provider call answers all of them, each under its own id.
func TestHeadCacheAnswersRepeatedHeadQueriesFromOneProviderCall(t *testing.T) {
	p := newHeadProvider(t)
	r, s := headRouter(t, 300*time.Millisecond, map[string]string{"normal": p.URL})
	var first string
	for i, id := range []string{`7`, `"abc"`, `9007199254740993`, `8`} {
		code, reply := mustPost(t, s, rpcBody(id, "eth_getBlockByNumber", `["latest",false]`))
		gotID, res := result(t, reply)
		if code != 200 || gotID != id {
			t.Fatalf("call %d: code=%d id=%s want %s (%q)", i, code, gotID, id, reply)
		}
		if i == 0 {
			first = res
		} else if res != first {
			t.Fatalf("call %d was not served from the cache: %s vs %s", i, res, first)
		}
	}
	if n := p.calls(); n != 1 {
		t.Fatalf("provider saw %d calls for four identical head queries", n)
	}
	// Each tag and method is its own entry.
	for _, q := range [][2]string{{"eth_getBlockByNumber", `["safe",false]`}, {"eth_getBlockByNumber", `["finalized",false]`}, {"eth_getHeaderByNumber", `["latest"]`}, {"eth_blockNumber", `[]`}} {
		_, a := result(t, mustPostReply(t, s, rpcBody("1", q[0], q[1])))
		_, b := result(t, mustPostReply(t, s, rpcBody("2", q[0], q[1])))
		if a != b || a == first {
			t.Fatalf("%s %s: a=%s b=%s first=%s", q[0], q[1], a, b, first)
		}
	}
	if n := p.calls(); n != 5 {
		t.Fatalf("provider saw %d calls, want 5 (one per distinct head query)", n)
	}
	if n := metricSum(t, r, "rpc_router_head_cache_hits_total", `chain="base"`); n != 7 {
		t.Fatalf("cache hits metric = %v, want 7", n)
	}
	// The entry expires: staleness is bounded by the configured time.
	time.Sleep(350 * time.Millisecond)
	if _, res := result(t, mustPostReply(t, s, rpcBody("3", "eth_getBlockByNumber", `["latest",false]`))); res == first {
		t.Fatal("expired entry was served")
	}
}

func mustPostReply(t *testing.T, s *httptest.Server, body string) string {
	t.Helper()
	_, reply := mustPost(t, s, body)
	return reply
}

// Anything that is not exactly a head query goes to the provider untouched.
func TestHeadCacheLeavesOtherRequestsOpaque(t *testing.T) {
	p := newHeadProvider(t)
	_, s := headRouter(t, time.Second, map[string]string{"normal": p.URL})
	bodies := []string{
		rpcBody("1", "eth_getBlockByNumber", `["0x3000000",false]`),
		rpcBody("1", "eth_getBlockByNumber", `["latest",true]`),
		rpcBody("1", "eth_getBlockByNumber", `["pending",false]`),
		rpcBody("1", "eth_getBlockByNumber", `["latest"]`),
		rpcBody("1", "eth_getBlockReceipts", `["latest"]`),
		rpcBody("1", "eth_call", `[{"to":"0x00"},"latest"]`),
		rpcBody("1", "eth_blockNumber", `["latest"]`),
		rpcBody("null", "eth_blockNumber", `[]`),
		`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[]}`,
		`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[],"extra":1}`,
		`{"jsonrpc":"2.0","id":1,"id":2,"method":"eth_blockNumber","params":[]}`,
		`[` + rpcBody("1", "eth_blockNumber", `[]`) + `,` + rpcBody("2", "eth_blockNumber", `[]`) + `]`,
		`{"jsonrpc": "2.0", "id": 1, "method": "eth_getBlockByNumber", "params": ["0x1", true]}`,
		`not json`,
	}
	for _, b := range bodies {
		for i := 0; i < 2; i++ {
			mustPost(t, s, b)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.bodies) != 2*len(bodies) {
		t.Fatalf("provider saw %d of %d requests", len(p.bodies), 2*len(bodies))
	}
	for i, got := range p.bodies {
		if got != bodies[i/2] {
			t.Fatalf("request %d reached the provider altered: %q, sent %q", i, got, bodies[i/2])
		}
	}
}

// A provider failure is never cached and reaches the caller exactly as sent.
func TestHeadCacheDoesNotCacheFailures(t *testing.T) {
	p := newHeadProvider(t)
	_, s := headRouter(t, time.Second, map[string]string{"normal": p.URL})
	q := rpcBody("5", "eth_getBlockByNumber", `["latest",false]`)
	for _, mode := range []string{"rpcerror", "503", "null"} {
		p.fail.Store(mode)
		before := p.calls()
		code, reply := mustPost(t, s, q)
		switch mode {
		case "rpcerror":
			if code != 200 || !strings.Contains(reply, `"id":5`) || !strings.Contains(reply, "header not found") {
				t.Fatalf("rpc error not passed through: %d %q", code, reply)
			}
		case "503":
			if code != 503 || reply != "busy" {
				t.Fatalf("503 not passed through: %d %q", code, reply)
			}
		case "null":
			if code != 200 || !strings.Contains(reply, `"result":null`) {
				t.Fatalf("null result not passed through: %d %q", code, reply)
			}
		}
		mustPost(t, s, q)
		if n := p.calls() - before; n < 2 {
			t.Fatalf("%s: second call was answered from a cached failure (%d provider calls)", mode, n)
		}
	}
	p.fail.Store("")
	_, a := result(t, mustPostReply(t, s, q))
	_, b := result(t, mustPostReply(t, s, q))
	if a != b {
		t.Fatal("cache did not resume after the provider recovered")
	}
}

// Off unless configured: the gateway (P1) stays a pure switch.
func TestHeadCacheIsOffByDefault(t *testing.T) {
	p := newHeadProvider(t)
	_, s := headRouter(t, 0, map[string]string{"normal": p.URL})
	q := rpcBody("1", "eth_getBlockByNumber", `["latest",false]`)
	_, a := result(t, mustPostReply(t, s, q))
	_, b := result(t, mustPostReply(t, s, q))
	if a == b || p.calls() != 2 {
		t.Fatalf("head query cached without head_cache_ms: %s %s calls=%d", a, b, p.calls())
	}
}

// Bifrost's two fetch streams ask at the same moment; they share one call.
func TestHeadCacheSharesOneInflightCall(t *testing.T) {
	p := newHeadProvider(t)
	p.delay = 100 * time.Millisecond
	_, s := headRouter(t, time.Second, map[string]string{"normal": p.URL})
	var wg sync.WaitGroup
	results := make([]string, 16)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, reply, err := post(s.Client(), s.URL+"/base", rpcBody(fmt.Sprint(i+1), "eth_getHeaderByNumber", `["latest"]`))
			if err == nil && code == 200 && strings.Contains(reply, fmt.Sprintf(`"id":%d,`, i+1)) {
				results[i] = reply[strings.Index(reply, `"result"`):]
			}
		}()
	}
	wg.Wait()
	for i, r := range results {
		if r == "" || r != results[0] {
			t.Fatalf("caller %d got %q, caller 0 got %q", i, r, results[0])
		}
	}
	if n := p.calls(); n != 1 {
		t.Fatalf("16 concurrent head queries made %d provider calls", n)
	}
}

// A head learned from one provider is never served after switching to another.
func TestHeadCacheIsPerBackend(t *testing.T) {
	a, b := newHeadProvider(t), newHeadProvider(t)
	b.head.Store(1000)
	r, s := headRouter(t, time.Second, map[string]string{"normal": a.URL, "alternate": b.URL})
	q := rpcBody("1", "eth_blockNumber", `[]`)
	_, fromA := result(t, mustPostReply(t, s, q))
	switchRoute(t, r, "base", "alternate")
	_, fromB := result(t, mustPostReply(t, s, q))
	if fromA != `"0x1"` || fromB != `"0x3e9"` {
		t.Fatalf("after switch got %s then %s", fromA, fromB)
	}
}

func TestHeadCacheConfigBoundsAndReload(t *testing.T) {
	p := newHeadProvider(t)
	c, file := fixtureConfig(t, map[string]map[string]string{"base": {"normal": p.URL}})
	for _, bad := range []int{-1, 5001} {
		ch := c.Chains["base"]
		ch.HeadCacheMS = bad
		c.Chains["base"] = ch
		writeJSON(t, file, c)
		if _, err := loadCatalog(file); err == nil {
			t.Fatalf("head_cache_ms=%d accepted", bad)
		}
	}
	// The time can be tuned, or the cache turned off, by reload without a restart.
	ch := c.Chains["base"]
	ch.HeadCacheMS = 0
	c.Chains["base"] = ch
	writeJSON(t, file, c)
	r, err := newRouter(file, true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	ch.HeadCacheMS = 1000
	c.Chains["base"] = ch
	writeJSON(t, file, c)
	if w := adminRequest(r, "/reload", `{}`, r.current.Load().state.Version); w.Code != 200 {
		t.Fatalf("reload changing head_cache_ms: %d %s", w.Code, w.Body.String())
	}
	s := httptest.NewServer(r)
	defer s.Close()
	q := rpcBody("1", "eth_blockNumber", `[]`)
	for i := 0; i < 3; i++ {
		if code, _, err := post(s.Client(), s.URL+"/base/", q); err != nil || code != 200 {
			t.Fatal(code, err)
		}
	}
	if n := p.calls(); n != 1 {
		t.Fatalf("cache not active after reload: %d provider calls", n)
	}
}

// A scanner polls the head many times a second. Entries are replaced in the
// background before they expire, so after the first call no poll waits for the
// provider, the head keeps advancing, and the provider is asked only a few
// times per window.
func TestHeadCacheRefreshesBeforeExpiryWithoutStallingCallers(t *testing.T) {
	p := newHeadProvider(t)
	p.delay = 120 * time.Millisecond
	_, s := headRouter(t, 300*time.Millisecond, map[string]string{"normal": p.URL})
	q := rpcBody("1", "eth_getBlockByNumber", `["latest",false]`)
	mustPost(t, s, q) // first call waits for the provider
	seen := map[string]bool{}
	var slowest time.Duration
	stop := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(stop) {
		start := time.Now()
		_, res := result(t, mustPostReply(t, s, q))
		if d := time.Since(start); d > slowest {
			slowest = d
		}
		seen[res] = true
		time.Sleep(15 * time.Millisecond)
	}
	if slowest > 80*time.Millisecond {
		t.Fatalf("a poll waited %v for the provider (round trip is 120ms)", slowest)
	}
	if len(seen) < 4 {
		t.Fatalf("head advanced only %d times in 1.2s with a 300ms window", len(seen))
	}
	if n := p.calls(); n > 12 {
		t.Fatalf("provider asked %d times in 1.2s", n)
	}
}

// The scanner keeps asking for a head header by its number after the cache has
// refreshed to a newer head. The reply it was given stays answerable for twice
// the window, then the query goes to the provider.
func TestHeadCacheAnswersByNumberForARecentlyReplacedHead(t *testing.T) {
	p := newHeadProvider(t)
	_, s := headRouter(t, 300*time.Millisecond, map[string]string{"normal": p.URL})
	latest := rpcBody("1", "eth_getBlockByNumber", `["latest",false]`)
	number := func(res string) string {
		var h struct {
			Number string `json:"number"`
		}
		if json.Unmarshal([]byte(res), &h) != nil || h.Number == "" {
			t.Fatalf("no number in %s", res)
		}
		return h.Number
	}
	_, first := result(t, mustPostReply(t, s, latest))
	old := number(first)
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, res := result(t, mustPostReply(t, s, latest))
		if number(res) != old {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("head never refreshed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	before := p.calls()
	id, res := result(t, mustPostReply(t, s, rpcBody("9", "eth_getBlockByNumber", `["`+old+`",false]`)))
	if id != "9" || res != first || p.calls() != before {
		t.Fatalf("replaced head by number: id=%s same=%t provider calls +%d", id, res == first, p.calls()-before)
	}
	time.Sleep(650 * time.Millisecond)
	before = p.calls()
	mustPost(t, s, rpcBody("10", "eth_getBlockByNumber", `["`+old+`",false]`))
	if p.calls() != before+1 {
		t.Fatalf("head older than twice the window answered from cache (provider calls +%d)", p.calls()-before)
	}
}
