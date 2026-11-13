package evm

import (
	"bytes"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// nullReceiptGate forwards everything to target except receipt reads, which it
// answers with a well-formed JSON-RPC null (a provider that serves blocks, headers and
// its probes but not receipts) while nulls > 0, counting every receipt read in hits.
// nulls < 0 answers null forever.
func nullReceiptGate(target string, nulls int32, hits *atomic.Int32) *httptest.Server {
	var left atomic.Int32
	left.Store(nulls)
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		if bytes.Contains(body, []byte(`"eth_getBlockReceipts"`)) || bytes.Contains(body, []byte(`"eth_getTransactionReceipt"`)) {
			hits.Add(1)
			if nulls < 0 || left.Add(-1) >= 0 {
				var r struct {
					ID json.RawMessage `json:"id"`
				}
				_ = json.Unmarshal(body, &r)
				rw.Header().Set("Content-Type", "application/json")
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":` + string(r.ID) + `,"result":null}`))
				return
			}
		}
		resp, err := http.Post(target+req.URL.Path, "application/json", bytes.NewReader(body))
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		rw.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		rw.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(rw, resp.Body)
	}))
}

// receiptsScanner builds a BSC scanner whose endpoints are the given gates in front of
// one node that serves block 99 with one transaction and its receipt, and returns the
// block read through the router (from the first endpoint).
func (s *UnstuckTestSuite) receiptsScanner(c *C, gates func(node string) []config.ChainEndpointConfig) (*EVMScanner, *etypes.Block, func()) {
	config.Init()
	to := ecommon.HexToAddress("0x9f4aab49a9cd8fc54dcb3701846f608a6f2c44da")
	tx := etypes.NewTx(&etypes.LegacyTx{Nonce: 0, To: &to, Value: big.NewInt(1), Gas: 21000, GasPrice: big.NewInt(1)})
	txJSON, err := json.Marshal(tx)
	c.Assert(err, IsNil)
	blockAnswer := strings.Replace(strings.Replace(reorgBlockJSON,
		`"transactions":[]`, `"transactions":[`+string(txJSON)+`]`, 1),
		`"transactionsRoot":"0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"`,
		`"transactionsRoot":"0x`+strings.Repeat("ab", 32)+`"`, 1)
	var header etypes.Header
	c.Assert(json.Unmarshal([]byte(blockAnswer), &header), IsNil)
	receiptBlock := header.Hash().Hex()
	node := rescanProxy(s.server.URL, blockAnswer, tx, &receiptBlock)
	eps := gates(node.URL)
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	cfg := config.BifrostChainConfiguration{
		ChainID:        common.BSCChain,
		RPCHost:        eps[0].RPCHost,
		SolvencyBlocks: 10,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:            common.BSCChain,
			StartBlockHeight:   1,
			HTTPRequestTimeout: time.Second * 10,
			GasCacheBlocks:     40,
			Concurrency:        1,
		},
	}
	if len(eps) > 1 {
		cfg.Endpoints = eps
	}
	e, err := NewEVMClient(s.thorKeys, cfg, nil, s.bridge, s.m, pubkeyMgr, thorclient.NewPoolMgr(s.bridge))
	c.Assert(err, IsNil)
	scanner := e.evmScanner
	for _, en := range scanner.endpointMgr.Entries() {
		for i := 0; i < 5 && en.State() != endpoints.StateHealthy; i++ {
			en.RecordHealthCheck(true, scanner.endpointMgr.Config())
		}
		c.Assert(en.Eligible(), Equals, true)
	}
	block, err := scanner.ethRpc.GetBlock(99)
	c.Assert(err, IsNil)
	c.Assert(block.Hash().Hex(), Equals, receiptBlock)
	return scanner, block, node.Close
}

// An active endpoint that serves the block but not its receipts fails the ordinary scan
// of the block; on the second failure in a row it is sidelined, and the retry reads the
// block and its receipts from the next endpoint.
func (s *UnstuckTestSuite) TestReceiptsTheActiveEndpointCannotServeMoveTheScanOn(c *C) {
	var aHits, bHits atomic.Int32
	var a, b *httptest.Server
	scanner, block, stop := s.receiptsScanner(c, func(node string) []config.ChainEndpointConfig {
		a = nullReceiptGate(node, -1, &aHits)
		b = receiptGate(node, false, &bHits)
		return []config.ChainEndpointConfig{{Name: "a", RPCHost: a.URL, Priority: 1}, {Name: "b", RPCHost: b.URL, Priority: 2}}
	})
	defer func() { a.Close(); b.Close(); stop() }()
	c.Assert(scanner.endpointMgr.Active().Name, Equals, "a")

	_, err := scanner.processBlock(block, 0)
	c.Assert(err, ErrorMatches, "(?s).*eth_getBlockReceipts returned 0 receipts for 1 transactions.*")
	c.Assert(scanner.endpointMgr.Active().Name, Equals, "a") // one failure moves nothing
	_, err = scanner.processBlock(block, 0)
	c.Assert(err, NotNil)
	c.Assert(scanner.endpointMgr.Active().Name, Equals, "b")
	c.Assert(scanner.endpointMgr.Entry("a").InCooldown(), Equals, true)

	block, err = scanner.ethRpc.GetBlock(99)
	c.Assert(err, IsNil)
	_, err = scanner.processBlock(block, 0)
	c.Assert(err, IsNil)
	c.Assert(aHits.Load(), Equals, int32(2))
	c.Assert(bHits.Load(), Equals, int32(1))
}

// A single failure followed by a served read (a load balancer that answered from a node
// a block behind, a reorg between the reads) moves nothing.
func (s *UnstuckTestSuite) TestOneReceiptsFailureThenSuccessKeepsTheActiveEndpoint(c *C) {
	var aHits, bHits atomic.Int32
	var a, b *httptest.Server
	scanner, block, stop := s.receiptsScanner(c, func(node string) []config.ChainEndpointConfig {
		a = nullReceiptGate(node, 1, &aHits)
		b = receiptGate(node, false, &bHits)
		return []config.ChainEndpointConfig{{Name: "a", RPCHost: a.URL, Priority: 1}, {Name: "b", RPCHost: b.URL, Priority: 2}}
	})
	defer func() { a.Close(); b.Close(); stop() }()

	_, err := scanner.processBlock(block, 0)
	c.Assert(err, NotNil)
	_, err = scanner.processBlock(block, 0)
	c.Assert(err, IsNil)
	_, err = scanner.processBlock(block, 0) // a later failure starts a new count
	c.Assert(err, IsNil)
	c.Assert(scanner.endpointMgr.Active().Name, Equals, "a")
	c.Assert(scanner.endpointMgr.Entry("a").InCooldown(), Equals, false)
	c.Assert(bHits.Load(), Equals, int32(0))
}

// With a single endpoint there is nowhere to move: it is never sidelined, and every
// retry fails until the node has the receipts (no observation is lost).
func (s *UnstuckTestSuite) TestReceiptsFailuresNeverSidelineASingleEndpoint(c *C) {
	var hits atomic.Int32
	var a *httptest.Server
	scanner, block, stop := s.receiptsScanner(c, func(node string) []config.ChainEndpointConfig {
		a = nullReceiptGate(node, -1, &hits)
		return []config.ChainEndpointConfig{{Name: "a", RPCHost: a.URL, Priority: 1}}
	})
	defer func() { a.Close(); stop() }()
	for i := 0; i < 3; i++ {
		_, err := scanner.processBlock(block, 0)
		c.Assert(err, NotNil)
	}
	c.Assert(hits.Load(), Equals, int32(3))
	c.Assert(scanner.endpointMgr.Active().InCooldown(), Equals, false)
}
