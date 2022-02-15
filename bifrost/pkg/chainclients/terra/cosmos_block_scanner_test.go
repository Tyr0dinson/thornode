package terra

import (
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"gitlab.com/thorchain/thornode/app"
	"gitlab.com/thorchain/thornode/bifrost/config"
	"gitlab.com/thorchain/thornode/bifrost/metrics"
	"gitlab.com/thorchain/thornode/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/common"

	"gitlab.com/thorchain/thornode/cmd"
	. "gopkg.in/check.v1"
)

type BlockScannerTestSuite struct {
	m      *metrics.Metrics
	bridge *thorclient.ThorchainBridge
	keys   *thorclient.Keys
}

var _ = Suite(&BlockScannerTestSuite{})

func (s *BlockScannerTestSuite) SetUpSuite(c *C) {
	s.m = GetMetricForTest(c)
	c.Assert(s.m, NotNil)
	cfg := config.ClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: "",
	}

	kb := cKeys.NewInMemory()
	_, _, err := kb.NewMnemonic(cfg.SignerName, cKeys.English, cmd.THORChainHDPath, hd.Secp256k1)
	c.Assert(err, IsNil)
	thorKeys := thorclient.NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd)
	c.Assert(err, IsNil)
	s.bridge, err = thorclient.NewThorchainBridge(cfg, s.m, thorKeys)
	c.Assert(err, IsNil)
	s.keys = thorKeys
}

func (s *BlockScannerTestSuite) TestCalculateAverageGasFees(c *C) {}

func (s *BlockScannerTestSuite) TestGetBlock(c *C) {
	feeAsset, err := common.NewAsset("TERRA.LUNA")
	c.Assert(err, IsNil)

	mockRPC := NewMockServiceClient()

	blockScanner := CosmosBlockScanner{
		feeAsset:  feeAsset,
		tmService: mockRPC,
	}

	block, err := blockScanner.GetBlock(1)

	c.Assert(err, IsNil)
	c.Assert(len(block.Data.Txs), Equals, 50)
	c.Assert(block.Header.Height, Equals, int64(6000011))
}

func (s *BlockScannerTestSuite) TestProcessTxs(c *C) {
	feeAsset, err := common.NewAsset("TERRA.LUNA")
	c.Assert(err, IsNil)

	mockRPC := NewMockServiceClient()

	encodingConfig := app.MakeEncodingConfig()
	ctx := client.Context{}
	ctx = ctx.WithInterfaceRegistry(encodingConfig.InterfaceRegistry)
	cdc := codec.NewProtoCodec(ctx.InterfaceRegistry)

	blockScanner := CosmosBlockScanner{
		feeAsset:  feeAsset,
		tmService: mockRPC,
		cdc:       cdc,
	}

	block, err := blockScanner.GetBlock(1)
	c.Assert(err, IsNil)

	txInItems, err := blockScanner.processTxs(1, block.Data.Txs)
	c.Assert(err, IsNil)

	// proccessTxs should filter out everything besides MsgSend
	c.Assert(len(txInItems), Equals, 7)
}
