package blockscanner

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	ckeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"gitlab.com/thorchain/thornode/bifrost/config"
	"gitlab.com/thorchain/thornode/bifrost/metrics"
	"gitlab.com/thorchain/thornode/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/cmd"
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain"
	. "gopkg.in/check.v1"
)

type SolvencyCheckerTestSuite struct {
	m      *metrics.Metrics
	bridge *thorclient.ThorchainBridge
	cfg    config.ClientConfiguration
	keys   *thorclient.Keys
}

var _ = Suite(&SolvencyCheckerTestSuite{})

func (s *SolvencyCheckerTestSuite) SetUpSuite(c *C) {
	var err error
	s.m = m
	thorchain.SetupConfigForTest()
	cfg := config.ClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: ".",
	}
	kb := ckeys.NewInMemory()
	_, _, err = kb.NewMnemonic(cfg.SignerName, ckeys.English, cmd.THORChainHDPath, hd.Secp256k1)
	c.Assert(err, IsNil)

	s.cfg = cfg
	s.keys = thorclient.NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd)
	s.bridge, err = thorclient.NewThorchainBridge(s.cfg, s.m, s.keys)
	c.Assert(err, IsNil)
}

func (s *SolvencyCheckerTestSuite) TearDownSuite(c *C) {
}

func httpTestHandler(c *C, rw http.ResponseWriter, fixture string) {
	var content []byte
	var err error

	switch fixture {
	case "500":
		rw.WriteHeader(http.StatusInternalServerError)
	default:
		content, err = ioutil.ReadFile(fixture)
		if err != nil {
			c.Fatal(err)
		}
	}

	rw.Header().Set("Content-Type", "application/json")
	if _, err := rw.Write(content); err != nil {
		c.Fatal(err)
	}
}
func (s *SolvencyCheckerTestSuite) TestSolvencyChecker(c *C) {
	storage := storage.NewMemStorage()
	db, err := leveldb.Open(storage, nil)
	c.Assert(err, IsNil)
	dbAccessor, err := NewLevelDBScannerStorage(db)
	c.Assert(err, IsNil)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, thorclient.AsgardVault):
			{
				httpTestHandler(c, w, "../../test/fixtures/endpoints/vaults/asgard.json")
			}
		}
	})
	server := httptest.NewServer(h)
	cfg := s.cfg
	cfg.ChainHost = server.Listener.Addr().String()
	thorchainBridge, err := thorclient.NewThorchainBridge(cfg, s.m, s.keys)
	c.Assert(err, IsNil)
	sc, err := NewSolvencyChecker(thorchainBridge, common.BTCChain, dbAccessor, func(height int64, pubKey common.PubKey) (common.Account, error) {
		return common.Account{}, nil
	})
	c.Assert(err, IsNil)
	c.Assert(sc, NotNil)

	// Transaction Not sending to asgard , should not update vault balance
	txIn := types.TxIn{
		Count: "1",
		Chain: common.BTCChain,
		TxArray: []types.TxInItem{
			{
				BlockHeight: 1024,
				Tx:          thorchain.GetRandomTxHash().String(),
				Memo:        "whatever",
				Sender:      thorchain.GetRandomBTCAddress().String(),
				To:          thorchain.GetRandomBTCAddress().String(),
				Coins: common.NewCoins(
					common.NewCoin(common.BTCAsset, cosmos.NewUint(1024000)),
				),
				Gas:                 nil,
				ObservedVaultPubKey: "",
			},
		},
		MemPool: true,
	}
	sc.UpdateMemPool(txIn)
	toAddr, err := common.NewAddress(txIn.TxArray[0].To)
	c.Assert(err, IsNil)
	vb, err := dbAccessor.GetVaultBalance(common.BTCChain, toAddr)
	c.Assert(err, IsNil)
	c.Assert(vb.Coins.IsEmpty(), Equals, true)

	sc.RestoreVaultBalanceFromAsgard()
	asgardVaultPubKey, err := common.NewPubKey("tthorpub1addwnpepqgju00ejlv07ngmwdqzh5aq7uv8zstwfd5cn6wzv5fuj65jjrqxpvxfsya0")
	c.Assert(err, IsNil)
	toAddr, err = asgardVaultPubKey.GetAddress(common.BTCChain)
	vaultBalance, err := dbAccessor.GetVaultBalance(common.BTCChain, toAddr)
	c.Assert(err, IsNil)
	c.Assert(vaultBalance.Coins.IsEmpty(), Equals, false)
	txIn = types.TxIn{
		Count: "1",
		Chain: common.BTCChain,
		TxArray: []types.TxInItem{
			{
				BlockHeight: 1024,
				Tx:          thorchain.GetRandomTxHash().String(),
				Memo:        "consolidate",
				Sender:      toAddr.String(),
				To:          toAddr.String(),
				Coins: common.NewCoins(
					common.NewCoin(common.BTCAsset, cosmos.NewUint(102400000)),
				),
				Gas: common.Gas{
					common.NewCoin(common.BTCAsset, cosmos.NewUint(1024)),
				},
			},
			{
				BlockHeight: 1024,
				Tx:          thorchain.GetRandomTxHash().String(),
				Memo:        "inbound to asgard",
				Sender:      thorchain.GetRandomBTCAddress().String(),
				To:          toAddr.String(),
				Coins: common.NewCoins(
					common.NewCoin(common.BTCAsset, cosmos.NewUint(102400000)),
				),
				Gas: common.Gas{
					common.NewCoin(common.BTCAsset, cosmos.NewUint(1024)),
				},
			},
			{
				BlockHeight: 1024,
				Tx:          thorchain.GetRandomTxHash().String(),
				Memo:        "outbound from asgard",
				Sender:      toAddr.String(),
				To:          thorchain.GetRandomBTCAddress().String(),
				Coins: common.NewCoins(
					common.NewCoin(common.BTCAsset, cosmos.NewUint(1024000)),
				),
				Gas: common.Gas{
					common.NewCoin(common.BTCAsset, cosmos.NewUint(1024)),
				},
			},
		},
		MemPool: true,
	}
	sc.UpdateMemPool(txIn)
	vaultBalance, err = dbAccessor.GetVaultBalance(common.BTCChain, toAddr)
	c.Assert(err, IsNil)
	vaultBalance.Coins.Contains(common.NewCoin(common.BTCAsset, cosmos.NewUint(196790607)))
	// nothing need to do
	c.Assert(sc.CheckSolvency(types.TxIn{}), IsNil)

	sc.accountAccessor = func(height int64, pubKey common.PubKey) (common.Account, error) {
		return common.Account{
			Sequence:      0,
			AccountNumber: 0,
			Coins: common.Coins{
				common.NewCoin(common.BTCAsset, cosmos.NewUint(196790607+102400000-1024000-1024)),
			},
			HasMemoFlag: false,
		}, nil
	}
	txIn = types.TxIn{
		Count: "1",
		Chain: common.BTCChain,
		TxArray: []types.TxInItem{
			{
				BlockHeight: 1024,
				Tx:          thorchain.GetRandomTxHash().String(),
				Memo:        "inbound to asgard",
				Sender:      thorchain.GetRandomBTCAddress().String(),
				To:          toAddr.String(),
				Coins: common.NewCoins(
					common.NewCoin(common.BTCAsset, cosmos.NewUint(102400000)),
				),
				Gas: common.Gas{
					common.NewCoin(common.BTCAsset, cosmos.NewUint(1024)),
				},
			},
			{
				BlockHeight: 1024,
				Tx:          thorchain.GetRandomTxHash().String(),
				Memo:        "outbound from asgard",
				Sender:      toAddr.String(),
				To:          thorchain.GetRandomBTCAddress().String(),
				Coins: common.NewCoins(
					common.NewCoin(common.BTCAsset, cosmos.NewUint(1024000)),
				),
				Gas: common.Gas{
					common.NewCoin(common.BTCAsset, cosmos.NewUint(1024)),
				},
			},
		},
		MemPool: false,
	}
	c.Assert(sc.CheckSolvency(txIn), IsNil)

	txIn = types.TxIn{
		Count: "1",
		Chain: common.BTCChain,
		TxArray: []types.TxInItem{
			{
				BlockHeight: 1024,
				Tx:          thorchain.GetRandomTxHash().String(),
				Memo:        "inbound to asgard",
				Sender:      thorchain.GetRandomBTCAddress().String(),
				To:          toAddr.String(),
				Coins: common.NewCoins(
					common.NewCoin(common.BTCAsset, cosmos.NewUint(102400000)),
				),
				Gas: common.Gas{
					common.NewCoin(common.BTCAsset, cosmos.NewUint(1024)),
				},
			},
			{
				BlockHeight: 1024,
				Tx:          thorchain.GetRandomTxHash().String(),
				Memo:        "outbound from asgard",
				Sender:      toAddr.String(),
				To:          thorchain.GetRandomBTCAddress().String(),
				Coins: common.NewCoins(
					common.NewCoin(common.BTCAsset, cosmos.NewUint(1024000)),
				),
				Gas: common.Gas{
					common.NewCoin(common.BTCAsset, cosmos.NewUint(1024)),
				},
			},
		},
		MemPool: false,
	}
	// become insolvent
	c.Assert(sc.CheckSolvency(txIn), NotNil)
}
