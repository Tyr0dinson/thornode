package ethereum

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/magiconair/properties/assert"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestETHPackage(t *testing.T) { TestingT(t) }

func TestUpdateCurrentBlockHeightMonotonic(t *testing.T) {
	scanner := &ETHScanner{}

	scanner.updateCurrentBlockHeight(100)
	scanner.updateCurrentBlockHeight(90)
	if got := scanner.getCurrentBlockHeight(); got != 100 {
		t.Fatalf("expected current block height to remain 100, got %d", got)
	}

	scanner.updateCurrentBlockHeight(110)
	if got := scanner.getCurrentBlockHeight(); got != 110 {
		t.Fatalf("expected current block height to advance to 110, got %d", got)
	}
}

type EthereumSuite struct {
	thordir        string
	thorKeys       *thorclient.Keys
	bridge         thorclient.ThorchainBridge
	m              *metrics.Metrics
	server         *httptest.Server
	mimirOverrides map[string]string
}

var _ = Suite(&EthereumSuite{})

var m *metrics.Metrics

func GetMetricForTest(c *C) *metrics.Metrics {
	if m == nil {
		var err error
		m, err = metrics.NewMetrics(config.BifrostMetricsConfiguration{
			Enabled:      false,
			ListenPort:   9000,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
			Chains:       common.Chains{common.ETHChain},
		})
		c.Assert(m, NotNil)
		c.Assert(err, IsNil)
	}
	return m
}

func (s *EthereumSuite) SetUpTest(c *C) {
	s.mimirOverrides = nil
	s.m = GetMetricForTest(c)
	c.Assert(s.m, NotNil)
	setupMocknetThorConfig()
	c.Assert(os.Setenv("NET", "mocknet"), IsNil)

	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch req.RequestURI {
		case thorclient.ChainVersionEndpoint:
			_, err := rw.Write([]byte(`{"current":"` + types2.GetCurrentVersion().String() + `"}`))
			c.Assert(err, IsNil)
		case thorclient.PubKeysEndpoint:
			priKey, _ := s.thorKeys.GetPrivateKey()
			tm, _ := cryptocodec.ToCmtPubKeyInterface(priKey.PubKey())
			pk, err := common.NewPubKeyFromCrypto(tm)
			c.Assert(err, IsNil)
			content, err := os.ReadFile("../../../../test/fixtures/endpoints/vaults/pubKeys.json")
			c.Assert(err, IsNil)
			var pubKeysVault openapi.VaultPubkeysResponse
			c.Assert(json.Unmarshal(content, &pubKeysVault), IsNil)
			chain := common.ETHChain.String()
			router := "0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44"
			pubKeysVault.Asgard = append(pubKeysVault.Asgard, openapi.VaultInfo{
				PubKey: pk.String(),
				Routers: []openapi.VaultRouter{
					{
						Chain:  &chain,
						Router: &router,
					},
				},
			})
			buf, err := json.MarshalIndent(pubKeysVault, "", "	")
			c.Assert(err, IsNil)
			_, err = rw.Write(buf)
			c.Assert(err, IsNil)
		case thorclient.InboundAddressesEndpoint:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/inbound_addresses/inbound_addresses.json")
		case thorclient.AsgardVault:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/vaults/asgard.json")
		case thorclient.LastBlockEndpoint:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/lastblock/root.json")
		case thorclient.NodeAccountEndpoint:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/nodeaccount/template.json")
		case "/status":
			rw.Header().Set("Content-Type", "application/json")
			_, err := rw.Write([]byte(`{"result":{"node_info":{"network":"mocknet"}}}`))
			c.Assert(err, IsNil)
		default:
			// Handle mimir and status endpoints (including double-slash normalization)
			requestPath := req.RequestURI
			requestPath = strings.Replace(requestPath, "//thorchain/mimir/key/", "/thorchain/mimir/key/", 1)
			if strings.Contains(requestPath, "//status") {
				requestPath = "/status"
			}

			if strings.HasPrefix(requestPath, "/thorchain/mimir/key/") {
				mimirKey := strings.TrimPrefix(requestPath, "/thorchain/mimir/key/")
				rw.Header().Set("Content-Type", "application/json")
				if s.mimirOverrides != nil {
					if value, exists := s.mimirOverrides[mimirKey]; exists {
						_, err := rw.Write([]byte(value))
						c.Assert(err, IsNil)
						return
					}
				}
				_, err := rw.Write([]byte(`-1`))
				c.Assert(err, IsNil)
				return
			}

			if requestPath == "/status" {
				rw.Header().Set("Content-Type", "application/json")
				_, err := rw.Write([]byte(`{"result":{"node_info":{"network":"mocknet"}}}`))
				c.Assert(err, IsNil)
				return
			}

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
			if rpcRequest.Method == "eth_getBalance" {
				var params []interface{}
				err = json.Unmarshal(rpcRequest.Params, &params)
				c.Assert(err, IsNil)
				// check if it is asking for pending balance
				if len(params) > 1 {
					if val, ok := params[1].(string); ok && val == "pending" {
						// Return 5 ETH for pending requests
						// standard tests (send 1-2 ETH) will pass.
						// regression test (send 6 ETH) will fail (correctly).
						_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x4563918244F40000"}`))
						c.Assert(err, IsNil)
						return
					}
				}
				_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x8ac7230489e80000"}`))
				c.Assert(err, IsNil)
			}
			if rpcRequest.Method == "eth_getTransactionByHash" {
				_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
				c.Assert(err, IsNil)
			}
			if rpcRequest.Method == "eth_getTransactionCount" {
				_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x0"}`))
				c.Assert(err, IsNil)
			}
			if rpcRequest.Method == "eth_chainId" {
				_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0xf"}`))
				c.Assert(err, IsNil)
			}
			if rpcRequest.Method == "eth_gasPrice" {
				_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
				c.Assert(err, IsNil)
			}
			if rpcRequest.Method == "eth_estimateGas" {
				_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x493df"}`))
				c.Assert(err, IsNil)
			}
			if rpcRequest.Method == "eth_getBlockByNumber" {
				_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"difficulty":"0x2","extraData":"0xd88301090e846765746888676f312e31342e32856c696e757800000000000000ef855333e6b03b825c2f1381f111e278232688e21ba8c36aa35689505d9470704420825b302cd70cc6610f1334a3d7c801ac4b8871bd9f0692c1c96f0f60ee0f01","gasLimit":"0x7a1200","gasUsed":"0xfbc9","hash":"0x45f139a64f563e12f61824a4b44edc2c955818d176b160538ae24f566a006c00","logsBloom":"0x00000000000000000002000000000000000000100000000000000000000000000000000000000000000000000000000000000800000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000400000000000800000000080000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001000000000000004000000000000000000000000000000000400000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000","miner":"0x0000000000000000000000000000000000000000","mixHash":"0x0000000000000000000000000000000000000000000000000000000000000000","nonce":"0x0000000000000000","number":"0x7","parentHash":"0x2f202f8aa7355e77bfbdcd63c08f7c4e43e0bcca61b45fe6a2bdb950d777fa38","receiptsRoot":"0xe1cf0352843e29447633b9f1710e443f2582691e4278febf322c0bb7f86202cc","sha3Uncles":"0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347","size":"0x38c","stateRoot":"0x303f9a24ba76fa8f350d36f4cef139e6be023f95646e2602cf9e6f939f91beea","timestamp":"0x5fde861b","totalDifficulty":"0xf","transactions":[{"blockHash":"0x45f139a64f563e12f61824a4b44edc2c955818d176b160538ae24f566a006c00","blockNumber":"0x7","from":"0xfb337706200a55009e6bbd41e4dc164d59bc9aa2","gas":"0x17cdc","gasPrice":"0x1","hash":"0x042602a2dff77111f3e711ab7c81adbcbc9a2d87973f4afb8dc0f2856021ec74","input":"0x31a053cf000000000000000000000000fd5111db462a68cfd6df19fb110dc8e9116a90e9000000000000000000000000000000000000000000000000000000000000004000000000000000000000000000000000000000000000000000000000000000444f55543a3841313034343144354241424535443444434138443531324646363236313039394135343741393739394536334337323238384530453742303534313444433200000000000000000000000000000000000000000000000000000000","nonce":"0x0","to":"0xe65e9d372f8cacc7b6dfcd4af6507851ed31bb44","transactionIndex":"0x0","value":"0xd6d8","v":"0x41","r":"0xbce697be8572d1543cd8c191c409cee2b4999a538e707286b5e14f7e8ff442b8","s":"0x4b8f8e8a14fb60dbe981f6ddbb31300bbc2ce8753ad6b82bdce8147280cd8e43"}],"transactionsRoot":"0xd42e9b932bffb89da313a7f9370d83c2fb4082a2d8ff162b70dcb36330a476db","uncles":[]}}`))
				c.Assert(err, IsNil)
			}
			if rpcRequest.Method == "eth_sendRawTransaction" {
				_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x88df016429689c079f3b2f6ad39fa052532c56795b733da78a91ebe6a713944b"}`))
				c.Assert(err, IsNil)
			}
			if rpcRequest.Method == "eth_getTransactionReceipt" {
				_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
				"transactionHash":"0x88df016429689c079f3b2f6ad39fa052532c56795b733da78a91ebe6a713944b",
				"transactionIndex":"0x0",
				"blockNumber":"0x1",
				"blockHash":"0x78bfef68fccd4507f9f4804ba5c65eb2f928ea45b3383ade88aaa720f1209cba",
				"cumulativeGasUsed":"0xc350",
				"contractAddress":"0x2a65aca4d5fc5b5c859090a6c34d164135398226",
				"gasUsed":"0x4dc",
				"effectiveGasPrice":"0x2540be400",
				"logsBloom":"0x0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
				"logs":[],
				"status":"0x1"
			}}`))
				c.Assert(err, IsNil)
			}
			if rpcRequest.Method == "eth_blockNumber" {
				_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x7"}`))
				c.Assert(err, IsNil)
			}
			if rpcRequest.Method == "eth_getBlockByNumber" {
				_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
				"difficulty": "0x31962a3fc82b",
				"extraData": "0x4477617266506f6f6c",
				"gasLimit": "0x47c3d8",
				"gasUsed": "0x0",
				"hash": "0x78bfef68fccd4507f9f4804ba5c65eb2f928ea45b3383ade88aaa720f1209cba",
				"logsBloom": "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
				"miner": "0x2a65aca4d5fc5b5c859090a6c34d164135398226",
				"nonce": "0xa5e8fb780cc2cd5e",
				"number": "0x1",
				"parentHash": "0x8b535592eb3192017a527bbf8e3596da86b3abea51d6257898b2ced9d3a83826",
				"receiptsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
				"sha3Uncles": "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
				"size": "0x20e",
				"stateRoot": "0xdc6ed0a382e50edfedb6bd296892690eb97eb3fc88fd55088d5ea753c48253dc",
				"timestamp": "0x579f4981",
				"totalDifficulty": "0x25cff06a0d96f4bee",
				"transactions": [],
				"transactionsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
				"uncles": [
		]}}`))
				c.Assert(err, IsNil)
			}
			if rpcRequest.Method == "eth_call" {
				// Handle allowance() calls - return low allowance that needs approval
				switch {
				case strings.Contains(string(rpcRequest.Params), "0xdd62ed3e"):
					_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x00000000000000000000000000000000000000000000000000000000000003e8"}`)) // 1000 in hex
					c.Assert(err, IsNil)
				case string(rpcRequest.Params) == `[{"from":"0x9f4aab49a9cd8fc54dcb3701846f608a6f2c44da","input":"0x03b6a6730000000000000000000000009f4aab49a9cd8fc54dcb3701846f608a6f2c44da0000000000000000000000003b7fa4dd21c6f9ba3ca375217ead7cab9d6bf483","to":"0xe65e9d372f8cacc7b6dfcd4af6507851ed31bb44"},"latest"]`:
					_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":5,"result":"0x0000000000000000000000000000000000000000000000000000000000000012"}`))
					c.Assert(err, IsNil)
				case string(rpcRequest.Params) == `[{"data":"0x95d89b41","from":"0x0000000000000000000000000000000000000000","to":"0x3b7fa4dd21c6f9ba3ca375217ead7cab9d6bf483"},"latest"]`:
					_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x00000000000000000000000000000000000000000000000000000000000000200000000000000000000000000000000000000000000000000000000000000003544b4e0000000000000000000000000000000000000000000000000000000000"}`))
					c.Assert(err, IsNil)
				default:
					// Default response for other eth_call (e.g., decimals)
					_, err = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x0000000000000000000000000000000000000000000000000000000000000012"}`))
					c.Assert(err, IsNil)
				}
			}
		}
	}))
	s.server = server
	cfg := config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       server.Listener.Addr().String(),
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: s.thordir,
	}

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := cKeys.NewInMemory(cdc)
	_, _, err := kb.NewMnemonic(cfg.SignerName, cKeys.English, cmd.THORChainHDPath, cfg.SignerPasswd, hd.Secp256k1)
	c.Assert(err, IsNil)
	s.thorKeys = thorclient.NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd)
	s.bridge, err = thorclient.NewThorchainBridge(cfg, s.m, s.thorKeys)
	c.Assert(err, IsNil)
}

func (s *EthereumSuite) TearDownTest(c *C) {
	if s.server != nil {
		s.server.Close()
		s.server = nil
	}
	c.Assert(os.Unsetenv("NET"), IsNil)

	if err := os.RemoveAll(s.thordir); err != nil {
		c.Error(err)
	}
}

// TestGetGasEstimateForSolvency verifies the ETH solvency buffer formula:
// 3 * MaxGasLimit * lastReportedGasPrice, converted from gwei to THORChain 1e8 units.
func (s *EthereumSuite) TestGetGasEstimateForSolvency(c *C) {
	client := &Client{
		cfg: config.BifrostChainConfiguration{
			ChainID: common.ETHChain,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				MaxGasLimit: 21_000,
			},
		},
		ethScanner: &ETHScanner{},
	}
	client.ethScanner.lastReportedGasPrice.Store(40)

	got := client.GetGasEstimateForSolvency()
	c.Assert(got.Equal(cosmos.NewUint(252_000)), Equals, true)
}

func (s *EthereumSuite) TestNewClient(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	poolMgr := thorclient.NewPoolMgr(s.bridge)

	// bridge is nil
	e, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{}, nil, nil, s.m, pubkeyMgr, poolMgr)
	c.Assert(e, IsNil)
	c.Assert(err, NotNil)

	// pubkey manager is nil
	e, err = NewClient(s.thorKeys, config.BifrostChainConfiguration{}, nil, s.bridge, s.m, nil, poolMgr)
	c.Assert(e, IsNil)
	c.Assert(err, NotNil)

	// pubkey manager is nil
	e, err = NewClient(s.thorKeys, config.BifrostChainConfiguration{}, nil, s.bridge, s.m, pubkeyMgr, nil)
	c.Assert(e, IsNil)
	c.Assert(err, NotNil)
	// pubkey manager is nil
	e, err = NewClient(nil, config.BifrostChainConfiguration{}, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(e, IsNil)
	c.Assert(err, NotNil)

	divergent := config.BifrostChainConfiguration{}
	divergent.EVM.TokenMaxGasMultiplier = constants.EVMTokenMaxGasMultiplier + 1
	e, err = NewClient(s.thorKeys, divergent, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(e, IsNil)
	c.Assert(err, ErrorMatches, ".*diverges from Thornode protocol value.*")
}

func (s *EthereumSuite) TestConvertSigningAmount(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	poolMgr := thorclient.NewPoolMgr(s.bridge)
	e, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{
		RPCHost: "http://" + s.server.Listener.Addr().String(),
		BlockScanner: config.BifrostBlockScannerConfiguration{
			StartBlockHeight:   1, // avoids querying thorchain for block height
			HTTPRequestTimeout: time.Second,
		},
	}, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(err, IsNil)
	c.Assert(e, NotNil)
	e.ethScanner.globalNetworkFeeQueue = make(chan common.NetworkFee, 1)
	c.Assert(e.ethScanner.tokens.SaveTokenMeta("TKN", "0x3b7FA4dd21c6f9BA3ca375217EAD7CAb9D6bF483", 18), IsNil)
	c.Assert(e.ethScanner.tokens.SaveTokenMeta("TKX", "0x3b7FA4dd21c6f9BA3ca375217EAD7CAb9D6bF482", 8), IsNil)
	result := e.convertSigningAmount(big.NewInt(100), "0x3b7FA4dd21c6f9BA3ca375217EAD7CAb9D6bF483")
	c.Assert(result.Uint64(), Equals, uint64(100*common.One*100))
	result = e.convertSigningAmount(big.NewInt(100000000), "0x3b7FA4dd21c6f9BA3ca375217EAD7CAb9D6bF482")
	c.Assert(result.Uint64(), Equals, uint64(100000000))
}

func TestGetTokenAddressFromAsset(t *testing.T) {
	token := getTokenAddressFromAsset(common.ETHAsset)
	assert.Equal(t, token, ethToken)
	a, err := common.NewAsset("ETH.TKN-0x3b7FA4dd21c6f9BA3ca375217EAD7CAb9D6bF483")
	assert.Equal(t, err, nil)
	token = getTokenAddressFromAsset(a)
	assert.Equal(t, token, "0X3B7FA4DD21C6F9BA3CA375217EAD7CAB9D6BF483")
}

func (s *EthereumSuite) TestClient(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	poolMgr := thorclient.NewPoolMgr(s.bridge)
	e, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{}, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(e, IsNil)
	c.Assert(err, NotNil)
	e2, err2 := NewClient(s.thorKeys, config.BifrostChainConfiguration{
		RPCHost: "http://" + s.server.Listener.Addr().String(),
		BlockScanner: config.BifrostBlockScannerConfiguration{
			StartBlockHeight:   1, // avoids querying thorchain for block height
			HTTPRequestTimeout: time.Second,
		},
		ChainID: common.ETHChain, // Important for determining gas rate units.
	}, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(err2, IsNil)
	c.Assert(e2, NotNil)
	e2.ethScanner.globalNetworkFeeQueue = make(chan common.NetworkFee, 1)
	e2.signingReady.Store(true) // Mark ready for tests that don't call Start()
	c.Assert(pubkeyMgr.Start(), IsNil)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	c.Check(e2.GetChain(), Equals, common.ETHChain)
	height, err := e2.GetHeight()
	c.Assert(err, IsNil)
	c.Check(height, Equals, int64(7))
	gasPrice := e2.GetGasPrice()
	c.Check(gasPrice.Uint64(), Equals, uint64(initialGasPrice))

	acct, err := e2.GetAccount(types2.GetRandomPubKey(), nil)
	c.Assert(err, IsNil)
	c.Check(acct.Sequence, Equals, int64(0))
	c.Check(acct.Coins[0].Amount.Uint64(), Equals, uint64(10*common.One))
	pk := types2.GetRandomPubKey()
	addr := e2.GetAddress(pk)
	c.Check(len(addr), Equals, 42)
	_, err = e2.BroadcastTx(stypes.TxOutItem{}, []byte(`{
		"from":"0xa7d9ddbe1f17865597fbd27ec712455208b6b76d",
		"gas":"0xc350",
		"gasPrice":"0x4a817c800",
		"input":"0x68656c6c6f21",
		"nonce":"0x15",
		"to":"0xf02c1c8e6114b1dbe8937a39260b5b0a374432bb",
		"transactionIndex":"0x41",
		"value":"0xf3dbb76162000",
		"v":"0x25",
		"r":"0x1b5e176d927f8e9ab405058b2d2457392da3e20f328b16ddabcebc33eaac5fea",
		"s":"0x4ba69724e8f69de52f0125ad8b3c5c2cef33019bac3249e2c0a2192766d1721c"
	}`))
	c.Assert(err, IsNil)
	input := []byte(`{
    "height": 1,
    "tx_array": [
        {
            "chain": "ETH",
			"from_address":"0xa7d9ddbe1f17865597fbd27ec712455208b6b76d",
            "to_address": "0xde0b295669a9fd93d5f28d9ec85e40f4cb697bae",
            "coin": {
                "asset": "ETH.ETH",
                "amount": "194765912"
            },
            "max_gas": [
                {
                    "asset": "ETH.ETH",
                    "amount": "600000"
                }
            ],
			"gas_rate":10
        }
    ]
}`) // gas_rate is 10 gwei
	var txOut stypes.TxOut
	err = json.Unmarshal(input, &txOut)
	c.Assert(err, IsNil)

	txOut.TxArray[0].VaultPubKey = e2.kw.GetPubKey()
	c.Logf(txOut.TxArray[0].VaultPubKey.String())
	c.Logf(e2.kw.GetPubKey().String())
	out := txOut.TxArray[0].TxOutItem(txOut.Height)
	out.Chain = common.ETHChain
	out.Memo = "OUT:B6BD1A69831B9CCC0A1E9939E9AFBFCA144C427B3F61E176EBDCB14E57981C1B"
	r, _, obs, err := e2.SignTx([]stypes.TxOutItem{out}, 1)
	c.Assert(err, IsNil)
	c.Assert(r, NotNil)
	c.Assert(obs, NotNil)
	fromAddr, err := out.VaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	for _, ob := range obs {
		c.Assert(ob.Sender, Equals, fromAddr.String())
	}
	_, err = e2.BroadcastTx(out, r)
	c.Assert(err, IsNil)
}

func (s *EthereumSuite) TestGetAccount(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	poolMgr := thorclient.NewPoolMgr(s.bridge)
	e, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{
		RPCHost: "http://" + s.server.Listener.Addr().String(),
		BlockScanner: config.BifrostBlockScannerConfiguration{
			StartBlockHeight:   1, // avoids querying thorchain for block height
			HTTPRequestTimeout: time.Second,
		},
	}, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(err, IsNil)
	c.Assert(e, NotNil)
	e.ethScanner.globalNetworkFeeQueue = make(chan common.NetworkFee, 1)
	c.Assert(pubkeyMgr.Start(), IsNil)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	acct, err := e.GetAccountByAddress("0x9f4aab49a9cd8fc54dcb3701846f608a6f2c44da", nil)
	c.Assert(err, IsNil)
	c.Assert(acct.Sequence, Equals, int64(0))
	b, err := e.GetBalance("0x9f4aab49a9cd8fc54dcb3701846f608a6f2c44da", "0x3b7FA4dd21c6f9BA3ca375217EAD7CAb9D6bF483", nil, "0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44")
	c.Assert(err, IsNil)
	c.Assert(b, NotNil)
}

func (s *EthereumSuite) TestGetVaultBalancesUsesVaultCoins(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	poolMgr := thorclient.NewPoolMgr(s.bridge)
	e, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{
		RPCHost: "http://" + s.server.Listener.Addr().String(),
		BlockScanner: config.BifrostBlockScannerConfiguration{
			StartBlockHeight:   1,
			HTTPRequestTimeout: time.Second,
		},
		ChainID: common.ETHChain,
	}, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(err, IsNil)
	c.Assert(e, NotNil)
	e.ethScanner.globalNetworkFeeQueue = make(chan common.NetworkFee, 1)

	// Node-local token metadata must not affect the solvency coin set.
	c.Assert(e.ethScanner.tokens.SaveTokenMeta("RAZE", "0x5EAA69B29F99C84FE5DE8200340B4E9B4AB38EAC", 18), IsNil)
	c.Assert(e.ethScanner.tokens.SaveTokenMeta("FLIP", "0x826180541412D574CF1336D22C0C0A287822678A", 18), IsNil)

	tkn, err := common.NewAsset("ETH.TKN-0X3B7FA4DD21C6F9BA3CA375217EAD7CAB9D6BF483")
	c.Assert(err, IsNil)
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	vault := types2.Vault{
		Coins: common.NewCoins(
			common.NewCoin(usdc, cosmos.NewUint(1)),
			common.NewCoin(common.BTCAsset, cosmos.NewUint(1)),
			common.NewCoin(common.ETHAsset, cosmos.NewUint(1)),
			common.NewCoin(tkn, cosmos.NewUint(1)),
			common.NewCoin(usdc, cosmos.NewUint(2)),
		),
	}

	coins, err := e.GetVaultBalances(
		"0x9f4aab49a9cd8fc54dcb3701846f608a6f2c44da",
		nil,
		"0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44",
		vault,
	)
	c.Assert(err, IsNil)
	c.Assert(coins, HasLen, 3)
	c.Assert(coins[0].Asset.Equals(common.ETHAsset), Equals, true)
	c.Assert(coins[0].Amount.Uint64(), Equals, uint64(10*common.One))
	c.Assert(coins[1].Asset.Equals(tkn), Equals, true)
	c.Assert(coins[1].Amount.IsZero(), Equals, true)
	c.Assert(coins[2].Asset.Equals(usdc), Equals, true)
	c.Assert(coins[2].Amount.IsZero(), Equals, true)
}

func (s *EthereumSuite) TestSignETHTx(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	poolMgr := thorclient.NewPoolMgr(s.bridge)
	chainConfig := config.BifrostChainConfiguration{
		RPCHost: "http://" + s.server.Listener.Addr().String(),
		BlockScanner: config.BifrostBlockScannerConfiguration{
			StartBlockHeight:   1, // avoids querying thorchain for block height
			HTTPRequestTimeout: time.Second,
			MaxGasLimit:        80000,
		},
		ChainID: common.ETHChain, // Important for determining gas rate units.
	}
	chainConfig.EVM.AggregatorMaxGasMultiplier = 10
	chainConfig.EVM.TokenMaxGasMultiplier = 3
	e, err := NewClient(s.thorKeys, chainConfig, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(err, IsNil)
	c.Assert(e, NotNil)
	e.ethScanner.globalNetworkFeeQueue = make(chan common.NetworkFee, 1)
	e.signingReady.Store(true) // Mark ready for tests that don't call Start()
	c.Assert(pubkeyMgr.Start(), IsNil)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	pubkeys := pubkeyMgr.GetAlgoPubKeys(common.SigningAlgoSecp256k1, true)
	addr, err := pubkeys[len(pubkeys)-1].GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	// Not ETH chain
	result, _, obs, err := e.SignTx([]stypes.TxOutItem{
		{
			Chain:       common.BTCChain,
			ToAddress:   addr,
			VaultPubKey: "",
		},
	}, 1)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(obs, IsNil)

	// to address is empty
	result, _, obs, err = e.SignTx([]stypes.TxOutItem{
		{
			Chain:       common.ETHChain,
			VaultPubKey: "",
		},
	}, 1)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(obs, IsNil)

	// vault pub key is empty
	result, _, obs, err = e.SignTx([]stypes.TxOutItem{
		{
			Chain:       common.ETHChain,
			ToAddress:   addr,
			VaultPubKey: "",
		},
	}, 1)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(obs, IsNil)

	// memo is empty
	result, _, obs, err = e.SignTx([]stypes.TxOutItem{
		{
			Chain:       common.ETHChain,
			ToAddress:   addr,
			VaultPubKey: e.localPubKey,
		},
	}, 1)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(obs, IsNil)

	// memo can't be parsed
	result, _, obs, err = e.SignTx([]stypes.TxOutItem{
		{
			Chain:       common.ETHChain,
			ToAddress:   addr,
			VaultPubKey: e.localPubKey,
			Memo:        "whatever",
		},
	}, 1)

	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(obs, IsNil)

	// memo is inbound
	result, _, obs, err = e.SignTx([]stypes.TxOutItem{
		{
			Chain:       common.ETHChain,
			ToAddress:   addr,
			VaultPubKey: e.localPubKey,
			Memo:        "swap:ETH.ETH",
		},
	}, 1)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(obs, IsNil)

	// Outbound
	result, _, obs, err = e.SignTx([]stypes.TxOutItem{
		{
			Chain:       common.ETHChain,
			ToAddress:   addr,
			VaultPubKey: e.localPubKey,
			Coins: common.Coins{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
			},
			MaxGas: common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(e.cfg.BlockScanner.MaxGasLimit*8)),
			},
			GasRate: 10, // 10 gwei
			Memo:    "OUT:4D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
		},
	}, 1)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	c.Assert(obs, NotNil)
	fromAddr, err := e.localPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	for _, ob := range obs {
		c.Assert(ob.Sender, Equals, fromAddr.String())
	}

	asset, err := common.NewAsset("ETH.TKN-0X3B7FA4DD21C6F9BA3CA375217EAD7CAB9D6BF483")
	c.Assert(err, IsNil)

	// Outbound
	result, _, obs, err = e.SignTx([]stypes.TxOutItem{
		{
			Chain:       common.ETHChain,
			ToAddress:   addr,
			VaultPubKey: e.localPubKey,
			Coins: common.Coins{
				common.NewCoin(asset, cosmos.NewUint(1e18)),
			},
			MaxGas: common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(e.cfg.BlockScanner.MaxGasLimit*8)),
			},
			GasRate: 1,
			Memo:    "OUT:4D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
		},
	}, 1)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	c.Assert(obs, NotNil)
	fromAddr, err = e.localPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	for _, ob := range obs {
		c.Assert(ob.Sender, Equals, fromAddr.String())
	}

	// refund
	result, _, obs, err = e.SignTx([]stypes.TxOutItem{
		{
			Chain:       common.ETHChain,
			ToAddress:   addr,
			VaultPubKey: e.localPubKey,
			Coins: common.Coins{
				common.NewCoin(asset, cosmos.NewUint(common.One)),
			},
			MaxGas: common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(e.cfg.BlockScanner.MaxGasLimit*8)),
			},
			GasRate: 1,
			Memo:    "REFUND:4D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
		},
	}, 1)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	c.Assert(obs, NotNil)
	fromAddr, err = e.localPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	for _, ob := range obs {
		c.Assert(ob.Sender, Equals, fromAddr.String())
	}

	// refund
	result, _, obs, err = e.SignTx([]stypes.TxOutItem{
		{
			Chain:       common.ETHChain,
			ToAddress:   addr,
			VaultPubKey: e.localPubKey,
			Coins: common.Coins{
				common.NewCoin(asset, cosmos.NewUint(common.One)),
			},
			MaxGas: common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(e.cfg.BlockScanner.MaxGasLimit*8)),
			},
			GasRate: 1,
			Memo:    "OUT:4D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
		},
	}, 1)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	c.Assert(obs, NotNil)
	fromAddr, err = e.localPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	for _, ob := range obs {
		c.Assert(ob.Sender, Equals, fromAddr.String())
	}

	// migrate
	result, _, obs, err = e.SignTx([]stypes.TxOutItem{
		{
			Chain:       common.ETHChain,
			ToAddress:   addr,
			VaultPubKey: e.localPubKey,
			Coins: common.Coins{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
			},
			MaxGas: common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(e.cfg.BlockScanner.MaxGasLimit*8)),
			},
			GasRate: 1,
			Memo:    "MIGRATE:1024",
		},
	}, 1)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	c.Assert(obs, NotNil)
	fromAddr, err = e.localPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	for _, ob := range obs {
		c.Assert(ob.Sender, Equals, fromAddr.String())
	}

	// migrate
	result, _, obs, err = e.SignTx([]stypes.TxOutItem{
		{
			Chain:       common.ETHChain,
			ToAddress:   addr,
			VaultPubKey: e.localPubKey,
			Coins: common.Coins{
				common.NewCoin(asset, cosmos.NewUint(common.One)),
			},
			MaxGas: common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(e.cfg.BlockScanner.MaxGasLimit*8)),
			},
			GasRate: 1,
			Memo:    "MIGRATE:1024",
		},
	}, 1)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	c.Assert(obs, NotNil)
	fromAddr, err = e.localPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	for _, ob := range obs {
		c.Assert(ob.Sender, Equals, fromAddr.String())
	}
}

func (s *EthereumSuite) TestGetAsgardAddresses(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	poolMgr := thorclient.NewPoolMgr(s.bridge)
	e, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{
		RPCHost: "http://" + s.server.Listener.Addr().String(),
		BlockScanner: config.BifrostBlockScannerConfiguration{
			StartBlockHeight:   1, // avoids querying thorchain for block height
			HTTPRequestTimeout: time.Second,
		},
	}, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(err, IsNil)
	c.Assert(e, NotNil)
	e.ethScanner.globalNetworkFeeQueue = make(chan common.NetworkFee, 1)
	c.Assert(pubkeyMgr.Start(), IsNil)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	addresses, err := e.getAsgardAddress()
	c.Assert(err, IsNil)
	c.Assert(addresses, NotNil)
}

func (s *EthereumSuite) TestGetConfirmationCount(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	poolMgr := thorclient.NewPoolMgr(s.bridge)
	e, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{
		RPCHost: "http://" + s.server.Listener.Addr().String(),
		BlockScanner: config.BifrostBlockScannerConfiguration{
			StartBlockHeight:   1, // avoids querying thorchain for block height
			HTTPRequestTimeout: time.Second,
		},
	}, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(err, IsNil)
	c.Assert(e, NotNil)
	e.ethScanner.globalNetworkFeeQueue = make(chan common.NetworkFee, 1)
	c.Assert(pubkeyMgr.Start(), IsNil)
	defer func() {
		c.Assert(pubkeyMgr.Stop(), IsNil)
	}()

	asgardAddresses, err := e.getAsgardAddress()
	c.Assert(err, IsNil)
	pubkey := types2.GetRandomPubKey()
	addr, err := pubkey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	c.Assert(e.GetConfirmationCount(stypes.TxIn{}), Equals, int64(0))
	c.Assert(e.GetConfirmationCount(stypes.TxIn{
		Chain: common.ETHChain,
		TxArray: []*stypes.TxInItem{
			{
				BlockHeight:         1,
				Tx:                  "4D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
				Memo:                "Not Sure",
				Sender:              addr.String(),
				To:                  addr.String(),
				ObservedVaultPubKey: pubkey,
			},
		},
		MemPool: true,
	}), Equals, int64(0))

	c.Assert(e.GetConfirmationCount(stypes.TxIn{
		Chain: common.ETHChain,
		TxArray: []*stypes.TxInItem{
			{
				BlockHeight: 1,
				Tx:          "4D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
				Memo:        "Not Sure",
				Sender:      addr.String(),
				To:          asgardAddresses[0].String(),
				Coins: common.Coins{
					common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000)),
				},
				ObservedVaultPubKey: pubkey,
			},
		},
		MemPool: false,
	}), Equals, int64(2))
	c.Assert(e.GetConfirmationCount(stypes.TxIn{
		Chain: common.ETHChain,
		TxArray: []*stypes.TxInItem{
			{
				BlockHeight: 1,
				Tx:          "4D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
				Memo:        "Not Sure",
				Sender:      addr.String(),
				To:          asgardAddresses[0].String(),
				Coins: common.Coins{
					common.NewCoin(common.ETHAsset, cosmos.NewUint(3e8)),
				},
				ObservedVaultPubKey: pubkey,
			},
		},
		MemPool: false,
	}), Equals, int64(2))
}

func (s *EthereumSuite) TestSignTxChecksMemLiability(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	poolMgr := thorclient.NewPoolMgr(s.bridge)
	e, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{
		RPCHost: "http://" + s.server.Listener.Addr().String(),
		BlockScanner: config.BifrostBlockScannerConfiguration{
			StartBlockHeight:   1,
			HTTPRequestTimeout: time.Second,
		},
		ChainID: common.ETHChain,
	}, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(err, IsNil)
	c.Assert(e, NotNil)
	e.ethScanner.globalNetworkFeeQueue = make(chan common.NetworkFee, 1)
	e.signingReady.Store(true) // Mark ready for tests that don't call Start()
	c.Assert(pubkeyMgr.Start(), IsNil)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()

	// Test: In-Memory Liability Tracking
	// The mock for eth_getBalance returns 10 ETH for "latest".
	// First Transaction (5 ETH + ~1.5 ETH fee) should SUCCEED and record liability.
	// Second Transaction (5 ETH + ~1.5 ETH fee) should FAIL because:
	//   EffectiveBalance = LatestBalance(10) - Liability(~6.5) = ~3.5 ETH < Required(~6.5 ETH)

	txOutItem1 := stypes.TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   types2.GetRandomETHAddress(),
		VaultPubKey: e.localPubKey,
		Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One))}, // 5 ETH
		Memo:        "MIGRATE:100",
		GasRate:     5,
		MaxGas:      common.Gas(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)))),
	}

	// First transaction should succeed
	rawTx1, _, _, err := e.SignTx([]stypes.TxOutItem{txOutItem1}, 100)
	c.Assert(err, IsNil, Commentf("First SignTx should succeed"))
	c.Assert(rawTx1, NotNil)

	// Verify liability was recorded
	c.Assert(len(e.pendingLiabilities[e.kw.GetPubKey().String()]) > 0 || len(e.pendingLiabilities) > 0, Equals, true, Commentf("Liability should be recorded"))

	// Second transaction should fail due to accumulated liability
	txOutItem2 := stypes.TxOutItem{
		Checkpoint:  []byte("1"),
		Chain:       common.ETHChain,
		ToAddress:   types2.GetRandomETHAddress(),
		VaultPubKey: e.localPubKey,
		Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One))}, // 5 ETH
		Memo:        "MIGRATE:101",
		GasRate:     5,
		MaxGas:      common.Gas(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)))),
	}

	rawTx2, _, _, err := e.SignTx([]stypes.TxOutItem{txOutItem2}, 101)
	c.Assert(err, NotNil, Commentf("Second SignTx should fail due to liability"))
	c.Assert(err.Error(), Matches, "insufficient gas asset balance.*")
	c.Assert(rawTx2, IsNil)
}

func (s *EthereumSuite) TestCheckAndApproveAllowance(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	c.Assert(pubkeyMgr.Start(), IsNil)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	poolMgr := thorclient.NewPoolMgr(s.bridge)

	chainConfig := config.BifrostChainConfiguration{
		ChainID: common.ETHChain,
		RPCHost: "http://" + s.server.Listener.Addr().String(),
		BlockScanner: config.BifrostBlockScannerConfiguration{
			StartBlockHeight:   1, // avoids querying thorchain for block height
			HTTPRequestTimeout: time.Second,
			MaxGasLimit:        80000,
		},
	}

	e, err := NewClient(s.thorKeys, chainConfig, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(err, IsNil)
	c.Assert(e, NotNil)

	tokenAsset, err := common.NewAsset("ETH.USDC-0xA0b86a33E6441e60DDB6B6B84b7efF8e6f3C5d0C")
	c.Assert(err, IsNil)

	txOutItem := stypes.TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   common.Address("0x1234567890123456789012345678901234567890"),
		VaultPubKey: e.localPubKey,
		Coins: common.Coins{
			common.NewCoin(tokenAsset, cosmos.NewUint(1000000)),
		},
		Memo:    "OUT:1234",
		GasRate: 50,
	}

	// Test 1: Mimir disabled (default -1) → Skip approval
	approvalTx, err := e.checkAndApproveAllowance(txOutItem, 100)
	c.Assert(err, IsNil)
	c.Assert(approvalTx, IsNil)

	// Enable mimir for remaining tests to verify the coin-type skip paths
	s.mimirOverrides = map[string]string{
		"EVMAllowanceCheck-ETH": "1",
	}
	// Sleep for the bridge HTTP cache TTL (ThorchainBlockTime) so the new
	// mimir value is picked up on the next query.
	time.Sleep(constants.ThorchainBlockTime)

	// Test 2: Native asset → Skip approval (even with mimir enabled)
	txOutItemNative := txOutItem
	txOutItemNative.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000)),
	}
	approvalTx, err = e.checkAndApproveAllowance(txOutItemNative, 100)
	c.Assert(err, IsNil)
	c.Assert(approvalTx, IsNil)

	// Test 3: Multiple coins → Error
	txOutItemMultiple := txOutItem
	txOutItemMultiple.Coins = common.Coins{
		common.NewCoin(tokenAsset, cosmos.NewUint(1000000)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000)),
	}
	approvalTx, err = e.checkAndApproveAllowance(txOutItemMultiple, 100)
	c.Assert(err, NotNil)
	c.Assert(approvalTx, IsNil)

	// Test 4: Empty coins array → Skip approval
	txOutItemEmpty := txOutItem
	txOutItemEmpty.Coins = common.Coins{}
	approvalTx, err = e.checkAndApproveAllowance(txOutItemEmpty, 100)
	c.Assert(err, IsNil)
	c.Assert(approvalTx, IsNil)

	// Test 5: Mimir enabled with insufficient allowance → Approval tx created
	// Mock returns allowance of 1000 (insufficient for 10 USDC)
	txOutItem.Coins = common.Coins{
		common.NewCoin(tokenAsset, cosmos.NewUint(1000000000)), // 10 USDC
	}
	approvalTx, err = e.checkAndApproveAllowance(txOutItem, 100)
	c.Assert(err, IsNil)
	c.Assert(approvalTx, NotNil) // Approval tx created since allowance is insufficient
}

func (s *EthereumSuite) TestSignTxWithAllowanceFlow(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	c.Assert(pubkeyMgr.Start(), IsNil)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	poolMgr := thorclient.NewPoolMgr(s.bridge)

	chainConfig := config.BifrostChainConfiguration{
		ChainID: common.ETHChain,
		RPCHost: "http://" + s.server.Listener.Addr().String(),
		BlockScanner: config.BifrostBlockScannerConfiguration{
			StartBlockHeight:   1, // avoids querying thorchain for block height
			HTTPRequestTimeout: time.Second,
			MaxGasLimit:        80000,
		},
	}
	chainConfig.EVM.TokenMaxGasMultiplier = 3

	e, err := NewClient(s.thorKeys, chainConfig, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(err, IsNil)
	c.Assert(e, NotNil)
	e.signingReady.Store(true)

	pubkeys := pubkeyMgr.GetPubKeys()
	addr, err := pubkeys[len(pubkeys)-1].GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	// Test SignTx with native asset (should work normally)
	result, checkpoint, obs, err := e.SignTx([]stypes.TxOutItem{
		{
			Chain:       common.ETHChain,
			ToAddress:   addr,
			VaultPubKey: e.localPubKey,
			Coins: common.Coins{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
			},
			MaxGas: common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(e.cfg.BlockScanner.MaxGasLimit*8)),
			},
			GasRate: 1,
			Memo:    "OUT:4D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
		},
	}, 1)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	c.Assert(checkpoint, NotNil)
	c.Assert(obs, NotNil)

	// Verify nonce is serialized in checkpoint
	var nonce uint64
	err = json.Unmarshal(checkpoint, &nonce)
	c.Assert(err, IsNil)
	c.Assert(nonce, Equals, uint64(0)) // First transaction from address

	// Test SignTx with token asset (allowance check called but skipped due to disabled mimir)
	tokenAsset, err := common.NewAsset("ETH.USDC-0xA0b86a33E6441e60DDB6B6B84b7efF8e6f3C5d0C")
	c.Assert(err, IsNil)

	result, checkpoint, obs, err = e.SignTx([]stypes.TxOutItem{
		{
			Chain:       common.ETHChain,
			ToAddress:   addr,
			VaultPubKey: e.localPubKey,
			Coins: common.Coins{
				common.NewCoin(tokenAsset, cosmos.NewUint(1000000)), // 1 USDC
			},
			MaxGas: common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(e.cfg.BlockScanner.MaxGasLimit*8)),
			},
			GasRate: 1,
			Memo:    "OUT:4D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
		},
	}, 1)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	c.Assert(checkpoint, NotNil)
	c.Assert(obs, NotNil)

	// Verify nonce is serialized in checkpoint
	err = json.Unmarshal(checkpoint, &nonce)
	c.Assert(err, IsNil)
	c.Assert(nonce, Equals, uint64(0)) // First transaction from address

	// Sleep for bridge cache TTL so the disabled mimir response expires
	time.Sleep(constants.ThorchainBlockTime)

	// Enable the allowance check via mimir
	s.mimirOverrides = map[string]string{
		"EVMAllowanceCheck-ETH": "1",
	}

	// Test SignTx with token asset and enabled mimir (triggers approval + sign flow).
	// After broadcasting the approval, SignTx waits for it to be mined then proceeds
	// to build and sign the outbound.
	result, checkpoint, obs, err = e.SignTx([]stypes.TxOutItem{
		{
			Chain:       common.ETHChain,
			ToAddress:   addr,
			VaultPubKey: e.localPubKey,
			Coins: common.Coins{
				common.NewCoin(tokenAsset, cosmos.NewUint(1000000)), // 1 USDC
			},
			MaxGas: common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(e.cfg.BlockScanner.MaxGasLimit*8)),
			},
			GasRate: 1,
			Memo:    "OUT:4D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
		},
	}, 1)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	c.Assert(checkpoint, NotNil)
	c.Assert(obs, NotNil)

	// Verify nonce in checkpoint (incremented after approval tx)
	err = json.Unmarshal(checkpoint, &nonce)
	c.Assert(err, IsNil)
	c.Assert(nonce, Equals, uint64(1))
}

func (s *EthereumSuite) newApprovalBroadcastFixture(c *C) (*Client, stypes.TxOutItem, func()) {
	s.mimirOverrides = map[string]string{"EVMAllowanceCheck-ETH": "1"}
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	c.Assert(pubkeyMgr.Start(), IsNil)
	cfg := config.BifrostChainConfiguration{
		ChainID: common.ETHChain, RPCHost: s.server.URL,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			StartBlockHeight: 1, MaxGasLimit: 80000, HTTPRequestTimeout: time.Second,
		},
	}
	cfg.EVM.TokenMaxGasMultiplier = 3
	e, err := NewClient(s.thorKeys, cfg, nil, s.bridge, s.m, pubkeyMgr, thorclient.NewPoolMgr(s.bridge))
	c.Assert(err, IsNil)
	e.signingReady.Store(true)
	originalClient := e.client
	pubkeys := pubkeyMgr.GetPubKeys()
	address, err := pubkeys[len(pubkeys)-1].GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	asset, err := common.NewAsset("ETH.TKN-0x17AB05351FC94A1A67BF3F56DDBB941AE6C63E25")
	c.Assert(err, IsNil)
	txOut := stypes.TxOutItem{
		Chain: common.ETHChain, ToAddress: address, VaultPubKey: e.localPubKey,
		Coins:   common.Coins{common.NewCoin(asset, cosmos.NewUint(common.One))},
		MaxGas:  common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(80000*8))},
		GasRate: 1, Memo: "MIGRATE:524",
	}
	return e, txOut, func() {
		originalClient.Close()
		c.Assert(pubkeyMgr.Stop(), IsNil)
	}
}

// Exercise SignTx, not just error classification: peers submitting the same
// mined approval must produce a transfer at nonce 1, never the spent nonce 0.
func (s *EthereumSuite) TestSignTxMinedApprovalNonce(c *C) {
	e, txOut, cleanup := s.newApprovalBroadcastFixture(c)
	defer cleanup()
	var err error
	for _, tc := range []struct {
		name, sendError, receipt string
		success                  bool
	}{
		{"detailed nonce error", "nonce too low: next nonce 1, tx nonce 0", "mined", true},
		{"bare nonce error", "nonce too low", "mined", true},
		{"receipt absent", "nonce too low: next nonce 1, tx nonce 0", "missing", false},
		{"approval reverted", "nonce too low", "reverted", false},
		{"different transaction", "nonce too low", "wrong hash", false},
		{"receipt has no block", "nonce too low", "no block", false},
		{"receipt RPC failed", "nonce too low", "RPC error", false},
		{"unrelated broadcast error", "insufficient funds", "mined", false},
	} {
		// Handler failures are reported back to the test goroutine.
		events := make(chan string, 16)
		var approvalHash ecommon.Hash
		var retrying, nonceRPCError atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, readErr := io.ReadAll(r.Body)
			var req struct {
				ID     json.RawMessage   `json:"id"`
				Method string            `json:"method"`
				Params []json.RawMessage `json:"params"`
			}
			if readErr != nil || json.Unmarshal(body, &req) != nil {
				events <- "bad request"
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			response := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID}
			switch req.Method {
			case "eth_getTransactionCount":
				if !retrying.Load() {
					r.Body = io.NopCloser(bytes.NewReader(body))
					s.server.Config.Handler.ServeHTTP(w, r)
					return
				}
				events <- "checkpoint nonce checked"
				if len(req.Params) != 2 || string(req.Params[1]) != `"latest"` {
					events <- "checkpoint used unconfirmed nonce"
				}
				if nonceRPCError.Load() {
					response["error"] = map[string]interface{}{"code": -32000, "message": "nonce lookup unavailable"}
				} else {
					response["result"] = "0x1"
				}
			case "eth_call":
				var args struct{ Data, Input string }
				if retrying.Load() && len(req.Params) > 0 && json.Unmarshal(req.Params[0], &args) == nil &&
					(strings.HasPrefix(args.Data, "0xdd62ed3e") || strings.HasPrefix(args.Input, "0xdd62ed3e")) {
					// Allowance can become visible while the exact receipt is absent.
					// It must not advance a fixed nonce checkpoint.
					events <- "retry allowance read"
					response["result"] = "0x" + strings.Repeat("f", 64)
				} else {
					r.Body = io.NopCloser(bytes.NewReader(body))
					s.server.Config.Handler.ServeHTTP(w, r)
					return
				}
			case "eth_sendRawTransaction":
				var raw string
				if len(req.Params) != 1 || json.Unmarshal(req.Params[0], &raw) != nil {
					events <- "bad transaction request"
					http.Error(w, "bad transaction", http.StatusBadRequest)
					return
				}
				encoded, decodeErr := hex.DecodeString(strings.TrimPrefix(raw, "0x"))
				var signed etypes.Transaction
				if decodeErr != nil || signed.UnmarshalBinary(encoded) != nil {
					events <- "bad signed transaction"
					http.Error(w, "bad transaction", http.StatusBadRequest)
					return
				}
				if signed.Nonce() != 0 || len(signed.Data()) < 4 || hex.EncodeToString(signed.Data()[:4]) != "095ea7b3" {
					events <- "not the expected approval"
				}
				approvalHash = signed.Hash()
				events <- "approval submitted"
				response["error"] = map[string]interface{}{"code": -32000, "message": tc.sendError}
			case "eth_getTransactionReceipt":
				events <- "receipt requested"
				var hash ecommon.Hash
				if len(req.Params) != 1 || json.Unmarshal(req.Params[0], &hash) != nil || hash != approvalHash {
					events <- "wrong receipt requested"
				}
				receipt := &etypes.Receipt{
					TxHash: approvalHash, BlockNumber: big.NewInt(313),
					Status: etypes.ReceiptStatusSuccessful, Logs: []*etypes.Log{},
				}
				switch tc.receipt {
				case "missing":
					receipt = nil
				case "reverted":
					receipt.Status = etypes.ReceiptStatusFailed
				case "wrong hash":
					receipt.TxHash = ecommon.HexToHash("0x1234")
				case "no block":
					receipt.BlockNumber = nil
				}
				if tc.receipt == "RPC error" {
					response["error"] = map[string]interface{}{"code": -32000, "message": "temporary receipt failure"}
				} else {
					response["result"] = receipt
				}
			default:
				r.Body = io.NopCloser(bytes.NewReader(body))
				s.server.Config.Handler.ServeHTTP(w, r)
				return
			}
			if encodeErr := json.NewEncoder(w).Encode(response); encodeErr != nil {
				events <- "response write failed"
			}
		}))
		e.client, err = ethclient.Dial(server.URL)
		c.Assert(err, IsNil)
		result, checkpoint, observation, signErr := e.SignTx([]stypes.TxOutItem{txOut}, 524)
		var nonce uint64
		c.Assert(json.Unmarshal(checkpoint, &nonce), IsNil, Commentf("%s", tc.name))
		if tc.success {
			c.Assert(signErr, IsNil, Commentf("%s", tc.name))
			c.Assert(nonce, Equals, uint64(1), Commentf("%s", tc.name))
			c.Assert(observation, NotNil)
			var transfer etypes.Transaction
			c.Assert(transfer.UnmarshalJSON(result), IsNil)
			c.Assert(transfer.Nonce(), Equals, uint64(1))
		} else {
			c.Assert(signErr, NotNil, Commentf("%s", tc.name))
			c.Assert(result, IsNil)
			c.Assert(observation, IsNil)
			c.Assert(nonce, Equals, uint64(0))
		}
		expected := map[string]int{"approval submitted": 1}
		if strings.HasPrefix(tc.sendError, "nonce too low") {
			expected["receipt requested"] = 1
			if !tc.success {
				retry := txOut
				retry.Checkpoint = checkpoint
				retrying.Store(true)
				nonceRPCError.Store(true)
				raw, retained, obs, retryErr := e.SignTx([]stypes.TxOutItem{retry}, 524)
				c.Assert(retryErr, NotNil, Commentf("%s", tc.name))
				c.Assert(raw, IsNil)
				c.Assert(obs, IsNil)
				c.Assert(retained, DeepEquals, checkpoint)
				nonceRPCError.Store(false)
				raw, retired, obs, retryErr := e.SignTx([]stypes.TxOutItem{retry}, 524)
				c.Assert(retryErr, IsNil, Commentf("%s", tc.name))
				c.Assert(raw, IsNil, Commentf("must not sign a transfer at spent nonce 0: %s", tc.name))
				c.Assert(retired, IsNil)
				c.Assert(obs, IsNil)
				expected["checkpoint nonce checked"] = 2
			}
		}
		e.client.Close()
		server.Close()
		close(events)
		seen := make(map[string]int)
		for event := range events {
			seen[event]++
		}
		c.Assert(seen, DeepEquals, expected, Commentf("%s", tc.name))
	}
}

func (s *EthereumSuite) TestSignTxLegacyCheckpointNeverApprovesOrAdvances(c *C) {
	e, txOut, cleanup := s.newApprovalBroadcastFixture(c)
	defer cleanup()
	txOut.Checkpoint = []byte("7") // May already have exposed a transfer signature.
	events := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if readErr != nil || json.Unmarshal(body, &req) != nil {
			events <- "invalid request"
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		response := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "eth_getTransactionCount":
			if len(req.Params) == 2 && string(req.Params[1]) == `"latest"` {
				events <- "confirmed nonce checked"
				response["result"] = "0x7"
			} else {
				events <- "pending nonce checked"
				response["result"] = "0x9"
			}
		case "eth_call":
			var args struct{ Data, Input string }
			if len(req.Params) > 0 && json.Unmarshal(req.Params[0], &args) == nil &&
				(strings.HasPrefix(args.Data, "0xdd62ed3e") || strings.HasPrefix(args.Input, "0xdd62ed3e")) {
				events <- "allowance checked"
				response["result"] = "0x" + strings.Repeat("0", 64)
			} else {
				r.Body = io.NopCloser(bytes.NewReader(body))
				s.server.Config.Handler.ServeHTTP(w, r)
				return
			}
		case "eth_sendRawTransaction":
			events <- "approval submitted"
			response["error"] = map[string]interface{}{"code": -32000, "message": "must not approve a numeric checkpoint"}
		default:
			r.Body = io.NopCloser(bytes.NewReader(body))
			s.server.Config.Handler.ServeHTTP(w, r)
			return
		}
		if encodeErr := json.NewEncoder(w).Encode(response); encodeErr != nil {
			events <- "response write failed"
		}
	}))
	defer server.Close()
	var err error
	e.client, err = ethclient.Dial(server.URL)
	c.Assert(err, IsNil)
	defer e.client.Close()
	raw, checkpoint, observation, err := e.SignTx([]stypes.TxOutItem{txOut}, 524)
	c.Assert(err, IsNil)
	c.Assert(raw, NotNil)
	c.Assert(observation, NotNil)
	c.Assert(checkpoint, DeepEquals, txOut.Checkpoint)
	var transfer etypes.Transaction
	c.Assert(transfer.UnmarshalJSON(raw), IsNil)
	c.Assert(transfer.Nonce(), Equals, uint64(7))
	server.Close()
	close(events)
	seen := make(map[string]int)
	for event := range events {
		seen[event]++
	}
	c.Assert(seen, DeepEquals, map[string]int{"confirmed nonce checked": 2})
}
