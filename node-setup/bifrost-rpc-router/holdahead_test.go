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

const (
	holdVaultA = "0x1111111111111111111111111111111111111111"
	holdVaultB = "0x2222222222222222222222222222222222222222"
	holdRouter = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	holdToken  = "0xcccccccccccccccccccccccccccccccccccccccc"
	holdOther  = "0x9999999999999999999999999999999999999999"
)

// evmHold stands in for an EVM provider whose head the test moves and whose
// blocks carry the transactions and logs the test gives them.
type evmHold struct {
	*httptest.Server
	mu   sync.Mutex
	head uint64
	txs  map[uint64]string // JSON objects for the block's transactions
	logs map[uint64]string // JSON objects for the block's receipts
	seen map[string]int
	null map[string]bool
}

func newEVMHold(t *testing.T, head uint64) *evmHold {
	p := &evmHold{head: head, txs: map[uint64]string{}, logs: map[uint64]string{}, seen: map[string]int{}, null: map[string]bool{}}
	hash := func(n uint64) string { return fmt.Sprintf("0x%064x", n+0xb10c0000) }
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
		head := p.head
		number := head
		if n, ok := parseTag(first); ok {
			number = n
		}
		header := fmt.Sprintf(`"number":"0x%x","hash":%q,"parentHash":%q`, number, hash(number), hash(number-1))
		result := "null"
		switch {
		case p.null[key] || number > head:
		case q.Method == "eth_blockNumber":
			result = fmt.Sprintf(`"0x%x"`, head)
		case q.Method == "eth_getBlockByNumber" && len(params) == 2 && params[1] == true:
			result = "{" + header + `,"transactions":[` + p.txs[number] + `]}`
		case q.Method == "eth_getBlockByNumber":
			result = "{" + header + "}"
		case q.Method == "eth_getHeaderByNumber":
			result = "{" + header + `,"bare":true}`
		case q.Method == "eth_getBlockReceipts":
			if strings.HasPrefix(first, "0x") && len(first) == 66 {
				for n := head; n+64 > head; n-- {
					if hash(n) == first {
						number = n
					}
				}
			}
			result = "[" + p.logs[number] + "]"
		case q.Method == "eth_call" || q.Method == "eth_getBalance":
			result = `"0x01"`
		}
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, q.ID, result)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *evmHold) set(fn func()) {
	p.mu.Lock()
	fn()
	p.mu.Unlock()
}

func (p *evmHold) count(method, params string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[method+" "+params]
}

func holdRouterFor(t *testing.T, p *evmHold, hold int) *httptest.Server {
	t.Helper()
	// Stop the router's head poller soon after the test: left running against a
	// closed provider it redials every few milliseconds for a minute.
	idle := tipIdleNS.Swap(int64(time.Second))
	t.Cleanup(func() { tipIdleNS.Store(idle) })
	c, file := fixtureConfig(t, map[string]map[string]string{"base": {"normal": p.URL}})
	ch := c.Chains["base"]
	ch.HeadCacheMS, ch.TipAhead, ch.CallAhead, ch.HoldBlocks = 60, true, true, hold
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

// teachVaults makes the solvency run a scanner makes, twice, so the router knows the
// vault addresses (the gas-balance queries) and the router contract (the
// allowance calls made from a vault).
func teachVaults(t *testing.T, s *httptest.Server) {
	t.Helper()
	for _, tag := range []string{"0x50", "0x5a"} {
		for _, vault := range []string{holdVaultA, holdVaultB} {
			call := fmt.Sprintf(`[{"from":%q,"input":"0x03b6a673000000000000000000000000%s000000000000000000000000%s","to":%q},%q]`,
				vault, vault[2:], holdToken[2:], holdRouter, tag)
			mustPost(t, s, rpcBody("1", "eth_call", call))
			mustPost(t, s, rpcBody("1", "eth_getBalance", fmt.Sprintf(`[%q,%q]`, vault, tag)))
		}
	}
}

func headNumber(t *testing.T, s *httptest.Server) string {
	t.Helper()
	_, got := result(t, mustPostReply(t, s, rpcBody("1", "eth_blockNumber", "[]")))
	return got
}

func waitHead(t *testing.T, s *httptest.Server, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := headNumber(t, s)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("head is %s, want %s", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// holdReady waits until the router has polled the head itself, which is when
// it starts deciding what the scanner sees.
func holdReady(t *testing.T, p *evmHold, s *httptest.Server) {
	t.Helper()
	waitHead(t, s, `"0x64"`)
	deadline := time.Now().Add(3 * time.Second)
	for p.count("eth_getBlockByNumber", latestQuery) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the router never polled the head")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
}

// stays fails if the visible head leaves want during d.
func stays(t *testing.T, s *httptest.Server, want string, d time.Duration) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(15 * time.Millisecond) {
		if got := headNumber(t, s); got != want {
			t.Fatalf("head moved to %s while it should be held at %s", got, want)
		}
	}
}

func transfer(from, to string) string {
	return fmt.Sprintf(`{"hash":"0x%064x","from":%q,"to":%q,"input":"0x"}`, 7, from, to)
}

func TestHoldIsOffWithoutTheOption(t *testing.T) {
	p := newEVMHold(t, 100)
	s := holdRouterFor(t, p, 0)
	teachVaults(t, s)
	holdReady(t, p, s)
	p.set(func() { p.txs[101] = transfer(holdOther, holdVaultA); p.head = 101 })
	waitHead(t, s, `"0x65"`) // A block paying a vault is shown as soon as it is seen.
}

func TestHoldShowsBlocksWithNothingForTheVaultsAtOnce(t *testing.T) {
	p := newEVMHold(t, 100)
	s := holdRouterFor(t, p, 3)
	teachVaults(t, s)
	holdReady(t, p, s)
	// A token transfer between strangers is not a vault transaction.
	p.set(func() { p.txs[101] = transfer(holdOther, holdToken); p.head = 101 })
	waitHead(t, s, `"0x65"`)
	p.set(func() { p.head = 102 })
	waitHead(t, s, `"0x66"`)
}

func TestHoldKeepsAVaultBlockBackUntilThreeMoreExist(t *testing.T) {
	p := newEVMHold(t, 100)
	s := holdRouterFor(t, p, 3)
	teachVaults(t, s)
	holdReady(t, p, s)

	p.set(func() { p.txs[101] = transfer(holdOther, holdVaultA); p.head = 101 })
	stays(t, s, `"0x64"`, 250*time.Millisecond)
	p.set(func() { p.head = 103 })
	stays(t, s, `"0x64"`, 250*time.Millisecond)
	// The header the scanner is given is the held one too.
	_, header := result(t, mustPostReply(t, s, rpcBody("2", "eth_getBlockByNumber", latestQuery)))
	if !strings.Contains(header, `"number":"0x64"`) {
		t.Fatalf("latest header while held: %s", header)
	}
	// So is the bare header, the form Bifrost's scanner polls with.
	_, bare := result(t, mustPostReply(t, s, rpcBody("2", "eth_getHeaderByNumber", `["latest"]`)))
	if !strings.Contains(bare, `"number":"0x64"`) || !strings.Contains(bare, `"bare":true`) {
		t.Fatalf("latest bare header while held: %s", bare)
	}

	p.set(func() { p.head = 104 })
	waitHead(t, s, `"0x68"`)
	_, bare = result(t, mustPostReply(t, s, rpcBody("2", "eth_getHeaderByNumber", `["latest"]`)))
	if !strings.Contains(bare, `"number":"0x68"`) || !strings.Contains(bare, `"bare":true`) {
		t.Fatalf("latest bare header after release: %s", bare)
	}
	// Its follow-up query for that header by height is the provider's reply to
	// that exact call, made once by the router.
	_, byHeight := result(t, mustPostReply(t, s, rpcBody("2", "eth_getHeaderByNumber", `["0x68"]`)))
	if !strings.Contains(byHeight, `"number":"0x68"`) || p.count("eth_getHeaderByNumber", `["0x68"]`) != 1 {
		t.Fatalf("bare header by height: %s, provider calls %d", byHeight, p.count("eth_getHeaderByNumber", `["0x68"]`))
	}
	// The scanner now finds all four blocks already fetched, each the
	// provider's own reply, with no further provider call.
	for n := uint64(101); n <= 104; n++ {
		_, got := result(t, mustPostReply(t, s, rpcBody("3", "eth_getBlockByNumber", full(n))))
		if !strings.Contains(got, fmt.Sprintf(`"number":"0x%x"`, n)) {
			t.Fatalf("block %d: %s", n, got)
		}
		if c := p.count("eth_getBlockByNumber", full(n)); c != 1 {
			t.Fatalf("block %d reached the provider %d times, want once (the early call)", n, c)
		}
	}
}

func TestHoldTreatsARouterLogAndAVaultInCalldataAsVaultBlocks(t *testing.T) {
	for name, set := range map[string]func(p *evmHold){
		"router log": func(p *evmHold) {
			p.logs[101] = fmt.Sprintf(`{"logs":[{"address":%q,"topics":[]}]}`, holdRouter)
		},
		"vault in calldata": func(p *evmHold) {
			p.txs[101] = fmt.Sprintf(`{"hash":"0x01","from":%q,"to":%q,"input":"0xa9059cbb000000000000000000000000%s"}`, holdOther, holdToken, holdVaultB[2:])
		},
		"sent by a vault": func(p *evmHold) { p.txs[101] = transfer(holdVaultB, holdOther) },
	} {
		t.Run(name, func(t *testing.T) {
			p := newEVMHold(t, 100)
			s := holdRouterFor(t, p, 3)
			teachVaults(t, s)
			// The scanner asks for receipts by number, so the router does too.
			mustPost(t, s, rpcBody("1", "eth_getBlockReceipts", `["0x64"]`))
			holdReady(t, p, s)
			p.set(func() { set(p); p.head = 102 })
			stays(t, s, `"0x64"`, 250*time.Millisecond)
			p.set(func() { p.head = 104 })
			waitHead(t, s, `"0x68"`)
		})
	}
}

// A load-balanced provider answers some polls from a node a block behind. The
// hold must survive that, or it would never complete on a real provider.
func TestHoldSurvivesAPollThatComesBackABlockBehind(t *testing.T) {
	p := newEVMHold(t, 100)
	s := holdRouterFor(t, p, 3)
	teachVaults(t, s)
	holdReady(t, p, s)
	p.set(func() { p.txs[101] = transfer(holdOther, holdVaultA); p.head = 102 })
	stays(t, s, `"0x64"`, 150*time.Millisecond)
	for i := 0; i < 3; i++ {
		p.set(func() { p.head = 101 })
		time.Sleep(70 * time.Millisecond)
		p.set(func() { p.head = 103 })
		time.Sleep(70 * time.Millisecond)
	}
	if got := headNumber(t, s); got != `"0x64"` {
		t.Fatalf("head moved to %s after polls that came back behind", got)
	}
	p.set(func() { p.head = 104 })
	waitHead(t, s, `"0x68"`)
}

func TestHoldIsExtendedByALaterVaultBlock(t *testing.T) {
	p := newEVMHold(t, 100)
	s := holdRouterFor(t, p, 3)
	teachVaults(t, s)
	holdReady(t, p, s)
	p.set(func() {
		p.txs[101] = transfer(holdOther, holdVaultA)
		p.txs[103] = transfer(holdOther, holdVaultB)
		p.head = 104
	})
	stays(t, s, `"0x64"`, 250*time.Millisecond) // 104 is only one past 103.
	p.set(func() { p.head = 106 })
	waitHead(t, s, `"0x6a"`)
}

func TestHoldGivesUpAfterItsLimit(t *testing.T) {
	old := holdMaxNS.Swap(int64(300 * time.Millisecond))
	t.Cleanup(func() { holdMaxNS.Store(old) })
	p := newEVMHold(t, 100)
	s := holdRouterFor(t, p, 3)
	teachVaults(t, s)
	holdReady(t, p, s)
	// The chain stops right after a vault block: the block is shown anyway.
	p.set(func() { p.txs[101] = transfer(holdOther, holdVaultA); p.head = 101 })
	stays(t, s, `"0x64"`, 150*time.Millisecond)
	waitHead(t, s, `"0x65"`)
}

func TestHoldShowsABlockItCouldNotInspect(t *testing.T) {
	old := holdInspectNS.Swap(int64(200 * time.Millisecond))
	t.Cleanup(func() { holdInspectNS.Store(old) })
	p := newEVMHold(t, 100)
	s := holdRouterFor(t, p, 3)
	teachVaults(t, s)
	holdReady(t, p, s)
	p.set(func() { p.null["eth_getBlockByNumber "+full(101)] = true; p.head = 101 })
	waitHead(t, s, `"0x65"`)
}

func TestHoldDoesNothingBeforeTheVaultsAreKnown(t *testing.T) {
	p := newEVMHold(t, 100)
	s := holdRouterFor(t, p, 3)
	holdReady(t, p, s) // No solvency run has passed through.
	p.set(func() { p.txs[101] = transfer(holdOther, holdVaultA); p.head = 101 })
	waitHead(t, s, `"0x65"`)
}

func TestHoldIgnoresPinnedCallsThatAreNotASolvencyRun(t *testing.T) {
	p := newEVMHold(t, 100)
	s := holdRouterFor(t, p, 3)
	// Token metadata calls pinned to a height: no balance query, so no vault and
	// no router contract is learned from them.
	for _, tag := range []string{"0x50", "0x5a"} {
		for i := 0; i < 5; i++ {
			mustPost(t, s, rpcBody("1", "eth_call", fmt.Sprintf(`[{"input":"0x313ce56%d","to":%q},%q]`, i, holdToken, tag)))
		}
	}
	holdReady(t, p, s)
	p.set(func() { p.txs[101] = transfer(holdOther, holdToken); p.head = 101 })
	waitHead(t, s, `"0x65"`)
}

func TestHoldConfig(t *testing.T) {
	for name, tc := range map[string]struct {
		tip, calls bool
		hold       int
		valid      bool
	}{
		"on":                {true, true, 3, true},
		"needs tip_ahead":   {false, true, 3, false},
		"needs call_ahead":  {true, false, 3, false},
		"negative":          {true, true, -1, false},
		"too many":          {true, true, 9, false},
		"off needs nothing": {false, false, 0, true},
	} {
		t.Run(name, func(t *testing.T) {
			c, file := fixtureConfig(t, map[string]map[string]string{"base": {"normal": "https://example.invalid"}})
			ch := c.Chains["base"]
			ch.HeadCacheMS, ch.TipAhead, ch.CallAhead, ch.HoldBlocks = 200, tc.tip, tc.calls, tc.hold
			c.Chains["base"] = ch
			writeJSON(t, file, c)
			if _, err := newRouter(file, true); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}
