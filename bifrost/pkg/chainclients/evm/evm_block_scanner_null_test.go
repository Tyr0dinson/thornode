package evm

import (
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"time"

	ethclient "github.com/ethereum/go-ethereum/ethclient"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	thorcommon "gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// newNullReceiptScanner builds a scanner against a node that serves the fixture
// block but answers every receipt request (single, batch, or whole block) with null,
// the answer of a node that has not imported the block yet.
func (s *BlockScannerTestSuite) newNullReceiptScanner(c *C, chain thorcommon.Chain) (*EVMScanner, func()) {
	var resp struct {
		Result json.RawMessage `json:"result"`
	}
	c.Assert(json.Unmarshal(blockByNumberResp, &resp), IsNil)
	rpcResults := map[string]string{
		"eth_chainId":               `"0xa868"`,
		"eth_gasPrice":              `"0x5d21dba00"`,
		"eth_call":                  `"0x52554e45"`,
		"eth_getBlockByNumber":      string(resp.Result),
		"eth_getTransactionReceipt": `null`,
		"eth_getBlockReceipts":      `null`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch {
		case req.RequestURI == thorclient.ChainVersionEndpoint:
			_, _ = rw.Write([]byte(`{"current":"` + types.GetCurrentVersion().String() + `"}`))
		case req.RequestURI == thorclient.PubKeysEndpoint:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/vaults/pubKeys.json")
		case req.RequestURI == thorclient.InboundAddressesEndpoint:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/inbound_addresses/inbound_addresses.json")
		case req.RequestURI == thorclient.AsgardVault:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/vaults/asgard.json")
		case strings.HasPrefix(req.RequestURI, thorclient.NodeAccountEndpoint):
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/nodeaccount/template.json")
		case strings.HasPrefix(req.RequestURI, thorclient.LastBlockEndpoint):
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/lastblock/eth.json")
		case strings.HasPrefix(req.RequestURI, thorclient.AuthAccountEndpoint):
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/auth/accounts/template.json")
		case strings.HasPrefix(req.RequestURI, thorclient.MimirEndpoint+"/key"):
			_, _ = rw.Write([]byte(`-1`))
		default:
			body, _ := io.ReadAll(req.Body)
			rw.Header().Set("Content-Type", "application/json")
			if len(body) > 0 && body[0] == '[' {
				var batch []struct {
					Method string `json:"method"`
					ID     int    `json:"id"`
				}
				c.Assert(json.Unmarshal(body, &batch), IsNil)
				out := make([]map[string]json.RawMessage, len(batch))
				for i, v := range batch {
					out[i] = map[string]json.RawMessage{"result": json.RawMessage(rpcResults[v.Method]), "id": json.RawMessage(strconv.Itoa(v.ID))}
				}
				c.Assert(json.NewEncoder(rw).Encode(out), IsNil)
				return
			}
			var r struct {
				Method string `json:"method"`
			}
			c.Assert(json.Unmarshal(body, &r), IsNil)
			c.Assert(json.NewEncoder(rw).Encode(map[string]json.RawMessage{"result": json.RawMessage(rpcResults[r.Method])}), IsNil)
		}
	}))
	ethClient, err := ethclient.Dial(server.URL)
	c.Assert(err, IsNil)
	rpcClient, err := evm.NewEthRPC(ethClient, time.Second, chain.String())
	c.Assert(err, IsNil)
	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	u, err := url.Parse(server.URL)
	c.Assert(err, IsNil)
	bridge, err := thorclient.NewThorchainBridge(config.BifrostClientConfiguration{
		ChainID: "thorchain", ChainHost: u.Host, SignerName: "bob", SignerPasswd: "password",
	}, s.m, s.keys)
	c.Assert(err, IsNil)
	pubKeyMgr, err := pubkeymanager.NewPubKeyManager(bridge, s.m)
	c.Assert(err, IsNil)
	c.Assert(pubKeyMgr.Start(), IsNil)
	cfg := getConfigForTest()
	cfg.ChainID = chain
	bs, err := NewEVMScanner(cfg, storage, big.NewInt(43112), ethClient, rpcClient, bridge, s.m, pubKeyMgr, func(int64) error { return nil }, nil)
	c.Assert(err, IsNil)
	return bs, func() {
		c.Assert(pubKeyMgr.Stop(), IsNil)
		server.Close()
	}
}

// A null receipt for a transaction the block lists means the answering node has not
// imported the block yet (a lagging endpoint after a failover): the block scan must
// fail so it is retried, on both the batched eth_getTransactionReceipt path (AVAX)
// and the eth_getBlockReceipts path (BSC, POL, BASE).
func (s *BlockScannerTestSuite) TestNullReceiptsFailTheBlock(c *C) {
	bs, stop := s.newNullReceiptScanner(c, thorcommon.AVAXChain)
	defer stop()
	_, err := bs.FetchTxs(1, 1)
	c.Assert(err, ErrorMatches, "(?s).*receipt for tx 0x.*unavailable.*")

	bsc, stopBSC := s.newNullReceiptScanner(c, thorcommon.BSCChain)
	defer stopBSC()
	_, err = bsc.FetchTxs(1, 1)
	c.Assert(err, ErrorMatches, "(?s).*eth_getBlockReceipts returned 0 receipts for 1 transactions.*")
}
