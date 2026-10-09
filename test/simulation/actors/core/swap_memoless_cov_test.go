package core

import (
	"encoding/json"
	"errors"
	"fmt"
	rand "math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"testing"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const covCQuotePath = "/thorchain/quote/swap"

// covCQuote returns a quote handler; when minIn is non-zero the quote carries
// it as recommended_min_amount_in. Requested amounts are recorded.
func covCQuote(expectedOut, outboundFee, minIn uint64, amounts *[]string, mu *sync.Mutex) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if amounts != nil {
			mu.Lock()
			*amounts = append(*amounts, r.URL.Query().Get("amount"))
			mu.Unlock()
		}
		quote := map[string]interface{}{
			"expected_amount_out": fmt.Sprint(expectedOut),
			"fees":                map[string]interface{}{"asset": "ETH.ETH", "outbound": fmt.Sprint(outboundFee), "liquidity": "0", "total": "0"},
		}
		if minIn > 0 {
			quote["recommended_min_amount_in"] = fmt.Sprint(minIn)
		}
		_ = json.NewEncoder(w).Encode(quote)
	}
}

func newMemolessActorCovC(t *testing.T, from, to common.Asset, memolessType MemolessType, mode MemolessTransferMode) *SwapMemolessActor {
	t.Helper()
	return &SwapMemolessActor{
		SwapActor: SwapActor{
			Actor:                 *simtypes.NewActor("memoless", rand.New(rand.NewPCG(1, 2))),
			from:                  from,
			to:                    to,
			swapAmount:            sdkmath.ZeroUint(),
			fromBalance:           sdkmath.ZeroUint(),
			toBalance:             sdkmath.ZeroUint(),
			requiredOutboundSpend: sdkmath.ZeroUint(),
			minExpected:           sdkmath.ZeroUint(),
			maxExpected:           sdkmath.ZeroUint(),
		},
		memolessType: memolessType,
		transferMode: mode,
	}
}

// withUserCovC attaches an acquired user to the actor as acquireUser would.
func (a *SwapMemolessActor) withUserCovC(t *testing.T, user *simtypes.User, balance uint64) {
	t.Helper()
	if !user.Acquire() {
		t.Fatal("acquire")
	}
	a.user = user
	a.fromAddress = user.Address(a.from.Chain)
	a.toAddress = user.Address(a.to.Chain)
	a.fromBalance = sdkmath.NewUint(balance)
}

func TestSwapMemolessConstructorsAndStringsCovC(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	ref := NewSwapMemolessActor(common.BTCAsset, common.ETHAsset, MemolessTypeRef, rng)
	if ref.Name != "SwapMemoless BTC.BTC => ETH.ETH" || len(ref.Ops) != 10 {
		t.Fatalf("unexpected ref actor %s ops=%d", ref.Name, len(ref.Ops))
	}
	if !strings.Contains(ref.Ops[5].String(), "prepareSwapMemolessByRef") {
		t.Fatalf("ref actor must prepare by ref, got %s", ref.Ops[5])
	}
	amt := NewSwapMemolessActor(common.BTCAsset, common.ETHAsset, MemolessTypeAmount, rng)
	if !strings.Contains(amt.Ops[5].String(), "prepareSwapMemolessByAmount") {
		t.Fatalf("amount actor must prepare by amount, got %s", amt.Ops[5])
	}
	direct := NewDirectERC20SwapMemolessActor(common.BTCAsset, common.ETHAsset, rng)
	if !strings.HasPrefix(direct.Name, "SwapMemoless direct ERC20 ") {
		t.Fatalf("unexpected direct actor name %s", direct.Name)
	}
	unknown := newSwapMemolessActor(common.BTCAsset, common.ETHAsset, MemolessType(7), MemolessTransferModeRouter, rng)
	if len(unknown.Ops) != 4 {
		t.Fatalf("unknown memoless type should only quote and verify, got %d ops", len(unknown.Ops))
	}

	if MemolessTypeRef.String() != "MemolessTypeRef" || MemolessTypeAmount.String() != "MemolessTypeAmount" || MemolessType(9).String() != "MemolessType(9)" {
		t.Fatal("unexpected MemolessType strings")
	}
	if MemolessTransferModeRouter.String() != "router" || MemolessTransferModeDirectERC20.String() != "direct_erc20" || MemolessTransferMode(5).String() != "unknown_5" {
		t.Fatal("unexpected transfer mode strings")
	}

	a := newMemolessActorCovC(t, common.BTCAsset, common.ETHAsset, MemolessTypeRef, MemolessTransferModeRouter)
	if res := a.updateLogContext(nil); !res.Continue {
		t.Fatal("updateLogContext must continue")
	}
}

func TestBuildMemolessSwapRegistrationMemoCovC(t *testing.T) {
	if got := buildMemolessSwapRegistrationMemo(common.BTCAsset, common.ETHAsset, "0xdest", "bc1qrefund"); got != "=:ETH.ETH:0xdest" {
		t.Fatalf("unexpected memo %q", got)
	}
	if got := buildMemolessSwapRegistrationMemo(common.XMRAsset, common.BTCAsset, "bc1qdest", "4refund"); got != "=:BTC.BTC:bc1qdest/4refund" {
		t.Fatalf("XMR source must carry an explicit refund address, got %q", got)
	}
}

func TestSwapMemolessGetQuoteCovC(t *testing.T) {
	api := newCovThornode(t)
	api.Handle(covCQuotePath, covCQuote(5000, 100, 0, nil, nil))
	dust := common.BTCChain.DustThreshold().Uint64()
	newUser := func(idx int, btc uint64) (*simtypes.User, *covChainClient) {
		client := newCovChainClient(common.NewCoin(common.BTCAsset, cosmos.NewUint(btc+dust)))
		user, _ := newCovUser(t, idx, map[common.Chain]simtypes.LiteChainClient{
			common.BTCChain: client,
			common.ETHChain: newCovChainClient(common.NewCoin(common.ETHAsset, cosmos.NewUint(7))),
		})
		return user, client
	}

	t.Run("ref quote sets expectations", func(t *testing.T) {
		user, _ := newUser(0, 10*common.One)
		a := newMemolessActorCovC(t, common.BTCAsset, common.ETHAsset, MemolessTypeRef, MemolessTransferModeRouter)
		a.swapAmount = sdkmath.NewUint(common.One)
		if res := a.getQuote(&simtypes.OpConfig{Users: []*simtypes.User{user}}); !res.Continue {
			t.Fatalf("quote failed: %+v", res)
		}
		if a.user != user || !a.requiredOutboundSpend.Equal(sdkmath.NewUint(5100)) || !a.toBalance.Equal(sdkmath.NewUint(7)) {
			t.Fatalf("unexpected state user=%v spend=%s to=%s", a.user, a.requiredOutboundSpend, a.toBalance)
		}
	})

	t.Run("amount encoding reselects a user with headroom", func(t *testing.T) {
		small, _ := newUser(0, common.One+50)
		big, bigClient := newUser(1, 5*common.One)
		calls := 0
		account := bigClient.Account
		bigClient.AccountFn = func(*common.PubKey) (*common.Account, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("wallet syncing")
			}
			acct := account
			return &acct, nil
		}
		a := newMemolessActorCovC(t, common.BTCAsset, common.ETHAsset, MemolessTypeAmount, MemolessTransferModeRouter)
		a.swapAmount = sdkmath.NewUint(common.One)
		if res := a.getQuote(&simtypes.OpConfig{Users: []*simtypes.User{small, big}}); !res.Continue {
			t.Fatalf("quote failed: %+v", res)
		}
		if a.user != big || !a.swapAmount.Equal(sdkmath.NewUint(common.One)) {
			t.Fatalf("expected the user with headroom and original amount, got %v %s", a.user.Name(), a.swapAmount)
		}
		assertReleased(t, small)
	})

	t.Run("amount encoding without headroom skips", func(t *testing.T) {
		small, _ := newUser(0, common.One+50)
		a := newMemolessActorCovC(t, common.BTCAsset, common.ETHAsset, MemolessTypeAmount, MemolessTransferModeRouter)
		a.swapAmount = sdkmath.NewUint(common.One)
		res := a.getQuote(&simtypes.OpConfig{Users: []*simtypes.User{small}})
		if !res.Finish || res.Error != nil {
			t.Fatalf("unfundable encoded amount must skip the actor: %+v", res)
		}
		assertReleased(t, small)
	})

	t.Run("quote failure stays pending", func(t *testing.T) {
		api.Status(covCQuotePath, 400, "pool halted")
		defer api.Handle(covCQuotePath, covCQuote(5000, 100, 0, nil, nil))
		user, _ := newUser(0, 10*common.One)
		a := newMemolessActorCovC(t, common.BTCAsset, common.ETHAsset, MemolessTypeRef, MemolessTransferModeRouter)
		a.swapAmount = sdkmath.NewUint(common.One)
		if res := a.getQuote(&simtypes.OpConfig{Users: []*simtypes.User{user}}); res.Continue || res.Finish {
			t.Fatalf("quote failure must retry: %+v", res)
		}
		assertReleased(t, user)
	})
}

func TestSwapMemolessRegisterSwapCovC(t *testing.T) {
	user, bridge := newCovUser(t, 0, nil)
	a := newMemolessActorCovC(t, common.BTCAsset, common.ETHAsset, MemolessTypeRef, MemolessTransferModeRouter)
	a.withUserCovC(t, user, common.One)
	if res := a.registerSwap(nil); !res.Continue {
		t.Fatalf("register failed: %+v", res)
	}
	want := "=:ETH.ETH:" + a.toAddress.String()
	if a.registeredMemo != want || a.registrationTxID.IsEmpty() {
		t.Fatalf("unexpected registration %q %s", a.registeredMemo, a.registrationTxID)
	}
	if len(bridge.BlockingMsgs) != 1 || len(bridge.Msgs) != 0 {
		t.Fatal("registration must use a blocking broadcast")
	}
	msg := bridge.BlockingMsgs[0].(*stypes.MsgDeposit)
	if msg.Memo != "REFERENCE:BTC.BTC:"+want || !msg.Coins[0].Amount.IsZero() {
		t.Fatalf("unexpected registration msg %s %s", msg.Memo, msg.Coins)
	}

	bridge.BroadcastWithBlockingFn = func(...sdk.Msg) (common.TxID, error) { return "", errors.New("mempool full") }
	if res := a.registerSwap(nil); res.Continue || res.Finish {
		t.Fatalf("broadcast failure must retry: %+v", res)
	}
}

func TestSwapMemolessGetMemolessRefCovC(t *testing.T) {
	api := newCovThornode(t)
	a := newMemolessActorCovC(t, common.BTCAsset, common.ETHAsset, MemolessTypeRef, MemolessTransferModeRouter)
	a.registrationTxID = "REGTX"
	a.registeredMemo = "=:ETH.ETH:0xabc"
	path := "/thorchain/memo/REGTX"

	api.JSON(path, `{"memo":"=:ETH.ETH:0xother","reference":"42"}`)
	if res := a.getMemolessRef(nil); res.Continue || res.Error == nil || !strings.Contains(res.Error.Error(), "does not match") {
		t.Fatalf("mismatched memo must fail: %+v", res)
	}
	api.JSON(path, `{"memo":"=:ETH.ETH:0xabc","reference":"x42"}`)
	if res := a.getMemolessRef(nil); res.Continue || res.Error == nil {
		t.Fatalf("unparseable reference must fail: %+v", res)
	}
	api.JSON(path, `{"memo":"=:ETH.ETH:0xabc","reference":"00042"}`)
	if res := a.getMemolessRef(nil); !res.Continue || a.memolessRef != 42 {
		t.Fatalf("expected ref 42, got %d (%+v)", a.memolessRef, res)
	}
	if api.Hits(path) != 3 {
		t.Fatalf("each attempt should query once, got %d", api.Hits(path))
	}
}

func TestSwapMemolessPrepareCovC(t *testing.T) {
	a := newMemolessActorCovC(t, common.BTCAsset, common.ETHAsset, MemolessTypeRef, MemolessTransferModeRouter)
	if a.amountModulus() != 100000 {
		t.Fatalf("unexpected modulus %d", a.amountModulus())
	}
	a.memolessRef = 42
	if res := a.prepareSwapMemolessByRef(nil); !res.Continue || a.swapMemo != "r:00042" {
		t.Fatalf("unexpected ref memo %q", a.swapMemo)
	}

	a.swapAmount = sdkmath.NewUint(1234567890)
	if res := a.prepareSwapMemolessByAmount(nil); !res.Continue || a.swapAmount.Uint64() != 1234500042 {
		t.Fatalf("unexpected encoded amount %s", a.swapAmount)
	}

	low := common.XRPAsset // 6 decimal gas asset
	b := newMemolessActorCovC(t, low, common.BTCAsset, MemolessTypeAmount, MemolessTransferModeRouter)
	b.memolessRef = 7
	divisor := b.amountScalingDivisor()
	if divisor <= 1 {
		t.Fatalf("low decimal asset must scale, got %d", divisor)
	}
	b.swapAmount = sdkmath.NewUint(98765432123)
	if res := b.prepareSwapMemolessByAmount(nil); !res.Continue {
		t.Fatal("prepare failed")
	}
	got := b.swapAmount.Uint64()
	if got%divisor != 0 || (got/divisor)%b.amountModulus() != 7 {
		t.Fatalf("scaled amount %d does not encode ref 7 with divisor %d", got, divisor)
	}

	// encoded-ref helpers on scaled amounts round up to the next encodable value
	target := sdkmath.NewUint(divisor*100000*3 + 1)
	at := b.amountWithEncodedRefAtOrAbove(target)
	if at.LT(target) || (at.Uint64()/divisor)%100000 != 7 || at.Uint64()%divisor != 0 {
		t.Fatalf("unexpected encoded amount %s for target %s", at, target)
	}
	b.memolessRef = 0
	above := b.amountWithEncodedRefAtOrAbove(target)
	if above.LT(target) || (above.Uint64()/divisor)%100000 != 0 {
		t.Fatalf("ref below remainder must roll to next modulus, got %s", above)
	}
	maxAmt := b.maxAmountWithEncodedRefAtOrAbove(target)
	if (maxAmt.Uint64()/divisor)%100000 != 99999 || maxAmt.LT(at) {
		t.Fatalf("unexpected max encoded amount %s", maxAmt)
	}

	// 18-decimal tokens and unknown tokens need no scaling
	if high := covCFirstToken(t, common.ETHChain); newMemolessActorCovC(t, high, common.BTCAsset, MemolessTypeAmount, 0).amountScalingDivisor() != 1 {
		t.Fatal("high decimal token must not scale")
	}
	unknown, err := common.NewAsset("ETH.NOPE-0X0000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if newMemolessActorCovC(t, unknown, common.BTCAsset, MemolessTypeAmount, 0).amountScalingDivisor() != 1 {
		t.Fatal("unknown token must not scale")
	}
	if c := newMemolessActorCovC(t, low, common.BTCAsset, MemolessTypeAmount, 0); true {
		c.swapAmount = sdkmath.NewUint(common.One)
		if !c.tokenAmount(6).Equal(sdkmath.NewUint(1_000_000)) || !c.tokenAmount(18).Equal(sdkmath.NewUintFromString("1000000000000000000")) {
			t.Fatal("unexpected token amount conversion")
		}
	}
}

func TestSwapMemolessRequotePreparedSwapCovC(t *testing.T) {
	api := newCovThornode(t)
	user, _ := newCovUser(t, 0, nil)
	a := newMemolessActorCovC(t, common.BTCAsset, common.ETHAsset, MemolessTypeRef, MemolessTransferModeRouter)
	a.withUserCovC(t, user, 10*common.One)
	a.swapAmount = sdkmath.NewUint(common.One)

	api.Status(covCQuotePath, 400, "halted")
	if res := a.requotePreparedSwap(nil); res.Continue || res.Finish {
		t.Fatalf("quote failure must retry: %+v", res)
	}

	api.Handle(covCQuotePath, covCQuote(9000, 0, 2*common.One, nil, nil))
	if res := a.requotePreparedSwap(nil); !res.Continue || !a.swapAmount.Equal(sdkmath.NewUint(2*common.One)) {
		t.Fatalf("expected amount raised to recommended min, got %s (%+v)", a.swapAmount, res)
	}
	if !a.minExpected.Equal(sdkmath.NewUint(8100)) || !a.maxExpected.Equal(sdkmath.NewUint(9900)) {
		t.Fatalf("unexpected expected range %s-%s", a.minExpected, a.maxExpected)
	}

	// an unknown balance (0 => no cap) cannot absorb the recommended minimum
	a.fromBalance = sdkmath.ZeroUint()
	a.swapAmount = sdkmath.NewUint(common.One)
	if res := a.requotePreparedSwap(nil); !res.Finish || res.Error == nil {
		t.Fatalf("minimum above balance must fail: %+v", res)
	}
}

func TestSwapMemolessRequoteEncodedSwapCovC(t *testing.T) {
	api := newCovThornode(t)
	user, _ := newCovUser(t, 0, nil)
	a := newMemolessActorCovC(t, common.BTCAsset, common.ETHAsset, MemolessTypeAmount, MemolessTransferModeRouter)
	a.withUserCovC(t, user, 10*common.One)
	a.memolessRef = 42
	a.swapAmount = sdkmath.NewUint(100000042)

	api.Status(covCQuotePath, 400, "halted")
	if res := a.requoteEncodedSwap(nil); res.Continue || res.Finish {
		t.Fatalf("quote failure must retry: %+v", res)
	}

	var amounts []string
	var mu sync.Mutex
	api.Handle(covCQuotePath, covCQuote(9000, 0, 150000000, &amounts, &mu))
	if res := a.requoteEncodedSwap(nil); !res.Continue {
		t.Fatalf("requote failed: %+v", res)
	}
	if a.swapAmount.Uint64() != 150000042 {
		t.Fatalf("expected re-encoded amount 150000042, got %s", a.swapAmount)
	}
	if amounts[len(amounts)-1] != "150000042" {
		t.Fatalf("final quote must use the encoded amount, got %v", amounts)
	}

	// raised amount above balance fails the actor
	a.swapAmount = sdkmath.NewUint(100000042)
	a.fromBalance = sdkmath.NewUint(150000010)
	if res := a.requoteEncodedSwap(nil); !res.Finish || res.Error == nil {
		t.Fatalf("encoded amount above balance must fail: %+v", res)
	}

	// already settled amount passes straight through
	api.Handle(covCQuotePath, covCQuote(9000, 0, 0, nil, nil))
	a.fromBalance = sdkmath.NewUint(10 * common.One)
	a.swapAmount = sdkmath.NewUint(100000042)
	if res := a.requoteEncodedSwap(nil); !res.Continue || a.swapAmount.Uint64() != 100000042 {
		t.Fatalf("settled amount must continue unchanged: %+v %s", res, a.swapAmount)
	}
}

func TestSwapMemolessSignSwapUTXOCovC(t *testing.T) {
	api := newCovThornode(t)
	client := newCovChainClient()
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.BTCChain: client})
	a := newMemolessActorCovC(t, common.BTCAsset, common.ETHAsset, MemolessTypeRef, MemolessTransferModeRouter)
	a.withUserCovC(t, user, 10*common.One)
	a.swapAmount = sdkmath.NewUint(common.One)
	a.swapMemo = "r:00042"

	api.Status("/thorchain/inbound_addresses", 400, "bad")
	if res := a.signSwap(nil); res.Continue || res.Finish {
		t.Fatalf("inbound failure must retry: %+v", res)
	}

	api.JSON("/thorchain/inbound_addresses", covCInboundAddresses)
	if res := a.signSwap(nil); !res.Continue || string(a.signedSwap) != "signed:r:00042" {
		t.Fatalf("sign failed: %+v %q", res, a.signedSwap)
	}
	if a.swapTx.ToAddress != "bc1qvault" || !a.swapTx.OneShotMemo || !a.swapTx.Coin.Amount.Equal(sdkmath.NewUint(common.One)) {
		t.Fatalf("unexpected swap tx %+v", a.swapTx)
	}

	client.SignErr = errors.New("rpc down")
	if res := a.signSwap(nil); res.Continue || res.Finish {
		t.Fatalf("sign failure must retry: %+v", res)
	}
	client.SignErr = errors.New("insufficient funds for fee")
	if res := a.signSwap(nil); !res.Finish || res.Error != nil {
		t.Fatalf("funding failure must skip the actor: %+v", res)
	}
	assertReleased(t, user)

	// TRON/SOL defer signing to broadcast
	sol := newMemolessActorCovC(t, common.SOLAsset, common.ETHAsset, MemolessTypeRef, MemolessTransferModeRouter)
	solUser, _ := newCovUser(t, 1, map[common.Chain]simtypes.LiteChainClient{common.SOLChain: newCovChainClient()})
	sol.withUserCovC(t, solUser, common.One)
	sol.swapAmount = sdkmath.NewUint(common.One)
	sol.signedSwap = []byte("stale")
	api.JSON("/thorchain/inbound_addresses", `[{"chain":"SOL","address":"SolVault"}]`)
	if res := sol.signSwap(nil); !res.Continue || sol.signedSwap != nil || sol.swapTx.ToAddress != "SolVault" {
		t.Fatalf("SOL signing must be deferred: %+v", res)
	}
}

func TestSwapMemolessSignDirectTokenTransferCovC(t *testing.T) {
	api := newCovThornode(t)
	token := covCFirstToken(t, common.ETHChain)
	client, rpc := newCovEVMClient(t, common.ETHChain, 0)
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.ETHChain: client})
	a := newMemolessActorCovC(t, token, common.BTCAsset, MemolessTypeAmount, MemolessTransferModeDirectERC20)
	a.withUserCovC(t, user, 10*common.One)
	a.swapAmount = sdkmath.NewUint(common.One)

	api.Status("/thorchain/inbound_addresses", 400, "bad")
	if res := a.signSwap(nil); res.Continue || res.Finish {
		t.Fatalf("inbound failure must retry: %+v", res)
	}
	api.JSON("/thorchain/inbound_addresses", covCInboundAddresses)
	if res := a.signSwap(nil); !res.Continue || len(a.signedSwap) == 0 {
		t.Fatalf("direct transfer sign failed: %+v", res)
	}
	if rpc.CallCount("eth_sendRawTransaction") != 0 {
		t.Fatal("signing must not broadcast")
	}

	rpc.Fns["eth_gasPrice"] = func([]json.RawMessage) (interface{}, *covRPCError) {
		return nil, &covRPCError{Code: -32000, Message: "rpc unavailable"}
	}
	if res := a.signSwap(nil); res.Continue || res.Finish {
		t.Fatalf("sign failure must retry: %+v", res)
	}
	rpc.Fns["eth_gasPrice"] = func([]json.RawMessage) (interface{}, *covRPCError) {
		return nil, &covRPCError{Code: -32000, Message: "insufficient funds for gas"}
	}
	if res := a.signSwap(nil); !res.Finish {
		t.Fatalf("funding failure must skip: %+v", res)
	}
	assertReleased(t, user)

	// non-EVM client and unknown token
	other, _ := newCovUser(t, 1, map[common.Chain]simtypes.LiteChainClient{common.ETHChain: newCovChainClient()})
	b := newMemolessActorCovC(t, token, common.BTCAsset, MemolessTypeAmount, MemolessTransferModeDirectERC20)
	b.withUserCovC(t, other, common.One)
	b.swapAmount = sdkmath.NewUint(common.One)
	if res := b.signSwap(nil); res.Continue || res.Finish {
		t.Fatalf("non-EVM client must retry: %+v", res)
	}
	unknown, err := common.NewAsset("ETH.NOPE-0X0000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	b.from = unknown
	if res := b.signSwap(nil); res.Continue || res.Finish {
		t.Fatalf("unknown token must retry: %+v", res)
	}
}

func TestSwapMemolessSignTokenSwapCovC(t *testing.T) {
	api := newCovThornode(t)
	token := covCFirstToken(t, common.ETHChain)
	client, rpc := newCovEVMClient(t, common.ETHChain, 0)
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.ETHChain: client})
	newActor := func() *SwapMemolessActor {
		a := newMemolessActorCovC(t, token, common.BTCAsset, MemolessTypeRef, MemolessTransferModeRouter)
		if user.IsLocked() {
			user.Release()
		}
		a.withUserCovC(t, user, 10*common.One)
		a.swapAmount = sdkmath.NewUint(common.One)
		a.swapMemo = "r:00042"
		return a
	}
	a := newActor()
	retry := func(step string) {
		t.Helper()
		if res := a.signSwap(nil); res.Continue || res.Finish {
			t.Fatalf("%s: expected retry, got %+v", step, res)
		}
	}

	api.Status("/thorchain/inbound_addresses", 400, "bad")
	retry("inbound failure")
	api.JSON("/thorchain/inbound_addresses", `[{"chain":"ETH","address":"0x00000000000000000000000000000000000000aa"}]`)
	retry("missing router")

	api.JSON("/thorchain/inbound_addresses", covCInboundAddresses)
	if res := a.signSwap(nil); !res.Continue || len(a.signedSwap) == 0 {
		t.Fatalf("token swap sign failed: %+v", res)
	}
	if got := len(rpc.SentTxs(t)); got != 1 {
		t.Fatalf("approve must be broadcast before signing the deposit, sent %d", got)
	}

	// approve sign failures
	rpc.Fns["eth_gasPrice"] = func([]json.RawMessage) (interface{}, *covRPCError) {
		return nil, &covRPCError{Code: -32000, Message: "rpc unavailable"}
	}
	retry("approve sign failure")
	rpc.Fns["eth_gasPrice"] = func([]json.RawMessage) (interface{}, *covRPCError) {
		return nil, &covRPCError{Code: -32000, Message: "insufficient funds"}
	}
	if res := a.signSwap(nil); !res.Finish {
		t.Fatalf("approve funding failure must skip: %+v", res)
	}
	delete(rpc.Fns, "eth_gasPrice")

	// approve broadcast failures
	a = newActor()
	rpc.SendErr = "nonce too low"
	retry("approve broadcast failure")
	rpc.SendErr = "insufficient funds for gas * price + value"
	if res := a.signSwap(nil); !res.Finish {
		t.Fatalf("approve broadcast funding failure must skip: %+v", res)
	}
	rpc.SendErr = ""

	// deposit sign failures (second gas price lookup fails)
	for _, msg := range []string{"rpc unavailable", "insufficient funds"} {
		a = newActor()
		calls := 0
		failMsg := msg
		rpc.Fns["eth_gasPrice"] = func([]json.RawMessage) (interface{}, *covRPCError) {
			calls++
			if calls > 1 {
				return nil, &covRPCError{Code: -32000, Message: failMsg}
			}
			return "0x3b9aca00", nil
		}
		res := a.signSwap(nil)
		if failMsg == "insufficient funds" && !res.Finish {
			t.Fatalf("deposit funding failure must skip: %+v", res)
		}
		if failMsg != "insufficient funds" && (res.Continue || res.Finish) {
			t.Fatalf("deposit sign failure must retry: %+v", res)
		}
	}
	delete(rpc.Fns, "eth_gasPrice")

	// non-EVM client and unknown token
	other, _ := newCovUser(t, 1, map[common.Chain]simtypes.LiteChainClient{common.ETHChain: newCovChainClient()})
	b := newMemolessActorCovC(t, token, common.BTCAsset, MemolessTypeRef, MemolessTransferModeRouter)
	b.withUserCovC(t, other, common.One)
	b.swapAmount = sdkmath.NewUint(common.One)
	if res := b.signSwap(nil); res.Continue || res.Finish {
		t.Fatalf("non-EVM client must retry: %+v", res)
	}
	unknown, err := common.NewAsset("ETH.NOPE-0X0000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	b.from = unknown
	if res := b.signSwap(nil); res.Continue || res.Finish {
		t.Fatalf("unknown token must retry: %+v", res)
	}
}

func TestSwapMemolessBroadcastSwapCovC(t *testing.T) {
	client := newCovChainClient()
	client.TxID = "BTCSWAP"
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.BTCChain: client})
	a := newMemolessActorCovC(t, common.BTCAsset, common.ETHAsset, MemolessTypeRef, MemolessTransferModeRouter)
	a.withUserCovC(t, user, common.One)
	a.signedSwap = []byte("payload")

	if res := a.broadcastSwap(nil); !res.Continue || a.swapTxID != "BTCSWAP" {
		t.Fatalf("broadcast failed: %+v %s", res, a.swapTxID)
	}
	if string(client.Broadcast[0]) != "payload" {
		t.Fatal("presigned payload must be broadcast")
	}

	client.BroadcastErr = errors.New("connection refused")
	if res := a.broadcastSwap(nil); res.Continue || res.Finish || res.Error == nil {
		t.Fatalf("transient broadcast failure must retry: %+v", res)
	}
	client.BroadcastErr = errors.New("insufficient balance")
	if res := a.broadcastSwap(nil); !res.Finish || res.Error != nil {
		t.Fatalf("funding failure must skip: %+v", res)
	}
	assertReleased(t, user)
}
