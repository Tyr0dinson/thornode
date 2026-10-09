package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"text/template"

	"cosmossdk.io/math"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

// closedPortCov returns a localhost port with nothing listening on it.
func closedPortCov(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := portCov(t, l.Addr())
	_ = l.Close()
	return port
}

func serverPortCov(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func setNativeTxIDsCov(t *testing.T, routine int, ids []string) {
	t.Helper()
	nativeTxIDsMu.Lock()
	nativeTxIDs[routine] = ids
	nativeTxIDsMu.Unlock()
	t.Cleanup(func() {
		nativeTxIDsMu.Lock()
		delete(nativeTxIDs, routine)
		nativeTxIDsMu.Unlock()
	})
}

func getNativeTxIDsCov(routine int) []string {
	nativeTxIDsMu.Lock()
	defer nativeTxIDsMu.Unlock()
	return append([]string{}, nativeTxIDs[routine]...)
}

func requireJQCov(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// NewOperation
////////////////////////////////////////////////////////////////////////////////////////

func TestNewOperationErrorsCov(t *testing.T) {
	if _, err := NewOperation(map[string]any{"type": 5}); err == nil || !strings.Contains(err.Error(), "not a string") {
		t.Fatalf("expected non-string type error, got %v", err)
	}
	if _, err := NewOperation(map[string]any{"type": "nope"}); err == nil || !strings.Contains(err.Error(), "unknown operation type") {
		t.Fatalf("expected unknown type error, got %v", err)
	}
	// mapstructure decoding rejects unused keys
	if _, err := NewOperation(map[string]any{"type": "create-blocks", "bogus": 1}); err == nil || !strings.Contains(err.Error(), "failed to decode operation") {
		t.Fatalf("expected unused key error, got %v", err)
	}
	// json decoding rejects invalid values
	if _, err := NewOperation(map[string]any{"type": "tx-ban", "signer": "not-an-address"}); err == nil || !strings.Contains(err.Error(), "failed to decode operation") {
		t.Fatalf("expected json decode error, got %v", err)
	}
	// json encoding fails on unsupported values
	if _, err := NewOperation(map[string]any{"type": "tx-ban", "signer": make(chan int)}); err == nil || !strings.Contains(err.Error(), "failed to encode operation") {
		t.Fatalf("expected json encode error, got %v", err)
	}

	// weakly typed input and status default
	op, err := NewOperation(map[string]any{"type": "create-blocks", "count": "3", "exit": 2})
	if err != nil {
		t.Fatal(err)
	}
	cb := asCov[*OpCreateBlocks](t, op)
	if cb.Count != 3 || cb.Exit == nil || *cb.Exit != 2 || cb.OpType() != "create-blocks" {
		t.Fatalf("unexpected create-blocks op: %+v", cb)
	}
	op, err = NewOperation(map[string]any{"type": "check", "endpoint": "http://localhost:1317/x", "status": 404})
	if err != nil {
		t.Fatal(err)
	}
	if asCov[*OpCheck](t, op).Status != 404 {
		t.Fatal("explicit status must be kept")
	}
	op, err = NewOperation(map[string]any{"type": "check"})
	if err != nil {
		t.Fatal(err)
	}
	if asCov[*OpCheck](t, op).Status != 0 {
		t.Fatal("status must not default without endpoint")
	}
}

const allTxOpsSuiteCov = `type: tx-ban
signer: {{ addr_thor_bird }}
node_address: {{ addr_thor_dog }}
---
type: tx-errata-tx
signer: {{ addr_thor_dog }}
tx_id: "{{ observe_txid 1 }}"
chain: BTC
---
type: tx-mimir
key: FOO
value: 2500
signer: {{ addr_thor_dog }}
---
type: tx-observed-in
signer: {{ addr_thor_dog }}
txs:
  - tx:
      id: "{{ observe_txid 1 }}"
      chain: BTC
      from_address: {{ addr_btc_fox }}
      to_address: {{ addr_btc_dog }}
      coins:
        - amount: "1000000"
          asset: "BTC.BTC"
          decimals: 8
      gas:
        - amount: "10000"
          asset: "BTC.BTC"
      memo: "=:THOR.RUNE:{{ addr_thor_fox }}"
    block_height: 5
    finalise_height: 5
    observed_pub_key: {{ pubkey_dog }}
---
type: tx-observed-out
signer: {{ addr_thor_dog }}
txs:
  - tx:
      id: "{{ observe_txid 2 }}"
      chain: BTC
      from_address: {{ addr_btc_dog }}
      to_address: {{ addr_btc_fox }}
      coins:
        - amount: "1000"
          asset: "BTC.BTC"
          decimals: 8
      gas:
        - amount: "10000"
          asset: "BTC.BTC"
      memo: "OUT:{{ native_txid -1 }}"
    block_height: 5
    finalise_height: 5
    observed_pub_key: {{ pubkey_dog }}
---
type: tx-network-fee
signer: {{ addr_thor_dog }}
block_height: 1
chain: GAIA
transaction_size: 1000
transaction_rate: 10
---
type: tx-node-pause-chain
signer: {{ addr_thor_dog }}
value: 1
---
type: tx-send
from_address: {{ addr_thor_fox }}
to_address: {{ addr_thor_wolf }}
amount:
  - denom: rune
    amount: "1"
memo: hello
---
type: tx-set-ip-address
signer: {{ addr_thor_dog }}
ip_address: "10.0.0.1"
---
type: tx-set-node-keys
signer: {{ addr_thor_cat }}
pub_key_set: { "secp256k1": {{ pubkey_goat }}, "ed25519": {{ pubkey_cat }} }
validator_cons_pub_key: {{ cons_pubkey_cat }}
---
type: tx-solvency
signer: {{ addr_thor_dog }}
chain: BTC
pub_key: {{ pubkey_dog }}
coins:
  - amount: "99000000"
    asset: "BTC.BTC"
    decimals: 8
height: 1
---
type: tx-tss-keysign
signer: {{ addr_thor_dog }}
id: "9c97754842280d53bad5135b7cdcc41a0345ce1c4cacf2a29e77e98e4ff9e3fd"
pub_key: {{ pubkey_dog }}
height: 1
coins:
  - asset: "BTC.BTC"
    amount: "1"
memo: "doesn't matter"
blame:
  fail_reason: "fail to keysign"
  blame_nodes:
    - pubkey: {{ pubkey_dog }}
---
type: tx-tss-pool
signer: {{ addr_thor_dog }}
pool_pub_key: {{ pubkey_dog }}
keygen_type: "AsgardKeygen"
pub_keys:
  - {{ pubkey_dog }}
chains:
  - THOR
---
type: tx-version
signer: {{ addr_thor_fox }}
version: "0.1.2"
---
type: tx-bank-send
from_address: {{ addr_thor_fox }}
to_address: {{ addr_thor_wolf }}
amount:
  - denom: rune
    amount: "1"
---
type: tx-propose-upgrade
signer: {{ addr_thor_fox }}
name: 1.2.3
upgrade:
  height: 100
  info: "upgrade"
---
type: tx-approve-upgrade
signer: {{ addr_thor_cat }}
name: 1.2.3
---
type: tx-reject-upgrade
signer: {{ addr_thor_frog }}
name: 1.2.3
---
type: tx-deposit
signer: {{ addr_thor_cat }}
coins:
  - amount: "200000000"
    asset: "rune"
memo: "noop"
---
type: tx-execute-contract
sender: {{ addr_thor_dog }}
contract: "tthor14hj2tavq8fpesdwxxcu44rty3hh90vhujrvcmstl4zr3txmfvw9sw58u9f"
msg: { "increment": {}}
gas: 100000
fees: 100rune
---
type: tx-instantiate-contract
code_id: 1
msg: {}
label: "simple-contract"
sender: {{ addr_thor_dog }}
gas: 100000
---
type: tx-instantiate-contract-2
code_id: 1
msg: {}
label: "simple-contract"
salt: "c2FsdA=="
sender: {{ addr_thor_dog }}
---
type: tx-migrate-contract
contract: tthor14hj2tavq8fpesdwxxcu44rty3hh90vhujrvcmstl4zr3txmfvw9sw58u9f
msg: "{}"
code_id: 2
sender: {{ addr_thor_fox }}
---
type: tx-store-code
sender: {{ addr_thor_dog }}
wasm_file: "counter.wasm"
gas: 2000000
fees: 2000rune
---
type: tx-sudo-contract
contract: tthor14hj2tavq8fpesdwxxcu44rty3hh90vhujrvcmstl4zr3txmfvw9sw58u9f
msg: {}
authority: {{ addr_thor_cat }}
---
type: tx-update-admin
contract: tthor14hj2tavq8fpesdwxxcu44rty3hh90vhujrvcmstl4zr3txmfvw9sw58u9f
sender: {{ addr_thor_fox }}
new_admin: {{ addr_thor_cat }}
---
type: tx-clear-admin
contract: tthor14hj2tavq8fpesdwxxcu44rty3hh90vhujrvcmstl4zr3txmfvw9sw58u9f
sender: {{ addr_thor_fox }}
---
type: tx-price-feed
prices:
  USDC: "1.00"
`

// TestTxOperationsUnreachableNodeCov parses every tx operation type and executes
// each against a routine whose RPC port has nothing listening: every operation
// must surface an error (either from ValidateBasic or from the failed broadcast).
func TestTxOperationsUnreachableNodeCov(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suite.yaml")
	writeFileCov(t, path, allTxOpsSuiteCov)

	ops, _, _, failExport := parseOps(zerolog.New(io.Discard), path, template.Must(templates.Clone()), nil)
	if failExport {
		t.Fatal("unexpected fail export flag")
	}
	if len(ops) != 28 {
		t.Fatalf("expected 28 operations, got %d", len(ops))
	}

	t.Setenv("DEBUG", "1") // skip the 100ms log drain on each failed broadcast
	routine := closedPortCov(t) - 26657
	setNativeTxIDsCov(t, routine, []string{"NATIVE1", "NATIVE2"})

	types := map[string]bool{}
	for _, op := range ops {
		types[op.OpType()] = true
		if op.OpType() == "tx-price-feed" {
			continue // dialed on a different port, covered separately
		}
		out := &bytes.Buffer{}
		err := op.Execute(out, "suites/x.yaml", 0, routine, nil, make(chan string, 1))
		if err == nil {
			t.Fatalf("%s: expected error against unreachable node", op.OpType())
		}
	}
	if len(types) != 28 {
		t.Fatalf("expected 28 distinct operation types, got %d", len(types))
	}

	// the observed-out memo was rendered with the routine's native txids
	for _, op := range ops {
		if oo, ok := op.(*OpTxObservedOut); ok {
			if oo.Txs[0].Tx.Memo != "OUT:NATIVE2" {
				t.Fatalf("observed out memo not rendered: %s", oo.Txs[0].Tx.Memo)
			}
		}
	}
	// typed fields decoded through json
	for _, op := range ops {
		switch o := op.(type) {
		case *OpTxMimir:
			if o.Key != "FOO" || o.Value != 2500 {
				t.Fatalf("unexpected mimir op: %+v", o)
			}
		case *OpTxExecuteContract:
			if o.Fees != "100rune" || o.Gas == nil || *o.Gas != 100000 {
				t.Fatalf("unexpected execute contract op: %+v", o)
			}
		case *OpTxSend:
			if o.Memo != "hello" || len(o.Amount) != 1 {
				t.Fatalf("unexpected send op: %+v", o)
			}
		}
	}
}

func TestSimpleOperationsCov(t *testing.T) {
	if err := (&OpEnv{Key: "A", Value: "B"}).Execute(nil, "", 0, 0, nil, nil); err != nil {
		t.Fatalf("env execute should be a no-op: %v", err)
	}
	err := (&OpFailExportInvariants{}).Execute(nil, "", 0, 0, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "last operation") {
		t.Fatalf("unexpected fail-export-invariants error: %v", err)
	}

	routine := closedPortCov(t) - 26657
	out := &bytes.Buffer{}

	// ValidateBasic failure is reported with the json encoded operation
	ban := &OpTxBan{OpBase: OpBase{Type: "tx-ban"}}
	err = ban.Execute(out, "", 0, routine, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "failed to validate basic") {
		t.Fatalf("expected validate basic failure, got %v", err)
	}
	if !strings.Contains(out.String(), `"type": "tx-ban"`) {
		t.Fatalf("expected operation dump, got %s", out.String())
	}

	// invalid bank send sender
	err = (&OpTxBankSend{}).Execute(out, "", 0, routine, nil, nil)
	if err == nil {
		t.Fatal("expected bech32 error for empty bank send sender")
	}

	// solvency with invalid pubkey fails to construct
	err = (&OpTxSolvency{Chain: common.BTCChain}).Execute(out, "", 0, routine, nil, nil)
	if err == nil {
		t.Fatal("expected solvency construction error")
	}

	// store code with missing or invalid wasm files
	sender := templateAddress["addr_thor_dog"]
	sc := &OpTxStoreCode{WasmFile: "does-not-exist.wasm"}
	sc.Sender = sender
	if err = sc.Execute(out, "", 0, routine, nil, nil); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected missing wasm error, got %v", err)
	}
	sc.WasmFile = "../../regression/README.md"
	if err = sc.Execute(out, "", 0, routine, nil, nil); err == nil || !strings.Contains(err.Error(), "invalid input file") {
		t.Fatalf("expected invalid wasm error, got %v", err)
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// opFuncMap / OpState
////////////////////////////////////////////////////////////////////////////////////////

func TestOpFuncMapNativeTxIDCov(t *testing.T) {
	routine := 987654
	setNativeTxIDsCov(t, routine, []string{"A", "B", "C"})
	fn := asCov[func(int) string](t, opFuncMap(routine)["native_txid"])
	if fn(1) != "A" || fn(3) != "C" || fn(-1) != "C" || fn(-3) != "A" {
		t.Fatal("unexpected native_txid lookup")
	}
}

func TestOpStateCov(t *testing.T) {
	base := t.TempDir()
	t.Setenv("REGTEST_HOME_BASE", base)
	routine := 4
	genesisPath := filepath.Join(base, "4", ".thornode", "config", "genesis.json")
	writeFileCov(t, genesisPath, `{
  "chain_id": "thorchain",
  "app_state": {
    "bank": {"balances": [{"address": "a1", "coins": [{"denom": "rune", "amount": "1"}]}]}
  }
}`)

	op := &OpState{Genesis: map[string]any{
		"app_state": map[string]any{
			"bank": map[string]any{"balances": []any{
				map[string]any{"address": "a1", "coins": []any{map[string]any{"denom": "rune", "amount": "5"}}},
				map[string]any{"address": "a2"},
			}},
			"thorchain": map[string]any{"reserve": "100"},
		},
	}}
	if err := op.Execute(io.Discard, "", 0, routine, nil, nil); err != nil {
		t.Fatal(err)
	}

	bz, err := os.ReadFile(genesisPath)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	if err = json.Unmarshal(bz, &got); err != nil {
		t.Fatal(err)
	}
	if got["chain_id"] != "thorchain" {
		t.Fatal("existing genesis fields must be kept")
	}
	appState := mapCov(t, got["app_state"])
	balances := asCov[[]any](t, mapCov(t, appState["bank"])["balances"])
	if len(balances) != 2 {
		t.Fatalf("expected 2 balances, got %v", balances)
	}
	coins := asCov[[]any](t, mapCov(t, balances[0])["coins"])
	if len(coins) != 1 || mapCov(t, coins[0])["amount"] != "5" {
		t.Fatalf("expected coins merged on denom, got %v", coins)
	}
	if mapCov(t, appState["thorchain"])["reserve"] != "100" {
		t.Fatal("expected new thorchain state to be added")
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// OpCheck
////////////////////////////////////////////////////////////////////////////////////////

func TestOpCheckEmptyEndpointCov(t *testing.T) {
	t.Setenv("AUTO_UPDATE", "")
	op := &OpCheck{}
	op.prefetch(0)
	err := op.Execute(io.Discard, "", 0, 0, nil, nil)
	if err == nil || err.Error() != "check" {
		t.Fatalf("expected breakpoint check error, got %v", err)
	}
}

func checkServerCov(t *testing.T, handler http.HandlerFunc) (*httptest.Server, int) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, serverPortCov(t, srv) - 1317
}

func TestOpCheckExecuteCov(t *testing.T) {
	requireJQCov(t)
	t.Setenv("AUTO_UPDATE", "")
	t.Setenv("IGNORE_FAILURES", "")
	t.Setenv("DEBUG", "")

	var hits int32
	_, routine := checkServerCov(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		switch r.URL.Path {
		case "/thorchain/flaky":
			// first request is not indexed yet, the retry succeeds
			if n == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/thorchain/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		case "/thorchain/empty":
		case "/thorchain/error":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`boom`))
		default:
			_, _ = fmt.Fprintf(w, `{"asset":%q,"param":%q,"n":5}`, "BTC.BTC", r.URL.Query().Get("p"))
		}
	})
	setNativeTxIDsCov(t, routine, []string{"TXA", "TXB"})

	// passing asserts, params and native_txid rendering
	op := &OpCheck{
		Endpoint: "http://localhost:1317/thorchain/pool",
		Params:   map[string]string{"p": "{{ native_txid 1 }}"},
		Status:   200,
		Asserts: []string{
			`.asset == "BTC.BTC"`,
			`.param == "{{ native_txid 1 }}"`,
			`.n == 5`,
		},
	}
	// params are not rendered, so send the rendered value directly
	op.Params["p"] = "TXA"
	op.prefetch(routine)
	if !strings.HasSuffix(op.prefetchResp.Request.URL.Host, strconv.Itoa(routine+1317)) {
		t.Fatalf("routine offset not applied: %s", op.prefetchResp.Request.URL.Host)
	}
	out := &bytes.Buffer{}
	if err := op.Execute(out, "", 0, routine, nil, nil); err != nil {
		t.Fatalf("expected passing check: %v\n%s", err, out.String())
	}

	// 404 is retried until the endpoint is indexed
	atomic.StoreInt32(&hits, 0)
	op = &OpCheck{Endpoint: "http://localhost:1317/thorchain/flaky", Status: 200, Asserts: []string{".ok"}}
	op.prefetch(routine)
	if err := op.Execute(io.Discard, "", 0, routine, nil, nil); err != nil {
		t.Fatalf("expected retried check to pass: %v", err)
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("expected 2 requests, got %d", hits)
	}

	// expected 404 is not retried
	atomic.StoreInt32(&hits, 0)
	op = &OpCheck{Endpoint: "http://localhost:1317/thorchain/missing", Status: 404, Asserts: []string{`.error == "not found"`}}
	op.prefetch(routine)
	if err := op.Execute(io.Discard, "", 0, routine, nil, nil); err != nil {
		t.Fatalf("expected 404 check to pass: %v", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected a single request, got %d", hits)
	}

	// status mismatch
	op = &OpCheck{Endpoint: "http://localhost:1317/thorchain/error", Status: 200}
	op.prefetch(routine)
	out.Reset()
	err := op.Execute(out, "", 0, routine, nil, nil)
	if err == nil || err.Error() != "unexpected status code: 500" {
		t.Fatalf("expected status error, got %v", err)
	}
	if !strings.Contains(out.String(), "boom") {
		t.Fatalf("expected response dump, got %s", out.String())
	}

	// empty response
	logs := make(chan string, 1)
	logs <- "node log line"
	op = &OpCheck{Endpoint: "http://localhost:1317/thorchain/empty", Status: 200}
	op.prefetch(routine)
	out.Reset()
	err = op.Execute(out, "", 0, routine, nil, logs)
	if err == nil || err.Error() != "empty response" {
		t.Fatalf("expected empty response error, got %v", err)
	}
	if !strings.Contains(out.String(), "node log line") {
		t.Fatalf("expected logs to be dumped, got %s", out.String())
	}

	// failing and invalid asserts
	op = &OpCheck{
		Endpoint: "http://localhost:1317/thorchain/pool",
		Status:   200,
		Asserts:  []string{`.n == 6`, `.n ==== `},
	}
	op.prefetch(routine)
	out.Reset()
	err = op.Execute(out, "", 0, routine, nil, make(chan string, 1))
	if err == nil || err.Error() != "2 failed asserts" {
		t.Fatalf("expected 2 failed asserts, got %v", err)
	}
	if !strings.Contains(out.String(), "Failed Assert: \x1b[0m.n == 6") && !strings.Contains(out.String(), ".n == 6") {
		t.Fatalf("expected failed assert dump, got %s", out.String())
	}

	// IGNORE_FAILURES suppresses the error
	t.Setenv("IGNORE_FAILURES", "1")
	op = &OpCheck{Endpoint: "http://localhost:1317/thorchain/pool", Status: 200, Asserts: []string{`.n == 6`}}
	op.prefetch(routine)
	if err = op.Execute(io.Discard, "", 0, routine, nil, nil); err != nil {
		t.Fatalf("expected ignored failure, got %v", err)
	}

	// connection errors are reported by Execute
	t.Setenv("IGNORE_FAILURES", "")
	op = &OpCheck{Endpoint: "http://localhost:1317/x", Status: 200}
	op.prefetch(closedPortCov(t) - 1317)
	if err = op.Execute(io.Discard, "", 0, 0, nil, nil); err == nil {
		t.Fatal("expected connection error")
	}
}

func TestOpCheckAutoUpdateCov(t *testing.T) {
	requireJQCov(t)
	t.Setenv("AUTO_UPDATE", "1")
	t.Setenv("IGNORE_FAILURES", "")

	_, routine := checkServerCov(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"n":5,"s":"new","list":[1,2]}`))
	})

	suite := `type: check
endpoint: http://localhost:1317/thorchain/x
asserts:
  - .n == 4
  - .s == "old"
  - .missing == 1
  - .list|length == 1
  - .ok
`
	path := filepath.Join(t.TempDir(), "suite.yaml")
	writeFileCov(t, path, suite)

	op := &OpCheck{
		Endpoint: "http://localhost:1317/thorchain/x",
		Status:   200,
		Asserts: []string{
			`.n == 4`,
			`.s == "old"`,
			`.missing == 1`,
			`.list|length == 1`,
			`.ok`,
			`.notinfile == 7`,
			`.n ==== 1`,
		},
	}
	// AUTO_UPDATE prefetches inside Execute
	out := &bytes.Buffer{}
	if err := op.Execute(out, path, 0, routine, nil, make(chan string)); err != nil {
		t.Fatalf("auto update should not fail: %v\n%s", err, out.String())
	}

	bz, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `type: check
endpoint: http://localhost:1317/thorchain/x
asserts:
  - .n == 5
  - .s == "new"
  - .missing == 1
  - .list|length == 1
  - .ok
`
	if string(bz) != want {
		t.Fatalf("unexpected auto-updated suite:\n%s", string(bz))
	}
	for _, msg := range []string{"skipping auto update on null value", "skipping auto update on length", "failed to find assertion to update", "failed to evaluate expression"} {
		if !strings.Contains(out.String(), msg) {
			t.Fatalf("expected %q in output:\n%s", msg, out.String())
		}
	}

	// unreadable test file is returned
	op = &OpCheck{Endpoint: "http://localhost:1317/thorchain/x", Status: 200, Asserts: []string{`.n == 4`}}
	err = op.Execute(io.Discard, filepath.Join(t.TempDir(), "missing.yaml"), 0, routine, nil, nil)
	if err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected missing file error, got %v", err)
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// OpCreateBlocks / invariants
////////////////////////////////////////////////////////////////////////////////////////

type blockServerCov struct {
	height       string
	blockStatus  int
	invariantFn  func(w http.ResponseWriter, inv string)
	blockGets    int32
	invariantHit int32
}

func (b *blockServerCov) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/newBlock":
		_, _ = w.Write([]byte(b.height))
	case r.URL.Path == "/thorchain/block":
		atomic.AddInt32(&b.blockGets, 1)
		if b.blockStatus != http.StatusOK {
			w.WriteHeader(b.blockStatus)
			return
		}
		h, _ := strconv.ParseInt(r.URL.Query().Get("height"), 10, 64)
		gas := "12345"
		block := openapi.BlockResponse{
			Id: openapi.BlockResponseId{Hash: "IDHASH"},
			Header: openapi.BlockResponseHeader{
				ChainId:         "thorchain",
				Height:          h,
				Time:            "2026-01-01T00:00:00Z",
				LastBlockId:     openapi.BlockResponseId{Hash: "LASTHASH"},
				LastCommitHash:  "LASTCOMMIT",
				AppHash:         "APPHASH",
				LastResultsHash: "RESULTSHASH",
			},
			Txs: []openapi.BlockTx{{Hash: "TX", Result: openapi.BlockTxResult{GasUsed: &gas}}},
		}
		_ = json.NewEncoder(w).Encode(block)
	case strings.HasPrefix(r.URL.Path, "/thorchain/invariant/"):
		atomic.AddInt32(&b.invariantHit, 1)
		inv := strings.TrimPrefix(r.URL.Path, "/thorchain/invariant/")
		if b.invariantFn != nil {
			b.invariantFn(w, inv)
			return
		}
		_, _ = fmt.Fprintf(w, `{"invariant":%q,"broken":false,"msg":[]}`, inv)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func startBlockServerCov(t *testing.T, b http.Handler) int {
	t.Helper()
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	t.Setenv("REGTEST_CREATE_BLOCK_PORT_BASE", "1317")
	return serverPortCov(t, srv) - 1317
}

func TestOpCreateBlocksCov(t *testing.T) {
	mnt := t.TempDir()
	t.Setenv("REGTEST_MNT_DIR", mnt)
	t.Setenv("UNFILTER_EXPORT", "")
	t.Setenv("TIME_FACTOR", "")

	b := &blockServerCov{height: "7", blockStatus: http.StatusOK}
	routine := startBlockServerCov(t, b)

	logs := make(chan string, 2)
	logs <- "stale"
	op := &OpCreateBlocks{Count: 1}
	if err := op.Execute(io.Discard, "suites/grp/cb.yaml", 0, routine, nil, logs); err != nil {
		t.Fatalf("create blocks failed: %v", err)
	}
	if len(logs) != 0 {
		t.Fatal("expected stale logs to be drained")
	}
	t.Setenv("DEBUG", "1") // skip the 100ms log drain for the remaining cases
	if int(atomic.LoadInt32(&b.invariantHit)) != len(invariants) {
		t.Fatalf("expected all %d invariants to be checked, got %d", len(invariants), b.invariantHit)
	}

	blockPath := filepath.Join(mnt, "blocks", "grp", "cb", "007.json")
	readBlock := func() openapi.BlockResponse {
		t.Helper()
		bz, err := os.ReadFile(blockPath)
		if err != nil {
			t.Fatal(err)
		}
		var block openapi.BlockResponse
		if err = json.Unmarshal(bz, &block); err != nil {
			t.Fatal(err)
		}
		return block
	}
	block := readBlock()
	blank := strings.Repeat("0", 64)
	if block.Id.Hash != "" || block.Header.Time != "" || block.Header.LastBlockId.Hash != "" || block.Header.LastCommitHash != "" {
		t.Fatalf("non-deterministic fields not zeroed: %+v", block)
	}
	if block.Header.AppHash != blank || block.Header.LastResultsHash != blank || *block.Txs[0].Result.GasUsed != "0" {
		t.Fatalf("filtered fields not applied: %+v", block.Header)
	}
	if block.Header.Height != 7 {
		t.Fatalf("unexpected height %d", block.Header.Height)
	}

	// UNFILTER_EXPORT keeps app hash and gas, skip_invariants skips invariant checks
	t.Setenv("UNFILTER_EXPORT", "1")
	atomic.StoreInt32(&b.invariantHit, 0)
	op = &OpCreateBlocks{Count: 1, SkipInvariants: true}
	if err := op.Execute(io.Discard, "suites/grp/cb.yaml", 0, routine, nil, nil); err != nil {
		t.Fatal(err)
	}
	block = readBlock()
	if block.Header.AppHash != "APPHASH" || *block.Txs[0].Result.GasUsed != "12345" {
		t.Fatalf("unfiltered fields modified: %+v", block.Header)
	}
	if atomic.LoadInt32(&b.invariantHit) != 0 {
		t.Fatal("invariants must be skipped")
	}
	t.Setenv("UNFILTER_EXPORT", "")

	// exit code expected but blocks were created
	exit := 1
	op = &OpCreateBlocks{Count: 1, SkipInvariants: true, Exit: &exit}
	if err := op.Execute(io.Discard, "suites/grp/cb.yaml", 0, routine, nil, nil); err == nil || err.Error() != "expected exit code" {
		t.Fatalf("expected exit code error, got %v", err)
	}

	// unparsable height
	b.height = "not-a-number"
	op = &OpCreateBlocks{Count: 1, SkipInvariants: true}
	if err := op.Execute(io.Discard, "suites/grp/cb.yaml", 0, routine, nil, nil); err == nil {
		t.Fatal("expected height parse error")
	}
	b.height = "8"

	// block directory cannot be created (mnt is a regular file)
	mntFile := filepath.Join(t.TempDir(), "file")
	writeFileCov(t, mntFile, "x")
	t.Setenv("REGTEST_MNT_DIR", mntFile)
	if err := op.Execute(io.Discard, "suites/grp/cb.yaml", 0, routine, nil, nil); err == nil {
		t.Fatal("expected block directory error")
	}
	t.Setenv("REGTEST_MNT_DIR", mnt)

	// block endpoint never returns 200
	b.blockStatus = http.StatusInternalServerError
	atomic.StoreInt32(&b.blockGets, 0)
	if err := op.Execute(io.Discard, "suites/grp/cb.yaml", 0, routine, nil, nil); err == nil || err.Error() != "failed to get block: 500" {
		t.Fatalf("expected block status error, got %v", err)
	}
	if atomic.LoadInt32(&b.blockGets) != 5 {
		t.Fatalf("expected 5 block attempts, got %d", b.blockGets)
	}
}

func TestOpCreateBlocksExitCov(t *testing.T) {
	t.Setenv("DEBUG", "1") // skip the 100ms log drain
	t.Setenv("REGTEST_CREATE_BLOCK_PORT_BASE", "0")
	routine := closedPortCov(t)

	// unexpected failure without an exit code
	op := &OpCreateBlocks{Count: 1}
	if err := op.Execute(io.Discard, "suites/x.yaml", 0, routine, nil, nil); err == nil {
		t.Fatal("expected connection error")
	}

	startExit := func(code int) *os.Process {
		t.Helper()
		cmd := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)) //nolint:gosec // fixed test command
		if err := cmd.Start(); err != nil {
			t.Skipf("cannot start sh: %v", err)
		}
		return cmd.Process
	}

	// matching exit code
	exit := 3
	op = &OpCreateBlocks{Count: 1, Exit: &exit}
	p := startExit(3)
	if err := op.Execute(io.Discard, "suites/x.yaml", 0, routine, p, nil); err != nil {
		t.Fatalf("expected matching exit code to succeed: %v", err)
	}

	// waiting on an already reaped process fails
	if err := op.Execute(io.Discard, "suites/x.yaml", 0, routine, p, nil); err == nil {
		t.Fatal("expected wait error for reaped process")
	}

	// NOTE: a mismatched exit code is logged as "bad exit code" but the
	// operation returns the (nil) wait error, so the mismatch does not fail it.
	out := &bytes.Buffer{}
	if err := op.Execute(out, "suites/x.yaml", 0, routine, startExit(4), nil); err != nil {
		t.Fatalf("documenting current behaviour: mismatch returns nil, got %v", err)
	}
	if !strings.Contains(out.String(), "bad exit code") {
		t.Fatalf("expected bad exit code log, got %s", out.String())
	}
}

func TestCheckInvariantsCov(t *testing.T) {
	if len(invariants) < 2 {
		t.Skip("need at least two invariants")
	}
	broken, bad := invariants[0], invariants[1]
	b := &blockServerCov{invariantFn: func(w http.ResponseWriter, inv string) {
		switch inv {
		case broken:
			_, _ = fmt.Fprintf(w, `{"invariant":%q,"broken":true,"msg":["oops"]}`, inv)
		case bad:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("panic"))
		default:
			_, _ = w.Write([]byte("not json"))
		}
	}}
	routine := startBlockServerCov(t, b)

	err := checkInvariants(routine)
	if err == nil {
		t.Fatal("expected invariant errors")
	}
	msg := err.Error()
	if !strings.Contains(msg, broken+" invariant is broken: [oops]") {
		t.Fatalf("missing broken invariant error: %s", msg)
	}
	if !strings.Contains(msg, "invariant "+bad+" status code 500 response: panic") {
		t.Fatalf("missing status code error: %s", msg)
	}
	if len(invariants) > 2 && !strings.Contains(msg, "invalid character") {
		t.Fatalf("missing decode error: %s", msg)
	}

	// unreachable api
	if err = checkInvariants(closedPortCov(t) - 1317); err == nil {
		t.Fatal("expected connection errors")
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// sendMsg against a fake CometBFT RPC
////////////////////////////////////////////////////////////////////////////////////////

type sentTxCov struct {
	memo     string
	sequence uint64
	gas      uint64
	fee      sdk.Coins
}

type fakeCometCov struct {
	t    *testing.T
	code uint32
	mu   sync.Mutex
	txs  []sentTxCov
}

func (f *fakeCometCov) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var result any
	switch req.Method {
	case "abci_query":
		acc := authtypes.NewBaseAccount(sdk.MustAccAddressFromBech32(templateAddress["addr_thor_dog"]), nil, 1, 2)
		anyAcc, err := codectypes.NewAnyWithValue(acc)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		bz, err := (&authtypes.QueryAccountResponse{Account: anyAcc}).Marshal()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		result = map[string]any{"response": map[string]any{
			"code": 0, "log": "", "info": "", "index": "0", "value": base64.StdEncoding.EncodeToString(bz),
			"height": "5", "codespace": "",
		}}
	case "broadcast_tx_sync":
		var p struct {
			Tx []byte `json:"tx"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tx, err := encodingConfig.TxConfig.TxDecoder()(p.Tx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		stx, ok := tx.(authsigning.Tx)
		if !ok {
			http.Error(w, "unexpected tx type", http.StatusBadRequest)
			return
		}
		sigs, err := stx.GetSignaturesV2()
		if err != nil || len(sigs) != 1 {
			http.Error(w, "bad signatures", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.txs = append(f.txs, sentTxCov{memo: stx.GetMemo(), sequence: sigs[0].Sequence, gas: stx.GetGas(), fee: stx.GetFee()})
		f.mu.Unlock()
		result = map[string]any{"code": f.code, "data": "", "log": "raw-log", "codespace": "", "hash": "ABCDEF0123456789"}
	default:
		result = map[string]any{}
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func (f *fakeCometCov) last() sentTxCov {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.txs[len(f.txs)-1]
}

func TestSendMsgFakeRPCCov(t *testing.T) {
	fake := &fakeCometCov{t: t}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	routine := serverPortCov(t, srv) - 26657
	setNativeTxIDsCov(t, routine, nil)

	dog := sdk.MustAccAddressFromBech32(templateAddress["addr_thor_dog"])
	cat := sdk.MustAccAddressFromBech32(templateAddress["addr_thor_cat"])
	send := &OpTxSend{}
	send.FromAddress = dog
	send.ToAddress = cat
	send.Amount = sdk.NewCoins(sdk.NewInt64Coin("rune", 1))

	// success: tx hash recorded, sequence from the account query
	if err := send.Execute(io.Discard, "", 0, routine, nil, nil); err != nil {
		t.Fatalf("send failed: %v", err)
	}
	if ids := getNativeTxIDsCov(routine); len(ids) != 1 || ids[0] != "ABCDEF0123456789" {
		t.Fatalf("expected tx hash recorded, got %v", ids)
	}
	if tx := fake.last(); tx.sequence != 2 || tx.memo != "" {
		t.Fatalf("unexpected tx: %+v", tx)
	}

	// sequence and gas overrides
	seq, gas := int64(7), int64(300000)
	send.Sequence, send.Gas = &seq, &gas
	if err := send.Execute(io.Discard, "", 0, routine, nil, nil); err != nil {
		t.Fatal(err)
	}
	if tx := fake.last(); tx.sequence != 7 || tx.gas != 300000 {
		t.Fatalf("expected sequence/gas overrides, got %+v", tx)
	}

	// NOTE: setting a memo rebuilds the factory from the base factory, dropping
	// the sequence and gas overrides (documents current behaviour).
	send.Memo = "hello"
	if err := send.Execute(io.Discard, "", 0, routine, nil, nil); err != nil {
		t.Fatal(err)
	}
	if tx := fake.last(); tx.memo != "hello" || tx.sequence != 2 || tx.gas != 0 {
		t.Fatalf("unexpected memo tx: %+v", tx)
	}

	// fees are applied (and also reset a sequence override, gas is applied after)
	execOp := &OpTxExecuteContract{Fees: "100rune", Gas: &gas, Sequence: &seq}
	execOp.Sender = dog.String()
	execOp.Contract = "tthor14hj2tavq8fpesdwxxcu44rty3hh90vhujrvcmstl4zr3txmfvw9sw58u9f"
	execOp.Msg = []byte(`{"increment":{}}`)
	if err := execOp.Execute(io.Discard, "", 0, routine, nil, nil); err != nil {
		t.Fatal(err)
	}
	if tx := fake.last(); tx.fee.String() != "100rune" || tx.gas != 300000 || tx.sequence != 2 {
		t.Fatalf("unexpected fee tx: %+v", tx)
	}

	// non-zero code is not recorded as a native tx id
	fake.code = 5
	send.Memo = ""
	if err := send.Execute(io.Discard, "", 0, routine, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ids := getNativeTxIDsCov(routine); len(ids) != 4 {
		t.Fatalf("failed tx must not be recorded, got %v", ids)
	}

	// other tx operations against the fake node
	fake.code = 0
	mimir := &OpTxMimir{}
	mimir.Key, mimir.Value, mimir.Signer = "FOO", 1, dog
	ver := &OpTxVersion{}
	ver.Version, ver.Signer = "1.2.3", dog
	dep := &OpTxDeposit{}
	dep.Signer, dep.Memo = dog, "noop"
	dep.Coins = common.Coins{common.NewCoin(common.RuneAsset(), math.NewUint(1))}
	for _, op := range []Operation{
		mimir,
		ver,
		&OpTxNodePauseChain{Value: 1, Signer: dog},
		&OpTxSetIPAddress{IPAddress: "1.2.3.4", Signer: dog},
		&OpTxNetworkFee{BlockHeight: 1, Chain: common.BTCChain, TransactionSize: 1, TransactionRate: 1, Signer: dog},
		&OpTxErrataTx{TxID: common.TxID(txIDACov), Chain: common.BTCChain, Signer: dog},
		dep,
	} {
		if err := op.Execute(io.Discard, "", 0, routine, nil, nil); err != nil {
			t.Fatalf("%T: %v", op, err)
		}
	}
	if ids := getNativeTxIDsCov(routine); len(ids) != 11 {
		t.Fatalf("expected 11 recorded tx ids, got %d", len(ids))
	}

	// keysign op embeds the message directly
	ks := &OpTxTssKeysign{}
	ks.MsgTssKeysignFail = types.MsgTssKeysignFail{}
	if err := ks.Execute(io.Discard, "", 0, routine, nil, nil); err == nil {
		t.Fatal("expected empty keysign fail message to be rejected")
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// OpTxPriceFeed against a fake ebifrost server
////////////////////////////////////////////////////////////////////////////////////////

type fakeBifrostCov struct {
	ebifrost.UnimplementedLocalhostBifrostServer
	mu      sync.Mutex
	batches []*common.QuorumPriceFeedBatch
}

func (f *fakeBifrostCov) SendQuorumPriceFeedBatch(_ context.Context, b *common.QuorumPriceFeedBatch) (*ebifrost.SendQuorumPriceFeedBatchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, b)
	return &ebifrost.SendQuorumPriceFeedBatchResult{}, nil
}

func TestOpTxPriceFeedCov(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeBifrostCov{}
	srv := grpc.NewServer()
	ebifrost.RegisterLocalhostBifrostServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()
	routine := portCov(t, lis.Addr()) - 50051

	op := &OpTxPriceFeed{Prices: map[string]string{"USDC": "1.25"}}
	if err = op.Execute(io.Discard, "", 0, routine, nil, nil); err != nil {
		t.Fatalf("price feed failed: %v", err)
	}

	fake.mu.Lock()
	if len(fake.batches) != 1 {
		fake.mu.Unlock()
		t.Fatalf("expected 1 batch, got %d", len(fake.batches))
	}
	batch := fake.batches[0]
	fake.mu.Unlock()

	feed := batch.QuorumPriceFeeds[0].PriceFeed
	if feed.Time != priceFeedTime || len(feed.Version) != 8 {
		t.Fatalf("unexpected feed metadata: time=%d version=%x", feed.Time, feed.Version)
	}
	priv, err := nodeSecpPrivKey("dog")
	if err != nil {
		t.Fatal(err)
	}
	att := batch.QuorumPriceFeeds[0].Attestations[0]
	if !bytes.Equal(att.PubKey, priv.PubKey().Bytes()) {
		t.Fatal("attestation must be signed by dog by default")
	}
	payload, err := feed.GetSignablePayload()
	if err != nil {
		t.Fatal(err)
	}
	if !priv.PubKey().VerifySignature(payload, att.Signature) {
		t.Fatal("attestation signature does not verify")
	}
	nonZero := 0
	for _, r := range feed.Rates {
		if r.BigFloat().Sign() != 0 {
			nonZero++
			if f, _ := r.BigFloat().Float64(); f != 1.25 {
				t.Fatalf("unexpected rate %v", f)
			}
		}
	}
	if nonZero != 1 {
		t.Fatalf("expected exactly one priced symbol, got %d", nonZero)
	}

	// explicit node
	op = &OpTxPriceFeed{Node: "cat", Prices: map[string]string{"USDT": "1"}}
	if err = op.Execute(io.Discard, "", 0, routine, nil, nil); err != nil {
		t.Fatal(err)
	}
	catPriv, _ := nodeSecpPrivKey("cat")
	fake.mu.Lock()
	gotPub := fake.batches[1].QuorumPriceFeeds[0].Attestations[0].PubKey
	fake.mu.Unlock()
	if !bytes.Equal(gotPub, catPriv.PubKey().Bytes()) {
		t.Fatal("attestation must be signed by the configured node")
	}

	// errors
	if err = (&OpTxPriceFeed{Node: "unicorn"}).Execute(io.Discard, "", 0, routine, nil, nil); err == nil {
		t.Fatal("expected unknown node error")
	}
	if err = (&OpTxPriceFeed{Prices: map[string]string{"USDC": "abc"}}).Execute(io.Discard, "", 0, routine, nil, nil); err == nil || !strings.Contains(err.Error(), "failed to parse price for USDC") {
		t.Fatalf("expected parse error, got %v", err)
	}
	if err = (&OpTxPriceFeed{Prices: map[string]string{"USDC": "1e40"}}).Execute(io.Discard, "", 0, routine, nil, nil); err == nil || !strings.Contains(err.Error(), "failed to build oracle price for USDC") {
		t.Fatalf("expected oracle price error, got %v", err)
	}
	closed := closedPortCov(t) - 50051
	if err = (&OpTxPriceFeed{}).Execute(io.Discard, "", 0, closed, nil, nil); err == nil || !strings.Contains(err.Error(), "failed to send price feed") {
		t.Fatalf("expected send error, got %v", err)
	}
}
