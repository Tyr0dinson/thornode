package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type batchReply []struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
}

func batchResult(t *testing.T, reply string) batchReply {
	t.Helper()
	var out batchReply
	if err := json.Unmarshal([]byte(reply), &out); err != nil {
		t.Fatalf("not a batch reply: %v: %s", err, reply)
	}
	return out
}

func (p *headProvider) batches() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, b := range p.bodies {
		if strings.HasPrefix(strings.TrimSpace(b), "[") {
			n++
		}
	}
	return n
}

// Recent Bifrost releases read the tip header and the header it is scanning in one
// batch. Both are answered as the same single calls would be.
func TestHeaderBatchIsAnsweredFromTheHeadCache(t *testing.T) {
	p := newHeadProvider(t)
	r, s := headRouter(t, time.Second, map[string]string{"normal": p.URL})
	mustPost(t, s, rpcBody("1", "eth_getHeaderByNumber", `["latest"]`)) // head 0x1 is now cached

	batch := "[" + rpcBody("7", "eth_getHeaderByNumber", `["latest"]`) + "," + rpcBody(`"h"`, "eth_getHeaderByNumber", `["0x1"]`) + "]"
	got := batchResult(t, mustPostReply(t, s, batch))
	if len(got) != 2 || string(got[0].ID) != "7" || string(got[1].ID) != `"h"` {
		t.Fatalf("batch reply ids or length: %+v", got)
	}
	for i, e := range got {
		if !strings.Contains(string(e.Result), `"number":"0x1"`) {
			t.Fatalf("element %d: %s", i, e.Result)
		}
	}
	if p.calls() != 1 || p.batches() != 0 {
		t.Fatalf("provider saw %d calls (%d batches), want only the first head query", p.calls(), p.batches())
	}
	if n := metricSum(t, r, "rpc_router_header_batches_total", `chain="base"`); n != 1 {
		t.Fatalf("header batches metric = %v, want 1", n)
	}
}

// What the caches do not hold is fetched as the single call Bifrost itself
// falls back to, never as a batch.
func TestHeaderBatchFetchesWhatIsNotHeldAsSingleCalls(t *testing.T) {
	p := newHeadProvider(t)
	_, s := headRouter(t, time.Second, map[string]string{"normal": p.URL})
	batch := "[" + rpcBody("1", "eth_getHeaderByNumber", `["latest"]`) + "," + rpcBody("2", "eth_getHeaderByNumber", `["0x99"]`) + "," + rpcBody("3", "eth_getBlockByNumber", `["0x98",false]`) + "]"
	got := batchResult(t, mustPostReply(t, s, batch))
	if len(got) != 3 || string(got[0].ID) != "1" || string(got[1].ID) != "2" || string(got[2].ID) != "3" {
		t.Fatalf("batch reply: %+v", got)
	}
	if p.calls() != 3 || p.batches() != 0 {
		t.Fatalf("provider saw %d calls (%d batches), want 3 single calls", p.calls(), p.batches())
	}
	p.mu.Lock()
	sent := strings.Join(p.bodies, "\n")
	p.mu.Unlock()
	for _, want := range []string{`"method":"eth_getHeaderByNumber","params":["latest"]`, `"method":"eth_getHeaderByNumber","params":["0x99"]`, `"method":"eth_getBlockByNumber","params":["0x98",false]`} {
		if !strings.Contains(sent, want) {
			t.Fatalf("provider was not sent %s; got:\n%s", want, sent)
		}
	}
}

// Anything else in a batch, and any element the provider did not answer
// plainly, leaves the whole batch to the provider.
func TestOtherBatchesAreForwardedUntouched(t *testing.T) {
	p := newHeadProvider(t)
	r, s := headRouter(t, time.Second, map[string]string{"normal": p.URL})
	latest := rpcBody("1", "eth_getHeaderByNumber", `["latest"]`)
	for name, batch := range map[string]string{
		"other call":   "[" + latest + "," + rpcBody("2", "eth_getBalance", `["0x00000000000000000000000000000000000000aa","latest"]`) + "]",
		"repeated id":  "[" + latest + "," + rpcBody("1", "eth_getHeaderByNumber", `["0x5"]`) + "]",
		"one element":  "[" + latest + "]",
		"block number": "[" + latest + "," + rpcBody("2", "eth_blockNumber", `[]`) + "]",
		"full block":   "[" + latest + "," + rpcBody("2", "eth_getBlockByNumber", `["0x5",true]`) + "]",
		"notification": "[" + latest + `,{"jsonrpc":"2.0","method":"eth_getHeaderByNumber","params":["0x5"]}]`,
	} {
		before := p.batches()
		reply := mustPostReply(t, s, batch)
		if !strings.Contains(reply, `"echo"`) || p.batches() != before+1 {
			t.Fatalf("%s: not forwarded as sent: %s", name, reply)
		}
	}
	p.fail.Store("null") // The provider has no such header: its own batch reply must reach the caller.
	before := p.batches()
	if reply := mustPostReply(t, s, "["+latest+","+rpcBody("2", "eth_getHeaderByNumber", `["0x77"]`)+"]"); !strings.Contains(reply, `"echo"`) || p.batches() != before+1 {
		t.Fatalf("batch with a missing header was answered here: %s", reply)
	}
	if n := metricSum(t, r, "rpc_router_header_batches_total", `chain="base"`); n != 0 {
		t.Fatalf("header batches metric = %v, want 0", n)
	}
}

func TestHeaderBatchIsOffWithoutTheHeadCache(t *testing.T) {
	p := newHeadProvider(t)
	_, s := headRouter(t, 0, map[string]string{"normal": p.URL})
	batch := "[" + rpcBody("1", "eth_getHeaderByNumber", `["latest"]`) + "," + rpcBody("2", "eth_getHeaderByNumber", `["0x1"]`) + "]"
	if reply := mustPostReply(t, s, batch); !strings.Contains(reply, `"echo"`) {
		t.Fatalf("batch answered with the head cache off: %s", reply)
	}
}

// While a vault block is held back, the tip header inside a batch is the held
// head too, so the scanner's two views of the chain agree.
func TestHeaderBatchSeesTheHeldHead(t *testing.T) {
	p := newEVMHold(t, 100)
	s := holdRouterFor(t, p, 3)
	teachVaults(t, s)
	holdReady(t, p, s)
	p.set(func() { p.txs[101] = transfer(holdOther, holdVaultA); p.head = 102 })
	stays(t, s, `"0x64"`, 200*time.Millisecond)
	batch := "[" + rpcBody("1", "eth_getHeaderByNumber", `["latest"]`) + "," + rpcBody("2", "eth_getHeaderByNumber", `["0x64"]`) + "]"
	got := batchResult(t, mustPostReply(t, s, batch))
	if len(got) != 2 || !strings.Contains(string(got[0].Result), `"number":"0x64"`) || !strings.Contains(string(got[1].Result), `"number":"0x64"`) {
		t.Fatalf("batch while held: %+v", got)
	}
}

// Recent Bifrost releases skip receipts for most blocks. Once it has stopped asking,
// the router stops fetching them early, and starts again if it resumes.
func TestReadAheadFetchesReceiptsOnlyWhileTheScannerAsksForThem(t *testing.T) {
	p := newChainProvider(t, 0x1000)
	_, s := readAheadRouter(t, p, 2)
	mustPost(t, s, rpcBody("1", "eth_getBlockByNumber", safeQuery))
	block := func(n int) { mustPost(t, s, rpcBody("2", "eth_getBlockByNumber", fmt.Sprintf(`["0x%x",true]`, n))) }
	receipts := func(n int) string { return fmt.Sprintf(`["0x%x"]`, n) }

	block(0x100)
	p.waitFor(t, "eth_getBlockReceipts", receipts(0x100), 1) // Before it knows better, receipts are fetched early.
	for n := 0x101; n < 0x100+2*receiptWindow; n++ {
		block(n)
	}
	time.Sleep(100 * time.Millisecond)
	last := 0x100 + 2*receiptWindow - 1
	if n := p.count("eth_getBlockReceipts", receipts(last)) + p.count("eth_getBlockReceipts", receipts(last+1)); n != 0 {
		t.Fatalf("receipts still fetched early (%d calls) after %d blocks with no receipts request", n, 2*receiptWindow)
	}
	p.waitFor(t, "eth_getBlockByNumber", fmt.Sprintf(`["0x%x",true]`, last+2), 1) // Blocks are still fetched early.

	// An occasional receipts request, as for a block with a vault transaction, changes nothing.
	mustPost(t, s, rpcBody("3", "eth_getBlockReceipts", receipts(last)))
	block(last + 1)
	time.Sleep(100 * time.Millisecond)
	if n := p.count("eth_getBlockReceipts", receipts(last+2)); n != 0 {
		t.Fatalf("one receipts request turned early receipts back on (%d calls)", n)
	}

	// A scanner that asks for every block's receipts gets them early again.
	for n := 0x300; n < 0x300+receiptWindow; n++ {
		block(n)
		mustPost(t, s, rpcBody("3", "eth_getBlockReceipts", receipts(n)))
	}
	block(0x300 + receiptWindow)
	p.waitFor(t, "eth_getBlockReceipts", receipts(0x300+receiptWindow+1), 1)
}

func TestTipAheadFetchesReceiptsOnlyWhileTheScannerAsksForThem(t *testing.T) {
	p := newEVMTip(t, 100)
	s := tipRouter(t, p, 100, true)
	mustPost(t, s, rpcBody("1", "eth_getBlockByNumber", latestQuery))
	mustPost(t, s, rpcBody("2", "eth_getBlockReceipts", `["0x64"]`)) // The scanner's form: by number.
	p.set(func() { p.head = 101 })
	p.waitFor(t, "eth_getBlockReceipts", `["0x65"]`, 1)
	for n := uint64(1); n <= receiptWindow; n++ { // It then scans a window of blocks without asking for receipts.
		mustPost(t, s, rpcBody("3", "eth_getBlockByNumber", full(n)))
	}
	p.set(func() { p.head = 102 })
	p.waitFor(t, "eth_getBlockByNumber", full(102), 1)
	time.Sleep(100 * time.Millisecond)
	if n := p.count("eth_getBlockReceipts", `["0x66"]`); n != 0 {
		t.Fatalf("receipts fetched early %d times after the scanner stopped asking", n)
	}
}
