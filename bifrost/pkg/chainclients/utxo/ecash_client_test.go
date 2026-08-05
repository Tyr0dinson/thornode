//go:build mocknet
// +build mocknet

package utxo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	utxorpc "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

const (
	xecFixtureBlockHash = "0a0d997aeaf94c89379fbf9de688ecaeded81c37774f6680f83703a17e32b438"
	xecFixtureInbound   = "05aa81ee89142a6358263ee87e34fea77534bfd0e4c5ba9def56b6ea0d729cb4"
	xecFixtureInput     = "d86cbb3662b3ef30dc2b1648963d2b33433a3a3e045159230fb1ad558cc8d7ff"
	xecFixtureVault     = "qpxh73huzlhjfzcccr03zkpd9nd3wsaseg5aean4r2"
)

type xecFixtureBridge struct {
	thorclient.ThorchainBridge
}

func (xecFixtureBridge) GetMimir(string) (int64, error) { return -1, nil }

type ECashClientSuite struct {
	client          *Client
	server          *httptest.Server
	scannerStorage  *blockscanner.BlockScannerStorage
	feeQueue        chan common.NetworkFee
	broadcastParams []interface{}
	broadcastMu     sync.Mutex
}

var _ = Suite(&ECashClientSuite{})

func (s *ECashClientSuite) fixturePath(name string) string {
	return filepath.Join("../../../../test/fixtures/xec", name)
}

func (s *ECashClientSuite) fixtureName(method string, params []interface{}) string {
	switch method {
	case "getblockhash":
		return "blockhash.json"
	case "getblock":
		return "block_verbose.json"
	case "getblockcount":
		return "blockcount.json"
	case "getblockstats":
		return "blockstats.json"
	case "getnetworkinfo":
		return "getnetworkinfo.json"
	case "listunspent":
		return "listunspent.json"
	case "sendrawtransaction":
		return "sendrawtransaction.json"
	case "getrawtransaction":
		if len(params) > 0 && params[0] == xecFixtureInput {
			return "tx-input.json"
		}
		return "tx-inbound.json"
	default:
		return ""
	}
}

func (s *ECashClientSuite) fixtureResponse(method string, params []interface{}, id interface{}) (map[string]interface{}, error) {
	name := s.fixtureName(method, params)
	if name == "" {
		return nil, fmt.Errorf("unexpected RPC method: %s", method)
	}
	buf, err := os.ReadFile(s.fixturePath(name))
	if err != nil {
		return nil, err
	}

	var response map[string]interface{}
	if err := json.Unmarshal(buf, &response); err != nil {
		return nil, err
	}
	response["id"] = id
	return response, nil
}

func (s *ECashClientSuite) SetUpTest(c *C) {
	s.broadcastMu.Lock()
	s.broadcastParams = nil
	s.broadcastMu.Unlock()

	s.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			c.Errorf("read RPC request: %v", err)
			http.Error(rw, "failed to read request", http.StatusBadRequest)
			return
		}
		rw.Header().Set("Content-Type", "application/json")

		var batch []struct {
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
			ID     interface{}   `json:"id"`
		}
		if json.Unmarshal(body, &batch) == nil && len(batch) > 0 {
			responses := make([]map[string]interface{}, len(batch))
			for i, request := range batch {
				responses[i], err = s.fixtureResponse(request.Method, request.Params, request.ID)
				if err != nil {
					c.Errorf("load RPC fixture: %v", err)
					http.Error(rw, "failed to load RPC fixture", http.StatusInternalServerError)
					return
				}
			}
			if err := json.NewEncoder(rw).Encode(responses); err != nil {
				c.Errorf("encode batch RPC response: %v", err)
			}
			return
		}

		var request struct {
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
			ID     interface{}   `json:"id"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			c.Errorf("decode RPC request: %v", err)
			http.Error(rw, "failed to decode RPC request", http.StatusBadRequest)
			return
		}
		if request.Method == "sendrawtransaction" {
			s.broadcastMu.Lock()
			s.broadcastParams = append([]interface{}(nil), request.Params...)
			s.broadcastMu.Unlock()
		}
		response, err := s.fixtureResponse(request.Method, request.Params, request.ID)
		if err != nil {
			c.Errorf("load RPC fixture: %v", err)
			http.Error(rw, "failed to load RPC fixture", http.StatusInternalServerError)
			return
		}
		if err := json.NewEncoder(rw).Encode(response); err != nil {
			c.Errorf("encode RPC response: %v", err)
		}
	}))

	rpcClient, err := utxorpc.NewClient(
		s.server.URL, "fixture", "fixture", 0, time.Second, common.XECChain, zerolog.Nop(),
	)
	c.Assert(err, IsNil)

	chainCfg := config.BifrostChainConfiguration{ChainID: common.XECChain}
	chainCfg.BlockScanner.ChainID = common.XECChain
	chainCfg.BlockScanner.StartBlockHeight = 1
	chainCfg.UTXO.BlockCacheCount = 1_000
	chainCfg.UTXO.EstimatedAverageTxSize = 264
	chainCfg.UTXO.MaxSatsPerVByte = 9_765
	chainCfg.UTXO.MaxUTXOsToSpend = 10
	chainCfg.UTXO.TransactionBatchSize = 100

	s.scannerStorage, err = blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	db := s.scannerStorage.GetInternalDb()
	temporalStorage, err := sharedutxo.NewTemporalStorage(db, 0)
	c.Assert(err, IsNil)
	cache, err := signercache.NewSignerCacheManager(db)
	c.Assert(err, IsNil)

	s.feeQueue = make(chan common.NetworkFee, 1)

	s.client = &Client{
		cfg:                       chainCfg,
		log:                       zerolog.Nop(),
		m:                         GetMetricForTest(c, common.XECChain),
		rpc:                       rpcClient,
		bridge:                    xecFixtureBridge{},
		wg:                        &sync.WaitGroup{},
		signerLock:                &sync.Mutex{},
		vaultLocks:                make(map[string]*sync.Mutex),
		temporalStorage:           temporalStorage,
		signerCacheManager:        cache,
		globalNetworkFeeQueue:     s.feeQueue,
		regexpRemoveTrailingZeros: regexp.MustCompile(`(?:00)+$`),
	}
	s.client.blockScanner, err = blockscanner.NewBlockScanner(
		chainCfg.BlockScanner, s.scannerStorage, s.client.m, s.client.bridge, s.client,
	)
	c.Assert(err, IsNil)
	s.client.asgardCache.Store(&sharedutxo.AsgardCache{
		Addresses: []common.Address{common.Address(xecFixtureVault)},
		FetchedAt: time.Now(),
	})
}

func (s *ECashClientSuite) TearDownTest(c *C) {
	s.server.Close()
	c.Assert(s.scannerStorage.Close(), IsNil)
}

func (s *ECashClientSuite) TestFetchTxsNormalizesXECAndReportsNativeFee(c *C) {
	// Consolidation is unrelated to scanning and would require a full THORChain
	// bridge fixture, so mark it in progress for this focused public-path test.
	s.client.consolidateInProgress.Store(true)
	defer s.client.consolidateInProgress.Store(false)

	txIn, err := s.client.FetchTxs(102, 102)
	c.Assert(err, IsNil)
	c.Check(txIn.Chain, Equals, common.XECChain)
	c.Assert(txIn.TxArray, HasLen, 1)
	item := txIn.TxArray[0]
	c.Check(item.Tx, Equals, xecFixtureInbound)
	c.Check(item.To, Equals, xecFixtureVault)
	c.Check(item.Memo, Equals, "SWAP:THOR.RUNE:tthor1fixture")
	c.Assert(item.Coins, HasLen, 1)
	c.Check(item.Coins[0].Amount.Equal(cosmos.NewUint(10_000_000_000)), Equals, true)
	c.Check(item.Coins[0].Decimals, Equals, int64(2))
	c.Assert(item.Gas, HasLen, 1)
	c.Check(item.Gas[0].Amount.Equal(cosmos.NewUint(264_000_000)), Equals, true)
	c.Check(item.Gas[0].Decimals, Equals, int64(2))

	c.Check(s.client.minRelayFeeSats.Load(), Equals, uint64(1_000))

	select {
	case fee := <-s.feeQueue:
		c.Check(fee.Chain, Equals, common.XECChain)
		c.Check(fee.TransactionSize, Equals, uint64(264))
		c.Check(fee.TransactionRate, Equals, uint64(1))
	case <-time.After(time.Second):
		c.Fatal("expected XEC network fee")
	}

	blockMeta, err := s.client.temporalStorage.GetBlockMeta(102)
	c.Assert(err, IsNil)
	c.Assert(blockMeta, NotNil)
	c.Check(blockMeta.Height, Equals, int64(102))
	c.Check(blockMeta.BlockHash, Equals, xecFixtureBlockHash)
}

func (s *ECashClientSuite) TestSignAndBroadcastOneXEC(c *C) {
	wif, err := btcutil.DecodeWIF("cRr3fyhDouygaZKvQEKHWiKyNCqCJHBF5h4NifbxVAyYiWRb28xL")
	c.Assert(err, IsNil)
	s.client.nodePrivKey = wif.PrivKey
	s.client.nodePubKey, err = bech32AccountPubKey(wif.PrivKey)
	c.Assert(err, IsNil)

	txOut := stypes.TxOutItem{
		Chain:       common.XECChain,
		ToAddress:   common.Address(xecFixtureVault),
		VaultPubKey: s.client.nodePubKey,
		Coins: common.Coins{
			common.NewCoin(common.XECAsset, cosmos.NewUint(common.One)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.XECAsset, cosmos.NewUint(264_000_000)),
		},
		GasRate: 1,
		InHash:  common.TxID("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		Memo:    "OUT:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}

	sourceScript, err := s.client.getSourceScript(txOut)
	c.Assert(err, IsNil)
	_, inputAmounts, err := s.client.buildTx(txOut, sourceScript)
	c.Assert(err, IsNil)
	c.Check(inputAmounts[formatUtxoKey(xecFixtureInbound, 2)], Equals, int64(4_999_989_736))

	payload, checkpointBytes, observation, err := s.client.SignTx(txOut, 102)
	c.Assert(err, IsNil)
	c.Assert(payload, NotNil)
	c.Check(checkpointBytes, HasLen, 0)
	c.Assert(observation, NotNil)
	c.Check(observation.To, Equals, xecFixtureVault)
	c.Assert(observation.Coins, HasLen, 1)
	c.Check(observation.Coins[0].Amount.Equal(cosmos.NewUint(common.One)), Equals, true)
	c.Check(observation.Coins[0].Decimals, Equals, int64(2))

	msgTx := wire.NewMsgTx(wire.TxVersion)
	c.Assert(msgTx.Deserialize(bytes.NewReader(payload)), IsNil)
	c.Assert(len(msgTx.TxOut) > 0, Equals, true)
	c.Check(msgTx.TxOut[0].Value, Equals, int64(100))

	txid, err := s.client.BroadcastTx(txOut, payload)
	c.Assert(err, IsNil)
	c.Check(txid, Equals, xecFixtureInbound)
	s.broadcastMu.Lock()
	defer s.broadcastMu.Unlock()
	c.Assert(s.broadcastParams, HasLen, 1)
	_, ok := s.broadcastParams[0].(string)
	c.Check(ok, Equals, true)
}

func (s *ECashClientSuite) TestDustBaseUnits(c *C) {
	c.Check(s.client.dustBaseUnits().Equal(cosmos.NewUint(10_000)), Equals, true)

	btcClient := &Client{}
	btcClient.cfg.ChainID = common.BTCChain
	c.Check(btcClient.dustBaseUnits().Equal(common.BTCChain.DustThreshold()), Equals, true)
}
