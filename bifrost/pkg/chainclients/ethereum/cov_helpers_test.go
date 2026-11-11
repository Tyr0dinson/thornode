package ethereum

import (
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
	etypes "github.com/ethereum/go-ethereum/core/types"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// covChainID is the EVM chain id the coverage fake RPC reports.
const covChainID = 1337

// covRouter is the router contract the coverage pubkey manager reports for every vault.
var covRouter = common.Address("0xe65e9d372f8cacc7b6dfcd4af6507851ed31bb44")

// covRPCHandler returns the JSON-RPC result for one call, or an error that is
// returned to the caller as a JSON-RPC error object.
type covRPCHandler func(params []json.RawMessage) (any, error)

// covRPCServer is a programmable fake Ethereum JSON-RPC endpoint.
type covRPCServer struct {
	srv      *httptest.Server
	mu       sync.Mutex
	handlers map[string]covRPCHandler
	calls    map[string]int
	params   map[string][]json.RawMessage
}

func newCovRPCServer(t *testing.T) *covRPCServer {
	t.Helper()
	s := &covRPCServer{
		handlers: map[string]covRPCHandler{},
		calls:    map[string]int{},
		params:   map[string][]json.RawMessage{},
	}
	s.set("eth_chainId", covResult(fmt.Sprintf("0x%x", covChainID)))
	s.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		var r struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err = json.Unmarshal(body, &r); err != nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		h := s.handlers[r.Method]
		s.calls[r.Method]++
		s.params[r.Method] = r.Params
		s.mu.Unlock()
		resp := map[string]any{"jsonrpc": "2.0", "id": r.ID}
		if h == nil {
			resp["error"] = map[string]any{"code": -32601, "message": "method not found: " + r.Method}
		} else {
			res, herr := h(r.Params)
			if herr != nil {
				resp["error"] = map[string]any{"code": -32000, "message": herr.Error()}
			} else {
				resp["result"] = res
			}
		}
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(resp)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *covRPCServer) set(method string, h covRPCHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

func (s *covRPCServer) count(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

func (s *covRPCServer) lastParams(method string) []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.params[method]
}

// covResult returns a handler that always yields the given result.
func covResult(v any) covRPCHandler {
	return func([]json.RawMessage) (any, error) { return v, nil }
}

// covErr returns a handler that always yields a JSON-RPC error.
func covErr(msg string) covRPCHandler {
	return func([]json.RawMessage) (any, error) { return nil, errors.New(msg) }
}

func covHex(v uint64) string { return fmt.Sprintf("0x%x", v) }

func covBigHex(v *big.Int) string { return "0x" + v.Text(16) }

// covUint256 ABI-encodes a single uint256 word.
func covUint256(v *big.Int) string {
	return "0x" + fmt.Sprintf("%064x", v)
}

// covHeader builds a header for the given height.
func covHeader(num uint64, parent ecommon.Hash, hasTxs bool) *etypes.Header {
	txHash := etypes.EmptyTxsHash
	if hasTxs {
		txHash = ecommon.HexToHash("0x01")
	}
	return &etypes.Header{
		ParentHash:  parent,
		UncleHash:   etypes.EmptyUncleHash,
		Root:        ecommon.HexToHash("0x02"),
		TxHash:      txHash,
		ReceiptHash: etypes.EmptyReceiptsHash,
		Difficulty:  big.NewInt(0),
		Number:      new(big.Int).SetUint64(num),
		GasLimit:    30_000_000,
		Time:        1_700_000_000 + num,
		BaseFee:     big.NewInt(1_000_000_000),
	}
}

// covBlockJSON renders a full block (with transaction objects) as returned by eth_getBlockByNumber.
func covBlockJSON(t *testing.T, header *etypes.Header, txs []*etypes.Transaction) json.RawMessage {
	t.Helper()
	hb, err := header.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	var m map[string]json.RawMessage
	if err = json.Unmarshal(hb, &m); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	txJSON := make([]json.RawMessage, 0, len(txs))
	for _, tx := range txs {
		var b []byte
		b, err = tx.MarshalJSON()
		if err != nil {
			t.Fatalf("marshal tx: %v", err)
		}
		txJSON = append(txJSON, b)
	}
	m["transactions"], _ = json.Marshal(txJSON)
	m["uncles"] = json.RawMessage(`[]`)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal block: %v", err)
	}
	return out
}

// covPendingBlockJSON renders a block as geth returns the "pending" block: the
// hash, nonce and miner fields are null.
func covPendingBlockJSON(t *testing.T, header *etypes.Header, txs []*etypes.Transaction) json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(covBlockJSON(t, header, txs), &m); err != nil {
		t.Fatalf("unmarshal block: %v", err)
	}
	for _, field := range []string{"hash", "nonce", "miner"} {
		m[field] = json.RawMessage(`null`)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal block: %v", err)
	}
	return out
}

// covReceiptJSON renders a receipt for the given tx hash.
func covReceiptJSON(txHash ecommon.Hash, status uint64, logs []*etypes.Log) json.RawMessage {
	if logs == nil {
		logs = []*etypes.Log{}
	}
	for _, l := range logs {
		l.TxHash = txHash
		l.BlockHash = ecommon.HexToHash("0x03")
	}
	logJSON, _ := json.Marshal(logs)
	return json.RawMessage(fmt.Sprintf(`{"transactionHash":%q,"transactionIndex":"0x0","blockNumber":"0x1","blockHash":"0x0000000000000000000000000000000000000000000000000000000000000003","cumulativeGasUsed":"0x5208","gasUsed":"0x5208","effectiveGasPrice":"0x3b9aca00","logsBloom":"0x%s","logs":%s,"status":"%s","type":"0x0","contractAddress":null}`,
		txHash.Hex(), strings.Repeat("00", 256), string(logJSON), covHex(status)))
}

// covBridge is a programmable ThorchainBridge; any method not overridden panics.
type covBridge struct {
	thorclient.ThorchainBridge
	mu           sync.Mutex
	mimir        map[string]int64
	mimirErr     error
	height       int64
	heightErr    error
	constants    map[string]int64
	constErr     error
	asgards      thortypes.Vaults
	asgardErr    error
	keysignCalls int
	keysignErr   error
	lastObserved int64
	lastObsErr   error
}

func newCovBridge() *covBridge {
	return &covBridge{
		mimir:     map[string]int64{},
		height:    1000,
		constants: map[string]int64{},
	}
}

func (b *covBridge) GetMimir(key string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mimirErr != nil {
		return 0, b.mimirErr
	}
	return b.mimir[key], nil
}

func (b *covBridge) GetMimirWithRef(template, ref string) (int64, error) {
	return b.GetMimir(fmt.Sprintf(template, ref))
}

func (b *covBridge) setMimir(key string, v int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mimir[key] = v
}

func (b *covBridge) GetBlockHeight() (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.height, b.heightErr
}

func (b *covBridge) GetConstants() (map[string]int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.constants, b.constErr
}

func (b *covBridge) GetAsgards() (thortypes.Vaults, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(thortypes.Vaults, len(b.asgards))
	copy(out, b.asgards)
	return out, b.asgardErr
}

func (b *covBridge) PostKeysignFailure(_ thortypes.Blame, _ int64, _ string, _ common.Coins, _ common.PubKey) (common.TxID, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.keysignCalls++
	return common.BlankTxID, b.keysignErr
}

func (b *covBridge) GetLastObservedInHeight(_ common.Chain) (int64, error) {
	return b.lastObserved, b.lastObsErr
}

// covPubKeyMgr reports a fixed set of vault pubkeys that all share covRouter.
type covPubKeyMgr struct {
	*pubkeymanager.MockPoolAddressValidator
	mu      sync.Mutex
	pubkeys common.PubKeys
}

func (p *covPubKeyMgr) GetAlgoPubKeys(_ common.SigningAlgo, _ bool) common.PubKeys {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append(common.PubKeys{}, p.pubkeys...)
}

func (p *covPubKeyMgr) GetContract(_ common.Chain, pk common.PubKey) common.Address {
	for _, item := range p.GetAlgoPubKeys(common.SigningAlgoSecp256k1, true) {
		if item.Equals(pk) {
			return covRouter
		}
	}
	return common.NoAddress
}

func (p *covPubKeyMgr) GetContracts(_ common.Chain, _ bool) []common.Address {
	return []common.Address{covRouter}
}

func (p *covPubKeyMgr) IsValidPoolAddress(addr string, chain common.Chain) (bool, common.ChainPoolInfo) {
	for _, pk := range p.GetAlgoPubKeys(common.SigningAlgoSecp256k1, true) {
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

func (p *covPubKeyMgr) IsActivePoolAddress(addr string, chain common.Chain) (bool, common.ChainPoolInfo) {
	return p.IsValidPoolAddress(addr, chain)
}

// covPoolMgr doubles the amount, or errors for assets in failFor.
type covPoolMgr struct {
	failFor common.Asset
}

func (p covPoolMgr) GetValue(source, _ common.Asset, amount cosmos.Uint) (cosmos.Uint, error) {
	if source.Equals(p.failFor) {
		return cosmos.ZeroUint(), errors.New("no pool")
	}
	return amount.MulUint64(2), nil
}

// covEnv bundles a Client wired to fakes.
type covEnv struct {
	rpc       *covRPCServer
	bridge    *covBridge
	pkm       *covPubKeyMgr
	client    *Client
	keys      *thorclient.Keys
	pubKey    common.PubKey
	vaultAddr common.Address
	feeQ      chan common.NetworkFee
	errataQ   chan stypes.ErrataBlock
}

func covKeys(t *testing.T) *thorclient.Keys {
	t.Helper()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := cKeys.NewInMemory(cdc)
	mnemonic := "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"
	if _, err := kb.NewAccount("bob", mnemonic, "", cmd.THORChainHDPath, hd.Secp256k1); err != nil {
		t.Fatalf("new account: %v", err)
	}
	return thorclient.NewKeysWithKeybase(kb, "bob", "password")
}

func newCovEnv(t *testing.T, mod func(*config.BifrostChainConfiguration)) *covEnv {
	t.Helper()
	setupMocknetThorConfig()
	rpc := newCovRPCServer(t)
	bridge := newCovBridge()
	keys := covKeys(t)
	priv, err := keys.GetPrivateKey()
	if err != nil {
		t.Fatalf("priv: %v", err)
	}
	tmPub, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
	if err != nil {
		t.Fatalf("pub: %v", err)
	}
	pk, err := common.NewPubKeyFromCrypto(tmPub)
	if err != nil {
		t.Fatalf("pk: %v", err)
	}
	addr, err := pk.GetAddress(common.ETHChain)
	if err != nil {
		t.Fatalf("addr: %v", err)
	}
	pkm := &covPubKeyMgr{
		MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(),
		pubkeys:                  common.PubKeys{pk},
	}
	cfg := config.BifrostChainConfiguration{
		ChainID:          common.ETHChain,
		RPCHost:          rpc.srv.URL,
		MaxPendingNonces: 10,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:              common.ETHChain,
			StartBlockHeight:     1,
			HTTPRequestTimeout:   5 * time.Second,
			Concurrency:          1,
			GasCacheBlocks:       1,
			GasPriceResolution:   1,
			MaxGasLimit:          100_000,
			MaxReorgRescanBlocks: 0,
		},
	}
	cfg.EVM.ReconstructLiabilitiesConcurrency = 2
	cfg.EVM.MaxGasTipPercentage = 10
	if mod != nil {
		mod(&cfg)
	}
	c, err := NewClient(keys, cfg, nil, bridge, covMetrics(t), pkm, covPoolMgr{})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.signingReady.Store(true)
	feeQ := make(chan common.NetworkFee, 16)
	errataQ := make(chan stypes.ErrataBlock, 16)
	c.ethScanner.globalNetworkFeeQueue = feeQ
	c.ethScanner.globalErrataQueue = errataQ
	c.globalSolvencyQueue = make(chan stypes.Solvency, 16)
	t.Cleanup(func() { c.client.Close() })
	return &covEnv{
		rpc:       rpc,
		bridge:    bridge,
		pkm:       pkm,
		client:    c,
		keys:      keys,
		pubKey:    pk,
		vaultAddr: addr,
		feeQ:      feeQ,
		errataQ:   errataQ,
	}
}

// covMetrics shares the package metrics instance with the gocheck suites
// (prometheus collectors can only be registered once per process).
func covMetrics(t *testing.T) *metrics.Metrics {
	t.Helper()
	if m == nil {
		var err error
		m, err = metrics.NewMetrics(config.BifrostMetricsConfiguration{
			Enabled:      false,
			ListenPort:   9000,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
			Chains:       common.Chains{common.ETHChain},
		})
		if err != nil {
			t.Fatalf("metrics: %v", err)
		}
	}
	return m
}

// covSignedTx signs a legacy/dynamic tx locally with the env's key.
func (e *covEnv) covSignedTx(t *testing.T, tx *etypes.Transaction) *etypes.Transaction {
	t.Helper()
	raw, err := e.client.kw.Sign(tx, e.pubKey, 1)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	out := &etypes.Transaction{}
	if err = out.UnmarshalJSON(raw); err != nil {
		t.Fatalf("unmarshal signed: %v", err)
	}
	return out
}
