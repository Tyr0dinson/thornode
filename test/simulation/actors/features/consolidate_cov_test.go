package features

import (
	rand "math/rand/v2"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func rngCov() *rand.Rand { return rand.New(rand.NewPCG(11, 12)) }

func TestConsolidateBuildsActorPerUTXOChainCov(t *testing.T) {
	root := Consolidate(rngCov())
	names := map[string]int{}
	for child := range root.Children {
		names[child.Name] = len(child.Ops)
	}
	for _, asset := range []common.Asset{common.BTCAsset, common.BCHAsset, common.DOGEAsset, common.LTCAsset, common.ZECAsset} {
		ops, ok := names["Feature-Consolidate-"+asset.String()]
		if !ok {
			t.Fatalf("missing consolidate actor for %s: %v", asset, names)
		}
		// acquire + height + 10 donations + verify
		if ops != 13 {
			t.Fatalf("expected 13 ops for %s, got %d", asset, ops)
		}
	}
	if len(names) != 5 {
		t.Fatalf("expected 5 children, got %d", len(names))
	}
}

func TestChurnIntervalRestoreActorCov(t *testing.T) {
	a := NewChurnIntervalRestoreActor(rngCov())
	if a.Name != "ChurnInterval-Restore" || len(a.Children) != 1 {
		t.Fatalf("unexpected restore actor %+v", a)
	}
	want := constants.NewConstantValue().GetInt64Value(constants.ChurnInterval)
	for child := range a.Children {
		if !strings.Contains(child.Name, "ChurnInterval=") || !strings.HasSuffix(child.Name, "="+itoaCov(want)) {
			t.Fatalf("unexpected mimir child %s", child.Name)
		}
	}
}

func itoaCov(v int64) string { return cosmos.NewInt(v).String() }

func newConsolidateCov() *ConsolidateActor {
	return &ConsolidateActor{Actor: *NewActor("c", rngCov()), asset: common.BTCAsset, consolidateCount: 1}
}

func TestConsolidateAcquireUserCov(t *testing.T) {
	routes := newRoutesCov(t, nil)
	a := newConsolidateCov()

	// pool query failure keeps the op pending
	if res := a.acquireUser(&OpConfig{}); res.Continue || res.Error != nil {
		t.Fatalf("expected pending on pool error, got %+v", res)
	}

	routes.set("/thorchain/pool/BTC.BTC", `{"asset":"BTC.BTC","balance_asset":"10000"}`)
	locked, _, _ := newUserCov(t, "dog", common.BTCChain, common.NewCoin(common.BTCAsset, cosmos.NewUint(1e6)))
	if !locked.Acquire() {
		t.Fatal("acquire")
	}
	accErr, _, accClient := newUserCov(t, "cat", common.BTCChain)
	accClient.accErr = errCov
	poor, _, _ := newUserCov(t, "pig", common.BTCChain, common.NewCoin(common.BTCAsset, cosmos.NewUint(100)))

	res := a.acquireUser(&OpConfig{Users: []*User{locked, accErr, poor}})
	if res.Continue || a.account != nil {
		t.Fatalf("no eligible user expected, got %+v", res)
	}
	if !a.donateAmount.Equal(cosmos.NewUint(100)) {
		t.Fatalf("donate amount should be 1%% of depth, got %s", a.donateAmount)
	}
	if accErr.IsLocked() || poor.IsLocked() || !locked.IsLocked() {
		t.Fatal("rejected users must be released, pre-locked user untouched")
	}

	rich, _, _ := newUserCov(t, "cat", common.BTCChain, common.NewCoin(common.BTCAsset, cosmos.NewUint(101)))
	res = a.acquireUser(&OpConfig{Users: []*User{locked, rich}})
	if !res.Continue || a.account != rich || !rich.IsLocked() {
		t.Fatalf("expected rich user acquired, got %+v", res)
	}
}

func TestConsolidateHeightDonateVerifyCov(t *testing.T) {
	routes := newRoutesCov(t, nil)
	a := newConsolidateCov()

	if res := a.getHeight(&OpConfig{}); res.Continue {
		t.Fatal("block error must keep op pending")
	}
	routes.set("/thorchain/block", `{"header":{"height":42}}`)
	if res := a.getHeight(&OpConfig{}); !res.Continue || a.scanHeight != 42 {
		t.Fatalf("expected height 42, got %d (%+v)", a.scanHeight, res)
	}

	user, _, client := newUserCov(t, "dog", common.BTCChain)
	a.account = user
	a.donateAmount = cosmos.NewUint(55)

	// inbound address lookup fails
	res := a.donate(&OpConfig{})
	if res.Continue || res.Finish || res.Error == nil {
		t.Fatalf("expected retryable donate error, got %+v", res)
	}

	routes.set("/thorchain/inbound_addresses", `[{"chain":"BTC","address":"bc-vault"}]`)
	if res = a.donate(&OpConfig{}); !res.Continue {
		t.Fatalf("donate should succeed, got %+v", res)
	}
	if len(client.signed) != 1 || client.signed[0].Memo != "DONATE:BTC.BTC" ||
		client.signed[0].ToAddress != "bc-vault" || !client.signed[0].Coin.Amount.Equal(cosmos.NewUint(55)) {
		t.Fatalf("unexpected donate tx %+v", client.signed)
	}

	// verify: block 42 lacks consolidate, block 43 has it
	if !user.Acquire() {
		t.Fatal("acquire")
	}
	routes.set("/thorchain/block?height=42", `{"txs":[{"tx":{"messages":[{"@type":"/types.MsgObservedTxQuorum","quoTx":{"obsTx":{"tx":{"chain":"BTC","memo":"other"}}}}]}}]}`)
	routes.set("/thorchain/block?height=43", `{"txs":[{"tx":{"messages":[
		{"@type":"/types.MsgObservedTxQuorum","quoTx":{"obsTx":{"tx":{"chain":"LTC","memo":"consolidate"}}}},
		{"@type":"/types.MsgObservedTxQuorum","quoTx":{"obsTx":{"tx":{"chain":"BTC","memo":"consolidate"}}}}]}}]}`)
	if res = a.verifyConsolidate(&OpConfig{}); !res.Continue {
		t.Fatalf("expected consolidate found, got %+v", res)
	}
	if a.scanHeight != 44 || user.IsLocked() {
		t.Fatalf("expected scan to advance to 44 and release user, got %d locked=%v", a.scanHeight, user.IsLocked())
	}

	// missing block stops the scan without releasing
	if res = a.verifyConsolidate(&OpConfig{}); res.Continue {
		t.Fatal("missing block must keep op pending")
	}
}

func TestMultiNodeMimirActorCov(t *testing.T) {
	routes := newRoutesCov(t, nil)
	dog, dogBridge, _ := newUserCov(t, "dog", "")
	cat, catBridge, _ := newUserCov(t, "cat", "")
	a := NewMultiNodeMimirActor("FOO", 7, rngCov())
	if a.Name != "Mimir(all) FOO=7" || len(a.Ops) != 1 {
		t.Fatalf("unexpected actor %s", a.Name)
	}
	cfg := &OpConfig{NodeUsers: []*User{dog, cat}}

	if res := a.Ops[0](cfg); res.Continue || res.Error == nil {
		t.Fatalf("nodes error must surface, got %+v", res)
	}

	routes.set("/thorchain/nodes", `[{"node_address":"`+thorAddrCov(t, dog).String()+`","status":"Active"},{"node_address":"`+thorAddrCov(t, cat).String()+`","status":"Standby"}]`)
	if res := a.Ops[0](cfg); !res.Continue {
		t.Fatalf("expected continue, got %+v", res)
	}
	if len(catBridge.sent()) != 0 {
		t.Fatal("inactive node must not vote")
	}
	msgs := dogBridge.sent()
	m, ok := msgs[0].(*types.MsgMimir)
	if len(msgs) != 1 || !ok || m.Key != "FOO" || m.Value != 7 || !m.Signer.Equals(thorAddrCov(t, dog)) {
		t.Fatalf("unexpected mimir votes %v", msgs)
	}

	dogBridge.err = errCov
	if res := a.Ops[0](cfg); res.Continue || res.Error == nil {
		t.Fatalf("broadcast error must surface, got %+v", res)
	}
}
