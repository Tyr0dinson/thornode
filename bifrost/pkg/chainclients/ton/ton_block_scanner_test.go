package ton

import (
	"context"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/ton"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"

	. "gopkg.in/check.v1"
)

// -------------------------------------------------------------------------------------
// Tests
// -------------------------------------------------------------------------------------

type BlockScannerTestSuite struct {
	metrics *metrics.Metrics
	bridge  thorclient.ThorchainBridge
	keys    *thorclient.Keys
	client  ton.APIClientWrapped
}

var _ = Suite(&BlockScannerTestSuite{})

func (s *BlockScannerTestSuite) SetUpSuite(c *C) {
	s.metrics = GetMetricForTest(c)
	c.Assert(s.metrics, NotNil)
	cfg := config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: "",
	}

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kb := keyring.NewInMemory(codec.NewProtoCodec(registry))
	_, _, err := kb.NewMnemonic(cfg.SignerName, keyring.English, cmd.THORChainHDPath, cfg.SignerPasswd, hd.Secp256k1)
	c.Assert(err, IsNil)

	thorKeys := thorclient.NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd)
	c.Assert(err, IsNil)

	s.bridge, err = thorclient.NewThorchainBridge(cfg, s.metrics, thorKeys)
	c.Assert(err, IsNil)
	s.keys = thorKeys

	// Set up ton api client
	pool := liteclient.NewConnectionPool()

	liteClientConfig, err := liteclient.GetConfigFromUrl(context.Background(), "https://ton.org/testnet-global.config.json")
	c.Assert(err, IsNil)

	// connect to mainnet lite servers
	err = pool.AddConnectionsFromConfig(context.Background(), liteClientConfig)
	c.Assert(err, IsNil)

	// api client with full proof checks
	s.client = ton.NewAPIClient(pool, ton.ProofCheckPolicyFast).WithRetry()
	s.client.SetTrustedBlockFromConfig(liteClientConfig)
}

func (s *BlockScannerTestSuite) TestGetHeight(c *C) {
	cfg := config.BifrostBlockScannerConfiguration{ChainID: common.TONChain}
	blockScanner := TonBlockScanner{
		config: cfg,
		client: s.client,
	}

	block, err := blockScanner.GetHeight()

	c.Assert(err, IsNil)
	c.Assert(block, Not(Equals), int64(0))
}
