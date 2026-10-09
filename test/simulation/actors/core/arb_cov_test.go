package core

import (
	"errors"
	rand "math/rand/v2"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func covCPool(asset, rune, assetBal, status string) map[string]string {
	return map[string]string{"asset": asset, "balance_rune": rune, "balance_asset": assetBal, "status": status}
}

func newArbActorCovC() *ArbActor {
	return &ArbActor{
		Actor:         *simtypes.NewActor("arb", rand.New(rand.NewPCG(1, 2))),
		originalPools: map[string]stypes.Pool{},
	}
}

func TestNewArbActorCovC(t *testing.T) {
	actor := NewArbActor(rand.New(rand.NewPCG(1, 2)))
	if actor.Name != "Arbitrage" || len(actor.Ops) != 5 || actor.Timeout != time.Hour || actor.Interval != 5*time.Second {
		t.Fatalf("unexpected arb actor %+v", actor)
	}
}

func TestArbInitRecordsOriginalPoolsCovC(t *testing.T) {
	api := newCovThornode(t)
	a := newArbActorCovC()
	api.Status("/thorchain/pools", 400, "bad")
	if res := a.init(nil); res.Continue || res.Error == nil {
		t.Fatalf("pool failure must retry with error: %+v", res)
	}
	api.JSON("/thorchain/pools", []map[string]string{covCPool("BTC.BTC", "1000", "10", "Available")})
	if res := a.init(nil); !res.Continue {
		t.Fatalf("init failed: %+v", res)
	}
	pool := a.originalPools["BTC.BTC"]
	if !pool.BalanceRune.Equal(cosmos.NewUint(1000)) || !pool.BalanceAsset.Equal(cosmos.NewUint(10)) {
		t.Fatalf("unexpected original pool %+v", pool)
	}
}

func TestArbAcquireUserCovC(t *testing.T) {
	busy, _ := newCovUser(t, 0, nil)
	free, _ := newCovUser(t, 1, nil)
	a := newArbActorCovC()
	if !busy.Acquire() {
		t.Fatal("acquire")
	}
	if res := a.acquireUser(&simtypes.OpConfig{Users: []*simtypes.User{busy}}); res.Continue {
		t.Fatal("all users busy must retry")
	}
	if res := a.acquireUser(&simtypes.OpConfig{Users: []*simtypes.User{busy, free}}); !res.Continue || a.account != free {
		t.Fatalf("free user must be acquired: %+v", res)
	}
	want, err := free.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		t.Fatal(err)
	}
	if !a.thorAddress.Equals(want) || !free.IsLocked() {
		t.Fatal("thor address not captured or user not locked")
	}
}

func TestArbEnableTradeAssetsCovC(t *testing.T) {
	node, bridge := newCovUser(t, 12, nil)
	a := newArbActorCovC()
	cfg := &simtypes.OpConfig{NodeUsers: []*simtypes.User{node}}
	if !node.Acquire() {
		t.Fatal("acquire")
	}
	if res := a.enableTradeAssets(cfg); res.Continue {
		t.Fatal("busy node must retry")
	}
	node.Release()
	if res := a.enableTradeAssets(cfg); !res.Continue {
		t.Fatalf("mimir broadcast failed: %+v", res)
	}
	msg := bridge.Msgs[0].(*stypes.MsgMimir)
	if msg.Key != "TradeAccountsEnabled" || msg.Value != 1 {
		t.Fatalf("unexpected mimir %+v", msg)
	}
	bridge.BroadcastFn = func(...sdk.Msg) (common.TxID, error) { return "", errors.New("down") }
	if res := a.enableTradeAssets(cfg); res.Continue {
		t.Fatal("broadcast failure must retry")
	}
	if node.IsLocked() {
		t.Fatal("node must be released")
	}
}

func TestArbBootstrapTradeAssetsCovC(t *testing.T) {
	api := newCovThornode(t)
	btc := newCovChainClient(common.NewCoin(common.BTCAsset, cosmos.NewUint(1000)))
	doge := newCovChainClient(common.NewCoin(common.DOGEAsset, cosmos.NewUint(50)))
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.BTCChain: btc, common.DOGEChain: doge})
	a := newArbActorCovC()
	a.account = user
	thor, err := user.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		t.Fatal(err)
	}
	a.thorAddress = thor
	if !user.Acquire() {
		t.Fatal("acquire")
	}

	api.Status("/thorchain/pools", 400, "bad")
	if res := a.bootstrapTradeAssets(nil); res.Continue || res.Error == nil {
		t.Fatalf("pool failure must retry with error: %+v", res)
	}

	api.JSON("/thorchain/pools", []map[string]string{
		covCPool("BTC.BTC", "1000", "10", "Available"),
		covCPool("DOGE.DOGE", "1000", "10", "Available"),
	})
	api.JSON("/thorchain/inbound_addresses", covCInboundAddresses)
	if res := a.bootstrapTradeAssets(nil); !res.Continue {
		t.Fatalf("bootstrap failed: %+v", res)
	}
	if !a.Backgrounded() || user.IsLocked() {
		t.Fatal("bootstrap must background the actor and release the user")
	}
	for name, tc := range map[string]struct {
		client *covChainClient
		to     common.Address
		amount uint64
	}{"BTC": {btc, "bc1qvault", 900}, "DOGE": {doge, "DVault", 45}} {
		if len(tc.client.Signed) != 1 {
			t.Fatalf("%s: expected one deposit, got %d", name, len(tc.client.Signed))
		}
		tx := tc.client.Signed[0]
		if tx.ToAddress != tc.to || tx.Memo != "trade+:"+thor.String() || tx.Coin.Amount.Uint64() != tc.amount {
			t.Fatalf("%s: unexpected deposit %+v", name, tx)
		}
	}
}

func TestArbArbCovC(t *testing.T) {
	api := newCovThornode(t)
	user, bridge := newCovUser(t, 0, nil)
	a := newArbActorCovC()
	a.account = user
	thor, err := user.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		t.Fatal(err)
	}
	a.thorAddress = thor
	a.originalPools["BTC.BTC"] = stypes.Pool{BalanceRune: cosmos.NewUint(100000), BalanceAsset: cosmos.NewUint(1000)}
	a.originalPools["ETH.ETH"] = stypes.Pool{BalanceRune: cosmos.NewUint(100000), BalanceAsset: cosmos.NewUint(1000)}

	if !user.Acquire() {
		t.Fatal("acquire")
	}
	if res := a.arb(nil); res.Continue || res.Finish {
		t.Fatal("busy user must retry")
	}
	user.Release()

	api.Status("/thorchain/pools", 400, "bad")
	if res := a.arb(nil); res.Continue || res.Finish || res.Error == nil {
		t.Fatalf("pool failure must retry with error: %+v", res)
	}

	api.JSON("/thorchain/pools", []map[string]string{})
	if res := a.arb(nil); !res.Finish || res.Error != nil {
		t.Fatalf("drained pools must finish: %+v", res)
	}

	api.JSON("/thorchain/pools", []map[string]string{
		covCPool("BTC.BTC", "100000", "1000", "Available"),
		covCPool("ETH.ETH", "0", "1000", "Available"),
		covCPool("DOGE.DOGE", "100", "10", "Staged"),
	})
	if res := a.arb(nil); res.Continue || res.Finish {
		t.Fatal("fewer than two arb pools must retry")
	}

	api.JSON("/thorchain/pools", []map[string]string{
		covCPool("BTC.BTC", "100000", "1000", "Available"),
		covCPool("ETH.ETH", "100500", "1000", "Available"),
	})
	if res := a.arb(nil); res.Continue || res.Finish || bridge.MsgCount() != 0 {
		t.Fatalf("divergence under 1%% must not trade: %+v", res)
	}

	// ETH price is up 5%: sell ETH trade asset for BTC
	api.JSON("/thorchain/pools", []map[string]string{
		covCPool("BTC.BTC", "100000", "1000", "Available"),
		covCPool("ETH.ETH", "105000", "1000", "Available"),
	})
	if res := a.arb(nil); res.Continue || res.Finish {
		t.Fatalf("arb op keeps running: %+v", res)
	}
	if bridge.MsgCount() != 1 {
		t.Fatalf("expected one arb deposit, got %d", bridge.MsgCount())
	}
	msg := bridge.Msgs[0].(*stypes.MsgDeposit)
	if msg.Memo != "=:BTC~BTC" || msg.Coins[0].Asset.String() != "ETH~ETH" {
		t.Fatalf("unexpected arb %s %s", msg.Memo, msg.Coins)
	}
	// runeValue = 50 * 100000 / 2 / 10000 = 250; asset = 250 * 1000 / 105000 = 2
	if !msg.Coins[0].Amount.Equal(cosmos.NewUint(2)) {
		t.Fatalf("unexpected arb amount %s", msg.Coins[0].Amount)
	}

	bridge.BroadcastFn = func(...sdk.Msg) (common.TxID, error) { return "", errors.New("down") }
	if res := a.arb(nil); res.Continue || res.Finish {
		t.Fatalf("broadcast failure must retry: %+v", res)
	}
	if user.IsLocked() {
		t.Fatal("arb must release the user after each iteration")
	}
}
