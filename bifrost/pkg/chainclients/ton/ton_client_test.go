package ton

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/tvm/cell"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
	"gitlab.com/thorchain/thornode/v3/config"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type TonTestSuite struct {
	client  *TonClient
	thordir string
	pubkey  common.PubKey
}

var _ = Suite(&TonTestSuite{})

var m *metrics.Metrics

func GetMetricForTest(c *C) *metrics.Metrics {
	if m == nil {
		var err error
		m, err = metrics.NewMetrics(config.BifrostMetricsConfiguration{
			Enabled:      false,
			ListenPort:   9000,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
			Chains:       common.Chains{common.GAIAChain},
		})
		c.Assert(m, NotNil)
		c.Assert(err, IsNil)
	}
	return m
}

func (s *TonTestSuite) SetUpSuite(c *C) {
	ccfg := cosmos.GetConfig()
	ccfg.SetBech32PrefixForAccount("sthor", "sthorpub")

	metrics := GetMetricForTest(c)
	c.Assert(metrics, NotNil)

	ns := strconv.Itoa(time.Now().Nanosecond())
	s.thordir = filepath.Join(os.TempDir(), ns, ".thorcli")
	clientConfig := config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: s.thordir,
	}

	chainConfig := config.BifrostChainConfiguration{
		ChainID: common.TONChain,
		RPCHost: "mock",
		BlockScanner: config.BifrostBlockScannerConfiguration{
			StartBlockHeight: 1,
		},
	}

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)

	kb := keyring.NewInMemory(codec.NewProtoCodec(registry), func(options *keyring.Options) {
		options.SupportedAlgos = append(options.SupportedAlgos, ed25519.Ed25519)
	})
	_, _, err := kb.NewMnemonic(
		"ed-"+clientConfig.SignerName,
		keyring.English,
		"m/44'/118'/0'/0'/0'",
		clientConfig.SignerPasswd,
		ed25519.Ed25519,
	)
	c.Assert(err, IsNil)

	keys := thorclient.NewKeysWithKeybase(
		kb, clientConfig.SignerName, clientConfig.SignerPasswd,
	)
	c.Assert(err, IsNil)

	priv, err := keys.GetPrivateKeyEDDSA()
	c.Assert(err, IsNil)

	pub, _ := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())

	s.pubkey, err = common.NewPubKeyFromCrypto(pub)
	c.Assert(err, IsNil)

	bridge, err := thorclient.NewThorchainBridge(
		clientConfig, metrics, keys,
	)
	c.Assert(err, IsNil)

	s.client, err = NewTonClient(
		keys,
		chainConfig,
		nil,
		bridge,
		metrics,
	)
	c.Assert(err, IsNil)
}

func (s *TonTestSuite) TearDownSuite(c *C) {
	c.Assert(os.Unsetenv("NET"), IsNil)
	if err := os.RemoveAll(s.thordir); err != nil {
		c.Error(err)
	}
}

func (s *TonTestSuite) TestGetAddress(c *C) {
	pubKey := common.PubKey("sthorpub1addwnpepqf72ur2e8zk8r5augtrly40cuy94f7e663zh798tyms6pu2k8qdswf4es66")
	address := s.client.GetAddress(pubKey)

	if common.IsMocknet {
		c.Assert(address, Equals, "0QC0PbKWouvBPwTD9vaQ8LOQrMATFgHssiLzgNjxz-nBYsOj")
	} else {
		c.Assert(address, Equals, "UQC0PbKWouvBPwTD9vaQ8LOQrMATFgHssiLzgNjxz-nBYngp")
	}

	address = s.client.GetAddress("nonsense")
	c.Assert(address, Equals, "")
}

func (s *TonTestSuite) TestGetAccountByAddress(c *C) {
	address := "UQC0PbKWouvBPwTD9vaQ8LOQrMATFgHssiLzgNjxz-nBYngp"

	account, err := s.client.GetAccountByAddress(address, nil)
	c.Assert(err, IsNil)
	c.Assert(len(account.Coins), Equals, 1)
	c.Assert(account.Coins[0].Amount.String(), Equals, "1231978")
}

func (s *TonTestSuite) TestGetChain(c *C) {
	chain := s.client.GetChain()
	c.Assert(chain.Equals(common.TONChain), Equals, true)
}

func (s *TonTestSuite) TestGetConfirmationCountAndBlocks(c *C) {
	c.Assert(s.client.GetConfirmationCount(types.TxIn{}), Equals, int64(0))
	c.Assert(ConfirmationBlocks, Equals, int64(1))
}

func (s *TonTestSuite) TestShouldReportSolvency(c *C) {
	c.Assert(s.client.ShouldReportSolvency(99), Equals, true)
	c.Assert(s.client.ShouldReportSolvency(100), Equals, false)
}

func (s *TonTestSuite) TestGetHeight(c *C) {
	height, err := s.client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(256673)) // mock client height (256674 - 1)
}

func (s *TonTestSuite) TestGetConfig(c *C) {
	cfg := s.client.GetConfig()
	c.Assert(cfg, NotNil)
	c.Assert(cfg.RPCHost, Equals, "mock")
}

func (s *TonTestSuite) TestAccountByAddress(c *C) {
	account, err := s.client.GetAccountByAddress("UQC0PbKWouvBPwTD9vaQ8LOQrMATFgHssiLzgNjxz-nBYngp", nil)
	c.Assert(err, IsNil)
	c.Assert(account, NotNil)
	c.Assert(account.Coins, HasLen, 1)
	c.Assert(account.Coins[0].Asset.Equals(common.TONAsset), Equals, true)
	c.Assert(account.Coins[0].Amount.String(), Equals, "1231978")
}

func (s *TonTestSuite) TestCreateTx(c *C) {
	testCases := []struct {
		Amount uint64
		To     common.Address
		Memo   string
		Fail   bool
	}{
		{
			To:     types2.GetRandomTONAddress(),
			Amount: 54981861,
			Memo:   "TEST1",
		},
		{
			To:     types2.GetRandomTONAddress(),
			Amount: 0,
			Memo:   "TEST2",
			Fail:   true,
		},
		{
			To:     "bogus",
			Amount: 61186468,
			Memo:   "TEST3",
			Fail:   true,
		},
	}

	for _, tc := range testCases {

		coins := []common.Coin{{
			Asset:    common.TONAsset,
			Amount:   cosmos.NewUint(tc.Amount),
			Decimals: 6,
		}}

		txOutItem := types.TxOutItem{
			Chain:            common.TONChain,
			ToAddress:        tc.To,
			VaultPubKeyEddsa: s.pubkey,
			Coins:            coins,
			Memo:             tc.Memo,
		}

		signed, _, txIn, err := s.client.SignTx(txOutItem, 123)
		if tc.Fail {
			c.Assert(err, NotNil)
			continue
		}
		c.Assert(err, IsNil)
		c.Assert(signed, NotNil)
		c.Assert(txIn, NotNil)

		root, err := cell.FromBOC(signed)
		c.Assert(err, IsNil)
		c.Assert(root.RefsNum(), Equals, uint(1))

		payload, err := root.PeekRef(0)
		c.Assert(err, IsNil)

		slice := payload.BeginParse()

		var msg tlb.InternalMessage
		err = tlb.LoadFromCell(&msg, slice)
		c.Assert(err, IsNil)

		c.Assert(msg.Bounce, Equals, false)
		c.Assert(msg.Amount.Nano().Uint64(), Equals, tc.Amount*10) // 1e8 -> 1e9
		c.Assert(msg.DstAddr.IsTestnetOnly(), Equals, common.IsMocknet)

		to := address.MustParseAddr(tc.To.String())
		c.Assert(msg.DstAddr.Equals(to), Equals, true)
	}
}
