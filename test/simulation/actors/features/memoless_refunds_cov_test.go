package features

import (
	"strings"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

var tokenAssetCov = common.Asset{Chain: common.ETHChain, Symbol: "TKN-0X1", Ticker: "TKN"}

func TestMemolessRefundsTreeCov(t *testing.T) {
	root := MemolessRefunds(rngCov())
	var lower, restore bool
	counts := map[string]int{}
	root.WalkDepthFirst(func(a *Actor) bool {
		switch {
		case a.Name == "Memoless Refund Lower TTL":
			lower = true
		case a.Name == "Memoless Refund Restore TTL":
			restore = true
		case strings.HasPrefix(a.Name, "MemolessRefund-"):
			scenario := strings.Split(a.Name, "-")[1]
			counts[scenario]++
			if scenario == "ExpiredRef" && !strings.Contains(a.Name, "-ETH.") {
				t.Errorf("expired ref must be ETH only: %s", a.Name)
			}
			if strings.Contains(a.Name, "-ETH.ETH") || strings.Contains(a.Name, "-AVAX.AVAX") {
				t.Errorf("gas assets must be skipped: %s", a.Name)
			}
		}
		return true
	})
	if !lower || !restore {
		t.Fatal("ttl lower/restore stages missing")
	}
	if counts["Unparsable"] == 0 || counts["Unparsable"] != counts["NonexistentRef"] || counts["Unparsable"] != counts["UnpoolableTarget"] {
		t.Fatalf("each token needs every always-on scenario: %v", counts)
	}
	if counts["ExpiredRef"] == 0 || counts["ExpiredRef"] >= counts["Unparsable"] {
		t.Fatalf("expired ref should be a strict ETH-only subset: %v", counts)
	}
}

func TestMemolessRefundActorOpsPerScenarioCov(t *testing.T) {
	for scenario, want := range map[MemolessRefundScenario]int{
		MemolessRefundUnparsable:       4,
		MemolessRefundNonexistentRef:   4,
		MemolessRefundUnpoolableTarget: 5,
		MemolessRefundExpiredRef:       6,
	} {
		a := NewMemolessRefundActor(scenario, tokenAssetCov, rngCov())
		if len(a.Ops) != want {
			t.Errorf("%s: want %d ops got %d", scenario, want, len(a.Ops))
		}
		if a.Name != "MemolessRefund-"+scenario.String()+"-ETH.TKN-0X1" {
			t.Errorf("unexpected name %s", a.Name)
		}
		if scenario == MemolessRefundExpiredRef && a.Timeout != 5*time.Minute {
			t.Errorf("expired ref timeout %s", a.Timeout)
		}
	}
	if MemolessRefundScenario(9).String() != "Unknown(9)" {
		t.Fatal("unknown scenario string")
	}
}

func newMemolessCov(s MemolessRefundScenario) *MemolessRefundActor {
	return &MemolessRefundActor{Actor: *NewActor("m", rngCov()), scenario: s, asset: tokenAssetCov}
}

func TestMemolessAcquireUserCov(t *testing.T) {
	routes := newRoutesCov(t, nil)
	a := newMemolessCov(MemolessRefundUnparsable)
	if res := a.acquireUser(&OpConfig{}); res.Continue {
		t.Fatal("pool error must keep pending")
	}
	routes.set("/thorchain/pool/ETH.TKN-0X1", `{"asset":"ETH.TKN-0X1","balance_asset":"100000"}`)
	if res := a.acquireUser(&OpConfig{}); res.Continue {
		t.Fatal("fee error must keep pending")
	}
	routes.set("/thorchain/outbound_fee/ETH.TKN-0X1", `[{"asset":"ETH.TKN-0X1","outbound_fee":"10"}]`)

	locked, _, _ := newUserCov(t, "dog", common.ETHChain)
	locked.Acquire()
	bad, _, badClient := newUserCov(t, "cat", common.ETHChain)
	badClient.accErr = errCov
	poor, _, _ := newUserCov(t, "pig", common.ETHChain, common.NewCoin(tokenAssetCov, cosmos.NewUint(1000)))
	if res := a.acquireUser(&OpConfig{Users: []*User{locked, bad, poor}}); res.Continue || a.user != nil {
		t.Fatalf("no user should qualify, got %+v", res)
	}
	if !a.amount.Equal(cosmos.NewUint(1000)) {
		t.Fatalf("amount should be 1%% of depth, got %s", a.amount)
	}
	if bad.IsLocked() || poor.IsLocked() {
		t.Fatal("rejected users must be released")
	}
	rich, _, _ := newUserCov(t, "pig", common.ETHChain, common.NewCoin(tokenAssetCov, cosmos.NewUint(1001)))
	if res := a.acquireUser(&OpConfig{Users: []*User{rich}}); !res.Continue || a.user != rich {
		t.Fatalf("expected rich user, got %+v", res)
	}
}

func TestMemolessRegisterMemoCov(t *testing.T) {
	routes := newRoutesCov(t, nil)

	// already resolved
	a := newMemolessCov(MemolessRefundUnpoolableTarget)
	a.memolessRef = 5
	if res := a.registerMemo(&OpConfig{}); !res.Continue {
		t.Fatal("resolved ref short-circuits")
	}

	// scenarios without registration pass through
	user, bridge, _ := newUserCov(t, "dog", common.ETHChain)
	a = newMemolessCov(MemolessRefundUnparsable)
	a.user = user
	if res := a.registerMemo(&OpConfig{}); !res.Continue || len(bridge.sent()) != 0 {
		t.Fatal("unparsable scenario must not register")
	}

	// broadcast error
	a = newMemolessCov(MemolessRefundUnpoolableTarget)
	a.user = user
	bridge.err = errCov
	if res := a.registerMemo(&OpConfig{}); res.Continue || res.Error == nil {
		t.Fatalf("expected broadcast error, got %+v", res)
	}
	bridge.err = nil
	bridge.txid = "REGTX"

	// registration sent; memo hash lookup not yet available
	if res := a.registerMemo(&OpConfig{}); res.Continue || res.Error != nil {
		t.Fatalf("expected pending, got %+v", res)
	}
	addr := user.Address(common.ETHChain)
	wantBody := "=:" + unpoolableAsset + ":" + addr.String()
	if a.registeredBody != wantBody || lastMemoCov(t, bridge) != "REFERENCE:ETH.TKN-0X1:"+wantBody {
		t.Fatalf("unexpected registration %q / %q", a.registeredBody, lastMemoCov(t, bridge))
	}
	if _, ok := bridge.sent()[0].(*types.MsgDeposit); !ok {
		t.Fatal("registration must be a MsgDeposit")
	}

	// mismatching memo keeps polling without re-broadcasting
	routes.set("/thorchain/memo/REGTX", `{"reference":"77","memo":"other"}`)
	if res := a.registerMemo(&OpConfig{}); res.Continue || len(bridge.sent()) != 1 {
		t.Fatal("mismatched memo must keep pending without rebroadcast")
	}

	// unparsable reference
	routes.set("/thorchain/memo/REGTX", `{"reference":"x7","memo":"`+wantBody+`"}`)
	if res := a.registerMemo(&OpConfig{}); res.Error == nil {
		t.Fatal("bad reference must error")
	}

	// block lookup fails
	routes.set("/thorchain/memo/REGTX", `{"reference":"77","memo":"`+wantBody+`"}`)
	if res := a.registerMemo(&OpConfig{}); res.Error == nil {
		t.Fatal("block error must surface")
	}
	a.memolessRef = 0

	routes.set("/thorchain/block", `{"header":{"height":300}}`)
	if res := a.registerMemo(&OpConfig{}); !res.Continue || a.memolessRef != 77 || a.registeredHeight != 300 {
		t.Fatalf("expected ref 77 at 300, got %+v ref=%d h=%d", res, a.memolessRef, a.registeredHeight)
	}

	// expired-ref scenario registers a same-chain gas target
	b := newMemolessCov(MemolessRefundExpiredRef)
	b.user = user
	_ = b.registerMemo(&OpConfig{})
	if b.registeredBody != "=:ETH.ETH:"+addr.String() {
		t.Fatalf("unexpected expired body %q", b.registeredBody)
	}
}

func TestMemolessWaitForExpiryCov(t *testing.T) {
	routes := newRoutesCov(t, nil)
	a := newMemolessCov(MemolessRefundExpiredRef)
	a.registeredHeight = 100

	if res := a.waitForExpiry(&OpConfig{}); res.Continue {
		t.Fatal("mimir error must keep pending")
	}
	routes.set("/thorchain/mimir", `{"MEMOLESSTXNTTL":10}`)
	if res := a.waitForExpiry(&OpConfig{}); res.Continue {
		t.Fatal("block error must keep pending")
	}
	routes.set("/thorchain/block", `{"header":{"height":111}}`)
	if res := a.waitForExpiry(&OpConfig{}); res.Continue {
		t.Fatal("111 < 100+10+2 must keep pending")
	}
	routes.set("/thorchain/block", `{"header":{"height":112}}`)
	if res := a.waitForExpiry(&OpConfig{}); !res.Continue {
		t.Fatal("112 reaches expiry")
	}

	// unset mimir falls back to 100
	routes.set("/thorchain/mimir", `{}`)
	ttl, err := memolessTTL()
	if err != nil || ttl != 100 {
		t.Fatalf("fallback ttl %d %v", ttl, err)
	}
}

func TestMemolessPrepareMemoCov(t *testing.T) {
	for s, want := range map[MemolessRefundScenario]string{
		MemolessRefundUnparsable:       "xyz not a valid memo",
		MemolessRefundNonexistentRef:   "r:99998",
		MemolessRefundExpiredRef:       "r:42",
		MemolessRefundUnpoolableTarget: "r:42",
	} {
		a := newMemolessCov(s)
		a.memolessRef = 42
		if res := a.prepareMemo(&OpConfig{}); !res.Continue || a.depositMemo != want {
			t.Errorf("%s: memo %q", s, a.depositMemo)
		}
	}
	a := newMemolessCov(MemolessRefundScenario(99))
	if res := a.prepareMemo(&OpConfig{}); res.Continue || res.Error == nil {
		t.Fatal("unknown scenario must error")
	}
}

func TestMemolessSendInboundErrorCov(t *testing.T) {
	newRoutesCov(t, nil)
	user, _, _ := newUserCov(t, "dog", common.ETHChain)
	a := newMemolessCov(MemolessRefundUnparsable)
	a.user = user
	if res := a.sendInbound(&OpConfig{}); res.Continue || res.Error == nil || a.inboundTxid != "" {
		t.Fatalf("inbound lookup failure must error, got %+v", res)
	}
}

func TestMemolessVerifyRefundCov(t *testing.T) {
	routes := newRoutesCov(t, nil)
	user, _, _ := newUserCov(t, "dog", common.ETHChain)
	a := newMemolessCov(MemolessRefundUnparsable)
	a.user = user
	a.inboundTxid = "IN1"

	if res := a.verifyRefund(&OpConfig{}); res.Continue || res.Finish {
		t.Fatal("stages error must keep pending")
	}
	routes.set("/thorchain/tx/stages/IN1", `{"outbound_signed":{"completed":false}}`)
	if res := a.verifyRefund(&OpConfig{}); res.Finish {
		t.Fatal("incomplete outbound keeps pending")
	}
	routes.set("/thorchain/tx/stages/IN1", `{"outbound_signed":{"completed":true}}`)
	if res := a.verifyRefund(&OpConfig{}); res.Finish {
		t.Fatal("details error keeps pending")
	}

	out := `{"chain":"ETH","coins":[{"asset":"ETH.TKN-0X1","amount":"5"}]}`
	action := `{"chain":"ETH","to_address":"x","coin":{"asset":"ETH.TKN-0X1","amount":"5"},"max_gas":[],"memo":"REFUND:IN1"}`
	for _, tc := range []struct {
		details string
		errPart string
	}{
		{`{"out_txs":[],"actions":[]}`, "exactly one out tx"},
		{`{"out_txs":[` + out + `],"actions":[]}`, "exactly one action"},
		{`{"out_txs":[` + out + `],"actions":[{"chain":"ETH","to_address":"x","coin":{"asset":"A","amount":"1"},"max_gas":[],"memo":"OUT:IN1"}]}`, "expected REFUND"},
		{`{"out_txs":[{"chain":"ETH","coins":[]}],"actions":[` + action + `]}`, "no coins"},
		{`{"out_txs":[{"chain":"ETH","coins":[{"asset":"ETH.ETH","amount":"5"}]}],"actions":[` + action + `]}`, "asset mismatch"},
	} {
		routes.set("/thorchain/tx/details/IN1", tc.details)
		res := a.verifyRefund(&OpConfig{})
		if !res.Finish || res.Error == nil || !strings.Contains(res.Error.Error(), tc.errPart) {
			t.Fatalf("want %q, got %+v", tc.errPart, res)
		}
	}

	// original memo fallback and success releases the user
	user.Acquire()
	routes.set("/thorchain/tx/details/IN1", `{"out_txs":[`+out+`],"actions":[{"chain":"ETH","to_address":"x","coin":{"asset":"ETH.TKN-0X1","amount":"5"},"max_gas":[],"original_memo":"REFUND:IN1"}]}`)
	res := a.verifyRefund(&OpConfig{})
	if !res.Finish || res.Error != nil || user.IsLocked() {
		t.Fatalf("expected verified refund and release, got %+v locked=%v", res, user.IsLocked())
	}
}
