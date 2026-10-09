package features

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// bondYieldFixtureCov wires a node user (dog), a registrar user (pig) and a
// fake thornode so the bond-yield ops can be driven one at a time.
type bondYieldFixtureCov struct {
	routes     *routesCov
	state      *BondYieldState
	node       *User
	nodeBridge *fakeBridgeCov
	owner      *User
	ownerBr    *fakeBridgeCov
	nodeAddr   string
	ownerAddr  string
	ethAddr    string
}

func newBondYieldFixtureCov(t *testing.T) *bondYieldFixtureCov {
	t.Helper()
	f := &bondYieldFixtureCov{routes: newRoutesCov(t, nil), state: NewBondYieldState()}
	f.node, f.nodeBridge, _ = newUserCov(t, "dog", "")
	f.owner, f.ownerBr, _ = newUserCov(t, "pig", "")
	f.nodeAddr = thorAddrCov(t, f.node).String()
	f.ownerAddr = f.owner.Address(common.THORChain).String()
	f.ethAddr = f.owner.Address(common.ETHChain).String()
	return f
}

// prime fills the state as if setup had selected node and owner.
func (f *bondYieldFixtureCov) prime(t *testing.T) {
	t.Helper()
	acc, err := cosmos.AccAddressFromBech32(f.nodeAddr)
	if err != nil {
		t.Fatal(err)
	}
	f.state.node = f.node
	f.state.nodeAddress = acc
	f.state.providerAddress = common.Address(f.nodeAddr)
	f.state.owner = f.owner
	f.state.payoutName = "simyieldpig"
}

func (f *bondYieldFixtureCov) setTHORName(owner, aliasChain, aliasAddr, pref string, mult int64, collector string) {
	f.routes.set("/thorchain/thorname/simyieldpig", fmt.Sprintf(
		`{"owner":%q,"preferred_asset":%q,"preferred_asset_outbound_fee_multiplier":%d,"affiliate_collector_rune":%q,"aliases":[{"chain":%q,"address":%q}]}`,
		owner, pref, mult, collector, aliasChain, aliasAddr))
}

func (f *bondYieldFixtureCov) setNode(status, award string, providers string) {
	f.routes.set("/thorchain/node/"+f.nodeAddr, fmt.Sprintf(
		`{"status":%q,"current_award":%q,"bond_providers":{"providers":[%s]}}`, status, award, providers))
}

func TestBondYieldConstructorsCov(t *testing.T) {
	state := NewBondYieldState()
	if !state.payoutAsset.Equals(common.ETHAsset) || state.SkipReason() != "" {
		t.Fatal("unexpected default state")
	}
	rng := rngCov()
	for _, tc := range []struct {
		a       *Actor
		ops     int
		timeout time.Duration
	}{
		{NewBondYieldSetupActor(state, rng), 11, 5 * time.Minute},
		{NewBondYieldSelectTargetActor(state, rng), 2, 2 * time.Minute},
		{NewBondYieldTargetAutoClaimActor(state, rng), 7, 2 * time.Minute},
		{NewBondYieldChurnOutActor(state, rng), 4, 35 * time.Minute},
	} {
		if len(tc.a.Ops) != tc.ops || tc.a.Timeout != tc.timeout {
			t.Errorf("%s: ops=%d timeout=%s", tc.a.Name, len(tc.a.Ops), tc.a.Timeout)
		}
	}

	cleanup := NewBondYieldCleanupActor(rng)
	var keys []string
	cleanup.WalkDepthFirst(func(a *Actor) bool {
		if strings.HasPrefix(a.Name, "Mimir(all) ") {
			keys = append(keys, strings.Split(strings.TrimPrefix(a.Name, "Mimir(all) "), "=")[0])
		}
		return true
	})
	if strings.Join(keys, ",") != "BondYieldEnabled,MinimumL1OutboundFeeUSD,MinOutboundFeeMultiplierBasisPoints,ChurnInterval" {
		t.Fatalf("unexpected cleanup chain %v", keys)
	}
}

func TestBondYieldRequireClusterAndSkipCov(t *testing.T) {
	f := newBondYieldFixtureCov(t)
	setup := &BondYieldSetupActor{Actor: *NewActor("s", rngCov()), state: f.state}
	if res := setup.requireMocknetCluster(nil); res.Continue || res.Finish {
		t.Fatal("nodes error keeps pending")
	}

	sel := &BondYieldSelectTargetActor{Actor: *NewActor("sel", rngCov()), state: f.state}
	ac := &BondYieldTargetAutoClaimActor{Actor: *NewActor("ac", rngCov()), state: f.state}
	co := &BondYieldChurnOutActor{Actor: *NewActor("co", rngCov()), state: f.state}
	for _, skip := range []func(*OpConfig) OpResult{sel.skipIfRequired, ac.skipIfRequired, co.skipIfRequired} {
		if res := skip(nil); !res.Continue {
			t.Fatal("no skip reason must continue")
		}
	}

	f.routes.set("/thorchain/nodes", `[{"node_address":"a","status":"Active"}]`)
	if res := setup.requireMocknetCluster(nil); !res.Finish || !strings.Contains(f.state.SkipReason(), "node_count=1") {
		t.Fatalf("single node must skip, got %+v %q", res, f.state.SkipReason())
	}
	for _, skip := range []func(*OpConfig) OpResult{sel.skipIfRequired, ac.skipIfRequired, co.skipIfRequired} {
		if res := skip(nil); !res.Finish || res.Continue {
			t.Fatal("skip reason must finish")
		}
	}

	f.state.skipReason = ""
	f.routes.set("/thorchain/nodes", `[{"node_address":"a"},{"node_address":"b"}]`)
	if res := setup.requireMocknetCluster(nil); !res.Continue {
		t.Fatal("cluster detected must continue")
	}
}

func TestBondYieldAcquireActiveNodeAndOwnerCov(t *testing.T) {
	f := newBondYieldFixtureCov(t)
	setup := &BondYieldSetupActor{Actor: *NewActor("s", rngCov()), state: f.state}
	cat, _, _ := newUserCov(t, "cat", "")
	cfg := &OpConfig{NodeUsers: []*User{cat, f.node}}

	if res := setup.acquireActiveNodeAndOwner(cfg); res.Continue {
		t.Fatal("nodes error keeps pending")
	}
	f.routes.set("/thorchain/nodes", `[{"node_address":"x","status":"Active"},{"node_address":"y","status":"Active"}]`)
	if res := setup.acquireActiveNodeAndOwner(cfg); res.Continue {
		t.Fatal("two active nodes keeps pending")
	}
	f.routes.set("/thorchain/nodes", `[{"node_address":"x","status":"Active"}]`)
	if res := setup.acquireActiveNodeAndOwner(cfg); res.Continue || f.state.node != nil {
		t.Fatal("no matching node user keeps pending")
	}

	f.routes.set("/thorchain/nodes", `[{"node_address":"`+f.nodeAddr+`","status":"Active"},{"node_address":"z","status":"Standby"}]`)
	locked, _, _ := newUserCov(t, "cat", "")
	locked.Acquire()
	poor, _, _ := newUserCov(t, "dog", "")
	f.routes.set("/cosmos/bank/v1beta1/balances/"+poor.Address(common.THORChain).String(), `{"balances":[{"denom":"rune","amount":"100"}]}`)
	cfg.Users = []*User{locked, f.owner, poor}
	// owner has no balance route yet -> error -> skipped
	if res := setup.acquireActiveNodeAndOwner(cfg); res.Continue || f.state.owner != nil {
		t.Fatal("no funded owner keeps pending")
	}
	if f.state.node != f.node || f.state.providerAddress.String() != f.nodeAddr {
		t.Fatal("active node must be selected")
	}

	f.routes.set("/cosmos/bank/v1beta1/balances/"+f.ownerAddr, `{"balances":[{"denom":"rune","amount":"5000000000"}]}`)
	if res := setup.acquireActiveNodeAndOwner(cfg); !res.Continue || f.state.owner != f.owner || f.state.payoutName != "simyieldpig" {
		t.Fatalf("expected owner pig, got %+v %q", res, f.state.payoutName)
	}
}

func TestBondYieldMimirOpsCov(t *testing.T) {
	f := newBondYieldFixtureCov(t)
	f.prime(t)
	setup := &BondYieldSetupActor{Actor: *NewActor("s", rngCov()), state: f.state}

	f.node.Acquire()
	if res := setup.enableBondYield(nil); res.Continue {
		t.Fatal("locked node keeps pending")
	}
	f.node.Release()

	f.nodeBridge.err = errCov
	if res := setup.enableBondYield(nil); res.Continue {
		t.Fatal("broadcast error keeps pending")
	}
	if res := setup.setLowOutboundFeeMimirs(nil); res.Continue || res.Finish {
		t.Fatal("fee mimir broadcast error keeps pending")
	}
	f.nodeBridge.err = nil

	if res := setup.enableBondYield(nil); !res.Continue || f.node.IsLocked() {
		t.Fatal("enable must succeed and release node")
	}
	if res := setup.setLowOutboundFeeMimirs(nil); !res.Continue {
		t.Fatal("fee mimirs must succeed")
	}
	got := map[string]int64{}
	for _, m := range f.nodeBridge.sent() {
		mm := m.(*types.MsgMimir)
		if mm.Signer.String() != f.nodeAddr {
			t.Fatalf("wrong signer %s", mm.Signer)
		}
		got[mm.Key] = mm.Value
	}
	if got["BondYieldEnabled"] != 1 || got[bondYieldMinL1OutboundFeeUSDKey] != 0 || got[bondYieldMinOutboundFeeMultKey] != 1 || len(got) != 3 {
		t.Fatalf("unexpected mimir votes %v", got)
	}

	if res := setup.waitBondYieldMimirs(nil); res.Continue {
		t.Fatal("mimir error keeps pending")
	}
	f.routes.set("/thorchain/mimir", `{"BONDYIELDENABLED":1}`)
	if res := setup.waitBondYieldMimirs(nil); res.Continue {
		t.Fatal("missing fee mimirs keeps pending")
	}
	f.routes.set("/thorchain/mimir", `{"BONDYIELDENABLED":1,"MINIMUML1OUTBOUNDFEEUSD":0,"MINOUTBOUNDFEEMULTIPLIERBASISPOINTS":1}`)
	if res := setup.waitBondYieldMimirs(nil); !res.Continue {
		t.Fatal("case-insensitive mimirs must match")
	}
}

func TestBondYieldSetupTHORNameFlowCov(t *testing.T) {
	f := newBondYieldFixtureCov(t)
	f.prime(t)
	setup := &BondYieldSetupActor{Actor: *NewActor("s", rngCov()), state: f.state}

	// registration
	f.ownerBr.err = errCov
	if res := setup.registerPayoutTHORName(nil); res.Continue {
		t.Fatal("registration error keeps pending")
	}
	f.ownerBr.err = nil
	if res := setup.registerPayoutTHORName(nil); !res.Continue {
		t.Fatal("registration must succeed")
	}
	want := fmt.Sprintf("~:simyieldpig:ETH:%s:%s:ETH.ETH:0:10000", f.ethAddr, f.ownerAddr)
	if memo := lastMemoCov(t, f.ownerBr); memo != want {
		t.Fatalf("registration memo %q want %q", memo, want)
	}
	dep := f.ownerBr.sent()[0].(*types.MsgDeposit)
	if !dep.Coins[0].Amount.Equal(cosmos.NewUint(bondYieldTHORNameFundRune)) {
		t.Fatalf("registration must fund %d rune, got %s", bondYieldTHORNameFundRune, dep.Coins[0].Amount)
	}

	// wait registered: walk every pending branch then success
	if res := setup.waitPayoutTHORNameRegistered(nil); res.Continue {
		t.Fatal("thorname error keeps pending")
	}
	for _, tc := range []struct {
		owner, chain, addr, pref string
		mult                     int64
	}{
		{"someone", "ETH", f.ethAddr, "ETH.ETH", 10000},
		{f.ownerAddr, "BTC", f.ethAddr, "ETH.ETH", 10000},
		{f.ownerAddr, "ETH", f.ethAddr, "BTC.BTC", 10000},
		{f.ownerAddr, "ETH", f.ethAddr, "ETH.ETH", 5},
	} {
		f.setTHORName(tc.owner, tc.chain, tc.addr, tc.pref, tc.mult, "")
		if res := setup.waitPayoutTHORNameRegistered(nil); res.Continue {
			t.Fatalf("expected pending for %+v", tc)
		}
	}
	f.setTHORName(f.ownerAddr, "ETH", f.ethAddr, "ETH.ETH", 10000, "")
	if res := setup.waitPayoutTHORNameRegistered(nil); !res.Continue {
		t.Fatal("registered thorname must continue")
	}

	// transfer to provider
	f.ownerBr.err = errCov
	if res := setup.transferPayoutTHORNameToProvider(nil); res.Continue {
		t.Fatal("transfer error keeps pending")
	}
	f.ownerBr.err = nil
	if res := setup.transferPayoutTHORNameToProvider(nil); !res.Continue {
		t.Fatal("transfer must succeed")
	}
	if memo := lastMemoCov(t, f.ownerBr); memo != fmt.Sprintf("~:simyieldpig:THOR:%s:%s::0:-1", f.ownerAddr, f.nodeAddr) {
		t.Fatalf("transfer memo %q", memo)
	}

	f.routes.del("/thorchain/thorname/simyieldpig")
	if res := setup.waitPayoutTHORNameOwnedByProvider(nil); res.Continue {
		t.Fatal("thorname error keeps pending")
	}
	if res := setup.waitPayoutTHORName(nil); res.Continue {
		t.Fatal("thorname error keeps pending")
	}
	if res := setup.waitPayoutTHORNameOwnedByProvider(nil); res.Continue {
		t.Fatal("still owned by registrar")
	}
	f.setTHORName(f.ownerAddr, "ETH", f.ethAddr, "ETH.ETH", 10000, "")
	if res := setup.waitPayoutTHORNameOwnedByProvider(nil); res.Continue {
		t.Fatal("still owned by registrar")
	}
	f.setTHORName(f.nodeAddr, "ETH", f.ethAddr, "ETH.ETH", 10000, "")
	if res := setup.waitPayoutTHORNameOwnedByProvider(nil); !res.Continue || f.state.payoutOwnerNode != f.node || f.state.payoutOwnerAddress.String() != f.nodeAddr {
		t.Fatal("provider ownership must be recorded")
	}

	// provider re-config signed by the node
	f.nodeBridge.err = errCov
	if res := setup.configureProviderPayoutTHORName(nil); res.Continue {
		t.Fatal("config error keeps pending")
	}
	f.nodeBridge.err = nil
	if res := setup.configureProviderPayoutTHORName(nil); !res.Continue {
		t.Fatal("config must succeed")
	}
	if memo := lastMemoCov(t, f.nodeBridge); memo != fmt.Sprintf("~:simyieldpig:ETH:%s:%s:ETH.ETH:0:10000", f.ethAddr, f.nodeAddr) {
		t.Fatalf("provider config memo %q", memo)
	}

	for _, tc := range []struct {
		owner, chain, pref string
		mult               int64
	}{
		{f.ownerAddr, "ETH", "ETH.ETH", 10000},
		{f.nodeAddr, "BTC", "ETH.ETH", 10000},
		{f.nodeAddr, "ETH", "BTC.BTC", 10000},
		{f.nodeAddr, "ETH", "ETH.ETH", 1},
	} {
		f.setTHORName(tc.owner, tc.chain, f.ethAddr, tc.pref, tc.mult, "")
		if res := setup.waitPayoutTHORName(nil); res.Continue {
			t.Fatalf("expected pending for %+v", tc)
		}
	}
	f.setTHORName(f.nodeAddr, "ETH", f.ethAddr, "ETH.ETH", 10000, "")
	if res := setup.waitPayoutTHORName(nil); !res.Continue {
		t.Fatal("ready thorname must continue")
	}
}

func TestBondYieldSelectTargetCov(t *testing.T) {
	f := newBondYieldFixtureCov(t)
	sel := &BondYieldSelectTargetActor{Actor: *NewActor("sel", rngCov()), state: f.state}
	cat, _, _ := newUserCov(t, "cat", "")
	cfg := &OpConfig{NodeUsers: []*User{cat, f.node}}

	if res := sel.selectActiveTargetWithYield(cfg); res.Continue {
		t.Fatal("nodes error keeps pending")
	}
	f.routes.set("/thorchain/nodes", `[{"node_address":"`+f.nodeAddr+`","status":"Active"}]`)
	// node query 404 -> skipped
	if res := sel.selectActiveTargetWithYield(cfg); res.Continue {
		t.Fatal("node query error keeps pending")
	}
	f.setNode("Active", "1", `{"bond_address":"`+f.nodeAddr+`"}`)
	if res := sel.selectActiveTargetWithYield(cfg); res.Continue {
		t.Fatal("award <= 1 keeps pending")
	}
	f.setNode("Active", "500", `{"bond_address":"other"}`)
	if res := sel.selectActiveTargetWithYield(cfg); res.Continue {
		t.Fatal("missing self-provider keeps pending")
	}
	f.setNode("Active", "500", `{"bond_address":"`+f.nodeAddr+`"}`)
	if res := sel.selectActiveTargetWithYield(cfg); !res.Continue || f.state.node != f.node || f.state.providerAddress.String() != f.nodeAddr {
		t.Fatalf("expected node selected, got %+v", res)
	}
}

func TestBondYieldTargetAutoClaimFlowCov(t *testing.T) {
	f := newBondYieldFixtureCov(t)
	f.prime(t)
	ac := &BondYieldTargetAutoClaimActor{Actor: *NewActor("ac", rngCov()), state: f.state}

	// already owned by target: no transfer
	f.state.payoutOwnerAddress = f.state.providerAddress
	if res := ac.transferPayoutTHORNameToTarget(nil); !res.Continue || len(f.ownerBr.sent())+len(f.nodeBridge.sent()) != 0 {
		t.Fatal("same owner must skip transfer")
	}

	// transfer from a previous owner (pig)
	f.state.payoutOwnerAddress = common.Address(f.ownerAddr)
	f.state.payoutOwnerNode = f.owner
	f.ownerBr.err = errCov
	if res := ac.transferPayoutTHORNameToTarget(nil); res.Continue {
		t.Fatal("transfer error keeps pending")
	}
	f.ownerBr.err = nil
	if res := ac.transferPayoutTHORNameToTarget(nil); !res.Continue {
		t.Fatal("transfer must succeed")
	}
	if memo := lastMemoCov(t, f.ownerBr); memo != fmt.Sprintf("~:simyieldpig:THOR:%s:%s::0:-1", f.ownerAddr, f.nodeAddr) {
		t.Fatalf("transfer memo %q", memo)
	}

	if res := ac.waitPayoutTHORNameOwnedByTarget(nil); res.Continue {
		t.Fatal("thorname error keeps pending")
	}
	if res := ac.waitTargetPayoutTHORName(nil); res.Continue {
		t.Fatal("thorname error keeps pending")
	}
	f.setTHORName(f.ownerAddr, "ETH", f.ethAddr, "ETH.ETH", 10000, "")
	if res := ac.waitPayoutTHORNameOwnedByTarget(nil); res.Continue {
		t.Fatal("not yet owned by target")
	}
	f.setTHORName(f.nodeAddr, "ETH", f.ethAddr, "ETH.ETH", 10000, "")
	if res := ac.waitPayoutTHORNameOwnedByTarget(nil); !res.Continue || f.state.payoutOwnerNode != f.node {
		t.Fatal("target ownership must be recorded")
	}

	f.nodeBridge.err = errCov
	if res := ac.configureTargetPayoutTHORName(nil); res.Continue {
		t.Fatal("config error keeps pending")
	}
	if res := ac.configureTargetAutoClaim(nil); res.Continue {
		t.Fatal("auto-claim error keeps pending")
	}
	f.nodeBridge.err = nil
	if res := ac.configureTargetPayoutTHORName(nil); !res.Continue {
		t.Fatal("config must succeed")
	}

	for _, tc := range []struct {
		owner, chain, pref string
		mult               int64
	}{
		{f.ownerAddr, "ETH", "ETH.ETH", 10000},
		{f.nodeAddr, "BTC", "ETH.ETH", 10000},
		{f.nodeAddr, "ETH", "BTC.BTC", 10000},
		{f.nodeAddr, "ETH", "ETH.ETH", 1},
	} {
		f.setTHORName(tc.owner, tc.chain, f.ethAddr, tc.pref, tc.mult, "")
		if res := ac.waitTargetPayoutTHORName(nil); res.Continue {
			t.Fatalf("expected pending for %+v", tc)
		}
	}
	f.setTHORName(f.nodeAddr, "ETH", f.ethAddr, "ETH.ETH", 10000, "1234")
	if res := ac.waitTargetPayoutTHORName(nil); !res.Continue || !f.state.collectorBeforeAutoClaim.Equal(cosmos.NewUint(1234)) {
		t.Fatalf("collector baseline must be recorded, got %s", f.state.collectorBeforeAutoClaim)
	}

	if res := ac.configureTargetAutoClaim(nil); !res.Continue {
		t.Fatal("auto-claim must succeed")
	}
	if memo := lastMemoCov(t, f.nodeBridge); memo != fmt.Sprintf("bondyield:%s:4000:simyieldpig", f.nodeAddr) {
		t.Fatalf("auto-claim memo %q", memo)
	}

	if res := ac.waitTargetAutoClaimConfigured(nil); res.Continue {
		t.Fatal("node error keeps pending")
	}
	f.setNode("Active", "", `{"bond_address":"other"}`)
	if res := ac.waitTargetAutoClaimConfigured(nil); res.Continue {
		t.Fatal("missing provider keeps pending")
	}
	f.setNode("Active", "", `{"bond_address":"`+f.nodeAddr+`","bps":4000,"payout_target":"other"}`)
	if res := ac.waitTargetAutoClaimConfigured(nil); res.Continue {
		t.Fatal("wrong payout target keeps pending")
	}
	f.setNode("Active", "", `{"bond_address":"`+f.nodeAddr+`","bps":4000,"payout_target":"simyieldpig"}`)
	if res := ac.waitTargetAutoClaimConfigured(nil); !res.Continue {
		t.Fatal("matching auto-claim must continue")
	}
}

func TestBondYieldChurnOutFlowCov(t *testing.T) {
	f := newBondYieldFixtureCov(t)
	f.prime(t)
	co := &BondYieldChurnOutActor{Actor: *NewActor("co", rngCov()), state: f.state}
	f.state.collectorBeforeAutoClaim = cosmos.NewUint(100)

	f.nodeBridge.err = errCov
	if res := co.requestTargetLeave(nil); res.Continue {
		t.Fatal("leave error keeps pending")
	}
	f.nodeBridge.err = nil
	if res := co.requestTargetLeave(nil); !res.Continue || lastMemoCov(t, f.nodeBridge) != "leave:"+f.nodeAddr {
		t.Fatal("leave must be broadcast")
	}

	// churn scheduling fails fast without nodes
	if res := co.scheduleTargetChurnOut(&OpConfig{}); res.Continue {
		t.Fatal("schedule error keeps pending")
	}

	steps := []struct {
		setup  func()
		finish bool
		cont   bool
	}{
		{func() {}, false, false}, // node query error
		{func() { f.setNode("Active", "", `{"bond_address":"other"}`) }, false, false},
		{func() { f.setNode("Active", "", `{"bond_address":"`+f.nodeAddr+`"}`) }, false, false},
		{func() { f.setNode("Standby", "", `{"bond_address":"`+f.nodeAddr+`"}`) }, false, false}, // network error
		{func() { f.routes.set("/thorchain/network", `{"vaults_migrating":true}`) }, false, false},
		{func() { f.routes.set("/thorchain/network", `{"vaults_migrating":false}`) }, false, false}, // thorname error
		{func() { f.setTHORName(f.ownerAddr, "ETH", f.ethAddr, "ETH.ETH", 1, "200") }, true, false},
		{func() { f.setTHORName(f.nodeAddr, "ETH", f.ethAddr, "ETH.ETH", 1, "100") }, false, false},
		{func() { f.setTHORName(f.nodeAddr, "ETH", f.ethAddr, "ETH.ETH", 1, "150") }, false, true},
	}
	for i, s := range steps {
		s.setup()
		res := co.waitTargetChurnedOut(nil)
		if res.Finish != s.finish || res.Continue != s.cont {
			t.Fatalf("step %d: got %+v", i, res)
		}
		if s.finish && (res.Error == nil || !strings.Contains(res.Error.Error(), "owner changed")) {
			t.Fatalf("step %d: expected owner-changed error, got %v", i, res.Error)
		}
	}
	if !f.state.collectorAfterAutoClaim.Equal(cosmos.NewUint(150)) {
		t.Fatalf("collector after auto-claim %s", f.state.collectorAfterAutoClaim)
	}
}

func TestBondYieldHelpersCov(t *testing.T) {
	f := newBondYieldFixtureCov(t)
	f.prime(t)

	f.node.Acquire()
	if _, err := broadcastLeave(f.node, f.state.nodeAddress); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("locked user must fail, got %v", err)
	}
	f.node.Release()

	_, memo, err := broadcastBondYieldAutoClaim(f.node, f.state.nodeAddress, 0, "")
	if err != nil || memo != "bondyield:"+f.nodeAddr+":0" {
		t.Fatalf("clearing memo %q %v", memo, err)
	}

	if !bondYieldUint("").IsZero() || !bondYieldUint("42").Equal(cosmos.NewUint(42)) {
		t.Fatal("bondYieldUint")
	}
	if v, ok := getMimirCaseInsensitive(map[string]int64{"FooBar": 3}, "FOOBAR"); !ok || v != 3 {
		t.Fatal("case-insensitive mimir lookup")
	}
	if _, ok := getMimirCaseInsensitive(map[string]int64{}, "x"); ok {
		t.Fatal("missing mimir")
	}
	tn := bondYieldTHORName{Aliases: []bondYieldAliasInfo{{Chain: "ETH", Address: "0xa"}}}
	if !hasAlias(tn, common.ETHChain, "0xa") || hasAlias(tn, common.ETHChain, "0xb") || hasAlias(tn, common.BTCChain, "0xa") {
		t.Fatal("hasAlias")
	}
}
