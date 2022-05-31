package evm

import (
	"encoding/json"
	"io/ioutil"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"gitlab.com/thorchain/thornode/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/common"
	. "gopkg.in/check.v1"
)

type TokenManagerTestSuite struct {
	prefix  string
	storage *blockscanner.BlockScannerStorage
	client  *ethclient.Client
}

var testWhiteList = []ERC20Token{
	{
		Address: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
		Symbol:  "USDC",
	},
	{
		Address: "0xB0b86991c6218b36c1d19D4a2e9Eb0cE3606eB49",
		Symbol:  "TKN",
	},
	{
		Address: "0x1f9840a85d5aF5bf1D1762F925BDADdC4201F984",
		Symbol:  "UNI",
	},
}

var _ = Suite(&TokenManagerTestSuite{})

func getTestEthClient(c *C) (*ethclient.Client, error) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, err := ioutil.ReadAll(req.Body)
		c.Assert(err, IsNil)
		type RPCRequest struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      interface{}     `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		var rpcRequest RPCRequest
		err = json.Unmarshal(body, &rpcRequest)
		c.Assert(err, IsNil)
		if rpcRequest.Method == "eth_chainId" {
			_, err := rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x539"}`))
			c.Assert(err, IsNil)
		}
		if rpcRequest.Method == "eth_gasPrice" {
			_, err := rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
			c.Assert(err, IsNil)
		}
		if rpcRequest.Method == "eth_call" {
			symbolInput := "0x95d89b41"
			decimalsInput := "0x313ce567"

			// Symbol Call
			if strings.Contains(string(rpcRequest.Params), symbolInput) {
				// TKN
				_, err := rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x544B4E"}`))
				c.Assert(err, IsNil)

				// Decimal call
			} else if strings.Contains(string(rpcRequest.Params), decimalsInput) {
				// 18
				_, err := rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x12"}`))
				c.Assert(err, IsNil)
			}
			_, err := rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x0"}`))
			c.Assert(err, IsNil)
		}
	}))
	return ethclient.Dial(server.URL)
}

func (s *TokenManagerTestSuite) SetUpSuite(c *C) {
	storage, err := blockscanner.NewBlockScannerStorage("test/path")
	c.Assert(err, IsNil)
	ethClient, err := getTestEthClient(c)
	c.Assert(err, IsNil)

	s.storage = storage
	s.client = ethClient
	s.prefix = "eth-tokenmeta-"
}

func (s *TokenManagerTestSuite) TestSaveAndGet(c *C) {
	manager, err := NewTokenManager(s.storage.GetInternalDb(), s.prefix, common.ETHAsset, 18, time.Second, testWhiteList, s.client)
	c.Assert(err, IsNil)

	// Test non-whitelisted token gets rejected
	tether := "0xdAC17F958D2ee523a2206206994597C13D831ec7"
	meta, err := manager.GetTokenMeta(tether)
	c.Assert(err.Error(), Equals, "token: 0xdAC17F958D2ee523a2206206994597C13D831ec7 is not whitelisted")

	// Test whitelisted token gets saved
	usdc := "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
	meta, err = manager.GetTokenMeta(usdc)
	c.Assert(err, IsNil)
	c.Assert(meta.Decimal, Equals, uint64(18))
	// c.Assert(meta.Symbol, Equals, "TKN")

}

func (s *TokenManagerTestSuite) TestConvertAmounts(c *C) {
	manager, err := NewTokenManager(s.storage.GetInternalDb(), s.prefix, common.ETHAsset, 18, time.Second, testWhiteList, s.client)
	c.Assert(err, IsNil)

	err = manager.SaveTokenMeta("0xB0b86991c6218b36c1d19D4a2e9Eb0cE3606eB49", "TKN", 9)
	c.Assert(err, IsNil)

	convertedAmt := manager.ConvertSigningAmount(big.NewInt(2111111112), "0xB0b86991c6218b36c1d19D4a2e9Eb0cE3606eB49")
	c.Assert(convertedAmt.Int64(), Equals, 2111111112)

}
