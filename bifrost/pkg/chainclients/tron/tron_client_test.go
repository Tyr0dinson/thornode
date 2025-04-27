package tron

import (
	"bytes"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/ethereum/go-ethereum/accounts/abi"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
	"gitlab.com/thorchain/thornode/v3/config"

	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type TronTestSuite struct {
	thordir string
	metrics *metrics.Metrics
	client  *TronClient
}

var _ = Suite(&TronTestSuite{})

var m *metrics.Metrics

func GetMetricForTest(c *C) *metrics.Metrics {
	if m == nil {
		var err error
		m, err = metrics.NewMetrics(config.BifrostMetricsConfiguration{
			Enabled:      false,
			ListenPort:   9000,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
			Chains:       common.Chains{common.TRONChain},
		})
		c.Assert(m, NotNil)
		c.Assert(err, IsNil)
	}
	return m
}

func (s *TronTestSuite) SetUpSuite(c *C) {
	cosmosSDKConfg := cosmos.GetConfig()
	cosmosSDKConfg.SetBech32PrefixForAccount("sthor", "sthorpub")

	s.metrics = GetMetricForTest(c)
	c.Assert(s.metrics, NotNil)
	ns := strconv.Itoa(time.Now().Nanosecond())
	c.Assert(os.Setenv("NET", "stagenet"), IsNil)

	s.thordir = filepath.Join(os.TempDir(), ns, ".thorcli")

	clientConfig := config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: s.thordir,
	}

	chainConfig := config.BifrostChainConfiguration{
		ChainID: common.TRONChain,
		APIHost: "https://nile.trongrid.io",
		RPCHost: "https://nile.trongrid.io/jsonrpc",
		BlockScanner: config.BifrostBlockScannerConfiguration{
			APIHost:            "https://nile.trongrid.io",
			RPCHost:            "https://nile.trongrid.io/jsonrpc",
			HTTPRequestTimeout: time.Second * 2,
		},
	}

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)

	kb := keyring.NewInMemory(codec.NewProtoCodec(registry))
	_, _, err := kb.NewMnemonic(
		clientConfig.SignerName,
		keyring.English,
		cmd.THORChainHDPath,
		clientConfig.SignerPasswd,
		hd.Secp256k1,
	)
	c.Assert(err, IsNil)

	keys := thorclient.NewKeysWithKeybase(
		kb, clientConfig.SignerName, clientConfig.SignerPasswd,
	)
	c.Assert(err, IsNil)

	bridge, err := thorclient.NewThorchainBridge(
		clientConfig, s.metrics, keys,
	)
	c.Assert(err, IsNil)

	whitelist := map[string]tokenlist.ERC20Token{
		"TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf": {
			Name:     "USDT",
			Symbol:   "USDT",
			Address:  "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf",
			Decimals: 6,
		},
	}

	scanner, err := NewTronBlockScanner(
		chainConfig.BlockScanner, bridge, func(int64) error { return nil },
	)
	c.Assert(err, IsNil)

	s.client = &TronClient{
		chainId:     common.TRONChain.String(),
		config:      chainConfig,
		bridge:      bridge,
		whitelist:   whitelist,
		api:         api.NewTronApi(chainConfig.BlockScanner),
		rpc:         rpc.NewTronRpc(chainConfig.BlockScanner),
		tronScanner: scanner,
	}

	s.client.abi, err = abi.JSON(bytes.NewReader(trc20ContractABI))
	c.Assert(err, IsNil)
}

func (s *TronTestSuite) TearDownSuite(c *C) {
	c.Assert(os.Unsetenv("NET"), IsNil)
	if err := os.RemoveAll(s.thordir); err != nil {
		c.Error(err)
	}
}

func (s *TronTestSuite) TestGetAddress(c *C) {
	pubKey := common.PubKey("sthorpub1addwnpepqf72ur2e8zk8r5augtrly40cuy94f7e663zh798tyms6pu2k8qdswf4es66")
	address := s.client.GetAddress(pubKey)
	c.Assert(address, Equals, "TJWs2NWfr4y61S6FbUYhUBJmMhK1SYaEFi")
}

func (s *TronTestSuite) TestGetTokenBalance(c *C) {
	address := "TUFig61gEjyEPPn1xrnhE8uJwsLxViugx5"
	contract := "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf"

	balance, err := s.client.getTokenBalance(address, contract)
	fmt.Println("balance:", balance)
	c.Assert(err, IsNil)
	c.Check(balance, Not(Equals), int64(0), Commentf("Balance: %d", balance))
}

func (s *TronTestSuite) TestGetAccountByAddress(c *C) {
	address := "TUFig61gEjyEPPn1xrnhE8uJwsLxViugx5"

	height, err := s.client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Not(Equals), 0, Commentf("Height: %d", height))

	account, err := s.client.GetAccountByAddress(address, big.NewInt(height))
	c.Assert(err, IsNil)
	c.Assert(len(account.Coins), Equals, 2)
}
