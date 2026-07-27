package utxo

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// newBootstrapRPCServer is a minimal single-request JSON-RPC mock. The handler
// maps a method name to a result; the envelope matches what the btcd rpcclient
// (BTC/DOGE path) expects.
func newBootstrapRPCServer(handler func(method string) interface{}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		defer r.Body.Close()
		var req struct {
			Method string      `json:"method"`
			ID     interface{} `json:"id"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  handler(req.Method),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// TestImportVaultAddressesUntrackedFailsClosed asserts ImportVaultAddresses
// returns an error (and leaves bootImportOK false) when an address is still not
// tracked after the watch-only import — the fail-closed guard added for #3084
// that stops solvency from being reported on a wallet that never took the import.
func TestImportVaultAddressesUntrackedFailsClosed(t *testing.T) {
	ttypes.SetupConfigForTest()

	// getaddressinfo always reports the address as untracked, so the post-import
	// verification never passes.
	srv := newBootstrapRPCServer(func(method string) interface{} {
		switch method {
		case "getaddressinfo", "validateaddress":
			return map[string]interface{}{"ismine": false, "iswatchonly": false}
		default: // createwallet, importaddress
			return nil
		}
	})
	defer srv.Close()

	rpcClient, err := rpc.NewClient(srv.URL, bob, password, 0, time.Second*10, common.BTCChain, zerolog.Nop())
	if err != nil {
		t.Fatalf("new rpc client: %v", err)
	}
	cl := &Client{
		rpc: rpcClient,
		log: zerolog.Nop(),
		cfg: config.BifrostChainConfiguration{ChainID: common.BTCChain},
	}

	pkStr := "tthorpub1addwnpepqflvfv08t6qt95lmttd6wpf3ss8wx63e9vf6fvyuj2yy6nnyna576rfzjks"
	if common.CurrentChainNetwork == common.MainNet {
		pkStr = "thorpub1addwnpepqwprh5vd0rrk78kd98qjruuazwvapnxft7f86w7hlf768whxytpn5quf2gs"
	}
	pk, err := common.NewPubKey(pkStr)
	if err != nil {
		t.Fatalf("new pubkey: %v", err)
	}

	err = cl.ImportVaultAddresses(common.PubKeys{pk})
	if err == nil {
		t.Fatal("expected error when address stays untracked after import")
	}
	if !strings.Contains(err.Error(), "not tracked after import") {
		t.Fatalf("unexpected error: %v", err)
	}
	if cl.bootImportOK.Load() {
		t.Fatal("bootImportOK must stay false when the import did not verify")
	}
}

// TestSolvencyGate asserts shouldReportSolvency (via walletReady) stays false
// until the bootstrap import is confirmed AND the wallet rescan has finished,
// then reports, and latches walletRescanDone so it never re-queries (#3084).
func TestSolvencyGate(t *testing.T) {
	ttypes.SetupConfigForTest()

	var scanning atomic.Bool // wallet idle by default
	srv := newBootstrapRPCServer(func(method string) interface{} {
		if method != "getwalletinfo" {
			return nil
		}
		if scanning.Load() {
			return map[string]interface{}{"scanning": map[string]interface{}{"duration": 10, "progress": 0.5}}
		}
		return map[string]interface{}{"scanning": false}
	})
	defer srv.Close()

	rpcClient, err := rpc.NewClient(srv.URL, bob, password, 0, time.Second*10, common.BTCChain, zerolog.Nop())
	if err != nil {
		t.Fatalf("new rpc client: %v", err)
	}
	cl := &Client{
		rpc: rpcClient,
		log: zerolog.Nop(),
		cfg: config.BifrostChainConfiguration{ChainID: common.BTCChain},
	}

	// Before the bootstrap import is confirmed: gated.
	if cl.shouldReportSolvency(100) {
		t.Fatal("solvency must be gated before bootstrap import completes")
	}

	// Import confirmed but wallet still rescanning: gated.
	cl.bootImportOK.Store(true)
	scanning.Store(true)
	if cl.shouldReportSolvency(100) {
		t.Fatal("solvency must be gated while the wallet is rescanning")
	}

	// Rescan finished: report, and latch.
	scanning.Store(false)
	if !cl.shouldReportSolvency(100) {
		t.Fatal("solvency must report once import is confirmed and rescan is done")
	}
	if !cl.walletRescanDone.Load() {
		t.Fatal("walletRescanDone must latch true after a completed rescan")
	}

	// Latched: a new scan appearing must not re-gate (no further getwalletinfo).
	scanning.Store(true)
	if !cl.walletReady() {
		t.Fatal("walletReady must stay true once the rescan has been observed done")
	}
}

// TestWalletReadyZECExempt asserts ZEC is exempt from the readiness gate. ZEC
// retrieves UTXOs via getaddressutxos (zebrad has no wallet), so it never runs a
// bootstrap import and bootImportOK is never set for it; the gate must not
// silence ZEC solvency reporting forever (#3084 regression).
func TestWalletReadyZECExempt(t *testing.T) {
	ttypes.SetupConfigForTest()
	cl := &Client{
		log: zerolog.Nop(),
		cfg: config.BifrostChainConfiguration{ChainID: common.ZECChain},
	}
	// bootImportOK deliberately left false — ZEC must still be ready.
	if !cl.walletReady() {
		t.Fatal("ZEC must be exempt from the wallet-readiness gate")
	}
	// height 100 is on the ZEC cadence (height%10==0); the gate must not block it.
	if !cl.shouldReportSolvency(100) {
		t.Fatal("ZEC solvency must not be gated by bootstrap-import readiness")
	}
}

// TestImportVaultAddressesDefinitiveRescanErrorGatesClosed asserts that a
// non-timeout rescan rejection (e.g. Bitcoin Core pruning mode disables
// importaddress rescan) resets bootImportOK so solvency stays gated on an
// unindexed wallet, rather than opening the gate and driving a false-insolvency
// slash (#3084 regression).
func TestImportVaultAddressesDefinitiveRescanErrorGatesClosed(t *testing.T) {
	ttypes.SetupConfigForTest()

	var gaiCalls, importCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		defer r.Body.Close()
		var req struct {
			Method string      `json:"method"`
			ID     interface{} `json:"id"`
		}
		_ = json.Unmarshal(body, &req)
		resp := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "getaddressinfo", "validateaddress":
			// Untracked on the first (collect) query, tracked afterwards (verify),
			// so the address is imported and then confirmed — reaching the rescan.
			tracked := gaiCalls.Add(1) > 1
			resp["result"] = map[string]interface{}{"ismine": false, "iswatchonly": tracked}
		case "importaddress":
			// 1st importaddress = watch-only (rescan=false) → ok; 2nd = the rescan
			// (rescan=true) → definitive daemon rejection (not a client timeout).
			if importCalls.Add(1) >= 2 {
				resp["error"] = map[string]interface{}{"code": -4, "message": "rescan is disabled in pruned mode"}
			} else {
				resp["result"] = nil
			}
		default: // createwallet
			resp["result"] = nil
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	rpcClient, err := rpc.NewClient(srv.URL, bob, password, 0, time.Second*10, common.BTCChain, zerolog.Nop())
	if err != nil {
		t.Fatalf("new rpc client: %v", err)
	}
	cl := &Client{
		rpc: rpcClient,
		log: zerolog.Nop(),
		cfg: config.BifrostChainConfiguration{ChainID: common.BTCChain},
	}

	pkStr := "tthorpub1addwnpepqflvfv08t6qt95lmttd6wpf3ss8wx63e9vf6fvyuj2yy6nnyna576rfzjks"
	if common.CurrentChainNetwork == common.MainNet {
		pkStr = "thorpub1addwnpepqwprh5vd0rrk78kd98qjruuazwvapnxft7f86w7hlf768whxytpn5quf2gs"
	}
	pk, err := common.NewPubKey(pkStr)
	if err != nil {
		t.Fatalf("new pubkey: %v", err)
	}

	err = cl.ImportVaultAddresses(common.PubKeys{pk})
	if err == nil {
		t.Fatal("expected a definitive rescan error")
	}
	if !strings.Contains(err.Error(), "rescan") {
		t.Fatalf("unexpected error: %v", err)
	}
	if cl.bootImportOK.Load() {
		t.Fatal("bootImportOK must be reset to false on a definitive rescan error so the gate stays closed")
	}
	if cl.walletReady() {
		t.Fatal("walletReady must stay false after a definitive rescan error")
	}
}
