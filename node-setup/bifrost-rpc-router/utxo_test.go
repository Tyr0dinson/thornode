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

// bitcoind stands in for a bitcoind-RPC provider. Block h holds a coinbase, a
// plain payment spending one parent, and a memo transaction (OP_RETURN output)
// spending two parents.
type bitcoind struct {
	*httptest.Server
	mu      sync.Mutex
	tip     int64
	seen    map[string]int // "method params" -> provider calls, single or inside a batch
	batches [][]string     // ids of every batch received, in order
	broken  map[string]bool
	delay   time.Duration
	zebra   bool // Like Zebra: lookups must use verbosity 1, and there is no getblockstats.
}

func blockHash(h int64) string     { return fmt.Sprintf("%064x", h) }
func parent(h int64, n int) string { return fmt.Sprintf("aa%062x", h*10+int64(n)) }

func (p *bitcoind) answer(method string, params json.RawMessage) (result, rpcErr string) {
	var args []any
	_ = json.Unmarshal(params, &args)
	key := method + " " + string(params)
	p.mu.Lock()
	p.seen[key]++
	tip, broken, zebra := p.tip, p.broken[key], p.zebra
	p.mu.Unlock()
	if broken {
		return "null", "null"
	}
	if zebra && method == "getblockstats" {
		return "null", `{"code":-32601,"message":"Method not found"}`
	}
	if zebra && method == "getrawtransaction" && len(args) == 2 && args[1] == true {
		return "null", `{"code":-1,"message":"Invalid params"}`
	}
	switch method {
	case "getblockcount":
		return fmt.Sprint(tip), "null"
	case "getblockhash":
		f, isNumber := args[0].(float64)
		h := int64(f)
		if !isNumber {
			return `"forwarded odd getblockhash"`, "null"
		}
		if h > tip {
			return "null", `{"code":-8,"message":"Block height out of range"}`
		}
		return `"` + blockHash(h) + `"`, "null"
	case "getblock":
		var h int64
		fmt.Sscanf(args[0].(string), "%x", &h)
		if len(args) != 2 || args[1] != float64(2) {
			return `{"hash":"` + args[0].(string) + `","tx":["only-ids"]}`, "null"
		}
		return fmt.Sprintf(`{"hash":%q,"height":%d,"tx":[`+
			`{"txid":"c0","vin":[{"coinbase":"00"}],"vout":[{"value":6.25,"scriptPubKey":{"type":"pubkeyhash"}}]},`+
			`{"txid":"t1","vin":[{"txid":%q,"vout":0}],"vout":[{"value":1,"scriptPubKey":{"type":"witness_v0_keyhash"}}]},`+
			`{"txid":"t2","vin":[{"txid":%q,"vout":1},{"txid":%q,"vout":0}],"vout":[{"value":2,"scriptPubKey":{"type":"witness_v0_keyhash"}},{"value":0,"scriptPubKey":{"type":"nulldata"}}]}]}`,
			blockHash(h), h, parent(h, 1), parent(h, 2), parent(h, 3)), "null"
	case "getrawtransaction":
		txid := args[0].(string)
		if !strings.HasPrefix(txid, "aa") {
			return "null", `{"code":-5,"message":"No such mempool or blockchain transaction"}`
		}
		return `{"txid":"` + txid + `","vout":[{"value":1.5}]}`, "null"
	}
	return `"forwarded ` + method + `"`, "null"
}

func newBitcoind(t *testing.T, tip int64) *bitcoind {
	p := &bitcoind{tip: tip, seen: map[string]int{}, broken: map[string]bool{}}
	type call struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	one := func(c call) string {
		result, rpcErr := p.answer(c.Method, c.Params)
		return `{"result":` + result + `,"error":` + rpcErr + `,"id":` + string(c.ID) + `}`
	}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		p.mu.Lock()
		delay := p.delay
		p.mu.Unlock()
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(strings.TrimSpace(string(b)), "[") {
			var calls []call
			_ = json.Unmarshal(b, &calls)
			ids, parts := make([]string, len(calls)), make([]string, len(calls))
			for i, c := range calls {
				ids[i], parts[i] = string(c.ID), one(c)
			}
			p.mu.Lock()
			p.batches = append(p.batches, ids)
			p.mu.Unlock()
			fmt.Fprint(w, "["+strings.Join(parts, ",")+"]")
			return
		}
		var c call
		_ = json.Unmarshal(b, &c)
		fmt.Fprint(w, one(c))
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *bitcoind) count(method, params string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[method+" "+params]
}

func (p *bitcoind) total(skip string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for k, v := range p.seen {
		if !strings.HasPrefix(k, skip) {
			n += v
		}
	}
	return n
}

func (p *bitcoind) setTip(h int64) {
	p.mu.Lock()
	p.tip = h
	p.mu.Unlock()
}

func (p *bitcoind) waitFor(t *testing.T, method, params string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for p.count(method, params) < want {
		if time.Now().After(deadline) {
			t.Fatalf("provider never saw %s %s", method, params)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond) // let the replies land in the router
}

func utxoRouter(t *testing.T, p *bitcoind, ms int) (*router, *httptest.Server) {
	t.Helper()
	c, file := fixtureConfig(t, map[string]map[string]string{"ltc": {"normal": p.URL}})
	ch := c.Chains["ltc"]
	ch.UTXOAheadMS = ms
	ch.Endpoints = map[string]Endpoint{"rpc": {Path: "/ltc"}}
	c.Chains["ltc"] = ch
	writeJSON(t, file, c)
	r, err := newRouter(file, true)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(r)
	t.Cleanup(func() { s.Close(); r.close() })
	return r, s
}

func ltc(t *testing.T, s *httptest.Server, body string) string {
	t.Helper()
	code, reply, err := post(s.Client(), s.URL+"/ltc", body)
	if err != nil || code != 200 {
		t.Fatalf("post: %d %v %s", code, err, reply)
	}
	return strings.TrimSpace(reply)
}

func call(id int, method, params string) string {
	if params == "" {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q}`, id, method)
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params)
}

func lookup(txid string) string  { return `["` + txid + `",true]` }
func lookup1(txid string) string { return `["` + txid + `",1]` }

// teach has "Bifrost" make the per-block calls a BTC-family client makes, so
// the router knows it may make them early: a lookup batch and the two fee calls.
func teach(t *testing.T, s *httptest.Server) {
	t.Helper()
	ltc(t, s, "["+call(1, "getrawtransaction", lookup(parent(1, 1)))+"]")
	ltc(t, s, call(1, "getblockstats", `["`+blockHash(1)+`"]`))
	ltc(t, s, call(1, "getnetworkinfo", ""))
}

// advance has "Bifrost" learn the current tip, then moves the provider on one
// block and waits for the router to have fetched it.
func advance(t *testing.T, p *bitcoind, s *httptest.Server, to int64) {
	t.Helper()
	teach(t, s)
	ltc(t, s, call(1, "getblockcount", ""))
	p.setTip(to)
	p.waitFor(t, "getrawtransaction", lookup(parent(to, 3)), 1)
}

func TestUTXOAheadIsOffByDefault(t *testing.T) {
	p := newBitcoind(t, 100)
	_, s := utxoRouter(t, p, 0)
	for i := 0; i < 3; i++ {
		if got := ltc(t, s, call(i, "getblockcount", "")); got != fmt.Sprintf(`{"result":100,"error":null,"id":%d}`, i) {
			t.Fatalf("reply %q", got)
		}
	}
	time.Sleep(150 * time.Millisecond)
	if n := p.count("getblockcount", ""); n != 3 {
		t.Fatalf("provider saw %d getblockcount calls, want 3 forwarded and no polling", n)
	}
}

func TestUTXOAheadAnswersTipChecksFromTheRoutersCopy(t *testing.T) {
	p := newBitcoind(t, 100)
	_, s := utxoRouter(t, p, 5000) // Polls every 2.5 s: not during this test.
	for i := 0; i < 20; i++ {
		if got := ltc(t, s, call(i, "getblockcount", "")); got != fmt.Sprintf(`{"result":100,"error":null,"id":%d}`, i) {
			t.Fatalf("getblockcount reply %q", got)
		}
		if got := ltc(t, s, call(i, "getblockhash", "[100]")); got != fmt.Sprintf(`{"result":%q,"error":null,"id":%d}`, blockHash(100), i) {
			t.Fatalf("getblockhash reply %q", got)
		}
	}
	// The router's own calls always carry params.
	if a, b := p.count("getblockcount", "[]"), p.count("getblockhash", "[100]"); a != 1 || b != 1 {
		t.Fatalf("provider calls: getblockcount %d, getblockhash %d, want 1 each", a, b)
	}
	// The first tip seen is where the scanner already is: nothing is fetched early.
	if n := p.total("getblock"); n != 0 {
		t.Fatalf("%d unexpected provider calls", n)
	}
	// An error (height past the tip) is the provider's own reply, never cached.
	for i := 0; i < 2; i++ {
		if got := ltc(t, s, call(7, "getblockhash", "[101]")); !strings.Contains(got, `"code":-8`) {
			t.Fatalf("out-of-range reply %q", got)
		}
	}
	if n := p.count("getblockhash", "[101]"); n != 4 { // Each try: the router's call, then the forwarded one.
		t.Fatalf("provider saw %d out-of-range calls", n)
	}
}

func TestUTXOAheadFetchesANewBlockBeforeTheScannerAsks(t *testing.T) {
	p := newBitcoind(t, 100)
	_, s := utxoRouter(t, p, 1000) // Long enough that the tip checks below stay within one life.
	advance(t, p, s, 101)
	before := p.total("getblockcount")

	if got := ltc(t, s, call(2, "getblockcount", "")); got != `{"result":101,"error":null,"id":2}` {
		t.Fatalf("tip %q", got)
	}
	if got := ltc(t, s, call(3, "getblockhash", "[101]")); !strings.Contains(got, blockHash(101)) {
		t.Fatalf("hash %q", got)
	}
	block := ltc(t, s, call(4, "getblock", `["`+blockHash(101)+`",2]`))
	if !strings.HasPrefix(block, `{"result":{"hash":"`+blockHash(101)+`","height":101,"tx":[`) || !strings.HasSuffix(block, `,"error":null,"id":4}`) {
		t.Fatalf("block %q", block)
	}
	// Bifrost's first-input batch, then its single lookups for the memo transaction.
	batch := ltc(t, s, "["+call(10, "getrawtransaction", lookup(parent(101, 1)))+","+call(11, "getrawtransaction", lookup(parent(101, 2)))+"]")
	want := `[{"result":{"txid":"` + parent(101, 1) + `","vout":[{"value":1.5}]},"error":null,"id":10},{"result":{"txid":"` + parent(101, 2) + `","vout":[{"value":1.5}]},"error":null,"id":11}]`
	if batch != want {
		t.Fatalf("batch\n got %s\nwant %s", batch, want)
	}
	for i, n := range []int{2, 3, 2} { // Asked for twice: still answered.
		if got := ltc(t, s, call(20+i, "getrawtransaction", lookup(parent(101, n)))); got != fmt.Sprintf(`{"result":{"txid":%q,"vout":[{"value":1.5}]},"error":null,"id":%d}`, parent(101, n), 20+i) {
			t.Fatalf("lookup %q", got)
		}
	}
	// The two per-block fee calls are held once each; a repeat goes to the provider.
	for i := 0; i < 2; i++ {
		if got := ltc(t, s, call(30+i, "getblockstats", `["`+blockHash(101)+`"]`)); got != fmt.Sprintf(`{"result":"forwarded getblockstats","error":null,"id":%d}`, 30+i) {
			t.Fatalf("getblockstats %q", got)
		}
		if got := ltc(t, s, call(40+i, "getnetworkinfo", "")); got != fmt.Sprintf(`{"result":"forwarded getnetworkinfo","error":null,"id":%d}`, 40+i) {
			t.Fatalf("getnetworkinfo %q", got)
		}
	}
	if a, b, c, d := p.count("getblockstats", `["`+blockHash(101)+`"]`), p.count("getnetworkinfo", "[]"), p.count("getnetworkinfo", ""), p.count("getblockstats", `["`+blockHash(100)+`"]`); a != 2 || b != 1 || c != 2 || d != 0 {
		t.Fatalf("fee calls at the provider: stats %d, netinfo early %d, netinfo forwarded %d, stats for the old block %d; want 2, 1, 2 (teaching call and the repeat), 0", a, b, c, d)
	}
	before += 2 // The two forwarded repeats.
	if after := p.total("getblockcount"); after != before {
		t.Fatalf("the scanner's block work reached the provider: %d calls became %d", before, after)
	}
	for _, q := range [][2]string{{"getblockhash", "[101]"}, {"getblock", `["` + blockHash(101) + `",2]`}, {"getrawtransaction", lookup(parent(101, 1))}, {"getrawtransaction", lookup(parent(101, 2))}, {"getrawtransaction", lookup(parent(101, 3))}} {
		if n := p.count(q[0], q[1]); n != 1 {
			t.Errorf("provider saw %s %s %d times, want once", q[0], q[1], n)
		}
	}
}

func TestUTXOAheadBatchSendsOnlyWhatItDoesNotHold(t *testing.T) {
	p := newBitcoind(t, 100)
	_, s := utxoRouter(t, p, 200)
	advance(t, p, s, 101)
	p.mu.Lock()
	p.batches = nil
	p.mu.Unlock()

	unknown, notATx := parent(999, 1), strings.Repeat("b", 64)
	reply := ltc(t, s, "["+strings.Join([]string{
		call(1, "getrawtransaction", lookup(unknown)),
		call(2, "getrawtransaction", lookup(parent(101, 1))),
		call(3, "getrawtransaction", lookup(notATx)),
		call(4, "getblockcount", ""),
		call(5, "getrawtransaction", lookup(parent(101, 2))),
	}, ",")+"]")
	want := `[{"result":{"txid":"` + unknown + `","vout":[{"value":1.5}]},"error":null,"id":1},` +
		`{"result":{"txid":"` + parent(101, 1) + `","vout":[{"value":1.5}]},"error":null,"id":2},` +
		`{"result":null,"error":{"code":-5,"message":"No such mempool or blockchain transaction"},"id":3},` +
		`{"result":101,"error":null,"id":4},` +
		`{"result":{"txid":"` + parent(101, 2) + `","vout":[{"value":1.5}]},"error":null,"id":5}]`
	if reply != want {
		t.Fatalf("merged batch\n got %s\nwant %s", reply, want)
	}
	p.mu.Lock()
	batches := fmt.Sprint(p.batches)
	p.mu.Unlock()
	if batches != "[[1 3 4]]" {
		t.Fatalf("provider batches %s, want one batch of the three elements not held, under their own ids", batches)
	}
}

func TestUTXOAheadForwardsBatchesItCannotTakeApart(t *testing.T) {
	p := newBitcoind(t, 100)
	_, s := utxoRouter(t, p, 200)
	advance(t, p, s, 101)
	held := call(1, "getrawtransaction", lookup(parent(101, 1)))
	for name, body := range map[string]string{
		"duplicate ids":   "[" + held + "," + held + "]",
		"nothing held":    "[" + call(1, "getrawtransaction", lookup(parent(999, 1))) + "," + call(2, "getblockcount", "") + "]",
		"not a call":      "[" + held + `,{"jsonrpc":"2.0","method":"ping"}]`,
		"unknown version": "[" + held + `,{"jsonrpc":"3.0","id":2,"method":"getblockcount"}]`,
	} {
		p.mu.Lock()
		p.batches = nil
		p.mu.Unlock()
		reply := ltc(t, s, body)
		p.mu.Lock()
		n, size := len(p.batches), 0
		if n == 1 {
			size = len(p.batches[0])
		}
		p.mu.Unlock()
		if n != 1 || size != 2 || !strings.HasPrefix(reply, "[") {
			t.Errorf("%s: provider saw %d batches (size %d), want the whole batch forwarded once; reply %s", name, n, size, reply)
		}
	}
}

func TestUTXOAheadNeverHandsOutAFailedEarlyCall(t *testing.T) {
	p := newBitcoind(t, 100)
	p.broken["getblock "+`["`+blockHash(101)+`",2]`] = true
	p.broken["getrawtransaction "+lookup(parent(102, 2))] = true
	_, s := utxoRouter(t, p, 200)
	teach(t, s)
	ltc(t, s, call(1, "getblockcount", ""))
	p.setTip(101)
	p.waitFor(t, "getblock", `["`+blockHash(101)+`",2]`, 1)
	if got := ltc(t, s, call(2, "getblock", `["`+blockHash(101)+`",2]`)); got != `{"result":null,"error":null,"id":2}` {
		t.Fatalf("a failed early block fetch must be forwarded, got %q", got)
	}
	if n := p.count("getblock", `["`+blockHash(101)+`",2]`); n != 2 {
		t.Fatalf("provider saw the block request %d times, want the early call and the forwarded one", n)
	}

	p.setTip(102)
	p.waitFor(t, "getrawtransaction", lookup(parent(102, 3)), 1)
	if got := ltc(t, s, call(3, "getrawtransaction", lookup(parent(102, 2)))); got != `{"result":null,"error":null,"id":3}` {
		t.Fatalf("a failed early lookup must be forwarded, got %q", got)
	}
	if n := p.count("getrawtransaction", lookup(parent(102, 2))); n != 2 {
		t.Fatalf("provider saw the failed lookup %d times, want the early call and the forwarded one", n)
	}
	if got := ltc(t, s, call(4, "getrawtransaction", lookup(parent(102, 3)))); !strings.Contains(got, parent(102, 3)) {
		t.Fatalf("the good lookup from the same batch should be held, got %q", got)
	}
	if n := p.count("getrawtransaction", lookup(parent(102, 3))); n != 1 {
		t.Fatalf("held lookup reached the provider %d times", n)
	}
}

func TestUTXOAheadLeavesEverythingElseOpaque(t *testing.T) {
	p := newBitcoind(t, 100)
	_, s := utxoRouter(t, p, 200)
	advance(t, p, s, 101)
	for _, q := range [][2]string{
		{"getblock", `["` + blockHash(101) + `",1]`},              // other verbosity
		{"getblock", `["` + blockHash(101) + `"]`},                // default verbosity
		{"getrawtransaction", `["` + parent(101, 1) + `",false]`}, // hex form
		{"getrawtransaction", `["` + parent(101, 1) + `"]`},
		{"getblockhash", `["101"]`},
		{"getblockhash", `[101.0]`},
		{"getblockcount", `[1]`},
		{"sendrawtransaction", `["00"]`},
		{"listunspent", `[0,9999999,["ltc1q"]]`},
		{"getrawmempool", `[]`},
	} {
		before := p.count(q[0], q[1])
		ltc(t, s, call(9, q[0], q[1]))
		if n := p.count(q[0], q[1]) - before; n != 1 {
			t.Errorf("%s %s reached the provider %d times, want exactly once", q[0], q[1], n)
		}
	}
	// A request on a sub-path or with a query string is not this RPC endpoint.
	before := p.total("")
	if code, _, _ := post(s.Client(), s.URL+"/ltc?x=1", call(1, "getblockcount", "")); code != 200 {
		t.Fatalf("query-string request: %d", code)
	}
	if p.count("getblockcount", "")+p.total("") == before {
		t.Fatal("query-string request was not forwarded")
	}
}

func TestUTXOAheadDropsEarlyAnswersAndStopsPollingWhenUnused(t *testing.T) {
	keep, idle := utxoKeepNS.Load(), utxoIdleNS.Load()
	utxoKeepNS.Store(int64(150 * time.Millisecond))
	utxoIdleNS.Store(int64(400 * time.Millisecond))
	t.Cleanup(func() { utxoKeepNS.Store(keep); utxoIdleNS.Store(idle) })
	p := newBitcoind(t, 100)
	_, s := utxoRouter(t, p, 200)
	advance(t, p, s, 101)
	time.Sleep(200 * time.Millisecond) // past utxoKeep
	ltc(t, s, call(2, "getblock", `["`+blockHash(101)+`",2]`))
	if n := p.count("getblock", `["`+blockHash(101)+`",2]`); n != 2 {
		t.Fatalf("an expired early block was handed out (provider saw %d calls)", n)
	}
	time.Sleep(700 * time.Millisecond) // past utxoIdle with no scanner calls
	quiet := p.count("getblockcount", "[]")
	time.Sleep(400 * time.Millisecond)
	if n := p.count("getblockcount", "[]"); n != quiet {
		t.Fatalf("the router kept polling an unused chain: %d then %d", quiet, n)
	}
	// The next scanner call starts it again.
	ltc(t, s, call(3, "getblockcount", ""))
	time.Sleep(300 * time.Millisecond)
	if n := p.count("getblockcount", "[]"); n <= quiet+1 {
		t.Fatalf("polling did not resume: %d then %d", quiet, n)
	}
}

func TestUTXOAheadSharesAnEarlyCallThatIsStillInFlight(t *testing.T) {
	p := newBitcoind(t, 100)
	_, s := utxoRouter(t, p, 200)
	ltc(t, s, call(1, "getblockcount", ""))
	p.mu.Lock()
	p.delay = 150 * time.Millisecond
	p.tip = 101
	p.mu.Unlock()
	p.waitFor(t, "getblockhash", "[101]", 1)
	// The scanner asks while the router's block call is still out.
	deadline := time.Now().Add(3 * time.Second)
	for p.count("getblock", `["`+blockHash(101)+`",2]`) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := ltc(t, s, call(2, "getblock", `["`+blockHash(101)+`",2]`)); !strings.Contains(got, `"height":101`) {
		t.Fatalf("block %q", got)
	}
	if n := p.count("getblock", `["`+blockHash(101)+`",2]`); n != 1 {
		t.Fatalf("provider saw the block request %d times, want the one early call shared", n)
	}
}

func TestUTXOAheadConfig(t *testing.T) {
	p := newBitcoind(t, 100)
	for _, tc := range []struct {
		utxo, head int
		ok         bool
	}{{0, 0, true}, {200, 0, true}, {5000, 0, true}, {199, 0, false}, {5001, 0, false}, {-1, 0, false}, {1000, 1000, false}} {
		c, file := fixtureConfig(t, map[string]map[string]string{"ltc": {"normal": p.URL}})
		ch := c.Chains["ltc"]
		ch.UTXOAheadMS, ch.HeadCacheMS = tc.utxo, tc.head
		c.Chains["ltc"] = ch
		writeJSON(t, file, c)
		r, err := newRouter(file, true)
		if (err == nil) != tc.ok {
			t.Errorf("utxo_ahead_ms=%d head_cache_ms=%d: err=%v, want ok=%v", tc.utxo, tc.head, err, tc.ok)
		}
		if r != nil {
			r.close()
		}
	}
	// It can be turned on, tuned and turned off by reload.
	on, off := Config{Chains: map[string]Chain{"ltc": {UTXOAheadMS: 1000}}}, Config{Chains: map[string]Chain{"ltc": {}}}
	if !compatible(off, on) || !compatible(on, off) {
		t.Error("utxo_ahead_ms must be reloadable")
	}
}

// Zebra rejects the boolean lookup form and has no getblockstats, and Bifrost's
// Zcash client sends no lookup batches. The router must not invent calls the
// scanner does not make: it only makes an early call in a form it has seen.
func TestUTXOAheadOnlyMakesEarlyCallsTheScannerHasUsed(t *testing.T) {
	p := newBitcoind(t, 100)
	p.zebra = true
	_, s := utxoRouter(t, p, 200)
	ltc(t, s, call(1, "getblockcount", ""))
	p.setTip(101)
	p.waitFor(t, "getblock", `["`+blockHash(101)+`",2]`, 1)
	time.Sleep(100 * time.Millisecond)
	if n := p.total("getblock"); n != 0 { // "getblock" also covers getblockcount, getblockhash and getblockstats.
		t.Fatalf("with nothing learned the router made %d calls beyond tip, hash and block", n)
	}
	// The block itself is still held.
	if got := ltc(t, s, call(2, "getblock", `["`+blockHash(101)+`",2]`)); !strings.Contains(got, `"height":101`) || p.count("getblock", `["`+blockHash(101)+`",2]`) != 1 {
		t.Fatalf("block %q", got)
	}

	// The scanner calls getnetworkinfo and makes single lookups in Zebra's form.
	ltc(t, s, call(3, "getnetworkinfo", "[]"))
	ltc(t, s, call(4, "getrawtransaction", lookup1(parent(101, 1))))
	p.setTip(102)
	p.waitFor(t, "getnetworkinfo", "[]", 2) // The scanner's call, then the router's early one.
	time.Sleep(100 * time.Millisecond)
	if got := ltc(t, s, call(5, "getnetworkinfo", "[]")); !strings.Contains(got, "forwarded getnetworkinfo") || p.count("getnetworkinfo", "[]") != 2 {
		t.Fatalf("getnetworkinfo was not held after being learned: %q, provider calls %d", got, p.count("getnetworkinfo", "[]"))
	}
	if a, b, c := p.count("getblockstats", `["`+blockHash(102)+`"]`), p.count("getrawtransaction", lookup(parent(102, 1))), p.count("getrawtransaction", lookup1(parent(102, 1))); a != 0 || b != 0 || c != 0 {
		t.Fatalf("calls the scanner never batched or made: stats %d, lookup(true) %d, lookup(1) %d", a, b, c)
	}

	// A lookup batch in Zebra's form teaches the form; lookups then come early in it.
	ltc(t, s, "["+call(6, "getrawtransaction", lookup1(parent(102, 1)))+"]")
	p.setTip(103)
	p.waitFor(t, "getrawtransaction", lookup1(parent(103, 3)), 1)
	if got := ltc(t, s, call(7, "getrawtransaction", lookup1(parent(103, 2)))); !strings.Contains(got, parent(103, 2)) || p.count("getrawtransaction", lookup1(parent(103, 2))) != 1 {
		t.Fatalf("lookup in the learned form was not held: %q", got)
	}
	// The other form is a different call: it goes to the provider and gets its error.
	if got := ltc(t, s, call(8, "getrawtransaction", lookup(parent(103, 2)))); !strings.Contains(got, "Invalid params") {
		t.Fatalf("a boolean-form lookup must be forwarded, got %q", got)
	}
	if n := p.count("getrawtransaction", lookup(parent(103, 1))); n != 0 {
		t.Fatalf("the router made %d early lookups in the form Zebra rejects", n)
	}
}
