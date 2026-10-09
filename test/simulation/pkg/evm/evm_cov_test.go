package evm

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const mnemonicCov = "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"

func keysCov(t *testing.T) *thorclient.Keys {
	t.Helper()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kr := keyring.NewInMemory(codec.NewProtoCodec(registry))
	if _, err := kr.NewAccount("dog", mnemonicCov, "", cmd.THORChainHDPath, hd.Secp256k1); err != nil {
		t.Fatal(err)
	}
	return thorclient.NewKeysWithKeybase(kr, "dog", "")
}

// fakeEVMCov is a minimal Ethereum JSON-RPC server.
type fakeEVMCov struct {
	mu       sync.Mutex
	calls    map[string]int
	fail     map[string]bool
	callData [][]byte
	sent     []*etypes.Transaction
	status   uint64
	callRet  string // hex returned from eth_call
	code     string
	balance  string
	nonce    string
	gasPrice string
}

func newFakeEVMCov(t *testing.T) (*fakeEVMCov, *httptest.Server) {
	f := &fakeEVMCov{
		calls:    map[string]int{},
		fail:     map[string]bool{},
		status:   1,
		callRet:  "0x" + fmt.Sprintf("%064x", new(big.Int).Mul(big.NewInt(5), big.NewInt(1e18))),
		code:     "0x",
		balance:  fmt.Sprintf("0x%x", big.NewInt(0).Mul(big.NewInt(3), big.NewInt(1e18))),
		nonce:    "0x7",
		gasPrice: "0x3b9aca00",
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeEVMCov) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		ID     json.RawMessage   `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[req.Method]++

	reply := func(result interface{}) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	if f.fail[req.Method] {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0", "id": req.ID,
			"error": map[string]interface{}{"code": -32000, "message": req.Method + " boom"},
		})
		return
	}

	switch req.Method {
	case "eth_chainId":
		reply("0x539")
	case "eth_getTransactionCount":
		reply(f.nonce)
	case "eth_gasPrice":
		reply(f.gasPrice)
	case "eth_getBalance":
		reply(f.balance)
	case "eth_getCode":
		reply(f.code)
	case "eth_call":
		var msg struct {
			Input string `json:"input"`
			Data  string `json:"data"`
		}
		_ = json.Unmarshal(req.Params[0], &msg)
		in := msg.Input
		if in == "" {
			in = msg.Data
		}
		f.callData = append(f.callData, ecommon.FromHex(in))
		reply(f.callRet)
	case "eth_sendRawTransaction":
		var raw string
		_ = json.Unmarshal(req.Params[0], &raw)
		tx := new(etypes.Transaction)
		if err := tx.UnmarshalBinary(ecommon.FromHex(raw)); err != nil {
			reply(nil)
			return
		}
		f.sent = append(f.sent, tx)
		reply(tx.Hash().Hex())
	case "eth_getTransactionReceipt":
		var h string
		_ = json.Unmarshal(req.Params[0], &h)
		reply(map[string]interface{}{
			"transactionHash":   h,
			"status":            fmt.Sprintf("0x%x", f.status),
			"cumulativeGasUsed": "0x5208",
			"gasUsed":           "0x5208",
			"logsBloom":         "0x" + strings.Repeat("00", 256),
			"logs":              []interface{}{},
			"blockHash":         "0x" + strings.Repeat("11", 32),
			"blockNumber":       "0x1",
			"transactionIndex":  "0x0",
		})
	default:
		reply(nil)
	}
}

func newClientCov(t *testing.T, chain common.Chain) (*Client, *fakeEVMCov) {
	t.Helper()
	f, srv := newFakeEVMCov(t)
	lc, err := NewConstructor(srv.URL)(chain, keysCov(t))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c, ok := lc.(*Client)
	if !ok {
		t.Fatalf("unexpected client type %T", lc)
	}
	return c, f
}

func TestABIsAndTokensCov(t *testing.T) {
	if _, ok := RouterABI().Methods["depositWithExpiry"]; !ok {
		t.Fatal("router abi missing depositWithExpiry")
	}
	if _, ok := ERC20ABI().Methods["balanceOf"]; !ok {
		t.Fatal("erc20 abi missing balanceOf")
	}
	if len(AggregatorABI().Methods) == 0 {
		t.Fatal("aggregator abi has no methods")
	}
	toks := Tokens(common.ETHChain)
	if len(toks) != 1 {
		t.Fatalf("expected 1 token, got %d", len(toks))
	}
	for asset, tok := range toks {
		if asset.Chain != common.ETHChain || tok.Symbol != "TKN" || tok.Decimals != 18 {
			t.Fatalf("unexpected token %v %+v", asset, tok)
		}
	}
}

func TestChainGasOffsetCov(t *testing.T) {
	cases := map[common.Chain]uint64{
		common.ETHChain:  0,
		common.BSCChain:  1,
		common.BASEChain: 2,
		common.POLChain:  3,
		common.AVAXChain: 4,
		common.BTCChain:  0,
	}
	seen := map[uint64]common.Chain{}
	for chain, want := range cases {
		if got := chainGasOffset(chain); got != want {
			t.Fatalf("%s: got %d want %d", chain, got, want)
		}
		if chain != common.BTCChain {
			if other, ok := seen[want]; ok {
				t.Fatalf("offset %d shared by %s and %s", want, chain, other)
			}
			seen[want] = chain
		}
	}
}

func TestNewClientErrorsCov(t *testing.T) {
	// chain id rpc failure
	f, srv := newFakeEVMCov(t)
	f.fail["eth_chainId"] = true
	if _, err := NewClient(common.ETHChain, srv.URL, keysCov(t)); err == nil || !strings.Contains(err.Error(), "fail to get chain id") {
		t.Fatalf("expected chain id error, got %v", err)
	}

	// invalid dial url
	if _, err := NewClient(common.ETHChain, "ftp://bad", keysCov(t)); err == nil || !strings.Contains(err.Error(), "fail to dial") {
		t.Fatalf("expected dial error, got %v", err)
	}

	// keys without a signer
	registry := codectypes.NewInterfaceRegistry()
	kr := keyring.NewInMemory(codec.NewProtoCodec(registry))
	empty := thorclient.NewKeysWithKeybase(kr, "missing", "")
	if _, err := NewClient(common.ETHChain, srv.URL, empty); err == nil || !strings.Contains(err.Error(), "private key") {
		t.Fatalf("expected private key error, got %v", err)
	}
}

func TestGetAccountCov(t *testing.T) {
	c, f := newClientCov(t, common.ETHChain)
	if c.chain != common.ETHChain || c.address.IsEmpty() {
		t.Fatalf("unexpected client %+v", c)
	}

	acct, err := c.GetAccount(nil)
	if err != nil {
		t.Fatal(err)
	}
	if acct.Sequence != 7 {
		t.Fatalf("sequence = %d", acct.Sequence)
	}
	if len(acct.Coins) != 2 {
		t.Fatalf("coins = %v", acct.Coins)
	}
	if !acct.Coins[0].Asset.Equals(common.ETHAsset) || acct.Coins[0].Amount.Uint64() != 3*common.One {
		t.Fatalf("gas coin = %v", acct.Coins[0])
	}
	if acct.Coins[1].Amount.Uint64() != 5*common.One {
		t.Fatalf("token coin = %v", acct.Coins[1])
	}
	// balanceOf selector
	if len(f.callData) != 1 || fmt.Sprintf("%x", f.callData[0][:4]) != "70a08231" {
		t.Fatalf("unexpected call data %x", f.callData)
	}

	// explicit pubkey
	pk := c.pubKey
	acct, err = c.GetAccount(&pk)
	if err != nil || acct.Sequence != 7 {
		t.Fatalf("explicit pubkey: %v %v", acct, err)
	}

	// invalid pubkey
	bad := common.PubKey("bogus")
	if _, err = c.GetAccount(&bad); err == nil {
		t.Fatal("expected address error for bad pubkey")
	}

	// token lookup errors are logged and skipped
	f.fail["eth_call"] = true
	acct, err = c.GetAccount(nil)
	if err != nil || len(acct.Coins) != 1 {
		t.Fatalf("expected only gas coin when token call fails: %v %v", acct, err)
	}

	f.fail["eth_getBalance"] = true
	if _, err = c.GetAccount(nil); err == nil || !strings.Contains(err.Error(), "balance") {
		t.Fatalf("expected balance error, got %v", err)
	}
	f.fail["eth_getTransactionCount"] = true
	if _, err = c.GetAccount(nil); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("expected nonce error, got %v", err)
	}
}

func TestSignAndBroadcastTxCov(t *testing.T) {
	c, f := newClientCov(t, common.BASEChain)
	to := common.Address("0x1111111111111111111111111111111111111111")
	signed, err := c.SignTx(SimTx{
		Chain:     common.BASEChain,
		ToAddress: to,
		Coin:      common.NewCoin(common.BaseETHAsset, cosmos.NewUint(2)),
		Memo:      "abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	tx := &etypes.Transaction{}
	if err = tx.UnmarshalJSON(signed); err != nil {
		t.Fatal(err)
	}
	if tx.Nonce() != 7 || tx.GasPrice().Int64() != 1e9 {
		t.Fatalf("nonce/gasprice = %d/%s", tx.Nonce(), tx.GasPrice())
	}
	if tx.Gas() != 21000+3*40+2 {
		t.Fatalf("gas = %d", tx.Gas())
	}
	if tx.Value().Cmp(big.NewInt(2e10)) != 0 || string(tx.Data()) != "abc" {
		t.Fatalf("value/data = %s/%q", tx.Value(), tx.Data())
	}
	if *tx.To() != ecommon.HexToAddress(to.String()) {
		t.Fatalf("to = %s", tx.To())
	}
	from, err := etypes.Sender(c.signer, tx)
	if err != nil || !strings.EqualFold(from.Hex(), c.address.String()) {
		t.Fatalf("sender %s != %s (%v)", from.Hex(), c.address, err)
	}

	txid, err := c.BroadcastTx(signed)
	if err != nil {
		t.Fatal(err)
	}
	if txid != strings.TrimPrefix(tx.Hash().Hex(), "0x") || len(f.sent) != 1 {
		t.Fatalf("txid %s sent %d", txid, len(f.sent))
	}
	if f.calls["eth_getTransactionReceipt"] != 1 {
		t.Fatalf("expected receipt wait, got %d", f.calls["eth_getTransactionReceipt"])
	}

	// reverted receipt
	f.status = 0
	if _, err = c.BroadcastTx(signed); err == nil || !strings.Contains(err.Error(), "status 0") {
		t.Fatalf("expected failure status error, got %v", err)
	}

	// send failure returns the txid with the error
	f.fail["eth_sendRawTransaction"] = true
	got, err := c.BroadcastTx(signed)
	if err == nil || got != txid {
		t.Fatalf("expected send error with txid, got %q %v", got, err)
	}

	// invalid json
	if _, err = c.BroadcastTx([]byte("nope")); err == nil {
		t.Fatal("expected unmarshal error")
	}

	// sign errors
	f.fail["eth_gasPrice"] = true
	if _, err = c.SignTx(SimTx{Chain: common.BASEChain, ToAddress: to, Coin: common.NewCoin(common.BaseETHAsset, cosmos.NewUint(1))}); err == nil || !strings.Contains(err.Error(), "gas price") {
		t.Fatalf("expected gas price error, got %v", err)
	}
	f.fail["eth_getTransactionCount"] = true
	if _, err = c.SignTx(SimTx{Chain: common.BASEChain, ToAddress: to, Coin: common.NewCoin(common.BaseETHAsset, cosmos.NewUint(1))}); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("expected nonce error, got %v", err)
	}
}

func TestDeferAndFlushReceiptsCov(t *testing.T) {
	c, f := newClientCov(t, common.ETHChain)
	to := common.Address("0x2222222222222222222222222222222222222222")
	c.DeferReceipts()
	var ids []string
	for i := 0; i < 2; i++ {
		signed, err := c.SignTx(SimTx{Chain: common.ETHChain, ToAddress: to, Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(uint64(i+1)))})
		if err != nil {
			t.Fatal(err)
		}
		id, err := c.BroadcastTx(signed)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if ids[0] == ids[1] || len(c.deferred) != 2 || f.calls["eth_getTransactionReceipt"] != 0 {
		t.Fatalf("expected 2 deferred, no receipt waits: %v %d", c.deferred, f.calls["eth_getTransactionReceipt"])
	}
	if err := c.FlushReceipts(); err != nil {
		t.Fatal(err)
	}
	if c.deferring || c.deferred != nil || f.calls["eth_getTransactionReceipt"] != 2 {
		t.Fatalf("flush did not reset: %v %v", c.deferring, c.deferred)
	}

	// failed receipt during flush names the tx
	c.DeferReceipts()
	signed, _ := c.SignTx(SimTx{Chain: common.ETHChain, ToAddress: to, Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(9))})
	id, err := c.BroadcastTx(signed)
	if err != nil {
		t.Fatal(err)
	}
	f.status = 0
	err = c.FlushReceipts()
	if err == nil || !strings.HasPrefix(err.Error(), id) {
		t.Fatalf("expected flush error naming %s, got %v", id, err)
	}
	if c.deferring || c.deferred != nil {
		t.Fatal("flush must reset even on error")
	}
}

func TestSignContractTxCov(t *testing.T) {
	c, _ := newClientCov(t, common.AVAXChain)
	contract := common.Address("0x3333333333333333333333333333333333333333")
	signed, err := c.SignContractTx(SimContractTx{
		Chain:    common.AVAXChain,
		Contract: contract,
		ABI:      ERC20ABI(),
		Method:   "approve",
		Args:     []interface{}{ecommon.HexToAddress("0x4444444444444444444444444444444444444444"), big.NewInt(10)},
		Value:    big.NewInt(42),
	})
	if err != nil {
		t.Fatal(err)
	}
	tx := &etypes.Transaction{}
	if err = tx.UnmarshalJSON(signed); err != nil {
		t.Fatal(err)
	}
	if tx.Gas() != ContractGasLimit+4 || tx.Value().Int64() != 42 {
		t.Fatalf("gas/value = %d/%s", tx.Gas(), tx.Value())
	}
	if fmt.Sprintf("%x", tx.Data()[:4]) != "095ea7b3" {
		t.Fatalf("approve selector = %x", tx.Data()[:4])
	}

	// nil value defaults to zero
	signed, err = c.SignContractTx(SimContractTx{
		Chain: common.AVAXChain, Contract: contract, ABI: ERC20ABI(), Method: "decimals",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.UnmarshalJSON(signed)
	if tx.Value().Sign() != 0 {
		t.Fatalf("expected zero value, got %s", tx.Value())
	}

	// pack failure
	if _, err = c.SignContractTx(SimContractTx{Chain: common.AVAXChain, Contract: contract, ABI: ERC20ABI(), Method: "nope"}); err == nil || !strings.Contains(err.Error(), "fail to pack") {
		t.Fatalf("expected pack error, got %v", err)
	}
}

func TestContractReadsCov(t *testing.T) {
	c, f := newClientCov(t, common.ETHChain)

	f.callRet = "0x" + fmt.Sprintf("%064x", 18)
	dec, err := c.GetTokenDecimals("0x17aB05351fC94a1a67Bf3f56DdbB941aE6c63E25")
	if err != nil || dec != 18 {
		t.Fatalf("decimals = %d %v", dec, err)
	}

	f.callRet = "0x" + fmt.Sprintf("%064x", 12345)
	bal, err := c.GetTokenBalance("0x17aB05351fC94a1a67Bf3f56DdbB941aE6c63E25", "0x5555555555555555555555555555555555555555")
	if err != nil || bal.Int64() != 12345 {
		t.Fatalf("balance = %v %v", bal, err)
	}

	// 2.5 tokens of 18 decimals -> 2.5e8
	f.callRet = "0x" + fmt.Sprintf("%064x", new(big.Int).Mul(big.NewInt(25), big.NewInt(1e17)))
	var tknAsset common.Asset
	for a := range Tokens(common.ETHChain) {
		tknAsset = a
	}
	allow, err := c.GetVaultAllowance("0x6666666666666666666666666666666666666666", "0x7777777777777777777777777777777777777777", tknAsset)
	if err != nil || allow.Uint64() != 250000000 {
		t.Fatalf("allowance = %v %v", allow, err)
	}

	has, err := c.HasContractCode("0x17aB05351fC94a1a67Bf3f56DdbB941aE6c63E25")
	if err != nil || has {
		t.Fatalf("expected no code: %v %v", has, err)
	}
	f.code = "0x6001"
	has, err = c.HasContractCode("0x17aB05351fC94a1a67Bf3f56DdbB941aE6c63E25")
	if err != nil || !has {
		t.Fatalf("expected code: %v %v", has, err)
	}

	f.fail["eth_call"] = true
	f.fail["eth_getCode"] = true
	if _, err = c.GetTokenDecimals("0x01"); err == nil {
		t.Fatal("expected decimals error")
	}
	if _, err = c.GetTokenBalance("0x01", "0x02"); err == nil {
		t.Fatal("expected balance error")
	}
	if _, err = c.GetVaultAllowance("0x01", "0x02", tknAsset); err == nil {
		t.Fatal("expected allowance error")
	}
	if _, err = c.HasContractCode("0x01"); err == nil {
		t.Fatal("expected code error")
	}
}
