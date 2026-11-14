package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// evmCalls stands in for an EVM provider answering state queries pinned to a
// block. Each result is derived from the exact params, so a test can tell
// whether the reply it got belongs to the call it made.
type evmCalls struct {
	*httptest.Server
	mu    sync.Mutex
	seen  map[string]int
	fail  map[string]bool // keys answered with a JSON-RPC error
	delay time.Duration
}

func callResult(method, params string) string { return fmt.Sprintf("%q", method+"@"+params) }

func newEVMCalls(t *testing.T) *evmCalls {
	p := &evmCalls{seen: map[string]int{}, fail: map[string]bool{}}
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
		fail, delay := p.fail[key], p.delay
		p.mu.Unlock()
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		if fail {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"execution reverted"}}`, q.ID)
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, q.ID, callResult(q.Method, string(q.Params)))
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *evmCalls) count(method, params string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[method+" "+params]
}

func (p *evmCalls) total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, v := range p.seen {
		n += v
	}
	return n
}

func (p *evmCalls) waitTotal(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for p.total() < want {
		if time.Now().After(deadline) {
			t.Fatalf("provider saw %d calls, want %d", p.total(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond)
}

func callRouter(t *testing.T, url string, on bool) *httptest.Server {
	t.Helper()
	c, file := fixtureConfig(t, map[string]map[string]string{"base": {"normal": url}})
	ch := c.Chains["base"]
	ch.HeadCacheMS, ch.CallAhead = 200, on
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

// The calls a solvency report makes for one vault: an allowance per token and
// the gas balance, all pinned to one block.
func allowance(vault, token int, tag string) string {
	return fmt.Sprintf(`[{"from":"0x%040x","input":"0x03b6a673%064x%064x","to":"0x00000000000000000000000000000000000000aa"},"%s"]`, vault, vault, token, tag)
}

func balance(vault int, tag string) string { return fmt.Sprintf(`["0x%040x","%s"]`, vault, tag) }

type pinned struct{ method, params string }

func solvencyRun(tag string, vaults, tokens int) []pinned {
	var run []pinned
	for v := 1; v <= vaults; v++ {
		for k := 1; k <= tokens; k++ {
			run = append(run, pinned{"eth_call", allowance(v, k, tag)})
		}
		run = append(run, pinned{"eth_getBalance", balance(v, tag)})
	}
	return run
}

func askAll(t *testing.T, s *httptest.Server, run []pinned) {
	t.Helper()
	for i, c := range run {
		id, got := result(t, mustPostReply(t, s, rpcBody(fmt.Sprint(i+1), c.method, c.params)))
		if id != fmt.Sprint(i+1) || got != callResult(c.method, c.params) {
			t.Fatalf("call %d (%s %s): id %s result %s", i, c.method, c.params, id, got)
		}
	}
}

func TestCallAheadIsOffWithoutTheOption(t *testing.T) {
	p := newEVMCalls(t)
	s := callRouter(t, p.URL, false)
	askAll(t, s, solvencyRun("0x64", 2, 5))
	second := solvencyRun("0x78", 2, 5)
	mustPost(t, s, rpcBody("1", second[0].method, second[0].params))
	time.Sleep(150 * time.Millisecond)
	if n := p.total(); n != 13 {
		t.Fatalf("provider saw %d calls with call_ahead off, want 12 + 1", n)
	}
}

func TestCallAheadFetchesTheKnownRunWhenItsFirstCallArrives(t *testing.T) {
	p := newEVMCalls(t)
	s := callRouter(t, p.URL, true)
	first := solvencyRun("0x64", 3, 6) // 21 calls, learned as they pass through
	askAll(t, s, first)
	if n := p.total(); n != len(first) {
		t.Fatalf("learning run: provider saw %d calls, want %d", n, len(first))
	}

	next := solvencyRun("0x78", 3, 6)
	p.set(40 * time.Millisecond)
	// Only the first call of the new run is made; the router fetches the rest.
	if _, got := result(t, mustPostReply(t, s, rpcBody("1", next[0].method, next[0].params))); got != callResult(next[0].method, next[0].params) {
		t.Fatalf("first call of the run: %s", got)
	}
	p.waitTotal(t, 2*len(first))
	for _, c := range next {
		if n := p.count(c.method, c.params); n != 1 {
			t.Fatalf("%s %s reached the provider %d times before the scanner asked, want 1", c.method, c.params, n)
		}
	}

	began := time.Now()
	askAll(t, s, next) // Every reply is the provider's own for that exact call.
	if took := time.Since(began); took > 400*time.Millisecond {
		t.Fatalf("21 held calls took %v; they were not answered from the early results", took)
	}
	if n := p.total(); n != 2*len(first) {
		t.Fatalf("provider saw %d calls, want each call of both runs once (%d)", n, 2*len(first))
	}
}

func (p *evmCalls) set(d time.Duration) {
	p.mu.Lock()
	p.delay = d
	p.mu.Unlock()
}

func TestCallAheadForwardsACallThatWasNotInTheLastRun(t *testing.T) {
	p := newEVMCalls(t)
	s := callRouter(t, p.URL, true)
	askAll(t, s, solvencyRun("0x64", 2, 5))

	next := solvencyRun("0x78", 2, 5)
	extra := pinned{"eth_call", allowance(9, 1, "0x78")} // A vault that was not there before.
	askAll(t, s, append(next, extra))
	if n := p.count(extra.method, extra.params); n != 1 {
		t.Fatalf("the new call reached the provider %d times, want forwarded once", n)
	}

	// It belongs to the run now and is fetched early next time.
	third := solvencyRun("0x8c", 2, 5)
	mustPost(t, s, rpcBody("1", third[0].method, third[0].params))
	later := pinned{"eth_call", allowance(9, 1, "0x8c")}
	deadline := time.Now().Add(3 * time.Second)
	for p.count(later.method, later.params) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("a call learned in the previous run was not fetched early")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCallAheadKeepsTheRunWhenAnUnrelatedCallPassesThrough(t *testing.T) {
	p := newEVMCalls(t)
	s := callRouter(t, p.URL, true)
	first := solvencyRun("0x64", 2, 5)
	askAll(t, s, first)

	// A one-off call at another height is forwarded, starts nothing, and does
	// not replace what was learned.
	stray := pinned{"eth_call", allowance(7, 7, "0x65")}
	askAll(t, s, []pinned{stray})
	time.Sleep(100 * time.Millisecond)
	if n := p.total(); n != len(first)+1 {
		t.Fatalf("provider saw %d calls after a stray call, want %d", n, len(first)+1)
	}

	next := solvencyRun("0x78", 2, 5)
	mustPost(t, s, rpcBody("1", next[0].method, next[0].params))
	p.waitTotal(t, 2*len(first)+1)
	if n := p.count(stray.method, allowance(7, 7, "0x78")); n != 0 {
		t.Fatal("the stray call became part of the run")
	}
}

func TestCallAheadNeverHandsOutAFailedEarlyCall(t *testing.T) {
	p := newEVMCalls(t)
	s := callRouter(t, p.URL, true)
	askAll(t, s, solvencyRun("0x64", 2, 5))

	next := solvencyRun("0x78", 2, 5)
	bad := next[3]
	p.mu.Lock()
	p.fail[bad.method+" "+bad.params] = true
	p.mu.Unlock()
	mustPost(t, s, rpcBody("1", next[0].method, next[0].params))
	p.waitTotal(t, 2*len(next))

	// The scanner's own request is forwarded and gets the provider's error.
	_, reply := mustPost(t, s, rpcBody("7", bad.method, bad.params))
	var v struct {
		ID    json.RawMessage `json:"id"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(reply), &v); err != nil || string(v.ID) != "7" || len(v.Error) == 0 {
		t.Fatalf("want the provider's error for id 7, got %s", reply)
	}
	if n := p.count(bad.method, bad.params); n != 2 {
		t.Fatalf("failed call reached the provider %d times, want early + forwarded", n)
	}
}

func TestCallAheadIgnoresCallsThatAreNotPinnedToAHeight(t *testing.T) {
	p := newEVMCalls(t)
	s := callRouter(t, p.URL, true)
	for _, tag := range []string{"latest", "pending", "0x064"} { // 0x064 is not canonical
		run := solvencyRun(tag, 2, 5)
		askAll(t, s, run)
		askAll(t, s, run)
		for _, c := range run {
			if n := p.count(c.method, c.params); n != 2 {
				t.Fatalf("%s %s reached the provider %d times, want both forwarded", c.method, c.params, n)
			}
		}
	}
}

func TestCallAheadResultsExpire(t *testing.T) {
	old := callKeepNS.Swap(int64(150 * time.Millisecond))
	t.Cleanup(func() { callKeepNS.Store(old) })
	p := newEVMCalls(t)
	s := callRouter(t, p.URL, true)
	askAll(t, s, solvencyRun("0x64", 2, 5))
	next := solvencyRun("0x78", 2, 5)
	mustPost(t, s, rpcBody("1", next[0].method, next[0].params))
	p.waitTotal(t, 2*len(next))
	time.Sleep(200 * time.Millisecond)
	askAll(t, s, next[1:])
	for _, c := range next[1:] {
		if n := p.count(c.method, c.params); n != 2 {
			t.Fatalf("%s %s reached the provider %d times after expiry, want early + forwarded", c.method, c.params, n)
		}
	}
}

func TestCallAheadNeedsTheHeadCache(t *testing.T) {
	c, file := fixtureConfig(t, map[string]map[string]string{"base": {"normal": "https://example.invalid"}})
	ch := c.Chains["base"]
	ch.CallAhead = true
	c.Chains["base"] = ch
	writeJSON(t, file, c)
	if _, err := newRouter(file, true); err == nil {
		t.Fatal("call_ahead without head_cache_ms was accepted")
	}
}
