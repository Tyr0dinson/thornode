package evm

import (
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	ecommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// covChainID is the EVM chain id returned by the fake JSON-RPC server.
var covChainID = big.NewInt(43114)

// covExternalKeyHex is a fixed, non-vault private key used to sign "user" txs.
const covExternalKeyHex = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"

////////////////////////////////////////////////////////////////////////////////
// fake JSON-RPC server
////////////////////////////////////////////////////////////////////////////////

type covRPCHandler func(params []json.RawMessage) (any, error)

type covRPC struct {
	mu       sync.Mutex
	handlers map[string]covRPCHandler
	calls    map[string]int
	headers  []http.Header
	srv      *httptest.Server
}

type covRPCReq struct {
	ID     json.RawMessage   `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

func newCovRPC(t *testing.T) *covRPC {
	t.Helper()
	r := &covRPC{
		handlers: map[string]covRPCHandler{},
		calls:    map[string]int{},
	}
	r.setResult("eth_chainId", hexutil.EncodeBig(covChainID))
	r.setResult("eth_blockNumber", "0x64")
	r.setResult("eth_gasPrice", "0x1")
	r.setResult("eth_getTransactionCount", "0x0")
	r.setResult("eth_getBalance", "0xde0b6b3a7640000") // 1e18
	r.setResult("eth_estimateGas", "0x5208")
	r.setResult("eth_sendRawTransaction", "0x88df016429689c079f3b2f6ad39fa052532c56795b733da78a91ebe6a713944b")
	// default eth_call: 18 decimals
	r.setResult("eth_call", "0x0000000000000000000000000000000000000000000000000000000000000012")
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *covRPC) set(method string, h covRPCHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[method] = h
}

func (r *covRPC) setResult(method string, result any) {
	r.set(method, func([]json.RawMessage) (any, error) { return result, nil })
}

func (r *covRPC) setError(method, msg string) {
	r.set(method, func([]json.RawMessage) (any, error) { return nil, errors.New(msg) })
}

func (r *covRPC) count(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[method]
}

func (r *covRPC) lastHeader() http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.headers) == 0 {
		return nil
	}
	return r.headers[len(r.headers)-1]
}

func (r *covRPC) handle(req covRPCReq) map[string]any {
	r.mu.Lock()
	r.calls[req.Method]++
	h, ok := r.handlers[req.Method]
	r.mu.Unlock()
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if !ok {
		resp["error"] = map[string]any{"code": -32601, "message": "method not found: " + req.Method}
		return resp
	}
	res, err := h(req.Params)
	if err != nil {
		resp["error"] = map[string]any{"code": -32000, "message": err.Error()}
		return resp
	}
	if res == nil {
		resp["result"] = json.RawMessage("null")
	} else {
		resp["result"] = res
	}
	return resp
}

func (r *covRPC) serve(rw http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.headers = append(r.headers, req.Header.Clone())
	r.mu.Unlock()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		rw.WriteHeader(http.StatusBadRequest)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		var reqs []covRPCReq
		if err = json.Unmarshal(body, &reqs); err != nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		out := make([]map[string]any, 0, len(reqs))
		for _, rq := range reqs {
			out = append(out, r.handle(rq))
		}
		_ = json.NewEncoder(rw).Encode(out)
		return
	}
	var rq covRPCReq
	if err = json.Unmarshal(body, &rq); err != nil {
		rw.WriteHeader(http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(rw).Encode(r.handle(rq))
}

////////////////////////////////////////////////////////////////////////////////
// fake thorchain bridge
////////////////////////////////////////////////////////////////////////////////

type covBridge struct {
	thorclient.ThorchainBridge

	mu               sync.Mutex
	mimirs           map[string]int64
	mimirErr         error
	height           int64
	heightErr        error
	heightCalls      int
	heightFailFrom   int // when > 0, GetBlockHeight fails from this call number on
	consts           map[string]int64
	constsErr        error
	asgards          ttypes.Vaults
	asgardsErr       error
	keysignFailErr   error
	keysignFailures  []ttypes.Blame
	hasNetworkFee    bool
	hasNetworkFeeErr error
	lastObserved     int64
}

func newCovBridge() *covBridge {
	return &covBridge{
		mimirs: map[string]int64{},
		height: 1000,
		consts: map[string]int64{constants.SigningTransactionPeriod.String(): 300},
	}
}

func (b *covBridge) setMimir(key string, v int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mimirs[key] = v
}

func (b *covBridge) GetMimir(key string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mimirErr != nil {
		return 0, b.mimirErr
	}
	if v, ok := b.mimirs[key]; ok {
		return v, nil
	}
	return -1, nil
}

func (b *covBridge) GetMimirWithRef(template, ref string) (int64, error) {
	return b.GetMimir(fmt.Sprintf(template, ref))
}

func (b *covBridge) GetBlockHeight() (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.heightCalls++
	if b.heightFailFrom > 0 && b.heightCalls >= b.heightFailFrom {
		return 0, errors.New("height unavailable")
	}
	return b.height, b.heightErr
}

func (b *covBridge) GetConstants() (map[string]int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.consts, b.constsErr
}

func (b *covBridge) GetAsgards() (ttypes.Vaults, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.asgards, b.asgardsErr
}

func (b *covBridge) PostKeysignFailure(blame ttypes.Blame, _ int64, _ string, _ common.Coins, _ common.PubKey) (common.TxID, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.keysignFailures = append(b.keysignFailures, blame)
	if b.keysignFailErr != nil {
		return "", b.keysignFailErr
	}
	return common.TxID("ABCDEF"), nil
}

func (b *covBridge) HasNetworkFee(common.Chain) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hasNetworkFee, b.hasNetworkFeeErr
}

func (b *covBridge) GetNetworkFee(common.Chain) (uint64, uint64, error) {
	return 0, 0, nil
}

func (b *covBridge) GetLastObservedInHeight(common.Chain) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastObserved, nil
}

func (b *covBridge) WaitToCatchUp() error { return nil }

////////////////////////////////////////////////////////////////////////////////
// fake pubkey manager
////////////////////////////////////////////////////////////////////////////////

type covPubKeyMgr struct {
	mu        sync.Mutex
	pubkeys   common.PubKeys
	contracts map[string]common.Address
}

var _ pubkeymanager.PubKeyValidator = &covPubKeyMgr{}

func newCovPubKeyMgr() *covPubKeyMgr {
	return &covPubKeyMgr{contracts: map[string]common.Address{}}
}

func (p *covPubKeyMgr) add(pk common.PubKey, router common.Address) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pubkeys = append(p.pubkeys, pk)
	if !router.IsEmpty() {
		p.contracts[pk.String()] = router
	}
}

func (p *covPubKeyMgr) match(addr string, chain common.Chain) (bool, common.ChainPoolInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pk := range p.pubkeys {
		cpi, err := common.NewChainPoolInfo(chain, pk)
		if err != nil {
			continue
		}
		if strings.EqualFold(cpi.PoolAddress.String(), addr) {
			return true, cpi
		}
	}
	return false, common.EmptyChainPoolInfo
}

func (p *covPubKeyMgr) IsValidPoolAddress(addr string, chain common.Chain) (bool, common.ChainPoolInfo) {
	return p.match(addr, chain)
}

func (p *covPubKeyMgr) IsActivePoolAddress(addr string, chain common.Chain) (bool, common.ChainPoolInfo) {
	return p.match(addr, chain)
}

func (p *covPubKeyMgr) HasPubKey(pk common.PubKey) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pubkeys.Contains(pk)
}
func (p *covPubKeyMgr) AddPubKey(pk common.PubKey, _ bool, _ common.SigningAlgo) {}
func (p *covPubKeyMgr) AddNodePubKey(common.PubKey, common.SigningAlgo)          {}
func (p *covPubKeyMgr) RemovePubKey(common.PubKey)                               {}
func (p *covPubKeyMgr) GetSignPubKeys() common.PubKeys                           { return p.GetPubKeys() }
func (p *covPubKeyMgr) GetNodePubKey(common.SigningAlgo) common.PubKey           { return common.EmptyPubKey }
func (p *covPubKeyMgr) RegisterCallback(pubkeymanager.OnNewPubKey)               {}

func (p *covPubKeyMgr) GetPubKeys() common.PubKeys {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append(common.PubKeys{}, p.pubkeys...)
}

func (p *covPubKeyMgr) GetAlgoPubKeys(common.SigningAlgo, bool) common.PubKeys {
	return p.GetPubKeys()
}

func (p *covPubKeyMgr) GetContracts(common.Chain, bool) []common.Address {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []common.Address{}
	for _, pk := range p.pubkeys {
		if c, ok := p.contracts[pk.String()]; ok {
			out = append(out, c)
		}
	}
	return out
}

func (p *covPubKeyMgr) GetContract(_ common.Chain, pk common.PubKey) common.Address {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.contracts[pk.String()]; ok {
		return c
	}
	return common.NoAddress
}

////////////////////////////////////////////////////////////////////////////////
// client construction
////////////////////////////////////////////////////////////////////////////////

const covRouter = common.Address("0x17aB05351fC94a1a67Bf3f56DdbB941aE6c63E25")

func covThorKeys(t *testing.T) *thorclient.Keys {
	t.Helper()
	setupMocknetThorConfig()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := cKeys.NewInMemory(cdc)
	mnemonic := "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"
	_, err := kb.NewAccount("bob", mnemonic, "", cmd.THORChainHDPath, hd.Secp256k1)
	require.NoError(t, err)
	return thorclient.NewKeysWithKeybase(kb, "bob", "password")
}

func covMetrics(t *testing.T) *metrics.Metrics {
	t.Helper()
	if m != nil {
		return m
	}
	var err error
	m, err = metrics.NewMetrics(config.BifrostMetricsConfiguration{
		Enabled:      false,
		ListenPort:   9000,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
		Chains:       common.Chains{common.AVAXChain},
	})
	require.NoError(t, err)
	return m
}

func covChainConfig(chain common.Chain, rpcURL string) config.BifrostChainConfiguration {
	cfg := config.BifrostChainConfiguration{
		ChainID:          chain,
		RPCHost:          rpcURL,
		SolvencyBlocks:   10,
		MaxPendingNonces: 5,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:              chain,
			StartBlockHeight:     1,
			HTTPRequestTimeout:   2 * time.Second,
			GasCacheBlocks:       1,
			Concurrency:          2,
			TransactionBatchSize: 10,
			MaxGasLimit:          100_000,
			GasPriceResolution:   1,
			MaxContractTxLogs:    10,
		},
	}
	cfg.EVM.ReconstructLiabilitiesConcurrency = 2
	return cfg
}

type covEnv struct {
	rpc    *covRPC
	bridge *covBridge
	pkm    *covPubKeyMgr
	client *EVMClient
	keys   *thorclient.Keys
}

// newCovEnv builds an EVMClient against the fake RPC/bridge. The local node key is
// registered as a vault with covRouter as its router.
func newCovEnv(t *testing.T, chain common.Chain, mod func(*config.BifrostChainConfiguration)) *covEnv {
	t.Helper()
	env := &covEnv{
		rpc:    newCovRPC(t),
		bridge: newCovBridge(),
		pkm:    newCovPubKeyMgr(),
		keys:   covThorKeys(t),
	}
	cfg := covChainConfig(chain, env.rpc.srv.URL)
	if mod != nil {
		mod(&cfg)
	}
	c, err := NewEVMClient(env.keys, cfg, nil, env.bridge, covMetrics(t), env.pkm, thorclient.NewPoolMgr(env.bridge))
	require.NoError(t, err)
	require.NotNil(t, c)
	env.pkm.add(c.localPubKey, covRouter)
	c.signingReady.Store(true)
	env.client = c
	return env
}

func (e *covEnv) vaultAddr(t *testing.T) common.Address {
	t.Helper()
	addr, err := e.client.localPubKey.GetAddress(e.client.cfg.ChainID)
	require.NoError(t, err)
	return addr
}

////////////////////////////////////////////////////////////////////////////////
// chain data builders
////////////////////////////////////////////////////////////////////////////////

func covExternalKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := crypto.HexToECDSA(covExternalKeyHex)
	require.NoError(t, err)
	return k
}

func covSignTx(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, to *ecommon.Address, value *big.Int, data []byte) *etypes.Transaction {
	t.Helper()
	var tx *etypes.Transaction
	if to == nil {
		tx = etypes.NewContractCreation(nonce, value, 100_000, big.NewInt(25_000_000_000), data)
	} else {
		tx = etypes.NewTransaction(nonce, *to, value, 100_000, big.NewInt(25_000_000_000), data)
	}
	signed, err := etypes.SignTx(tx, etypes.NewPragueSigner(covChainID), key)
	require.NoError(t, err)
	return signed
}

func covHeader(number int64, parent ecommon.Hash, hasTxs bool) *etypes.Header {
	h := &etypes.Header{
		ParentHash:  parent,
		UncleHash:   etypes.EmptyUncleHash,
		Root:        ecommon.HexToHash("0x01"),
		TxHash:      etypes.EmptyTxsHash,
		ReceiptHash: etypes.EmptyReceiptsHash,
		Difficulty:  big.NewInt(1),
		Number:      big.NewInt(number),
		GasLimit:    30_000_000,
		Time:        1_700_000_000,
	}
	if hasTxs {
		h.TxHash = ecommon.HexToHash("0x1234")
	}
	return h
}

// covBlockJSON returns a JSON-RPC block object containing the given txs.
func covBlockJSON(t *testing.T, header *etypes.Header, txs []*etypes.Transaction) json.RawMessage {
	t.Helper()
	hb, err := json.Marshal(header)
	require.NoError(t, err)
	obj := map[string]any{}
	require.NoError(t, json.Unmarshal(hb, &obj))
	signer := etypes.NewPragueSigner(covChainID)
	txList := make([]any, 0, len(txs))
	for i, tx := range txs {
		tb, mErr := json.Marshal(tx)
		require.NoError(t, mErr)
		txObj := map[string]any{}
		require.NoError(t, json.Unmarshal(tb, &txObj))
		from, sErr := etypes.Sender(signer, tx)
		require.NoError(t, sErr)
		txObj["from"] = from.Hex()
		txObj["blockHash"] = header.Hash().Hex()
		txObj["blockNumber"] = hexutil.EncodeBig(header.Number)
		txObj["transactionIndex"] = hexutil.EncodeUint64(uint64(i))
		txList = append(txList, txObj)
	}
	obj["transactions"] = txList
	obj["uncles"] = []any{}
	out, err := json.Marshal(obj)
	require.NoError(t, err)
	return out
}

// covTxJSON returns a JSON-RPC transaction object (pending when blockNumber is nil).
func covTxJSON(t *testing.T, tx *etypes.Transaction, blockNumber *big.Int) json.RawMessage {
	t.Helper()
	tb, err := json.Marshal(tx)
	require.NoError(t, err)
	txObj := map[string]any{}
	require.NoError(t, json.Unmarshal(tb, &txObj))
	from, err := etypes.Sender(etypes.NewPragueSigner(covChainID), tx)
	require.NoError(t, err)
	txObj["from"] = from.Hex()
	if blockNumber != nil {
		txObj["blockNumber"] = hexutil.EncodeBig(blockNumber)
		txObj["blockHash"] = ecommon.HexToHash("0xabcd").Hex()
		txObj["transactionIndex"] = "0x0"
	}
	out, err := json.Marshal(txObj)
	require.NoError(t, err)
	return out
}

func covReceiptJSON(t *testing.T, tx *etypes.Transaction, status uint64, gasPrice *big.Int, logs []*etypes.Log) json.RawMessage {
	t.Helper()
	if logs == nil {
		logs = []*etypes.Log{}
	}
	r := &etypes.Receipt{
		Type:              tx.Type(),
		Status:            status,
		CumulativeGasUsed: 21000,
		Logs:              logs,
		TxHash:            tx.Hash(),
		GasUsed:           21000,
		EffectiveGasPrice: gasPrice,
		BlockHash:         ecommon.HexToHash("0xabcd"),
		BlockNumber:       big.NewInt(1),
	}
	r.Bloom = etypes.CreateBloom(r)
	out, err := json.Marshal(r)
	require.NoError(t, err)
	return out
}

// covParamHash extracts a hash/string param at index i.
func covParamString(params []json.RawMessage, i int) string {
	if len(params) <= i {
		return ""
	}
	var s string
	_ = json.Unmarshal(params[i], &s)
	return s
}
