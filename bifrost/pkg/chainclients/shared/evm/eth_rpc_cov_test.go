package evm

import (
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	ecrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
)

// rpcHandlerCov answers a single JSON-RPC method. A nil result is encoded as
// JSON null; a non-nil error is returned as a JSON-RPC error object.
type rpcHandlerCov func(params []json.RawMessage) (any, error)

// fakeEthRPCCov is a minimal httptest-backed JSON-RPC server for ethclient.
type fakeEthRPCCov struct {
	mu       sync.Mutex
	handlers map[string]rpcHandlerCov
	calls    map[string]int
}

func (f *fakeEthRPCCov) set(method string, h rpcHandlerCov) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method] = h
}

func (f *fakeEthRPCCov) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func newFakeEthRPCCov(t *testing.T) (*fakeEthRPCCov, *ethclient.Client) {
	t.Helper()
	f := &fakeEthRPCCov{handlers: map[string]rpcHandlerCov{}, calls: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		h, ok := f.handlers[req.Method]
		f.calls[req.Method]++
		f.mu.Unlock()
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if !ok {
			resp["error"] = map[string]any{"code": -32601, "message": "method not found: " + req.Method}
		} else {
			res, err := h(req.Params)
			if err != nil {
				resp["error"] = map[string]any{"code": -32000, "message": err.Error()}
			} else {
				resp["result"] = res
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	client, err := ethclient.Dial(srv.URL)
	require.NoError(t, err)
	t.Cleanup(client.Close)
	return f, client
}

func constCov(v any) rpcHandlerCov {
	return func([]json.RawMessage) (any, error) { return v, nil }
}

func errCov(msg string) rpcHandlerCov {
	return func([]json.RawMessage) (any, error) { return nil, errors.New(msg) }
}

func toMapCov(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

func headerJSONCov(t *testing.T, n int64) map[string]any {
	t.Helper()
	return toMapCov(t, &etypes.Header{
		Number:      big.NewInt(n),
		Difficulty:  big.NewInt(1),
		UncleHash:   etypes.EmptyUncleHash,
		TxHash:      etypes.EmptyTxsHash,
		ReceiptHash: etypes.EmptyReceiptsHash,
		GasLimit:    30_000_000,
		Time:        1,
	})
}

func blockJSONCov(t *testing.T, n int64) map[string]any {
	t.Helper()
	m := headerJSONCov(t, n)
	m["transactions"] = []any{}
	m["uncles"] = []any{}
	return m
}

func receiptJSONCov(t *testing.T, status uint64) map[string]any {
	t.Helper()
	return toMapCov(t, &etypes.Receipt{
		Status:            status,
		CumulativeGasUsed: 21000,
		GasUsed:           21000,
		Logs:              []*etypes.Log{},
		TxHash:            ecommon.HexToHash("0x01"),
	})
}

func signedTxJSONCov(t *testing.T, key *ecdsa.PrivateKey, mined bool) (map[string]any, *etypes.Transaction) {
	t.Helper()
	signer := etypes.NewEIP155Signer(big.NewInt(1))
	tx, err := etypes.SignTx(etypes.NewTransaction(1, ecommon.HexToAddress("0x02"), big.NewInt(1), 21000, big.NewInt(1), nil), signer, key)
	require.NoError(t, err)
	m := toMapCov(t, tx)
	if mined {
		m["blockNumber"] = "0x1"
		m["blockHash"] = ecommon.HexToHash("0xabc").Hex()
		m["from"] = ecrypto.PubkeyToAddress(key.PublicKey).Hex()
	}
	return m, tx
}

func newEthRPCCov(t *testing.T) (*fakeEthRPCCov, *EthRPC) {
	t.Helper()
	f, client := newFakeEthRPCCov(t)
	e, err := NewEthRPC(client, time.Second, "ETH")
	require.NoError(t, err)
	require.NotNil(t, e)
	return f, e
}

func TestEthRPCBlockHeightCov(t *testing.T) {
	f, e := newEthRPCCov(t)

	f.set("eth_blockNumber", constCov("0x2a"))
	h, err := e.GetBlockHeight()
	require.NoError(t, err)
	require.Equal(t, int64(42), h)

	f.set("eth_blockNumber", errCov("boom"))
	h, err = e.GetBlockHeight()
	require.Error(t, err)
	require.Contains(t, err.Error(), "fail to get block height")
	require.Equal(t, int64(-1), h)
}

func TestEthRPCBlockHeightSafeCov(t *testing.T) {
	f, e := newEthRPCCov(t)

	var gotTag string
	f.set("eth_getBlockByNumber", func(p []json.RawMessage) (any, error) {
		_ = json.Unmarshal(p[0], &gotTag)
		return blockJSONCov(t, 77), nil
	})
	h, err := e.GetBlockHeightSafe()
	require.NoError(t, err)
	require.Equal(t, int64(77), h)
	require.Equal(t, "safe", gotTag)

	f.set("eth_getBlockByNumber", errCov("no safe block"))
	h, err = e.GetBlockHeightSafe()
	require.Error(t, err)
	require.Contains(t, err.Error(), "fail to get block")
	require.Equal(t, int64(-1), h)
}

func TestEthRPCHeaderAndBlocksCov(t *testing.T) {
	f, e := newEthRPCCov(t)

	f.set("eth_getBlockByNumber", func(p []json.RawMessage) (any, error) {
		var tag string
		_ = json.Unmarshal(p[0], &tag)
		switch tag {
		case "0xa":
			return blockJSONCov(t, 10), nil
		case "0xb":
			return nil, nil // not found
		default:
			return nil, errors.New("rpc down")
		}
	})

	hdr, err := e.GetHeader(10)
	require.NoError(t, err)
	require.Equal(t, int64(10), hdr.Number.Int64())

	blk, err := e.GetBlock(10)
	require.NoError(t, err)
	require.Equal(t, uint64(10), blk.NumberU64())

	blk, err = e.GetRPCBlock(10)
	require.NoError(t, err)
	require.Equal(t, uint64(10), blk.NumberU64())

	// a missing block maps to ErrUnavailableBlock
	blk, err = e.GetRPCBlock(11)
	require.ErrorIs(t, err, btypes.ErrUnavailableBlock)
	require.Nil(t, blk)

	// any other error is wrapped
	blk, err = e.GetRPCBlock(12)
	require.Error(t, err)
	require.NotErrorIs(t, err, btypes.ErrUnavailableBlock)
	require.Contains(t, err.Error(), "fail to fetch block")
	require.Nil(t, blk)
}

func TestEthRPCNonceCov(t *testing.T) {
	f, e := newEthRPCCov(t)
	addr := "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473"

	var tags []string
	f.set("eth_getTransactionCount", func(p []json.RawMessage) (any, error) {
		var tag string
		_ = json.Unmarshal(p[1], &tag)
		tags = append(tags, tag)
		if tag == "pending" {
			return "0x7", nil
		}
		return "0x5", nil
	})
	n, err := e.GetNonce(addr)
	require.NoError(t, err)
	require.Equal(t, uint64(7), n)

	n, err = e.GetNonceFinalized(addr)
	require.NoError(t, err)
	require.Equal(t, uint64(5), n)
	require.Equal(t, []string{"pending", "latest"}, tags)

	f.set("eth_getTransactionCount", errCov("nonce unavailable"))
	_, err = e.GetNonce(addr)
	require.ErrorContains(t, err, "fail to get account nonce")
	_, err = e.GetNonceFinalized(addr)
	require.ErrorContains(t, err, "fail to get account nonce")
}

func TestEthRPCEstimateGasAndReceiptCov(t *testing.T) {
	f, e := newEthRPCCov(t)

	var callArg map[string]any
	f.set("eth_estimateGas", func(p []json.RawMessage) (any, error) {
		_ = json.Unmarshal(p[0], &callArg)
		return "0x5208", nil
	})
	to := ecommon.HexToAddress("0x02")
	tx := etypes.NewTransaction(0, to, big.NewInt(3), 21000, big.NewInt(9), []byte{0x01})
	gas, err := e.EstimateGas("0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473", tx)
	require.NoError(t, err)
	require.Equal(t, uint64(21000), gas)
	require.EqualValues(t, "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473", callArg["from"])
	require.EqualValues(t, "0x0000000000000000000000000000000000000002", callArg["to"])
	require.EqualValues(t, "0x3", callArg["value"])
	require.EqualValues(t, "0x9", callArg["gasPrice"])

	f.set("eth_getTransactionReceipt", constCov(receiptJSONCov(t, 1)))
	r, err := e.GetReceipt("0x01")
	require.NoError(t, err)
	require.Equal(t, etypes.ReceiptStatusSuccessful, r.Status)
	require.Equal(t, uint64(21000), r.GasUsed)
}

func TestEthRPCCheckTransactionCov(t *testing.T) {
	f, e := newEthRPCCov(t)
	key, err := ecrypto.GenerateKey()
	require.NoError(t, err)

	// rpc error -> not found
	f.set("eth_getTransactionByHash", errCov("unknown"))
	require.False(t, e.CheckTransaction("0x01"))

	// null result -> not found
	f.set("eth_getTransactionByHash", constCov(nil))
	require.False(t, e.CheckTransaction("0x01"))

	// pending tx -> true (prefer false positive)
	pending, _ := signedTxJSONCov(t, key, false)
	f.set("eth_getTransactionByHash", constCov(pending))
	require.True(t, e.CheckTransaction("0x01"))
	require.Equal(t, 0, f.count("eth_getTransactionReceipt"))

	mined, _ := signedTxJSONCov(t, key, true)
	f.set("eth_getTransactionByHash", constCov(mined))

	// receipt lookup error -> true (unknown)
	f.set("eth_getTransactionReceipt", errCov("no receipt"))
	require.True(t, e.CheckTransaction("0x01"))

	// successful receipt -> true
	f.set("eth_getTransactionReceipt", constCov(receiptJSONCov(t, 1)))
	require.True(t, e.CheckTransaction("0x01"))

	// failed receipt -> false
	f.set("eth_getTransactionReceipt", constCov(receiptJSONCov(t, 0)))
	require.False(t, e.CheckTransaction("0x01"))
}
