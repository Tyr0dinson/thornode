package haven

import (
	"encoding/hex"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	ctypes "gitlab.com/thorchain/binance-sdk/common/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/bifrost/config"
	"gitlab.com/thorchain/thornode/bifrost/metrics"
	"gitlab.com/thorchain/thornode/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/cmd"
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	ttypes "gitlab.com/thorchain/thornode/x/thorchain/types"
)

const (
	bob      = "bob"
	password = "password"
)

func TestPackage(t *testing.T) { TestingT(t) }

type HavenSuite struct {
	client *Client
	server *httptest.Server
	bridge *thorclient.ThorchainBridge
	cfg    config.ChainConfiguration
	m      *metrics.Metrics
	keys   *thorclient.Keys
}

var _ = Suite(
	&HavenSuite{},
)

var m *metrics.Metrics

func GetMetricForTest(c *C) *metrics.Metrics {
	if m == nil {
		var err error
		m, err = metrics.NewMetrics(config.MetricsConfiguration{
			Enabled:      false,
			ListenPort:   9000,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
			Chains:       common.Chains{common.XHVChain},
		})
		c.Assert(m, NotNil)
		c.Assert(err, IsNil)
	}
	return m
}

func (s *HavenSuite) SetUpSuite(c *C) {
	ttypes.SetupConfigForTest()
	kb := cKeys.NewInMemory()
	_, _, err := kb.NewMnemonic(bob, cKeys.English, cmd.THORChainHDPath, password, hd.Secp256k1)
	c.Assert(err, IsNil)
	s.keys = thorclient.NewKeysWithKeybase(kb, bob, password)
}

func (s *HavenSuite) SetUpTest(c *C) {
	s.m = GetMetricForTest(c)
	s.cfg = config.ChainConfiguration{
		ChainID:     "XHV",
		UserName:    bob,
		Password:    password,
		DisableTLS:  true,
		HTTPostMode: true,
		BlockScanner: config.BlockScannerConfiguration{
			StartBlockHeight: 1, // avoids querying thorchain for block height
		},
	}
	ns := strconv.Itoa(time.Now().Nanosecond())
	ctypes.Network = ctypes.TestNetwork
	c.Assert(os.Setenv("NET", "mocknet"), IsNil)

	thordir := filepath.Join(os.TempDir(), ns, ".thorcli")
	cfg := config.ClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		SignerName:      bob,
		SignerPasswd:    password,
		ChainHomeFolder: thordir,
	}

	s.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.RequestURI == "/json_rpc" {
			r := struct {
				Method string `json:"method"`
				Params struct {
					AssetType string `json:"asset_type"`
				} `json:"params"`
			}{}
			json.NewDecoder(req.Body).Decode(&r)

			switch {
			case r.Method == "get_info":
				httpTestHandler(c, rw, "../../../../test/fixtures/xhv/get_info.json")
			case r.Method == "get_balance":
				if r.Params.AssetType == "XHV" {
					httpTestHandler(c, rw, "../../../../test/fixtures/xhv/get_balance_xhv.json")
				} else if r.Params.AssetType == "XUSD" {
					httpTestHandler(c, rw, "../../../../test/fixtures/xhv/get_balance_xusd.json")
				} else {
					httpTestHandler(c, rw, "../../../../test/fixtures/xhv/get_balance_other.json")
				}
			case r.Method == "get_block":
				httpTestHandler(c, rw, "../../../../test/fixtures/xhv/get_block.json")
			case r.Method == "generate_from_keys":
				httpTestHandler(c, rw, "../../../../test/fixtures/xhv/generate_wallet.json")
			case r.Method == "open_wallet":
				_, err := rw.Write([]byte(`{ "jsonrpc": "2.0", "id": "0", "result": { } } } }`))
				c.Assert(err, IsNil)
			case r.Method == "refresh":
				httpTestHandler(c, rw, "../../../../test/fixtures/xhv/refresh_wallet.json")
			}
		} else if req.RequestURI == "/get_transactions" {
			b := struct {
				Txs_Hashes []string
			}{}
			json.NewDecoder(req.Body).Decode(&b)
			if b.Txs_Hashes[0] == "27de3e1865c098cd4fded71bae1e8236fd27ce5dce6e524a9ac5cd1a17b5c241" {
				httpTestHandler(c, rw, "../../../../test/fixtures/xhv/tx_not_found.json")
			} else {
				httpTestHandler(c, rw, "../../../../test/fixtures/xhv/get_txs.json")
			}
		} else if req.RequestURI == "/get_transaction_pool" {
			httpTestHandler(c, rw, "../../../../test/fixtures/xhv/get_pool.json")
		} else if strings.HasPrefix(req.RequestURI, "/thorchain/node/") {
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/nodeaccount/template.json")
		} else if req.RequestURI == "/thorchain/lastblock" {
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/lastblock/btc.json")
		} else if strings.HasPrefix(req.RequestURI, "/auth/accounts/") {
			_, err := rw.Write([]byte(`{ "jsonrpc": "2.0", "id": "0", "result": { "height": "0", "result": { "value": { "account_number": "0", "sequence": "0" } } } }`))
			c.Assert(err, IsNil)
		} else if req.RequestURI == "/txs" {
			_, err := rw.Write([]byte(`{"height": "1", "txhash": "AAAA000000000000000000000000000000000000000000000000000000000000", "logs": [{"success": "true", "log": ""}]}`))
			c.Assert(err, IsNil)
		} else if strings.HasPrefix(req.RequestURI, thorclient.AsgardVault) {
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/vaults/asgard.json")
		} else if strings.HasPrefix(req.RequestURI, thorclient.PubKeysEndpoint) {
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/vaults/pubKeys.json")
		}
	}))
	var err error
	cfg.ChainHost = s.server.Listener.Addr().String()
	s.bridge, err = thorclient.NewThorchainBridge(cfg, s.m, s.keys)
	c.Assert(err, IsNil)

	s.cfg.WalletRPCHost = "http://" + s.server.Listener.Addr().String() + "/json_rpc"
	s.cfg.RPCHost = "http://" + s.server.Listener.Addr().String()
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	c.Assert(pubkeyMgr.Start(), IsNil)

	poolMgr := thorclient.NewPoolMgr(s.bridge)
	s.client, err = NewClient(s.keys, s.cfg, nil, s.bridge, pubkeyMgr, poolMgr, s.m)
	c.Assert(err, IsNil)
	c.Assert(s.client, NotNil)
}

func (s *HavenSuite) TearDownTest(_ *C) {
	s.server.Close()
}

func httpTestHandler(c *C, rw http.ResponseWriter, fixture string) {
	content, err := ioutil.ReadFile(fixture)
	if err != nil {
		c.Fatal(err)
	}
	rw.Header().Set("Content-Type", "application/json")
	if _, err := rw.Write(content); err != nil {
		c.Fatal(err)
	}
}

func (s *HavenSuite) TestGetChain(c *C) {
	chain := s.client.GetChain()
	c.Assert(chain, Equals, common.XHVChain)
}

func (s *HavenSuite) TestGetAddress(c *C) {
	c.Assert(os.Setenv("NET", "mainnet"), IsNil)
	pubkey := common.PubKey("tthorpub1addwnpepqflvfv08t6qt95lmttd6wpf3ss8wx63e9vf6fvyuj2yy6nnyna576rfzjks")
	addr := s.client.GetAddress(pubkey)
	c.Assert(addr, Equals, "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg")
}

func (s *HavenSuite) TestGetHeight(c *C) {
	height, err := s.client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(1072520))
}

func (s *HavenSuite) TestGetBlock(c *C) {
	block, err := GetBlock(1072521)
	c.Assert(err, IsNil)
	c.Assert(block.Block_Header.Hash, Equals, "b61ae0c02de22209eb2d707c47638d497649357de10cea3d361f91425cd17849")
	c.Assert(block.Tx_Hashes[0], Equals, "dde09739f16de9cd2b8c51f0258fa3816377fcbf7a60c19a6d5f162f1ea24270")
	c.Assert(len(block.Tx_Hashes), Equals, 1)
}

func (s *HavenSuite) TestGetTxs(c *C) {
	txs, missedTxs, err := GetTxs([]string{"dde09739f16de9cd2b8c51f0258fa3816377fcbf7a60c19a6d5f162f1ea24270"})
	c.Assert(err, IsNil)
	c.Assert(len(txs), Equals, 1)
	c.Assert(len(missedTxs), Equals, 0)
	c.Assert(txs[0].Hash, Equals, "dde09739f16de9cd2b8c51f0258fa3816377fcbf7a60c19a6d5f162f1ea24270")
}

func (s *HavenSuite) TestGetAccount(c *C) {
	acct, err := s.client.GetAccount("tthorpub1addwnpepqt7qug8vk9r3saw8n4r803ydj2g3dqwx0mvq5akhnze86fc536xcycgtrnv", nil)
	c.Assert(err, IsNil)
	c.Assert(acct.AccountNumber, Equals, int64(0))
	c.Assert(acct.Sequence, Equals, int64(0))
	c.Assert(len(acct.Coins), Equals, 2)
	c.Assert(acct.Coins[0].Amount.Uint64(), Equals, uint64(4199655896687))
	c.Assert(acct.Coins[1].Amount.Uint64(), Equals, uint64(5068663437))

	acct1, err := s.client.GetAccount("", nil)
	c.Assert(err, NotNil)
	c.Assert(acct1.AccountNumber, Equals, int64(0))
	c.Assert(acct1.Sequence, Equals, int64(0))
	c.Assert(acct1.Coins, HasLen, 0)
}

func (s *HavenSuite) TestGetMemPool(c *C) {
	txIns, err := s.client.getMemPool(73)
	c.Assert(err, IsNil)
	c.Assert(txIns.TxArray, HasLen, 1)

	// process it again , the tx will be ignored
	txIns, err = s.client.getMemPool(73)
	c.Assert(err, IsNil)
	c.Assert(txIns.TxArray, HasLen, 0)
}

func (s *HavenSuite) TestFetchTxs(c *C) {
	txs, err := s.client.FetchTxs(76)
	c.Assert(err, IsNil)
	c.Assert(txs.Chain, Equals, common.XHVChain)
	c.Assert(txs.Count, Equals, "1")
	c.Assert(txs.TxArray[0].BlockHeight, Equals, int64(76))
	c.Assert(txs.TxArray[0].Tx, Equals, "dde09739f16de9cd2b8c51f0258fa3816377fcbf7a60c19a6d5f162f1ea24270")
	c.Assert(txs.TxArray[0].Sender, Equals, "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg")
	c.Assert(txs.TxArray[0].To, Equals, "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg")
	c.Assert(txs.TxArray[0].Coins.EqualsEx(common.Coins{common.NewCoin(common.XHVAsset, cosmos.NewUint(2142457343))}), Equals, true)
	c.Assert(txs.TxArray[0].Gas.Equals(common.Gas{common.NewCoin(common.XHVAsset, cosmos.NewUint(51630))}), Equals, true)
	c.Assert(len(txs.TxArray), Equals, 1)
}

func (s *HavenSuite) TestGetOutput(c *C) {
	// make a tx
	txContent, err := ioutil.ReadFile("../../../../test/fixtures/xhv/tx.json")
	c.Assert(err, IsNil)
	var tx RawTx
	c.Assert(json.Unmarshal(txContent, &tx), IsNil)
	c.Assert(err, IsNil)
	tx.Hash = "a6413fe6e76ea529351dbd8bdf0ebc38344a43a14a4973fa1d11b048ad9fd330"
	tx.Block_Height = 72

	// set the tx pubkey
	var txPubKey [32]byte
	asByte, err := hex.DecodeString("c105d0cd55cd52f6c2648aadb5ac7bbcfc963158bcc443a192035306d7aaafe6")
	c.Assert(err, IsNil)
	copy(txPubKey[:], asByte[0:32])

	out, err := s.client.getOutput(&tx, &txPubKey)
	c.Assert(err, IsNil)
	c.Assert(out.Address, Equals, "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg")
	c.Assert(out.Amount, Equals, uint64(12000000000000))
	c.Assert(out.Asset, Equals, "XHV")
}

func (s *HavenSuite) TestParseTxExtra(c *C) {
	txContent, err := ioutil.ReadFile("../../../../test/fixtures/xhv/tx.json")
	c.Assert(err, IsNil)
	var tx RawTx
	c.Assert(json.Unmarshal(txContent, &tx), IsNil)
	c.Assert(err, IsNil)

	extra, err := s.client.parseTxExtra(tx.Extra)
	c.Assert(err, IsNil)
	c.Assert(hex.EncodeToString(extra[0x01][0]), Equals, "c105d0cd55cd52f6c2648aadb5ac7bbcfc963158bcc443a192035306d7aaafe6")
	c.Assert(string(extra[0x18][0]), Equals, "ADD:XHV.XHV:tthor1z63f3mzwv3g75az80xwmhrawdqcjpaekk0kd54:hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg")
}

func (s *HavenSuite) TestProcessReOrg(c *C) {
	// can't get previous block meta should not error
	result := struct {
		Block Block `json:"result"`
	}{}
	blockContent, err := ioutil.ReadFile("../../../../test/fixtures/xhv/get_block.json")
	c.Assert(err, IsNil)
	c.Assert(json.Unmarshal(blockContent, &result), IsNil)
	block := result.Block
	// should not trigger re-org process
	reorgedItems, err := s.client.processReorg(block)
	c.Assert(err, IsNil)
	c.Assert(reorgedItems, IsNil)

	// add one UTXO which will trigger the re-org process next
	previousHeight := block.Block_Header.Height - 1
	blockMeta := NewBlockMeta(ttypes.GetRandomTxHash().String(), previousHeight, ttypes.GetRandomTxHash().String())
	hash := "27de3e1865c098cd4fded71bae1e8236fd27ce5dce6e524a9ac5cd1a17b5c241"
	blockMeta.AddCustomerTransaction(hash)
	c.Assert(s.client.blockMetaAccessor.SaveBlockMeta(previousHeight, blockMeta), IsNil)
	s.client.globalErrataQueue = make(chan types.ErrataBlock, 1)
	reorgedItems, err = s.client.processReorg(block)
	c.Assert(err, IsNil)
	c.Assert(reorgedItems, NotNil)
	// make sure there is errata block in the queue
	c.Assert(s.client.globalErrataQueue, HasLen, 1)
	blockMeta, err = s.client.blockMetaAccessor.GetBlockMeta(previousHeight)
	c.Assert(err, IsNil)
	c.Assert(blockMeta, NotNil)
}

func (s *HavenSuite) TestConfirmationCountReady(c *C) {
	c.Assert(s.client.ConfirmationCountReady(types.TxIn{
		Chain:    common.XHVChain,
		TxArray:  nil,
		Filtered: true,
		MemPool:  false,
	}), Equals, true)
	pkey := ttypes.GetRandomPubKey()
	c.Assert(s.client.ConfirmationCountReady(types.TxIn{
		Chain: common.XHVChain,
		TxArray: []types.TxInItem{
			{
				BlockHeight: 2,
				Tx:          "24ed2d26fd5d4e0e8fa86633e40faf1bdfc8d1903b1cd02855286312d48818a2",
				Sender:      "hvxy6hvEBNw8FT8fS9ay1dd111bqwbwnAEVtTbJkNcnaP19yfDPr2TQBFmyN96xubhFb3ddTjFaokWMast17eRkA1jBW1yEvjh",
				To:          "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg",
				Coins: common.Coins{
					common.NewCoin(common.XHVAsset, cosmos.NewUint(123456)),
				},
				Memo:                "MEMO",
				ObservedVaultPubKey: pkey,
			},
		},
		Filtered: true,
		MemPool:  true,
	}), Equals, true)
	s.client.currentBlockHeight.Store(3)
	c.Assert(s.client.ConfirmationCountReady(types.TxIn{
		Chain: common.XHVChain,
		TxArray: []types.TxInItem{
			{
				BlockHeight: 2,
				Tx:          "24ed2d26fd5d4e0e8fa86633e40faf1bdfc8d1903b1cd02855286312d48818a2",
				Sender:      "hvxy6hvEBNw8FT8fS9ay1dd111bqwbwnAEVtTbJkNcnaP19yfDPr2TQBFmyN96xubhFb3ddTjFaokWMast17eRkA1jBW1yEvjh",
				To:          "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg",
				Coins: common.Coins{
					common.NewCoin(common.XHVAsset, cosmos.NewUint(123456)),
				},
				Memo:                "MEMO",
				ObservedVaultPubKey: pkey,
			},
		},
		Filtered:             true,
		MemPool:              false,
		ConfirmationRequired: 0,
	}), Equals, true)

	c.Assert(s.client.ConfirmationCountReady(types.TxIn{
		Chain: common.XHVChain,
		TxArray: []types.TxInItem{
			{
				BlockHeight: 2,
				Tx:          "24ed2d26fd5d4e0e8fa86633e40faf1bdfc8d1903b1cd02855286312d48818a2",
				Sender:      "hvxy6hvEBNw8FT8fS9ay1dd111bqwbwnAEVtTbJkNcnaP19yfDPr2TQBFmyN96xubhFb3ddTjFaokWMast17eRkA1jBW1yEvjh",
				To:          "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg",
				Coins: common.Coins{
					common.NewCoin(common.XHVAsset, cosmos.NewUint(12345600000)),
				},
				Gas: common.Gas{
					common.NewCoin(common.XHVAsset, cosmos.NewUint(40000)),
				},
				Memo:                "MEMO",
				ObservedVaultPubKey: pkey,
			},
		},
		Filtered:             true,
		MemPool:              false,
		ConfirmationRequired: 5,
	}), Equals, false)
}

func (s *HavenSuite) TestGetConfirmationCount(c *C) {
	pkey := ttypes.GetRandomPubKey()
	// no tx in item , confirmation count should be 0
	c.Assert(s.client.GetConfirmationCount(types.TxIn{
		Chain:   common.XHVChain,
		TxArray: nil,
	}), Equals, int64(0))
	// mempool txin , confirmation count should be 0
	c.Assert(s.client.GetConfirmationCount(types.TxIn{
		Chain: common.XHVChain,
		TxArray: []types.TxInItem{
			{
				BlockHeight: 2,
				Tx:          "24ed2d26fd5d4e0e8fa86633e40faf1bdfc8d1903b1cd02855286312d48818a2",
				Sender:      "hvxy6hvEBNw8FT8fS9ay1dd111bqwbwnAEVtTbJkNcnaP19yfDPr2TQBFmyN96xubhFb3ddTjFaokWMast17eRkA1jBW1yEvjh",
				To:          "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg",
				Coins: common.Coins{
					common.NewCoin(common.XHVAsset, cosmos.NewUint(123456)),
				},
				Memo:                "MEMO",
				ObservedVaultPubKey: pkey,
			},
		},
		Filtered:             true,
		MemPool:              true,
		ConfirmationRequired: 0,
	}), Equals, int64(0))

	c.Assert(s.client.GetConfirmationCount(types.TxIn{
		Chain: common.XHVChain,
		TxArray: []types.TxInItem{
			{
				BlockHeight: 2,
				Tx:          "24ed2d26fd5d4e0e8fa86633e40faf1bdfc8d1903b1cd02855286312d48818a2",
				Sender:      "hvxy6hvEBNw8FT8fS9ay1dd111bqwbwnAEVtTbJkNcnaP19yfDPr2TQBFmyN96xubhFb3ddTjFaokWMast17eRkA1jBW1yEvjh",
				To:          "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg",
				Coins: common.Coins{
					common.NewCoin(common.XHVAsset, cosmos.NewUint(123456)),
				},
				Memo:                "MEMO",
				ObservedVaultPubKey: pkey,
			},
		},
		Filtered:             true,
		MemPool:              false,
		ConfirmationRequired: 0,
	}), Equals, int64(0))

	c.Assert(s.client.GetConfirmationCount(types.TxIn{
		Chain: common.XHVChain,
		TxArray: []types.TxInItem{
			{
				BlockHeight: 2,
				Tx:          "24ed2d26fd5d4e0e8fa86633e40faf1bdfc8d1903b1cd02855286312d48818a2",
				Sender:      "hvxy6hvEBNw8FT8fS9ay1dd111bqwbwnAEVtTbJkNcnaP19yfDPr2TQBFmyN96xubhFb3ddTjFaokWMast17eRkA1jBW1yEvjh",
				To:          "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg",
				Coins: common.Coins{
					common.NewCoin(common.XHVAsset, cosmos.NewUint(12345600)),
				},
				Memo:                "MEMO",
				ObservedVaultPubKey: pkey,
			},
		},
		Filtered:             true,
		MemPool:              false,
		ConfirmationRequired: 0,
	}), Equals, int64(0))

	c.Assert(s.client.GetConfirmationCount(types.TxIn{
		Chain: common.XHVChain,
		TxArray: []types.TxInItem{
			{
				BlockHeight: 2,
				Tx:          "24ed2d26fd5d4e0e8fa86633e40faf1bdfc8d1903b1cd02855286312d48818a2",
				Sender:      "hvxy6hvEBNw8FT8fS9ay1dd111bqwbwnAEVtTbJkNcnaP19yfDPr2TQBFmyN96xubhFb3ddTjFaokWMast17eRkA1jBW1yEvjh",
				To:          "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg",
				Coins: common.Coins{
					common.NewCoin(common.XHVAsset, cosmos.NewUint(1700000000)),
				},
				Memo:                "MEMO",
				ObservedVaultPubKey: pkey,
			},
		},
		Filtered:             true,
		MemPool:              false,
		ConfirmationRequired: 0,
	}), Equals, int64(1))

	c.Assert(s.client.GetConfirmationCount(types.TxIn{
		Chain: common.XHVChain,
		TxArray: []types.TxInItem{
			{
				BlockHeight: 2,
				Tx:          "24ed2d26fd5d4e0e8fa86633e40faf1bdfc8d1903b1cd02855286312d48818a2",
				Sender:      "hvxy6hvEBNw8FT8fS9ay1dd111bqwbwnAEVtTbJkNcnaP19yfDPr2TQBFmyN96xubhFb3ddTjFaokWMast17eRkA1jBW1yEvjh",
				To:          "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg",
				Coins: common.Coins{
					common.NewCoin(common.XHVAsset, cosmos.NewUint(11000000000)),
				},
				Memo:                "MEMO",
				ObservedVaultPubKey: pkey,
			},
		},
		Filtered:             true,
		MemPool:              false,
		ConfirmationRequired: 0,
	}), Equals, int64(6))
}

func (s *HavenSuite) TestOnObservedTxIn(c *C) {
	pkey := ttypes.GetRandomPubKey()
	txIn := types.TxIn{
		Count: "1",
		Chain: common.XHVChain,
		TxArray: []types.TxInItem{
			{
				BlockHeight: 1,
				Tx:          "31f8699ce9028e9cd37f8a6d58a79e614a96e3fdd0f58be5fc36d2d95484716f",
				Sender:      "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg",
				To:          "hvxy6hvEBNw8FT8fS9ay1dd111bqwbwnAEVtTbJkNcnaP19yfDPr2TQBFmyN96xubhFb3ddTjFaokWMast17eRkA1jBW1yEvjh",
				Coins: common.Coins{
					common.NewCoin(common.XHVAsset, cosmos.NewUint(123456789)),
				},
				Memo:                "MEMO",
				ObservedVaultPubKey: pkey,
			},
		},
	}
	blockMeta := NewBlockMeta("000000001ab8a8484eb89f04b87d90eb88e2cbb2829e84eb36b966dcb28af90b", 1, "00000000ffa57c95f4f226f751114e9b24fdf8dbe2dbc02a860da9320bebd63e")
	c.Assert(s.client.blockMetaAccessor.SaveBlockMeta(blockMeta.Height, blockMeta), IsNil)
	s.client.OnObservedTxIn(txIn.TxArray[0], 1)
	blockMeta, err := s.client.blockMetaAccessor.GetBlockMeta(1)
	c.Assert(err, IsNil)
	c.Assert(blockMeta, NotNil)

	txIn = types.TxIn{
		Count: "1",
		Chain: common.XHVChain,
		TxArray: []types.TxInItem{
			{
				BlockHeight: 2,
				Tx:          "24ed2d26fd5d4e0e8fa86633e40faf1bdfc8d1903b1cd02855286312d48818a2",
				Sender:      "hvxy6hvEBNw8FT8fS9ay1dd111bqwbwnAEVtTbJkNcnaP19yfDPr2TQBFmyN96xubhFb3ddTjFaokWMast17eRkA1jBW1yEvjh",
				To:          "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg",
				Coins: common.Coins{
					common.NewCoin(common.XHVAsset, cosmos.NewUint(123456)),
				},
				Memo:                "MEMO",
				ObservedVaultPubKey: pkey,
			},
		},
	}
	blockMeta = NewBlockMeta("000000001ab8a8484eb89f04b87d90eb88e2cbb2829e84eb36b966dcb28af90b", 2, "00000000ffa57c95f4f226f751114e9b24fdf8dbe2dbc02a860da9320bebd63e")
	c.Assert(s.client.blockMetaAccessor.SaveBlockMeta(blockMeta.Height, blockMeta), IsNil)
	s.client.OnObservedTxIn(txIn.TxArray[0], 2)
	blockMeta, err = s.client.blockMetaAccessor.GetBlockMeta(2)
	c.Assert(err, IsNil)
	c.Assert(blockMeta, NotNil)

	txIn = types.TxIn{
		Count: "2",
		Chain: common.XHVChain,
		TxArray: []types.TxInItem{
			{
				BlockHeight: 3,
				Tx:          "44ed2d26fd5d4e0e8fa86633e40faf1bdfc8d1903b1cd02855286312d48818a2",
				Sender:      "hvxy6hvEBNw8FT8fS9ay1dd111bqwbwnAEVtTbJkNcnaP19yfDPr2TQBFmyN96xubhFb3ddTjFaokWMast17eRkA1jBW1yEvjh",
				To:          "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg",
				Coins: common.Coins{
					common.NewCoin(common.XHVAsset, cosmos.NewUint(12345678)),
				},
				Memo:                "MEMO",
				ObservedVaultPubKey: pkey,
			},
			{
				BlockHeight: 3,
				Tx:          "54ed2d26fd5d4e0e8fa86633e40faf1bdfc8d1903b1cd02855286312d48818a2",
				Sender:      "hvxy6hvEBNw8FT8fS9ay1dd111bqwbwnAEVtTbJkNcnaP19yfDPr2TQBFmyN96xubhFb3ddTjFaokWMast17eRkA1jBW1yEvjh",
				To:          "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg",
				Coins: common.Coins{
					common.NewCoin(common.XHVAsset, cosmos.NewUint(123456)),
				},
				Memo:                "MEMO",
				ObservedVaultPubKey: pkey,
			},
		},
	}
	blockMeta = NewBlockMeta("000000001ab8a8484eb89f04b87d90eb88e2cbb2829e84eb36b966dcb28af90b", 3, "00000000ffa57c95f4f226f751114e9b24fdf8dbe2dbc02a860da9320bebd63e")
	c.Assert(s.client.blockMetaAccessor.SaveBlockMeta(blockMeta.Height, blockMeta), IsNil)
	for _, item := range txIn.TxArray {
		s.client.OnObservedTxIn(item, 3)
	}

	blockMeta, err = s.client.blockMetaAccessor.GetBlockMeta(3)
	c.Assert(err, IsNil)
	c.Assert(blockMeta, NotNil)
}
