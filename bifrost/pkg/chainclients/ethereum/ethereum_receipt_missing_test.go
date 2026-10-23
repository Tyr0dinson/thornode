package ethereum

import (
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	thorcommon "gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// A node that lists a transaction in a block but answers null for its receipt has
// not imported the block yet (a lagging endpoint after a failover) or dropped it in a
// reorg. The scan of that block must fail so it is retried; it must never complete
// with the transaction silently missing.
func (s *BlockScannerTestSuite) TestMissingReceiptFailsTheBlock(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch {
		case req.RequestURI == thorclient.ChainVersionEndpoint:
			_, _ = rw.Write([]byte(`{"current":"` + types2.GetCurrentVersion().String() + `"}`))
		case req.RequestURI == thorclient.PubKeysEndpoint:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/vaults/pubKeys.json")
		case req.RequestURI == thorclient.InboundAddressesEndpoint:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/inbound_addresses/inbound_addresses.json")
		case req.RequestURI == thorclient.AsgardVault:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/vaults/asgard.json")
		case strings.HasPrefix(req.RequestURI, thorclient.NodeAccountEndpoint):
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/nodeaccount/template.json")
		case strings.HasPrefix(req.RequestURI, thorclient.MimirEndpoint):
			_, _ = rw.Write([]byte(`-1`))
		default:
			body, _ := io.ReadAll(req.Body)
			var rpcRequest struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &rpcRequest)
			rw.Header().Set("Content-Type", "application/json")
			switch rpcRequest.Method {
			case "eth_chainId":
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
			case "eth_getBlockReceipts", "eth_getTransactionReceipt":
				// the block has not been imported by this node: null, not an error
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
			default:
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
			}
		}
	}))
	defer server.Close()

	ethClient, err := ethclient.Dial(server.URL)
	c.Assert(err, IsNil)
	defer ethClient.Close()
	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	defer func() { c.Check(storage.Close(), IsNil) }()
	u, err := url.Parse(server.URL)
	c.Assert(err, IsNil)
	bridge, err := thorclient.NewThorchainBridge(config.BifrostClientConfiguration{
		ChainID: "thorchain", ChainHost: u.Host, SignerName: "bob", SignerPasswd: "password",
	}, s.m, s.keys)
	c.Assert(err, IsNil)
	pkeyMgr, err := pubkeymanager.NewPubKeyManager(bridge, s.m)
	c.Assert(err, IsNil)
	c.Assert(pkeyMgr.Start(), IsNil)
	defer func() { c.Assert(pkeyMgr.Stop(), IsNil) }()
	bs, err := NewETHScanner(getConfigForTest(), storage, big.NewInt(int64(Mainnet)), ethClient, bridge, s.m, pkeyMgr, func(int64) error { return nil }, nil)
	c.Assert(err, IsNil)

	tx := makeReceiptsTestTx(0)
	block := makeReceiptsTestBlock(5, []*etypes.Transaction{tx})

	// the single lookup reports the missing receipt as an error, not as "no tx"
	item, err := bs.fromTxToTxIn(tx)
	c.Assert(err, ErrorMatches, ".*not available on this node yet.*")
	c.Assert(item, IsNil)

	// and the block scan fails as a whole, so the block is retried
	_, err = bs.extractTxs(block, "")
	c.Assert(err, ErrorMatches, "block 5: tx 0x.*not available on this node yet.*")
}

// A transaction whose receipt is present but whose content cannot be parsed (here an
// unsigned transaction to a vault, so its sender cannot be recovered) fails the same
// way on every retry. It must be logged and skipped, never fail the block: failing it
// would stall ETH scanning on every node at that height.
func (s *BlockScannerTestSuite) TestContentErrorIsSkippedNotFatal(c *C) {
	// the receipt carries the hash of the block it belongs to, as every node's does
	receiptBlockHash := "0x78bfef68fccd4507f9f4804ba5c65eb2f928ea45b3383ade88aaa720f1209cba"
	receipt := func() string {
		return `{"jsonrpc":"2.0","id":1,"result":{"transactionHash":"0x88df016429689c079f3b2f6ad39fa052532c56795b733da78a91ebe6a713944b",` +
			`"transactionIndex":"0x0","blockNumber":"0x5","blockHash":"` + receiptBlockHash + `",` +
			`"cumulativeGasUsed":"0xc350","gasUsed":"0x4dc","effectiveGasPrice":"0x2540be400","logs":[],"status":"0x1",` +
			`"logsBloom":"0x` + strings.Repeat("00", 256) + `"}}`
	}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch {
		case req.RequestURI == thorclient.ChainVersionEndpoint:
			_, _ = rw.Write([]byte(`{"current":"` + types2.GetCurrentVersion().String() + `"}`))
		case req.RequestURI == thorclient.PubKeysEndpoint:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/vaults/pubKeys.json")
		case req.RequestURI == thorclient.InboundAddressesEndpoint:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/inbound_addresses/inbound_addresses.json")
		case req.RequestURI == thorclient.AsgardVault:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/vaults/asgard.json")
		case strings.HasPrefix(req.RequestURI, thorclient.NodeAccountEndpoint):
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/nodeaccount/template.json")
		case req.RequestURI == "/thorchain/mimir/key/EVMDisableContractWhitelist":
			_, _ = rw.Write([]byte(`1`)) // parse every transaction, as mainnet does
		case strings.HasPrefix(req.RequestURI, thorclient.MimirEndpoint):
			_, _ = rw.Write([]byte(`-1`))
		default:
			body, _ := io.ReadAll(req.Body)
			var rpcRequest struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &rpcRequest)
			rw.Header().Set("Content-Type", "application/json")
			switch rpcRequest.Method {
			case "eth_chainId":
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
			case "eth_getBlockReceipts":
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`))
			case "eth_getTransactionReceipt":
				_, _ = rw.Write([]byte(receipt()))
			default:
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
			}
		}
	}))
	defer server.Close()

	ethClient, err := ethclient.Dial(server.URL)
	c.Assert(err, IsNil)
	defer ethClient.Close()
	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	defer func() { c.Check(storage.Close(), IsNil) }()
	u, err := url.Parse(server.URL)
	c.Assert(err, IsNil)
	bridge, err := thorclient.NewThorchainBridge(config.BifrostClientConfiguration{
		ChainID: "thorchain", ChainHost: u.Host, SignerName: "bob", SignerPasswd: "password",
	}, s.m, s.keys)
	c.Assert(err, IsNil)
	pkeyMgr, err := pubkeymanager.NewPubKeyManager(bridge, s.m)
	c.Assert(err, IsNil)
	c.Assert(pkeyMgr.Start(), IsNil)
	defer func() { c.Assert(pkeyMgr.Stop(), IsNil) }()
	bs, err := NewETHScanner(getConfigForTest(), storage, big.NewInt(int64(Mainnet)), ethClient, bridge, s.m, pkeyMgr, func(int64) error { return nil }, nil)
	c.Assert(err, IsNil)

	// an unsigned transaction to a vault: receipt fine, sender unrecoverable
	pubKeys := pkeyMgr.GetPubKeys()
	c.Assert(len(pubKeys) > 0, Equals, true)
	vault, err := pubKeys[0].GetAddress(thorcommon.ETHChain)
	c.Assert(err, IsNil)
	to := ecommon.HexToAddress(vault.String())
	tx := etypes.NewTx(&etypes.LegacyTx{Nonce: 0, To: &to, Value: big.NewInt(1), Gas: 21000, GasPrice: big.NewInt(1)})

	item, err := bs.fromTxToTxIn(tx)
	c.Assert(err, ErrorMatches, "fail to get sender.*")
	c.Assert(item, IsNil)

	block := makeReceiptsTestBlock(5, []*etypes.Transaction{tx})
	receiptBlockHash = block.Hash().Hex()
	txIn, err := bs.extractTxs(block, "")
	c.Assert(err, IsNil) // skipped, the block completes
	c.Assert(txIn.TxArray, HasLen, 0)

	// the same receipt from another block (a node on another fork, or one that
	// reorged since the block was read) fails the block so it is retried: it must
	// not be observed, nor reach the content checks, from the wrong fork
	receiptBlockHash = "0x1111111111111111111111111111111111111111111111111111111111111111"
	_, err = bs.extractTxs(block, "")
	c.Assert(err, ErrorMatches, "(?s).*belongs to block 0x1111.*not the scanned block.*")
	c.Assert(errors.Is(err, errReceiptUnavailable), Equals, true)
}
