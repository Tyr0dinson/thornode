package utxo

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	btcchaincfg "github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcutil"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// -------------------------------------------------------------------------------------
// fixtures
// -------------------------------------------------------------------------------------

type utxoCovBridge struct {
	thorclient.ThorchainBridge
	mu              sync.Mutex
	asgards         ttypes.Vaults
	asgardErr       error
	lastObservedErr error
	pubkeys         []thorclient.PubKeyContractAddressPair
}

func (b *utxoCovBridge) GetAsgards() (ttypes.Vaults, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.asgards, b.asgardErr
}

func (b *utxoCovBridge) GetAsgardPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	return b.pubkeys, nil
}

func (b *utxoCovBridge) GetLastObservedInHeight(common.Chain) (int64, error) {
	return 0, b.lastObservedErr
}

func (b *utxoCovBridge) GetMimir(string) (int64, error)                { return 0, nil }
func (b *utxoCovBridge) GetMimirWithRef(string, string) (int64, error) { return 0, nil }

// utxoRPCCov serves the shared BTC fixtures, with per-method overrides.
type utxoRPCCov struct {
	mu        sync.Mutex
	overrides map[string]map[string]any
}

func rpcErrCov(code int, msg string) map[string]any {
	return map[string]any{"result": nil, "error": map[string]any{"code": code, "message": msg}}
}

func rpcResultCov(result any) map[string]any {
	return map[string]any{"result": result, "error": nil}
}

func (r *utxoRPCCov) set(key string, resp map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overrides[key] = resp
}

func (r *utxoRPCCov) respond(method string, params []any, id any) map[string]any {
	key := method
	if method == "getrawtransaction" && len(params) > 0 {
		key = fmt.Sprintf("%s-%v", method, params[0])
	}
	r.mu.Lock()
	resp, ok := r.overrides[key]
	if !ok {
		resp, ok = r.overrides[method]
	}
	r.mu.Unlock()
	if !ok {
		resp = btcChainRPCs[key]
		if resp == nil {
			resp = btcChainRPCs[method]
		}
	}
	if resp == nil {
		resp = rpcErrCov(-32601, "method not found")
	}
	out := make(map[string]any, len(resp)+1)
	for k, v := range resp {
		out[k] = v
	}
	out["id"] = id
	out["jsonrpc"] = "2.0"
	return out
}

func (r *utxoRPCCov) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	rw.Header().Set("Content-Type", "application/json")
	type call struct {
		Method string `json:"method"`
		Params []any  `json:"params"`
		ID     any    `json:"id"`
	}
	if len(body) > 0 && body[0] == '[' {
		var calls []call
		_ = json.Unmarshal(body, &calls)
		out := make([]map[string]any, len(calls))
		for i, cl := range calls {
			out[i] = r.respond(cl.Method, cl.Params, cl.ID)
		}
		_ = json.NewEncoder(rw).Encode(out)
		return
	}
	var cl call
	_ = json.Unmarshal(body, &cl)
	_ = json.NewEncoder(rw).Encode(r.respond(cl.Method, cl.Params, cl.ID))
}

type UTXOCovSuite struct {
	keys   *thorclient.Keys
	rpc    *utxoRPCCov
	server *httptest.Server
	bridge *utxoCovBridge
	client *Client
	cfg    config.BifrostChainConfiguration
	feeQ   chan common.NetworkFee
}

var _ = Suite(&UTXOCovSuite{})

func (s *UTXOCovSuite) SetUpSuite(c *C) {
	ttypes.SetupConfigForTest()
	s.keys = newUTXOKeysCov(c, true)
}

func newUTXOKeysCov(c *C, create bool) *thorclient.Keys {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kb := cKeys.NewInMemory(codec.NewProtoCodec(registry))
	if create {
		_, _, err := kb.NewMnemonic(bob, cKeys.English, cmd.THORChainHDPath, password, hd.Secp256k1)
		c.Assert(err, IsNil)
	}
	return thorclient.NewKeysWithKeybase(kb, bob, password)
}

func (s *UTXOCovSuite) SetUpTest(c *C) {
	s.rpc = &utxoRPCCov{overrides: map[string]map[string]any{}}
	s.server = httptest.NewServer(s.rpc)
	s.bridge = &utxoCovBridge{}
	s.cfg = config.BifrostChainConfiguration{
		ChainID:     common.BTCChain,
		UserName:    bob,
		Password:    password,
		DisableTLS:  true,
		HTTPostMode: true,
		RPCHost:     s.server.Listener.Addr().String(),
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:              common.BTCChain,
			StartBlockHeight:     1,
			MaxReorgRescanBlocks: 1,
		},
	}
	s.cfg.UTXO.TransactionBatchSize = 500
	s.cfg.UTXO.MaxMempoolBatches = 10
	s.cfg.UTXO.EstimatedAverageTxSize = 1000
	var err error
	s.client, err = NewClient(s.keys, s.cfg, nil, s.bridge, GetMetricForTest(c, common.BTCChain))
	c.Assert(err, IsNil)
	s.client.disableVinZeroBatch = true
	// never spawn the background consolidation goroutine from FetchTxs
	s.client.consolidateInProgress.Store(true)
	s.feeQ = make(chan common.NetworkFee, 10)
	s.client.globalNetworkFeeQueue = s.feeQ
}

func (s *UTXOCovSuite) TearDownTest(_ *C) {
	s.server.Close()
}

func regtestP2WPKHCov(c *C, pk common.PubKey) (string, string) {
	addr, err := pk.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	decoded, err := btcutil.DecodeAddress(addr.String(), &btcchaincfg.RegressionNetParams)
	c.Assert(err, IsNil)
	return addr.String(), "0014" + hex.EncodeToString(decoded.ScriptAddress())
}

// -------------------------------------------------------------------------------------
// tests
// -------------------------------------------------------------------------------------

func (s *UTXOCovSuite) TestNewClientErrorsCov(c *C) {
	m := GetMetricForTest(c, common.BTCChain)

	cfg := s.cfg
	cfg.ChainID = common.ETHChain
	_, err := NewClient(s.keys, cfg, nil, s.bridge, m)
	c.Assert(err, ErrorMatches, "unsupported utxo chain: ETH")

	_, err = NewClient(newUTXOKeysCov(c, false), s.cfg, nil, s.bridge, m)
	c.Assert(err, ErrorMatches, "fail to get THORChain private key.*")

	s.rpc.set("createwallet", rpcErrCov(-1, "wallet disabled"))
	_, err = NewClient(s.keys, s.cfg, nil, s.bridge, m)
	c.Assert(err, ErrorMatches, "fail to register.*wallet disabled.*")

	// An existing wallet (-4) is fine, but the import itself can fail.
	s.rpc.set("createwallet", rpcErrCov(-4, "already exists"))
	s.rpc.set("importaddress", rpcErrCov(-5, "invalid address"))
	_, err = NewClient(s.keys, s.cfg, nil, s.bridge, m)
	c.Assert(err, ErrorMatches, "fail to register.*invalid address.*")

	c.Check(s.client.RegisterPublicKey(common.PubKey("bogus")), ErrorMatches, "fail to get address from pubkey.*")
}

func (s *UTXOCovSuite) TestAccessorsCov(c *C) {
	s.client.lastFeeRate.Store(25)
	size, rate := s.client.GetNetworkFee()
	c.Check(size, Equals, uint64(1000))
	c.Check(rate, Equals, uint64(25))

	zec := &Client{cfg: config.BifrostChainConfiguration{ChainID: common.ZECChain}}
	zec.cfg.UTXO.EstimatedAverageTxSize = 1000
	zec.lastFeeRate.Store(10000)
	size, rate = zec.GetNetworkFee()
	c.Check(size, Equals, uint64(1))
	c.Check(rate, Equals, uint64(10000))

	c.Check(s.client.GetAddress(common.PubKey("bogus")), Equals, "")

	s.bridge.lastObservedErr = errors.New("thornode down")
	c.Check(s.client.RollbackBlockScanner(), ErrorMatches, ".*thornode down.*")
	s.bridge.lastObservedErr = nil
	c.Check(s.client.RollbackBlockScanner(), IsNil)

	vault := ttypes.GetRandomPubKey().String()
	_, _, err := s.client.GetLatestTxForVault(vault)
	c.Check(err, NotNil)
	c.Assert(s.client.signerCacheManager.SetSigned("in", types.InboundCacheKey(vault, "BTC"), "OBS"), IsNil)
	obs, _, err := s.client.GetLatestTxForVault(vault)
	c.Check(err, NotNil) // nothing broadcast yet
	c.Check(obs, Equals, "OBS")
}

func (s *UTXOCovSuite) TestGetAccountCov(c *C) {
	_, err := s.client.GetAccount(common.EmptyPubKey, nil)
	c.Check(err, ErrorMatches, "pubkey can't be empty")
	_, err = s.client.GetAccount(common.PubKey("bogus"), nil)
	c.Check(err, ErrorMatches, "fail to get address from pubkey.*")

	pk := ttypes.GetRandomPubKey()
	s.rpc.set("listunspent", rpcErrCov(-1, "wallet locked"))
	_, err = s.client.GetAccount(pk, nil)
	c.Check(err, ErrorMatches, "fail to get UTXOs.*wallet locked.*")

	addr, script := regtestP2WPKHCov(c, pk)
	s.rpc.set("listunspent", rpcResultCov([]map[string]any{
		// confirmed: counted
		{"txid": strings.Repeat("a", 64), "vout": 0, "address": addr, "scriptPubKey": script, "amount": 1.5, "confirmations": 3},
		// unconfirmed and not from asgard (tx fixture sender is unknown): skipped
		{"txid": strings.Repeat("b", 64), "vout": 0, "address": addr, "scriptPubKey": script, "amount": 2.0, "confirmations": 0},
		// not a decodable script: skipped
		{"txid": strings.Repeat("c", 64), "vout": 0, "address": addr, "scriptPubKey": "zz", "amount": 4.0, "confirmations": 9},
	}))
	acct, err := s.client.GetAccount(pk, nil)
	c.Assert(err, IsNil)
	c.Check(acct.Coins.GetCoin(common.BTCAsset).Amount.Uint64(), Equals, uint64(150000000))

	// Not implemented for UTXO chains.
	empty, err := s.client.GetAccountByAddress(addr, nil)
	c.Check(err, IsNil)
	c.Check(empty.Coins, HasLen, 0)
}

func (s *UTXOCovSuite) TestOnObservedTxInCov(c *C) {
	// Not a valid tx hash: nothing recorded.
	s.client.OnObservedTxIn(types.TxInItem{Tx: "nope"}, 50)
	meta, err := s.client.temporalStorage.GetBlockMeta(50)
	c.Assert(err, IsNil)
	c.Check(meta, IsNil)

	// A customer outbound: block meta is created and the signer cache updated.
	inHash := strings.Repeat("AB", 32)
	txIn := types.TxInItem{
		Tx:     strings.Repeat("cd", 32),
		Sender: "bcrt1qsomeone",
		Memo:   "OUT:" + inHash,
		To:     "bcrt1qdest",
		Coins:  common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))},
	}
	s.client.OnObservedTxIn(txIn, 50)
	meta, err = s.client.temporalStorage.GetBlockMeta(50)
	c.Assert(err, IsNil)
	c.Assert(meta, NotNil)
	c.Check(meta.CustomerTransactions, DeepEquals, []string{txIn.Tx})
	c.Check(s.client.signerCacheManager.HasSigned(txIn.CacheHash(common.BTCChain, inHash)), Equals, true)

	// Non-outbound memo: tracked but no signer cache entry.
	other := txIn
	other.Tx = strings.Repeat("ef", 32)
	other.Memo = "ADD:BTC.BTC"
	s.client.OnObservedTxIn(other, 50)
	meta, err = s.client.temporalStorage.GetBlockMeta(50)
	c.Assert(err, IsNil)
	c.Check(meta.CustomerTransactions, HasLen, 2)
	c.Check(s.client.signerCacheManager.HasSigned(other.CacheHash(common.BTCChain, "")), Equals, false)
}

func (s *UTXOCovSuite) TestFetchTxsErrorsCov(c *C) {
	s.rpc.set("getblockhash", rpcErrCov(-8, "Block height out of range"))
	_, err := s.client.FetchTxs(10, 10)
	c.Check(errors.Is(err, btypes.ErrUnavailableBlock), Equals, true)

	s.rpc.set("getblockhash", rpcErrCov(-1, "boom"))
	_, err = s.client.FetchTxs(10, 10)
	c.Check(err, ErrorMatches, "fail to get block.*boom.*")

	// A block without hashes is rejected.
	s.rpc.set("getblockhash", btcChainRPCs["getblockhash"])
	s.rpc.set("getblock", rpcResultCov(map[string]any{"height": 10}))
	_, err = s.client.FetchTxs(10, 10)
	c.Check(err, ErrorMatches, "fail to get block.*")
}

func (s *UTXOCovSuite) TestFetchTxsNearTipCov(c *C) {
	s.client.cfg.UTXO.BlockCacheCount = 10
	s.client.cfg.BlockScanner.ObservationFlexibilityBlocks = 5
	// the vault the block fixture pays to (see test/fixtures/endpoints/vaults/pubKeys.json)
	vault, err := common.NewPubKey("tthorpub1addwnpepqflvfv08t6qt95lmttd6wpf3ss8wx63e9vf6fvyuj2yy6nnyna576rfzjks")
	c.Assert(err, IsNil)
	s.bridge.pubkeys = []thorclient.PubKeyContractAddressPair{{PubKey: vault, Algo: common.SigningAlgoSecp256k1}}

	// A stale meta for the previous height triggers re-org handling: its
	// customer tx is gone from the chain, so an errata is raised and the block
	// is rescanned.
	goneTx := strings.Repeat("9", 64)
	stale := sharedutxo.NewBlockMeta("old-prev", 1696760, "stale-hash")
	stale.AddCustomerTransaction(goneTx)
	c.Assert(s.client.temporalStorage.SaveBlockMeta(1696760, stale), IsNil)
	s.rpc.set("getrawtransaction-"+goneTx, rpcErrCov(-5, "No such transaction"))
	errata := make(chan types.ErrataBlock, 10)
	s.client.globalErrataQueue = errata

	// An existing meta for the scanned height is updated in place.
	existing := sharedutxo.NewBlockMeta("", 1696761, "")
	existing.AddSelfTransaction(strings.Repeat("8", 64))
	c.Assert(s.client.temporalStorage.SaveBlockMeta(1696761, existing), IsNil)

	txIn, err := s.client.FetchTxs(1696761, 1696761)
	c.Assert(err, IsNil)
	c.Check(txIn.Chain, Equals, common.BTCChain)
	c.Check(s.client.getCurrentBlockHeight(), Equals, int64(1696761))

	// The rescanned block yields the inbound once; the main scan of the same
	// block then sees it as already observed.
	c.Assert(txIn.TxArray, HasLen, 1)
	c.Check(txIn.TxArray[0].Tx, Equals, "24ed2d26fd5d4e0e8fa86633e40faf1bdfc8d1903b1cd02855286312d48818a2")

	select {
	case eb := <-errata:
		c.Check(eb.Height, Equals, int64(1696760))
		c.Assert(eb.Txs, HasLen, 1)
		c.Check(eb.Txs[0].TxID.String(), Equals, goneTx)
	default:
		c.Fatal("expected an errata block for the vanished tx")
	}

	meta, err := s.client.temporalStorage.GetBlockMeta(1696761)
	c.Assert(err, IsNil)
	c.Assert(meta, NotNil)
	c.Check(meta.BlockHash, Equals, "000000008de7a25f64f9780b6c894016d2c63716a89f7c9e704ebb7e8377a0c8")
	c.Check(meta.SelfTransactions, HasLen, 1)

	// Near the tip a network fee is published from the block stats fixture.
	select {
	case fee := <-s.feeQ:
		c.Check(fee.Chain, Equals, common.BTCChain)
		c.Check(fee.TransactionRate > 0, Equals, true)
	default:
		c.Fatal("expected a network fee to be published")
	}

	// Block stats unavailable: the scan still succeeds but no fee is sent.
	s.rpc.set("getblockstats", rpcErrCov(-1, "stats unavailable"))
	_, err = s.client.FetchTxs(1696761, 1696761)
	c.Assert(err, IsNil)
	c.Check(len(s.feeQ), Equals, 0)
}

func (s *UTXOCovSuite) TestFetchMemPoolCov(c *C) {
	s.rpc.set("getrawmempool", rpcErrCov(-1, "mempool unavailable"))
	_, err := s.client.FetchMemPool(1)
	c.Check(err, ErrorMatches, ".*mempool unavailable.*")

	hashes := []string{strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64)}
	s.rpc.set("getrawmempool", rpcResultCov(hashes))
	s.client.cfg.UTXO.TransactionBatchSize = 1
	s.client.cfg.UTXO.MaxMempoolBatches = 2

	// Every tx lookup fails: they are released from the mempool cache so a
	// later poll retries them.
	s.rpc.set("getrawtransaction", rpcErrCov(-5, "No such mempool transaction"))
	txIn, err := s.client.FetchMemPool(1)
	c.Assert(err, IsNil)
	c.Check(txIn.MemPool, Equals, true)
	c.Check(txIn.TxArray, HasLen, 0)
	for _, h := range hashes[:2] {
		added, trackErr := s.client.temporalStorage.TrackMempoolTx(h)
		c.Assert(trackErr, IsNil)
		c.Check(added, Equals, true, Commentf("hash %s should have been released", h))
	}
}

func (s *UTXOCovSuite) TestConfirmationCountCov(c *C) {
	c.Check(s.client.GetConfirmationCount(types.TxIn{}), Equals, int64(0))
	c.Check(s.client.GetConfirmationCount(types.TxIn{MemPool: true, TxArray: []*types.TxInItem{{}}}), Equals, int64(0))

	txIn := types.TxIn{
		Chain: common.BTCChain,
		TxArray: []*types.TxInItem{{
			BlockHeight: 1696761,
			Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100000000))},
		}},
	}
	s.client.cfg.MinConfirmations = 3
	c.Check(s.client.GetConfirmationCount(txIn), Equals, int64(3))

	// Coinbase lookup failure falls back to the default coinbase.
	s.rpc.set("getblockhash", rpcErrCov(-1, "boom"))
	_, err := s.client.getCoinbaseValue(1)
	c.Check(err, ErrorMatches, "fail to get block verbose tx.*")
	s.client.cfg.MinConfirmations = 0
	c.Check(s.client.GetConfirmationCount(txIn), Equals, int64(1))

	// A block without a coinbase tx.
	s.rpc.set("getblockhash", btcChainRPCs["getblockhash"])
	s.rpc.set("getblock", rpcResultCov(map[string]any{"hash": "h", "height": 1, "tx": []any{}}))
	_, err = s.client.getCoinbaseValue(1)
	c.Check(err, ErrorMatches, "fail to get coinbase value")
}

func (s *UTXOCovSuite) TestShouldReportSolvencyCov(c *C) {
	newClient := func(chain common.Chain, last int64) *Client {
		cl := &Client{cfg: config.BifrostChainConfiguration{ChainID: chain}, log: log.Logger}
		cl.lastSolvencyCheckHeight.Store(last)
		return cl
	}
	c.Check(newClient(common.BTCChain, 10).ShouldReportSolvency(11), Equals, false)
	c.Check(newClient(common.BTCChain, 10).ShouldReportSolvency(12), Equals, true)
	c.Check(newClient(common.BCHChain, 0).ShouldReportSolvency(7), Equals, true)
	c.Check(newClient(common.DOGEChain, 0).ShouldReportSolvency(20), Equals, true)
	c.Check(newClient(common.ZECChain, 0).ShouldReportSolvency(21), Equals, false)
	c.Check(newClient(common.LTCChain, 0).ShouldReportSolvency(10), Equals, true)
	c.Check(newClient(common.LTCChain, 0).ShouldReportSolvency(12), Equals, false)
	c.Check(newClient(common.LTCChain, 7).ShouldReportSolvency(10), Equals, false)
}

func (s *UTXOCovSuite) TestReportSolvencyCov(c *C) {
	queue := make(chan types.Solvency, 10)
	s.client.globalSolvencyQueue = queue
	s.client.lastSolvencyCheckHeight.Store(100)

	// Too soon after the last check.
	c.Check(s.client.ReportSolvency(101), IsNil)

	s.bridge.asgardErr = errors.New("no asgards")
	c.Check(s.client.ReportSolvency(200), ErrorMatches, "fail to get asgards.*no asgards")
	s.bridge.asgardErr = nil

	// The account lookup fails for every vault: nothing is reported.
	s.rpc.set("listunspent", rpcErrCov(-1, "wallet locked"))
	s.bridge.asgards = ttypes.Vaults{{
		PubKey: ttypes.GetRandomPubKey(),
		Coins:  common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100))},
	}}
	c.Check(s.client.ReportSolvency(200), IsNil)
	c.Check(len(queue), Equals, 0)

	// ZEC sizes its gas estimate from the full ZIP-317 fee.
	zec := &Client{
		cfg:          config.BifrostChainConfiguration{ChainID: common.ZECChain},
		log:          log.Logger,
		bridge:       &utxoCovBridge{},
		blockScanner: s.client.blockScanner,
	}
	c.Check(zec.ReportSolvency(30), IsNil)
}

func (s *UTXOCovSuite) TestCanDeleteBlockAndNetworkInfoCov(c *C) {
	c.Check(s.client.canDeleteBlock(nil), Equals, true)

	meta := sharedutxo.NewBlockMeta("p", 1, "h")
	meta.AddSelfTransaction(strings.Repeat("1", 64))
	s.rpc.set("getmempoolentry", rpcResultCov(map[string]any{"vsize": 100, "time": 1, "height": 1}))
	c.Check(s.client.canDeleteBlock(meta), Equals, false)

	s.rpc.set("getmempoolentry", rpcErrCov(-5, "Transaction not in mempool"))
	c.Check(s.client.canDeleteBlock(meta), Equals, true)

	s.client.minRelayFeeSats.Store(77)
	s.rpc.set("getnetworkinfo", rpcErrCov(-1, "boom"))
	s.client.updateNetworkInfo()
	c.Check(s.client.minRelayFeeSats.Load(), Equals, uint64(77))
}

func (s *UTXOCovSuite) TestGetVinZeroTxsCov(c *C) {
	block, err := s.client.getBlock(1696761)
	c.Assert(err, IsNil)
	s.client.cfg.UTXO.TransactionBatchSize = 2

	vinZero, err := s.client.getVinZeroTxs(block)
	c.Assert(err, IsNil)
	// Every getrawtransaction answers with the shared tx fixture.
	c.Check(len(vinZero) > 0, Equals, true)
	for txid, tx := range vinZero {
		c.Check(tx.Txid, Equals, txid)
	}

	// Batch prefetch also feeds extractTxs.
	s.client.disableVinZeroBatch = false
	txIn, err := s.client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Check(txIn.Chain, Equals, common.BTCChain)
}

func (s *UTXOCovSuite) TestIsValidUTXOChainsCov(c *C) {
	for _, chain := range []common.Chain{common.DOGEChain, common.BCHChain, common.LTCChain, common.BTCChain, common.ZECChain} {
		cl := &Client{cfg: config.BifrostChainConfiguration{ChainID: chain}, log: log.Logger}
		// p2pkh is valid for every chain
		p2pkh := "76a914" + strings.Repeat("11", 20) + "88ac"
		c.Check(cl.isValidUTXO(p2pkh), Equals, true, Commentf("%s p2pkh", chain))
		// 1-of-1 bare multisig is rejected
		multisig := "5121" + "02" + strings.Repeat("22", 32) + "51ae"
		c.Check(cl.isValidUTXO(multisig), Equals, false, Commentf("%s multisig", chain))
		// unparseable script
		c.Check(cl.isValidUTXO("05"), Equals, false, Commentf("%s truncated", chain))
		c.Check(cl.isValidUTXO("zz"), Equals, false, Commentf("%s bad hex", chain))
	}
}
