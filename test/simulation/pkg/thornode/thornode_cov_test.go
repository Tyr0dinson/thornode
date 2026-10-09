package thornode

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
)

// withRoutesCov serves the given path->body routes (404 otherwise) and points
// the package at the fake server for the duration of the test.
func withRoutesCov(t *testing.T, routes map[string]string) *[]string {
	t.Helper()
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		body, ok := routes[r.URL.RequestURI()]
		if !ok {
			body, ok = routes[r.URL.Path]
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	origURL, origClient := thornodeURL, httpClient
	origSleep := thornodeHTTPSleep
	thornodeURL = server.URL
	httpClient = server.Client()
	thornodeHTTPSleep = func(time.Duration) {}
	t.Cleanup(func() {
		server.Close()
		thornodeURL, httpClient = origURL, origClient
		thornodeHTTPSleep = origSleep
	})
	return &seen
}

func TestBaseURLCov(t *testing.T) {
	withRoutesCov(t, nil)
	if BaseURL() != thornodeURL || !strings.HasPrefix(BaseURL(), "http://") {
		t.Fatalf("unexpected base url %q", BaseURL())
	}
}

func TestGetBalancesCov(t *testing.T) {
	withRoutesCov(t, map[string]string{
		"/cosmos/bank/v1beta1/balances/good": `{"balances":[{"denom":"rune","amount":"100"},{"denom":"btc/btc","amount":"5"}]}`,
		"/cosmos/bank/v1beta1/balances/bad":  `{"balances":[{"denom":"rune","amount":"x"}]}`,
		"/cosmos/bank/v1beta1/balances/bada": `{"balances":[{"denom":"a.b.c.d-","amount":"1"}]}`,
	})
	coins, err := GetBalances("good")
	if err != nil {
		t.Fatal(err)
	}
	if len(coins) != 2 || !coins[0].Asset.Equals(common.RuneNative) || coins[0].Amount.Uint64() != 100 {
		t.Fatalf("unexpected coins %v", coins)
	}
	if coins[1].Amount.Uint64() != 5 || coins[1].Asset.String() != "BTC/BTC" {
		t.Fatalf("unexpected second coin %v", coins[1])
	}
	if _, err = GetBalances("bad"); err == nil {
		t.Fatal("expected parse error")
	}
	if _, err = GetBalances("bada"); err == nil {
		t.Fatal("expected asset error")
	}
	if _, err = GetBalances("missing"); err == nil || !strings.Contains(err.Error(), "HTTP: 404") {
		t.Fatalf("expected 404 error, got %v", err)
	}
}

func TestInboundAddressQueriesCov(t *testing.T) {
	withRoutesCov(t, map[string]string{
		"/thorchain/inbound_addresses": `[
			{"chain":"BTC","address":"btc-addr","pub_key":"` + testPubKeyCov(t) + `"},
			{"chain":"ETH","address":"eth-addr","router":"0xrouter","pub_key":""}
		]`,
	})

	pk, err := GetInboundAddressPubKey(common.BTCChain)
	if err != nil || pk.String() != testPubKeyCov(t) {
		t.Fatalf("unexpected pubkey %s %v", pk, err)
	}
	if _, err = GetInboundAddressPubKey(common.ETHChain); err == nil {
		t.Fatal("expected empty pubkey error")
	}
	if _, err = GetInboundAddressPubKey(common.DOGEChain); err == nil {
		t.Fatal("expected missing chain error")
	}

	router, err := GetRouterAddress(common.ETHChain)
	if err != nil || router != "0xrouter" {
		t.Fatalf("unexpected router %s %v", router, err)
	}
	if _, err = GetRouterAddress(common.DOGEChain); err == nil {
		t.Fatal("expected missing router chain error")
	}

	addr, r, err := GetInboundAddress(common.ETHChain)
	if err != nil || addr != "eth-addr" || r == nil || *r != "0xrouter" {
		t.Fatalf("unexpected inbound %s %v %v", addr, r, err)
	}
}

func TestInboundAddressQueryErrorsCov(t *testing.T) {
	withRoutesCov(t, nil)
	if _, err := GetInboundAddressPubKey(common.BTCChain); err == nil {
		t.Fatal("expected http error")
	}
	if _, err := GetRouterAddress(common.BTCChain); err == nil {
		t.Fatal("expected http error")
	}
	if _, _, err := GetInboundAddress(common.BTCChain); err == nil {
		t.Fatal("expected http error")
	}
}

func TestSimpleGettersCov(t *testing.T) {
	seen := withRoutesCov(t, map[string]string{
		"/thorchain/pool/BTC.BTC/liquidity_providers": `[{"asset":"BTC.BTC","units":"7"}]`,
		"/thorchain/pools":                            `[{"asset":"BTC.BTC","balance_rune":"10","balance_asset":"5"}]`,
		"/thorchain/pool/BTC.BTC":                     `{"asset":"BTC.BTC","balance_rune":"10"}`,
		"/thorchain/vault/pk1":                        `{"pub_key":"pk1","status":"InactiveVault"}`,
		"/thorchain/vaults/asgard":                    `[{"pub_key":"pk2","status":"ActiveVault","vault_creation_height_xmr":77,"pub_key_frost":"f"}]`,
		"/thorchain/network":                          `{"bond_reward_rune":"12"}`,
		"/thorchain/nodes":                            `[{"node_address":"thor1x","status":"Active"}]`,
		"/thorchain/tx/stages/abc":                    `{"inbound_observed":{"completed":true}}`,
		"/thorchain/tx/details/abc":                   `{"tx_id":"abc"}`,
		"/thorchain/xmr/spent_refs/pk":                `{"spent_output_refs":[]}`,
		"/thorchain/tx/abc":                           `{"observed_tx":{"observed_pub_key":"opk","status":"done"}}`,
		"/thorchain/mimir":                            `{"HALTCHAINGLOBAL":1,"CHURNINTERVAL":43200}`,
		"/thorchain/block":                            `{"id":{"hash":"latest"}}`,
		"/thorchain/block?height=12":                  `{"id":{"hash":"h12"}}`,
		"/thorchain/memo/abc":                         `{"memo":"=:BTC.BTC:addr"}`,
	})

	lps, err := GetLiquidityProviders(common.BTCAsset)
	if err != nil || len(lps) != 1 || lps[0].Units != "7" {
		t.Fatalf("lps %v %v", lps, err)
	}
	pools, err := GetPools()
	if err != nil || len(pools) != 1 || pools[0].BalanceRune != "10" {
		t.Fatalf("pools %v %v", pools, err)
	}
	pool, err := GetPool(common.BTCAsset)
	if err != nil || pool.Asset != "BTC.BTC" {
		t.Fatalf("pool %v %v", pool, err)
	}
	vault, err := GetVault("pk1")
	if err != nil || vault.Status != "InactiveVault" {
		t.Fatalf("vault %v %v", vault, err)
	}
	vaults, err := GetVaults()
	if err != nil || len(vaults) != 1 || vaults[0].Status != "ActiveVault" {
		t.Fatalf("vaults %v %v", vaults, err)
	}
	network, err := GetNetwork()
	if err != nil || network.BondRewardRune != "12" {
		t.Fatalf("network %v %v", network, err)
	}
	nodes, err := GetNodes()
	if err != nil || len(nodes) != 1 || nodes[0].NodeAddress != "thor1x" {
		t.Fatalf("nodes %v %v", nodes, err)
	}
	stages, err := GetTxStages("abc")
	if err != nil || stages.InboundObserved.Completed == false {
		t.Fatalf("stages %v %v", stages, err)
	}
	details, err := GetTxDetails("abc")
	if err != nil || details.TxId == nil || *details.TxId != "abc" {
		t.Fatalf("details %v %v", details, err)
	}
	states, err := GetVaultXMRStates()
	if err != nil || len(states) != 1 || states[0].PubKeyFrost != "f" || states[0].VaultCreationHeightXmr != 77 || states[0].Status != "ActiveVault" {
		t.Fatalf("states %v %v", states, err)
	}
	refs, err := GetXMRSpentOutputRefs("pk")
	if err != nil || len(refs.SpentOutputRefs) != 0 {
		t.Fatalf("refs %v %v", refs, err)
	}
	txRefs, err := GetTxSpentRefs("abc")
	if err != nil || txRefs.ObservedTx.ObservedPubKey != "opk" || txRefs.ObservedTx.Status != "done" {
		t.Fatalf("tx refs %v %v", txRefs, err)
	}
	mimirs, err := GetMimirs()
	if err != nil || mimirs["CHURNINTERVAL"] != 43200 || mimirs["HALTCHAINGLOBAL"] != 1 {
		t.Fatalf("mimirs %v %v", mimirs, err)
	}
	block, err := GetBlock(0)
	if err != nil || block.Id.Hash != "latest" {
		t.Fatalf("block %v %v", block, err)
	}
	block, err = GetBlock(12)
	if err != nil || block.Id.Hash != "h12" {
		t.Fatalf("block 12 %v %v", block, err)
	}
	memo, err := GetMemoHash("abc")
	if err != nil || memo.Memo != "=:BTC.BTC:addr" {
		t.Fatalf("memo %v %v", memo, err)
	}

	// GetBlock must request the height query param only when positive
	found := false
	for _, s := range *seen {
		if s == "/thorchain/block?height=12" {
			found = true
		}
	}
	if !found {
		t.Fatalf("height query not sent: %v", *seen)
	}
}

func TestGetOutboundFeeInvalidAmountsCov(t *testing.T) {
	withRoutesCov(t, map[string]string{
		"/thorchain/outbound_fee/BTC.BTC": `[{"asset":"BTC.BTC","outbound_fee":"0"}]`,
		"/thorchain/outbound_fee/ETH.ETH": `[{"asset":"BTC.BTC","outbound_fee":"5"},{"asset":"eth.eth","outbound_fee":"9"}]`,
	})
	if _, err := GetOutboundFee(common.BTCAsset); err == nil || !strings.Contains(err.Error(), "invalid outbound fee") {
		t.Fatalf("zero fee must be rejected, got %v", err)
	}
	fee, err := GetOutboundFee(common.ETHAsset)
	if err != nil || fee.Uint64() != 9 {
		t.Fatalf("expected case-insensitive match fee 9, got %v %v", fee, err)
	}
	if _, err = GetOutboundFee(common.DOGEAsset); err == nil {
		t.Fatal("expected http error for unknown route")
	}
}

func TestGetSwapQuoteRequiresRefundAddressCov(t *testing.T) {
	withRoutesCov(t, nil)
	_, err := GetSwapQuote(common.XMRAsset, common.BTCAsset, sdkOneCov(), "dest", "", false)
	if common.XMRChain.RequiresExplicitRefundAddress() {
		if err == nil || !strings.Contains(err.Error(), "refund address is required") {
			t.Fatalf("expected refund address error, got %v", err)
		}
	} else if err == nil {
		t.Fatal("expected http error")
	}
}

func TestGetOnceErrorResponseAndRawBytesCov(t *testing.T) {
	withRoutesCov(t, map[string]string{
		"/err": `{"code":3,"message":"bad thing"}`,
		"/raw": `raw-bytes`,
		"/bad": `{not json`,
	})
	var out map[string]interface{}
	err := Get(thornodeURL+"/err", &out)
	if err == nil || err.Error() != "code: 3, message: bad thing" {
		t.Fatalf("expected decoded error response, got %v", err)
	}
	var raw []byte
	if err = Get(thornodeURL+"/raw", &raw); err != nil || string(raw) != "raw-bytes" {
		t.Fatalf("raw %q %v", raw, err)
	}
	if err = Get(thornodeURL+"/bad", &out); err == nil {
		t.Fatal("expected json error")
	}
}

func TestGetOnceTransportErrorCov(t *testing.T) {
	withRoutesCov(t, nil)
	httpClient = &http.Client{Transport: roundTripFuncCov(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("permanent transport failure")
	})}
	if err := Get(thornodeURL+"/x", new([]byte)); err == nil || !strings.Contains(err.Error(), "permanent transport failure") {
		t.Fatalf("expected transport error, got %v", err)
	}
	httpClient = &http.Client{Transport: roundTripFuncCov(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(errReaderCov{})}, nil
	})}
	if err := Get(thornodeURL+"/x", new([]byte)); err == nil || !strings.Contains(err.Error(), "read failure") {
		t.Fatalf("expected body read error, got %v", err)
	}
}

func TestIsRetryableNilCov(t *testing.T) {
	if isRetryableThornodeGetError(nil) {
		t.Fatal("nil error must not be retryable")
	}
}

func TestGetInactiveVaultsCov(t *testing.T) {
	withRoutesCov(t, map[string]string{
		"/thorchain/vaults/pubkeys": `{"asgard":[],"inactive":[{"pub_key":"pk1","routers":[]}]}`,
		"/thorchain/vault/pk1":      `{"pub_key":"pk1","status":"InactiveVault"}`,
	})
	vaults, err := GetInactiveVaults()
	if err != nil || len(vaults) != 1 || vaults[0].Status != "InactiveVault" {
		t.Fatalf("vaults %v %v", vaults, err)
	}
}

func TestConvertAssetAmountCov(t *testing.T) {
	withRoutesCov(t, map[string]string{
		"/thorchain/pools": `[
			{"asset":"BTC.BTC","balance_rune":"1000","balance_asset":"10"},
			{"asset":"ETH.ETH","balance_rune":"500","balance_asset":"50"}
		]`,
	})
	// 2 BTC -> 200 RUNE -> 20 ETH
	got, err := ConvertAssetAmount(openapi.Coin{Asset: "BTC.BTC", Amount: "2"}, "ETH.ETH")
	if err != nil || got.Uint64() != 20 {
		t.Fatalf("expected 20, got %v %v", got, err)
	}
	if _, err = ConvertAssetAmount(openapi.Coin{Asset: "DOGE.DOGE", Amount: "2"}, "ETH.ETH"); err == nil || err.Error() != "source asset not found" {
		t.Fatalf("expected source error, got %v", err)
	}
	if _, err = ConvertAssetAmount(openapi.Coin{Asset: "BTC.BTC", Amount: "2"}, "DOGE.DOGE"); err == nil || err.Error() != "target asset not found" {
		t.Fatalf("expected target error, got %v", err)
	}
}

func TestConvertAssetAmountPoolsErrorCov(t *testing.T) {
	withRoutesCov(t, nil)
	got, err := ConvertAssetAmount(openapi.Coin{Asset: "BTC.BTC", Amount: "2"}, "ETH.ETH")
	if err == nil || !got.IsZero() {
		t.Fatalf("expected pools error and zero, got %v %v", got, err)
	}
}

var pubKeyCov string

func testPubKeyCov(t *testing.T) string {
	t.Helper()
	if pubKeyCov == "" {
		pk := secp256k1.GenPrivKeyFromSecret([]byte("thornode-cov")).PubKey()
		s, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, pk)
		if err != nil {
			t.Fatal(err)
		}
		pubKeyCov = s
	}
	return pubKeyCov
}

func sdkOneCov() cosmos.Uint { return cosmos.NewUint(1) }

type roundTripFuncCov func(*http.Request) (*http.Response, error)

func (f roundTripFuncCov) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errReaderCov struct{}

func (errReaderCov) Read([]byte) (int, error) { return 0, errors.New("read failure") }

// Documents current behaviour: THORNode's querier marshals int64 fields as
// JSON strings, but VaultXMRState.VaultCreationHeightXmr has no ",string" tag,
// so a non-zero string-encoded height fails to decode (suspected bug).
func TestGetVaultXMRStatesStringHeightCov(t *testing.T) {
	withRoutesCov(t, map[string]string{
		"/thorchain/vaults/asgard": `[{"pub_key_frost":"f","vault_creation_height_xmr":"123"}]`,
	})
	if _, err := GetVaultXMRStates(); err == nil {
		t.Fatal("expected string-encoded int64 to fail decoding")
	}
}
