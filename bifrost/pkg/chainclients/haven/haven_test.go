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
	_, _, err := kb.NewMnemonic(bob, cKeys.English, cmd.THORChainHDPath, hd.Secp256k1)
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
			}
		} else if req.RequestURI == "/get_transactions" {
			httpTestHandler(c, rw, "../../../../test/fixtures/xhv/get_txs.json")
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
	c.Assert(height, Equals, int64(1072521))
}

func (s *HavenSuite) TestGetBlock(c *C) {
	block, err := GetBlock(1072521)
	c.Assert(err, IsNil)
	c.Assert(block.Block_Header.Hash, Equals, "ca34a34e93cd4b2b5e8a38f6e117b1679aeb5ccc500d11190a1dd51a519f7f2c")
	c.Assert(block.Tx_Hashes[0], Equals, "a6413fe6e76ea529351dbd8bdf0ebc38344a43a14a4973fa1d11b048ad9fd330")
	c.Assert(len(block.Tx_Hashes), Equals, 1)
}

func (s *HavenSuite) TestGetTxs(c *C) {
	txs, err := GetTxes([]string{"a6413fe6e76ea529351dbd8bdf0ebc38344a43a14a4973fa1d11b048ad9fd330"})
	c.Assert(err, IsNil)
	c.Assert(len(txs), Equals, 1)
	c.Assert(txs[0].Hash, Equals, "a6413fe6e76ea529351dbd8bdf0ebc38344a43a14a4973fa1d11b048ad9fd330")
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
	txs, err := s.client.FetchTxs(72)
	c.Assert(err, IsNil)
	c.Assert(txs.Chain, Equals, common.XHVChain)
	c.Assert(txs.Count, Equals, "1")
	c.Assert(txs.TxArray[0].BlockHeight, Equals, int64(72))
	c.Assert(txs.TxArray[0].Tx, Equals, "a6413fe6e76ea529351dbd8bdf0ebc38344a43a14a4973fa1d11b048ad9fd330")
	c.Assert(txs.TxArray[0].Sender, Equals, "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg")
	c.Assert(txs.TxArray[0].To, Equals, "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg")
	c.Assert(txs.TxArray[0].Coins.EqualsEx(common.Coins{common.NewCoin(common.XHVAsset, cosmos.NewUint(1200000000))}), Equals, true)
	c.Assert(txs.TxArray[0].Gas.Equals(common.Gas{common.NewCoin(common.XHVAsset, cosmos.NewUint(51660))}), Equals, true)
	c.Assert(len(txs.TxArray), Equals, 1)
}

func (s *HavenSuite) TestGetOutput(c *C) {
	// make a tx
	txJson := "{\n  \"version\": 6, \n  \"vin\": [ {\n      \"key\": {\n        \"amount\": 0, \n        \"key_offsets\": [ 0, 1, 1, 3, 1, 3, 1, 4, 1, 5, 2\n        ], \n        \"k_image\": \"7913ed5e31e099565b674d629ce5a3d2515c4487ee49d7ea2dedaf0345388f6a\"\n      }\n    }\n  ], \n  \"vout\": [ {\n      \"amount\": 0, \n      \"target\": {\n        \"key\": \"3798c60ae30ae8ad8d8eb1a18cfa6259c734e9a9fb8a4157e719f9355641f2d5\"\n      }\n    }, {\n      \"amount\": 0, \n      \"target\": {\n        \"key\": \"b9a6f786c823d08158d0bdcdf3e21895ff87c3520727a0109d36a44a5e3e9135\"\n      }\n    }\n  ], \n  \"extra\": [ 1, 193, 5, 208, 205, 85, 205, 82, 246, 194, 100, 138, 173, 181, 172, 123, 188, 252, 150, 49, 88, 188, 196, 67, 161, 146, 3, 83, 6, 215, 170, 175, 230, 2, 9, 1, 224, 89, 76, 1, 245, 226, 38, 207, 24, 155, 1, 65, 68, 68, 58, 88, 72, 86, 46, 88, 72, 86, 58, 116, 116, 104, 111, 114, 49, 122, 54, 51, 102, 51, 109, 122, 119, 118, 51, 103, 55, 53, 97, 122, 56, 48, 120, 119, 109, 104, 114, 97, 119, 100, 113, 99, 106, 112, 97, 101, 107, 107, 48, 107, 100, 53, 52, 58, 104, 118, 120, 121, 54, 65, 66, 122, 72, 81, 70, 98, 102, 81, 78, 114, 68, 106, 54, 107, 98, 68, 85, 98, 107, 55, 57, 101, 97, 69, 112, 98, 112, 84, 76, 120, 80, 106, 90, 80, 90, 99, 76, 76, 86, 110, 66, 88, 55, 75, 71, 65, 105, 101, 112, 52, 99, 103, 80, 77, 112, 49, 104, 85, 116, 72, 65, 87, 114, 110, 65, 70, 113, 49, 111, 50, 54, 81, 56, 113, 118, 68, 102, 54, 99, 98, 109, 99, 55, 106, 116, 70, 78, 102, 88, 72, 77, 103\n  ], \n  \"pricing_record_height\": 0, \n  \"output_unlock_times\": [ 0, 0\n  ], \n  \"amount_burnt\": 0, \n  \"amount_minted\": 0, \n  \"rct_signatures\": {\n    \"type\": 7, \n    \"txnFee\": 516600000, \n    \"txnOffshoreFee\": 0, \n    \"ecdhInfo\": [ {\n        \"amount\": \"d4ed75fe8b04ea39\"\n      }, {\n        \"amount\": \"8950371d85e446ee\"\n      }], \n    \"outPk\": [ \"68a9fe1e4f10cc9370433811bdc6d3bdbed94db5093284f222d944067f12776d\", \"14c89f6d2af36c652a1f24637459322167436c0bc2fbb22d15517da8b1c01305\"], \n    \"maskSums\": [ \"0000000000000000000000000000000000000000000000000000000000000000\", \"0000000000000000000000000000000000000000000000000000000000000000\"]\n  }, \n  \"rctsig_prunable\": {\n    \"nbp\": 1, \n    \"bp\": [ {\n        \"A\": \"20b29c681a29308443426bd89785849068690c6a3cd810db6a7f6b9728eb8577\", \n        \"S\": \"a2c78fd91225a6fcc73bc56ff6866174aa2a014d52defa350b83cbef83901eaf\", \n        \"T1\": \"449d2ee79807843952372bfd232493619c0e06c9486b53cd30a479b4ce9c7421\", \n        \"T2\": \"37a16f219f3844924fd8788bc070ff32f4a48464d909c426b638352c9ad45f59\", \n        \"taux\": \"1bdf538bb09907507525115f3a1732bd27d6f0cc3387f092cdb1862a534fbb06\", \n        \"mu\": \"d61a1e42be7b36b23f639baeab3a6a6833d36708be62f524ad6ccc06a8922e02\", \n        \"L\": [ \"10fdefbc870d812c38d96dba33a22de3b7df973c38bbb6d35526281784e39048\", \"49515d8c402de0a370bf626a93deeb90dfe366e1c44955bba04333463edded6a\", \"86f6b5e4599f2fa522da645fb645a17930c829d933975cd5d3c5489d19739f01\", \"03b5973fed6b13d16f9f64fb8050f888e6314fe8675308b6c7fb40e90093443e\", \"f9ee144e6f6ad11390b7328aba422608103f210d33acadf36af33b55b304b003\", \"5220b40e4531d558ba4d2631f67f355e16170fffeb566be7776d0973a03b3310\", \"6b0c23cfdb50a7c31f60d97e6e8ac1e19137b3fc2b47689e580748e8bebb98c4\"\n        ], \n        \"R\": [ \"c22e65b2ce3555034c57e18d925987bd697a1683694ce10dc5e03d5fcad8c8ea\", \"be8aae39c869dae723e7ebdaf491cd4f5aaddefb8ef23f6ebfd22dba69f4947e\", \"6b98923362112f0e27f6322f79211e0b3f5bc791caf969d657b2a05690070ccf\", \"ae6c15f1a5536edc19db4d23b75603aa4985f3f16bec687a5f0f21ba01531fef\", \"1739d9f99148293d1d61747ff3bece5e1774b0306e0dff3c288806828e32ed7d\", \"c07b95f3bff2bf627ce6a7fac1af17fb36764334fa5abee9068e1d508c360230\", \"5e056368e76dfcd4e26342e296a86133c86827523f5e1daa9ae30c0291590e9a\"\n        ], \n        \"a\": \"c2487cbe2f17ad5d8e335e04b587828fd4c530751788ec63ae90a0c57ca53c09\", \n        \"b\": \"14278e22295856147ce18562e93c7c216df04cead77b49f3fb413121db200305\", \n        \"t\": \"083300d424529b0f6f6ffc67aa0f9e0bbe1d33057a5debaf923f8056a7f2c20f\"\n      }\n    ], \n    \"CLSAGs\": [ {\n        \"s\": [ \"5b02a2838766a48fdce7e897f986b338fe08c2f137893f042f542bb1d7d74508\", \"241e16f74182b427f443516c6193ac0c920dc23d46ea233ea35bc6e156a7c507\", \"801fdf7b4c5597d7d08f497f2d3fc02608290f8382fb5b4cbf85e6069c6e3909\", \"487786fdfe5dc3fd0ca96169a0e853dbffeea23ab11858527214cf239cafe305\", \"4a335531e762dd3ab3c27cf18951d7d38cb1cd6067b8924fea72f6a617bdfa0d\", \"9cf5e0119ef0d271952057a60441ea5f46141bb20728313f73b296f35f04c206\", \"c84784d963c213ccf38fe30c18da1c1c7514184678d65ffbe8562c92e2f6ec01\", \"9efee7f9c2241d6736b2c487aae7f0879dd440005b1a27d5c9499557fcd0a205\", \"21c8e5f6025652776a7522c30dbfa7c51f732ae31b4af29771cf409705123a02\", \"58b69a93025161a5fcebd2bc3ef4d7fe451a3d5ff852bd7e49db19b6af4a170d\", \"7a5c20b689c5c1a5da038b1c0a2a16537901f3050efea21de806799d4ab45a02\"], \n        \"c1\": \"dacae6852b8ed97b989c239becbecaed4bcf69cfd86f770a21d48e19b75f5b09\", \n        \"D\": \"a5764d68665120e6878727f4135dd8db97b7ccc0ec6b0c116d713bf4b6b0fee7\"\n      }], \n    \"pseudoOuts\": [ \"340f4ca857b99a6def6d6bbd63ad67fa4994137d757c6e42dd32689d9e828d95\"]\n  }\n}"
	var rawTx RawTx
	err := json.Unmarshal([]byte(txJson), &rawTx)
	c.Assert(err, IsNil)
	rawTx.Hash = "a6413fe6e76ea529351dbd8bdf0ebc38344a43a14a4973fa1d11b048ad9fd330"
	rawTx.Block_Height = 72

	// set the tx pubkey
	var txPubKey [32]byte
	asByte, err := hex.DecodeString("c105d0cd55cd52f6c2648aadb5ac7bbcfc963158bcc443a192035306d7aaafe6")
	c.Assert(err, IsNil)
	copy(txPubKey[:], asByte[0:32])

	out, err := s.client.getOutput(&rawTx, &txPubKey)
	c.Assert(err, IsNil)
	c.Assert(out.Address, Equals, "hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg")
	c.Assert(out.Amount, Equals, uint64(12000000000000))
	c.Assert(out.Asset, Equals, "XHV")
}

func (s *HavenSuite) TestProcessReOrg(c *C) {
	// can't get previous block meta should not error
	var result Block
	blockContent, err := ioutil.ReadFile("../../../../test/fixtures/xhv/get_block.json")
	c.Assert(err, IsNil)
	c.Assert(json.Unmarshal(blockContent, &result), IsNil)
	// should not trigger re-org process
	reorgedItems, err := s.client.processReorg(result)
	c.Assert(err, IsNil)
	c.Assert(reorgedItems, IsNil)

	// add one UTXO which will trigger the re-org process next
	previousHeight := result.Block_Header.Height - 1
	blockMeta := NewBlockMeta(ttypes.GetRandomTxHash().String(), previousHeight, ttypes.GetRandomTxHash().String())
	hash := "27de3e1865c098cd4fded71bae1e8236fd27ce5dce6e524a9ac5cd1a17b5c241"
	blockMeta.AddCustomerTransaction(hash)
	c.Assert(s.client.blockMetaAccessor.SaveBlockMeta(previousHeight, blockMeta), IsNil)
	s.client.globalErrataQueue = make(chan types.ErrataBlock, 1)
	reorgedItems, err = s.client.processReorg(result)
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
