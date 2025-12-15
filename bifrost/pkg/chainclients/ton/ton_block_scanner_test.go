package ton

import (
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/ton/mock"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	. "gopkg.in/check.v1"
)

// -------------------------------------------------------------------------------------
// Tests
// -------------------------------------------------------------------------------------

type BlockScannerTestSuite struct {
	scanner *TonBlockScanner
}

var _ = Suite(&BlockScannerTestSuite{})

func (s *BlockScannerTestSuite) SetUpSuite(c *C) {
	clientConfig := config.BifrostClientConfiguration{
		ChainID:      "thorchain",
		ChainHost:    "localhost",
		SignerName:   "bob",
		SignerPasswd: "password",
	}

	scannerConfig := config.BifrostBlockScannerConfiguration{
		ChainID:            common.TONChain,
		HTTPRequestTimeout: time.Second * 2,
		GasCacheBlocks:     1,
	}

	bridge, err := thorclient.NewThorchainBridge(
		clientConfig, nil, nil,
	)
	c.Assert(err, IsNil)

	api := mock.NewMockApiClient()

	s.scanner, err = NewTonBlockScanner(
		scannerConfig,
		bridge,
		api,
	)

	s.scanner.globalNetworkFeeQueue = make(chan common.NetworkFee)

	go func() {
		for range s.scanner.globalNetworkFeeQueue {
			time.Sleep(time.Millisecond * 200)
			continue
		}
	}()

	c.Assert(err, IsNil)
}

func (s *BlockScannerTestSuite) TestGetHeight(c *C) {
	block, err := s.scanner.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(block, Equals, int64(256673))
}

func (s *BlockScannerTestSuite) TestFetchMemPool(c *C) {
	txIn, err := s.scanner.FetchMemPool(time.Now().Unix())
	c.Assert(err, IsNil)
	c.Assert(txIn.Chain.IsEmpty(), Equals, true)
	c.Assert(txIn.TxArray, HasLen, 0)
}

func (s *BlockScannerTestSuite) TestFetchTxs(c *C) {
	txs, err := mock.GetTxs()
	c.Assert(err, IsNil)
	c.Assert(txs, HasLen, len(mock.RawTxs))

	items := 0
	for _, tx := range mock.RawTxs {
		if !tx.Skip {
			items++
		}
	}

	testCases := []struct {
		Height int64
		Items  int
		Error  bool
	}{
		{
			Height: 0,
			Items:  0,
		},
		{
			Height: 99,
			Error:  true,
		},
		{
			Height: 100000,
			Items:  items,
		},
	}
	for _, tc := range testCases {
		txIn, err := s.scanner.FetchTxs(tc.Height, tc.Height)
		if tc.Error {
			c.Assert(err, NotNil)
		} else {
			c.Assert(err, IsNil)
			c.Assert(txIn.TxArray, HasLen, tc.Items)
		}
	}
}

func (s *BlockScannerTestSuite) TestGetNetworkFee(c *C) {
	// amount blocks
	c.Assert(s.scanner.feeRateCache, HasLen, 1)
	// amount txs
	// bounceable msg fees are considered too
	c.Assert(s.scanner.feeRateCache[0], HasLen, 4)

	// 1. 3031086
	// 2. 1988534
	// 4. 2104574
	// 4. 2495998
	// == 2300286 -> max(2000000, 5000000)
	median := uint64(5000000)

	size, rate := s.scanner.GetNetworkFee()
	c.Assert(size, Equals, uint64(1))
	c.Assert(rate, Equals, median)
}
