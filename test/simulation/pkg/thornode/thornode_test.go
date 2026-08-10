package thornode

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	sdk "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestGetSwapQuoteAddsExtendedQueryForUTXOSources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("extended"); got != "true" {
			t.Fatalf("expected extended=true query param, got %q", got)
		}
		if got := r.URL.Query().Get("destination"); got != "destination" {
			t.Fatalf("expected destination query param, got %q", got)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	originalURL := thornodeURL
	originalClient := httpClient
	defer func() {
		thornodeURL = originalURL
		httpClient = originalClient
	}()

	thornodeURL = server.URL
	httpClient = server.Client()

	if _, err := GetSwapQuote(common.BTCAsset, common.XMRAsset, sdk.NewUint(1), common.Address("destination"), common.Address("sender"), true); err != nil {
		t.Fatalf("expected swap quote request to succeed, got %v", err)
	}
}

func TestGetSwapQuoteOmitsExtendedQueryForNonUTXOSources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("extended"); got != "" {
			t.Fatalf("expected extended query param to be omitted, got %q", got)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	originalURL := thornodeURL
	originalClient := httpClient
	defer func() {
		thornodeURL = originalURL
		httpClient = originalClient
	}()

	thornodeURL = server.URL
	httpClient = server.Client()

	if _, err := GetSwapQuote(common.ETHAsset, common.XMRAsset, sdk.NewUint(1), common.Address("destination"), common.Address("sender"), false); err != nil {
		t.Fatalf("expected swap quote request to succeed, got %v", err)
	}
}

func TestGetMempoolSwapUsageTOR(t *testing.T) {
	newThornodeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/thorchain/network" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"mempool_swap_usage_tor":"12345678901234567890"}`))
	}))

	usage, err := GetMempoolSwapUsageTOR()
	if err != nil {
		t.Fatalf("expected mempool swap usage request to succeed, got %v", err)
	}
	if got, want := usage.String(), "12345678901234567890"; got != want {
		t.Fatalf("unexpected mempool swap usage: got %s, want %s", got, want)
	}
}

func TestGetMempoolSwapUsageTORRejectsInvalidValue(t *testing.T) {
	newThornodeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"mempool_swap_usage_tor":"not-a-number"}`))
	}))

	usage, err := GetMempoolSwapUsageTOR()
	if err == nil {
		t.Fatal("expected invalid mempool swap usage to fail")
	}
	if !usage.IsZero() {
		t.Fatalf("expected invalid mempool swap usage to return zero, got %s", usage)
	}
}

func TestGetMimirUsesEffectiveKeyEndpoint(t *testing.T) {
	paths := make(chan string, 1)
	newThornodeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.EscapedPath()
		_, _ = w.Write([]byte(`120`))
	}))

	value, err := GetMimir("SigningTransactionPeriod")
	if err != nil {
		t.Fatalf("expected effective Mimir request to succeed, got %v", err)
	}
	if value != 120 {
		t.Fatalf("unexpected effective Mimir value: got %d, want 120", value)
	}
	if got, want := <-paths, "/thorchain/mimir/key/SigningTransactionPeriod"; got != want {
		t.Fatalf("unexpected Mimir path: got %q, want %q", got, want)
	}
}

func TestGetTHORName(t *testing.T) {
	paths := make(chan string, 1)
	newThornodeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{
			"name":"sim affiliate",
			"preferred_asset":"THOR.RUNE",
			"affiliate_collector_rune":"123456789",
			"aliases":[]
		}`))
	}))

	thorname, err := GetTHORName("sim affiliate")
	if err != nil {
		t.Fatalf("expected THORName request to succeed, got %v", err)
	}
	if got, want := <-paths, "/thorchain/thorname/sim%20affiliate"; got != want {
		t.Fatalf("unexpected THORName path: got %q, want %q", got, want)
	}
	if got, want := thorname.GetName(), "sim affiliate"; got != want {
		t.Fatalf("unexpected THORName: got %q, want %q", got, want)
	}
	if got, want := thorname.GetAffiliateCollectorRune(), "123456789"; got != want {
		t.Fatalf("unexpected affiliate collector: got %q, want %q", got, want)
	}
}

func TestGetMempoolSwapQueueState(t *testing.T) {
	newThornodeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/thorchain/queue/staged":
			_, _ = w.Write([]byte(`{
				"staged_outbound_items":[{
					"tx_out_item":{
						"height":"45",
						"in_hash":"STAGED-TX",
						"chain":"BTC",
						"to_address":"bc1-staged",
						"coin":{"asset":"BTC.BTC","amount":"123","decimals":"8"},
						"max_gas":[{"asset":"BTC.BTC","amount":"7","decimals":"8"}],
						"gas_rate":"17"
					},
					"scheduled_height":"45",
					"staged_height":40
				}]
			}`))
		case "/thorchain/queue/outbound":
			_, _ = w.Write([]byte(`[{
				"in_hash":"OUTBOUND-TX",
				"chain":"BTC",
				"to_address":"bc1-outbound",
				"coin":{"asset":"BTC.BTC","amount":"456","decimals":8},
				"max_gas":[]
			}]`))
		case "/thorchain/queue/scheduled":
			_, _ = w.Write([]byte(`[{
				"height":99,
				"in_hash":"SCHEDULED-TX",
				"chain":"BTC",
				"to_address":"bc1-scheduled",
				"coin":{"asset":"BTC.BTC","amount":"789","decimals":8},
				"max_gas":[]
			}]`))
		case "/thorchain/queue/swap/details/SWAP-TX":
			_, _ = w.Write([]byte(`{
				"swap":{
					"target_asset":"BTC.BTC",
					"state":{"count":0,"in":"0","out":"0","failed_swaps":[]}
				},
				"status":"queued",
				"queue_type":"advanced"
			}`))
		case "/thorchain/block":
			_, _ = w.Write([]byte(`{"header":{"height":9876}}`))
		default:
			http.NotFound(w, r)
		}
	}))

	staged, err := GetStagedOutbounds()
	if err != nil {
		t.Fatalf("expected staged outbound request to succeed, got %v", err)
	}
	if len(staged.StagedOutboundItems) != 1 {
		t.Fatalf("expected one staged outbound, got %d", len(staged.StagedOutboundItems))
	}
	stagedItem := staged.StagedOutboundItems[0]
	if stagedItem.TxOutItem == nil || stagedItem.TxOutItem.GetInHash() != "STAGED-TX" {
		t.Fatalf("unexpected staged outbound item: %#v", stagedItem.TxOutItem)
	}
	if stagedItem.TxOutItem.GetHeight() != 45 || stagedItem.TxOutItem.GetGasRate() != 17 {
		t.Fatalf("unexpected staged tx numeric fields: %#v", stagedItem.TxOutItem)
	}
	if stagedItem.TxOutItem.Coin.GetDecimals() != 8 || len(stagedItem.TxOutItem.MaxGas) != 1 || stagedItem.TxOutItem.MaxGas[0].GetDecimals() != 8 {
		t.Fatalf("unexpected staged coin numeric fields: %#v", stagedItem.TxOutItem)
	}
	if stagedItem.GetScheduledHeight() != 45 || stagedItem.GetStagedHeight() != 40 {
		t.Fatalf("unexpected staged outbound heights: %#v", stagedItem)
	}

	outbounds, err := GetOutboundQueue()
	if err != nil {
		t.Fatalf("expected outbound queue request to succeed, got %v", err)
	}
	if len(outbounds) != 1 || outbounds[0].GetInHash() != "OUTBOUND-TX" {
		t.Fatalf("unexpected outbound queue: %#v", outbounds)
	}

	scheduled, err := GetScheduledOutboundQueue()
	if err != nil {
		t.Fatalf("expected scheduled outbound queue request to succeed, got %v", err)
	}
	if len(scheduled) != 1 || scheduled[0].GetInHash() != "SCHEDULED-TX" || scheduled[0].GetHeight() != 99 {
		t.Fatalf("unexpected scheduled outbound queue: %#v", scheduled)
	}

	details, err := GetSwapDetails("SWAP-TX")
	if err != nil {
		t.Fatalf("expected swap details request to succeed, got %v", err)
	}
	if details.GetStatus() != "queued" || details.GetQueueType() != "advanced" {
		t.Fatalf("unexpected swap details status: %#v", details)
	}
	if details.Swap == nil || details.Swap.State == nil {
		t.Fatalf("expected swap execution state, got %#v", details.Swap)
	}
	if details.Swap.State.GetCount() != 0 || details.Swap.State.GetIn() != "0" || details.Swap.State.GetOut() != "0" {
		t.Fatalf("unexpected swap execution progress: %#v", details.Swap.State)
	}

	height, err := GetBlockHeight()
	if err != nil {
		t.Fatalf("expected block height request to succeed, got %v", err)
	}
	if height != 9876 {
		t.Fatalf("unexpected block height: got %d, want 9876", height)
	}
}

func TestGetRetriesRetryableTimeout(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			time.Sleep(50 * time.Millisecond)
		}
		_, _ = w.Write([]byte(`{"ok":"true"}`))
	}))
	defer server.Close()

	originalClient := httpClient
	originalAttempts := thornodeHTTPRetryAttempts
	originalDelay := thornodeHTTPRetryDelay
	originalSleep := thornodeHTTPSleep
	defer func() {
		httpClient = originalClient
		thornodeHTTPRetryAttempts = originalAttempts
		thornodeHTTPRetryDelay = originalDelay
		thornodeHTTPSleep = originalSleep
	}()

	client := server.Client()
	client.Timeout = 10 * time.Millisecond
	httpClient = client
	thornodeHTTPRetryAttempts = 2
	thornodeHTTPRetryDelay = 0
	thornodeHTTPSleep = func(time.Duration) {}

	var got map[string]string
	if err := Get(server.URL, &got); err != nil {
		t.Fatalf("expected retryable timeout to recover, got %v", err)
	}
	if gotAttempts := attempts.Load(); gotAttempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", gotAttempts)
	}
	if got["ok"] != "true" {
		t.Fatalf("expected decoded response after retry, got %#v", got)
	}
}

func TestGetDoesNotRetryPermanentHTTPError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, `{"code":2,"message":"permanent"}`, http.StatusInternalServerError)
	}))
	defer server.Close()

	originalClient := httpClient
	originalAttempts := thornodeHTTPRetryAttempts
	originalDelay := thornodeHTTPRetryDelay
	originalSleep := thornodeHTTPSleep
	defer func() {
		httpClient = originalClient
		thornodeHTTPRetryAttempts = originalAttempts
		thornodeHTTPRetryDelay = originalDelay
		thornodeHTTPSleep = originalSleep
	}()

	httpClient = server.Client()
	thornodeHTTPRetryAttempts = 2
	thornodeHTTPRetryDelay = 0
	thornodeHTTPSleep = func(time.Duration) {}

	var got map[string]string
	if err := Get(server.URL, &got); err == nil {
		t.Fatal("expected permanent HTTP 500 response to fail")
	}
	if attempts != 1 {
		t.Fatalf("expected permanent HTTP error to avoid retries, got %d attempts", attempts)
	}
}

func TestGetOutboundFeeRequiresValidAssetFee(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		wantError      bool
	}{
		{"matching", `[{"asset":"BASE.ETH","outbound_fee":"48883897"}]`, false},
		{"case insensitive", `[{"asset":"base.eth","outbound_fee":"48883897"}]`, false},
		{"missing", `[]`, true},
		{"wrong asset", `[{"asset":"ETH.ETH","outbound_fee":"48883897"}]`, true},
		{"zero", `[{"asset":"BASE.ETH","outbound_fee":"0"}]`, true},
		{"invalid", `[{"asset":"BASE.ETH","outbound_fee":"bad"}]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/thorchain/outbound_fee/BASE.ETH" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()
			originalURL := thornodeURL
			thornodeURL = server.URL
			defer func() { thornodeURL = originalURL }()
			fee, err := GetOutboundFee(common.BASEChain.GetGasAsset())
			if (err != nil) != tc.wantError {
				t.Fatalf("fee = %s, error = %v, want error %v", fee, err, tc.wantError)
			}
			if !tc.wantError && !fee.Equal(sdk.NewUint(48_883_897)) {
				t.Fatalf("fee = %s", fee)
			}
		})
	}
}

// Refund addresses belong to the source chain, independently of the destination.
// Native XMR requires one; representations that settle on THORChain do not.
func TestGetSwapQuoteRefundAddressContract(t *testing.T) {
	const sourceAddress = "44e1sJc2gWfJM8PLmu4XWcAAQUK5q8uAF1E7qYPKzGuU7y5TPokcGiU6oAXAxDJXbPbGME2xWBgFdSUzrxhiKLiqGfv3C1e"
	for _, tc := range []struct {
		name, from, refund string
		wantRefund         bool
		wantError          bool
	}{
		{"native XMR", "XMR.XMR", sourceAddress, true, false},
		{"missing native XMR source", "XMR.XMR", "", false, true},
		{"XMR synth", "XMR/XMR", "sender", false, false},
		{"XMR trade asset", "XMR~XMR", "sender", false, false},
		{"BTC unchanged", "BTC.BTC", "sender", false, false},
		{"ETH unchanged", "ETH.ETH", "sender", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asset, err := common.NewAsset(tc.from)
			if err != nil {
				t.Fatal(err)
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				params := r.URL.Query()
				if params.Has("refund_address") != tc.wantRefund {
					t.Errorf("refund parameter presence = %v, want %v", params.Has("refund_address"), tc.wantRefund)
				}
				if tc.wantRefund && params.Get("refund_address") != sourceAddress {
					t.Errorf("refund address = %q, want source address", params.Get("refund_address"))
				}
				if params.Get("destination") != "destination" {
					t.Errorf("destination changed: %q", params.Get("destination"))
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			originalURL, originalClient := thornodeURL, httpClient
			thornodeURL, httpClient = server.URL, server.Client()
			defer func() { thornodeURL, httpClient = originalURL, originalClient }()

			_, err = GetSwapQuote(asset, common.BTCAsset, sdk.NewUint(1), common.Address("destination"), common.Address(tc.refund), false)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "refund address is required") || requests != 0 {
					t.Fatalf("missing source address: error = %v, requests = %d", err, requests)
				}
			} else if err != nil || requests != 1 {
				t.Fatalf("quote: error = %v, requests = %d", err, requests)
			}
		})
	}
}

func TestGetSwapQuoteRetryPreservesXMRRefundAddress(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.URL.Query().Get("refund_address"); got != "source-address" {
			t.Errorf("attempt %d refund address = %q", requests, got)
		}
		if requests == 1 {
			http.Error(w, "temporary timeout", http.StatusGatewayTimeout)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	originalURL, originalClient := thornodeURL, httpClient
	originalDelay := thornodeHTTPRetryDelay
	thornodeURL, httpClient = server.URL, server.Client()
	thornodeHTTPRetryDelay = 0
	defer func() {
		thornodeURL, httpClient = originalURL, originalClient
		thornodeHTTPRetryDelay = originalDelay
	}()

	_, err := GetSwapQuote(common.XMRAsset, common.BTCAsset, sdk.NewUint(1), common.Address("destination"), common.Address("source-address"), false)
	if err != nil || requests != 2 {
		t.Fatalf("retried quote: error = %v, requests = %d", err, requests)
	}
}

func TestGetInboundAddressDistinguishesAbsenceFromFailedLookup(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		status       int
		closeServer  bool
		wantError    bool
		wantNotFound bool
	}{
		{name: "existing", body: `[{"chain":"XMR","address":"existing-XMR-vault"}]`},
		{name: "absent", body: `[]`, wantError: true, wantNotFound: true},
		{name: "other chain", body: `[{"chain":"BTC","address":"BTC-vault"}]`, wantError: true, wantNotFound: true},
		{name: "server error", body: `unavailable`, status: http.StatusServiceUnavailable, wantError: true},
		{name: "transport error", closeServer: true, wantError: true},
		{name: "malformed JSON", body: `[`, wantError: true},
		{name: "null response", body: `null`, wantError: true},
		{name: "missing chain", body: `[{"address":"unknown-vault"}]`, wantError: true},
		{name: "missing address", body: `[{"chain":"XMR"}]`, wantError: true},
		{name: "empty address", body: `[{"chain":"XMR","address":""}]`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/thorchain/inbound_addresses" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			if tc.closeServer {
				server.Close()
			}
			originalURL, originalClient := thornodeURL, httpClient
			originalAttempts := thornodeHTTPRetryAttempts
			thornodeURL, httpClient = server.URL, server.Client()
			thornodeHTTPRetryAttempts = 1
			defer func() {
				thornodeURL, httpClient = originalURL, originalClient
				thornodeHTTPRetryAttempts = originalAttempts
			}()

			address, _, err := GetInboundAddress(common.XMRChain)
			if (err != nil) != tc.wantError || errors.Is(err, ErrInboundAddressNotFound) != tc.wantNotFound {
				t.Fatalf("lookup error = %v, want error=%v, not found=%v", err, tc.wantError, tc.wantNotFound)
			}
			if !tc.wantError && address != common.Address("existing-XMR-vault") {
				t.Fatalf("unexpected inbound address %s", address)
			}
		})
	}
}

func TestGetTxSpentRefsUsesStoredOutboundVoter(t *testing.T) {
	for _, tc := range []struct {
		name, status, individualStatus string
		consensusHeight                int64
		reverted, missingStored        bool
	}{
		{name: "processed before final observation", status: "done", consensusHeight: 718},
		{name: "late final observation", consensusHeight: 718},
		{name: "unprocessed stored voter", individualStatus: "done"},
		{name: "reverted stored voter", status: "reverted", individualStatus: "done", consensusHeight: 718, reverted: true},
		{name: "missing stored voter", missingStored: true, individualStatus: "done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/thorchain/tx/details/outbound" || r.URL.Query().Get("outbound") != "true" {
					t.Errorf("wrong outbound voter endpoint: %s", r.URL.RequestURI())
					http.NotFound(w, r)
					return
				}
				// The endpoint omits status for incomplete observations.
				storedStatus, individualStatus := "", ""
				if tc.status != "" {
					storedStatus = fmt.Sprintf(`,"status":%q`, tc.status)
				}
				if tc.individualStatus != "" {
					individualStatus = fmt.Sprintf(`,"status":%q`, tc.individualStatus)
				}
				stored := fmt.Sprintf(`{"tx":{"id":"outbound","chain":"XMR"},"observed_pub_key":"frost-vault"%s,"spent_output_refs":[{"tx_hash":"source","output_index":0,"key_image":"image","spend_tx_hash":"outbound"}]}`, storedStatus)
				if tc.missingStored {
					stored = "null"
				}
				// Individual votes are not the stored processing outcome.
				_, _ = fmt.Fprintf(w, `{"tx":%s,"consensus_height":%d,"reverted":%t,"txs":[{"tx":{"id":"outbound","chain":"XMR"}%s}]}`, stored, tc.consensusHeight, tc.reverted, individualStatus)
			}))
			defer server.Close()
			originalURL, originalClient := thornodeURL, httpClient
			defer func() { thornodeURL, httpClient = originalURL, originalClient }()
			thornodeURL, httpClient = server.URL, server.Client()
			got, err := GetTxSpentRefs("outbound")
			if err != nil {
				t.Fatal(err)
			}
			if got.ConsensusHeight != tc.consensusHeight || got.Reverted != tc.reverted {
				t.Fatalf("lost stored consensus/reversion metadata: %+v", got)
			}
			tx := got.ObservedTx
			if tc.missingStored {
				if tx.Tx.GetId() != "" || tx.Status != "" || len(tx.SpentOutputRefs) != 0 {
					t.Fatalf("individual vote supplied missing stored evidence: %+v", tx)
				}
				return
			}
			if tx.Tx.GetId() != "outbound" || tx.Tx.GetChain() != "XMR" || tx.ObservedPubKey != "frost-vault" || tx.Status != tc.status || len(tx.SpentOutputRefs) != 1 {
				t.Fatalf("incomplete stored observation: %+v", tx)
			}
			ref := tx.SpentOutputRefs[0]
			if ref.TxHash != "source" || ref.OutputIndex != 0 || ref.KeyImage != "image" || ref.SpendTxHash != "outbound" {
				t.Fatalf("lost reference identity: %+v", ref)
			}
		})
	}
}

func newThornodeTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(handler)
	originalURL := thornodeURL
	originalClient := httpClient
	thornodeURL = server.URL
	httpClient = server.Client()
	t.Cleanup(func() {
		server.Close()
		thornodeURL = originalURL
		httpClient = originalClient
	})
	return server
}
