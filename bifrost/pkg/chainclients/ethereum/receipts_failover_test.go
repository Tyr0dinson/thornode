package ethereum

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
// its probes but not receipts), counting them in hits.
func nullReceiptGate(target string, hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		if bytes.Contains(body, []byte(`"eth_getBlockReceipts"`)) || bytes.Contains(body, []byte(`"eth_getTransactionReceipt"`)) {
			hits.Add(1)
			var r struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(body, &r)
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":` + string(r.ID) + `,"result":null}`))
			return
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

// An active endpoint that serves the block but not its receipts fails the ordinary scan
// of the block; on the second failure in a row it is sidelined, and the retry reads the
// block and its receipts from the next endpoint.
func (s *UnstuckTestSuite) TestReceiptsTheActiveEndpointCannotServeMoveTheScanOn(c *C) {
	config.Init()
	to := ecommon.HexToAddress("0x9f4aab49a9cd8fc54dcb3701846f608a6f2c44da")
	tx := etypes.NewTx(&etypes.LegacyTx{Nonce: 0, To: &to, Value: big.NewInt(1), Gas: 21000, GasPrice: big.NewInt(1)})
	txJSON, err := json.Marshal(tx)
	c.Assert(err, IsNil)
	blockAnswer := strings.Replace(strings.Replace(reorgBlockJSON,
		`"transactions":[]`, `"transactions":[`+string(txJSON)+`]`, 1),
		`"transactionsRoot":"0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"`,
		`"transactionsRoot":"0x`+strings.Repeat("ab", 32)+`"`, 1)
	// a scanned ETH block carries a base fee (the gas price feed reads it)
	blockAnswer = strings.Replace(blockAnswer, `"gasUsed":"0x0"`, `"gasUsed":"0x0","baseFeePerGas":"0x1"`, 1)
	var header etypes.Header
	c.Assert(json.Unmarshal([]byte(blockAnswer), &header), IsNil)
	receiptBlock := header.Hash().Hex()
	node := rescanProxy(s.server.URL, blockAnswer, tx, &receiptBlock)
	defer node.Close()
	var aHits, bHits atomic.Int32
	a := nullReceiptGate(node.URL, &aHits)
	defer a.Close()
	b := receiptGate(node.URL, false, &bHits)
	defer b.Close()
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	e, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{
		ChainID: common.ETHChain,
		RPCHost: a.URL,
		Endpoints: []config.ChainEndpointConfig{
			{Name: "a", RPCHost: a.URL, Priority: 1},
			{Name: "b", RPCHost: b.URL, Priority: 2},
		},
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:            common.ETHChain,
			StartBlockHeight:   1,
			HTTPRequestTimeout: time.Second * 10,
			GasCacheBlocks:     40,
			Concurrency:        1,
			GasPriceResolution: 10_000_000_000,
			FixedGasRate:       10_000_000_000,
		},
	}, nil, s.bridge, s.m, pubkeyMgr, thorclient.NewPoolMgr(s.bridge))
	c.Assert(err, IsNil)
	scanner := e.ethScanner
	for _, en := range scanner.endpointMgr.Entries() {
		for i := 0; i < 5 && en.State() != endpoints.StateHealthy; i++ {
			en.RecordHealthCheck(true, scanner.endpointMgr.Config())
		}
		c.Assert(en.Eligible(), Equals, true)
	}
	c.Assert(scanner.endpointMgr.Active().Name, Equals, "a")
	block, err := scanner.getRPCBlock(99)
	c.Assert(err, IsNil)
	c.Assert(block.Hash().Hex(), Equals, receiptBlock)

	_, err = scanner.processBlock(block, 0)
	c.Assert(err, ErrorMatches, "(?s).*receipt.*")
	c.Assert(scanner.endpointMgr.Active().Name, Equals, "a") // one failure moves nothing
	_, err = scanner.processBlock(block, 0)
	c.Assert(err, NotNil)
	c.Assert(scanner.endpointMgr.Active().Name, Equals, "b")
	c.Assert(scanner.endpointMgr.Entry("a").InCooldown(), Equals, true)

	block, err = scanner.getRPCBlock(99)
	c.Assert(err, IsNil)
	_, err = scanner.processBlock(block, 0)
	c.Assert(err, IsNil)
	c.Assert(bHits.Load() > 0, Equals, true)
}
