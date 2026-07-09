package tron

import (
	"net/http/httptest"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
	"gitlab.com/thorchain/thornode/v3/config"

	. "gopkg.in/check.v1"
)

type BlockScannerTestSuite struct {
	api     *httptest.Server
	scanner *TronBlockScanner
}

var _ = Suite(&BlockScannerTestSuite{})

func (s *BlockScannerTestSuite) SetUpSuite(c *C) {
	s.api = api.NewMockServer()
	c.Assert(s.api, NotNil)

	clientConfig := config.BifrostClientConfiguration{
		ChainID:      "thorchain",
		ChainHost:    "localhost",
		SignerName:   "bob",
		SignerPasswd: "password",
	}

	chainConfig := config.BifrostChainConfiguration{
		APIHost: s.api.URL,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			HTTPRequestTimeout: time.Second * 2,
		},
	}

	bridge, err := thorclient.NewThorchainBridge(
		clientConfig, nil, nil,
	)
	c.Assert(err, IsNil)

	pubkeyMgr := &pubkeymanager.MockPoolAddressValidator{}

	s.scanner, err = NewTronBlockScanner(
		chainConfig,
		bridge,
		pubkeyMgr,
		func(h int64) error { return nil },
	)
	c.Assert(err, IsNil)

	s.scanner.whitelist = map[string]tokenlist.ERC20Token{
		"TG3XXyExBkPp9nzdajDZsozEu4BkaSJozs": {
			Name:     "USDT",
			Symbol:   "USDT",
			Address:  "TG3XXyExBkPp9nzdajDZsozEu4BkaSJozs",
			Decimals: 6,
		},
	}
}

func (s *BlockScannerTestSuite) TestGetHeight(c *C) {
	height, err := s.scanner.GetHeight()
	c.Assert(err, IsNil)
	// scanning 1 blocks behind the tip
	c.Assert(height, Equals, 55088560-ConfirmationBlocks)
}

func (s *BlockScannerTestSuite) TestFetchTxs(c *C) {
	txIn, err := s.scanner.FetchTxs(69351239, 0)
	c.Assert(err, IsNil)
	// Every relevant transfer in the block is observed. Txs whose info fetch errors
	// (mock 404) are observed with the fallback gas (1 sun) rather than dropped.
	c.Assert(len(txIn.TxArray), Equals, 63)
	var found, sawFallbackGas bool
	for _, tx := range txIn.TxArray {
		if tx.Tx == "e1cd4454d71d8973e89155bc2c2a91aa56fd470c8ce64547ce8c69c789c21f0c" {
			found = true
		}
		if len(tx.Gas) == 1 && tx.Gas[0].Amount.String() == "1" {
			sawFallbackGas = true
		}
	}
	c.Assert(found, Equals, true)
	c.Assert(sawFallbackGas, Equals, true)
}

func (s *BlockScannerTestSuite) TestGetMaxEnergy(c *C) {
	// fail because there is no refAddress set
	s.scanner.refAddress = ""
	maxEnergy, err := s.scanner.getMaxEnergy()
	c.Assert(err, NotNil)
	c.Assert(maxEnergy, Equals, int64(0))

	// set any address, using mock return
	s.scanner.refAddress = "TXhG4Vhrxyu8htL8tBdCmndF66D1jyKgJW"
	maxEnergy, err = s.scanner.getMaxEnergy()
	c.Assert(err, IsNil)
	// mock data returns 900k (2x)
	c.Assert(maxEnergy, Equals, int64(1800000))
}
