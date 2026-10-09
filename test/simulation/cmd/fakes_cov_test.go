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
)

// evmFakeCmdCov is a minimal EVM JSON-RPC node: balances are static per
// address (lowercase hex), every transaction is accepted with a successful
// receipt, and ERC20 decimals() reports 18.
type evmFakeCmdCov struct {
	mu       sync.Mutex
	server   *httptest.Server
	balances map[string]*big.Int // wei and token base units, keyed by address
	methods  map[string]int
	sentRaw  int
	// defaultBalance is returned for addresses without an explicit balance
	defaultBalance *big.Int
}

func newEVMFakeCmdCov(t *testing.T) *evmFakeCmdCov {
	t.Helper()
	f := &evmFakeCmdCov{balances: map[string]*big.Int{}, methods: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *evmFakeCmdCov) setBalance(addr string, v *big.Int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.balances[strings.ToLower(addr)] = v
}

func (f *evmFakeCmdCov) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.methods[method]
}

func (f *evmFakeCmdCov) balanceOf(addr string) *big.Int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.balances[strings.ToLower(addr)]; ok {
		return b
	}
	if f.defaultBalance != nil {
		return f.defaultBalance
	}
	return big.NewInt(0)
}

func (f *evmFakeCmdCov) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage   `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.methods[req.Method]++
	f.mu.Unlock()

	var result interface{}
	switch req.Method {
	case "eth_chainId":
		result = "0x539"
	case "eth_getTransactionCount":
		result = "0x0"
	case "eth_gasPrice":
		result = "0x1"
	case "eth_getBalance":
		var addr string
		_ = json.Unmarshal(req.Params[0], &addr)
		result = fmt.Sprintf("0x%x", f.balanceOf(addr))
	case "eth_call":
		var call map[string]string
		_ = json.Unmarshal(req.Params[0], &call)
		data := call["input"]
		if data == "" {
			data = call["data"]
		}
		switch {
		case strings.HasPrefix(data, "0x313ce567"): // decimals()
			result = fmt.Sprintf("0x%064x", 18)
		case strings.HasPrefix(data, "0x70a08231"): // balanceOf(address)
			result = fmt.Sprintf("0x%064x", f.balanceOf("0x"+data[len(data)-40:]))
		default:
			result = "0x"
		}
	case "eth_sendRawTransaction":
		f.mu.Lock()
		f.sentRaw++
		f.mu.Unlock()
		result = "0x" + strings.Repeat("ab", 32)
	case "eth_getTransactionReceipt":
		var hash string
		_ = json.Unmarshal(req.Params[0], &hash)
		result = map[string]interface{}{
			"type":              "0x0",
			"status":            "0x1",
			"cumulativeGasUsed": "0x5208",
			"logsBloom":         "0x" + strings.Repeat("00", 256),
			"logs":              []interface{}{},
			"transactionHash":   hash,
			"contractAddress":   nil,
			"gasUsed":           "0x5208",
			"effectiveGasPrice": "0x1",
			"blockHash":         "0x" + strings.Repeat("cd", 32),
			"blockNumber":       "0x1",
			"transactionIndex":  "0x0",
		}
	default:
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0", "id": req.ID,
			"error": map[string]interface{}{"code": -32601, "message": "method not found"},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

// moneroHelperFakeCmdCov fakes the XMR simulation helper endpoints.
type moneroHelperFakeCmdCov struct {
	mu       sync.Mutex
	server   *httptest.Server
	faucets  []string
	balances int
	birthday []uint64
}

func newMoneroHelperFakeCmdCov(t *testing.T, height uint64, unlockedAtomic uint64) *moneroHelperFakeCmdCov {
	t.Helper()
	f := &moneroHelperFakeCmdCov{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/v1/monero/chain/height":
			_, _ = fmt.Fprintf(w, `{"height":%d}`, height)
		case "/v1/monero/sim/faucet":
			var req struct {
				ToAddress string `json:"to_address"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.faucets = append(f.faucets, req.ToAddress)
			_, _ = fmt.Fprintf(w, `{"tx_hash":"faucet%d"}`, len(f.faucets))
		case "/v1/monero/sim/balance":
			var req struct {
				BirthdayHeight *uint64 `json:"birthday_height"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.BirthdayHeight != nil {
				f.birthday = append(f.birthday, *req.BirthdayHeight)
			}
			f.balances++
			_, _ = fmt.Fprintf(w, `{"balance_atomic":%d,"unlocked_balance_atomic":%d}`, unlockedAtomic, unlockedAtomic)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}
