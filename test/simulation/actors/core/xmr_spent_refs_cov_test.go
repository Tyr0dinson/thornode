package core

import (
	"errors"
	rand "math/rand/v2"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func newXMRSpentRefActorCovB2() *XMRSpentRefVerifyActor {
	return &XMRSpentRefVerifyActor{Actor: *simtypes.NewActor("spent", rand.New(rand.NewPCG(1, 2)))}
}

func TestNewXMRSpentRefVerifyActorCovB2(t *testing.T) {
	actor := NewXMRSpentRefVerifyActor(rand.New(rand.NewPCG(1, 2)))
	if actor.Name != "XMRSpentRefVerify" || len(actor.Ops) != 5 || actor.Timeout != swapActorTimeout(common.BTCAsset, common.XMRAsset) {
		t.Fatalf("unexpected actor %s ops=%d timeout=%s", actor.Name, len(actor.Ops), actor.Timeout)
	}
	for i, name := range []string{"verifyVaultHasXMR", "swapToXMR", "waitForSwapOutbound", "waitForSpentRefsOnVault", "logVaultBalance"} {
		if !strings.Contains(actor.Ops[i].String(), name) {
			t.Fatalf("op %d = %s, want %s", i, actor.Ops[i], name)
		}
	}
}

func TestXMRSpentRefVerifyVaultHasXMRCovB2(t *testing.T) {
	api := newCovThornode(t)
	a := newXMRSpentRefActorCovB2()
	vaultsPath := "/thorchain/vaults/asgard"

	api.Status(vaultsPath, 400, "bad")
	if res := a.verifyVaultHasXMR(nil); res.Continue || res.Finish {
		t.Fatalf("vault query failure must retry: %+v", res)
	}

	api.JSON(vaultsPath, `[{"status":"RetiringVault","pub_key_frost":"old"},{"status":"ActiveVault","pub_key_frost":""}]`)
	if res := a.verifyVaultHasXMR(nil); res.Continue || res.Finish {
		t.Fatalf("no active frost vault must retry: %+v", res)
	}

	api.JSON(vaultsPath, `[{"status":"ActiveVault","pub_key_frost":"frost1","vault_creation_height_xmr":5}]`)
	api.Status("/thorchain/xmr/spent_refs/frost1", 400, "bad")
	if res := a.verifyVaultHasXMR(nil); res.Continue || res.Finish {
		t.Fatalf("spent ref snapshot failure must retry: %+v", res)
	}
	if a.vaultPubKey != "" {
		t.Fatal("failed snapshot must not capture a vault")
	}

	ref := common.OutputRef{TxHash: strings.Repeat("A", 64), OutputIndex: 3}
	api.JSON("/thorchain/xmr/spent_refs/frost1", map[string]interface{}{"spent_output_refs": []common.OutputRef{ref}})
	if res := a.verifyVaultHasXMR(nil); !res.Continue {
		t.Fatalf("active vault must continue: %+v", res)
	}
	if a.vaultPubKey != "frost1" {
		t.Fatalf("captured vault %q", a.vaultPubKey)
	}
	if _, ok := a.spentBefore[strings.Repeat("a", 64)+":3"]; !ok || len(a.spentBefore) != 1 {
		t.Fatalf("unexpected snapshot %v", a.spentBefore)
	}
}

func TestXMRSpentRefSwapToXMRCovB2(t *testing.T) {
	api := newCovThornode(t)
	a := newXMRSpentRefActorCovB2()
	node, nodeBridge := newCovUser(t, 12, nil)
	btc := newCovChainClient()
	btc.TxID = "SWAPTX"
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.BTCChain: btc})

	nodeBridge.NetworkFeeFn = func(common.Chain) (uint64, uint64, error) { return 0, 0, nil }
	if res := a.swapToXMR(&simtypes.OpConfig{NodeUsers: []*simtypes.User{node}, Users: []*simtypes.User{user}}); res.Continue || res.Finish {
		t.Fatalf("missing XMR fee must wait: %+v", res)
	}
	nodeBridge.NetworkFeeFn = nil

	if res := a.swapToXMR(&simtypes.OpConfig{NodeUsers: []*simtypes.User{node}}); res.Continue || res.Finish {
		t.Fatalf("no users must retry: %+v", res)
	}
	noBTC, _ := newCovUser(t, 1, nil)
	if res := a.swapToXMR(&simtypes.OpConfig{NodeUsers: []*simtypes.User{node}, Users: []*simtypes.User{noBTC}}); res.Continue || res.Finish {
		t.Fatalf("no BTC client must retry: %+v", res)
	}

	cfg := &simtypes.OpConfig{NodeUsers: []*simtypes.User{node}, Users: []*simtypes.User{user}}
	api.Status("/thorchain/inbound_addresses", 400, "bad")
	if res := a.swapToXMR(cfg); res.Continue || res.Finish || a.swapTxID != "" {
		t.Fatalf("deposit failure must retry: %+v", res)
	}

	api.JSON("/thorchain/inbound_addresses", `[{"chain":"BTC","address":"bc1qvault"}]`)
	if res := a.swapToXMR(cfg); !res.Continue || a.swapTxID != "SWAPTX" {
		t.Fatalf("swap must be sent: %+v txid=%q", res, a.swapTxID)
	}
	tx := btc.Signed[0]
	if tx.ToAddress != "bc1qvault" || tx.Memo != "=:XMR.XMR:"+user.Address(common.XMRChain).String() || tx.Coin.Amount.Uint64() != 10_000_000 {
		t.Fatalf("unexpected swap tx %+v", tx)
	}

	// already sent: no second deposit
	if res := a.swapToXMR(cfg); !res.Continue || len(btc.Signed) != 1 {
		t.Fatalf("swap must be sent once: %+v", res)
	}
}

func TestXMRSpentRefWaitForSwapOutboundCovB2(t *testing.T) {
	a := newXMRSpentRefActorCovB2()
	a.swapTxID = "SWAP"
	stages := openapi.TxStagesResponse{}
	details := openapi.TxDetailsResponse{}
	var stagesErr error
	a.getSwapStages = func(id string) (openapi.TxStagesResponse, error) {
		if id != "SWAP" {
			t.Fatalf("queried %s", id)
		}
		return stages, stagesErr
	}
	a.getSwapDetails = func(string) (openapi.TxDetailsResponse, error) { return details, nil }

	stagesErr = errors.New("not found")
	if res := a.waitForSwapOutbound(nil); res.Continue || res.Finish {
		t.Fatalf("stage failure must wait: %+v", res)
	}
	stagesErr = nil
	if res := a.waitForSwapOutbound(nil); res.Continue || res.Finish {
		t.Fatalf("unsigned outbound must wait: %+v", res)
	}

	stages.OutboundSigned = &openapi.OutboundSignedStage{Completed: true}
	details.OutTxs = []openapi.Tx{{Chain: openapi.PtrString("BTC"), Id: openapi.PtrString("OUT")}}
	if res := a.waitForSwapOutbound(nil); !res.Finish || res.Error == nil {
		t.Fatalf("non-XMR outbound must fail: %+v", res)
	}
	details.OutTxs = []openapi.Tx{{Chain: openapi.PtrString("XMR")}}
	if res := a.waitForSwapOutbound(nil); !res.Finish || res.Error == nil {
		t.Fatalf("unidentified outbound must fail: %+v", res)
	}
	details.OutTxs = []openapi.Tx{{Chain: openapi.PtrString("XMR"), Id: openapi.PtrString("OUTX")}}
	if res := a.waitForSwapOutbound(nil); !res.Continue || a.outboundTxID != "OUTX" {
		t.Fatalf("XMR outbound must be captured: %+v %q", res, a.outboundTxID)
	}
}

func TestXMRSpentRefWaitForSpentRefsOnVaultCovB2(t *testing.T) {
	api := newCovThornode(t)
	outbound := strings.Repeat("b", 64)
	a := newXMRSpentRefActorCovB2()
	a.outboundTxID = outbound
	a.captureSpentRefs("frost-vault", nil)
	txPath := "/thorchain/tx/" + outbound
	refsPath := "/thorchain/xmr/spent_refs/frost-vault"

	api.Status(txPath, 404, "missing")
	if res := a.waitForSpentRefsOnVault(nil); res.Continue || res.Finish {
		t.Fatalf("missing observation must wait: %+v", res)
	}
	observed := map[string]interface{}{"observed_tx": map[string]interface{}{
		"tx":               map[string]interface{}{"id": outbound, "chain": "XMR", "coins": []interface{}{}, "gas": []interface{}{}},
		"observed_pub_key": "frost-vault",
		"status":           "done",
	}}
	api.JSON(txPath, observed)
	api.Status(refsPath, 400, "bad")
	if res := a.waitForSpentRefsOnVault(nil); res.Continue || res.Finish {
		t.Fatalf("stored ref failure must wait: %+v", res)
	}

	ref := common.OutputRef{TxHash: strings.Repeat("f", 64), OutputIndex: 0, KeyImage: strings.Repeat("1", 64), SpendTxHash: outbound}
	api.JSON(refsPath, map[string]interface{}{"spent_output_refs": []common.OutputRef{ref}})
	if res := a.waitForSpentRefsOnVault(nil); !res.Continue || res.Error != nil {
		t.Fatalf("new stored refs must verify: %+v", res)
	}
}

func TestXMRSpentRefLogVaultBalanceCovB2(t *testing.T) {
	api := newCovThornode(t)
	a := newXMRSpentRefActorCovB2()
	api.Status("/thorchain/vaults/asgard", 400, "bad")
	if res := a.logVaultBalance(nil); res.Continue || res.Finish {
		t.Fatalf("vault failure must retry: %+v", res)
	}
	api.JSON("/thorchain/vaults/asgard", `[
		{"status":"RetiringVault","coins":[{"asset":"XMR.XMR","amount":"1"}]},
		{"status":"ActiveVault","coins":[{"asset":"BTC.BTC","amount":"2"},{"asset":"XMR.XMR","amount":"3"}]}
	]`)
	if res := a.logVaultBalance(nil); !res.Finish || res.Error != nil {
		t.Fatalf("balance logging must finish: %+v", res)
	}
}
