package core

import (
	"errors"
	rand "math/rand/v2"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestMimirActorBroadcastsMimirFromFirstNodeCov(t *testing.T) {
	node, bridge := newCovUser(t, 12, nil)
	actor := NewMimirActor("HaltETHChain", 1, rand.New(rand.NewPCG(1, 2)))
	if actor.Name != "Mimir HaltETHChain=1" {
		t.Fatalf("unexpected actor name %q", actor.Name)
	}
	actor.InitRoot()
	if err := actor.Execute(&simtypes.OpConfig{NodeUsers: []*simtypes.User{node}}); err != nil {
		t.Fatalf("mimir actor failed: %v", err)
	}
	if len(bridge.Msgs) != 1 {
		t.Fatalf("expected one mimir msg, got %d", len(bridge.Msgs))
	}
	msg, ok := bridge.Msgs[0].(*stypes.MsgMimir)
	if !ok || msg.Key != "HaltETHChain" || msg.Value != 1 {
		t.Fatalf("unexpected msg %#v", bridge.Msgs[0])
	}
	addr, err := node.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		t.Fatal(err)
	}
	if !msg.Signer.Equals(addr) {
		t.Fatalf("mimir signed by %s, want %s", msg.Signer, addr)
	}
	if node.IsLocked() {
		t.Fatal("node lock not released")
	}
}

func TestMimirActorRetriesWhenLockedOrBroadcastFailsCov(t *testing.T) {
	node, bridge := newCovUser(t, 12, nil)
	a := &MimirActor{Actor: *simtypes.NewActor("m", rand.New(rand.NewPCG(1, 2))), key: "K", value: 2}
	cfg := &simtypes.OpConfig{NodeUsers: []*simtypes.User{node}}

	if !node.Acquire() {
		t.Fatal("acquire")
	}
	if res := a.sendMimir(cfg); res.Continue || res.Finish {
		t.Fatalf("locked node must retry: %+v", res)
	}
	if bridge.MsgCount() != 0 {
		t.Fatal("broadcast while node locked")
	}
	node.Release()

	bridge.BroadcastFn = func(...sdk.Msg) (common.TxID, error) { return "", errors.New("boom") }
	if res := a.sendMimir(cfg); res.Continue || res.Finish {
		t.Fatalf("broadcast failure must retry: %+v", res)
	}
	if node.IsLocked() {
		t.Fatal("node lock leaked after broadcast failure")
	}
}

func TestVerifyLPActorCov(t *testing.T) {
	api := newCovThornode(t)
	path := "/thorchain/pool/BTC.BTC/liquidity_providers"
	a := &VerifyLPActor{Actor: *simtypes.NewActor("v", rand.New(rand.NewPCG(1, 2))), asset: common.BTCAsset}
	if NewVerifyLPActor(common.BTCAsset, rand.New(rand.NewPCG(1, 2))).Name != "VerifyLP-BTC.BTC" {
		t.Fatal("unexpected name")
	}

	api.Status(path, 400, "bad")
	if res := a.verifyLP(nil); res.Continue || res.Finish {
		t.Fatalf("query failure must retry: %+v", res)
	}
	api.JSON(path, []openapi.LiquidityProvider{
		{PendingAsset: "5", PendingRune: "0", Units: "10"},
		{PendingAsset: "0", PendingRune: "0", Units: "0"},
	})
	if res := a.verifyLP(nil); res.Continue || res.Finish {
		t.Fatalf("pending/zero-unit LPs must not finish: %+v", res)
	}
	api.JSON(path, []openapi.LiquidityProvider{{PendingAsset: "0", PendingRune: "0", Units: "10"}})
	if res := a.verifyLP(nil); !res.Finish || res.Error != nil {
		t.Fatalf("completed LP must finish: %+v", res)
	}
	if api.Hits(path) != 3 {
		t.Fatalf("expected 3 LP queries, got %d", api.Hits(path))
	}
}
