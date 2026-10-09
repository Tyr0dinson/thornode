package monero

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	thored25519 "gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func moneroKeysCov(t *testing.T, withEd bool) *thorclient.Keys {
	t.Helper()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kr := keyring.NewInMemory(codec.NewProtoCodec(registry), func(options *keyring.Options) {
		options.SupportedAlgos = keyring.SigningAlgoList{thored25519.Ed25519}
	})
	if withEd {
		if _, err := kr.NewAccount(thored25519.SignerNameEDDSA("dog"), testMnemonic, "", thored25519.HDPath, thored25519.Ed25519); err != nil {
			t.Fatal(err)
		}
	}
	return thorclient.NewKeysWithKeybase(kr, "dog", "")
}

func TestNewClientDerivesAddressAndEndpointsCov(t *testing.T) {
	t.Setenv("XMR_SIM_HELPER_AUTH_BEARER", "  helper-token ")
	t.Setenv("XMR_FROST_ENDPOINT", "http://frost.example/")
	t.Setenv("XMR_FROST_AUTH_BEARER", "")

	lc, err := NewConstructor("http://helper.example/")(common.XMRChain, moneroKeysCov(t, true))
	if err != nil {
		t.Fatal(err)
	}
	c := lc.(*Client)
	spendKeyHex, address := testSpendKeyHexAndAddress(t)
	if c.spendKeyHex != spendKeyHex || !c.address.Equals(address) {
		t.Fatalf("unexpected key material %s %s", c.spendKeyHex, c.address)
	}
	if c.helperEndpoint != "http://helper.example" || c.frostEndpoint != "http://frost.example" {
		t.Fatalf("trailing slashes must be trimmed: %s %s", c.helperEndpoint, c.frostEndpoint)
	}
	if c.helperBearer != "helper-token" || c.frostBearer != defaultFrostBearer {
		t.Fatalf("unexpected bearers %q %q", c.helperBearer, c.frostBearer)
	}

	// empty helper endpoint falls back to the default
	lc, err = NewClient(common.XMRChain, "", moneroKeysCov(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if lc.(*Client).helperEndpoint != defaultHelperEndpoint {
		t.Fatalf("expected default helper endpoint, got %s", lc.(*Client).helperEndpoint)
	}
}

func TestNewClientErrorsCov(t *testing.T) {
	if _, err := NewClient(common.BTCChain, "", moneroKeysCov(t, true)); err == nil || !strings.Contains(err.Error(), "unsupported chain") {
		t.Fatalf("expected unsupported chain, got %v", err)
	}
	if _, err := NewClient(common.XMRChain, "", moneroKeysCov(t, false)); err == nil || !strings.Contains(err.Error(), "failed to get Ed25519 private key") {
		t.Fatalf("expected key error, got %v", err)
	}
}

func newHelperServerCov(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	_, address := testSpendKeyHexAndAddress(t)
	return &Client{
		chain:          common.XMRChain,
		address:        address,
		helperEndpoint: server.URL,
		frostEndpoint:  server.URL,
		httpClient:     server.Client(),
	}
}

func TestClientHTTPErrorsCov(t *testing.T) {
	c := newHelperServerCov(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("empty bearer must not send Authorization header")
		}
		switch r.URL.Path {
		case "/v1/monero/sim/balance":
			_, _ = w.Write([]byte(`{"address":"someone-else","unlocked_balance_atomic":10000}`))
		case "/v1/monero/chain/height":
			_, _ = w.Write([]byte(`{"height":18446744073709551615}`))
		default:
			http.Error(w, "nope", http.StatusBadGateway)
		}
	})

	if _, err := c.GetAccount(nil); err == nil || !strings.Contains(err.Error(), "unexpected helper address someone-else") {
		t.Fatalf("expected address mismatch, got %v", err)
	}
	pk := common.PubKey("vault")
	if _, err := c.GetAccount(&pk); err == nil || !strings.Contains(err.Error(), "http 502 from GET") {
		t.Fatalf("expected vault balance error, got %v", err)
	}
	if _, err := c.GetHeight(); err == nil || !strings.Contains(err.Error(), "exceeds int64 range") {
		t.Fatalf("expected height overflow error, got %v", err)
	}
	if _, err := c.BroadcastTx([]byte(`{}`)); err == nil || !strings.Contains(err.Error(), "http 502 from POST") {
		t.Fatalf("expected transfer error, got %v", err)
	}
	if _, err := c.BroadcastTx([]byte(`not-json`)); err == nil || !strings.Contains(err.Error(), "failed to parse simulated Monero transfer request") {
		t.Fatalf("expected parse error, got %v", err)
	}
	// a non-retryable faucet error returns immediately
	if _, err := c.Faucet(c.address, sdkmath.NewUint(1)); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected faucet error, got %v", err)
	}

	// helper unreachable
	c.helperEndpoint = "http://127.0.0.1:1"
	c.frostEndpoint = c.helperEndpoint
	if _, err := c.GetAccount(nil); err == nil {
		t.Fatal("expected transport error for balance")
	}
	if _, err := c.GetHeight(); err == nil {
		t.Fatal("expected transport error for height")
	}
	if _, _, err := c.GetVaultBalance(pk); err == nil {
		t.Fatal("expected transport error for vault balance")
	}

	// malformed URLs fail request construction
	if err := c.getJSON("://bad", "", nil); err == nil {
		t.Fatal("expected request error")
	}
	if err := c.postJSON("://bad", "", struct{}{}, nil); err == nil {
		t.Fatal("expected request error")
	}
	if err := c.postJSON(c.helperEndpoint, "", make(chan int), nil); err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestSignTxRejectsWrongChainOrAssetCov(t *testing.T) {
	c := &Client{}
	if _, err := c.SignTx(SimTx{Chain: common.BTCChain, Coin: common.NewCoin(common.XMRAsset, sdkmath.NewUint(1))}); err == nil || !strings.Contains(err.Error(), "unsupported chain") {
		t.Fatalf("expected chain error, got %v", err)
	}
	if _, err := c.SignTx(SimTx{Chain: common.XMRChain, Coin: common.NewCoin(common.BTCAsset, sdkmath.NewUint(1))}); err == nil || !strings.Contains(err.Error(), "unsupported asset") {
		t.Fatalf("expected asset error, got %v", err)
	}
}

func TestSpendableBalanceAtomicClampsCov(t *testing.T) {
	cases := []struct {
		in   frostBalanceResponse
		want uint64
	}{
		{frostBalanceResponse{Balance: 10, UnlockedBalance: 20}, 10},
		{frostBalanceResponse{Balance: 10, UnlockedBalance: 4}, 4},
		{frostBalanceResponse{Balance: 10, UnlockedBalance: 4, SpendableBalance: ptrUint64(7)}, 7},
		{frostBalanceResponse{Balance: 10, UnlockedBalance: 4, SpendableBalance: ptrUint64(70)}, 10},
	}
	for i, tc := range cases {
		if got := spendableBalanceAtomic(tc.in); got != tc.want {
			t.Errorf("case %d: got %d want %d", i, got, tc.want)
		}
	}
}

func TestDecodeResponseCov(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusTeapot, Body: io.NopCloser(strings.NewReader(" short and stout \n"))}
	err := decodeResponse(resp, nil)
	if err == nil || err.Error() != "http 418 from <unknown> <unknown>: short and stout" {
		t.Fatalf("unexpected error %v", err)
	}
	resp = &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ignored"))}
	if err = decodeResponse(resp, nil); err != nil {
		t.Fatalf("nil out must skip decoding, got %v", err)
	}
	resp = &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(errReaderCov{})}
	if err = decodeResponse(resp, nil); err == nil {
		t.Fatal("expected read error")
	}
}

type errReaderCov struct{}

func (errReaderCov) Read([]byte) (int, error) { return 0, errors.New("read failure") }

func TestKeyHelpersCov(t *testing.T) {
	if isRetryableFaucetError(nil) {
		t.Fatal("nil is not retryable")
	}
	if _, err := addressFromSpendKeyHex("zz"); err == nil || !strings.Contains(err.Error(), "failed to decode") {
		t.Fatalf("expected decode error, got %v", err)
	}
	if _, err := addressFromSpendKeyHex("abcd"); err == nil || !strings.Contains(err.Error(), "invalid XMR spend key length: 2") {
		t.Fatalf("expected length error, got %v", err)
	}
	if got := envOrDefault("XMR_COV_UNSET_VAR", "fb"); got != "fb" {
		t.Fatalf("expected fallback, got %s", got)
	}
}
