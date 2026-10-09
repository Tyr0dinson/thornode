package ethereum

import (
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const covToken = "0x3b7FA4dd21c6f9BA3ca375217EAD7CAb9D6bF483" // nolint gosec:G101 not a credential

// covNonces answers eth_getTransactionCount with latest/pending nonces.
func covNonces(latest, pending uint64) covRPCHandler {
	return func(p []json.RawMessage) (any, error) {
		if len(p) > 1 && strings.Contains(string(p[1]), "pending") {
			return covHex(pending), nil
		}
		return covHex(latest), nil
	}
}

// covKeyManager is a fake TSS key manager returning a fixed RemoteSign result.
type covKeyManager struct {
	tss.ThorchainKeyManager
	sig, rec []byte
	err      error
}

func (k covKeyManager) RemoteSign(_ []byte, _ common.SigningAlgo, _ string, _ int64) ([]byte, []byte, error) {
	return k.sig, k.rec, k.err
}

// useTSS swaps the key sign wrapper for one backed by a fake TSS key manager
// and registers a remote vault pubkey (not the local key) with the pubkey manager.
func (e *covEnv) useTSS(t *testing.T, km covKeyManager) common.PubKey {
	t.Helper()
	kw, err := evm.NewKeySignWrapper(e.client.kw.GetPrivKey(), e.pubKey, km, e.client.chainID, "ETH")
	if err != nil {
		t.Fatalf("kw: %v", err)
	}
	e.client.kw = kw
	remote := thortypes.GetRandomPubKey()
	e.pkm.mu.Lock()
	e.pkm.pubkeys = append(e.pkm.pubkeys, remote)
	e.pkm.mu.Unlock()
	return remote
}

func (e *covEnv) outbound(coin common.Coin, memo string) stypes.TxOutItem {
	return stypes.TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   thortypes.GetRandomETHAddress(),
		VaultPubKey: e.pubKey,
		Coins:       common.Coins{coin},
		Memo:        memo,
		MaxGas:      common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		GasRate:     30,
		InHash:      thortypes.GetRandomTxHash(),
	}
}

// happySignRPC configures the RPC fake so a plain ETH outbound can be signed.
func (e *covEnv) happySignRPC() {
	e.rpc.set("eth_getTransactionCount", covNonces(5, 5))
	e.rpc.set("eth_estimateGas", covResult(covHex(60_000)))
	e.rpc.set("eth_getBalance", covResult(covBigHex(new(big.Int).Mul(big.NewInt(100), big.NewInt(1e18)))))
	e.rpc.set("eth_blockNumber", covResult(covHex(16)))
	e.rpc.set("eth_sendRawTransaction", covResult("0x88df016429689c079f3b2f6ad39fa052532c56795b733da78a91ebe6a713944b"))
}

func ethCoin(amt uint64) common.Coin {
	return common.NewCoin(common.ETHAsset, cosmos.NewUint(amt))
}

func tokenCoin(t *testing.T, amt uint64) common.Coin {
	t.Helper()
	a, err := common.NewAsset("ETH.TKN-" + strings.ToUpper(covToken))
	if err != nil {
		t.Fatalf("asset: %v", err)
	}
	return common.NewCoin(a, cosmos.NewUint(amt))
}

func TestCovClientAccessors(t *testing.T) {
	env := newCovEnv(t, func(cfg *config.BifrostChainConfiguration) {
		cfg.BlockScanner.FixedGasRate = 3_000_000_000
	})
	c := env.client

	if got := c.GetConfig(); !got.ChainID.Equals(common.ETHChain) || got.BlockScanner.FixedGasRate != 3_000_000_000 {
		t.Fatalf("unexpected config: %+v", got)
	}
	if c.GetChain() != common.ETHChain {
		t.Fatalf("chain = %s", c.GetChain())
	}
	if c.IsBlockScannerHealthy() {
		t.Fatal("block scanner should not be healthy before it ran")
	}
	h, err := c.GetBlockScannerHeight()
	if err != nil || h != c.blockScanner.PreviousHeight() {
		t.Fatalf("height=%d err=%v", h, err)
	}
	// the fixed gas rate is used for fee estimation
	fee := c.GetGasFee(21_000)
	if !fee.Equals(common.GetEVMGasFee(common.ETHChain, big.NewInt(3_000_000_000), 21_000)) {
		t.Fatalf("gas fee = %s", fee)
	}
	if got := c.GetAddress(env.pubKey); !strings.EqualFold(got, env.vaultAddr.String()) {
		t.Fatalf("address = %s", got)
	}
	if got := c.GetAddress(common.PubKey("bogus")); got != "" {
		t.Fatalf("invalid pubkey should yield empty address, got %s", got)
	}

	// RollbackBlockScanner: no last observed height is a no-op, an error is surfaced
	if err = c.RollbackBlockScanner(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	env.bridge.lastObsErr = errors.New("boom")
	if err = c.RollbackBlockScanner(); err == nil {
		t.Fatal("expected rollback error")
	}

	// GetNetworkFee / FetchMemPool on the scanner
	c.ethScanner.lastReportedGasPrice.Store(7)
	size, rate := c.ethScanner.GetNetworkFee()
	if size != 100_000 || rate != 7 {
		t.Fatalf("network fee = %d/%d", size, rate)
	}
	mp, err := c.ethScanner.FetchMemPool(1)
	if err != nil || len(mp.TxArray) != 0 {
		t.Fatalf("mempool = %+v %v", mp, err)
	}
}

func TestCovStopClosesResources(t *testing.T) {
	env := newCovEnv(t, nil)
	env.client.Stop()
	select {
	case <-env.client.stopchan:
	default:
		t.Fatal("stopchan should be closed after Stop")
	}
	if _, err := env.client.GetNonce(env.vaultAddr.String()); err == nil {
		t.Fatal("rpc client should be closed after Stop")
	}
}

func TestCovNewClientValidation(t *testing.T) {
	setupMocknetThorConfig()
	keys := covKeys(t)
	bridge := newCovBridge()
	pkm := &covPubKeyMgr{}

	// bad RPC host fails to dial
	_, err := NewClient(keys, config.BifrostChainConfiguration{RPCHost: "foo://bar"}, nil, bridge, covMetrics(t), pkm, covPoolMgr{})
	if err == nil || !strings.Contains(err.Error(), "fail to dial") {
		t.Fatalf("expected dial error, got %v", err)
	}

	// chain id RPC error
	rpc := newCovRPCServer(t)
	rpc.set("eth_chainId", covErr("nope"))
	_, err = NewClient(keys, config.BifrostChainConfiguration{RPCHost: rpc.srv.URL}, nil, bridge, covMetrics(t), pkm, covPoolMgr{})
	if err == nil || !strings.Contains(err.Error(), "fail to get chain id") {
		t.Fatalf("expected chain id error, got %v", err)
	}

	// chain id zero is rejected
	rpc.set("eth_chainId", covResult("0x0"))
	zeroCfg := config.BifrostChainConfiguration{RPCHost: rpc.srv.URL}
	zeroCfg.BlockScanner.HTTPRequestTimeout = 5 * time.Second
	_, err = NewClient(keys, zeroCfg, nil, bridge, covMetrics(t), pkm, covPoolMgr{})
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("expected invalid chain id error, got %v", err)
	}

	// persistent storage path
	rpc.set("eth_chainId", covResult("0x539"))
	cfg := config.BifrostChainConfiguration{RPCHost: rpc.srv.URL}
	cfg.BlockScanner.HTTPRequestTimeout = 5 * time.Second
	cfg.BlockScanner.DBPath = t.TempDir()
	cfg.BlockScanner.ChainID = common.ETHChain
	cfg.BlockScanner.StartBlockHeight = 1
	c, err := NewClient(keys, cfg, nil, bridge, covMetrics(t), pkm, covPoolMgr{})
	if err != nil {
		t.Fatalf("new client with db path: %v", err)
	}
	if c.chainID.Int64() != covChainID {
		t.Fatalf("chain id = %s", c.chainID)
	}
	c.client.Close()
}

func TestCovGetLatestTxForVaultAndBroadcast(t *testing.T) {
	env := newCovEnv(t, nil)
	env.happySignRPC()
	c := env.client
	vault := env.pubKey.String()

	if _, _, err := c.GetLatestTxForVault(vault); err == nil {
		t.Fatal("expected error when nothing recorded")
	}

	// observed outbound records the inbound key
	inHash := thortypes.GetRandomTxHash()
	txIn := stypes.TxInItem{
		Tx:                  "abc123",
		Memo:                "OUT:" + inHash.String(),
		To:                  thortypes.GetRandomETHAddress().String(),
		Coins:               common.Coins{ethCoin(100)},
		ObservedVaultPubKey: env.pubKey,
	}
	c.OnObservedTxIn(txIn, 5)
	if !c.signerCacheManager.HasSigned(txIn.CacheHash(common.ETHChain, inHash.String())) {
		t.Fatal("outbound observation should mark signer cache")
	}

	// sign + broadcast records the broadcast key and a signed tx item
	toi := env.outbound(ethCoin(common.One), "OUT:"+inHash.String())
	raw, _, _, err := c.SignTx(toi, 10)
	if err != nil || raw == nil {
		t.Fatalf("sign: %v", err)
	}
	txID, err := c.BroadcastTx(toi, raw)
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if !c.signerCacheManager.HasSigned(toi.CacheHash()) {
		t.Fatal("broadcast should mark tx out item as signed")
	}
	items, err := c.ethScanner.blockMetaAccessor.GetSignedTxItems()
	if err != nil || len(items) != 1 || items[0].Hash != txID || items[0].Height != 1000 {
		t.Fatalf("signed items = %+v err=%v", items, err)
	}
	obs, bc, err := c.GetLatestTxForVault(vault)
	if err != nil || obs != "abc123" || bc != txID {
		t.Fatalf("latest = %s/%s err=%v", obs, bc, err)
	}

	// broadcast errors other than already-known are returned
	env.rpc.set("eth_sendRawTransaction", covErr("insufficient funds"))
	if _, err = c.BroadcastTx(toi, raw); err == nil {
		t.Fatal("expected broadcast error")
	}
	// already known is tolerated, thorchain height error does not fail broadcast
	env.rpc.set("eth_sendRawTransaction", covErr("already known"))
	env.bridge.heightErr = errors.New("no height")
	if _, err = c.BroadcastTx(toi, raw); err != nil {
		t.Fatalf("already known should be tolerated: %v", err)
	}
	// malformed tx bytes
	if _, err = c.BroadcastTx(toi, []byte("not json")); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestCovOnObservedTxInIgnoredMemos(t *testing.T) {
	env := newCovEnv(t, nil)
	c := env.client
	// block meta exists so the scanner records the tx
	header := covHeader(7, ecommon.Hash{}, false)
	c.ethScanner.updateCurrentBlockHeight(7)
	if err := c.ethScanner.blockMetaAccessor.SaveBlockMeta(7, evmtypes.NewBlockMeta(header, stypes.TxIn{})); err != nil {
		t.Fatal(err)
	}
	for _, memo := range []string{"not a memo !!", "=:ETH.ETH:" + thortypes.GetRandomETHAddress().String(), "OUT:"} {
		item := stypes.TxInItem{Tx: "tx-" + memo, Memo: memo, ObservedVaultPubKey: env.pubKey}
		c.OnObservedTxIn(item, 7)
	}
	meta, err := c.ethScanner.blockMetaAccessor.GetBlockMeta(7)
	if err != nil || meta == nil {
		t.Fatalf("meta: %v", err)
	}
	if len(meta.Transactions) != 3 {
		t.Fatalf("expected 3 recorded txs, got %d", len(meta.Transactions))
	}
	// duplicate observation does not duplicate meta
	c.OnObservedTxIn(stypes.TxInItem{Tx: "tx-not a memo !!", Memo: "x"}, 7)
	meta, _ = c.ethScanner.blockMetaAccessor.GetBlockMeta(7)
	if len(meta.Transactions) != 3 {
		t.Fatalf("duplicate should be ignored, got %d", len(meta.Transactions))
	}
	// missing block meta is a no-op
	c.OnObservedTxIn(stypes.TxInItem{Tx: "other", Memo: "x"}, 99)
	meta, _ = c.ethScanner.blockMetaAccessor.GetBlockMeta(99)
	if meta != nil {
		t.Fatalf("no meta should be created, got %+v", meta)
	}
}

func TestCovConfirmationCountReady(t *testing.T) {
	env := newCovEnv(t, nil)
	c := env.client
	if !c.ConfirmationCountReady(stypes.TxIn{}) {
		t.Fatal("empty txIn should be ready")
	}
	if !c.ConfirmationCountReady(stypes.TxIn{MemPool: true, TxArray: []*stypes.TxInItem{{BlockHeight: 1}}}) {
		t.Fatal("mempool txIn should be ready")
	}
	c.ethScanner.updateCurrentBlockHeight(100)
	txIn := stypes.TxIn{TxArray: []*stypes.TxInItem{{BlockHeight: 95}}, ConfirmationRequired: 5}
	if !c.ConfirmationCountReady(txIn) {
		t.Fatal("5 confirmations should be ready")
	}
	txIn.ConfirmationRequired = 6
	if c.ConfirmationCountReady(txIn) {
		t.Fatal("6 confirmations should not be ready")
	}
	if got := c.GetConfirmationCount(stypes.TxIn{MemPool: true, TxArray: []*stypes.TxInItem{{}}}); got != 0 {
		t.Fatalf("mempool confirmation count = %d", got)
	}
}

func TestCovGetTotalTransactionValue(t *testing.T) {
	env := newCovEnv(t, nil)
	c := env.client
	tkn := tokenCoin(t, 50)
	failAsset, _ := common.NewAsset("ETH.BAD-0X1111111111111111111111111111111111111111")
	c.poolMgr = covPoolMgr{failFor: failAsset}
	asgard := common.Address(env.vaultAddr.String())
	txIn := stypes.TxIn{TxArray: []*stypes.TxInItem{
		{Sender: strings.ToUpper(asgard.String()), Coins: common.Coins{ethCoin(1000)}}, // excluded (case-insensitive)
		{Sender: "0xabc", Coins: common.Coins{ethCoin(10), tkn, common.NewCoin(failAsset, cosmos.NewUint(7)), common.NewCoin(common.ETHAsset, cosmos.ZeroUint())}},
	}}
	got := c.getTotalTransactionValue(txIn, []common.Address{asgard})
	// 10 ETH + 2*50 token value; failing pool and empty coin skipped
	if !got.Equal(cosmos.NewUint(110)) {
		t.Fatalf("total = %s", got)
	}
	if !c.getTotalTransactionValue(stypes.TxIn{}, nil).IsZero() {
		t.Fatal("empty txIn should be zero")
	}
}

func TestCovGetBalance(t *testing.T) {
	env := newCovEnv(t, nil)
	c := env.client
	addr := env.vaultAddr.String()

	env.rpc.set("eth_getBalance", func(p []json.RawMessage) (any, error) {
		if strings.Contains(string(p[1]), "pending") {
			return covHex(7), nil
		}
		return covHex(9), nil
	})
	bal, err := c.GetBalance(addr, ethToken, big.NewInt(-1), "")
	if err != nil || bal.Int64() != 7 {
		t.Fatalf("pending balance = %v err=%v", bal, err)
	}
	bal, err = c.GetBalance(addr, ethToken, nil, "")
	if err != nil || bal.Int64() != 9 {
		t.Fatalf("latest balance = %v err=%v", bal, err)
	}

	// token without router contract
	if _, err = c.GetBalance(addr, covToken, nil, ""); err == nil {
		t.Fatal("expected missing contract error")
	}
	// token vaultAllowance
	env.rpc.set("eth_call", covResult(covUint256(big.NewInt(12345))))
	bal, err = c.GetBalance(addr, covToken, nil, covRouter.String())
	if err != nil || bal.Int64() != 12345 {
		t.Fatalf("token balance = %v err=%v", bal, err)
	}
	// call error
	env.rpc.set("eth_call", covErr("reverted"))
	if _, err = c.GetBalance(addr, covToken, nil, covRouter.String()); err == nil {
		t.Fatal("expected call error")
	}
	// undecodable output
	env.rpc.set("eth_call", covResult("0x"))
	if _, err = c.GetBalance(addr, covToken, nil, covRouter.String()); err == nil {
		t.Fatal("expected unpack error")
	}
}

func TestCovAccounts(t *testing.T) {
	env := newCovEnv(t, nil)
	c := env.client
	if err := c.ethScanner.tokens.SaveTokenMeta("TKN", covToken, 6); err != nil {
		t.Fatal(err)
	}
	env.rpc.set("eth_getTransactionCount", covNonces(3, 4))
	env.rpc.set("eth_getBalance", covResult(covBigHex(big.NewInt(2e18))))
	// 5 tokens with 6 decimals
	env.rpc.set("eth_call", covResult(covUint256(big.NewInt(5_000_000))))

	tkn := tokenCoin(t, 1)
	vault := thortypes.Vault{
		PubKey: env.pubKey,
		Coins:  common.Coins{ethCoin(1), tkn, tkn, common.NewCoin(common.BTCAsset, cosmos.NewUint(1))},
	}
	acct, err := c.GetVaultAccount(vault, nil)
	if err != nil {
		t.Fatalf("vault account: %v", err)
	}
	if acct.Sequence != 4 {
		t.Fatalf("nonce = %d", acct.Sequence)
	}
	if len(acct.Coins) != 2 {
		t.Fatalf("coins = %s", acct.Coins)
	}
	if !acct.Coins.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(2 * common.One)) {
		t.Fatalf("eth = %s", acct.Coins)
	}
	if !acct.Coins.GetCoin(tkn.Asset).Amount.Equal(cosmos.NewUint(5 * common.One)) {
		t.Fatalf("token = %s", acct.Coins)
	}

	acct, err = c.GetAccountByAddress(env.vaultAddr.String(), nil)
	if err != nil || acct.Sequence != 4 || len(acct.Coins) != 2 {
		t.Fatalf("account by address = %+v err=%v", acct, err)
	}

	// failing token balance is skipped
	env.rpc.set("eth_call", covErr("reverted"))
	acct, err = c.GetAccount(env.pubKey, nil)
	if err != nil || len(acct.Coins) != 1 {
		t.Fatalf("account = %+v err=%v", acct, err)
	}
	acct, err = c.GetVaultAccount(vault, nil)
	if err != nil || len(acct.Coins) != 1 {
		t.Fatalf("vault account w/ failing token = %+v err=%v", acct, err)
	}

	// nonce error is surfaced
	env.rpc.set("eth_getTransactionCount", covErr("down"))
	if _, err = c.GetVaultAccount(vault, nil); err == nil {
		t.Fatal("expected nonce error")
	}
	if _, err = c.GetAccount(env.pubKey, nil); err == nil {
		t.Fatal("expected nonce error")
	}
	if _, err = c.GetAccountByAddress(env.vaultAddr.String(), nil); err == nil {
		t.Fatal("expected nonce error")
	}
	if _, err = c.GetNonce(env.vaultAddr.String()); err == nil {
		t.Fatal("expected nonce error")
	}
}

func TestCovGetSmartContractByAddress(t *testing.T) {
	env := newCovEnv(t, nil)
	c := env.client
	if got := c.getSmartContractByAddress(common.Address(env.vaultAddr.String())); !got.Equals(covRouter) {
		t.Fatalf("router = %s", got)
	}
	if got := c.getSmartContractByAddress(thortypes.GetRandomETHAddress()); !got.IsEmpty() {
		t.Fatalf("unknown address should have no router, got %s", got)
	}
	// pubkeys that fail address derivation are skipped
	env.pkm.pubkeys = common.PubKeys{common.PubKey("bogus"), env.pubKey}
	if got := c.getSmartContractByAddress(common.Address(env.vaultAddr.String())); !got.Equals(covRouter) {
		t.Fatalf("router = %s", got)
	}
}

func TestCovReconstructVaultLiability(t *testing.T) {
	env := newCovEnv(t, nil)
	c := env.client
	vault := env.vaultAddr.String()

	// no pending txs
	env.rpc.set("eth_getTransactionCount", covNonces(4, 4))
	ready, err := c.reconstructVaultLiability(vault)
	if err != nil || !ready {
		t.Fatalf("ready=%v err=%v", ready, err)
	}

	// pending txs found in the pending block
	to := ecommon.HexToAddress(covRouter.String())
	mine := env.covSignedTx(t, etypes.NewTx(&etypes.DynamicFeeTx{
		ChainID: c.chainID, Nonce: 4, To: &to, Value: big.NewInt(1000), Gas: 21000,
		GasFeeCap: big.NewInt(10), GasTipCap: big.NewInt(1),
	}))
	stale := env.covSignedTx(t, etypes.NewTx(&etypes.DynamicFeeTx{
		ChainID: c.chainID, Nonce: 1, To: &to, Value: big.NewInt(5), Gas: 21000,
		GasFeeCap: big.NewInt(10), GasTipCap: big.NewInt(1),
	}))
	otherKey, _ := crypto.GenerateKey()
	other, err := etypes.SignTx(etypes.NewTx(&etypes.DynamicFeeTx{
		ChainID: c.chainID, Nonce: 4, To: &to, Value: big.NewInt(5), Gas: 21000,
		GasFeeCap: big.NewInt(10), GasTipCap: big.NewInt(1),
	}), etypes.LatestSignerForChainID(c.chainID), otherKey)
	if err != nil {
		t.Fatal(err)
	}
	block := covBlockJSON(t, covHeader(20, ecommon.Hash{}, true), []*etypes.Transaction{other, stale, mine})
	env.rpc.set("eth_getBlockByNumber", covResult(block))
	env.rpc.set("eth_getTransactionCount", covNonces(4, 5))
	ready, err = c.reconstructVaultLiability(vault)
	if err != nil || !ready {
		t.Fatalf("ready=%v err=%v", ready, err)
	}
	// liability = value + gasFeeCap*gas = 1000 + 210000
	if got := c.getPendingLiability(vault, 4); got.Int64() != 211_000 {
		t.Fatalf("liability = %s", got)
	}

	// more pending than visible -> not ready
	env.rpc.set("eth_getTransactionCount", covNonces(4, 7))
	ready, err = c.reconstructVaultLiability(vault)
	if err != nil || ready {
		t.Fatalf("expected not ready, ready=%v err=%v", ready, err)
	}

	// rpc failures
	env.rpc.set("eth_getBlockByNumber", covErr("no block"))
	if _, err = c.reconstructVaultLiability(vault); err == nil || !strings.Contains(err.Error(), "pending block") {
		t.Fatalf("expected pending block error, got %v", err)
	}
	env.rpc.set("eth_getTransactionCount", func(p []json.RawMessage) (any, error) {
		if strings.Contains(string(p[1]), "pending") {
			return nil, errors.New("down")
		}
		return covHex(1), nil
	})
	if _, err = c.reconstructVaultLiability(vault); err == nil || !strings.Contains(err.Error(), "pending nonce") {
		t.Fatalf("expected pending nonce error, got %v", err)
	}
	env.rpc.set("eth_getTransactionCount", covErr("down"))
	if _, err = c.reconstructVaultLiability(vault); err == nil || !strings.Contains(err.Error(), "confirmed nonce") {
		t.Fatalf("expected confirmed nonce error, got %v", err)
	}
}

func TestCovReconstructLiabilities(t *testing.T) {
	// no vaults -> ready immediately
	env := newCovEnv(t, nil)
	c := env.client
	c.signingReady.Store(false)
	env.pkm.pubkeys = common.PubKeys{common.PubKey("bogus")}
	c.wg.Add(1)
	c.reconstructLiabilities()
	if !c.signingReady.Load() {
		t.Fatal("expected signing ready with no vaults")
	}

	// vault with no pending txs -> ready
	env.pkm.pubkeys = common.PubKeys{env.pubKey}
	env.rpc.set("eth_getTransactionCount", covNonces(2, 2))
	c.signingReady.Store(false)
	c.wg.Add(1)
	c.reconstructLiabilities()
	if !c.signingReady.Load() {
		t.Fatal("expected signing ready")
	}

	// rpc error while stopping: stop is honoured and signing stays disabled
	c.signingReady.Store(false)
	env.rpc.set("eth_getTransactionCount", func([]json.RawMessage) (any, error) {
		select {
		case <-c.stopchan:
		default:
			close(c.stopchan)
		}
		return nil, errors.New("down")
	})
	c.wg.Add(1)
	c.reconstructLiabilities()
	if c.signingReady.Load() {
		t.Fatal("signing must not be ready after stop")
	}
}

func TestCovSignTxValidation(t *testing.T) {
	env := newCovEnv(t, nil)
	env.happySignRPC()
	c := env.client
	inHash := thortypes.GetRandomTxHash().String()
	base := env.outbound(ethCoin(common.One), "OUT:"+inHash)

	expectErr := func(name string, toi stypes.TxOutItem, contains string) {
		t.Helper()
		_, _, _, err := c.SignTx(toi, 1)
		if err == nil || !strings.Contains(err.Error(), contains) {
			t.Fatalf("%s: expected error containing %q, got %v", name, contains, err)
		}
	}
	expectSkip := func(name string, toi stypes.TxOutItem) {
		t.Helper()
		raw, cp, in, err := c.SignTx(toi, 1)
		if err != nil || raw != nil || cp != nil || in != nil {
			t.Fatalf("%s: expected silent skip, got raw=%v cp=%s in=%v err=%v", name, raw != nil, cp, in, err)
		}
	}

	c.signingReady.Store(false)
	expectErr("not ready", base, "still initializing")
	c.signingReady.Store(true)

	toi := base
	toi.Chain = common.BTCChain
	expectErr("chain", toi, "not support")

	toi = base
	toi.ToAddress = ""
	expectErr("to", toi, "to address is empty")

	toi = base
	toi.VaultPubKey = ""
	expectErr("vault", toi, "vault public key is empty")

	toi = base
	toi.Coins = nil
	expectErr("coins", toi, "coins is empty")

	toi = base
	toi.Memo = "OUT:nothex"
	expectErr("memo", toi, "fail to parse memo")

	toi = base
	toi.Memo = "=:ETH.ETH:" + thortypes.GetRandomETHAddress().String()
	expectErr("inbound memo", toi, "inbound memo")

	toi = base
	toi.VaultPubKey = thortypes.GetRandomPubKey()
	expectErr("no router", toi, "smart contract address")

	toi = base
	toi.Memo = "REFUND:" + inHash
	toi.Aggregator = "0x4ac1d98d9cef99ec6546ded4bd550b0b287aad6d"
	expectErr("refund aggregator", toi, "can't use transferOutAndCall")

	toi = base
	toi.Coins = common.Coins{tokenCoin(t, 100)}
	toi.Aggregator = "0x4ac1d98d9cef99ec6546ded4bd550b0b287aad6d"
	expectErr("aggregator token", toi, "only be used when outbound asset is ETH")

	toi = base
	toi.Aggregator = "0x1234"
	expectSkip("aggregator roundtrip", toi)

	toi = base
	toi.Aggregator = "0x4ac1d98d9cef99ec6546ded4bd550b0b287aad6d"
	toi.AggregatorTargetAsset = "0x99"
	expectSkip("target roundtrip", toi)

	toi = base
	toi.Memo = "MIGRATE:10"
	toi.Aggregator = "0x4ac1d98d9cef99ec6546ded4bd550b0b287aad6d"
	expectErr("migrate aggregator", toi, "migration can't use aggregator")

	toi = base
	toi.Memo = "MIGRATE:10"
	toi.Coins = common.Coins{tokenCoin(t, 100)}
	expectErr("migrate unknown vault", toi, "new smart contract address")

	toi = base
	toi.Checkpoint = []byte("not-a-number")
	expectErr("checkpoint", toi, "deserialize checkpoint")

	env.rpc.set("eth_getTransactionCount", covNonces(1, 20))
	expectErr("pending nonce", base, "too far in future")

	env.rpc.set("eth_getTransactionCount", func(p []json.RawMessage) (any, error) {
		if strings.Contains(string(p[1]), "pending") {
			return covHex(1), nil
		}
		return nil, errors.New("down")
	})
	expectErr("finalized nonce", base, "finalized nonce")

	env.rpc.set("eth_getTransactionCount", covErr("down"))
	expectErr("nonce", base, "nonce")

	env.rpc.set("eth_getTransactionCount", covNonces(5, 5))
	env.rpc.set("eth_estimateGas", covErr("execution reverted"))
	expectSkip("estimate gas", base)

	// aggregator estimated gas over its limit
	env.rpc.set("eth_estimateGas", covResult(covHex(500_000)))
	toi = base
	toi.Aggregator = "0x4ac1d98d9cef99ec6546ded4bd550b0b287aad6d"
	toi.AggregatorTargetAsset = "0x4ac1d98d9cef99ec6546ded4bd550b0b287aad6d"
	expectSkip("aggregator gas limit", toi)

	// unknown aggregator gas limit
	toi.Aggregator = "0x1111111111111111111111111111111111111111"
	expectSkip("unknown aggregator", toi)

	// max gas exceeded
	env.rpc.set("eth_estimateGas", covResult(covHex(60_000)))
	toi = base
	toi.MaxGas = common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))}
	expectSkip("max gas", toi)

	// insufficient balance
	env.rpc.set("eth_getBalance", covResult("0x1"))
	expectErr("balance", base, "insufficient gas asset balance")
	env.rpc.set("eth_getBalance", covErr("down"))
	expectErr("balance rpc", base, "gas asset balance")
}

func TestCovSignTxPaths(t *testing.T) {
	env := newCovEnv(t, nil)
	env.happySignRPC()
	c := env.client
	inHash := thortypes.GetRandomTxHash().String()

	// aggregator outbound within the gas limit
	toi := env.outbound(ethCoin(common.One), "OUT:"+inHash)
	toi.Aggregator = "0x4ac1d98d9cef99ec6546ded4bd550b0b287aad6d"
	toi.AggregatorTargetAsset = "0x4ac1d98d9cef99ec6546ded4bd550b0b287aad6d"
	raw, cp, txIn, err := c.SignTx(toi, 1)
	if err != nil || raw == nil || string(cp) != "5" || txIn == nil {
		t.Fatalf("aggregator sign: raw=%v cp=%s txIn=%v err=%v", raw != nil, cp, txIn, err)
	}
	signed := &etypes.Transaction{}
	if err = signed.UnmarshalJSON(raw); err != nil {
		t.Fatal(err)
	}
	// gas limit derives from the aggregator-multiplied max gas at the 20 gwei rate
	wantGas := uint64(1e18 / 2e10 * constants.EVMAggregatorMaxGasMultiplier)
	transferOutAndCall := c.vaultABI.Methods["transferOutAndCall"].ID
	if signed.Gas() != wantGas || signed.Nonce() != 5 || !strings.HasPrefix(string(signed.Data()), string(transferOutAndCall)) {
		t.Fatalf("aggregator tx gas=%d nonce=%d", signed.Gas(), signed.Nonce())
	}
	if txIn.BlockHeight != 16 || !strings.EqualFold(txIn.Sender, env.vaultAddr.String()) {
		t.Fatalf("txIn = %+v", txIn)
	}
	if got := c.getPendingLiability(env.vaultAddr.String(), 5); got.Sign() <= 0 {
		t.Fatalf("liability should be recorded, got %s", got)
	}

	// migrate token to another vault uses transferAllowance; checkpoint nonce reused
	other := thortypes.GetRandomPubKey()
	env.pkm.pubkeys = append(env.pkm.pubkeys, other)
	otherAddr, _ := other.GetAddress(common.ETHChain)
	if err = c.ethScanner.tokens.SaveTokenMeta("TKN", covToken, 18); err != nil {
		t.Fatal(err)
	}
	toi = env.outbound(tokenCoin(t, 100), "MIGRATE:10")
	toi.ToAddress = otherAddr
	toi.Checkpoint = []byte("9")
	env.rpc.set("eth_blockNumber", covErr("down"))
	c.ethScanner.updateCurrentBlockHeight(33)
	raw, cp, txIn, err = c.SignTx(toi, 1)
	if err != nil || raw == nil || string(cp) != "9" {
		t.Fatalf("migrate sign: err=%v cp=%s", err, cp)
	}
	if err = signed.UnmarshalJSON(raw); err != nil {
		t.Fatal(err)
	}
	transferAllowance := c.vaultABI.Methods["transferAllowance"].ID
	if signed.Nonce() != 9 || !strings.HasPrefix(string(signed.Data()), string(transferAllowance)) {
		t.Fatalf("migrate tx nonce=%d data=%x", signed.Nonce(), signed.Data()[:4])
	}
	if txIn.BlockHeight != 33 {
		t.Fatalf("expected scanner height fallback, got %d", txIn.BlockHeight)
	}

	// migrate ETH uses transferOut
	toi = env.outbound(ethCoin(common.One), "MIGRATE:10")
	toi.ToAddress = otherAddr
	raw, _, _, err = c.SignTx(toi, 1)
	if err != nil || raw == nil {
		t.Fatalf("migrate eth: %v", err)
	}

	// gas price lower bound: scanner price far below the outbound rate
	c.ethScanner.gasPrice.Store(big.NewInt(1))
	toi = env.outbound(ethCoin(common.One), "OUT:"+inHash)
	toi.GasRate = 90
	raw, _, _, err = c.SignTx(toi, 1)
	if err != nil {
		t.Fatalf("lower bound sign: %v", err)
	}
	if err = signed.UnmarshalJSON(raw); err != nil {
		t.Fatal(err)
	}
	// 2/3 of 90 gwei
	if signed.GasFeeCap().Cmp(big.NewInt(60_000_000_000)) != 0 {
		t.Fatalf("gas fee cap = %s", signed.GasFeeCap())
	}
}

func TestCovSignTxFixedGasRate(t *testing.T) {
	env := newCovEnv(t, func(cfg *config.BifrostChainConfiguration) {
		cfg.BlockScanner.FixedGasRate = 10_000_000_000
	})
	env.happySignRPC()
	c := env.client
	toi := env.outbound(ethCoin(common.One), "OUT:"+thortypes.GetRandomTxHash().String())
	raw, _, txIn, err := c.SignTx(toi, 1)
	if err != nil || raw == nil || txIn == nil {
		t.Fatalf("fixed rate sign: %v", err)
	}
	signed := &etypes.Transaction{}
	if err = signed.UnmarshalJSON(raw); err != nil {
		t.Fatal(err)
	}
	if signed.Type() != etypes.LegacyTxType || signed.Gas() != 60_000 {
		t.Fatalf("type=%d gas=%d", signed.Type(), signed.Gas())
	}
	// over scheduled max fee
	toi.MaxGas = common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))}
	raw, cp, in, err := c.SignTx(toi, 1)
	if err != nil || raw != nil || cp != nil || in != nil {
		t.Fatalf("expected silent skip, err=%v", err)
	}
}

func TestCovSignTxTSS(t *testing.T) {
	env := newCovEnv(t, nil)
	env.happySignRPC()
	c := env.client

	remote := env.useTSS(t, covKeyManager{err: tss.ErrNotSelectedForKeysign})
	toi := env.outbound(ethCoin(common.One), "OUT:"+thortypes.GetRandomTxHash().String())
	toi.VaultPubKey = remote
	raw, cp, in, err := c.SignTx(toi, 1)
	if err != nil || raw != nil || cp != nil || in != nil {
		t.Fatalf("not selected should skip silently: err=%v", err)
	}

	env.useTSS(t, covKeyManager{err: errors.New("tss down")})
	raw, cp, _, err = c.SignTx(toi, 1)
	if err == nil || raw != nil || string(cp) != "5" {
		t.Fatalf("expected sign error with checkpoint, err=%v cp=%s", err, cp)
	}
}

func TestCovSign(t *testing.T) {
	env := newCovEnv(t, nil)
	c := env.client
	to := ecommon.HexToAddress(covRouter.String())
	tx := etypes.NewTransaction(1, to, big.NewInt(0), 21000, big.NewInt(1), nil)
	toi := env.outbound(ethCoin(1), "OUT:"+thortypes.GetRandomTxHash().String())

	// local key signs directly
	raw, err := c.sign(tx, env.pubKey, 1, toi)
	if err != nil || len(raw) == 0 {
		t.Fatalf("local sign: %v", err)
	}

	remote := env.useTSS(t, covKeyManager{err: tss.ErrNotSelectedForKeysign})
	if _, err = c.sign(tx, remote, 1, toi); !errors.Is(err, tss.ErrNotSelectedForKeysign) {
		t.Fatalf("expected not selected, got %v", err)
	}

	// keysign failure without blame nodes is not posted
	env.useTSS(t, covKeyManager{err: tss.NewKeysignError(thortypes.Blame{FailReason: "x"})})
	if _, err = c.sign(tx, remote, 1, toi); err == nil {
		t.Fatal("expected error")
	}
	if env.bridge.keysignCalls != 0 {
		t.Fatalf("keysign failure should not be posted, calls=%d", env.bridge.keysignCalls)
	}

	// keysign failure with blame is posted to thorchain
	blame := thortypes.Blame{FailReason: "x", BlameNodes: []thortypes.Node{{Pubkey: "pk"}}}
	env.useTSS(t, covKeyManager{err: tss.NewKeysignError(blame)})
	if _, err = c.sign(tx, remote, 1, toi); err == nil {
		t.Fatal("expected error")
	}
	if env.bridge.keysignCalls != 1 {
		t.Fatalf("keysign failure should be posted once, calls=%d", env.bridge.keysignCalls)
	}

	// posting the failure fails too -> both errors returned
	env.bridge.keysignErr = errors.New("post failed")
	_, err = c.sign(tx, remote, 1, toi)
	if err == nil || !strings.Contains(err.Error(), "post failed") {
		t.Fatalf("expected combined error, got %v", err)
	}

	// generic error
	env.useTSS(t, covKeyManager{err: errors.New("generic")})
	if _, err = c.sign(tx, remote, 1, toi); err == nil || !strings.Contains(err.Error(), "generic") {
		t.Fatalf("expected generic error, got %v", err)
	}
}

func TestCovCheckAndApproveAllowance(t *testing.T) {
	env := newCovEnv(t, nil)
	c := env.client
	key := strings.ReplaceAll(constants.MimirTemplateEVMAllowanceCheck, "%s", "ETH")
	tkn := tokenCoin(t, 100)
	if err := c.ethScanner.tokens.SaveTokenMeta("TKN", covToken, 18); err != nil {
		t.Fatal(err)
	}
	toi := env.outbound(tkn, "OUT:"+thortypes.GetRandomTxHash().String())

	// mimir lookup failure -> skip
	env.bridge.mimirErr = errors.New("down")
	tx, err := c.checkAndApproveAllowance(toi, 1)
	if tx != nil || err != nil {
		t.Fatalf("mimir error should skip, tx=%v err=%v", tx, err)
	}
	env.bridge.mimirErr = nil

	// disabled -> skip
	tx, err = c.checkAndApproveAllowance(toi, 1)
	if tx != nil || err != nil {
		t.Fatalf("disabled should skip")
	}
	env.bridge.setMimir(key, 1)

	// no coins / multiple coins / native
	noCoins := toi
	noCoins.Coins = nil
	if tx, err = c.checkAndApproveAllowance(noCoins, 1); tx != nil || err != nil {
		t.Fatal("no coins should skip")
	}
	multi := toi
	multi.Coins = common.Coins{tkn, ethCoin(1)}
	if _, err = c.checkAndApproveAllowance(multi, 1); err == nil {
		t.Fatal("multiple coins should error")
	}
	native := env.outbound(ethCoin(1), toi.Memo)
	if tx, err = c.checkAndApproveAllowance(native, 1); tx != nil || err != nil {
		t.Fatal("native should skip")
	}

	// vault without router
	noRouter := toi
	noRouter.VaultPubKey = thortypes.GetRandomPubKey()
	if _, err = c.checkAndApproveAllowance(noRouter, 1); err == nil || !strings.Contains(err.Error(), "no router") {
		t.Fatalf("expected router error, got %v", err)
	}

	// allowance call failure / bad output
	env.rpc.set("eth_call", covErr("reverted"))
	if _, err = c.checkAndApproveAllowance(toi, 1); err == nil {
		t.Fatal("expected call error")
	}
	env.rpc.set("eth_call", covResult("0x"))
	if _, err = c.checkAndApproveAllowance(toi, 1); err == nil {
		t.Fatal("expected unpack error")
	}

	// sufficient allowance
	env.rpc.set("eth_call", covResult(covUint256(new(big.Int).Mul(big.NewInt(1e18), big.NewInt(1e6)))))
	if tx, err = c.checkAndApproveAllowance(toi, 1); tx != nil || err != nil {
		t.Fatalf("sufficient allowance should skip, err=%v", err)
	}

	// insufficient allowance -> approval tx with lower-bounded gas price
	env.rpc.set("eth_call", covResult(covUint256(big.NewInt(1))))
	c.ethScanner.gasPrice.Store(big.NewInt(1))
	tx, err = c.checkAndApproveAllowance(toi, 7)
	if err != nil || tx == nil {
		t.Fatalf("expected approval tx, err=%v", err)
	}
	if tx.Nonce() != 7 || !strings.EqualFold(tx.To().Hex(), covToken) || tx.Gas() != 100_000 {
		t.Fatalf("approval tx nonce=%d to=%s gas=%d", tx.Nonce(), tx.To().Hex(), tx.Gas())
	}
	// 2/3 of 30 gwei
	if tx.GasPrice().Cmp(big.NewInt(20_000_000_000)) != 0 {
		t.Fatalf("approval gas price = %s", tx.GasPrice())
	}
	approve := c.ethScanner.erc20ABI.Methods["approve"].ID
	if !strings.HasPrefix(string(tx.Data()), string(approve)) {
		t.Fatalf("approval data = %x", tx.Data())
	}

	// higher scanner price is rounded down to 10 gwei and used
	c.ethScanner.gasPrice.Store(big.NewInt(55_123_456_789))
	tx, err = c.checkAndApproveAllowance(toi, 7)
	if err != nil || tx.GasPrice().Cmp(big.NewInt(50_000_000_000)) != 0 {
		t.Fatalf("approval gas price = %v err=%v", tx.GasPrice(), err)
	}
}

func TestCovSignTxApprovalBroadcastFailure(t *testing.T) {
	env := newCovEnv(t, nil)
	env.happySignRPC()
	c := env.client
	env.bridge.setMimir(strings.ReplaceAll(constants.MimirTemplateEVMAllowanceCheck, "%s", "ETH"), 1)
	if err := c.ethScanner.tokens.SaveTokenMeta("TKN", covToken, 18); err != nil {
		t.Fatal(err)
	}
	env.rpc.set("eth_call", covResult(covUint256(big.NewInt(1))))
	env.rpc.set("eth_sendRawTransaction", covErr("rejected"))
	toi := env.outbound(tokenCoin(t, 100), "OUT:"+thortypes.GetRandomTxHash().String())
	_, cp, _, err := c.SignTx(toi, 1)
	if err == nil || !strings.Contains(err.Error(), "broadcast approval") || string(cp) != "5" {
		t.Fatalf("expected approval broadcast failure, cp=%s err=%v", cp, err)
	}

	// approval keysign not selected -> skip silently
	remote := env.useTSS(t, covKeyManager{err: tss.ErrNotSelectedForKeysign})
	toi.VaultPubKey = remote
	raw, cp, in, err := c.SignTx(toi, 1)
	if err != nil || raw != nil || cp != nil || in != nil {
		t.Fatalf("expected skip, err=%v", err)
	}
	// approval keysign failure
	env.useTSS(t, covKeyManager{err: errors.New("tss down")})
	_, cp, _, err = c.SignTx(toi, 1)
	if err == nil || !strings.Contains(err.Error(), "approval transaction") || string(cp) != "5" {
		t.Fatalf("expected approval sign failure, cp=%s err=%v", cp, err)
	}
}

func TestCovReportSolvency(t *testing.T) {
	env := newCovEnv(t, nil)
	c := env.client
	env.rpc.set("eth_getTransactionCount", covNonces(1, 1))
	env.rpc.set("eth_getBalance", covResult(covBigHex(big.NewInt(1e18)))) // 1 ETH
	c.ethScanner.updateCurrentBlockHeight(5)

	drain := func() []stypes.Solvency {
		var out []stypes.Solvency
		for {
			select {
			case s := <-c.globalSolvencyQueue:
				out = append(out, s)
			default:
				return out
			}
		}
	}

	// not a reporting height
	if err := c.ReportSolvency(21); err != nil || len(drain()) != 0 {
		t.Fatalf("non reporting height should be a no-op, err=%v", err)
	}
	// unhealthy scanner and height equals scan height
	c.ethScanner.updateCurrentBlockHeight(40)
	if err := c.ReportSolvency(40); err != nil || len(drain()) != 0 {
		t.Fatal("unhealthy scan-hook call should be a no-op")
	}
	// asgard lookup failure
	env.bridge.asgardErr = errors.New("down")
	if err := c.ReportSolvency(20); err == nil {
		t.Fatal("expected asgard error")
	}
	env.bridge.asgardErr = nil

	solvent := thortypes.Vault{PubKey: env.pubKey, Coins: common.Coins{ethCoin(common.One / 2)}}
	insolvent := thortypes.Vault{PubKey: thortypes.GetRandomPubKey(), Coins: common.Coins{ethCoin(10 * common.One)}}
	btcOnly := thortypes.Vault{PubKey: thortypes.GetRandomPubKey(), Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))}}

	// all solvent while unhealthy -> report solvent vaults
	env.bridge.asgards = thortypes.Vaults{solvent, btcOnly}
	if err := c.ReportSolvency(20); err != nil {
		t.Fatal(err)
	}
	msgs := drain()
	if len(msgs) != 1 || !msgs[0].PubKey.Equals(env.pubKey) || msgs[0].Height != 20 {
		t.Fatalf("solvent msgs = %+v", msgs)
	}
	if c.lastSolvencyCheckHeight.Load() != 20 {
		t.Fatalf("last solvency height = %d", c.lastSolvencyCheckHeight.Load())
	}

	// insolvent vault reported alone
	env.bridge.asgards = thortypes.Vaults{solvent, insolvent}
	if err := c.ReportSolvency(60); err != nil {
		t.Fatal(err)
	}
	msgs = drain()
	if len(msgs) != 1 || !msgs[0].PubKey.Equals(insolvent.PubKey) {
		t.Fatalf("insolvent msgs = %+v", msgs)
	}

	// account lookup failures are skipped
	env.rpc.set("eth_getTransactionCount", covErr("down"))
	if err := c.ReportSolvency(80); err != nil || len(drain()) != 0 {
		t.Fatalf("failed accounts should not be reported, err=%v", err)
	}
}
