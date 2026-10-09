package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	bank "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
	protov2 "google.golang.org/protobuf/proto"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/util"
	"gitlab.com/thorchain/thornode/v3/tools/thorscan"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Harness
////////////////////////////////////////////////////////////////////////////////////////

// actCovTx is a minimal sdk.Tx that also carries a memo (ctypes.TxWithMemo).
type actCovTx struct {
	msgs []sdk.Msg
	memo string
}

func (tx actCovTx) GetMsgs() []sdk.Msg                 { return tx.msgs }
func (actCovTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }
func (tx actCovTx) GetMemo() string                    { return tx.memo }
func actCovPtr[T any](v T) *T                          { return &v }
func actCovCode(c int64) openapi.BlockTxResult         { return openapi.BlockTxResult{Code: actCovPtr(c)} }
func actCovHeader(h int64) openapi.BlockResponseHeader { return openapi.BlockResponseHeader{Height: h} }
func actCovBlockResp(h int64) openapi.BlockResponse {
	return openapi.BlockResponse{Header: actCovHeader(h)}
}

func actCovJSON(t *testing.T, v any) string {
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
func actCovMsgs(msgs ...sdk.Msg) []sdk.Msg                         { return msgs }
func actCovEvents(events ...map[string]string) []map[string]string { return events }

type actCovNote struct {
	hook string
	text string
}

type actCovEnv struct {
	t         *testing.T
	mu        sync.Mutex
	responses map[string]string // path (without leading slash) -> JSON body
	requests  map[string]int
	notes     []actCovNote
}

// actCovSetup isolates global config, storage, cache, vaults and the failed tx
// limiter, and routes thornode queries and slack webhooks to local servers.
func actCovSetup(t *testing.T) *actCovEnv {
	t.Helper()
	env := &actCovEnv{t: t, responses: map[string]string{}, requests: map[string]int{}}

	thornode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.mu.Lock()
		defer env.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/")
		env.requests[path]++
		body, ok := env.responses[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(thornode.Close)

	hooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := map[string]string{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&data))
		env.mu.Lock()
		env.notes = append(env.notes, actCovNote{hook: strings.TrimPrefix(r.URL.Path, "/"), text: data["text"]})
		env.mu.Unlock()
	}))
	t.Cleanup(hooks.Close)

	orig := config.Get()
	origVaults := vaults
	origLimiter := failedTxLimiter
	t.Cleanup(func() {
		config.Set(orig)
		util.InitCache()
		vaults = origVaults
		failedTxLimiter = origLimiter
	})

	cfg := orig
	cfg.StoragePath = t.TempDir()
	cfg.MaxRetries = 0
	cfg.Console = false
	cfg.Network = "mainnet"
	cfg.Endpoints.Thornode = thornode.URL
	cfg.Endpoints.CacheSize = 100
	cfg.Notifications.Activity = config.Webhooks{Category: "Activity", Slack: hooks.URL + "/activity"}
	cfg.Notifications.Security = config.Webhooks{Category: "Security", Slack: hooks.URL + "/security"}
	cfg.Notifications.FailedRefunds = config.Webhooks{Category: "Failed Refunds", Slack: hooks.URL + "/refunds"}
	config.Set(cfg)
	util.InitCache()
	vaults = &Vaults{Active: map[string]bool{}, Retiring: map[string]bool{}}
	failedTxLimiter = rate.NewLimiter(rate.Inf, 1)

	// default prices: RUNE = $2, BTC = $100k, ETH = $1k, USDC = $1
	env.set("thorchain/network", `{"rune_price_in_tor":"200000000","tor_price_in_rune":"50000000"}`)
	env.set("thorchain/pools", `[
		{"asset":"BTC.BTC","asset_tor_price":"10000000000000","balance_rune":"5000000000000","balance_asset":"100000000"},
		{"asset":"ETH.ETH","asset_tor_price":"100000000000","balance_rune":"50000000000","balance_asset":"100000000"},
		{"asset":"ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48","asset_tor_price":"100000000","balance_rune":"50000000","balance_asset":"100000000"}
	]`)
	env.set("thorchain/mimir", `{}`)
	return env
}

func (e *actCovEnv) set(path, body string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.responses[path] = body
}

func (e *actCovEnv) reqs(path string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.requests[path]
}

func (e *actCovEnv) take() []actCovNote {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := e.notes
	e.notes = nil
	return n
}

func actCovBTC(amount uint64) common.Coin {
	return common.NewCoin(common.BTCAsset, cosmos.NewUint(amount))
}

func actCovInbound(memo string, coin common.Coin, blockHeight, finaliseHeight int64) *types.MsgObservedTxQuorum {
	return &types.MsgObservedTxQuorum{QuoTx: &common.QuorumTx{
		Inbound: true,
		ObsTx: common.ObservedTx{
			Tx: common.Tx{
				ID:          types.GetRandomTxHash(),
				Chain:       coin.Asset.Chain,
				FromAddress: common.Address("bc1qfromaddr"),
				ToAddress:   common.Address("bc1qvaultaddr"),
				Coins:       common.Coins{coin},
				Memo:        memo,
			},
			BlockHeight:    blockHeight,
			FinaliseHeight: finaliseHeight,
			ObservedPubKey: common.PubKey("inactive-pubkey"),
		},
	}}
}

func actCovTxBlock(height int64, txs ...thorscan.BlockTx) *thorscan.BlockResponse {
	return &thorscan.BlockResponse{BlockResponse: actCovBlockResp(height), Txs: txs}
}

func actCovBlockTx(code int64, tx sdk.Tx, events ...map[string]string) thorscan.BlockTx {
	res := actCovCode(code)
	res.Events = events
	return thorscan.BlockTx{BlockTx: openapi.BlockTx{Hash: "ACTCOVHASH", Result: res}, Tx: tx}
}

////////////////////////////////////////////////////////////////////////////////////////
// Large Unconfirmed Inbounds
////////////////////////////////////////////////////////////////////////////////////////

func TestActCovLargeUnconfirmedInbounds(t *testing.T) {
	env := actCovSetup(t)

	big := actCovInbound("=:ETH.ETH:0xabc", actCovBTC(300_000_000), 100, 101) // $300k, 10 min confirm
	danger := actCovInbound("=:ETH.ETH:0xdef", actCovBTC(20_000_000_000), 100, 110)
	outbound := actCovInbound("", actCovBTC(300_000_000), 100, 101)
	outbound.QuoTx.Inbound = false
	block := actCovTxBlock(1001,
		actCovBlockTx(1, actCovTx{msgs: actCovMsgs(big)}), // failed tx skipped
		actCovBlockTx(0, nil),                             // failed decode skipped
		actCovBlockTx(0, actCovTx{msgs: actCovMsgs(
			&thorchain.MsgSend{}, // not observed tx
			outbound,             // not inbound
			actCovInbound("MIGRATE:5", actCovBTC(300_000_000), 100, 101),                 // migration
			actCovInbound("consolidate", actCovBTC(300_000_000), 100, 101),               // consolidate
			actCovInbound("TRADE+:thor1abc", actCovBTC(300_000_000), 100, 101),           // trade deposit
			actCovInbound("=:ETH.ETH:0xabc", actCovBTC(100_000_000), 100, 101),           // below threshold
			actCovInbound("=:ETH.ETH:0xabc", actCovBTC(300_000_000), 100, 100),           // fast confirm
			actCovInbound("NOTAMEMO:foo", actCovBTC(300_000_000), 100, 100),              // unparsable memo, fast
			actCovInbound(testMemoInactiveVaultRefund, actCovBTC(300_000_000), 100, 101), // not mocknet -> alerts
			big,
			danger,
		)}),
	)

	LargeUnconfirmedInbounds(block)
	notes := env.take()

	activity, security := 0, 0
	for _, n := range notes {
		require.Contains(t, n.text, "Large Unconfirmed Inbound")
		switch n.hook {
		case "activity":
			activity++
		case "security":
			security++
		}
	}
	require.Equal(t, 3, activity, "test memo (mainnet), big and danger inbounds alert")
	require.Equal(t, 1, security, "only the >$10M inbound reaches security")

	var bigText string
	for _, n := range notes {
		if strings.Contains(n.text, big.QuoTx.ObsTx.Tx.ID.String()) {
			bigText = n.text
		}
	}
	require.Contains(t, bigText, "*Confirmation Time*: 10m")
	require.Contains(t, bigText, "3.000000 BTC.BTC ($300,000.00)")
	require.Contains(t, bigText, "`=:ETH.ETH:0xabc`")

	// seen inbounds never alert twice
	LargeUnconfirmedInbounds(block)
	require.Empty(t, env.take(), "previously seen inbounds are skipped")
}

func TestActCovLargeUnconfirmedInboundsCloutAndMocknet(t *testing.T) {
	env := actCovSetup(t)

	// clout raises the threshold above the inbound value
	env.set("thorchain/clout/swap/bc1qfromaddr", `{"address":"bc1qfromaddr","score":"10000000000000"}`)
	LargeUnconfirmedInbounds(actCovTxBlock(10, actCovBlockTx(0, actCovTx{msgs: actCovMsgs(
		actCovInbound("=:ETH.ETH:0xabc", actCovBTC(300_000_000), 100, 101),
	)})))
	require.Empty(t, env.take())

	// test memo is skipped on mocknet
	cfg := config.Get()
	cfg.Network = "mocknet"
	config.Set(cfg)
	env.set("thorchain/clout/swap/bc1qfromaddr", `{}`)
	LargeUnconfirmedInbounds(actCovTxBlock(20, actCovBlockTx(0, actCovTx{msgs: actCovMsgs(
		actCovInbound(testMemoInactiveVaultRefund, actCovBTC(300_000_000), 100, 101),
	)})))
	require.Empty(t, env.take())
}

func TestActCovLargeUnconfirmedInboundsMemoless(t *testing.T) {
	env := actCovSetup(t)

	// gas asset memoless with registered reference 12345
	env.set("thorchain/memo/BTC.BTC/12345", `{"memo":"=:ETH.ETH:0xregistered"}`)
	gas := actCovInbound("", actCovBTC(300_012_345), 100, 101)

	// token memoless (6 decimals) normalised before extracting reference 777
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	require.NoError(t, err)
	env.set("thorchain/pool/ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", `{"asset":"ETH.USDC","decimals":6}`)
	token := actCovInbound("", common.NewCoin(usdc, cosmos.NewUint(30_000_000_077_700)), 100, 120)

	// token without a registered reference keeps the raw memo
	noRef := actCovInbound("", common.NewCoin(usdc, cosmos.NewUint(30_000_000_000_100)), 100, 120)

	LargeUnconfirmedInbounds(actCovTxBlock(55, actCovBlockTx(0, actCovTx{msgs: actCovMsgs(gas, token, noRef)})))
	notes := env.take()
	require.Len(t, notes, 3)
	require.Contains(t, notes[0].text, "`=:ETH.ETH:0xregistered` (memoless: 12345)")
	require.Equal(t, 1, env.reqs("thorchain/memo/ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48/777"))
	require.Equal(t, 1, env.reqs("thorchain/memo/ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48/1"))
	require.Contains(t, notes[2].text, "*Memo*: ``")
}

////////////////////////////////////////////////////////////////////////////////////////
// Large Streaming Swaps
////////////////////////////////////////////////////////////////////////////////////////

func actCovSwapEvent(id, count, quantity, coin, memo string) map[string]string {
	return map[string]string{
		"type":                    types.SwapEventType,
		"id":                      id,
		"chain":                   "BTC",
		"from":                    "bc1qswapper",
		"coin":                    coin,
		"memo":                    memo,
		"streaming_swap_count":    count,
		"streaming_swap_quantity": quantity,
		"pool_slip":               "0",
		"swap_slip":               "0",
	}
}

func TestActCovLargeStreamingSwaps(t *testing.T) {
	env := actCovSetup(t)

	env.set("thorchain/tx/SWAP1", `{"observed_tx":{"tx":{"coins":[{"asset":"BTC.BTC","amount":"300000000"}],"gas":[]}}}`)
	env.set("thorchain/tx/LOAN1", `{"observed_tx":{"tx":{"coins":[{"asset":"BTC.BTC","amount":"300000000"}],"gas":[]}}}`)
	env.set("thorchain/tx/SMALL1", `{"observed_tx":{"tx":{"coins":[{"asset":"BTC.BTC","amount":"1000000"}],"gas":[]}}}`)

	block := &thorscan.BlockResponse{BlockResponse: actCovBlockResp(77)}
	block.EndBlockEvents = actCovEvents(
		map[string]string{"type": "other"},
		actCovSwapEvent("X2", "2", "10", "100000000 BTC.BTC", "=:ETH.ETH:0xabc"), // not first
		actCovSwapEvent("X3", "1", "1", "100000000 BTC.BTC", "=:ETH.ETH:0xabc"),  // single
		actCovSwapEvent("X4", "1", "2", "1000 BTC.BTC", "=:ETH.ETH:0xabc"),       // small approx
		actCovSwapEvent("SWAP1", "1", "10", "30000000 BTC.BTC", "=:ETH.ETH:0xabc:0/5/0"),
	)
	block.FinalizeBlockEvents = actCovEvents(
		actCovSwapEvent("LOAN1", "1", "10", "30000000 BTC.BTC", "noop"),
		actCovSwapEvent("SMALL1", "1", "100", "1000000 BTC.BTC", "=:ETH.ETH:0xabc:0/bad/0"), // precise value too small
	)

	LargeStreamingSwaps(block)
	notes := env.take()
	require.Len(t, notes, 2)
	require.Contains(t, notes[0].text, "Streaming Swap")
	require.Contains(t, notes[0].text, ":moneybag::moneybag::moneybag:")
	require.Contains(t, notes[0].text, "*Interval*: 5 blocks")
	require.Contains(t, notes[0].text, "*Expected Swap Time*: 5m")
	require.Contains(t, notes[0].text, "Clout (0xabc)")
	require.Contains(t, notes[0].text, "*Quantity*: 10 swaps")
	require.NotContains(t, notes[1].text, "Interval")
	require.Contains(t, notes[1].text, "`noop`")

	// seen swaps are not fetched or alerted again
	LargeStreamingSwaps(block)
	require.Empty(t, env.take())
	require.Equal(t, 1, env.reqs("thorchain/tx/SWAP1"))
}

////////////////////////////////////////////////////////////////////////////////////////
// Scheduled Outbounds
////////////////////////////////////////////////////////////////////////////////////////

func actCovOutEvent(inHash, memo, asset, amount, vault string) map[string]string {
	return map[string]string{
		"type":             types.ScheduledOutboundEventType,
		"in_hash":          inHash,
		"memo":             memo,
		"coin_asset":       asset,
		"coin_amount":      amount,
		"to_address":       "0xrecipient",
		"vault_pub_key":    vault,
		"gas_rate":         "10",
		"max_gas_amount_0": "1000",
	}
}

func actCovOutBlock(height int64, events ...map[string]string) *thorscan.BlockResponse {
	block := &thorscan.BlockResponse{BlockResponse: actCovBlockResp(height)}
	block.EndBlockEvents = events
	return block
}

func TestActCovScheduledOutboundSwapWithDelta(t *testing.T) {
	env := actCovSetup(t)

	// inbound 3 BTC ($300k) -> outbound 400 ETH ($400k): +33% delta is Danger
	env.set("thorchain/tx/status/IN1", `{
		"tx":{"memo":"=:ETH.ETH:0xrecipient","from_address":"bc1qsender","coins":[{"asset":"BTC.BTC","amount":"300000000"}],"gas":[]},
		"stages":{"inbound_observed":{"completed":true},
			"swap_status":{"pending":false,"streaming":{"interval":2,"quantity":5,"count":5}},
			"outbound_delay":{"remaining_delay_seconds":120,"completed":false},
			"outbound_signed":{"blocks_since_scheduled":10,"completed":false}}
	}`)

	block := actCovOutBlock(500,
		map[string]string{"type": "other"},
		actCovOutEvent("IN1", "OUT:IN1", "ETH.ETH", "40000000000", "pubkeyAAAA"),
	)
	ScheduledOutbounds(block)
	notes := env.take()
	require.Len(t, notes, 2)
	require.Equal(t, "activity", notes[0].hook)
	require.Equal(t, "security", notes[1].hook)
	text := notes[0].text
	require.Contains(t, text, "Scheduled Outbound")
	require.Contains(t, text, "*Inbound Amount*: 3.000000 BTC.BTC ($300,000.00)")
	require.Contains(t, text, ":small_red_triangle: $100,000.00 (33.33%)")
	require.Contains(t, text, ":rotating_light:")
	require.Contains(t, text, "*Stream Duration*: 1m")
	require.Contains(t, text, "Stream Price Shift (BTC.BTC)")
	require.Contains(t, text, "*Expected Delay*: 2m")
	require.Contains(t, text, "Clout (bc1qsender)")
	require.Contains(t, text, "Track")

	// the same outbound rescheduled to a new vault alerts as rescheduled; the
	// outbound alert itself is suppressed because its value is below the security
	// threshold (delta-driven Danger is only evaluated after the reschedule check)
	ScheduledOutbounds(actCovOutBlock(510, actCovOutEvent("IN1", "OUT:IN1", "ETH.ETH", "40000000000", "pubkeyBBBB")))
	notes = env.take()
	require.Len(t, notes, 1)
	require.Contains(t, notes[0].text, "Rescheduled Outbound")
	require.Contains(t, notes[0].text, ":rotating_light: `AAAA` -> `BBBB` :rotating_light:")
	require.Contains(t, notes[0].text, "*Inbound Memo*: `=:ETH.ETH:0xrecipient`")
	require.Contains(t, notes[0].text, "*Gas Rate*: 10 -> 10")
}

func TestActCovScheduledOutboundMultiRefund(t *testing.T) {
	env := actCovSetup(t)

	env.set("thorchain/tx/status/IN2", `{
		"tx":{"memo":"=:ETH.ETH:0xrecipient","coins":[{"asset":"ETH.ETH","amount":"30000000000"}],"gas":[]},
		"stages":{"inbound_observed":{"completed":true}}
	}`)

	// one outbound from block events, one refund from a tx event (partial fill)
	block := actCovOutBlock(600, actCovOutEvent("IN2", "OUT:IN2", "ETH.ETH", "20000000000", "pubkeyAAAA"))
	block.Txs = []thorscan.BlockTx{
		actCovBlockTx(0, nil, actCovOutEvent("IN2", "IGNORED", "ETH.ETH", "1", "pubkeyAAAA")), // nil tx skipped
		actCovBlockTx(0, actCovTx{}, map[string]string{"type": "other"},
			actCovOutEvent("IN2", "REFUND:IN2", "ETH.ETH", "10000000000", "pubkeyAAAA")),
	}
	ScheduledOutbounds(block)
	notes := env.take()
	require.Len(t, notes, 1)
	text := notes[0].text
	require.Contains(t, text, "Scheduled Outbounds (2) _(partial fill)_")
	require.Contains(t, text, "*Outbound 1 Amount*: 200.000000 ETH.ETH")
	require.Contains(t, text, "*Outbound 2 Memo*: `REFUND:IN2`")
	require.Contains(t, text, "*Delta*: $0.00 (0.00%)")
	require.Contains(t, text, "*Expected Delay*: 0s")
}

func TestActCovScheduledOutboundSkips(t *testing.T) {
	env := actCovSetup(t)

	env.set("thorchain/tx/status/INW", `{"tx":{"memo":"WITHDRAW:BTC.BTC","coins":[],"gas":[]},"stages":{"inbound_observed":{"completed":true}}}`)
	env.set("thorchain/tx/status/INS", `{"tx":{"memo":"=:BTC.BTC:bc1q","coins":[{"asset":"BTC.BTC","amount":"100000000"}],"gas":[]},"stages":{"inbound_observed":{"completed":true}}}`)
	env.set("thorchain/tx/status/INT", `{"tx":{"memo":"TEST-INACTIVE-VAULT-REFUND","coins":[],"gas":[]},"stages":{"inbound_observed":{"completed":true}}}`)
	env.set("thorchain/tx/status/INX", `{"tx":{"memo":"BOGUS:memo","coins":[],"gas":[]},"stages":{"inbound_observed":{"completed":true}}}`)
	env.set("thorchain/tx/status/INC", `{"tx":{"memo":"WITHDRAW:BTC.BTC","coins":[],"gas":[]},"stages":{"inbound_observed":{"completed":true}}}`)
	env.set("thorchain/clout/swap/0xclouty", `{"score":"100000000000000"}`)

	cloutEvent := actCovOutEvent("INC", "OUT:INC", "BTC.BTC", "300000000", "pubkeyAAAA")
	cloutEvent["to_address"] = "0xclouty"

	ScheduledOutbounds(actCovOutBlock(700,
		actCovOutEvent("INM", "MIGRATE:5", "BTC.BTC", "300000000", "pubkeyAAAA"),  // migrate
		actCovOutEvent("INR", "RAGNAROK:5", "BTC.BTC", "300000000", "pubkeyAAAA"), // ragnarok
		actCovOutEvent("INL", "OUT:INL", "BTC.BTC", "1000", "pubkeyAAAA"),         // tiny
		cloutEvent, // clout covers it
		actCovOutEvent("INW", "OUT:INW", "BTC.BTC", "100000000", "pubkeyAAAA"), // $100k non-swap, no delta
		actCovOutEvent("INS", "OUT:INS", "BTC.BTC", "100000000", "pubkeyAAAA"), // $100k swap, no delta
		actCovOutEvent(common.BlankTxID.String(), "OUT:", "BTC.BTC", "1000", "pubkeyAAAA"),
		actCovOutEvent("INX", "OUT:INX", "BTC.BTC", "300000000", "pubkeyAAAA"), // bad memo -> unknown, alerts
	))
	notes := env.take()
	require.Len(t, notes, 1)
	require.Contains(t, notes[0].text, "`BOGUS:memo`")
	require.NotContains(t, notes[0].text, "Delta")

	// test memo is ignored on mocknet
	cfg := config.Get()
	cfg.Network = "mocknet"
	config.Set(cfg)
	ScheduledOutbounds(actCovOutBlock(710, actCovOutEvent("INT", "OUT:INT", "BTC.BTC", "300000000", "pubkeyAAAA")))
	require.Empty(t, env.take())
}

func TestActCovRescheduledOutbounds(t *testing.T) {
	env := actCovSetup(t)

	// ragnarok outbound: no status lookup, still alerts on reschedule
	rag := actCovOutEvent("INR", "RAGNAROK:5", "BTC.BTC", "300000000", "pubkeyAAAA")
	require.False(t, rescheduledOutbounds(10, rag), "first sighting is not a reschedule")
	require.True(t, rescheduledOutbounds(11, rag))
	notes := env.take()
	require.Len(t, notes, 1)
	require.Contains(t, notes[0].text, "Rescheduled Outbound")
	require.Contains(t, notes[0].text, ":moneybag::moneybag::moneybag:")
	require.Contains(t, notes[0].text, "`AAAA` -> `AAAA`")
	require.NotContains(t, notes[0].text, "Inbound Memo")
	require.Zero(t, env.reqs("thorchain/tx/status/INR"))

	// nil outbound signed stage
	env.set("thorchain/tx/status/INN", `{"stages":{"inbound_observed":{"completed":true}}}`)
	ev := actCovOutEvent("INN", "OUT:INN", "BTC.BTC", "100", "pubkeyAAAA")
	require.False(t, rescheduledOutbounds(20, ev))
	require.True(t, rescheduledOutbounds(21, ev))
	require.Empty(t, env.take())

	// too old
	env.set("thorchain/tx/status/INO", `{"stages":{"inbound_observed":{"completed":true},"outbound_signed":{"blocks_since_scheduled":100000,"completed":false}}}`)
	ev = actCovOutEvent("INO", "OUT:INO", "BTC.BTC", "100", "pubkeyAAAA")
	require.False(t, rescheduledOutbounds(30, ev))
	require.True(t, rescheduledOutbounds(31, ev))
	require.Empty(t, env.take())

	// nil inbound memo and an unparsable memo
	env.set("thorchain/tx/status/INU", `{"stages":{"inbound_observed":{"completed":true},"outbound_signed":{"blocks_since_scheduled":1,"completed":false}}}`)
	ev = actCovOutEvent("INU", "OUT:INU", "BTC.BTC", "100", "pubkeyAAAA")
	require.False(t, rescheduledOutbounds(40, ev))
	require.True(t, rescheduledOutbounds(41, ev))
	env.set("thorchain/tx/status/INB", `{"tx":{"memo":"BOGUS","coins":[],"gas":[]},"stages":{"inbound_observed":{"completed":true},"outbound_signed":{"blocks_since_scheduled":1,"completed":false}}}`)
	ev = actCovOutEvent("INB", "OUT:INB", "BTC.BTC", "100", "pubkeyAAAA")
	require.False(t, rescheduledOutbounds(50, ev))
	require.True(t, rescheduledOutbounds(51, ev))
	notes = env.take()
	require.Len(t, notes, 2)
	require.Contains(t, notes[0].text, "*Inbound Memo*: `<no memo>`")
	require.Contains(t, notes[0].text, "*Age*: 6s (1 blocks)")
	require.Contains(t, notes[1].text, "*Inbound Memo*: `BOGUS`")
	require.NotContains(t, notes[1].text, "[Track]")

	// blank in hash is never tracked
	require.False(t, rescheduledOutbounds(60, actCovOutEvent(common.BlankTxID.String(), "OUT:", "BTC.BTC", "1", "pubkeyAAAA")))
}

////////////////////////////////////////////////////////////////////////////////////////
// Large Transfers
////////////////////////////////////////////////////////////////////////////////////////

func TestActCovLargeTransfers(t *testing.T) {
	env := actCovSetup(t)

	bigAmt := sdk.NewCoins(sdk.NewCoin("rune", cosmos.NewInt(2_000_000*common.One)))
	smallAmt := sdk.NewCoins(sdk.NewCoin("rune", cosmos.NewInt(10*common.One)))
	from := types.GetRandomBech32Addr()
	to := types.GetRandomBech32Addr()
	reserve := "thor1dheycdevq39qlkxs2a6wuuzyn4aqxhve4qxtxt"
	pool := "thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0"

	block := actCovTxBlock(900,
		actCovBlockTx(1, actCovTx{msgs: actCovMsgs(&thorchain.MsgSend{FromAddress: from, ToAddress: to, Amount: bigAmt})}),
		actCovBlockTx(0, nil),
		actCovBlockTx(0, actCovTx{msgs: actCovMsgs(
			&thorchain.MsgDeposit{},
			&thorchain.MsgSend{FromAddress: from, ToAddress: to, Amount: smallAmt},
			&thorchain.MsgSend{FromAddress: from, ToAddress: to, Amount: bigAmt},
			&bank.MsgSend{FromAddress: reserve, ToAddress: pool, Amount: bigAmt},
		)}),
		actCovBlockTx(0, actCovTx{memo: "MIGRATE:42", msgs: actCovMsgs(
			&bank.MsgSend{FromAddress: reserve, ToAddress: pool, Amount: bigAmt},
		)}),
	)
	LargeTransfers(block)
	notes := env.take()
	require.Len(t, notes, 3)
	require.Contains(t, notes[0].text, "Large Transfer >> 2,000,000 RUNE ($4,000,000.00)")
	require.Contains(t, notes[0].text, from.String())
	require.Contains(t, notes[1].text, "|Reserve Module>")
	require.Contains(t, notes[1].text, "|Pool Module>")
	require.Contains(t, notes[2].text, "External Migration `MIGRATE:42` (2,000,000 RUNE)")
}

////////////////////////////////////////////////////////////////////////////////////////
// Inactive Vault Inbounds
////////////////////////////////////////////////////////////////////////////////////////

func TestActCovInactiveVaultInbounds(t *testing.T) {
	env := actCovSetup(t)
	vaults = nil

	env.set("thorchain/vaults/asgard", `[
		{"status":"ActiveVault","pub_key":"active-ecdsa","pub_key_eddsa":"active-eddsa","coins":[]},
		{"status":"RetiringVault","pub_key":"retiring-ecdsa","pub_key_eddsa":"retiring-eddsa","coins":[]},
		{"status":"InactiveVault","coins":[]}
	]`)

	mk := func(pk string) *types.MsgObservedTxQuorum {
		m := actCovInbound("=:ETH.ETH:0xabc", actCovBTC(1), 1, 1)
		m.QuoTx.ObsTx.ObservedPubKey = common.PubKey(pk)
		return m
	}
	inactive := mk("inactive-pubkey")
	consensus := mk("inactive-pubkey")
	env.set("thorchain/tx/"+consensus.QuoTx.ObsTx.Tx.ID.String(), `{"consensus_height":5}`)
	outbound := mk("inactive-pubkey")
	outbound.QuoTx.Inbound = false

	block := actCovTxBlock(1000,
		actCovBlockTx(0, nil),
		actCovBlockTx(0, actCovTx{msgs: actCovMsgs(
			&thorchain.MsgSend{},
			outbound,
			mk("active-ecdsa"),
			mk("active-eddsa"),
			mk("retiring-eddsa"),
			consensus,
			inactive,
		)}),
	)
	InactiveVaultInbounds(block)
	notes := env.take()
	require.Len(t, notes, 1)
	require.Contains(t, notes[0].text, "Inbound to Non-Active Vault")
	require.Contains(t, notes[0].text, inactive.QuoTx.ObsTx.Tx.ID.String())
	require.Contains(t, notes[0].text, "*Vault*: inactive-pubkey")
	require.True(t, vaults.Active["active-eddsa"])
	require.True(t, vaults.Retiring["retiring-ecdsa"])
	require.Equal(t, int64(1000), vaults.Height)

	stored := Vaults{}
	require.NoError(t, util.Load("vaults", &stored))
	require.True(t, stored.Active["active-ecdsa"])

	// seen inbounds do not repeat; vaults are not refetched without an update
	InactiveVaultInbounds(actCovTxBlock(1001, actCovBlockTx(0, actCovTx{msgs: actCovMsgs(inactive)})))
	require.Empty(t, env.take())
	require.Equal(t, 1, env.reqs("thorchain/vaults/asgard"))

	// an active vault event refreshes the vault set; retiring vaults alert after 12h
	refresh := actCovTxBlock(1000+7200, actCovBlockTx(0, actCovTx{msgs: actCovMsgs(mk("retiring-ecdsa"))},
		map[string]string{"type": "other"},
		map[string]string{"type": types.VaultStatus_ActiveVault.String()}))
	InactiveVaultInbounds(refresh)
	require.Equal(t, 2, env.reqs("thorchain/vaults/asgard"))
	require.Equal(t, int64(8200), vaults.Height)
	require.Empty(t, env.take(), "retiring vault refreshed at the same height is still within 12h")

	vaults.Height = 0
	InactiveVaultInbounds(actCovTxBlock(9000, actCovBlockTx(0, actCovTx{msgs: actCovMsgs(mk("retiring-ecdsa"))})))
	require.Len(t, env.take(), 1)
}

////////////////////////////////////////////////////////////////////////////////////////
// New Node / Bond
////////////////////////////////////////////////////////////////////////////////////////

const actCovModule = "thor1v8ppstuf6e3x0r4glqc68d5jqcs2tf38cg2q6y"

func TestActCovNewNode(t *testing.T) {
	env := actCovSetup(t)

	signer := types.GetRandomBech32Addr()
	node := types.GetRandomBech32Addr()
	runeAmt := sdk.NewCoins(sdk.NewCoin("rune", cosmos.NewInt(300_000*common.One)))
	newNode := map[string]string{"type": "new_node", "address": node.String()}

	block := actCovTxBlock(42,
		actCovBlockTx(0, actCovTx{}, map[string]string{"type": "other"}),
		actCovBlockTx(0, actCovTx{msgs: actCovMsgs(
			&thorchain.MsgDeposit{
				Signer: signer,
				Coins:  common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(500_000*common.One)), actCovBTC(1)},
			},
			&thorchain.MsgSend{FromAddress: signer, ToAddress: node, Amount: runeAmt}, // not module
			&bank.MsgSend{FromAddress: "thor1someoperatorWXYZ", ToAddress: actCovModule, Amount: runeAmt},
			&bank.MsgSend{FromAddress: "thor1someoperatorWXYZ", ToAddress: "thor1other", Amount: runeAmt},
			&types.MsgObservedTxQuorum{},
		)}, newNode),
	)
	NewNode(block)
	notes := env.take()
	require.Len(t, notes, 2)
	s := signer.String()
	n := node.String()
	require.Contains(t, notes[0].text, "New Node")
	require.Contains(t, notes[0].text, fmt.Sprintf("*Operator*: `%s`", s[len(s)-4:]))
	require.Contains(t, notes[0].text, fmt.Sprintf("*Node*: `%s`", n[len(n)-4:]))
	require.Contains(t, notes[0].text, "*Amount*: 500,000.00000000 RUNE")
	require.Contains(t, notes[1].text, "*Operator*: `WXYZ`")
	require.Contains(t, notes[1].text, "*Amount*: 300,000.00000000 RUNE")
}

func TestActCovBond(t *testing.T) {
	env := actCovSetup(t)

	signer := types.GetRandomBech32Addr()
	node := types.GetRandomBech32Addr()
	env.set("thorchain/nodes", actCovJSON(t, []map[string]string{
		{"node_address": "tthor1unrelated", "node_operator_address": "tthor1unrelatedop"},
		{"node_address": node.String(), "node_operator_address": "tthor1operatorOPER"},
	}))
	bondEvent := map[string]string{"type": types.BondEventType, "amount": "250000000000"}
	runeAmt := sdk.NewCoins(sdk.NewCoin("rune", cosmos.NewInt(100*common.One)))

	block := actCovTxBlock(43,
		// new node bonds are reported by NewNode instead
		actCovBlockTx(0, actCovTx{msgs: actCovMsgs(&thorchain.MsgDeposit{Signer: signer, Memo: "BOND:" + node.String()})},
			map[string]string{"type": "new_node"}, bondEvent),
		actCovBlockTx(0, actCovTx{}, map[string]string{"type": "other"}),
		actCovBlockTx(0, actCovTx{msgs: actCovMsgs(
			&thorchain.MsgDeposit{
				Signer: signer,
				Memo:   "BOND:" + node.String(),
				Coins:  common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(1000*common.One))},
			},
			&thorchain.MsgDeposit{Signer: signer, Memo: "UNBOND:" + node.String() + ":2500"},
			&thorchain.MsgSend{FromAddress: signer, ToAddress: node, Amount: runeAmt},
			&bank.MsgSend{FromAddress: "thor1other", ToAddress: "thor1nope", Amount: runeAmt},
			&types.MsgObservedTxQuorum{},
		)}, bondEvent),
		actCovBlockTx(0, actCovTx{memo: "BOND:" + node.String(), msgs: actCovMsgs(
			&bank.MsgSend{FromAddress: "thor1providerPROV", ToAddress: actCovModule, Amount: runeAmt},
		)}, bondEvent),
	)
	Bond(block)
	notes := env.take()
	require.Len(t, notes, 3)
	s := signer.String()
	n := node.String()
	require.Contains(t, notes[0].text, fmt.Sprintf("*Provider*: `%s`", s[len(s)-4:]))
	require.Contains(t, notes[0].text, "*Amount*: 1,000.00000000")
	require.Contains(t, notes[0].text, fmt.Sprintf("*Node*: `%s`", n[len(n)-4:]))
	require.Contains(t, notes[0].text, "*Operator*: `OPER`")
	require.Contains(t, notes[1].text, "*Unbond Amount*: 2,500.00000000")
	require.Contains(t, notes[2].text, "*Provider*: `PROV`")
	require.Contains(t, notes[2].text, "*Amount*: 100.00000000")
}

////////////////////////////////////////////////////////////////////////////////////////
// Failed Transactions / Refunds
////////////////////////////////////////////////////////////////////////////////////////

func TestActCovFailedTransactions(t *testing.T) {
	env := actCovSetup(t)
	failedTxLimiter = rate.NewLimiter(0, 1) // exactly one alert allowed

	failed := actCovBlockTx(7, nil)
	failed.Result.Codespace = actCovPtr("thorchain")
	failed.Result.Log = actCovPtr("boom")
	block := actCovTxBlock(44,
		actCovBlockTx(0, actCovTx{}),
		actCovBlockTx(5, actCovTx{}),
		actCovBlockTx(32, actCovTx{}),
		actCovBlockTx(99, actCovTx{}),
		failed,
		actCovBlockTx(8, actCovTx{}), // rate limited
	)
	FailedTransactions(block)
	notes := env.take()
	require.Len(t, notes, 1)
	require.Contains(t, notes[0].text, "Failed Transaction")
	require.Contains(t, notes[0].text, "*Code*: 7")
	require.Contains(t, notes[0].text, "*Failed Decode*: true")
	require.Contains(t, notes[0].text, "*Codespace*: `thorchain`")
	require.Contains(t, notes[0].text, "*Log*: `boom`")
}

func TestActCovFailedRefunds(t *testing.T) {
	env := actCovSetup(t)

	block := &thorscan.BlockResponse{BlockResponse: actCovBlockResp(45)}
	block.EndBlockEvents = actCovEvents(
		map[string]string{"type": "swap"},
		map[string]string{"type": types.RefundEventType, "reason": "some other reason", "coin": "1 BTC.BTC"},
		map[string]string{"type": types.RefundEventType, "reason": "fail to refund", "coin": "1 BTC.BTC"}, // dust
		map[string]string{
			"type": types.RefundEventType, "reason": "fail to refund:\n   insufficient   funds", "coin": "100000000 BTC.BTC",
			"chain": "BTC", "id": "REFUNDID", "from": "bc1qfrom", "memo": "=:ETH.ETH:0xabc",
		},
	)
	FailedRefunds(block)
	notes := env.take()
	require.Len(t, notes, 1)
	require.Equal(t, "refunds", notes[0].hook)
	require.Contains(t, notes[0].text, "Failed Refund")
	require.Contains(t, notes[0].text, "*Reason*: `fail to refund: insufficient funds`")
	require.Contains(t, notes[0].text, "1.000000 BTC.BTC ($100,000.00)")
}

////////////////////////////////////////////////////////////////////////////////////////
// THORName / High Slip / ScanActivity
////////////////////////////////////////////////////////////////////////////////////////

func TestActCovTHORNameRegistrations(t *testing.T) {
	env := actCovSetup(t)

	event := map[string]string{
		"type": types.THORNameEventType, "name": "alice", "registration_fee": "1000000000",
		"fund_amount": "200000000", "expire": "14500",
	}
	block := actCovTxBlock(100,
		actCovBlockTx(0, actCovTx{}, map[string]string{"type": "other"}),
		actCovBlockTx(0, actCovTx{msgs: actCovMsgs(&thorchain.MsgDeposit{Memo: "~:alice:THOR:tthor1x"})}, event),
		actCovBlockTx(0, actCovTx{memo: "~:alice:BTC:bc1q", msgs: actCovMsgs(
			&bank.MsgSend{ToAddress: "thor1notmodule"},
			&bank.MsgSend{ToAddress: actCovModule},
			&thorchain.MsgSend{ToAddress: types.GetRandomBech32Addr()},
		)}, event),
	)
	THORNameRegistrations(block)
	notes := env.take()
	require.Len(t, notes, 2)
	require.Contains(t, notes[0].text, "THORName Registration")
	require.Contains(t, notes[0].text, "*THORName*: `alice`")
	expire := util.FormatDuration(time.Duration(14400) * constants.ThorchainBlockTime)
	require.Contains(t, notes[0].text, fmt.Sprintf("*Expiration*: `14500` (%s)", expire))
	require.Contains(t, notes[0].text, "*Registration Fee*: 10.00000000")
	require.Contains(t, notes[0].text, "*Memo*: `~:alice:THOR:tthor1x`")
	require.Contains(t, notes[1].text, "*Memo*: `~:alice:BTC:bc1q`")
}

func TestActCovLargeHighSlipSwaps(t *testing.T) {
	env := actCovSetup(t)

	low := actCovSwapEvent("LOW", "1", "1", "300000000 BTC.BTC", "=:ETH.ETH")
	small := actCovSwapEvent("SMALL", "1", "1", "1000 BTC.BTC", "=:ETH.ETH")
	small["swap_slip"] = "500"
	high := actCovSwapEvent("HIGH", "1", "1", "300000000 BTC.BTC", "=:ETH.ETH")
	high["pool_slip"] = "250"
	high["swap_slip"] = "50"

	block := &thorscan.BlockResponse{BlockResponse: actCovBlockResp(46)}
	block.EndBlockEvents = actCovEvents(map[string]string{"type": "other"}, low, small)
	block.FinalizeBlockEvents = actCovEvents(high)
	LargeHighSlipSwaps(block)
	notes := env.take()
	require.Len(t, notes, 1)
	require.Contains(t, notes[0].text, "High Slip Swap")
	require.Contains(t, notes[0].text, "*Swap Slip*: 0.50%")
	require.Contains(t, notes[0].text, "*Pool Slip*: 2.50%")
	require.Contains(t, notes[0].text, ":moneybag::moneybag::moneybag:")
}

func TestActCovScanActivityEmptyBlock(t *testing.T) {
	env := actCovSetup(t)
	ScanActivity(actCovTxBlock(47))
	require.Empty(t, env.take())
	require.Zero(t, env.reqs("thorchain/vaults/asgard"), "vaults already loaded and no update event")
}
