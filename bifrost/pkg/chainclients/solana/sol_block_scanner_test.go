package solana

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	. "gopkg.in/check.v1"
)

const TestGasPriceResolution = 1

func httpTestHandler(c *C, rw http.ResponseWriter, fixture string) {
	var content []byte
	var err error

	switch fixture {
	case "500":
		rw.WriteHeader(http.StatusInternalServerError)
	default:
		content, err = os.ReadFile(fixture)
		if err != nil {
			c.Fatal(err)
		}
	}

	rw.Header().Set("Content-Type", "application/json")
	if _, err = rw.Write(content); err != nil {
		c.Fatal(err)
	}
}

func getConfigForTest(rpcHost string) config.BifrostBlockScannerConfiguration {
	return config.BifrostBlockScannerConfiguration{
		ChainID:                    common.SOLChain,
		StartBlockHeight:           1, // avoids querying thorchain for block height
		BlockScanProcessors:        1,
		HTTPRequestTimeout:         time.Second,
		HTTPRequestReadTimeout:     time.Second * 30,
		HTTPRequestWriteTimeout:    time.Second * 30,
		MaxHTTPRequestRetry:        3,
		BlockHeightDiscoverBackoff: time.Second,
		BlockRetryInterval:         time.Second,
		GasCacheBlocks:             100,
		Concurrency:                1,
		GasPriceResolution:         TestGasPriceResolution,
		TransactionBatchSize:       500,
	}
}

type BlockScannerTestSuite struct {
	m       *metrics.Metrics
	bridge  thorclient.ThorchainBridge
	keys    *thorclient.Keys
	scanner *SOLScanner
}

var _ = Suite(&BlockScannerTestSuite{})

func (s *BlockScannerTestSuite) SetUpSuite(c *C) {
	thorchain.SetupConfigForTest()
	s.m = GetMetricForTest(c)
	c.Assert(s.m, NotNil)
	cfg := config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: "",
	}

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := cKeys.NewInMemory(cdc)
	_, _, err := kb.NewMnemonic(cfg.SignerName, cKeys.English, cmd.THORChainHDPath, cfg.SignerPasswd, hd.Secp256k1)
	c.Assert(err, IsNil)
	thorKeys := thorclient.NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd)
	c.Assert(err, IsNil)
	s.keys = thorKeys
	s.bridge, err = thorclient.NewThorchainBridge(cfg, s.m, thorKeys)
	c.Assert(err, IsNil)

	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch req.RequestURI {
		case thorclient.PubKeysEndpoint:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/vaults/pubKeys.json")
		default:
			body, err := io.ReadAll(req.Body)
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
			switch rpcRequest.Method {
			case "getBlock":
				httpTestHandler(c, rw, "./test/getBlock.json")
			case "getSlot":
				httpTestHandler(c, rw, "./test/getSlot.json")
			case "getLatestBlockHash":
				httpTestHandler(c, rw, "./test/getLatestBlockHash.json")
			default:
				rw.WriteHeader(404)
			}
		}
	}))

	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	signerCacheManager, err := signercache.NewSignerCacheManager(storage.GetInternalDb())
	c.Assert(err, IsNil)
	pubKeyManager, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)

	scannerCfg := getConfigForTest("http://" + server.Listener.Addr().String())

	rpcClient := rpc.NewSolRPC("http://"+server.Listener.Addr().String(), scannerCfg.HTTPRequestTimeout)
	scanner, err := NewSOLScanner(scannerCfg, storage, s.bridge, s.m, rpcClient, pubKeyManager, nil, signerCacheManager)
	c.Assert(err, IsNil)
	s.scanner = scanner
}

// func (s *BlockScannerTestSuite) TestGetBlock(c *C) {
// 	txIns, err := s.scanner.FetchTxs(1, 1)
// 	c.Assert(err, IsNil)
// 	c.Assert(len(txIns.TxArray), Equals, 1)
// 	// for _, txIn := range txIns.TxArray {
// 	// 	// fmt.Printf("tx: %s, coins: %+v", txIn.Tx, txIn.Coins[0].String())
// 	// }
// }

func (s *BlockScannerTestSuite) TestGetTxInItem(c *C) {
	// Load the test txs
	txFiles, err := os.ReadDir("test/txs")
	c.Assert(err, IsNil)
	for _, txFile := range txFiles {
		txPath := filepath.Join("test/txs", txFile.Name())
		txFile, err := os.Open(txPath)
		c.Assert(err, IsNil)
		defer txFile.Close()

		// Parse the tx
		tx := new(rpc.RPCTransaction)
		err = json.NewDecoder(txFile).Decode(tx)
		c.Assert(err, IsNil)

		innerInstructions := tx.GetInnerInstructions()

		for i, instruction := range tx.GetInstructions() {
			fmt.Println("Instruction: ", i)

			instInnerInstructions := []rpc.RPCInstruction{}
			// get inner instructions if any
			if len(innerInstructions) > 0 {
				for _, inner := range innerInstructions {
					if inner.Index == i {
						instInnerInstructions = inner.Instructions
					}
				}
			}

			for j, account := range instruction.Accounts {
				fmt.Println("account", j, tx.Transaction.Message.AccountKeys[account])
			}

			if len(instInnerInstructions) > 0 {
				fmt.Println("Inst has inner instructions: ", len(instInnerInstructions))
			}

			// iterate through inner instructions
			for _, inner := range instInnerInstructions {
				for j, account := range inner.Accounts {
					fmt.Println("inner account", j, tx.Transaction.Message.AccountKeys[account])
				}
			}
		}

		txInItem := s.scanner.getTxInItem(*tx, 1)
		c.Assert(txInItem, NotNil)
	}
}
