package core

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	etypes "github.com/ethereum/go-ethereum/core/types"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"

	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
)

////////////////////////////////////////////////////////////////////////////////////////
// Fake EVM JSON-RPC (for real *evm.Client instances)
////////////////////////////////////////////////////////////////////////////////////////

// covEVM is an httptest Ethereum JSON-RPC node. Defaults: chain id 1337, nonce
// 0, gas price 1 gwei, eth_getBalance = Balance (wei), eth_call = 32-byte
// CallResult (or CallFn), eth_getCode = Code, eth_sendRawTransaction echoes the
// tx hash (or fails with SendErr), receipts report ReceiptStatus. Override any
// method entirely via Fns.
type covEVM struct {
	server *httptest.Server

	mu            sync.Mutex
	Balance       *big.Int
	CallResult    *big.Int
	CallFn        func(to string, data []byte) (*big.Int, error)
	Code          string
	ReceiptStatus uint64
	SendErr       string
	Fns           map[string]func(params []json.RawMessage) (interface{}, *covRPCError)
	Calls         map[string]int
	RawTxs        []string
}

type covRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func newCovEVM(t testing.TB) *covEVM {
	t.Helper()
	e := &covEVM{
		Balance:       big.NewInt(0),
		CallResult:    big.NewInt(0),
		ReceiptStatus: 1,
		Fns:           map[string]func([]json.RawMessage) (interface{}, *covRPCError){},
		Calls:         map[string]int{},
	}
	e.server = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.server.Close)
	return e
}

func covHexBig(v *big.Int) string { return "0x" + v.Text(16) }

func (e *covEVM) handle(method string, params []json.RawMessage) (interface{}, *covRPCError) {
	e.mu.Lock()
	e.Calls[method]++
	fn, ok := e.Fns[method]
	e.mu.Unlock()
	if ok {
		return fn(params)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	switch method {
	case "eth_chainId":
		return "0x539", nil
	case "eth_getTransactionCount":
		return "0x0", nil
	case "eth_gasPrice":
		return "0x3b9aca00", nil
	case "eth_getBalance":
		return covHexBig(e.Balance), nil
	case "eth_getCode":
		if e.Code == "" {
			return "0x", nil
		}
		return e.Code, nil
	case "eth_call":
		var call struct {
			To    string `json:"to"`
			Data  string `json:"data"`
			Input string `json:"input"`
		}
		if len(params) > 0 {
			_ = json.Unmarshal(params[0], &call)
		}
		data := call.Input
		if data == "" {
			data = call.Data
		}
		result := e.CallResult
		if e.CallFn != nil {
			raw, _ := hex.DecodeString(strings.TrimPrefix(data, "0x"))
			r, err := e.CallFn(strings.ToLower(call.To), raw)
			if err != nil {
				return nil, &covRPCError{Code: -32000, Message: err.Error()}
			}
			result = r
		}
		return fmt.Sprintf("0x%064x", result), nil
	case "eth_sendRawTransaction":
		if e.SendErr != "" {
			return nil, &covRPCError{Code: -32000, Message: e.SendErr}
		}
		var raw string
		_ = json.Unmarshal(params[0], &raw)
		e.RawTxs = append(e.RawTxs, raw)
		buf, _ := hex.DecodeString(strings.TrimPrefix(raw, "0x"))
		tx := new(etypes.Transaction)
		if err := tx.UnmarshalBinary(buf); err != nil {
			return nil, &covRPCError{Code: -32000, Message: err.Error()}
		}
		return tx.Hash().Hex(), nil
	case "eth_getTransactionReceipt":
		var hash string
		_ = json.Unmarshal(params[0], &hash)
		return map[string]interface{}{
			"transactionHash":   hash,
			"transactionIndex":  "0x0",
			"blockHash":         "0x" + strings.Repeat("11", 32),
			"blockNumber":       "0x1",
			"cumulativeGasUsed": "0x5208",
			"gasUsed":           "0x5208",
			"effectiveGasPrice": "0x3b9aca00",
			"logs":              []interface{}{},
			"logsBloom":         "0x" + strings.Repeat("00", 256),
			"status":            fmt.Sprintf("0x%x", e.ReceiptStatus),
			"type":              "0x0",
		}, nil
	}
	return nil, &covRPCError{Code: -32601, Message: "method not found: " + method}
}

func (e *covEVM) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage   `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, rpcErr := e.handle(req.Method, req.Params)
	resp := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// CallCount returns how many times the RPC method was invoked.
func (e *covEVM) CallCount(method string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Calls[method]
}

// SentTxs returns the decoded transactions accepted by eth_sendRawTransaction.
func (e *covEVM) SentTxs(t testing.TB) []*etypes.Transaction {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	txs := make([]*etypes.Transaction, 0, len(e.RawTxs))
	for _, raw := range e.RawTxs {
		buf, err := hex.DecodeString(strings.TrimPrefix(raw, "0x"))
		if err != nil {
			t.Fatal(err)
		}
		tx := new(etypes.Transaction)
		if err := tx.UnmarshalBinary(buf); err != nil {
			t.Fatal(err)
		}
		txs = append(txs, tx)
	}
	return txs
}

var (
	covKeysMu    sync.Mutex
	covKeysCache = map[int]*thorclient.Keys{}
)

// covKeys derives the thorclient keys for the mocknet mnemonic at idx, the
// same way simtypes.NewUser does.
func covKeys(t testing.TB, idx int) *thorclient.Keys {
	t.Helper()
	covKeysMu.Lock()
	defer covKeysMu.Unlock()
	if keys, ok := covKeysCache[idx]; ok {
		return keys
	}
	mnemonic := covMnemonics[idx]
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kr := keyring.NewInMemory(cdc, func(options *keyring.Options) {
		options.SupportedAlgos = keyring.SigningAlgoList{hd.Secp256k1}
	})
	name := strings.Split(mnemonic, " ")[0]
	if _, err := kr.NewAccount(name, mnemonic, "", cmd.THORChainHDPath, hd.Secp256k1); err != nil {
		t.Fatalf("keyring: %v", err)
	}
	keys := thorclient.NewKeysWithKeybase(kr, name, "")
	covKeysCache[idx] = keys
	return keys
}

// newCovEVMClient returns a real *evm.Client for chain backed by a fake RPC,
// using the keys of mnemonic idx (so it matches newCovUser(t, idx, ...)).
func newCovEVMClient(t testing.TB, chain common.Chain, idx int) (*evm.Client, *covEVM) {
	t.Helper()
	rpc := newCovEVM(t)
	client, err := evm.NewClient(chain, rpc.server.URL, covKeys(t, idx))
	if err != nil {
		t.Fatalf("evm client: %v", err)
	}
	return client.(*evm.Client), rpc
}
