package rpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc/zec"
	"gitlab.com/thorchain/thornode/v3/config"

	"github.com/btcsuite/btcd/btcjson"
	ethrpc "github.com/ethereum/go-ethereum/rpc"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type RPCSuite struct {
	backoffs []time.Duration // the retry backoffs of the running test
}

var _ = Suite(&RPCSuite{})

// realRetrySleep is retrySleep as the package sets it, before any test.
var realRetrySleep = retrySleep

// Retries record their backoffs instead of sleeping through them.
func (s *RPCSuite) SetUpTest(c *C) {
	s.backoffs = nil
	retrySleep = func(d time.Duration) { s.backoffs = append(s.backoffs, d) }
}

func (s *RPCSuite) TearDownTest(c *C) {
	retrySleep = realRetrySleep
}

// ---------------------------------------------------------------------------
// Mock JSON-RPC server
// ---------------------------------------------------------------------------

// jsonRPCRequest is the structure of a JSON-RPC request
type jsonRPCRequest struct {
	Method string        `json:"method"`
	Params []interface{} `json:"params"`
	ID     interface{}   `json:"id"`
}

// newMockRPCServer creates a mock HTTP server that responds to JSON-RPC requests.
// The handler function receives the method and params and returns a result or error.
func newMockRPCServer(handler func(method string, params []interface{}) (interface{}, *btcjson.RPCError)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		defer r.Body.Close()

		// Try to parse as a batch request (array of requests)
		var batchReqs []jsonRPCRequest
		if err := json.Unmarshal(body, &batchReqs); err == nil && len(batchReqs) > 0 {
			// batch request
			var responses []json.RawMessage
			for _, req := range batchReqs {
				result, rpcErr := handler(req.Method, req.Params)
				resp := map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      req.ID,
				}
				if rpcErr != nil {
					resp["error"] = map[string]interface{}{
						"code":    rpcErr.Code,
						"message": rpcErr.Message,
					}
				} else {
					resp["result"] = result
				}
				raw, _ := json.Marshal(resp)
				responses = append(responses, raw)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(responses)
			return
		}

		// Single request
		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		result, rpcErr := handler(req.Method, req.Params)
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
		}
		if rpcErr != nil {
			resp["error"] = map[string]interface{}{
				"code":    rpcErr.Code,
				"message": rpcErr.Message,
			}
		} else {
			resp["result"] = result
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// newErrorServer returns a server that always responds with the given HTTP status code.
func newErrorServer(statusCode int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":-1,"message":"server error"}}`, statusCode)
	}))
}

// ---------------------------------------------------------------------------
// Mock SerializableTx
// ---------------------------------------------------------------------------

type mockTx struct {
	data     []byte
	serError error
}

func (m *mockTx) SerializeSize() int { return len(m.data) }
func (m *mockTx) Serialize(w io.Writer) error {
	if m.serError != nil {
		return m.serError
	}
	_, err := w.Write(m.data)
	return err
}

// ---------------------------------------------------------------------------
// NewClient tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestNewClient(c *C) {
	log := zerolog.Nop()

	// BTC chain (default) with explicit http
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 3, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)
	c.Assert(cl, NotNil)
	c.Assert(cl.client, NotNil)
	c.Assert(cl.chain, Equals, common.BTCChain)
	c.Assert(cl.maxRetries, Equals, 3)
}

func (s *RPCSuite) TestNewClientNoScheme(c *C) {
	log := zerolog.Nop()

	// Without scheme, should default to http://
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, nil
	})
	defer srv.Close()

	// Strip http:// from the URL to test auto-prefix
	host := srv.URL[len("http://"):]
	cl, err := NewClient(host, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)
	c.Assert(cl, NotNil)
}

func (s *RPCSuite) TestNewClientZEC(c *C) {
	log := zerolog.Nop()

	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 2, time.Second*10, common.ZECChain, log)
	c.Assert(err, IsNil)
	c.Assert(cl, NotNil)
	c.Assert(cl.client, NotNil)
	c.Assert(cl.chain, Equals, common.ZECChain)
}

func (s *RPCSuite) TestNewClientInvalidURL(c *C) {
	log := zerolog.Nop()

	// Invalid URL for default chain should fail
	cl, err := NewClient("://invalid", "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, NotNil)
	c.Assert(cl, IsNil)
}

// ---------------------------------------------------------------------------
// GetBlockCount tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetBlockCount(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getblockcount")
		return 800000, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	count, err := cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(count, Equals, int64(800000))
}

func (s *RPCSuite) TestGetBlockCountError(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, btcjson.NewRPCError(btcjson.ErrRPCClientNotConnected, "not connected")
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	_, err = cl.GetBlockCount()
	c.Assert(err, NotNil)
}

// ---------------------------------------------------------------------------
// GetBlockHash tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetBlockHash(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getblockhash")
		return "0000000000000000000abcdef1234567890abcdef1234567890abcdef1234567", nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	hash, err := cl.GetBlockHash(800000)
	c.Assert(err, IsNil)
	c.Assert(hash, Equals, "0000000000000000000abcdef1234567890abcdef1234567890abcdef1234567")
}

// ---------------------------------------------------------------------------
// GetBlockVerboseTxs tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetBlockVerboseTxs(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getblock")
		return map[string]interface{}{
			"hash":   "abc123",
			"height": 100,
			"tx":     []interface{}{},
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	block, err := cl.GetBlockVerboseTxs("abc123")
	c.Assert(err, IsNil)
	c.Assert(block, NotNil)
	c.Assert(block.Hash, Equals, "abc123")
}

// ---------------------------------------------------------------------------
// GetBlockVerbose tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetBlockVerbose(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getblock")
		return map[string]interface{}{
			"hash":   "abc123",
			"height": 100,
			"tx":     []interface{}{"txid1"},
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	block, err := cl.GetBlockVerbose("abc123")
	c.Assert(err, IsNil)
	c.Assert(block, NotNil)
	c.Assert(block.Hash, Equals, "abc123")
}

// ---------------------------------------------------------------------------
// GetBlockStats tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetBlockStats(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getblockstats")
		return map[string]interface{}{
			"avgfee": 1000,
			"height": 100,
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	stats, err := cl.GetBlockStats("abc123")
	c.Assert(err, IsNil)
	c.Assert(stats, NotNil)
}

// ---------------------------------------------------------------------------
// GetMempoolEntry tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetMempoolEntry(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getmempoolentry")
		return map[string]interface{}{
			"vsize": 200,
			"fees": map[string]interface{}{
				"base": 0.0001,
			},
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	entry, err := cl.GetMempoolEntry("txid123")
	c.Assert(err, IsNil)
	c.Assert(entry, NotNil)
}

// ---------------------------------------------------------------------------
// BatchGetMempoolEntry tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestBatchGetMempoolEntry(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getmempoolentry")
		return map[string]interface{}{
			"vsize": 200,
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	results, errs, err := cl.BatchGetMempoolEntry([]string{"txid1", "txid2"})
	c.Assert(err, IsNil)
	c.Assert(results, HasLen, 2)
	c.Assert(errs, HasLen, 2)
}

func (s *RPCSuite) TestBatchGetMempoolEntryZEC(c *C) {
	log := zerolog.Nop()

	// ZEC uses sequential calls through zec.Client.BatchCall
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return map[string]interface{}{
			"vsize": 150,
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.ZECChain, log)
	c.Assert(err, IsNil)

	results, errs, err := cl.BatchGetMempoolEntry([]string{"txid1"})
	c.Assert(err, IsNil)
	c.Assert(results, HasLen, 1)
	c.Assert(errs, HasLen, 1)
}

// ---------------------------------------------------------------------------
// GetRawMempool tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetRawMempool(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getrawmempool")
		return []string{"txid1", "txid2", "txid3"}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	txids, err := cl.GetRawMempool()
	c.Assert(err, IsNil)
	c.Assert(txids, HasLen, 3)
}

// ---------------------------------------------------------------------------
// GetRawTransactionVerbose tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetRawTransactionVerbose(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getrawtransaction")
		return map[string]interface{}{
			"txid": "abc123",
			"vout": []interface{}{},
			"vin":  []interface{}{},
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	tx, err := cl.GetRawTransactionVerbose("abc123")
	c.Assert(err, IsNil)
	c.Assert(tx, NotNil)
	c.Assert(tx.Txid, Equals, "abc123")
}

// ---------------------------------------------------------------------------
// GetRawTransaction tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetRawTransaction(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getrawtransaction")
		return "0100000001abcdef", nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	rawTx, err := cl.GetRawTransaction("abc123")
	c.Assert(err, IsNil)
	c.Assert(rawTx, Equals, "0100000001abcdef")
}

// ---------------------------------------------------------------------------
// BatchGetRawTransactionVerbose tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestBatchGetRawTransactionVerbose(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getrawtransaction")
		return map[string]interface{}{
			"txid": "tx1",
			"vout": []interface{}{},
			"vin":  []interface{}{},
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	results, errs, err := cl.BatchGetRawTransactionVerbose([]string{"tx1", "tx2"})
	c.Assert(err, IsNil)
	c.Assert(results, HasLen, 2)
	c.Assert(errs, HasLen, 2)
}

func (s *RPCSuite) TestBatchGetRawTransactionVerboseZEC(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return map[string]interface{}{
			"txid": "tx1",
			"vout": []interface{}{},
			"vin":  []interface{}{},
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.ZECChain, log)
	c.Assert(err, IsNil)

	results, errs, err := cl.BatchGetRawTransactionVerbose([]string{"tx1"})
	c.Assert(err, IsNil)
	c.Assert(results, HasLen, 1)
	c.Assert(errs, HasLen, 1)
}

// ---------------------------------------------------------------------------
// SendRawTransaction tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestSendRawTransaction(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "sendrawtransaction")
		return "txid_result_123", nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	txData := []byte{0x01, 0x02, 0x03}
	tx := &mockTx{data: txData}
	txid, err := cl.SendRawTransaction(tx, 0.1)
	c.Assert(err, IsNil)
	c.Assert(txid, Equals, "txid_result_123")
}

func (s *RPCSuite) TestSendRawTransactionNilTx(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return "txid_nil", nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	txid, err := cl.SendRawTransaction(nil, 0)
	c.Assert(err, IsNil)
	c.Assert(txid, Equals, "txid_nil")
}

func (s *RPCSuite) TestSendRawTransactionSerializeError(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	tx := &mockTx{data: []byte{0x01}, serError: fmt.Errorf("serialize failed")}
	_, err = cl.SendRawTransaction(tx, 0)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "serialize failed")
}

func (s *RPCSuite) TestSendRawTransactionHex(c *C) {
	log := zerolog.Nop()
	var receivedHex string
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		if len(params) > 0 {
			if s, ok := params[0].(string); ok {
				receivedHex = s
			}
		}
		return "txid_hex", nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	data := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	tx := &mockTx{data: data}
	_, err = cl.SendRawTransaction(tx, false)
	c.Assert(err, IsNil)
	c.Assert(receivedHex, Equals, hex.EncodeToString(data))
}

// ---------------------------------------------------------------------------
// ImportAddress tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestImportAddress(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "importaddress")
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	err = cl.ImportAddress("bc1qtest")
	c.Assert(err, IsNil)
}

// ---------------------------------------------------------------------------
// ImportAddressRescan tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestImportAddressRescan(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "importaddress")
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	err = cl.ImportAddressRescan("bc1qtest")
	c.Assert(err, IsNil)
}

// ---------------------------------------------------------------------------
// CreateWallet tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestCreateWallet(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "createwallet")
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	err = cl.CreateWallet("test-wallet")
	c.Assert(err, IsNil)
}

func (s *RPCSuite) TestCreateWalletAlreadyExistsZEC(c *C) {
	log := zerolog.Nop()

	// ZEC path uses zec.Client which goes through HTTP directly.
	// Create a server that returns -4 error via HTTP with proper JSON-RPC error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a non-200 status with a JSON body containing code -4
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":-4,"message":"wallet already exists"}}`))
	}))
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.ZECChain, log)
	c.Assert(err, IsNil)

	// The zec client will get a non-200 response, the error goes through extractBTCError
	// but the format won't produce a *btcjson.RPCError via extractBTCError since it's from zec client
	err = cl.CreateWallet("existing-wallet")
	c.Assert(err, NotNil) // ZEC client returns error on non-200
}

func (s *RPCSuite) TestCreateWalletAlreadyExistsExtractBTCError(c *C) {
	// Test the CreateWallet error suppression logic:
	// extractBTCError returns a *btcjson.RPCError, which CreateWallet checks for code -4
	rpcErr := btcjson.NewRPCError(btcjson.ErrRPCWallet, "wallet already exists")
	c.Assert(rpcErr.Code, Equals, btcjson.ErrRPCWallet)

	// Verify extractBTCError can produce btcjson.RPCError from a formatted error string
	err := extractBTCError(fmt.Errorf(`500 Internal Server Error: {"error":{"code":-4,"message":"wallet already exists"}}`))
	rpcErr2, ok := err.(*btcjson.RPCError)
	c.Assert(ok, Equals, true)
	c.Assert(rpcErr2.Code, Equals, btcjson.ErrRPCWallet)
}

func (s *RPCSuite) TestCreateWalletOtherError(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, btcjson.NewRPCError(btcjson.ErrRPCInternal.Code, "internal error")
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	err = cl.CreateWallet("test")
	c.Assert(err, NotNil)
}

// ---------------------------------------------------------------------------
// GetZecAddressUTXOs tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetZecAddressUTXOsPreservesRawHeightAndIntegerValue(c *C) {
	// The RPC adapter preserves even an out-of-supply value exactly; monetary
	// validation belongs to the selector. This odd value detects float64 loss.
	const exactValue = uint64(9007199254740993)
	expected := []ZecAddressUTXO{
		{Txid: strings.Repeat("AB", 32), Address: "t1vault", Index: 3, Script: "76A914ABC88AC", Satoshis: exactValue, Height: 3500001},
		{Txid: strings.Repeat("cd", 32), Address: "t1vault", Index: 0, Script: "76a914abc88ac", Satoshis: 1, Height: -1},
		{Txid: strings.Repeat("ef", 32), Address: "t1vault", Index: 2, Script: "76a914abc88ac", Satoshis: 123456789, Height: 0},
	}
	var methods []string
	server := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		methods = append(methods, method)
		c.Assert(method, Equals, "getaddressutxos")
		c.Assert(params, DeepEquals, []interface{}{"t1vault"})
		return expected, nil
	})
	defer server.Close()
	client, err := NewClient(server.URL, "user", "pass", 0, time.Second, common.ZECChain, zerolog.Nop())
	c.Assert(err, IsNil)
	result, err := client.GetZecAddressUTXOs("t1vault")
	c.Assert(err, IsNil)
	c.Assert(result, DeepEquals, expected)
	// No tip query or derived confirmation count may rewrite creation heights.
	c.Assert(methods, DeepEquals, []string{"getaddressutxos"})
}

func (s *RPCSuite) TestGetZecAddressUTXOsPropagatesRPCAndDecodeErrors(c *C) {
	for _, response := range []struct {
		name   string
		result interface{}
		rpcErr *btcjson.RPCError
		match  string
	}{
		{"RPC failure", nil, btcjson.NewRPCError(btcjson.ErrRPCInternal.Code, "utxo index unavailable"), ".*utxo index unavailable.*"},
		{"negative value", json.RawMessage(`[{"txid":"bad","satoshis":-1,"height":3500000}]`), nil, ".*cannot unmarshal.*"},
		{"fractional value", json.RawMessage(`[{"txid":"bad","satoshis":1.5,"height":3500000}]`), nil, ".*cannot unmarshal.*"},
	} {
		server := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
			c.Assert(method, Equals, "getaddressutxos")
			return response.result, response.rpcErr
		})
		client, err := NewClient(server.URL, "user", "pass", 0, time.Second, common.ZECChain, zerolog.Nop())
		c.Assert(err, IsNil)
		_, err = client.GetZecAddressUTXOs("t1vault")
		c.Assert(err, ErrorMatches, response.match, Commentf("%s", response.name))
		server.Close()
	}
}

func (s *RPCSuite) TestGetZecAddressUTXOsRejectsOtherChainsWithoutRPC(c *C) {
	calls := 0
	server := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		calls++
		return nil, nil
	})
	defer server.Close()
	client, err := NewClient(server.URL, "user", "pass", 0, time.Second, common.BTCChain, zerolog.Nop())
	c.Assert(err, IsNil)
	result, err := client.GetZecAddressUTXOs("bc1vault")
	c.Assert(err, ErrorMatches, "ZEC address UTXOs requested for BTC")
	c.Assert(result, IsNil)
	c.Assert(calls, Equals, 0)
}

// ---------------------------------------------------------------------------
// ListUnspent tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestListUnspent(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "listunspent")
		return []map[string]interface{}{
			{
				"txid":          "txid1",
				"vout":          0,
				"address":       "bc1qtest",
				"scriptPubKey":  "0014abc",
				"amount":        1.5,
				"confirmations": 10,
				"spendable":     true,
			},
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	unspent, err := cl.ListUnspent("bc1qtest")
	c.Assert(err, IsNil)
	c.Assert(unspent, HasLen, 1)
	c.Assert(unspent[0].TxID, Equals, "txid1")
}

func (s *RPCSuite) TestListUnspentZEC(c *C) {
	log := zerolog.Nop()
	callCount := 0
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		callCount++
		switch method {
		case "getblockcount":
			return 1000, nil
		case "getaddressutxos":
			return []map[string]interface{}{
				{
					"txid":        "zectx1",
					"address":     "t1test",
					"outputIndex": 0,
					"script":      "76a914abc88ac",
					"satoshis":    100000000,
					"height":      990,
				},
			}, nil
		}
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.ZECChain, log)
	c.Assert(err, IsNil)

	unspent, err := cl.ListUnspent("t1test")
	c.Assert(err, IsNil)
	c.Assert(unspent, HasLen, 1)
	c.Assert(unspent[0].TxID, Equals, "zectx1")
	c.Assert(unspent[0].Amount, Equals, 1.0)
	c.Assert(unspent[0].Confirmations, Equals, int64(10))
	c.Assert(unspent[0].Spendable, Equals, true)
}

func (s *RPCSuite) TestListUnspentZECBlockCountError(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		if method == "getblockcount" {
			return nil, btcjson.NewRPCError(btcjson.ErrRPCInternal.Code, "block count error")
		}
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.ZECChain, log)
	c.Assert(err, IsNil)

	_, err = cl.ListUnspent("t1test")
	c.Assert(err, NotNil)
}

// ---------------------------------------------------------------------------
// GetNetworkInfo tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetNetworkInfo(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getnetworkinfo")
		return map[string]interface{}{
			"version":         210100,
			"subversion":      "/Satoshi:21.1.0/",
			"protocolversion": 70016,
			"relayfee":        0.00001,
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	info, err := cl.GetNetworkInfo()
	c.Assert(err, IsNil)
	c.Assert(info, NotNil)
	c.Assert(info.Version, Equals, int32(210100))
}

// ---------------------------------------------------------------------------
// Call routing tests (BTC vs ZEC)
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestCallZEC(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return 12345, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.ZECChain, log)
	c.Assert(err, IsNil)

	var count int64
	err = cl.Call(&count, "getblockcount")
	c.Assert(err, IsNil)
	c.Assert(count, Equals, int64(12345))
}

// ---------------------------------------------------------------------------
// extractBTCError tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestExtractBTCErrorNil(c *C) {
	c.Assert(extractBTCError(nil), IsNil)
}

func (s *RPCSuite) TestExtractBTCErrorNonRPC(c *C) {
	err := extractBTCError(errors.New("simple error without colon"))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "simple error without colon")
}

func (s *RPCSuite) TestExtractBTCErrorWithRPCJSON(c *C) {
	err := extractBTCError(fmt.Errorf("404 Not Found: %s", `{"error":{"code":-32601,"message":"Method not found"}}`))
	c.Assert(err, NotNil)
	rpcErr, ok := err.(*btcjson.RPCError)
	c.Assert(ok, Equals, true)
	c.Assert(rpcErr.Code, Equals, btcjson.RPCErrorCode(-32601))
	c.Assert(rpcErr.Message, Equals, "Method not found")
}

func (s *RPCSuite) TestExtractBTCErrorInvalidJSON(c *C) {
	err := extractBTCError(fmt.Errorf("500 Internal Server Error: not json"))
	c.Assert(err, NotNil)
	// Should return the original error since JSON parsing failed
	c.Assert(err.Error(), Equals, "500 Internal Server Error: not json")
}

// ---------------------------------------------------------------------------
// retry tests (expanded)
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestRetry(c *C) {
	cl := Client{maxRetries: 3}
	called := 0
	err := cl.retry(func() error {
		called++
		return nil
	})
	c.Assert(err, IsNil)
	c.Assert(called, Equals, 1)

	called = 0
	err = cl.retry(func() error {
		called++
		return errors.New("error")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 1)

	called = 0
	c.Assert(s.backoffs, HasLen, 0)

	err = cl.retry(func() error {
		called++
		return errors.New("500 Internal Server Error: work queue depth exceeded")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 4)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second})

	called = 0
	s.backoffs = nil
	err = cl.retry(func() error {
		called++
		if called < 2 {
			return errors.New("500 Internal Server Error: work queue depth exceeded")
		}
		return nil
	})
	c.Assert(err, IsNil)
	c.Assert(called, Equals, 2)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second})
}

// The backoff starts at 1s and doubles per retry, up to 10s, and outside the
// tests retry sleeps it.
func (s *RPCSuite) TestRetryBackoff(c *C) {
	cl := Client{maxRetries: 6, log: zerolog.Nop()}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("503 Service Unavailable: server overloaded")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 7)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second,
	})
	c.Assert(reflect.ValueOf(realRetrySleep).Pointer(), Equals, reflect.ValueOf(time.Sleep).Pointer())
}

func (s *RPCSuite) TestRetryConnectionRefused(c *C) {
	cl := Client{maxRetries: 1, log: zerolog.Nop()}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("dial tcp 127.0.0.1:8332: connect: connection refused")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 2)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second})
}

func (s *RPCSuite) TestRetryEOF(c *C) {
	cl := Client{maxRetries: 1, log: zerolog.Nop()}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("Post http://localhost:8332: EOF")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 2)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second})
}

func (s *RPCSuite) TestRetryLoadingBlockIndex(c *C) {
	cl := Client{maxRetries: 1, log: zerolog.Nop()}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("loading block index")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 2)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second})
}

func (s *RPCSuite) TestRetryVerifyingWallet(c *C) {
	cl := Client{maxRetries: 1, log: zerolog.Nop()}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("verifying wallet")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 2)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second})
}

func (s *RPCSuite) TestRetryRescanning(c *C) {
	cl := Client{maxRetries: 1, log: zerolog.Nop()}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("currently rescanning")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 2)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second})
}

func (s *RPCSuite) TestRetry503(c *C) {
	cl := Client{maxRetries: 1, log: zerolog.Nop()}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("503 Service Unavailable: server overloaded")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 2)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second})
}

func (s *RPCSuite) TestRetryZeroRetries(c *C) {
	cl := Client{maxRetries: 0}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("connect: connection refused")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 1) // no retries, just the initial call
	c.Assert(s.backoffs, HasLen, 0)
}

// ---------------------------------------------------------------------------
// Batch error path tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestBatchGetMempoolEntryServerError(c *C) {
	log := zerolog.Nop()
	srv := newErrorServer(500)
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	_, _, err = cl.BatchGetMempoolEntry([]string{"tx1"})
	c.Assert(err, NotNil)
}

func (s *RPCSuite) TestBatchGetRawTransactionVerboseServerError(c *C) {
	log := zerolog.Nop()
	srv := newErrorServer(500)
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	_, _, err = cl.BatchGetRawTransactionVerbose([]string{"tx1"})
	c.Assert(err, NotNil)
}

// ---------------------------------------------------------------------------
// Batch with individual errors
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestBatchGetMempoolEntryWithPerItemError(c *C) {
	log := zerolog.Nop()
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		defer r.Body.Close()

		var batchReqs []jsonRPCRequest
		if err := json.Unmarshal(body, &batchReqs); err == nil && len(batchReqs) > 0 {
			var responses []json.RawMessage
			for _, req := range batchReqs {
				callNum++
				resp := map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      req.ID,
				}
				if callNum == 1 {
					resp["result"] = map[string]interface{}{"vsize": 200}
				} else {
					resp["error"] = map[string]interface{}{
						"code":    -5,
						"message": "Transaction not in mempool",
					}
				}
				raw, _ := json.Marshal(resp)
				responses = append(responses, raw)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(responses)
			return
		}
	}))
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	results, errs, err := cl.BatchGetMempoolEntry([]string{"tx1", "tx2"})
	c.Assert(err, IsNil)
	c.Assert(results, HasLen, 2)
	c.Assert(errs[0], IsNil)
	c.Assert(errs[1], NotNil)
}

// ---------------------------------------------------------------------------
// SendRawTransaction with RPC error
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestSendRawTransactionRPCError(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, btcjson.NewRPCError(btcjson.ErrRPCVerify, "TX rejected")
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	tx := &mockTx{data: []byte{0x01}}
	_, err = cl.SendRawTransaction(tx, 0)
	c.Assert(err, NotNil)
}

// ---------------------------------------------------------------------------
// Serialize helper for SendRawTransaction encoding
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestSerializableTxInterface(c *C) {
	data := []byte{0x01, 0x02, 0x03, 0x04}
	tx := &mockTx{data: data}
	c.Assert(tx.SerializeSize(), Equals, 4)

	var buf bytes.Buffer
	err := tx.Serialize(&buf)
	c.Assert(err, IsNil)
	c.Assert(buf.Bytes(), DeepEquals, data)
}

// ---------------------------------------------------------------------------
// ZecClientTimeout constant
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestZecClientTimeout(c *C) {
	c.Assert(ZecClientTimeout, Equals, time.Second*10)
}

// ---------------------------------------------------------------------------
// mockUTXOReader is a minimal UTXOReader for fallback tests
// ---------------------------------------------------------------------------

type mockUTXOReader struct {
	getBlockCountFn func() (int64, error)
	callFn          func(result any, method string, args ...interface{}) error
	batchRawTxFn    func(txids []string) ([]*btcjson.TxRawResult, []error, error)
	batchMempoolFn  func(txids []string) ([]*btcjson.GetMempoolEntryResult, []error, error)
	called          bool
}

var errMockNotSupported = errors.New("not supported by mock")

func (m *mockUTXOReader) GetBlockCount() (int64, error) {
	m.called = true
	if m.getBlockCountFn != nil {
		return m.getBlockCountFn()
	}
	return 0, errMockNotSupported
}

func (m *mockUTXOReader) GetBlockHash(_ int64) (string, error) {
	return "", errMockNotSupported
}

func (m *mockUTXOReader) GetBlockVerboseTxs(_ string) (*btcjson.GetBlockVerboseTxResult, error) {
	return nil, errMockNotSupported
}

func (m *mockUTXOReader) GetBlockVerbose(_ string) (*btcjson.GetBlockVerboseResult, error) {
	return nil, errMockNotSupported
}

func (m *mockUTXOReader) GetBlockStats(_ string) (*btcjson.GetBlockStatsResult, error) {
	return nil, errMockNotSupported
}

func (m *mockUTXOReader) GetMempoolEntry(_ string) (*btcjson.GetMempoolEntryResult, error) {
	return nil, errMockNotSupported
}

func (m *mockUTXOReader) BatchGetMempoolEntry(txids []string) ([]*btcjson.GetMempoolEntryResult, []error, error) {
	m.called = true
	if m.batchMempoolFn != nil {
		return m.batchMempoolFn(txids)
	}
	return nil, nil, errMockNotSupported
}

func (m *mockUTXOReader) GetRawMempool() ([]string, error) {
	return nil, errMockNotSupported
}

func (m *mockUTXOReader) GetRawTransactionVerbose(_ string) (*btcjson.TxRawResult, error) {
	return nil, errMockNotSupported
}

func (m *mockUTXOReader) GetRawTransaction(_ string) (string, error) {
	return "", errMockNotSupported
}

func (m *mockUTXOReader) BatchGetRawTransactionVerbose(txids []string) ([]*btcjson.TxRawResult, []error, error) {
	m.called = true
	if m.batchRawTxFn != nil {
		return m.batchRawTxFn(txids)
	}
	return nil, nil, errMockNotSupported
}

func (m *mockUTXOReader) GetNetworkInfo() (*btcjson.GetNetworkInfoResult, error) {
	return nil, errMockNotSupported
}

func (m *mockUTXOReader) Call(result any, method string, args ...interface{}) error {
	m.called = true
	if m.callFn != nil {
		return m.callFn(result, method, args...)
	}
	return errMockNotSupported
}

func (m *mockUTXOReader) BatchCall(_ []ethrpc.BatchElem) error {
	return errMockNotSupported
}

// ---------------------------------------------------------------------------
// NewClientWithFallbacks / withFallback tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestFallbackTriedOnPrimaryFailure(c *C) {
	srv := newErrorServer(500)
	defer srv.Close()

	mock := &mockUTXOReader{
		getBlockCountFn: func() (int64, error) { return 12345, nil },
	}

	cl, err := NewClientWithFallbacks(srv.URL, "u", "p", "", 0, time.Second*10, common.BTCChain, zerolog.Nop(), []UTXOReader{mock}, 0)
	c.Assert(err, IsNil)

	count, err := cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(count, Equals, int64(12345))
	c.Assert(mock.called, Equals, true)
}

func (s *RPCSuite) TestFallbackNotTriedOnSuccess(c *C) {
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return 800000, nil
	})
	defer srv.Close()

	mock := &mockUTXOReader{}

	cl, err := NewClientWithFallbacks(srv.URL, "u", "p", "", 0, time.Second*10, common.BTCChain, zerolog.Nop(), []UTXOReader{mock}, 0)
	c.Assert(err, IsNil)

	count, err := cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(count, Equals, int64(800000))
	c.Assert(mock.called, Equals, false)
}

func (s *RPCSuite) TestFallbackNotTriedOnRateLimit(c *C) {
	srv := newErrorServer(429)
	defer srv.Close()

	mock := &mockUTXOReader{
		getBlockCountFn: func() (int64, error) { return 99999, nil },
	}

	cl, err := NewClientWithFallbacks(srv.URL, "u", "p", "", 0, time.Second*10, common.BTCChain, zerolog.Nop(), []UTXOReader{mock}, 0)
	c.Assert(err, IsNil)

	_, err = cl.GetBlockCount()
	c.Assert(err, NotNil)
	c.Assert(mock.called, Equals, false)
}

func (s *RPCSuite) TestAllFallbacksFailReturnsError(c *C) {
	srv := newErrorServer(500)
	defer srv.Close()

	mock := &mockUTXOReader{
		getBlockCountFn: func() (int64, error) { return 0, errors.New("fallback also failed") },
	}

	cl, err := NewClientWithFallbacks(srv.URL, "u", "p", "", 0, time.Second*10, common.BTCChain, zerolog.Nop(), []UTXOReader{mock}, 0)
	c.Assert(err, IsNil)

	_, err = cl.GetBlockCount()
	c.Assert(err, NotNil)
	c.Assert(mock.called, Equals, true)
}

// blockStatsFloat mirrors the BCH-specific struct used for getblockstats, whose
// avgfeerate is a float (unlike btcjson.GetBlockStatsResult's int64).
type blockStatsFloat struct {
	AverageFeeRate float64 `json:"avgfeerate"`
}

// CallWithFallback must fail over to a read fallback when the primary errors on a
// generic JSON-RPC read (e.g. BCH getblockstats).
func (s *RPCSuite) TestCallWithFallbackTriedOnPrimaryFailure(c *C) {
	srv := newErrorServer(500)
	defer srv.Close()

	mock := &mockUTXOReader{
		callFn: func(result any, method string, args ...interface{}) error {
			bs, ok := result.(*blockStatsFloat)
			c.Assert(ok, Equals, true)
			c.Assert(method, Equals, "getblockstats")
			bs.AverageFeeRate = 1.5
			return nil
		},
	}

	cl, err := NewClientWithFallbacks(srv.URL, "u", "p", "", 0, time.Second*10, common.BCHChain, zerolog.Nop(), []UTXOReader{mock}, 0)
	c.Assert(err, IsNil)

	var bs blockStatsFloat
	err = cl.CallWithFallback(&bs, "getblockstats", "somehash")
	c.Assert(err, IsNil)
	c.Assert(bs.AverageFeeRate, Equals, 1.5)
	c.Assert(mock.called, Equals, true)
}

// CallWithFallback must not consult fallbacks when the primary succeeds.
func (s *RPCSuite) TestCallWithFallbackNotTriedOnSuccess(c *C) {
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return map[string]interface{}{"avgfeerate": 2.5}, nil
	})
	defer srv.Close()

	mock := &mockUTXOReader{}

	cl, err := NewClientWithFallbacks(srv.URL, "u", "p", "", 0, time.Second*10, common.BCHChain, zerolog.Nop(), []UTXOReader{mock}, 0)
	c.Assert(err, IsNil)

	var bs blockStatsFloat
	err = cl.CallWithFallback(&bs, "getblockstats", "somehash")
	c.Assert(err, IsNil)
	c.Assert(bs.AverageFeeRate, Equals, 2.5)
	c.Assert(mock.called, Equals, false)
}

// CallWithFallback must back off (not fail over) on HTTP 429.
func (s *RPCSuite) TestCallWithFallbackNotTriedOnRateLimit(c *C) {
	srv := newErrorServer(429)
	defer srv.Close()

	mock := &mockUTXOReader{}

	cl, err := NewClientWithFallbacks(srv.URL, "u", "p", "", 0, time.Second*10, common.BCHChain, zerolog.Nop(), []UTXOReader{mock}, 0)
	c.Assert(err, IsNil)

	var bs blockStatsFloat
	err = cl.CallWithFallback(&bs, "getblockstats", "somehash")
	c.Assert(err, NotNil)
	c.Assert(mock.called, Equals, false)
}

// ---------------------------------------------------------------------------
// Auth (Basic / Bearer) tests
// ---------------------------------------------------------------------------

// newAuthCapturingServer records the Authorization header of the last request and
// answers getblockcount so a read can complete.
func newAuthCapturingServer(captured *string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*captured = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var req jsonRPCRequest
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0", "id": req.ID, "result": 800000,
		})
	}))
}

// A bearer token must be sent as Authorization: Bearer.
func (s *RPCSuite) TestNewClientWithAuthBearer(c *C) {
	var got string
	srv := newAuthCapturingServer(&got)
	defer srv.Close()

	cl, err := NewClientWithAuth(srv.URL, "", "", "tok123", 0, time.Second*10, common.BTCChain, zerolog.Nop())
	c.Assert(err, IsNil)
	_, err = cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(got, Equals, "Bearer tok123")
}

// With no bearer, Basic auth is sent from user/password (legacy behavior).
func (s *RPCSuite) TestNewClientWithAuthBasicWhenNoBearer(c *C) {
	var got string
	srv := newAuthCapturingServer(&got)
	defer srv.Close()

	cl, err := NewClientWithAuth(srv.URL, "user", "pass", "", 0, time.Second*10, common.BTCChain, zerolog.Nop())
	c.Assert(err, IsNil)
	_, err = cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(got, Equals, "Basic "+base64.StdEncoding.EncodeToString([]byte("user:pass")))
}

// Bearer takes precedence when both bearer and basic creds are set (mirrors EVM).
func (s *RPCSuite) TestNewClientWithAuthBearerPrecedence(c *C) {
	var got string
	srv := newAuthCapturingServer(&got)
	defer srv.Close()

	cl, err := NewClientWithAuth(srv.URL, "user", "pass", "tok123", 0, time.Second*10, common.BTCChain, zerolog.Nop())
	c.Assert(err, IsNil)
	_, err = cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(got, Equals, "Bearer tok123")
}

// The legacy NewClient wrapper still sends Basic auth unchanged.
func (s *RPCSuite) TestNewClientWrapperStillBasic(c *C) {
	var got string
	srv := newAuthCapturingServer(&got)
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, zerolog.Nop())
	c.Assert(err, IsNil)
	_, err = cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(got, Equals, "Basic "+base64.StdEncoding.EncodeToString([]byte("user:pass")))
}

// Empty credentials with no bearer preserve the legacy empty-Basic header.
func (s *RPCSuite) TestNewClientWithAuthEmptyCreds(c *C) {
	var got string
	srv := newAuthCapturingServer(&got)
	defer srv.Close()

	cl, err := NewClientWithAuth(srv.URL, "", "", "", 0, time.Second*10, common.BTCChain, zerolog.Nop())
	c.Assert(err, IsNil)
	_, err = cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(got, Equals, "Basic "+base64.StdEncoding.EncodeToString([]byte(":")))
}

// ZEC uses a different transport (zec.NewClient with a pre-populated header); verify the
// bearer token reaches the wire there too.
func (s *RPCSuite) TestNewClientWithAuthBearerZEC(c *C) {
	var got string
	srv := newAuthCapturingServer(&got)
	defer srv.Close()

	cl, err := NewClientWithAuth(srv.URL, "", "", "zectok", 0, time.Second*10, common.ZECChain, zerolog.Nop())
	c.Assert(err, IsNil)
	_, err = cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(got, Equals, "Bearer zectok")
}

func (s *RPCSuite) TestIsRateLimitError(c *C) {
	c.Assert(isRateLimitError(nil), Equals, false)
	c.Assert(isRateLimitError(errors.New("429 too many requests")), Equals, true)
	c.Assert(isRateLimitError(errors.New("secondary endpoint returned status 429: rate limited")), Equals, true)
	c.Assert(isRateLimitError(errors.New("500 internal server error")), Equals, false)
}

// ---------------------------------------------------------------------------
// Mock rpcClient – asserts BatchCallContext is used (not BatchCall)
// ---------------------------------------------------------------------------

type mockRPCClient struct {
	batchCallCalled        bool
	batchCallContextCalled bool
	lastCtx                context.Context
}

func (m *mockRPCClient) Call(result any, method string, args ...any) error {
	return nil
}

func (m *mockRPCClient) CallContext(ctx context.Context, result any, method string, args ...any) error {
	return nil
}

func (m *mockRPCClient) BatchCall(batch []ethrpc.BatchElem) error {
	m.batchCallCalled = true
	return nil
}

func (m *mockRPCClient) BatchCallContext(ctx context.Context, batch []ethrpc.BatchElem) error {
	m.batchCallContextCalled = true
	m.lastCtx = ctx
	// Populate results so the callers don't fail on type assertions.
	for i := range batch {
		switch batch[i].Result.(type) {
		case *btcjson.GetMempoolEntryResult:
			batch[i].Result = &btcjson.GetMempoolEntryResult{}
		case *btcjson.TxRawResult:
			batch[i].Result = &btcjson.TxRawResult{}
		}
	}
	return nil
}

// TestBatchGetMempoolEntryUsesBatchCallContext verifies that BatchGetMempoolEntry
// dispatches via BatchCallContext (not BatchCall) and passes a context with a
// deadline when the client has a positive timeout.
func (s *RPCSuite) TestBatchGetMempoolEntryUsesBatchCallContext(c *C) {
	mock := &mockRPCClient{}
	cl := &Client{
		client:  mock,
		log:     zerolog.Nop(),
		chain:   common.BTCChain,
		timeout: 10 * time.Second,
	}

	_, _, err := cl.BatchGetMempoolEntry([]string{"txid1"})
	c.Assert(err, IsNil)

	c.Assert(mock.batchCallCalled, Equals, false)
	c.Assert(mock.batchCallContextCalled, Equals, true)

	deadline, ok := mock.lastCtx.Deadline()
	c.Assert(ok, Equals, true)
	c.Assert(deadline.After(time.Now()), Equals, true)
}

// TestBatchGetRawTransactionVerboseUsesBatchCallContext verifies that
// BatchGetRawTransactionVerbose dispatches via BatchCallContext (not BatchCall)
// and passes a context with a deadline when the client has a positive timeout.
func (s *RPCSuite) TestBatchGetRawTransactionVerboseUsesBatchCallContext(c *C) {
	mock := &mockRPCClient{}
	cl := &Client{
		client:  mock,
		log:     zerolog.Nop(),
		chain:   common.BTCChain,
		timeout: 10 * time.Second,
	}

	_, _, err := cl.BatchGetRawTransactionVerbose([]string{"tx1"})
	c.Assert(err, IsNil)

	c.Assert(mock.batchCallCalled, Equals, false)
	c.Assert(mock.batchCallContextCalled, Equals, true)

	deadline, ok := mock.lastCtx.Deadline()
	c.Assert(ok, Equals, true)
	c.Assert(deadline.After(time.Now()), Equals, true)
}

// TestGetNetworkInfoWarnings verifies getnetworkinfo decodes on both the pre-28
// string "warnings" and the Core 28/29 array "warnings" without dropping RelayFee.
func (s *RPCSuite) TestGetNetworkInfoWarnings(c *C) {
	log := zerolog.Nop()
	cases := []struct {
		name     string
		warnings interface{}
	}{
		{"core<=27 string", "some warning"},
		{"core 28/29 array", []string{"warn a", "warn b"}},
		{"core 28/29 empty array", []string{}},
	}
	for _, tc := range cases {
		srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
			c.Assert(method, Equals, "getnetworkinfo")
			return map[string]interface{}{
				"relayfee": 0.00001,
				"warnings": tc.warnings,
			}, nil
		})

		cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
		c.Assert(err, IsNil)

		info, err := cl.GetNetworkInfo()
		c.Assert(err, IsNil, Commentf("case %s", tc.name))
		c.Assert(info, NotNil)
		c.Assert(info.RelayFee, Equals, 0.00001, Commentf("case %s: RelayFee must survive decode", tc.name))
		srv.Close()
	}
}

// TestFlexibleWarningsUnmarshal covers the raw decode of the warnings field.
func (s *RPCSuite) TestFlexibleWarningsUnmarshal(c *C) {
	var w flexibleWarnings
	c.Assert(json.Unmarshal([]byte(`"hi"`), &w), IsNil)
	c.Assert([]string(w), DeepEquals, []string{"hi"})

	w = nil
	c.Assert(json.Unmarshal([]byte(`["a","b"]`), &w), IsNil)
	c.Assert([]string(w), DeepEquals, []string{"a", "b"})

	w = nil
	c.Assert(json.Unmarshal([]byte(`""`), &w), IsNil)
	c.Assert(len(w), Equals, 0)

	w = nil
	c.Assert(json.Unmarshal([]byte(`null`), &w), IsNil)
	c.Assert(len(w), Equals, 0)

	w = nil
	c.Assert(json.Unmarshal([]byte(`123`), &w), NotNil)
}

// ---------------------------------------------------------------------------
// endpoint-manager-aware fallback tests
// ---------------------------------------------------------------------------

// newPlainErrorServer answers with the status and a non-JSON body: an endpoint
// failure, unlike newErrorServer whose JSON-RPC error body is an application answer
// (Bitcoin Core before 28 reports every RPC error with HTTP 500).
func newPlainErrorServer(statusCode int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad gateway", statusCode)
	}))
}

func mgrFor(c *C, threshold int, hosts map[string]string, names ...string) (*endpoints.Manager, []config.ChainEndpointConfig) {
	var cfgEps []config.ChainEndpointConfig
	for i, n := range names {
		host := hosts[n]
		if host == "" {
			host = "http://" + n
		}
		cfgEps = append(cfgEps, config.ChainEndpointConfig{Name: n, RPCHost: host, Priority: i + 1})
	}
	m, err := endpoints.NewManager(common.BTCChain, cfgEps, config.ChainFailoverConfig{
		ErrorThreshold: threshold, ErrorWindow: time.Minute, MinRecoveryChecks: 1, RateLimitBackoff: time.Minute,
	}, zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range m.Entries() {
		e.SetIdentity("genesis")
	}
	return m, cfgEps
}

func (s *RPCSuite) TestManagerSkipsUnhealthyPrimaryWithoutRetrying(c *C) {
	var primaryHits int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&primaryHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer primary.Close()
	fb := &mockUTXOReader{getBlockCountFn: func() (int64, error) { return 7, nil }}
	m, cfgEps := mgrFor(c, 1, map[string]string{"primary": primary.URL}, "primary", "fb")
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 2, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{fb}, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)

	n, err := cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(n, Equals, int64(7))
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateUnhealthy)
	hitsAfterFirst := atomic.LoadInt32(&primaryHits)
	c.Assert(hitsAfterFirst > 0, Equals, true)

	// second read: primary is unhealthy, must not be touched at all
	n, err = cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(n, Equals, int64(7))
	c.Assert(atomic.LoadInt32(&primaryHits), Equals, hitsAfterFirst)
}

func (s *RPCSuite) TestManagerSkipsUnhealthyFallback(c *C) {
	primary := newErrorServer(500)
	defer primary.Close()
	bad := &mockUTXOReader{getBlockCountFn: func() (int64, error) { return 1, nil }}
	good := &mockUTXOReader{getBlockCountFn: func() (int64, error) { return 2, nil }}
	m, cfgEps := mgrFor(c, 1, map[string]string{"primary": primary.URL}, "primary", "bad", "good")
	m.RecordError("bad", endpoints.ErrorKindTransport)
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{bad, good}, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	n, err := cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(n, Equals, int64(2))
	c.Assert(bad.called, Equals, false)
}

func (s *RPCSuite) TestAllDownStillTriesPrimaryAndFallbacks(c *C) {
	primary := newMockRPCServer(func(method string, _ []interface{}) (interface{}, *btcjson.RPCError) {
		return 42, nil
	})
	defer primary.Close()
	fb := &mockUTXOReader{}
	m, cfgEps := mgrFor(c, 1, map[string]string{"primary": primary.URL}, "primary", "fb")
	m.RecordError("primary", endpoints.ErrorKindTransport)
	m.RecordError("fb", endpoints.ErrorKindTransport)
	c.Assert(m.AllDown(), Equals, true)
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{fb}, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	n, err := cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(n, Equals, int64(42))
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateHealthy) // inline success recovered it
}

func (s *RPCSuite) TestRateLimitedPrimaryServesFromFallbackAndCoolsDown(c *C) {
	primary := newErrorServer(429)
	defer primary.Close()
	fb := &mockUTXOReader{getBlockCountFn: func() (int64, error) { return 9, nil }}
	m, cfgEps := mgrFor(c, 5, map[string]string{"primary": primary.URL}, "primary", "fb")
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{fb}, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	n, err := cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(n, Equals, int64(9))
	c.Assert(m.Entry("primary").InCooldown(), Equals, true)
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateHealthy)
}

func (s *RPCSuite) TestRateLimitWithoutFallbackStillBacksOff(c *C) {
	primary := newErrorServer(429)
	defer primary.Close()
	m, cfgEps := mgrFor(c, 5, map[string]string{"primary": primary.URL}, "primary")
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), nil, 50*time.Millisecond)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	start := time.Now()
	_, err = cl.GetBlockCount()
	c.Assert(err, NotNil)
	c.Assert(time.Since(start) >= 50*time.Millisecond, Equals, true)
}

func (s *RPCSuite) TestBatchGetRawTransactionVerboseFallsOverWithManager(c *C) {
	primary := newPlainErrorServer(502)
	defer primary.Close()
	fb := &mockUTXOReader{batchRawTxFn: func(txids []string) ([]*btcjson.TxRawResult, []error, error) {
		out := make([]*btcjson.TxRawResult, len(txids))
		for i, id := range txids {
			out[i] = &btcjson.TxRawResult{Txid: id}
		}
		return out, make([]error, len(txids)), nil
	}}
	m, cfgEps := mgrFor(c, 1, map[string]string{"primary": primary.URL}, "primary", "fb")
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{fb}, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	res, errs, err := cl.BatchGetRawTransactionVerbose([]string{"a", "b"})
	c.Assert(err, IsNil)
	c.Assert(errs, HasLen, 2)
	c.Assert(res[1].Txid, Equals, "b")
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateUnhealthy)
}

func (s *RPCSuite) TestBatchGetMempoolEntryRateLimitServesFromFallbackAndCoolsDown(c *C) {
	primary := newErrorServer(429)
	defer primary.Close()
	fb := &mockUTXOReader{batchMempoolFn: func(txids []string) ([]*btcjson.GetMempoolEntryResult, []error, error) {
		return make([]*btcjson.GetMempoolEntryResult, len(txids)), make([]error, len(txids)), nil
	}}
	m, cfgEps := mgrFor(c, 5, map[string]string{"primary": primary.URL}, "primary", "fb")
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{fb}, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	_, _, err = cl.BatchGetMempoolEntry([]string{"a"})
	c.Assert(err, IsNil)
	c.Assert(m.Entry("primary").InCooldown(), Equals, true)
}

func (s *RPCSuite) TestPrimaryOnlyReadsNeverFallBack(c *C) {
	primary := newErrorServer(500)
	defer primary.Close()
	fb := &mockUTXOReader{}
	m, cfgEps := mgrFor(c, 5, map[string]string{"primary": primary.URL + "/SECRET"}, "primary", "fb")
	cl, err := NewClientWithFallbacks(primary.URL+"/SECRET", "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{fb}, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	_, err = cl.GetRawTransactionVerbosePrimary("abc")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Not(Matches), "(?s).*SECRET.*")
	_, err = cl.GetMempoolEntryPrimary("abc")
	c.Assert(err, NotNil)
	c.Assert(fb.called, Equals, false)
}

func (s *RPCSuite) TestWithEndpointManagerRejectsMismatch(c *C) {
	primary := newErrorServer(500)
	defer primary.Close()
	m, cfgEps := mgrFor(c, 5, nil, "primary", "fb")
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), nil, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps) // 2 endpoints for 0 fallbacks: ignored
	_, err = cl.GetBlockCount()
	c.Assert(err, NotNil)
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateHealthy) // manager not attached
}

func (s *RPCSuite) TestApplicationErrorIsNotAnEndpointFailure(c *C) {
	// Bitcoin Core answers "No such mempool transaction" as a JSON-RPC error (-5)
	primary := newMockRPCServer(func(method string, _ []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, btcjson.NewRPCError(-5, "No such mempool transaction")
	})
	defer primary.Close()
	fb := &mockUTXOReader{} // its GetMempoolEntry returns a plain error: an endpoint failure
	m, cfgEps := mgrFor(c, 5, map[string]string{"primary": primary.URL}, "primary", "fb")
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{fb}, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	for i := 0; i < 3; i++ {
		_, err = cl.GetMempoolEntry("abc")
		c.Assert(err, NotNil)
	}
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("primary").Snapshot().InlineErrors, Equals, 0) // application answers are not failures
	c.Assert(m.Entry("fb").Snapshot().InlineErrors, Equals, 3)      // still tried, as in Phase 1, and its plain errors count
}

func (s *RPCSuite) TestReadsFollowManagerOrderAfterFailover(c *C) {
	var primaryHits int32
	primary := newMockRPCServer(func(method string, _ []interface{}) (interface{}, *btcjson.RPCError) {
		atomic.AddInt32(&primaryHits, 1)
		return 1, nil
	})
	defer primary.Close()
	fb := &mockUTXOReader{getBlockCountFn: func() (int64, error) { return 2, nil }}
	m, cfgEps := mgrFor(c, 1, map[string]string{"primary": primary.URL}, "primary", "fb")
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{fb}, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	// evict the primary, then let it recover: active stays on fb (no prefer_higher_priority)
	m.RecordError("primary", endpoints.ErrorKindTransport)
	m.Entry("primary").RecordHealthCheck(true, m.Config())
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Active().Name, Equals, "fb")
	n, err := cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(n, Equals, int64(2)) // served by the active fallback first
	c.Assert(atomic.LoadInt32(&primaryHits), Equals, int32(0))
}

func (s *RPCSuite) TestSuccessfulBatchRecordsSuccess(c *C) {
	var fail atomic.Bool
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// echo the request ids: a batch element whose id is missing from the response
		// is a failure (rpc.ErrMissingBatchResponse), which is exactly what the
		// endpoint accounting must see
		var reqs []struct {
			ID json.RawMessage `json:"id"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &reqs)
		out := make([]string, 0, len(reqs))
		for _, rq := range reqs {
			out = append(out, `{"jsonrpc":"2.0","id":`+string(rq.ID)+`,"result":{"txid":"a"}}`)
		}
		_, _ = w.Write([]byte("[" + strings.Join(out, ",") + "]"))
	}))
	defer primary.Close()
	m, cfgEps := mgrFor(c, 2, map[string]string{"primary": primary.URL}, "primary")
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), nil, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	for i := 0; i < 3; i++ { // alternate failure and success: never two consecutive failures
		fail.Store(true)
		_, _, _ = cl.BatchGetRawTransactionVerbose([]string{"a"})
		fail.Store(false)
		_, _, err = cl.BatchGetRawTransactionVerbose([]string{"a"})
		c.Assert(err, IsNil)
	}
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateHealthy)
	c.Assert(m.Entry("primary").Snapshot().InlineErrors, Equals, 0)
}

func (s *RPCSuite) TestIsRateLimitErrorRecognizesZcashFormat(c *C) {
	c.Assert(isRateLimitError(errors.New("response status: 429")), Equals, true)
	c.Assert(isRateLimitError(errors.New("response status: 429: response body: slow down")), Equals, true)
	c.Assert(isRateLimitError(errors.New("response status: 500")), Equals, false)
}

func (s *RPCSuite) TestJSONRPCEndpointAndRateLimitCodesAreClassified(c *C) {
	code := int32(-32601)
	primary := newMockRPCServer(func(method string, _ []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, btcjson.NewRPCError(btcjson.RPCErrorCode(atomic.LoadInt32(&code)), "nope")
	})
	defer primary.Close()
	fb := &mockUTXOReader{getBlockCountFn: func() (int64, error) { return 5, nil }}
	m, cfgEps := mgrFor(c, 1, map[string]string{"primary": primary.URL}, "primary", "fb")
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{fb}, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	n, err := cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(n, Equals, int64(5))
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateUnhealthy) // -32601: endpoint failure

	m2, cfgEps2 := mgrFor(c, 1, map[string]string{"primary": primary.URL}, "primary", "fb")
	atomic.StoreInt32(&code, -32005)
	cl2, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{fb}, 0)
	c.Assert(err, IsNil)
	cl2.WithEndpointManager(m2, cfgEps2)
	_, err = cl2.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(m2.Entry("primary").State(), Equals, endpoints.StateHealthy)
	c.Assert(m2.Entry("primary").InCooldown(), Equals, true) // -32005: cooldown
}

func (s *RPCSuite) TestDisabledPrimaryRejectsPrimaryOnlyOperations(c *C) {
	primary := newMockRPCServer(func(method string, _ []interface{}) (interface{}, *btcjson.RPCError) {
		return "txid", nil
	})
	defer primary.Close()
	m, cfgEps := mgrFor(c, 5, map[string]string{"primary": primary.URL}, "primary", "fb")
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{&mockUTXOReader{}}, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	_, err = cl.SendRawTransaction(&mockTx{data: []byte{1}}, nil)
	c.Assert(err, IsNil)
	m.Disable("primary", "identity mismatch")
	_, err = cl.SendRawTransaction(&mockTx{data: []byte{1}}, nil)
	c.Assert(err, ErrorMatches, ".*primary BTC endpoint primary is disabled: identity mismatch.*")
	c.Assert(cl.ImportAddress("addr"), ErrorMatches, ".*disabled.*")
	_, err = cl.GetRawTransactionVerbosePrimary("abc")
	c.Assert(err, ErrorMatches, ".*disabled.*")
}

// A primary whose network is not verified yet (its identity probe failed at
// bootstrap, with identity required) must not get primary-only operations either: an
// endpoint that reports no identity is never used, so no broadcast, wallet call or
// verification read reaches it until its probe verifies it.
func (s *RPCSuite) TestUnverifiedPrimaryRejectsPrimaryOnlyOperations(c *C) {
	primary := newMockRPCServer(func(method string, _ []interface{}) (interface{}, *btcjson.RPCError) {
		return "txid", nil
	})
	defer primary.Close()
	cfgEps := []config.ChainEndpointConfig{{Name: "primary", RPCHost: primary.URL, Priority: 1}, {Name: "fb", RPCHost: "http://fb", Priority: 2}}
	m, err := endpoints.NewManager(common.BTCChain, cfgEps, config.ChainFailoverConfig{
		ErrorThreshold: 5, ErrorWindow: time.Minute, MinRecoveryChecks: 1, RateLimitBackoff: time.Minute,
	}, zerolog.Nop())
	c.Assert(err, IsNil)
	m.Entry("fb").SetIdentity("genesis") // only the fallback is verified
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{&mockUTXOReader{}}, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	_, err = cl.SendRawTransaction(&mockTx{data: []byte{1}}, nil)
	c.Assert(err, ErrorMatches, ".*primary BTC endpoint primary is not usable: its network is not verified.*")
	c.Assert(cl.ImportAddress("addr"), ErrorMatches, ".*not verified.*")
	_, err = cl.GetRawTransactionVerbosePrimary("abc")
	c.Assert(err, ErrorMatches, ".*not verified.*")

	// once its probe verifies it, it serves them
	m.Entry("primary").SetIdentity("genesis")
	_, err = cl.SendRawTransaction(&mockTx{data: []byte{1}}, nil)
	c.Assert(err, IsNil)
}

func (s *RPCSuite) TestBatchElementErrorsAreClassified(c *C) {
	// HTTP 200 with a per-element rate limit: the aggregate error is nil, so this
	// used to look like a served batch
	body := `[{"jsonrpc":"2.0","id":1,"result":{"txid":"a"}},{"jsonrpc":"2.0","id":2,"error":{"code":-32005,"message":"limit"}}]`
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer primary.Close()
	fb := &mockUTXOReader{batchRawTxFn: func(txids []string) ([]*btcjson.TxRawResult, []error, error) {
		out := make([]*btcjson.TxRawResult, len(txids))
		for i, id := range txids {
			out[i] = &btcjson.TxRawResult{Txid: id}
		}
		return out, make([]error, len(txids)), nil
	}}
	m, cfgEps := mgrFor(c, 5, map[string]string{"primary": primary.URL}, "primary", "fb")
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{fb}, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	res, _, err := cl.BatchGetRawTransactionVerbose([]string{"a", "b"})
	c.Assert(err, IsNil)
	c.Assert(res[1].Txid, Equals, "b") // served by the fallback, not the throttled primary
	c.Assert(m.Entry("primary").InCooldown(), Equals, true)
}

func (s *RPCSuite) TestRetryLogRedactsEndpointURL(c *C) {
	var buf bytes.Buffer
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "503 Service Unavailable", http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	host := primary.URL + "/v1/SECRETKEY"
	m, cfgEps := mgrFor(c, 5, map[string]string{"primary": host}, "primary")
	cl, err := NewClientWithFallbacks(host, "u", "SECRETPASS", "", 1, time.Second, common.BTCChain, zerolog.New(&buf), nil, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	_, _ = cl.GetBlockCount()
	c.Assert(buf.String(), Matches, "(?s).*retrying.*")
	c.Assert(buf.String(), Not(Matches), "(?s).*SECRET.*")
}

func (s *RPCSuite) TestBatchMissingResponseIsAnEndpointFailure(c *C) {
	// a batch reply that omits an element leaves BatchElem.Error set with a nil
	// aggregate error: the endpoint did not serve the read
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer primary.Close()
	fb := &mockUTXOReader{batchRawTxFn: func(txids []string) ([]*btcjson.TxRawResult, []error, error) {
		out := make([]*btcjson.TxRawResult, len(txids))
		for i, id := range txids {
			out[i] = &btcjson.TxRawResult{Txid: id}
		}
		return out, make([]error, len(txids)), nil
	}}
	m, cfgEps := mgrFor(c, 1, map[string]string{"primary": primary.URL}, "primary", "fb")
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{fb}, 0)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	res, _, err := cl.BatchGetRawTransactionVerbose([]string{"a"})
	c.Assert(err, IsNil)
	c.Assert(res[0].Txid, Equals, "a") // served by the fallback
	c.Assert(m.Entry("primary").State(), Equals, endpoints.StateUnhealthy)
}

func (s *RPCSuite) TestURLCredentialsAreLiftedIntoTheAuthHeader(c *C) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":7}`))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	c.Assert(err, IsNil)
	cl, err := NewClient("http://user:pw@"+u.Host, "", "", 0, time.Second, common.BTCChain, zerolog.Nop())
	c.Assert(err, IsNil)
	_, err = cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(auth, Equals, "Basic dXNlcjpwdw==")
}

func (s *RPCSuite) TestFallbackClientRedactsWithoutAManager(c *C) {
	var buf bytes.Buffer
	// no manager is ever attached to a fallback client, and its own retry logging
	// must still not print the endpoint URL
	cl, err := NewClientWithAuth("http://127.0.0.1:1/v1/SECRETKEY", "u", "SECRETPASS", "", 1, 200*time.Millisecond, common.BTCChain, zerolog.New(&buf))
	c.Assert(err, IsNil)
	_, err = cl.GetBlockCount()
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Not(Matches), "(?s).*SECRET.*")
	c.Assert(buf.String(), Not(Matches), "(?s).*SECRET.*")
}

// Zebra answers through the zec client with *zec.Error: its application answers
// (not found, out of range) count as served, not as endpoint failures.
func TestClassifyRPCErrorRecognisesZecErrors(t *testing.T) {
	if got := classifyRPCError(&zec.Error{Code: -5, Message: "No such mempool or blockchain transaction"}); got != verdictApplication {
		t.Fatalf("zec -5: got verdict %d, want application", got)
	}
	if got := classifyRPCError(&zec.Error{Code: -8, Message: "Block height out of range"}); got != verdictApplication {
		t.Fatalf("zec -8: got verdict %d, want application", got)
	}
	if got := classifyRPCError(&zec.Error{Code: -32601, Message: "Method not found"}); got != verdictEndpointError {
		t.Fatalf("zec -32601: got verdict %d, want endpoint error", got)
	}
	if got := classifyRPCError(fmt.Errorf("wrapped: %w", &zec.Error{Code: -5})); got != verdictApplication {
		t.Fatalf("wrapped zec -5: got verdict %d, want application", got)
	}
}

// A gateway that refuses the request (wrong API key, auth failure) has not served the
// read, whatever JSON it puts in the body: it is charged like any failed endpoint, not
// recorded as a daemon answer. A daemon's own errors keep their codes.
func TestClassifyRPCErrorChargesAGatewayRefusal(t *testing.T) {
	refusal := ethrpc.HTTPError{StatusCode: 403, Status: "403 Forbidden", Body: []byte(`{"message":"Invalid API key"}`)}
	withErrorObject := ethrpc.HTTPError{StatusCode: 401, Status: "401 Unauthorized", Body: []byte(`{"error":{"code":-32000,"message":"unauthorized"}}`)}
	for _, err := range []error{
		refusal,
		extractBTCError(refusal), // as a fallback client returns it
		withErrorObject,
		extractBTCError(withErrorObject),
		ethrpc.HTTPError{StatusCode: 500, Status: "500 Internal Server Error", Body: []byte(`{"message":"upstream error"}`)},
	} {
		if got := classifyRPCError(err); got != verdictFailure {
			t.Fatalf("%v: got verdict %d, want a failure", err, got)
		}
	}
	for _, body := range []string{
		`{"result":null,"error":{"code":-5,"message":"No such mempool or blockchain transaction"},"id":1}`,
		`{"result":null,"error":{"code":-8,"message":"Block height out of range"},"id":1}`,
	} {
		daemon := ethrpc.HTTPError{StatusCode: 500, Status: "500 Internal Server Error", Body: []byte(body)}
		if got := classifyRPCError(daemon); got != verdictApplication {
			t.Fatalf("%s: got verdict %d, want the daemon's answer", body, got)
		}
		if got := classifyRPCError(extractBTCError(daemon)); got != verdictApplication {
			t.Fatalf("extracted %s: got verdict %d, want the daemon's answer", body, got)
		}
	}
}

// A daemon still warming up (RPC_IN_WARMUP, -28) refuses before running the request:
// it has not served the read, so it is charged and the read moves on, like a method it
// cannot serve.
func TestClassifyRPCErrorWarmupIsAnEndpointError(t *testing.T) {
	warmup := ethrpc.HTTPError{StatusCode: 500, Status: "500 Internal Server Error", Body: []byte(`{"result":null,"error":{"code":-28,"message":"Loading block index..."},"id":1}`)}
	for _, err := range []error{warmup, extractBTCError(warmup), btcjson.NewRPCError(btcjson.ErrRPCInWarmup, "Loading wallet...")} {
		if got := classifyRPCError(err); got != verdictEndpointError {
			t.Fatalf("%v: got verdict %d, want an endpoint error", err, got)
		}
	}
}

// An HTTP 429 answered with a generic JSON-RPC error body keeps its rate-limit
// classification through extractBTCError, which the fallback clients return through.
func TestExtractBTCErrorKeepsRateLimit(t *testing.T) {
	err := errors.New(`429 Too Many Requests: {"error":{"code":-1,"message":"server error"},"id":1}`)
	if got := extractBTCError(err); got != err {
		t.Fatalf("429 was rewritten to %v", got)
	}
	if classifyRPCError(extractBTCError(err)) != verdictRateLimit {
		t.Fatal("429 lost its rate-limit classification")
	}
	// ordinary HTTP 500 bodies still decode to the JSON-RPC error
	var rpcErr *btcjson.RPCError
	if !errors.As(extractBTCError(errors.New(`500 Internal Server Error: {"error":{"code":-5,"message":"No such mempool transaction"}}`)), &rpcErr) || rpcErr.Code != -5 {
		t.Fatal("500 body no longer decodes to the JSON-RPC error")
	}
}

// A rate-limited primary costs the caller a rate_limit_backoff sleep only when nothing
// served the read: an application answer from a fallback ("no such transaction") is
// the read served, returned at once.
func (s *RPCSuite) TestRateLimitedPrimaryDoesNotSleepWhenAFallbackAnswered(c *C) {
	primary := newErrorServer(429)
	defer primary.Close()
	fb := &mockUTXOReader{getBlockCountFn: func() (int64, error) {
		return 0, btcjson.NewRPCError(btcjson.ErrRPCInvalidAddressOrKey, "No such mempool or blockchain transaction")
	}}
	m, cfgEps := mgrFor(c, 5, map[string]string{"primary": primary.URL}, "primary", "fb")
	cl, err := NewClientWithFallbacks(primary.URL, "u", "p", "", 0, time.Second, common.BTCChain, zerolog.Nop(), []UTXOReader{fb}, 2*time.Second)
	c.Assert(err, IsNil)
	cl.WithEndpointManager(m, cfgEps)
	start := time.Now()
	_, err = cl.GetBlockCount()
	c.Assert(err, ErrorMatches, "(?s).*No such mempool or blockchain transaction.*")
	c.Assert(time.Since(start) < time.Second, Equals, true)
	c.Assert(m.Entry("primary").InCooldown(), Equals, true)
}
