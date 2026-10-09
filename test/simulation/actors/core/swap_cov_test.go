package core

import (
	"encoding/json"
	"errors"
	"fmt"
	rand "math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"testing"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const covBQuotePath = "/thorchain/quote/swap"

const covBInbound = `[
	{"chain":"BTC","address":"bc1qvault"},
	{"chain":"ETH","address":"0x00000000000000000000000000000000000000aa","router":"0x00000000000000000000000000000000000000bb"}
]`

// covBQuoteFn serves quotes computed from the requested amount. A non-empty
// error string is returned as a 400 response body.
func covBQuoteFn(t *testing.T, fn func(amount uint64) (expectedOut, minIn uint64, errMsg string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		amount, err := strconv.ParseUint(r.URL.Query().Get("amount"), 10, 64)
		if err != nil {
			t.Errorf("bad quote amount: %v", err)
		}
		out, minIn, errMsg := fn(amount)
		if errMsg != "" {
			http.Error(w, errMsg, http.StatusBadRequest)
			return
		}
		quote := map[string]interface{}{
			"expected_amount_out": fmt.Sprint(out),
			"fees":                map[string]interface{}{"asset": "ETH.ETH", "outbound": "10", "liquidity": "0", "total": "0"},
		}
		if minIn > 0 {
			quote["recommended_min_amount_in"] = fmt.Sprint(minIn)
		}
		_ = json.NewEncoder(w).Encode(quote)
	}
}

func covBFirstToken(t *testing.T) common.Asset {
	t.Helper()
	for asset := range evm.Tokens(common.ETHChain) {
		return asset
	}
	t.Fatal("no ETH tokens")
	return common.EmptyAsset
}

func newSwapActorCovB(from, to common.Asset) *SwapActor {
	return &SwapActor{
		Actor:                 *simtypes.NewActor("swap", rand.New(rand.NewPCG(1, 2))),
		from:                  from,
		to:                    to,
		swapAmount:            sdkmath.ZeroUint(),
		fromBalance:           sdkmath.ZeroUint(),
		toBalance:             sdkmath.ZeroUint(),
		requiredOutboundSpend: sdkmath.ZeroUint(),
		minExpected:           sdkmath.ZeroUint(),
		maxExpected:           sdkmath.ZeroUint(),
	}
}

// newSwapUserCovB returns a user holding btc (+dust reserve) and eth balances.
func newSwapUserCovB(t *testing.T, idx int, btc uint64) (*simtypes.User, *covChainClient, *covChainClient) {
	t.Helper()
	btcClient := newCovChainClient(common.NewCoin(common.BTCAsset, cosmos.NewUint(btc+common.BTCChain.DustThreshold().Uint64())))
	ethClient := newCovChainClient(common.NewCoin(common.ETHAsset, cosmos.NewUint(500)))
	user, _ := newCovUser(t, idx, map[common.Chain]simtypes.LiteChainClient{common.BTCChain: btcClient, common.ETHChain: ethClient})
	return user, btcClient, ethClient
}

func TestNewSwapActorTokenPathCovB(t *testing.T) {
	token := covBFirstToken(t)
	actor := NewSwapActor(token, common.BTCAsset, rand.New(rand.NewPCG(1, 2)))
	if len(actor.Ops) != 4 || !strings.Contains(actor.Ops[2].String(), "sendTokenSwap") {
		t.Fatalf("token swaps must deposit through the router, got %v", actor.Ops[2])
	}
	if swapQuoteRecommendedMinAttempts(common.BTCAsset, common.XMRAsset) != 64 || swapQuoteRecommendedMinAttempts(common.BTCAsset, common.ETHAsset) != 4 {
		t.Fatal("unexpected quote attempt budget")
	}
	if !accountAssetAmount(nil, common.BTCAsset).IsZero() {
		t.Fatal("nil account must have zero balance")
	}
	if isPostSelectionFundingError(nil) || isPostSelectionFundingError(simtypes.ErrSimTxUncertain) || !isPostSelectionFundingError(errors.New("Insufficient Funds")) {
		t.Fatal("unexpected funding error classification")
	}
}

func TestGetSwapQuoteWithRecommendedMinCovB(t *testing.T) {
	api := newCovThornode(t)
	call := func(amount, maxAmount uint64) (openapi.QuoteSwapResponse, sdkmath.Uint, error) {
		return getSwapQuoteWithRecommendedMin(common.BTCAsset, common.ETHAsset, sdkmath.NewUint(amount), sdkmath.NewUint(maxAmount), "0xdest", "bc1qrefund", true)
	}

	// minimum reported in the error body is retried with a 1% buffer
	api.Handle(covBQuotePath, covBQuoteFn(t, func(amount uint64) (uint64, uint64, string) {
		if amount < 1000 {
			return 0, 0, "amount too small: recommended_min_amount_in: 1000"
		}
		return amount * 2, 0, ""
	}))
	quote, amount, err := call(100, 0)
	if err != nil || amount.Uint64() != 1010 || quote.ExpectedAmountOut != "2020" {
		t.Fatalf("expected retry at 1010, got %s %v %+v", amount, err, quote)
	}
	if _, amount, err = call(100, 1005); err == nil || amount.Uint64() != 1010 {
		t.Fatalf("buffered minimum above balance must fail, got %s %v", amount, err)
	}

	// non-minimum errors are returned unchanged
	api.Handle(covBQuotePath, covBQuoteFn(t, func(uint64) (uint64, uint64, string) { return 0, 0, "trading halted" }))
	if _, amount, err = call(100, 0); err == nil || !strings.Contains(err.Error(), "trading halted") || amount.Uint64() != 100 {
		t.Fatalf("expected plain error, got %s %v", amount, err)
	}

	// minimum in a successful quote
	api.Handle(covBQuotePath, covBQuoteFn(t, func(amount uint64) (uint64, uint64, string) { return amount, 700, "" }))
	if _, amount, err = call(100, 0); err != nil || amount.Uint64() != 700 {
		t.Fatalf("expected amount raised to 700, got %s %v", amount, err)
	}
	if _, amount, err = call(100, 600); err == nil || amount.Uint64() != 700 {
		t.Fatalf("quoted minimum above balance must fail, got %s %v", amount, err)
	}

	// a minimum that keeps moving never settles
	api.Handle(covBQuotePath, covBQuoteFn(t, func(amount uint64) (uint64, uint64, string) { return amount, amount + 1, "" }))
	if _, _, err = call(100, 0); err == nil || !strings.Contains(err.Error(), "failed to settle") {
		t.Fatalf("expected settle failure, got %v", err)
	}
}

func TestSwapAcquireUserCovB(t *testing.T) {
	api := newCovThornode(t)
	user, _, _ := newSwapUserCovB(t, 0, 10*common.One)
	cfg := &simtypes.OpConfig{Users: []*simtypes.User{user}}

	a := newSwapActorCovB(common.BTCAsset, common.ETHAsset)
	api.Status("/thorchain/pool/BTC.BTC", 400, "bad")
	if res := a.acquireUser(cfg); res.Continue || res.Finish {
		t.Fatalf("pool failure must retry: %+v", res)
	}
	api.JSON("/thorchain/pool/BTC.BTC", `{"asset":"BTC.BTC","balance_asset":"400000000","balance_rune":"1"}`)
	if res := a.acquireUser(cfg); !res.Continue || a.user != user || a.swapAmount.Uint64() != 1_000_000 {
		t.Fatalf("expected 0.25%% of pool depth, got %s (%+v)", a.swapAmount, res)
	}
	if !a.toBalance.Equal(sdkmath.NewUint(500)) || !a.fromAddress.Equals(user.Address(common.BTCChain)) {
		t.Fatal("balances/addresses not captured")
	}
	user.Release()

	// XMR paths wait for the XMR network fee first
	node, bridge := newCovUser(t, 12, nil)
	bridge.NetworkFeeFn = func(common.Chain) (uint64, uint64, error) { return 0, 0, nil }
	x := newSwapActorCovB(common.BTCAsset, common.XMRAsset)
	if res := x.acquireUser(&simtypes.OpConfig{NodeUsers: []*simtypes.User{node}, Users: cfg.Users}); res.Continue || x.user != nil {
		t.Fatalf("XMR swap must wait for network fee: %+v", res)
	}
}

func TestSwapGetQuoteCovB(t *testing.T) {
	api := newCovThornode(t)

	t.Run("success sets expectations", func(t *testing.T) {
		user, _, _ := newSwapUserCovB(t, 0, 10*common.One)
		a := newSwapActorCovB(common.BTCAsset, common.ETHAsset)
		a.swapAmount = sdkmath.NewUint(common.One)
		api.Handle(covBQuotePath, covBQuoteFn(t, func(uint64) (uint64, uint64, string) { return 1000, 0, "" }))
		if res := a.getQuote(&simtypes.OpConfig{Users: []*simtypes.User{user}}); !res.Continue {
			t.Fatalf("quote failed: %+v", res)
		}
		if !a.requiredOutboundSpend.Equal(sdkmath.NewUint(1010)) || !a.minExpected.Equal(sdkmath.NewUint(890)) || !a.maxExpected.Equal(sdkmath.NewUint(1130)) {
			t.Fatalf("unexpected range %s %s %s", a.requiredOutboundSpend, a.minExpected, a.maxExpected)
		}
	})

	t.Run("no users pending", func(t *testing.T) {
		a := newSwapActorCovB(common.BTCAsset, common.ETHAsset)
		a.swapAmount = sdkmath.NewUint(common.One)
		if res := a.getQuote(&simtypes.OpConfig{}); res.Continue || res.Finish {
			t.Fatalf("no users must retry: %+v", res)
		}
	})

	t.Run("unfundable target finishes", func(t *testing.T) {
		user, _, _ := newSwapUserCovB(t, 0, 10)
		a := newSwapActorCovB(common.BTCAsset, common.ETHAsset)
		a.swapAmount = sdkmath.NewUint(common.One)
		if res := a.getQuote(&simtypes.OpConfig{Users: []*simtypes.User{user}}); !res.Finish || res.Error != nil {
			t.Fatalf("unfundable swap must skip: %+v", res)
		}
	})

	t.Run("XMR slot busy", func(t *testing.T) {
		drainXMRSwapSlotForTest()
		defer drainXMRSwapSlotForTest()
		xmrSwapSlot <- struct{}{}
		user, _, _ := newSwapUserCovB(t, 0, 10*common.One)
		user.ChainClients[common.XMRChain] = newCovChainClient()
		a := newSwapActorCovB(common.BTCAsset, common.XMRAsset)
		a.swapAmount = sdkmath.NewUint(common.One)
		if res := a.getQuote(&simtypes.OpConfig{Users: []*simtypes.User{user}}); res.Continue || res.Finish {
			t.Fatalf("busy XMR slot must wait: %+v", res)
		}
		assertReleased(t, user)
		if a.xmrSwapSlotAcquired {
			t.Fatal("slot must not be marked acquired")
		}
	})

	t.Run("quote failure releases user", func(t *testing.T) {
		user, _, _ := newSwapUserCovB(t, 0, 10*common.One)
		a := newSwapActorCovB(common.BTCAsset, common.ETHAsset)
		a.swapAmount = sdkmath.NewUint(common.One)
		api.Handle(covBQuotePath, covBQuoteFn(t, func(uint64) (uint64, uint64, string) { return 0, 0, "halted" }))
		if res := a.getQuote(&simtypes.OpConfig{Users: []*simtypes.User{user}}); res.Continue || res.Finish {
			t.Fatalf("quote failure must retry: %+v", res)
		}
		assertReleased(t, user)
	})

	t.Run("minimum above balance reselects a larger user", func(t *testing.T) {
		small, _, _ := newSwapUserCovB(t, 0, 2*common.One)
		large, _, largeETH := newSwapUserCovB(t, 1, 50*common.One)
		// the large user is busy at first; it becomes visible on the retry
		if !large.Acquire() {
			t.Fatal("acquire")
		}
		quotes := 0
		api.Handle(covBQuotePath, covBQuoteFn(t, func(amount uint64) (uint64, uint64, string) {
			quotes++
			if quotes == 1 {
				large.Release()
			}
			if amount < 5*common.One {
				return 0, 0, "recommended_min_amount_in: 500000000"
			}
			return 1000, 0, ""
		}))
		largeETH.Account = common.NewAccount(0, 0, common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(9))}, true)
		a := newSwapActorCovB(common.BTCAsset, common.ETHAsset)
		a.swapAmount = sdkmath.NewUint(common.One)
		if res := a.getQuote(&simtypes.OpConfig{Users: []*simtypes.User{small, large}}); !res.Continue {
			t.Fatalf("quote failed: %+v", res)
		}
		if a.user != large || a.swapAmount.Uint64() != 505000000 || !a.toBalance.Equal(sdkmath.NewUint(9)) {
			t.Fatalf("expected large user at buffered minimum, got %v %s", a.user.Name(), a.swapAmount)
		}
		assertReleased(t, small)
	})

	t.Run("minimum raised then later quote error retries at new amount", func(t *testing.T) {
		user, _, _ := newSwapUserCovB(t, 0, 10*common.One)
		quotes := 0
		api.Handle(covBQuotePath, covBQuoteFn(t, func(amount uint64) (uint64, uint64, string) {
			quotes++
			switch quotes {
			case 1:
				return 1000, 2 * common.One, ""
			case 2:
				return 0, 0, "transient pool lookup failure"
			}
			return 1000, 0, ""
		}))
		a := newSwapActorCovB(common.BTCAsset, common.ETHAsset)
		a.swapAmount = sdkmath.NewUint(common.One)
		if res := a.getQuote(&simtypes.OpConfig{Users: []*simtypes.User{user}}); !res.Continue || a.swapAmount.Uint64() != 2*common.One {
			t.Fatalf("expected retry at raised amount, got %s (%+v)", a.swapAmount, res)
		}
		if quotes != 3 {
			t.Fatalf("expected 3 quote calls, got %d", quotes)
		}
	})

	t.Run("required swap releases slot on finish", func(t *testing.T) {
		drainXMRSwapSlotForTest()
		defer drainXMRSwapSlotForTest()
		user, _, _ := newSwapUserCovB(t, 0, 10)
		a := newSwapActorCovB(common.BTCAsset, common.XMRAsset)
		a.requireOutbound = true
		a.swapAmount = sdkmath.NewUint(common.One)
		if res := a.getQuote(&simtypes.OpConfig{Users: []*simtypes.User{user}}); !res.Finish || res.Error == nil {
			t.Fatalf("required unfundable swap must fail: %+v", res)
		}
		if len(xmrSwapSlot) != 0 {
			t.Fatal("slot must be free")
		}
	})
}

func TestSwapSendSwapCovB(t *testing.T) {
	api := newCovThornode(t)
	token := covBFirstToken(t)
	user, btc, _ := newSwapUserCovB(t, 0, 10*common.One)
	btc.TxID = "BTCIN"
	a := newSwapActorCovB(common.BTCAsset, token)
	a.user = user
	a.toAddress = "0xdest"
	a.swapAmount = sdkmath.NewUint(common.One)
	if !user.Acquire() {
		t.Fatal("acquire")
	}

	api.Status("/thorchain/inbound_addresses", 400, "bad")
	if res := a.sendSwap(nil); res.Continue || res.Finish {
		t.Fatalf("inbound failure must retry: %+v", res)
	}
	api.JSON("/thorchain/inbound_addresses", covBInbound)
	if res := a.sendSwap(nil); !res.Continue || a.swapTxID != "BTCIN" {
		t.Fatalf("send failed: %+v", res)
	}
	tx := btc.Signed[0]
	wantTo := strings.Split(token.String(), "-")[0]
	if tx.Memo != "=:"+wantTo+":0xdest" || tx.ToAddress != "bc1qvault" || !tx.Coin.Amount.Equal(sdkmath.NewUint(common.One)) {
		t.Fatalf("UTXO swaps must fuzzy match token assets, got %+v", tx)
	}

	btc.BroadcastErr = errors.New("connection refused")
	if res := a.sendSwap(nil); res.Continue || res.Finish || res.Error == nil {
		t.Fatalf("broadcast failure must retry: %+v", res)
	}
	btc.BroadcastErr = simtypes.ErrSimTxUncertain
	if res := a.sendSwap(nil); !res.Finish {
		t.Fatalf("uncertain broadcast must stop: %+v", res)
	}
	btc.BroadcastErr = errors.New("insufficient funds")
	if res := a.sendSwap(nil); !res.Finish || res.Error != nil {
		t.Fatalf("funding failure must skip: %+v", res)
	}
	assertReleased(t, user)

	// XMR destination waits for a spendable vault before sending
	x := newSwapActorCovB(common.BTCAsset, common.XMRAsset)
	x.user = user
	if res := x.sendSwap(&simtypes.OpConfig{}); res.Continue || res.Finish {
		t.Fatalf("XMR swap without vault balance must wait: %+v", res)
	}
}

func TestSwapSendTokenSwapCovB(t *testing.T) {
	api := newCovThornode(t)
	token := covBFirstToken(t)
	client, rpc := newCovEVMClient(t, common.ETHChain, 0)
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.ETHChain: client})
	newActor := func() *SwapActor {
		a := newSwapActorCovB(token, common.BTCAsset)
		if user.IsLocked() {
			user.Release()
		}
		if !user.Acquire() {
			t.Fatal("acquire")
		}
		a.user = user
		a.toAddress = "bc1qdest"
		a.swapAmount = sdkmath.NewUint(common.One)
		return a
	}
	a := newActor()
	retry := func(step string) {
		t.Helper()
		if res := a.sendTokenSwap(nil); res.Continue || res.Finish {
			t.Fatalf("%s: expected retry, got %+v", step, res)
		}
	}
	gasPriceFailsAfter := func(n int, msg string) {
		calls := 0
		rpc.Fns["eth_gasPrice"] = func([]json.RawMessage) (interface{}, *covRPCError) {
			calls++
			if calls > n {
				return nil, &covRPCError{Code: -32000, Message: msg}
			}
			return "0x3b9aca00", nil
		}
	}
	sendFailsAfter := func(n int, msg string) {
		calls := 0
		rpc.Fns["eth_sendRawTransaction"] = func(params []json.RawMessage) (interface{}, *covRPCError) {
			calls++
			if calls > n {
				return nil, &covRPCError{Code: -32000, Message: msg}
			}
			return "0x" + strings.Repeat("ab", 32), nil
		}
	}
	reset := func() {
		delete(rpc.Fns, "eth_gasPrice")
		delete(rpc.Fns, "eth_sendRawTransaction")
	}

	api.Status("/thorchain/inbound_addresses", 400, "bad")
	retry("inbound failure")
	api.JSON("/thorchain/inbound_addresses", `[{"chain":"ETH","address":"0x00000000000000000000000000000000000000aa"}]`)
	retry("missing router")
	api.JSON("/thorchain/inbound_addresses", covBInbound)

	if res := a.sendTokenSwap(nil); !res.Continue || a.swapTxID == "" {
		t.Fatalf("token swap failed: %+v", res)
	}
	txs := rpc.SentTxs(t)
	if len(txs) != 2 {
		t.Fatalf("expected approve + deposit, got %d", len(txs))
	}
	if strings.TrimPrefix(txs[1].Hash().Hex(), "0x") != a.swapTxID {
		t.Fatalf("swap txid must be the deposit hash, got %s", a.swapTxID)
	}
	if !strings.EqualFold(txs[1].To().Hex(), "0x00000000000000000000000000000000000000bb") {
		t.Fatalf("deposit must go to the router, got %s", txs[1].To().Hex())
	}

	gasPriceFailsAfter(0, "rpc unavailable")
	retry("approve sign failure")
	gasPriceFailsAfter(1, "rpc unavailable")
	retry("deposit sign failure")
	reset()

	sendFailsAfter(0, "nonce too low")
	retry("approve broadcast failure")
	sendFailsAfter(1, "nonce too low")
	retry("deposit broadcast failure")

	sendFailsAfter(0, "insufficient funds for gas")
	if res := a.sendTokenSwap(nil); !res.Finish || res.Error != nil {
		t.Fatalf("approve funding failure must skip: %+v", res)
	}
	assertReleased(t, user)
	a = newActor()
	sendFailsAfter(1, "insufficient funds for gas")
	if res := a.sendTokenSwap(nil); !res.Finish || res.Error != nil {
		t.Fatalf("deposit funding failure must skip: %+v", res)
	}
	assertReleased(t, user)
	reset()

	// XMR destination waits for vault balance
	x := newSwapActorCovB(token, common.XMRAsset)
	x.user = user
	if res := x.sendTokenSwap(&simtypes.OpConfig{}); res.Continue || res.Finish {
		t.Fatalf("XMR swap without vault balance must wait: %+v", res)
	}
}

func TestSwapVerifyOutboundCovB(t *testing.T) {
	api := newCovThornode(t)
	stagesPath := "/thorchain/tx/stages/SWAPIN"
	detailsPath := "/thorchain/tx/details/SWAPIN"
	signed := `{"inbound_observed":{"completed":true,"final_count":1},"outbound_signed":{"completed":true}}`
	ethClient := newCovChainClient(common.NewCoin(common.ETHAsset, cosmos.NewUint(1090)))
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.ETHChain: ethClient})
	newActor := func() *SwapActor {
		a := newSwapActorCovB(common.BTCAsset, common.ETHAsset)
		if !user.IsLocked() && !user.Acquire() {
			t.Fatal("acquire")
		}
		a.user = user
		a.swapTxID = "SWAPIN"
		a.swapAmount = sdkmath.NewUint(common.One)
		a.toAddress = "0xdest"
		a.toBalance = sdkmath.NewUint(1000)
		a.minExpected = sdkmath.NewUint(95)
		a.maxExpected = sdkmath.NewUint(105)
		return a
	}
	details := func(outs, actions int, memo *string, original *string, amount string) openapi.TxDetailsResponse {
		d := openapi.TxDetailsResponse{}
		for i := 0; i < outs; i++ {
			d.OutTxs = append(d.OutTxs, openapi.Tx{Coins: []openapi.Coin{{Asset: "ETH.ETH", Amount: amount}}})
		}
		for i := 0; i < actions; i++ {
			d.Actions = append(d.Actions, openapi.TxOutItem{Memo: memo, OriginalMemo: original})
		}
		return d
	}
	out := openapi.PtrString("OUT:SWAPIN")
	a := newActor()
	pending := func(step string) {
		t.Helper()
		if res := a.verifyOutbound(nil); res.Continue || res.Finish {
			t.Fatalf("%s: expected pending, got %+v", step, res)
		}
	}
	failWith := func(step, want string) {
		t.Helper()
		res := a.verifyOutbound(nil)
		if !res.Finish || res.Error == nil || !strings.Contains(res.Error.Error(), want) {
			t.Fatalf("%s: expected error %q, got %+v", step, want, res)
		}
	}

	api.Status(stagesPath, 404, "unknown")
	pending("stages failure")

	// swap finalised releases the XMR slot even before the outbound is signed
	drainXMRSwapSlotForTest()
	defer drainXMRSwapSlotForTest()
	xmrSwapSlot <- struct{}{}
	a.xmrSwapSlotAcquired = true
	api.JSON(stagesPath, `{"inbound_observed":{"completed":true,"final_count":1},"swap_finalised":{"completed":true}}`)
	pending("outbound unsigned")
	if a.xmrSwapSlotAcquired || len(xmrSwapSlot) != 0 {
		t.Fatal("finalised swap must release the XMR slot")
	}

	api.JSON(stagesPath, signed)
	api.Status(detailsPath, 404, "unknown")
	pending("details failure")

	api.JSON(detailsPath, details(2, 1, out, nil, "90"))
	failWith("two outs", "exactly one out transaction")
	api.JSON(detailsPath, details(1, 0, out, nil, "90"))
	failWith("no actions", "exactly one action")
	api.JSON(detailsPath, details(1, 1, nil, openapi.PtrString("REFUND:SWAPIN"), "90"))
	failWith("refund via original memo", "not be a refund")

	api.JSON(detailsPath, details(1, 1, out, nil, "100"))
	ethClient.AccountErr = errors.New("rpc down")
	pending("account failure")
	ethClient.AccountErr = nil

	// in range
	if res := a.verifyOutbound(nil); !res.Finish || res.Error != nil {
		t.Fatalf("in-range outbound must pass: %+v", res)
	}
	assertReleased(t, user)

	// out of range and the refreshed quote cannot be fetched
	a = newActor()
	api.JSON(detailsPath, details(1, 1, out, nil, "200"))
	api.Status(covBQuotePath, 400, "halted")
	failWith("quote refresh failure", "outside tolerance")
	assertReleased(t, user)

	// out of range but within the refreshed quote's drift range
	a = newActor()
	api.Handle(covBQuotePath, covBQuoteFn(t, func(uint64) (uint64, uint64, string) { return 190, 0, "" }))
	if res := a.verifyOutbound(nil); !res.Finish || res.Error != nil {
		t.Fatalf("drifted quote must accept the outbound: %+v", res)
	}

	// out of range of both quotes
	a = newActor()
	api.Handle(covBQuotePath, covBQuoteFn(t, func(uint64) (uint64, uint64, string) { return 1000, 0, "" }))
	failWith("outside refreshed quote", "outside tolerance")
}
