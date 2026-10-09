package core

import (
	"errors"
	rand "math/rand/v2"
	"strings"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const covCInboundAddresses = `[
	{"chain":"BTC","address":"bc1qvault","pub_key":"tthorpub1"},
	{"chain":"DOGE","address":"DVault"},
	{"chain":"ETH","address":"0x00000000000000000000000000000000000000aa","router":"0x00000000000000000000000000000000000000bb"}
]`

func covCBalances(rune uint64) string {
	return `{"balances":[{"denom":"rune","amount":"` + cosmos.NewUint(rune).String() + `"}]}`
}

func covCFirstToken(t *testing.T, chain common.Chain) common.Asset {
	t.Helper()
	for asset := range evm.Tokens(chain) {
		return asset
	}
	t.Fatalf("no %s tokens", chain)
	return common.EmptyAsset
}

func TestNewDualLPActorCovC(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	btc := NewDualLPActor(common.BTCAsset, rng)
	if btc.Name != "DualLP-BTC.BTC" || len(btc.Ops) != 4 || btc.Timeout != 2*time.Minute {
		t.Fatalf("unexpected BTC actor %s ops=%d timeout=%s", btc.Name, len(btc.Ops), btc.Timeout)
	}
	if !strings.Contains(btc.Ops[2].String(), "depositL1") || strings.Contains(btc.Ops[2].String(), "Token") {
		t.Fatalf("BTC must deposit gas asset, got %s", btc.Ops[2])
	}
	token := covCFirstToken(t, common.ETHChain)
	tok := NewDualLPActor(token, rng)
	if !strings.Contains(tok.Ops[2].String(), "depositL1Token") {
		t.Fatalf("ERC20 must deposit via router, got %s", tok.Ops[2])
	}
	if NewDualLPActor(common.XMRAsset, rng).Timeout != 8*time.Minute {
		t.Fatal("XMR dual LP timeout must be extended")
	}
}

func TestDualLPAcquireUserCovC(t *testing.T) {
	api := newCovThornode(t)
	lpPath := "/thorchain/pool/BTC.BTC/liquidity_providers"
	a := &DualLPActor{Actor: *simtypes.NewActor("dual", rand.New(rand.NewPCG(1, 2))), asset: common.BTCAsset}

	busy, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.BTCChain: newCovChainClient()})
	existing, _ := newCovUser(t, 1, map[common.Chain]simtypes.LiteChainClient{common.BTCChain: newCovChainClient()})
	noRune, _ := newCovUser(t, 2, map[common.Chain]simtypes.LiteChainClient{common.BTCChain: newCovChainClient()})
	balErr, _ := newCovUser(t, 3, map[common.Chain]simtypes.LiteChainClient{common.BTCChain: newCovChainClient()})
	funded, _ := newCovUser(t, 4, map[common.Chain]simtypes.LiteChainClient{
		common.BTCChain: newCovChainClient(common.NewCoin(common.BTCAsset, cosmos.NewUint(800))),
	})
	if !busy.Acquire() {
		t.Fatal("acquire")
	}
	cfg := &simtypes.OpConfig{Users: []*simtypes.User{busy, existing, noRune, balErr, funded}}

	api.Status(lpPath, 400, "bad")
	if res := a.acquireUser(cfg); res.Continue || res.Finish {
		t.Fatalf("LP failure must retry: %+v", res)
	}

	api.JSON(lpPath, []openapi.LiquidityProvider{covCLP(existing, common.BTCAsset, "1", "0", "0")})
	api.JSON("/cosmos/bank/v1beta1/balances/"+noRune.Address(common.THORChain).String(), covCBalances(0))
	api.Status("/cosmos/bank/v1beta1/balances/"+balErr.Address(common.THORChain).String(), 400, "bad")
	api.JSON("/cosmos/bank/v1beta1/balances/"+funded.Address(common.THORChain).String(), covCBalances(400))
	api.Status("/thorchain/block", 400, "no block")

	if res := a.acquireUser(cfg); !res.Continue {
		t.Fatalf("funded user must be acquired: %+v", res)
	}
	if a.account != funded || !a.runeAmount.Equal(cosmos.NewUint(100)) || !a.l1Amount.Equal(cosmos.NewUint(200)) {
		t.Fatalf("unexpected selection %s rune=%s l1=%s", a.account.Name(), a.runeAmount, a.l1Amount)
	}
	if a.minLastAddHeight != 0 {
		t.Fatal("block failure must leave min height unset")
	}
	assertReleased(t, existing, noRune, balErr)
	funded.Release()

	// the block height is captured as the minimum add height when available
	api.JSON("/thorchain/block", `{"header":{"height":77}}`)
	if res := a.acquireUser(cfg); !res.Continue || a.minLastAddHeight != 77 {
		t.Fatalf("expected min add height 77, got %d (%+v)", a.minLastAddHeight, res)
	}
	funded.Release()

	// nobody funded -> pending
	api.JSON("/cosmos/bank/v1beta1/balances/"+funded.Address(common.THORChain).String(), covCBalances(0))
	if res := a.acquireUser(cfg); res.Continue || res.Finish {
		t.Fatalf("no candidates must retry: %+v", res)
	}
	assertReleased(t, funded)
}

func newDualLPActorForUserCovC(t *testing.T, asset common.Asset, user *simtypes.User) *DualLPActor {
	t.Helper()
	a := &DualLPActor{Actor: *simtypes.NewActor("dual", rand.New(rand.NewPCG(1, 2))), asset: asset}
	a.account = user
	a.thorAddress = user.Address(common.THORChain)
	a.l1Address = user.Address(asset.Chain)
	a.runeAmount = cosmos.NewUint(100)
	a.l1Amount = cosmos.NewUint(200)
	return a
}

func TestDualLPDepositRuneCovC(t *testing.T) {
	user, bridge := newCovUser(t, 0, nil)
	a := newDualLPActorForUserCovC(t, common.BTCAsset, user)
	if res := a.depositRune(nil); !res.Continue {
		t.Fatalf("rune deposit failed: %+v", res)
	}
	msg := bridge.Msgs[0].(*stypes.MsgDeposit)
	if msg.Memo != "+:BTC.BTC:"+a.l1Address.String() || !msg.Coins[0].Amount.Equal(cosmos.NewUint(100)) {
		t.Fatalf("unexpected deposit %s %s", msg.Memo, msg.Coins)
	}
	bridge.BroadcastFn = func(...sdk.Msg) (common.TxID, error) { return "", errors.New("down") }
	if res := a.depositRune(nil); res.Continue || res.Finish {
		t.Fatalf("broadcast failure must retry: %+v", res)
	}
}

func TestDualLPDepositL1CovC(t *testing.T) {
	api := newCovThornode(t)
	client := newCovChainClient()
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.BTCChain: client})
	a := newDualLPActorForUserCovC(t, common.BTCAsset, user)

	api.Status("/thorchain/inbound_addresses", 400, "bad")
	if res := a.depositL1(nil); res.Continue || res.Finish || res.Error == nil {
		t.Fatalf("inbound failure must retry with error: %+v", res)
	}

	api.JSON("/thorchain/inbound_addresses", covCInboundAddresses)
	if res := a.depositL1(nil); !res.Continue || a.l1DepositAt.IsZero() {
		t.Fatalf("L1 deposit failed: %+v", res)
	}
	if len(client.Signed) != 1 {
		t.Fatalf("expected 1 signed tx, got %d", len(client.Signed))
	}
	tx := client.Signed[0]
	if tx.ToAddress != "bc1qvault" || tx.Memo != "+:BTC.BTC:"+a.thorAddress.String() || !tx.Coin.Amount.Equal(cosmos.NewUint(200)) {
		t.Fatalf("unexpected L1 tx %+v", tx)
	}

	client.BroadcastErr = simtypes.ErrSimTxUncertain
	if res := a.depositL1(nil); !res.Finish || !errors.Is(res.Error, simtypes.ErrSimTxUncertain) {
		t.Fatalf("uncertain broadcast must stop the actor: %+v", res)
	}
}

func TestDualLPDepositL1TokenCovC(t *testing.T) {
	api := newCovThornode(t)
	token := covCFirstToken(t, common.ETHChain)
	client, rpc := newCovEVMClient(t, common.ETHChain, 0)
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.ETHChain: client})
	a := newDualLPActorForUserCovC(t, token, user)

	api.Status("/thorchain/inbound_addresses", 400, "bad")
	if res := a.depositL1Token(nil); res.Continue || res.Finish {
		t.Fatalf("inbound failure must retry: %+v", res)
	}

	api.JSON("/thorchain/inbound_addresses", covCInboundAddresses)
	if res := a.depositL1Token(nil); !res.Continue || a.l1DepositAt.IsZero() {
		t.Fatalf("token deposit failed: %+v", res)
	}
	if got := len(rpc.SentTxs(t)); got != 2 {
		t.Fatalf("expected approve + deposit txs, got %d", got)
	}
}

func TestDualLPVerifyLPCovC(t *testing.T) {
	api := newCovThornode(t)
	lpPath := "/thorchain/pool/BTC.BTC/liquidity_providers"
	user, _ := newCovUser(t, 0, nil)
	a := newDualLPActorForUserCovC(t, common.BTCAsset, user)
	if !user.Acquire() {
		t.Fatal("acquire")
	}
	retry := func(step string) {
		t.Helper()
		if res := a.verifyLP(nil); res.Continue || res.Finish {
			t.Fatalf("%s: expected retry, got %+v", step, res)
		}
	}

	api.Status(lpPath, 400, "bad")
	retry("query failure")
	api.JSON(lpPath, []openapi.LiquidityProvider{})
	retry("no position")
	if a.lpWait != "waiting for LP position to be created" {
		t.Fatalf("unexpected wait state %q", a.lpWait)
	}
	api.JSON(lpPath, []openapi.LiquidityProvider{covCLP(user, common.BTCAsset, "0", "5", "0")})
	retry("pending asset")
	if !strings.Contains(a.lpWait, "pending_asset=5") {
		t.Fatalf("wait state must name pending side: %q", a.lpWait)
	}

	a.minLastAddHeight = 50
	lp := covCLP(user, common.BTCAsset, "10", "0", "0")
	api.JSON(lpPath, []openapi.LiquidityProvider{lp})
	retry("missing last add height")
	old := int64(50)
	lp.LastAddHeight = &old
	api.JSON(lpPath, []openapi.LiquidityProvider{lp})
	retry("stale last add height")
	if !user.IsLocked() {
		t.Fatal("user must stay locked while waiting")
	}
	fresh := int64(51)
	lp.LastAddHeight = &fresh
	api.JSON(lpPath, []openapi.LiquidityProvider{lp})
	if res := a.verifyLP(nil); !res.Finish || res.Error != nil {
		t.Fatalf("updated LP must finish: %+v", res)
	}
	assertReleased(t, user)
}
