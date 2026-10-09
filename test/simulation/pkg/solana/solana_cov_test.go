package solana

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const masterMnemonicCov = "master master master master master master master master master master master master master master master master master master master master master master master notice"

func edKeysCov(t *testing.T, withEd bool) *thorclient.Keys {
	t.Helper()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kr := keyring.NewInMemory(cdc, func(options *keyring.Options) {
		options.SupportedAlgos = keyring.SigningAlgoList{ed25519.Ed25519}
	})
	if withEd {
		if _, err := kr.NewAccount("ed-master", masterMnemonicCov, "", ed25519.HDPath, ed25519.Ed25519); err != nil {
			t.Fatal(err)
		}
	}
	return thorclient.NewKeysWithKeybase(kr, "master", "")
}

type solRPCFakeCov struct {
	t       *testing.T
	handler func(method string, params []json.RawMessage) any
	calls   map[string]int
}

func newSolServerCov(t *testing.T, handler func(method string, params []json.RawMessage) any) (*httptest.Server, *solRPCFakeCov) {
	f := &solRPCFakeCov{t: t, handler: handler, calls: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		f.calls[req.Method]++
		res := f.handler(req.Method, req.Params)
		if status, ok := res.(int); ok {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(server.Close)
	return server, f
}

func rpcResultCov(result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": 1, "result": result}
}

func fastBroadcastCov(t *testing.T, attempts, rebroadcastEvery int) {
	t.Helper()
	pa, pi, pr := solanaBroadcastConfirmationAttempts, solanaBroadcastConfirmationPollInterval, solanaBroadcastRebroadcastEvery
	solanaBroadcastConfirmationAttempts = attempts
	solanaBroadcastConfirmationPollInterval = 0
	solanaBroadcastRebroadcastEvery = rebroadcastEvery
	t.Cleanup(func() {
		solanaBroadcastConfirmationAttempts, solanaBroadcastConfirmationPollInterval, solanaBroadcastRebroadcastEvery = pa, pi, pr
	})
}

func confirmedTxCov(metaErr any) map[string]any {
	return map[string]any{
		"slot": 5,
		"meta": map[string]any{"err": metaErr},
		"transaction": map[string]any{
			"message": map[string]any{
				"accountKeys":         []any{},
				"addressTableLookups": []any{},
				"header": map[string]any{
					"numReadonlySignedAccounts":   0,
					"numReadonlyUnsignedAccounts": 0,
					"numRequiredSignatures":       1,
				},
				"instructions":    []any{},
				"recentBlockhash": "bh",
			},
			"signatures": []any{"sol-sig"},
		},
	}
}

func TestNewClientAndSignTxCov(t *testing.T) {
	server, fake := newSolServerCov(t, func(method string, _ []json.RawMessage) any {
		if method == "getLatestBlockhash" {
			return rpcResultCov(map[string]any{"value": map[string]any{"blockhash": "11111111111111111111111111111111"}})
		}
		t.Errorf("unexpected method %s", method)
		return http.StatusBadRequest
	})

	lc, err := NewConstructor(server.URL)(common.SOLChain, edKeysCov(t, true))
	if err != nil {
		t.Fatal(err)
	}
	c := lc.(*Client)
	if c.chain != common.SOLChain || c.address.IsEmpty() || c.pubKey.IsEmpty() {
		t.Fatalf("client not initialised: %+v", c)
	}
	wantAddr, err := c.pubKey.GetAddress(common.SOLChain)
	if err != nil || wantAddr != c.address {
		t.Fatalf("address mismatch %s != %s", c.address, wantAddr)
	}

	signed, err := c.SignTx(types.SimTx{
		Chain:     common.SOLChain,
		ToAddress: common.Address("6oEorfnzTgD4qa9G11SyuCxoudZT1Y44bnErCbhC7RQT"),
		Coin:      common.NewCoin(common.SOLAsset, cosmos.NewUint(10000)),
		Memo:      "SIMULATION:cov",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(signed) < 64+1 || fake.calls["getLatestBlockhash"] != 1 {
		t.Fatalf("unexpected signed tx len %d calls %v", len(signed), fake.calls)
	}
	// the serialized tx starts with the signature count (1) followed by the
	// ed25519 signature over the message produced by our signer
	if signed[0] != 1 {
		t.Fatalf("expected one signature, got %d", signed[0])
	}
	if !strings.Contains(string(signed), "SIMULATION:cov") {
		t.Fatal("memo must be embedded in the signed transaction")
	}
}

func TestNewClientWithoutEd25519KeyFailsCov(t *testing.T) {
	_, err := NewClient(common.SOLChain, "http://127.0.0.1:1", edKeysCov(t, false))
	if err == nil || !strings.Contains(err.Error(), "fail to get private key") {
		t.Fatalf("expected private key error, got %v", err)
	}
}

func TestSignTxBlockhashErrorCov(t *testing.T) {
	server, _ := newSolServerCov(t, func(string, []json.RawMessage) any { return http.StatusInternalServerError })
	c := &Client{rpc: rpc.NewSolRPC(server.URL, time.Second)}
	if _, err := c.SignTx(types.SimTx{}); err == nil || !strings.Contains(err.Error(), "fail to get latest blockhash") {
		t.Fatalf("expected blockhash error, got %v", err)
	}
}

func TestGetAccountErrorsCov(t *testing.T) {
	client, err := NewClient(common.SOLChain, "http://127.0.0.1:1", edKeysCov(t, true))
	if err != nil {
		t.Fatal(err)
	}
	c := client.(*Client)

	// a secp256k1 key has no SOL address
	secp := common.PubKey("bogus")
	if _, err = c.GetAccount(&secp); err == nil || !strings.Contains(err.Error(), "fail to get address from pubkey") {
		t.Fatalf("expected address error, got %v", err)
	}

	balanceFails := true
	server, _ := newSolServerCov(t, func(method string, _ []json.RawMessage) any {
		switch method {
		case "getBalance":
			if balanceFails {
				return http.StatusInternalServerError
			}
			return rpcResultCov(map[string]any{"value": 10})
		default:
			return http.StatusInternalServerError
		}
	})
	c.rpc = rpc.NewSolRPC(server.URL, time.Second)
	if _, err = c.GetAccount(&c.pubKey); err == nil || !strings.Contains(err.Error(), "fail to get balance") {
		t.Fatalf("expected balance error, got %v", err)
	}
	balanceFails = false
	if _, err = c.GetAccount(nil); err == nil || !strings.Contains(err.Error(), "owned token account lamports") {
		t.Fatalf("expected token account error, got %v", err)
	}
}

func TestBroadcastTxSendErrorCov(t *testing.T) {
	server, _ := newSolServerCov(t, func(string, []json.RawMessage) any {
		return map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": -32002, "message": "Blockhash not found"}}
	})
	c := &Client{rpc: rpc.NewSolRPC(server.URL, time.Second)}
	if _, err := c.BroadcastTx([]byte("x")); err == nil || !strings.Contains(err.Error(), "failed to broadcast tx") {
		t.Fatalf("expected broadcast error, got %v", err)
	}
}

func TestBroadcastTxFailedOnChainCov(t *testing.T) {
	fastBroadcastCov(t, 3, 0)
	server, _ := newSolServerCov(t, func(method string, _ []json.RawMessage) any {
		if method == "sendTransaction" {
			return rpcResultCov("sol-sig")
		}
		return rpcResultCov(confirmedTxCov(map[string]any{"InstructionError": []any{0, "Custom"}}))
	})
	c := &Client{rpc: rpc.NewSolRPC(server.URL, time.Second)}
	_, err := c.BroadcastTx([]byte("x"))
	if err == nil || !strings.Contains(err.Error(), "failed on solana node") {
		t.Fatalf("expected on-chain failure, got %v", err)
	}
}

func TestBroadcastTxRebroadcastsCov(t *testing.T) {
	fastBroadcastCov(t, 4, 1)
	var sentPayloads []string
	server, fake := newSolServerCov(t, func(method string, params []json.RawMessage) any {
		switch method {
		case "sendTransaction":
			var raw string
			_ = json.Unmarshal(params[0], &raw)
			sentPayloads = append(sentPayloads, raw)
			if len(sentPayloads) == 2 {
				// a failing rebroadcast is tolerated
				return http.StatusInternalServerError
			}
			return rpcResultCov("sol-sig")
		default:
			if len(sentPayloads) >= 3 {
				return rpcResultCov(confirmedTxCov(nil))
			}
			return rpcResultCov(nil)
		}
	})
	c := &Client{rpc: rpc.NewSolRPC(server.URL, time.Second)}
	txid, err := c.BroadcastTx([]byte("payload"))
	if err != nil || txid != "sol-sig" {
		t.Fatalf("expected confirmation after rebroadcast, got %s %v", txid, err)
	}
	want := base64.StdEncoding.EncodeToString([]byte("payload"))
	for _, p := range sentPayloads {
		if p != want {
			t.Fatalf("rebroadcast must reuse the same payload, got %q", p)
		}
	}
	if fake.calls["sendTransaction"] != 3 {
		t.Fatalf("expected 3 sends, got %d", fake.calls["sendTransaction"])
	}
}

func TestBroadcastTxRebroadcastSignatureMismatchCov(t *testing.T) {
	fastBroadcastCov(t, 3, 1)
	sends := 0
	server, _ := newSolServerCov(t, func(method string, _ []json.RawMessage) any {
		if method == "sendTransaction" {
			sends++
			if sends > 1 {
				return rpcResultCov("other-sig")
			}
			return rpcResultCov("sol-sig")
		}
		return http.StatusInternalServerError
	})
	c := &Client{rpc: rpc.NewSolRPC(server.URL, time.Second)}
	_, err := c.BroadcastTx([]byte("x"))
	if err == nil || !strings.Contains(err.Error(), "rebroadcast returned different solana signature other-sig") {
		t.Fatalf("expected signature mismatch, got %v", err)
	}
}
