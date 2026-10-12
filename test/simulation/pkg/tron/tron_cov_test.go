package tron

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cmtcrypto "github.com/cometbft/cometbft/crypto"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestGetAccountCov(t *testing.T) {
	var queried []string
	failing := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wallet/getaccount" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if failing {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Address string `json:"address"`
		}
		_ = json.Unmarshal(body, &req)
		queried = append(queried, req.Address)
		_, _ = w.Write([]byte(`{"balance":1234}`))
	}))
	defer server.Close()

	lc, err := NewConstructor(server.URL)(common.TRONChain, newTestKeys(t, "covuser", tronTestMnemonic))
	if err != nil {
		t.Fatal(err)
	}
	c := lc.(*Client)

	acct, err := c.GetAccount(nil)
	if err != nil {
		t.Fatal(err)
	}
	// 1e6 sun -> 1e8 simulation units
	if got := acct.Coins.GetCoin(common.TRXAsset).Amount.Uint64(); got != 123400 {
		t.Fatalf("expected 123400, got %d", got)
	}
	if len(queried) != 1 || queried[0] != c.address.String() {
		t.Fatalf("expected own address query, got %v", queried)
	}

	// explicit pubkey resolves its own TRON address
	pk, err := common.NewPubKeyFromCrypto(mustTmPubKeyCov(t, c.keys))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.GetAccount(&pk); err != nil {
		t.Fatal(err)
	}
	if queried[1] != c.address.String() {
		t.Fatalf("pubkey query must resolve to %s, got %s", c.address, queried[1])
	}

	bad := common.PubKey("bogus")
	if _, err = c.GetAccount(&bad); err == nil || !strings.Contains(err.Error(), "fail to get address from pubkey") {
		t.Fatalf("expected pubkey error, got %v", err)
	}

	failing = true
	if _, err = c.GetAccount(nil); err == nil {
		t.Fatal("expected api error")
	}
}

func mustTmPubKeyCov(t *testing.T, keys *thorclient.Keys) cmtcrypto.PubKey {
	t.Helper()
	priv, err := keys.GetPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pk, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
	if err != nil {
		t.Fatal(err)
	}
	return pk
}

func TestNewClientWithoutKeyFailsCov(t *testing.T) {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kr := keyring.NewInMemory(codec.NewProtoCodec(registry), func(options *keyring.Options) {
		options.SupportedAlgos = keyring.SigningAlgoList{hd.Secp256k1}
	})
	keys := thorclient.NewKeysWithKeybase(kr, "missing", "")
	if _, err := NewClient(common.TRONChain, "http://127.0.0.1:1", keys); err == nil || !strings.Contains(err.Error(), "fail to get private key") {
		t.Fatalf("expected private key error, got %v", err)
	}
}

func TestSignContractTxNotImplementedCov(t *testing.T) {
	c := &Client{}
	if _, err := c.SignContractTx(SimContractTx{}); err == nil || err.Error() != "not implemented" {
		t.Fatalf("expected not implemented, got %v", err)
	}
}

func TestBroadcastTxRejectsMalformedPayloadCov(t *testing.T) {
	c := &Client{}
	if _, err := c.BroadcastTx([]byte("{")); err == nil || !strings.Contains(err.Error(), "failed to decode signed tx") {
		t.Fatalf("expected decode error, got %v", err)
	}
	if _, err := c.BroadcastTx([]byte(`{}`)); err == nil || !strings.Contains(err.Error(), "has no txID") {
		t.Fatalf("expected missing txid error, got %v", err)
	}
}

func TestWaitForConfirmedBroadcastRebroadcastErrorsCov(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
		status   int
		want     string
	}{
		{"code and message", `{"result":false,"code":"SERVER_BUSY","message":"busy"}`, http.StatusOK, "failed to rebroadcast tx abcd: SERVER_BUSY - busy"},
		{"code only", `{"result":false,"code":"SERVER_BUSY"}`, http.StatusOK, "failed to rebroadcast tx abcd: SERVER_BUSY"},
		{"bare failure", `{"result":false}`, http.StatusOK, "failed to rebroadcast tx abcd"},
		{"transport failure", ``, http.StatusInternalServerError, "broadcasttransaction"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// leave headroom for the first poll and rebroadcast on a loaded runner;
			// a deadline that expires before any rebroadcast hides the failure text
			restore := overrideBroadcastObservationTimings(500*time.Millisecond, time.Millisecond)
			defer restore()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/wallet/gettransactioninfobyid":
					_, _ = w.Write([]byte(`{}`))
				case "/wallet/gettransactionbyid":
					// observed in the pool, but never mined
					_, _ = w.Write([]byte(`{"txID":"abcd"}`))
				case "/wallet/broadcasttransaction":
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.response))
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
			}))
			defer server.Close()

			c := newTestClient(t, server.URL)
			err := c.waitForConfirmedBroadcast("abcd", []byte(`{"txID":"abcd"}`), 0)
			if err == nil || !errors.Is(err, ErrSimTxUncertain) {
				t.Fatalf("expected uncertain error, got %v", err)
			}
			if !strings.Contains(err.Error(), "not confirmed on tron node") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q in %v", tc.want, err)
			}
		})
	}
}

func TestWaitForConfirmedBroadcastExpirationExtendsDeadlineCov(t *testing.T) {
	restore := overrideBroadcastObservationTimings(time.Millisecond, time.Millisecond)
	defer restore()
	prevGrace := tronBroadcastConfirmationGrace
	tronBroadcastConfirmationGrace = 0
	defer func() { tronBroadcastConfirmationGrace = prevGrace }()

	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wallet/gettransactioninfobyid":
			polls++
			if polls >= 5 {
				_, _ = w.Write([]byte(`{"id":"abcd","blockNumber":7}`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case "/wallet/gettransactionbyid":
			_, _ = w.Write([]byte(`{}`))
		case "/wallet/broadcasttransaction":
			_, _ = w.Write([]byte(`{"result":false,"code":"DUP_TRANSACTION_ERROR"}`))
		}
	}))
	defer server.Close()

	c := newTestClient(t, server.URL)
	// an expiration well in the future must keep polling past the 1ms timeout
	exp := time.Now().Add(2 * time.Second).UnixMilli()
	if err := c.waitForConfirmedBroadcast("abcd", []byte(`{}`), exp); err != nil {
		t.Fatalf("expected confirmation before expiration, got %v", err)
	}
	if polls < 5 {
		t.Fatalf("expected polling to continue until mined, got %d polls", polls)
	}
}
