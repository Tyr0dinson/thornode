package common

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

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const (
	inboundCov = "0x1111111111111111111111111111111111111111"
	routerCov  = "0x2222222222222222222222222222222222222222"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

type recordingClientCov struct {
	signed       []SimTx
	signErr      error
	broadcastErr error
}

func (r *recordingClientCov) GetAccount(*common.PubKey) (*common.Account, error) { return nil, nil }

func (r *recordingClientCov) SignTx(tx SimTx) ([]byte, error) {
	r.signed = append(r.signed, tx)
	if r.signErr != nil {
		return nil, r.signErr
	}
	return []byte("signed"), nil
}

func (r *recordingClientCov) BroadcastTx([]byte) (string, error) {
	if r.broadcastErr != nil {
		return "", r.broadcastErr
	}
	return "TXID", nil
}

func inboundHandlerCov(t *testing.T, chain common.Chain, withRouter bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/thorchain/inbound_addresses" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		entry := map[string]interface{}{"chain": chain.String(), "address": inboundCov}
		if withRouter {
			entry["router"] = routerCov
		}
		_ = json.NewEncoder(w).Encode([]interface{}{entry})
	}
}

type fakeEVMCov struct {
	mu      sync.Mutex
	sent    []*etypes.Transaction
	failSig bool
	failGas bool
}

func (f *fakeEVMCov) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		ID     json.RawMessage   `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	_ = json.Unmarshal(body, &req)
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(result interface{}) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	fail := func(msg string) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "error": map[string]interface{}{"code": -32000, "message": msg}})
	}
	switch req.Method {
	case "eth_chainId":
		reply("0x539")
	case "eth_getTransactionCount":
		reply(fmt.Sprintf("0x%x", len(f.sent)))
	case "eth_gasPrice":
		if f.failGas {
			fail("gas boom")
			return
		}
		reply("0x1")
	case "eth_sendRawTransaction":
		if f.failSig {
			fail("send boom")
			return
		}
		var raw string
		_ = json.Unmarshal(req.Params[0], &raw)
		tx := new(etypes.Transaction)
		_ = tx.UnmarshalBinary(ecommon.FromHex(raw))
		f.sent = append(f.sent, tx)
		reply(tx.Hash().Hex())
	case "eth_getTransactionReceipt":
		var h string
		_ = json.Unmarshal(req.Params[0], &h)
		reply(map[string]interface{}{
			"transactionHash": h, "status": "0x1", "cumulativeGasUsed": "0x1", "gasUsed": "0x1",
			"logsBloom": "0x" + strings.Repeat("00", 256), "logs": []interface{}{},
			"blockHash": "0x" + strings.Repeat("11", 32), "blockNumber": "0x1", "transactionIndex": "0x0",
		})
	default:
		reply(nil)
	}
}

func evmClientCov(t *testing.T) (*evm.Client, *fakeEVMCov) {
	t.Helper()
	f := &fakeEVMCov{}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kr := keyring.NewInMemory(codec.NewProtoCodec(registry))
	mnemonic := "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"
	if _, err := kr.NewAccount("dog", mnemonic, "", cmd.THORChainHDPath, hd.Secp256k1); err != nil {
		t.Fatal(err)
	}
	lc, err := evm.NewClient(common.ETHChain, srv.URL, thorclient.NewKeysWithKeybase(kr, "dog", ""))
	if err != nil {
		t.Fatal(err)
	}
	return lc.(*evm.Client), f
}

func methodByIDCov(a abi.ABI, sel []byte) (*abi.Method, error) {
	return a.MethodById(sel)
}

func tokenAssetCov() common.Asset {
	for a := range evm.Tokens(common.ETHChain) {
		return a
	}
	return common.EmptyAsset
}

// ---------------------------------------------------------------------------
// DepositL1
// ---------------------------------------------------------------------------

func TestDepositL1GenericPathCov(t *testing.T) {
	setThornodeCov(t, inboundHandlerCov(t, common.BTCChain, false))
	l := zerolog.Nop()
	client := &recordingClientCov{}
	txid, err := DepositL1(&l, client, common.BTCAsset, "=:ETH.ETH:addr", cosmos.NewUint(5000))
	if err != nil || txid != "TXID" {
		t.Fatalf("txid %q err %v", txid, err)
	}
	if len(client.signed) != 1 {
		t.Fatalf("signed %d", len(client.signed))
	}
	tx := client.signed[0]
	if tx.Chain != common.BTCChain || tx.ToAddress.String() != inboundCov || tx.Memo != "=:ETH.ETH:addr" ||
		!tx.Coin.Asset.Equals(common.BTCAsset) || tx.Coin.Amount.Uint64() != 5000 {
		t.Fatalf("unexpected tx %+v", tx)
	}

	// broadcast failure is returned
	client.broadcastErr = errors.New("permanent broadcast failure")
	if _, err = DepositL1(&l, client, common.BTCAsset, "m", cosmos.NewUint(1)); err == nil {
		t.Fatal("expected broadcast error")
	}
}

func TestDepositL1ETHWithoutRouterUsesTransferCov(t *testing.T) {
	setThornodeCov(t, inboundHandlerCov(t, common.ETHChain, false))
	l := zerolog.Nop()
	client := &recordingClientCov{}
	if _, err := DepositL1(&l, client, common.ETHAsset, "memo", cosmos.NewUint(1)); err != nil {
		t.Fatal(err)
	}
	if len(client.signed) != 1 || client.signed[0].ToAddress.String() != inboundCov {
		t.Fatalf("expected plain transfer, got %+v", client.signed)
	}
}

func TestDepositL1ETHRouterRequiresEVMClientCov(t *testing.T) {
	setThornodeCov(t, inboundHandlerCov(t, common.ETHChain, true))
	l := zerolog.Nop()
	_, err := DepositL1(&l, &recordingClientCov{}, common.ETHAsset, "memo", cosmos.NewUint(1))
	if err == nil || !strings.Contains(err.Error(), "failed to get evm client") {
		t.Fatalf("expected evm client error, got %v", err)
	}
}

func TestDepositL1ETHRouterPathCov(t *testing.T) {
	setThornodeCov(t, inboundHandlerCov(t, common.ETHChain, true))
	l := zerolog.Nop()
	c, f := evmClientCov(t)
	txid, err := DepositL1(&l, c, common.ETHAsset, "=:BTC.BTC:x", cosmos.NewUint(3))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 {
		t.Fatalf("sent %d", len(f.sent))
	}
	tx := f.sent[0]
	if strings.TrimPrefix(tx.Hash().Hex(), "0x") != txid {
		t.Fatalf("txid %s != %s", txid, tx.Hash().Hex())
	}
	if *tx.To() != ecommon.HexToAddress(routerCov) || tx.Value().Cmp(big.NewInt(3e10)) != 0 {
		t.Fatalf("to %s value %s", tx.To(), tx.Value())
	}
	method, err := methodByIDCov(evm.RouterABI(), tx.Data()[:4])
	if err != nil || method.Name != "depositWithExpiry" {
		t.Fatalf("method %v %v", method, err)
	}
	args, err := method.Inputs.Unpack(tx.Data()[4:])
	if err != nil {
		t.Fatal(err)
	}
	if args[0].(ecommon.Address) != ecommon.HexToAddress(inboundCov) || args[1].(ecommon.Address) != (ecommon.Address{}) ||
		args[2].(*big.Int).Cmp(big.NewInt(3e10)) != 0 || args[3].(string) != "=:BTC.BTC:x" {
		t.Fatalf("args %v", args)
	}

	// sign failure
	f.failGas = true
	if _, err = DepositL1(&l, c, common.ETHAsset, "m", cosmos.NewUint(1)); err == nil || !strings.Contains(err.Error(), "gas price") {
		t.Fatalf("expected sign error, got %v", err)
	}
	f.failGas = false
	// broadcast failure
	f.failSig = true
	if _, err = DepositL1(&l, c, common.ETHAsset, "m", cosmos.NewUint(1)); err == nil || !strings.Contains(err.Error(), "send boom") {
		t.Fatalf("expected broadcast error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// DepositL1Token
// ---------------------------------------------------------------------------

func TestDepositL1TokenErrorsCov(t *testing.T) {
	l := zerolog.Nop()
	setThornodeCov(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	if _, err := DepositL1Token(&l, nil, tokenAssetCov(), "m", cosmos.NewUint(1)); err == nil {
		t.Fatal("expected inbound error")
	}
	setThornodeCov(t, inboundHandlerCov(t, common.ETHChain, false))
	if _, err := DepositL1Token(&l, nil, tokenAssetCov(), "m", cosmos.NewUint(1)); err == nil || !strings.Contains(err.Error(), "router address") {
		t.Fatalf("expected router error, got %v", err)
	}
}

func TestDepositL1TokenApprovesThenDepositsCov(t *testing.T) {
	setThornodeCov(t, inboundHandlerCov(t, common.ETHChain, true))
	l := zerolog.Nop()
	c, f := evmClientCov(t)
	asset := tokenAssetCov()
	token := evm.Tokens(common.ETHChain)[asset]

	txid, err := DepositL1Token(&l, c, asset, "=:BTC.BTC:x", cosmos.NewUint(2*common.One))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 2 {
		t.Fatalf("expected approve+deposit, got %d", len(f.sent))
	}
	want := new(big.Int).Mul(big.NewInt(2), big.NewInt(1e18)) // 2 tokens at 18 decimals

	approve := f.sent[0]
	if *approve.To() != ecommon.HexToAddress(token.Address) {
		t.Fatalf("approve sent to %s", approve.To())
	}
	m, _ := methodByIDCov(evm.ERC20ABI(), approve.Data()[:4])
	if m == nil || m.Name != "approve" {
		t.Fatalf("approve method %v", m)
	}
	args, _ := m.Inputs.Unpack(approve.Data()[4:])
	if args[0].(ecommon.Address) != ecommon.HexToAddress(routerCov) || args[1].(*big.Int).Cmp(want) != 0 {
		t.Fatalf("approve args %v", args)
	}

	deposit := f.sent[1]
	if strings.TrimPrefix(deposit.Hash().Hex(), "0x") != txid || *deposit.To() != ecommon.HexToAddress(routerCov) {
		t.Fatalf("deposit tx %s to %s", txid, deposit.To())
	}
	m, _ = methodByIDCov(evm.RouterABI(), deposit.Data()[:4])
	args, _ = m.Inputs.Unpack(deposit.Data()[4:])
	if args[1].(ecommon.Address) != ecommon.HexToAddress(token.Address) || args[2].(*big.Int).Cmp(want) != 0 || args[3].(string) != "=:BTC.BTC:x" {
		t.Fatalf("deposit args %v", args)
	}

	// approve broadcast fails
	f.failSig = true
	if _, err = DepositL1Token(&l, c, asset, "m", cosmos.NewUint(1)); err == nil {
		t.Fatal("expected approve broadcast error")
	}
	f.failSig = false
	// approve sign fails
	f.failGas = true
	if _, err = DepositL1Token(&l, c, asset, "m", cosmos.NewUint(1)); err == nil {
		t.Fatal("expected approve sign error")
	}
}
