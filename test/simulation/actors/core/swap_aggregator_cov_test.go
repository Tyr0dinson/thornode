package core

import (
	"encoding/json"
	"errors"
	"math/big"
	rand "math/rand/v2"
	"strings"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const covB2Inbound = `[
	{"chain":"BTC","address":"bc1qvault"},
	{"chain":"ETH","address":"0x00000000000000000000000000000000000000aa","router":"0x00000000000000000000000000000000000000bb"}
]`

const covB2InboundNoRouter = `[{"chain":"ETH","address":"0x00000000000000000000000000000000000000aa"}]`

const covB2FinalToken = "0x00000000000000000000000000000000000000cc"

func covB2Token(t *testing.T) (common.Asset, string) {
	t.Helper()
	for asset, token := range evm.Tokens(common.ETHChain) {
		return asset, token.Address
	}
	t.Fatal("no ETH tokens")
	return common.EmptyAsset, ""
}

// covB2RPCFail returns an RPC override failing from the nth call (1-based)
// onward with msg; earlier calls return ok.
func covB2RPCFail(n int, ok interface{}, msg string) func([]json.RawMessage) (interface{}, *covRPCError) {
	calls := 0
	return func([]json.RawMessage) (interface{}, *covRPCError) {
		calls++
		if calls >= n {
			return nil, &covRPCError{Code: -32000, Message: msg}
		}
		return ok, nil
	}
}

const covB2GasPrice = "0x3b9aca00"

// covB2Hash is accepted by ethclient for eth_sendRawTransaction (the result is
// ignored; the client derives the hash locally).
var covB2Hash = "0x" + strings.Repeat("ab", 32)

func newAggActorCovB2(t *testing.T, from, to common.Asset, user *simtypes.User) *AggregatorSwapActor {
	t.Helper()
	a := &AggregatorSwapActor{
		SwapActor: SwapActor{
			Actor:                 *simtypes.NewActor("agg", rand.New(rand.NewPCG(1, 2))),
			from:                  from,
			to:                    to,
			swapAmount:            sdkmath.NewUint(common.One),
			requiredOutboundSpend: sdkmath.ZeroUint(),
		},
		aggregatorSuffix:      "aggr",
		aggregatorTargetAsset: covB2FinalToken,
		aggregatorTargetLimit: "100",
	}
	if user != nil {
		if !user.Acquire() {
			t.Fatal("acquire")
		}
		a.user = user
		a.fromAddress = user.Address(from.Chain)
		a.toAddress = user.Address(to.Chain)
	}
	return a
}

func TestAggregatorConstructorsCovB2(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	token, _ := covB2Token(t)
	gas := NewAggregatorSwapActor(common.BTCAsset, common.ETHAsset, "aggr", covB2FinalToken, "1", rng)
	if gas.Name != "AggSwap BTC.BTC => ETH.ETH" || len(gas.Ops) != 5 || !strings.Contains(gas.Ops[3].String(), "sendAggregatorSwap") {
		t.Fatalf("unexpected gas actor %s ops=%d", gas.Name, len(gas.Ops))
	}
	if gas.Timeout != swapActorTimeout(common.BTCAsset, common.ETHAsset)+2*time.Minute {
		t.Fatalf("unexpected timeout %s", gas.Timeout)
	}
	tok := NewAggregatorSwapActor(token, common.ETHAsset, "aggr", covB2FinalToken, "1", rng)
	if !strings.Contains(tok.Ops[3].String(), "sendAggregatorTokenSwap") {
		t.Fatalf("token source must use router deposit, got %s", tok.Ops[3])
	}
	if aggregatorSwapActorTimeout(common.SOLAsset, common.ETHAsset) != swapActorTimeout(common.SOLAsset, common.ETHAsset)+8*time.Minute {
		t.Fatal("SOL aggregator swaps need the extended timeout")
	}
	in := NewAggregatorSwapInActor(common.ETHAsset, common.BTCAsset, rng)
	if in.Name != "AggSwapIn ETH.ETH => BTC.BTC" || len(in.Ops) != 4 || !strings.Contains(in.Ops[2].String(), "sendAggregatorContractDeposit") {
		t.Fatalf("unexpected swap-in actor %s", in.Name)
	}
}

func TestAggregatorSnapshotFinalTokenBalanceCovB2(t *testing.T) {
	token, tokenAddr := covB2Token(t)

	// non-EVM destination: no verification
	a := newAggActorCovB2(t, common.ETHAsset, common.BTCAsset, nil)
	a.verifyFinalTokenDelivery = true
	if res := a.snapshotFinalTokenBalance(nil); !res.Continue || a.verifyFinalTokenDelivery {
		t.Fatalf("non-EVM destination must skip: %+v", res)
	}

	// same-chain round trip of the final token: skip
	a = newAggActorCovB2(t, token, common.ETHAsset, nil)
	a.aggregatorTargetAsset = strings.ToUpper(tokenAddr)
	if res := a.snapshotFinalTokenBalance(nil); !res.Continue || a.verifyFinalTokenDelivery {
		t.Fatalf("round trip final token must skip: %+v", res)
	}

	// non-EVM client: skip
	plain, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.ETHChain: newCovChainClient()})
	a = newAggActorCovB2(t, common.BTCAsset, common.ETHAsset, plain)
	if res := a.snapshotFinalTokenBalance(nil); !res.Continue || a.verifyFinalTokenDelivery {
		t.Fatalf("non-EVM client must skip: %+v", res)
	}

	client, rpc := newCovEVMClient(t, common.ETHChain, 1)
	user, _ := newCovUser(t, 1, map[common.Chain]simtypes.LiteChainClient{common.ETHChain: client})
	a = newAggActorCovB2(t, common.BTCAsset, common.ETHAsset, user)

	rpc.Fns["eth_getCode"] = covB2RPCFail(1, nil, "code lookup failed")
	if res := a.snapshotFinalTokenBalance(nil); !res.Continue || a.verifyFinalTokenDelivery {
		t.Fatalf("code lookup failure must skip: %+v", res)
	}
	delete(rpc.Fns, "eth_getCode")

	if res := a.snapshotFinalTokenBalance(nil); !res.Continue || a.verifyFinalTokenDelivery {
		t.Fatalf("undeployed aggregator must skip: %+v", res)
	}

	rpc.Code = "0x6080"
	rpc.CallFn = func(string, []byte) (*big.Int, error) { return nil, errors.New("execution reverted") }
	if res := a.snapshotFinalTokenBalance(nil); !res.Continue || a.verifyFinalTokenDelivery {
		t.Fatalf("balance read failure must skip: %+v", res)
	}

	rpc.CallFn = func(to string, data []byte) (*big.Int, error) {
		if to != covB2FinalToken {
			t.Errorf("balance read from %s", to)
		}
		return big.NewInt(500), nil
	}
	if res := a.snapshotFinalTokenBalance(nil); !res.Continue || !a.verifyFinalTokenDelivery || a.finalTokenStartBalance.Int64() != 500 {
		t.Fatalf("deployed aggregator must snapshot: %+v %v", res, a.finalTokenStartBalance)
	}
}

func TestAggregatorSendAggregatorSwapCovB2(t *testing.T) {
	api := newCovThornode(t)
	token, _ := covB2Token(t)
	btc := newCovChainClient()
	btc.TxID = "AGGTX"
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.BTCChain: btc})
	a := newAggActorCovB2(t, common.BTCAsset, token, user)

	api.Status("/thorchain/inbound_addresses", 400, "bad")
	if res := a.sendAggregatorSwap(nil); res.Continue || res.Finish {
		t.Fatalf("inbound failure must retry: %+v", res)
	}

	api.JSON("/thorchain/inbound_addresses", covB2Inbound)
	if res := a.sendAggregatorSwap(nil); !res.Continue || a.swapTxID != "AGGTX" {
		t.Fatalf("swap failed: %+v", res)
	}
	short := strings.Split(token.String(), "-")[0]
	want := "=:" + short + ":" + a.toAddress.String() + "::::aggr:" + covB2FinalToken + ":100"
	if tx := btc.Signed[0]; tx.Memo != want || tx.ToAddress != "bc1qvault" || tx.Coin.Amount.Uint64() != common.One {
		t.Fatalf("unexpected swap tx %+v, want memo %s", tx, want)
	}

	btc.BroadcastErr = simtypes.ErrSimTxUncertain
	if res := a.sendAggregatorSwap(nil); !res.Finish || !errors.Is(res.Error, simtypes.ErrSimTxUncertain) {
		t.Fatalf("uncertain broadcast must stop: %+v", res)
	}
	btc.BroadcastErr = errors.New("rejected")
	if res := a.sendAggregatorSwap(nil); res.Finish || res.Error == nil {
		t.Fatalf("broadcast failure must retry with error: %+v", res)
	}
}

func TestAggregatorSendAggregatorTokenSwapCovB2(t *testing.T) {
	api := newCovThornode(t)
	token, _ := covB2Token(t)
	client, rpc := newCovEVMClient(t, common.ETHChain, 0)
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.ETHChain: client})
	a := newAggActorCovB2(t, token, common.ETHAsset, user)
	retry := func(step string) {
		t.Helper()
		if res := a.sendAggregatorTokenSwap(nil); res.Continue || res.Finish {
			t.Fatalf("%s: expected retry, got %+v", step, res)
		}
	}

	api.Status("/thorchain/inbound_addresses", 400, "bad")
	retry("inbound failure")
	api.JSON("/thorchain/inbound_addresses", covB2InboundNoRouter)
	retry("missing router")
	api.JSON("/thorchain/inbound_addresses", covB2Inbound)

	rpc.Fns["eth_gasPrice"] = covB2RPCFail(1, covB2GasPrice, "down")
	retry("approve sign failure")
	rpc.Fns["eth_gasPrice"] = covB2RPCFail(2, covB2GasPrice, "down")
	rpc.Fns["eth_sendRawTransaction"] = covB2RPCFail(1, covB2Hash, "nonce too low")
	retry("approve broadcast failure")
	rpc.Fns["eth_sendRawTransaction"] = covB2RPCFail(100, covB2Hash, "")
	retry("deposit sign failure")
	delete(rpc.Fns, "eth_gasPrice")
	rpc.Fns["eth_sendRawTransaction"] = covB2RPCFail(2, covB2Hash, "replacement underpriced")
	retry("deposit broadcast failure")
	if a.swapTxID != "" {
		t.Fatal("failed attempts must not record a swap")
	}
	delete(rpc.Fns, "eth_sendRawTransaction")

	if res := a.sendAggregatorTokenSwap(nil); !res.Continue || a.swapTxID == "" {
		t.Fatalf("token swap failed: %+v", res)
	}
	if got := len(rpc.SentTxs(t)); got != 2 {
		t.Fatalf("expected approve + deposit, got %d", got)
	}
}

func covB2Details(action openapi.TxOutItem, outs ...openapi.Tx) openapi.TxDetailsResponse {
	return openapi.TxDetailsResponse{Actions: []openapi.TxOutItem{action}, OutTxs: outs}
}

func TestAggregatorVerifyOutboundCovB2(t *testing.T) {
	api := newCovThornode(t)
	client, rpc := newCovEVMClient(t, common.ETHChain, 0)
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.ETHChain: client})
	a := newAggActorCovB2(t, common.BTCAsset, common.ETHAsset, user)
	user.Release()
	a.swapTxID = "AGG"
	stages := "/thorchain/tx/stages/AGG"
	detailsPath := "/thorchain/tx/details/AGG"
	to := a.toAddress.String()
	goodAction := func() openapi.TxOutItem {
		return openapi.TxOutItem{
			Chain: "ETH", ToAddress: to, Coin: openapi.Coin{Asset: "ETH.ETH", Amount: "1"},
			Memo:                  openapi.PtrString("OUT:AGG"),
			Aggregator:            openapi.PtrString("0x1234aggr"),
			AggregatorTargetAsset: openapi.PtrString(covB2FinalToken),
			AggregatorTargetLimit: openapi.PtrString("100"),
		}
	}
	out := openapi.Tx{ToAddress: openapi.PtrString(to), Coins: []openapi.Coin{{Asset: "ETH.ETH", Amount: "1"}}}

	pending := func(step string) {
		t.Helper()
		if !user.Acquire() {
			t.Fatalf("%s: user still locked", step)
		}
		if res := a.verifyAggregatorOutbound(nil); res.Continue || res.Finish {
			t.Fatalf("%s: expected retry, got %+v", step, res)
		}
		user.Release()
	}
	final := func(step, wantErr string) {
		t.Helper()
		if !user.Acquire() {
			t.Fatalf("%s: user still locked", step)
		}
		res := a.verifyAggregatorOutbound(nil)
		if !res.Finish {
			t.Fatalf("%s: expected terminal result, got %+v", step, res)
		}
		if wantErr == "" && res.Error != nil {
			t.Fatalf("%s: unexpected error %v", step, res.Error)
		}
		if wantErr != "" && (res.Error == nil || !strings.Contains(res.Error.Error(), wantErr)) {
			t.Fatalf("%s: expected %q, got %v", step, wantErr, res.Error)
		}
		if user.IsLocked() {
			t.Fatalf("%s: user not released", step)
		}
	}

	api.Status(stages, 404, "missing")
	pending("stages failure")
	api.JSON(stages, `{"inbound_observed":{"completed":true,"final_count":1}}`)
	pending("outbound not signed")
	api.JSON(stages, `{"inbound_observed":{"completed":true,"final_count":1},"outbound_signed":{"completed":true}}`)
	api.Status(detailsPath, 404, "missing")
	pending("details failure")

	api.JSON(detailsPath, openapi.TxDetailsResponse{Actions: []openapi.TxOutItem{goodAction()}})
	final("no out tx", "exactly one out transaction")
	api.JSON(detailsPath, openapi.TxDetailsResponse{OutTxs: []openapi.Tx{out}})
	final("no action", "exactly one action")

	refund := goodAction()
	refund.Memo = nil
	refund.OriginalMemo = openapi.PtrString("REFUND:AGG")
	api.JSON(detailsPath, covB2Details(refund, out))
	final("refund", "refunded")

	direct := goodAction()
	direct.Aggregator = nil
	api.JSON(detailsPath, covB2Details(direct, out))
	final("direct fallback", "")
	otherOut := out
	otherOut.ToAddress = openapi.PtrString("0xsomeoneelse")
	api.JSON(detailsPath, covB2Details(direct, otherOut))
	final("missing aggregator", "missing aggregator field")

	wrong := goodAction()
	wrong.Aggregator = openapi.PtrString("0x1234other")
	api.JSON(detailsPath, covB2Details(wrong, out))
	final("aggregator suffix", "does not match suffix")

	wrong = goodAction()
	wrong.AggregatorTargetAsset = nil
	api.JSON(detailsPath, covB2Details(wrong, out))
	final("missing target", "missing aggregator_target_asset")
	wrong.AggregatorTargetAsset = openapi.PtrString("0xdead")
	api.JSON(detailsPath, covB2Details(wrong, out))
	final("target mismatch", "does not match expected")

	wrong = goodAction()
	wrong.AggregatorTargetLimit = nil
	api.JSON(detailsPath, covB2Details(wrong, out))
	final("missing limit", "missing aggregator_target_limit")
	wrong.AggregatorTargetLimit = openapi.PtrString("7")
	api.JSON(detailsPath, covB2Details(wrong, out))
	final("limit mismatch", "aggregator_target_limit")

	api.JSON(detailsPath, covB2Details(goodAction(), out))
	final("metadata only", "")

	// finalToken delivery verification
	a.verifyFinalTokenDelivery = true
	a.finalTokenStartBalance = big.NewInt(1000)
	rpc.CallFn = func(string, []byte) (*big.Int, error) { return nil, errors.New("reverted") }
	pending("balance read failure")
	rpc.CallFn = func(string, []byte) (*big.Int, error) { return big.NewInt(1000), nil }
	final("no delta", "did not increase")
	rpc.CallFn = func(string, []byte) (*big.Int, error) { return big.NewInt(1050), nil }
	final("delta below min", "below configured amountOutMin")
	rpc.CallFn = func(string, []byte) (*big.Int, error) { return big.NewInt(1100), nil }
	final("delivered", "")
	a.finalTokenStartBalance = nil
	final("missing snapshot", "missing finalToken start balance")

	a.user.ChainClients = map[common.Chain]simtypes.LiteChainClient{common.ETHChain: newCovChainClient()}
	final("non-EVM client", "expected evm client")
}

func TestAggregatorSwapInDepositCovB2(t *testing.T) {
	api := newCovThornode(t)
	client, rpc := newCovEVMClient(t, common.ETHChain, 0)
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.ETHChain: client})
	newIn := func(from common.Asset) *AggregatorSwapInActor {
		if user.IsLocked() {
			user.Release()
		}
		agg := newAggActorCovB2(t, from, common.BTCAsset, user)
		return &AggregatorSwapInActor{SwapActor: agg.SwapActor}
	}
	a := newIn(common.ETHAsset)
	retry := func(step string) {
		t.Helper()
		if res := a.sendAggregatorContractDeposit(nil); res.Continue || res.Finish {
			t.Fatalf("%s: expected retry, got %+v", step, res)
		}
	}

	nonEVM := &AggregatorSwapInActor{SwapActor: SwapActor{Actor: *simtypes.NewActor("in", rand.New(rand.NewPCG(1, 2))), from: common.BTCAsset, to: common.ETHAsset}}
	if res := nonEVM.sendAggregatorContractDeposit(nil); !res.Finish || res.Error == nil {
		t.Fatalf("non-EVM source must fail: %+v", res)
	}

	api.Status("/thorchain/inbound_addresses", 400, "bad")
	retry("inbound failure")
	api.JSON("/thorchain/inbound_addresses", covB2InboundNoRouter)
	retry("missing router")
	api.JSON("/thorchain/inbound_addresses", covB2Inbound)
	rpc.Fns["eth_getCode"] = covB2RPCFail(1, nil, "code lookup failed")
	retry("code lookup failure")
	delete(rpc.Fns, "eth_getCode")

	if res := a.sendAggregatorContractDeposit(nil); !res.Finish || res.Error != nil || user.IsLocked() {
		t.Fatalf("undeployed aggregator must skip and release: %+v", res)
	}

	rpc.Code = "0x6080"
	// gas path
	a = newIn(common.ETHAsset)
	rpc.Fns["eth_gasPrice"] = covB2RPCFail(1, covB2GasPrice, "down")
	retry("gas swapIn sign failure")
	delete(rpc.Fns, "eth_gasPrice")
	rpc.SendErr = "insufficient funds"
	retry("gas swapIn broadcast failure")
	rpc.SendErr = ""
	if res := a.sendAggregatorContractDeposit(nil); !res.Continue || a.swapTxID == "" {
		t.Fatalf("gas swap-in failed: %+v", res)
	}
	sent := rpc.SentTxs(t)
	if len(sent) != 1 || sent[0].Value().Cmp(new(big.Int).Mul(big.NewInt(common.One), big.NewInt(1e10))) != 0 {
		t.Fatalf("gas swap-in must forward the wei amount, got %d txs", len(sent))
	}
	if !strings.EqualFold(sent[0].To().Hex(), evm.MocknetAggregatorAddress) {
		t.Fatalf("swap-in must call the aggregator, got %s", sent[0].To().Hex())
	}

	// token path
	token, _ := covB2Token(t)
	unknown, err := common.NewAsset("ETH.NOPE-0X0000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	a = newIn(unknown)
	if res := a.sendAggregatorContractDeposit(nil); !res.Finish || res.Error == nil {
		t.Fatalf("unknown token must fail: %+v", res)
	}

	a = newIn(token)
	rpc.Fns["eth_gasPrice"] = covB2RPCFail(1, covB2GasPrice, "down")
	retry("approve sign failure")
	rpc.Fns["eth_gasPrice"] = covB2RPCFail(2, covB2GasPrice, "down")
	rpc.Fns["eth_sendRawTransaction"] = covB2RPCFail(1, covB2Hash, "nonce too low")
	retry("approve broadcast failure")
	rpc.Fns["eth_sendRawTransaction"] = covB2RPCFail(100, covB2Hash, "")
	retry("swapIn sign failure")
	delete(rpc.Fns, "eth_gasPrice")
	rpc.Fns["eth_sendRawTransaction"] = covB2RPCFail(2, covB2Hash, "underpriced")
	retry("swapIn broadcast failure")
	delete(rpc.Fns, "eth_sendRawTransaction")
	before := len(rpc.SentTxs(t))
	if res := a.sendAggregatorContractDeposit(nil); !res.Continue || a.swapTxID == "" {
		t.Fatalf("token swap-in failed: %+v", res)
	}
	if got := len(rpc.SentTxs(t)) - before; got != 2 {
		t.Fatalf("expected approve + swapIn, got %d", got)
	}
}
