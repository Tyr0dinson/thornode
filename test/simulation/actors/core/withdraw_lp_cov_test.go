package core

import (
	"errors"
	rand "math/rand/v2"
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func covCLP(user *simtypes.User, asset common.Asset, units, pendingAsset, pendingRune string) openapi.LiquidityProvider {
	thor := user.Address(common.THORChain).String()
	l1 := user.Address(asset.Chain).String()
	return openapi.LiquidityProvider{
		Asset:        asset.String(),
		RuneAddress:  &thor,
		AssetAddress: &l1,
		Units:        units,
		PendingAsset: pendingAsset,
		PendingRune:  pendingRune,
	}
}

func newWithdrawLPActorCovC() *WithdrawLPActor {
	return &WithdrawLPActor{
		Actor: *simtypes.NewActor("WithdrawLP", rand.New(rand.NewPCG(1, 2))),
		asset: common.BTCAsset,
	}
}

func TestNewWithdrawLPActorCovC(t *testing.T) {
	actor := NewWithdrawLPActor(common.BTCAsset, rand.New(rand.NewPCG(1, 2)))
	if actor.Name != "WithdrawLP-BTC.BTC" || len(actor.Ops) != 3 {
		t.Fatalf("unexpected actor: %s with %d ops", actor.Name, len(actor.Ops))
	}
}

func TestWithdrawLPAcquireUserCovC(t *testing.T) {
	api := newCovThornode(t)
	lpPath := "/thorchain/pool/BTC.BTC/liquidity_providers"
	locked, _ := newCovUser(t, 0, nil)
	pending, _ := newCovUser(t, 1, nil)
	holder, _ := newCovUser(t, 2, nil)
	if !locked.Acquire() {
		t.Fatal("acquire")
	}
	cfg := &simtypes.OpConfig{Users: []*simtypes.User{locked, pending, holder}}

	a := newWithdrawLPActorCovC()
	api.Status(lpPath, 400, "nope")
	if res := a.acquireUser(cfg); res.Continue || res.Finish {
		t.Fatalf("LP query failure must retry: %+v", res)
	}

	// no completed position for anyone yet
	api.JSON(lpPath, []openapi.LiquidityProvider{
		covCLP(locked, common.BTCAsset, "500", "0", "0"),
		covCLP(pending, common.BTCAsset, "100", "7", "0"),
	})
	if res := a.acquireUser(cfg); res.Continue || res.Finish {
		t.Fatalf("no completed LP must retry: %+v", res)
	}
	if pending.IsLocked() || holder.IsLocked() {
		t.Fatal("non-holders must be released")
	}

	api.JSON(lpPath, []openapi.LiquidityProvider{
		covCLP(locked, common.BTCAsset, "500", "0", "0"),
		covCLP(pending, common.BTCAsset, "100", "7", "0"),
		covCLP(holder, common.BTCAsset, "300", "0", "0"),
	})
	if res := a.acquireUser(cfg); !res.Continue {
		t.Fatalf("holder must be acquired: %+v", res)
	}
	if a.account != holder || !a.unitsBefore.Equal(cosmos.NewUint(300)) {
		t.Fatalf("acquired wrong holder %v units=%s", a.account.Name(), a.unitsBefore)
	}
	if !a.thorAddress.Equals(holder.Address(common.THORChain)) || !a.assetAddress.Equals(holder.Address(common.BTCChain)) {
		t.Fatal("addresses not captured")
	}
	if !holder.IsLocked() || pending.IsLocked() {
		t.Fatal("only the holder should stay locked")
	}
}

func TestWithdrawLPSendWithdrawCovC(t *testing.T) {
	user, bridge := newCovUser(t, 0, nil)
	a := newWithdrawLPActorCovC()
	a.account = user
	if !user.Acquire() {
		t.Fatal("acquire")
	}
	if res := a.sendWithdraw(nil); !res.Continue || res.Error != nil {
		t.Fatalf("withdraw broadcast failed: %+v", res)
	}
	msg, ok := bridge.Msgs[0].(*stypes.MsgDeposit)
	if !ok || msg.Memo != "-:BTC.BTC:5000:BTC.BTC" {
		t.Fatalf("unexpected withdraw msg %#v", bridge.Msgs[0])
	}
	if !msg.Coins[0].Amount.Equal(cosmos.NewUint(withdrawRuneDepositAmt)) || !msg.Coins[0].Asset.IsRune() {
		t.Fatalf("unexpected deposit coins %s", msg.Coins)
	}
	if a.withdrawTxID == "" || !user.IsLocked() {
		t.Fatal("txid must be recorded and the user kept locked")
	}

	bridge.BroadcastFn = func(...sdk.Msg) (common.TxID, error) { return "", errors.New("rejected") }
	res := a.sendWithdraw(nil)
	if !res.Finish || res.Error == nil || res.Error.Error() != "rejected" {
		t.Fatalf("broadcast failure must finish with error: %+v", res)
	}
	if user.IsLocked() {
		t.Fatal("user must be released after broadcast failure")
	}
}

func TestWithdrawLPVerifyWithdrawCovC(t *testing.T) {
	api := newCovThornode(t)
	user, _ := newCovUser(t, 0, nil)
	a := newWithdrawLPActorCovC()
	a.account = user
	a.thorAddress = user.Address(common.THORChain)
	a.assetAddress = user.Address(common.BTCChain)
	a.unitsBefore = cosmos.NewUint(300)
	a.withdrawTxID = "WTX"
	stagesPath := "/thorchain/tx/stages/WTX"
	detailsPath := "/thorchain/tx/details/WTX"
	lpPath := "/thorchain/pool/BTC.BTC/liquidity_providers"
	retry := func(step string) {
		t.Helper()
		if res := a.verifyWithdraw(nil); res.Continue || res.Finish {
			t.Fatalf("%s: expected retry, got %+v", step, res)
		}
	}

	api.Status(stagesPath, 404, "missing")
	retry("stages failure")
	api.JSON(stagesPath, `{"inbound_observed":{"completed":true,"final_count":1}}`)
	retry("outbound not signed")
	api.JSON(stagesPath, `{"inbound_observed":{"completed":true,"final_count":1},"outbound_signed":{"completed":true}}`)
	api.Status(detailsPath, 404, "missing")
	retry("details failure")

	to := a.assetAddress.String()
	api.JSON(detailsPath, openapi.TxDetailsResponse{OutTxs: []openapi.Tx{{ToAddress: &to}}})
	api.Status(lpPath, 400, "bad")
	retry("lp refresh failure")

	api.JSON(lpPath, []openapi.LiquidityProvider{covCLP(user, common.BTCAsset, "300", "0", "0")})
	retry("units unchanged")
	if !user.Acquire() {
		t.Fatal("acquire")
	}
	api.JSON(lpPath, []openapi.LiquidityProvider{covCLP(user, common.BTCAsset, "150", "0", "0")})
	if res := a.verifyWithdraw(nil); !res.Finish || res.Error != nil {
		t.Fatalf("units decreased must finish: %+v", res)
	}
	if user.IsLocked() {
		t.Fatal("user must be released on success")
	}

	// a fully withdrawn position (no LP record) also counts as decreased
	if !user.Acquire() {
		t.Fatal("acquire")
	}
	api.JSON(lpPath, []openapi.LiquidityProvider{})
	if res := a.verifyWithdraw(nil); !res.Finish || res.Error != nil {
		t.Fatalf("removed LP must finish: %+v", res)
	}

	// outbound to a different address is a terminal failure
	if !user.Acquire() {
		t.Fatal("acquire")
	}
	other := "bc1qother"
	api.JSON(detailsPath, openapi.TxDetailsResponse{OutTxs: []openapi.Tx{{}, {ToAddress: &other}}})
	res := a.verifyWithdraw(nil)
	if !res.Finish || res.Error == nil || !strings.Contains(res.Error.Error(), "expected XMR outbound") {
		t.Fatalf("missing outbound must fail: %+v", res)
	}
	if user.IsLocked() {
		t.Fatal("user must be released on failure")
	}
}
