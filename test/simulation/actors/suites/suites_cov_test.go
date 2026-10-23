package suites

import (
	rand "math/rand/v2"
	"net/http"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// routesCov serves fixed JSON bodies by path, 404 otherwise.
func routesCov(routes map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}
}

func findActorCov(t *testing.T, root *Actor, name string) *Actor {
	t.Helper()
	var found *Actor
	root.WalkDepthFirst(func(a *Actor) bool {
		if a.Name == name {
			found = a
		}
		return true
	})
	if found == nil {
		t.Fatalf("actor %q not found", name)
	}
	return found
}

func xmrEnabledCov(t *testing.T, enabled bool) {
	t.Helper()
	prev := acommon.XMRChainEnabled()
	acommon.SetXMRChainEnabled(enabled)
	t.Cleanup(func() { acommon.SetXMRChainEnabled(prev) })
}

func poolsJSONCov(n int, status string) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = `{"asset":"BTC.BTC","status":"` + status + `"}`
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func TestBootstrapBuildsPoolActorsAndVerifiesCov(t *testing.T) {
	xmrEnabledCov(t, false)
	setThornodeCov(t, routesCov(map[string]string{"/thorchain/pools": `[]`}))

	rng := rand.New(rand.NewPCG(1, 1))
	a := Bootstrap(rng)
	if a.Name != "Bootstrap" || len(a.Children) != len(acommon.SimChains) {
		t.Fatalf("expected one dual LP actor per chain, got %d children", len(a.Children))
	}
	tokenPools := findActorCov(t, a, "Bootstrap-TokenPools")
	tokens := 0
	evmChains := 0
	for _, chain := range acommon.SimChains {
		if chain.IsEVM() && !chain.Equals(common.BSCChain) {
			evmChains++
			tokens += len(evm.Tokens(chain))
		}
	}
	if len(tokenPools.Children) != tokens {
		t.Fatalf("expected %d token pool actors, got %d", tokens, len(tokenPools.Children))
	}
	verify := findActorCov(t, a, "Bootstrap-Verify")
	if len(verify.Ops) != 1 {
		t.Fatalf("expected one verify op, got %d", len(verify.Ops))
	}
	want := len(acommon.SimChains) + evmChains

	// wrong pool count
	setThornodeCov(t, routesCov(map[string]string{"/thorchain/pools": poolsJSONCov(want-1, "Available")}))
	res := verify.Ops[0](nil)
	if !res.Finish || res.Error == nil || !strings.Contains(res.Error.Error(), "expected") {
		t.Fatalf("expected count mismatch error, got %+v", res)
	}

	// a staged pool fails verification
	setThornodeCov(t, routesCov(map[string]string{"/thorchain/pools": poolsJSONCov(want, "Staged")}))
	res = verify.Ops[0](nil)
	if !res.Finish || res.Error == nil || !strings.Contains(res.Error.Error(), "not available") {
		t.Fatalf("expected not available error, got %+v", res)
	}

	// all good
	setThornodeCov(t, routesCov(map[string]string{"/thorchain/pools": poolsJSONCov(want, "Available")}))
	res = verify.Ops[0](nil)
	if !res.Finish || res.Error != nil {
		t.Fatalf("expected success, got %+v", res)
	}

	// query failure
	setThornodeCov(t, routesCov(nil))
	res = verify.Ops[0](nil)
	if !res.Finish || res.Error == nil {
		t.Fatalf("expected query error, got %+v", res)
	}

	// without XMR the initial inbound wait is a no-op
	if res = a.Ops[0](nil); !res.Continue {
		t.Fatalf("expected continue without XMR, got %+v", res)
	}
}

func TestWaitForInitialXMRInboundCov(t *testing.T) {
	xmrEnabledCov(t, true)

	// query fails: keep waiting
	setThornodeCov(t, routesCov(nil))
	if res := waitForInitialXMRInbound(nil); res.Continue || res.Finish || res.Error != nil {
		t.Fatalf("expected wait on error, got %+v", res)
	}

	// XMR missing from inbound list: keep waiting
	setThornodeCov(t, routesCov(map[string]string{
		"/thorchain/inbound_addresses": `[{"chain":"BTC","address":"btc"}]`,
	}))
	if res := waitForInitialXMRInbound(nil); res.Continue {
		t.Fatalf("expected wait without XMR inbound, got %+v", res)
	}

	// XMR present: continue
	setThornodeCov(t, routesCov(map[string]string{
		"/thorchain/inbound_addresses": `[{"chain":"XMR","address":"xmr-addr"}]`,
	}))
	if res := waitForInitialXMRInbound(nil); !res.Continue {
		t.Fatalf("expected continue with XMR inbound, got %+v", res)
	}
}

func TestRagnarokSuiteCov(t *testing.T) {
	xmrEnabledCov(t, false)
	rng := rand.New(rand.NewPCG(2, 2))
	a := Ragnarok(rng)
	if len(a.Children) != 1 {
		t.Fatalf("expected the mimir interval actor, got %d children", len(a.Children))
	}
	var interval *Actor
	for c := range a.Children {
		interval = c
	}
	if len(interval.Children) != len(acommon.SimChains) {
		t.Fatalf("expected one ragnarok actor per chain, got %d", len(interval.Children))
	}
	waitOp := interval.Ops[len(interval.Ops)-1]

	setThornodeCov(t, routesCov(nil))
	if res := waitOp(nil); res.Continue || res.Error == nil {
		t.Fatalf("expected mimir error, got %+v", res)
	}
	setThornodeCov(t, routesCov(map[string]string{"/thorchain/mimir": `{"FUNDMIGRATIONINTERVAL":30}`}))
	if res := waitOp(nil); res.Continue || res.Error != nil {
		t.Fatalf("expected wait for mimir, got %+v", res)
	}
	setThornodeCov(t, routesCov(map[string]string{"/thorchain/mimir": `{"FUNDMIGRATIONINTERVAL":5}`}))
	if res := waitOp(nil); !res.Continue {
		t.Fatalf("expected continue once mimir set, got %+v", res)
	}

	verify := findActorCov(t, a, "Ragnarok-Verify")
	setThornodeCov(t, routesCov(map[string]string{"/thorchain/pools": `[{"asset":"BTC.BTC"}]`}))
	if res := verify.Ops[0](nil); !res.Finish || res.Error == nil || !strings.Contains(res.Error.Error(), "found 1 pools") {
		t.Fatalf("expected leftover pool error, got %+v", res)
	}
	setThornodeCov(t, routesCov(map[string]string{"/thorchain/pools": `[]`}))
	if res := verify.Ops[0](nil); !res.Finish || res.Error != nil {
		t.Fatalf("expected success, got %+v", res)
	}
	setThornodeCov(t, routesCov(nil))
	if res := verify.Ops[0](nil); !res.Finish || res.Error == nil {
		t.Fatalf("expected query error, got %+v", res)
	}
}

func TestSwapsSuitesCov(t *testing.T) {
	xmrEnabledCov(t, true)
	rng := rand.New(rand.NewPCG(3, 3))

	swaps := Swaps(rng)
	names := actorNames(swaps)
	for _, want := range []string{
		"Swap SOL.SOL => BTC.BTC",
		"Swap BTC.BTC => SOL.SOL",
		"Swap BTC.BTC => GAIA.ATOM",
		"Swap GAIA.ATOM => ETH.ETH",
		"Swap ETH.ETH => TRON.TRX",
		"Swap TRON.TRX => BTC.BTC",
	} {
		if !names[want] {
			t.Errorf("expected actor %q in swaps suite", want)
		}
	}
	for _, want := range []string{
		"AggSwap GAIA.ATOM => ETH.ETH",
		"AggSwap SOL.SOL => AVAX.AVAX",
		"AggSwapIn ETH.ETH => BTC.BTC",
		"AggSwapIn AVAX.AVAX => BTC.BTC",
	} {
		if !names[want] {
			t.Errorf("expected aggregator actor %q", want)
		}
	}

	memoless := MemolessSwaps(rng)
	mnames := actorNames(memoless)
	direct := findActorCov(t, memoless, "Direct ERC20 Inbound Flags")
	if len(direct.Children) == 0 {
		t.Fatal("expected direct ERC20 mimir actors")
	}
	for c := range direct.Children {
		if !strings.Contains(strings.ToUpper(c.Name), "DIRECT") {
			t.Errorf("unexpected direct flag actor %q", c.Name)
		}
	}
	memolessCount := 0
	for name := range mnames {
		if strings.HasPrefix(name, "SwapMemoless") {
			memolessCount++
		}
	}
	// two memoless types per gas asset at minimum
	if memolessCount < 2*len(acommon.SimChains) {
		t.Fatalf("expected at least %d memoless swap actors, got %d", 2*len(acommon.SimChains), memolessCount)
	}
}

func TestXMRSuitesDisabledCov(t *testing.T) {
	xmrEnabledCov(t, false)
	t.Setenv("XMR_POST_CHURN_REQUIRED", "")
	rng := rand.New(rand.NewPCG(4, 4))
	for _, a := range []*Actor{XMRCoverage(rng), XMRSpentRefTracking(rng), XMRPostChurn(rng)} {
		if len(a.Children) != 0 {
			t.Fatalf("%s must be empty when XMR is disabled", a.Name)
		}
	}
}

func TestXMRSuitesEnabledCov(t *testing.T) {
	xmrEnabledCov(t, true)
	rng := rand.New(rand.NewPCG(5, 5))
	if a := XMRSpentRefTracking(rng); len(a.Children) != 1 {
		t.Fatalf("expected spent ref verify actor, got %d", len(a.Children))
	}
	if a := XMRKeygenRecovery(rng); len(a.Children) != 1 {
		t.Fatalf("expected keygen recovery actor, got %d", len(a.Children))
	}
}

func TestClusterChurnWithXMRPostChurnCov(t *testing.T) {
	xmrEnabledCov(t, true)
	a := ClusterChurnWithXMRPostChurn(rand.New(rand.NewPCG(6, 6)))
	plain := ClusterChurn(rand.New(rand.NewPCG(6, 6)))
	withXMR, without := actorNames(a), actorNames(plain)
	if len(withXMR) <= len(without) {
		t.Fatalf("XMR post-churn checks must add actors: %d vs %d", len(withXMR), len(without))
	}
}

func TestChurnSuitesStructureCov(t *testing.T) {
	for _, tc := range []struct {
		suite  func(*rand.Rand) *Actor
		name   string
		verify string
	}{
		{Churn, "ChurnSuite", "Churn"},
		{ChurnBalance, "ChurnBalanceSuite", "ChurnBalance"},
	} {
		a := tc.suite(rand.New(rand.NewPCG(7, 7)))
		if a.Name != tc.name {
			t.Fatalf("unexpected suite name %s", a.Name)
		}
		a.InitRoot()
		restore := findActorCov(t, a, "ChurnInterval-Restore")
		if !restore.Parents()[findActorCov(t, a, tc.verify)] || len(restore.Parents()) != 1 {
			t.Fatalf("%s: restore must run only after %s", tc.name, tc.verify)
		}
		if len(restore.Children) != 1 {
			t.Fatalf("%s: expected one CHURNINTERVAL vote, got %d", tc.name, len(restore.Children))
		}
		for c := range restore.Children {
			if !strings.HasPrefix(c.Name, "Mimir(all) ChurnInterval=") {
				t.Fatalf("%s: unexpected restore child %q", tc.name, c.Name)
			}
		}
	}
}
