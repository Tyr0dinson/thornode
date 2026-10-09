package core

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const (
	covDETHVault  = "0x1111111111111111111111111111111111111111"
	covDETHVault2 = "0x2222222222222222222222222222222222222222"
	covDRouter    = "0x3333333333333333333333333333333333333333"
	covDRouter2   = "0x4444444444444444444444444444444444444444"
)

func covDTokenAsset(t *testing.T) common.Asset {
	t.Helper()
	for asset := range evm.Tokens(common.ETHChain) {
		return asset
	}
	t.Fatal("no simulation ETH token")
	return common.EmptyAsset
}

func covDETHVaultJSON(address string, coins ...map[string]string) []map[string]interface{} {
	return []map[string]interface{}{
		{"status": covDRetiringVlt, "addresses": []map[string]string{{"chain": "ETH", "address": address}}},
		{
			"status":    covDActiveVault,
			"addresses": []map[string]string{{"chain": "ETH", "address": address}},
			"coins":     coins,
		},
	}
}

func TestChurnBalanceConstructionCovD(t *testing.T) {
	a := NewChurnBalanceActor(covDRNG())
	if a.Name != "ChurnBalance" || len(a.Ops) != 4 || a.Timeout.Minutes() != 10 {
		t.Fatalf("unexpected actor %+v", a)
	}
}

func TestChurnBalanceCapturePreChurnBalanceCovD(t *testing.T) {
	api := newCovThornode(t)
	token := covDTokenAsset(t)
	evmClient, rpc := newCovEVMClient(t, common.ETHChain, 0)
	user, _ := newCovUser(t, 0, map[common.Chain]LiteChainClient{common.ETHChain: evmClient})
	cfg := &OpConfig{Users: []*User{user}}
	a := &ChurnBalanceActor{Actor: *NewActor("cb", covDRNG())}

	// inbound lookup fails
	if res := a.capturePreChurnBalance(cfg); res.Continue || res.Finish {
		t.Fatalf("inbound failure: %+v", res)
	}
	// no router
	api.JSON(covDPathInbound, []interface{}{covDInbound(common.ETHChain, covDETHVault, "", "")})
	if res := a.capturePreChurnBalance(cfg); !res.Finish || res.Error == nil || !strings.Contains(res.Error.Error(), "no router") {
		t.Fatalf("missing router: %+v", res)
	}
	api.JSON(covDPathInbound, []interface{}{covDInbound(common.ETHChain, covDETHVault, covDRouter, "")})
	// vault lookup fails
	if res := a.capturePreChurnBalance(cfg); res.Continue || res.Finish {
		t.Fatalf("vault failure: %+v", res)
	}
	// no matching active vault
	api.JSON(covDPathVaults, covDETHVaultJSON(covDETHVault2))
	if res := a.capturePreChurnBalance(cfg); res.Continue || res.Finish {
		t.Fatalf("no active vault: %+v", res)
	}

	coins := []map[string]string{
		{"asset": "!!!.BAD", "amount": "1"},
		{"asset": "BTC.BTC", "amount": "1"},
		{"asset": "ETH.ETH", "amount": "1"},
		{"asset": token.String(), "amount": "500000000"},
	}
	api.JSON(covDPathVaults, covDETHVaultJSON(covDETHVault, coins...))

	// no user available
	if !user.Acquire() {
		t.Fatal("acquire")
	}
	if res := a.capturePreChurnBalance(cfg); res.Continue || res.Finish {
		t.Fatalf("no user: %+v", res)
	}
	user.Release()

	// wrong client type
	fakeUser, _ := newCovUser(t, 1, map[common.Chain]LiteChainClient{common.ETHChain: newCovChainClient()})
	if res := a.capturePreChurnBalance(&OpConfig{Users: []*User{fakeUser}}); !res.Finish || res.Error == nil {
		t.Fatalf("non evm client must fail: %+v", res)
	}
	if fakeUser.IsLocked() {
		t.Fatal("user leaked")
	}

	// allowance read fails
	rpc.CallFn = func(string, []byte) (*big.Int, error) { return nil, errors.New("reverted") }
	if res := a.capturePreChurnBalance(cfg); res.Continue || res.Finish {
		t.Fatalf("allowance failure: %+v", res)
	}

	// zero allowance is vacuous
	rpc.CallFn = nil
	if res := a.capturePreChurnBalance(cfg); !res.Finish || res.Error == nil || !strings.Contains(res.Error.Error(), "vacuously") {
		t.Fatalf("zero allowances must fail: %+v", res)
	}

	// success: 5 tokens (18 decimals) -> 5e8
	var gotRouter string
	rpc.CallFn = func(to string, _ []byte) (*big.Int, error) {
		gotRouter = to
		return new(big.Int).Mul(big.NewInt(5), big.NewInt(1e18)), nil
	}
	if res := a.capturePreChurnBalance(cfg); !res.Continue || res.Finish {
		t.Fatalf("expected snapshot: %+v", res)
	}
	if gotRouter != strings.ToLower(covDRouter) {
		t.Fatalf("allowance read from %s, want router", gotRouter)
	}
	if len(a.priorCoins) != 1 || !a.priorCoins[token.String()].Equal(cosmos.NewUint(5*common.One)) {
		t.Fatalf("unexpected snapshot %v", a.priorCoins)
	}
	if a.priorAddr.String() != covDETHVault || a.priorRouter.String() != covDRouter {
		t.Fatalf("unexpected prior vault %s / router %s", a.priorAddr, a.priorRouter)
	}
	if user.IsLocked() {
		t.Fatal("user leaked")
	}
}

func TestChurnBalanceTriggerChurnCovD(t *testing.T) {
	api := newCovThornode(t)
	dog, bridge := newCovUser(t, 12, nil)
	cfg := &OpConfig{NodeUsers: []*User{dog}}
	a := &ChurnBalanceActor{Actor: *NewActor("cb", covDRNG())}

	if !dog.Acquire() {
		t.Fatal("acquire")
	}
	if res := a.triggerChurn(cfg); res.Continue || res.Finish {
		t.Fatalf("locked node: %+v", res)
	}
	dog.Release()

	if res := a.triggerChurn(cfg); res.Continue || res.Finish {
		t.Fatalf("schedule failure: %+v", res)
	}

	api.JSON(covDPathNodes, covDNodes(t, []*User{dog}, covDActive))
	api.JSON(covDPathVaults, []map[string]interface{}{{"status": covDActiveVault, "status_since": 95}})
	api.JSON(covDPathMimir, map[string]int64{"CHURNINTERVAL": 13})
	bridge.BroadcastFn = covDFailAfter(1)
	if res := a.triggerChurn(cfg); res.Continue || res.Finish {
		t.Fatalf("halt churning broadcast failure: %+v", res)
	}

	bridge.BroadcastFn = nil
	bridge.Msgs = nil
	if res := a.triggerChurn(cfg); !res.Continue {
		t.Fatalf("expected churn trigger: %+v", res)
	}
	got := covDMimirMsgs(t, bridge)
	if got["CHURNINTERVAL"] != 13 || got["HALTCHURNING"] != 0 || len(got) != 2 {
		t.Fatalf("unexpected mimirs %v", got)
	}
	if dog.IsLocked() {
		t.Fatal("node leaked")
	}
}

func TestChurnBalanceWaitForMigrationCovD(t *testing.T) {
	api := newCovThornode(t)
	a := &ChurnBalanceActor{Actor: *NewActor("cb", covDRNG()), priorAddr: covDETHVault}

	if res := a.waitForMigration(nil); res.Continue || res.Finish {
		t.Fatalf("inbound failure: %+v", res)
	}
	api.JSON(covDPathInbound, []interface{}{covDInbound(common.ETHChain, covDETHVault, covDRouter, "")})
	if res := a.waitForMigration(nil); res.Continue || res.Finish {
		t.Fatalf("not rotated: %+v", res)
	}
	api.JSON(covDPathInbound, []interface{}{covDInbound(common.ETHChain, covDETHVault2, covDRouter, "")})
	if res := a.waitForMigration(nil); res.Continue || res.Finish {
		t.Fatalf("network failure: %+v", res)
	}
	api.JSON(covDPathNetwork, map[string]interface{}{"vaults_migrating": true})
	if res := a.waitForMigration(nil); res.Continue || res.Finish {
		t.Fatalf("migrating: %+v", res)
	}
	api.JSON(covDPathNetwork, map[string]interface{}{"vaults_migrating": false})
	if res := a.waitForMigration(nil); !res.Continue {
		t.Fatalf("expected migration complete: %+v", res)
	}
}

func TestChurnBalanceVerifyBalanceCovD(t *testing.T) {
	api := newCovThornode(t)
	token := covDTokenAsset(t)
	evmClient, rpc := newCovEVMClient(t, common.ETHChain, 0)
	user, _ := newCovUser(t, 0, map[common.Chain]LiteChainClient{common.ETHChain: evmClient})
	cfg := &OpConfig{Users: []*User{user}}
	a := &ChurnBalanceActor{
		Actor:      *NewActor("cb", covDRNG()),
		priorCoins: map[string]cosmos.Uint{token.String(): cosmos.NewUint(100 * common.One)},
	}

	if res := a.verifyBalance(cfg); res.Continue || res.Finish {
		t.Fatalf("inbound failure: %+v", res)
	}
	api.JSON(covDPathInbound, []interface{}{covDInbound(common.ETHChain, covDETHVault2, "", "")})
	if res := a.verifyBalance(cfg); !res.Finish || res.Error == nil {
		t.Fatalf("missing router: %+v", res)
	}
	api.JSON(covDPathInbound, []interface{}{covDInbound(common.ETHChain, covDETHVault2, covDRouter2, "")})

	if !user.Acquire() {
		t.Fatal("acquire")
	}
	if res := a.verifyBalance(cfg); res.Continue || res.Finish {
		t.Fatalf("no user: %+v", res)
	}
	user.Release()

	fakeUser, _ := newCovUser(t, 1, map[common.Chain]LiteChainClient{common.ETHChain: newCovChainClient()})
	if res := a.verifyBalance(&OpConfig{Users: []*User{fakeUser}}); !res.Finish || res.Error == nil {
		t.Fatalf("non evm client: %+v", res)
	}

	rpc.CallFn = func(string, []byte) (*big.Int, error) { return nil, errors.New("reverted") }
	if res := a.verifyBalance(cfg); res.Continue || res.Finish {
		t.Fatalf("allowance failure: %+v", res)
	}

	// 98 tokens < 99% threshold -> hard failure
	rpc.CallFn = func(string, []byte) (*big.Int, error) {
		return new(big.Int).Mul(big.NewInt(98), big.NewInt(1e18)), nil
	}
	if res := a.verifyBalance(cfg); !res.Finish || res.Error == nil {
		t.Fatalf("shortfall must fail: %+v", res)
	}

	// 99 tokens == threshold -> success
	rpc.CallFn = func(string, []byte) (*big.Int, error) {
		return new(big.Int).Mul(big.NewInt(99), big.NewInt(1e18)), nil
	}
	if res := a.verifyBalance(cfg); !res.Continue || res.Finish {
		t.Fatalf("threshold must pass: %+v", res)
	}

	// unparsable snapshot key fails verification
	a.priorCoins["!!!.BAD"] = cosmos.NewUint(1)
	if res := a.verifyBalance(cfg); !res.Finish || res.Error == nil {
		t.Fatalf("bad asset must fail: %+v", res)
	}
	if user.IsLocked() {
		t.Fatal("user leaked")
	}
}
