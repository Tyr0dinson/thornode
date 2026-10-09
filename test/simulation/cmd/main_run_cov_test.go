package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// readinessFakeCmdCov serves the TRON, Solana, ZEC and EVM readiness probes
// with heights that advance on every request, plus the bifrost ping.
type readinessFakeCmdCov struct {
	mu                         sync.Mutex
	server                     *httptest.Server
	tron, solana, zec, evm, bf int
}

func newReadinessFakeCmdCov(t *testing.T) *readinessFakeCmdCov {
	t.Helper()
	f := &readinessFakeCmdCov{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/ping":
			f.bf++
			return
		case "/wallet/getnowblock":
			_, _ = fmt.Fprintf(w, `{"block_header":{"raw_data":{"number":%d}}}`, 20+f.tron)
			f.tron++
			return
		}
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "getSlot":
			_, _ = fmt.Fprintf(w, `{"result":%d}`, 1+f.solana)
			f.solana++
		case "getblockcount":
			_, _ = fmt.Fprintf(w, `{"result":%d}`, 401+f.zec)
			f.zec++
		case "eth_chainId":
			f.evm++
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// TestMainRunsSimulationAgainstFakesCov drives main() end to end against fake
// endpoints with no stages selected: it must wait for every RPC to become
// ready, create users, enable XMR coverage and finish the (empty) DAG.
func TestMainRunsSimulationAgainstFakesCov(t *testing.T) {
	restoreSimEnvCmdCov(t)
	t.Setenv("SIMULATION_PPROF_ADDRESS", "off")
	t.Setenv("STAGES", "none")
	t.Setenv("PARALLELISM", "1")
	t.Setenv("RANDOM_SEED_HEX", "0102ab")
	t.Setenv("XMR_POST_CHURN_REQUIRED", "")
	acommon.SetXMRChainEnabled(false)

	ready := newReadinessFakeCmdCov(t)
	origRPCs := map[common.Chain]string{}
	for _, chain := range []common.Chain{
		common.TRONChain, common.SOLChain, common.ZECChain,
		common.ETHChain, common.AVAXChain, common.BASEChain, common.POLChain,
	} {
		origRPCs[chain] = chainRPCs[chain]
		chainRPCs[chain] = ready.server.URL
	}
	origBifrost := bifrostReadinessEndpoints
	bifrostReadinessEndpoints = []string{ready.server.URL + "/ping"}

	evmFake := newEVMFakeCmdCov(t)
	btc := newLedgerCmdCov()
	origCons := liteClientConstructors
	liteClientConstructors = map[common.Chain]LiteChainClientConstructor{
		common.ETHChain: evm.NewConstructor(evmFake.server.URL),
		common.BTCChain: func(common.Chain, *thorclient.Keys) (LiteChainClient, error) {
			return &ledgerClientCmdCov{ledger: btc}, nil
		},
	}
	btc.credit("", common.NewCoin(common.BTCAsset, sdkmath.NewUint(5)))
	// every EVM address is rich so the master reports a TKN balance
	evmFake.defaultBalance = new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil)
	t.Cleanup(func() {
		for chain, rpc := range origRPCs {
			chainRPCs[chain] = rpc
		}
		bifrostReadinessEndpoints = origBifrost
		liteClientConstructors = origCons
	})

	main()

	ready.mu.Lock()
	defer ready.mu.Unlock()
	if ready.bf != 1 {
		t.Fatalf("expected one bifrost ping, got %d", ready.bf)
	}
	if ready.tron != 4 || ready.solana != 3 || ready.zec != 2 {
		t.Fatalf("unexpected readiness polls tron=%d solana=%d zec=%d", ready.tron, ready.solana, ready.zec)
	}
	if ready.evm != 4 {
		t.Fatalf("expected one eth_chainId probe per EVM chain, got %d", ready.evm)
	}
	if !acommon.XMRChainEnabled() {
		t.Fatal("XMR coverage must be enabled once the inbound address is available")
	}
	if evmFake.count("eth_call") < 2 { // balanceOf + decimals
		t.Fatalf("expected token balance and decimals lookups, got %d eth_call", evmFake.count("eth_call"))
	}
}

func TestReadinessGettersCov(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/status"):
			w.WriteHeader(http.StatusServiceUnavailable)
		case strings.HasPrefix(r.URL.Path, "/badjson"):
			_, _ = w.Write([]byte(`{nope`))
		case strings.HasPrefix(r.URL.Path, "/rpcerr"):
			_, _ = w.Write([]byte(`{"error":{"code":-1,"message":"warming up"}}`))
		case r.URL.Path == "/ok/wallet/getnowblock":
			_, _ = w.Write([]byte(`{"block_header":{"raw_data":{"number":42}}}`))
		case r.URL.Path == "/ok":
			_, _ = w.Write([]byte(`{"result":7}`))
		}
	}))
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	if h, err := getTronBlockHeight(server.URL + "/ok"); err != nil || h != 42 {
		t.Fatalf("tron height %d %v", h, err)
	}
	if s, err := getSolanaSlot(server.URL + "/ok"); err != nil || s != 7 {
		t.Fatalf("solana slot %d %v", s, err)
	}
	if b, err := getZecBlockCount(server.URL + "/ok"); err != nil || b != 7 {
		t.Fatalf("zec count %d %v", b, err)
	}

	for name, fn := range map[string]func(string) error{
		"tron":   func(u string) error { _, err := getTronBlockHeight(u); return err },
		"solana": func(u string) error { _, err := getSolanaSlot(u); return err },
		"zec":    func(u string) error { _, err := getZecBlockCount(u); return err },
	} {
		if err := fn(closed.URL); err == nil {
			t.Errorf("%s: expected transport error", name)
		}
		if err := fn(server.URL + "/status"); err == nil || !strings.Contains(err.Error(), "status code: 503") {
			t.Errorf("%s: expected status error, got %v", name, err)
		}
		if err := fn(server.URL + "/badjson"); err == nil {
			t.Errorf("%s: expected decode error", name)
		}
	}
	if _, err := getSolanaSlot(server.URL + "/rpcerr"); err == nil || err.Error() != "solana rpc error -1: warming up" {
		t.Fatalf("expected solana rpc error, got %v", err)
	}
	if _, err := getZecBlockCount(server.URL + "/rpcerr"); err == nil || err.Error() != "zec rpc error -1: warming up" {
		t.Fatalf("expected zec rpc error, got %v", err)
	}
	server.Close()
}
