package features

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestInactiveVaultRefundsTreeCov(t *testing.T) {
	root := InactiveVaultRefunds(rngCov())
	names := map[string]bool{}
	for c := range root.Children {
		names[c.Name] = true
		if len(c.Ops) != 7 {
			t.Errorf("%s: expected 7 ops, got %d", c.Name, len(c.Ops))
		}
	}
	for _, chain := range acommon.SimChains {
		name := "InactiveVaultRefund-" + chain.GetGasAsset().String()
		if chain.Equals(common.SOLChain) {
			if names[name] {
				t.Error("SOL must be skipped")
			}
			continue
		}
		if !names[name] {
			t.Errorf("missing %s", name)
		}
	}
}

func TestXMRHistoricalRefundTreeCov(t *testing.T) {
	root := XMRHistoricalRefund(rngCov())
	var mimir, actor *Actor
	root.WalkDepthFirst(func(a *Actor) bool {
		if a.Name == "Mimir(all) HaltChurning=1" {
			mimir = a
		}
		if a.Name == "XMRHistoricalRefund-DepartedSigner" {
			actor = a
		}
		return true
	})
	if mimir == nil || actor == nil {
		t.Fatal("expected halt-churning vote and historical actor")
	}
	if actor.Timeout != 35*time.Minute || !mimir.Children[actor] {
		t.Fatal("historical actor must follow the halt vote with a 35m timeout")
	}
}

func newInactiveCov(asset common.Asset) *InactiveVaultRefundActor {
	return &InactiveVaultRefundActor{Actor: *NewActor("iv", rngCov()), asset: asset}
}

func TestInactiveAcquireUserCov(t *testing.T) {
	routes := newRoutesCov(t, nil)
	a := newInactiveCov(common.BTCAsset)
	if res := a.acquireUser(&OpConfig{}); res.Continue {
		t.Fatal("pool error keeps pending")
	}
	routes.set("/thorchain/pool/BTC.BTC", `{"asset":"BTC.BTC","balance_asset":"100000"}`)
	if res := a.acquireUser(&OpConfig{}); res.Continue {
		t.Fatal("fee error keeps pending")
	}
	routes.set("/thorchain/outbound_fee/BTC.BTC", `[{"asset":"BTC.BTC","outbound_fee":"1000"}]`)

	locked, _, _ := newUserCov(t, "dog", common.BTCChain)
	locked.Acquire()
	bad, _, badClient := newUserCov(t, "cat", common.BTCChain)
	badClient.accErr = errCov
	poor, _, _ := newUserCov(t, "pig", common.BTCChain, common.NewCoin(common.BTCAsset, cosmos.NewUint(3000)))
	if res := a.acquireUser(&OpConfig{Users: []*User{locked, bad, poor}}); res.Continue {
		t.Fatal("nobody qualifies")
	}
	// three outbound fees beat 1% of the depth
	if !a.amount.Equal(cosmos.NewUint(3000)) || bad.IsLocked() || poor.IsLocked() {
		t.Fatalf("amount %s, released %v %v", a.amount, bad.IsLocked(), poor.IsLocked())
	}
	rich, _, _ := newUserCov(t, "pig", common.BTCChain, common.NewCoin(common.BTCAsset, cosmos.NewUint(3001)))
	if res := a.acquireUser(&OpConfig{Users: []*User{rich}}); !res.Continue || a.account != rich {
		t.Fatal("rich user expected")
	}
	if a.refundAddress != rich.Address(common.BTCChain) {
		t.Fatalf("refund address %s", a.refundAddress)
	}
}

func historicalFixtureCov() (openapi.Vault, []openapi.Node) {
	statuses := map[string]string{"a": "Active", "b": "Active", "c": "Active", "d": "Standby"}
	vault := openapi.Vault{
		Status: "InactiveVault", PubKeyFrost: openapi.PtrString("frost"), Membership: []string{"a", "b", "c", "d"},
		Addresses: []openapi.VaultAddress{{Chain: "BTC", Address: "btc"}, {Chain: "XMR", Address: ""}, {Chain: "XMR", Address: "xmr-addr"}},
	}
	nodes := []openapi.Node{}
	for _, key := range vault.Membership {
		nodes = append(nodes, openapi.Node{Status: statuses[key], PubKeySet: openapi.NodePubKeySet{Secp256k1: openapi.PtrString(key)}})
	}
	return vault, nodes
}

func TestInactiveSelectHistoricalVaultCov(t *testing.T) {
	routes := newRoutesCov(t, nil)
	vault, _ := historicalFixtureCov()
	a := newInactiveCov(common.XMRAsset)
	a.requireHistoricalRecovery = true
	vaults := []openapi.Vault{{Status: "ActiveVault"}, vault}
	var vaultsErr error
	a.getInactiveVaults = func() ([]openapi.Vault, error) { return vaults, vaultsErr }

	if res := a.selectInactiveVault(nil); res.Error == nil {
		t.Fatal("mimir error must surface")
	}
	routes.set("/thorchain/mimir", `{"HALTCHURNING":0}`)
	if res := a.selectInactiveVault(nil); res.Continue || res.Finish || res.Error != nil {
		t.Fatalf("must wait for halt churning, got %+v", res)
	}
	routes.set("/thorchain/mimir", `{"HALTCHURNING":1}`)
	if res := a.selectInactiveVault(nil); res.Error == nil {
		t.Fatal("nodes error must surface")
	}
	routes.set("/thorchain/nodes", `[
		{"status":"Active","pub_key_set":{"secp256k1":"a"}},
		{"status":"Active","pub_key_set":{"secp256k1":"b"}},
		{"status":"Active","pub_key_set":{"secp256k1":"c"}},
		{"status":"Standby","pub_key_set":{"secp256k1":"d"}}]`)
	vaultsErr = errCov
	if res := a.selectInactiveVault(nil); res.Error != errCov {
		t.Fatal("vault error must surface")
	}
	vaultsErr = nil
	res := a.selectInactiveVault(nil)
	if !res.Continue || a.inboundAddress != "xmr-addr" || a.inboundVault != "frost" || len(a.recoveryMembers) != 4 {
		t.Fatalf("expected historical vault selected, got %+v %+v", res, a)
	}

	vaults = []openapi.Vault{{Status: "ActiveVault"}}
	if res = a.selectInactiveVault(nil); !res.Finish || res.Error == nil || !strings.Contains(res.Error.Error(), "cluster-churn") {
		t.Fatalf("missing vault must finish with error, got %+v", res)
	}
}

func TestInactiveSelectVaultNoAddressCov(t *testing.T) {
	a := newInactiveCov(common.BTCAsset)
	a.getInactiveVaults = func() ([]openapi.Vault, error) {
		return []openapi.Vault{{Status: "InactiveVault", Addresses: []openapi.VaultAddress{{Chain: "ETH", Address: "e"}}}}, nil
	}
	if res := a.selectInactiveVault(nil); !res.Finish || res.Error == nil {
		t.Fatalf("no BTC vault must finish with error, got %+v", res)
	}
}

func TestInactiveRegisterAndResolveMemoCov(t *testing.T) {
	routes := newRoutesCov(t, nil)

	// non-XMR chains use a plain memo and skip registration
	b := newInactiveCov(common.BTCAsset)
	if res := b.registerRefundMemo(nil); !res.Continue || b.memo != "TEST-INACTIVE-VAULT-REFUND" {
		t.Fatalf("plain memo expected, got %+v", res)
	}
	if res := b.resolveRefundMemo(nil); !res.Continue || b.memo != "TEST-INACTIVE-VAULT-REFUND" {
		t.Fatal("non-XMR resolve is a no-op")
	}

	user, bridge, _ := newUserCov(t, "dog", common.XMRChain)
	a := newInactiveCov(common.XMRAsset)
	a.account = user
	a.refundAddress = "xmr-refund"
	bridge.err = errCov
	if res := a.registerRefundMemo(nil); res.Error == nil {
		t.Fatal("broadcast error must surface")
	}
	bridge.err = nil
	bridge.txid = "REG1"
	if res := a.registerRefundMemo(nil); !res.Continue || a.registrationTxID != "REG1" {
		t.Fatalf("registration expected, got %+v", res)
	}
	want := "+:XMR.XMR:" + thorAddrCov(t, user).String() + "/xmr-refund"
	if a.registeredMemo != want || lastMemoCov(t, bridge) != "REFERENCE:XMR.XMR:"+want {
		t.Fatalf("unexpected memo %q / %q", a.registeredMemo, lastMemoCov(t, bridge))
	}
	if _, ok := bridge.sent()[0].(*types.MsgDeposit); !ok {
		t.Fatal("registration must be MsgDeposit")
	}

	if res := a.resolveRefundMemo(nil); res.Error == nil {
		t.Fatal("memo lookup error must surface")
	}
	routes.set("/thorchain/memo/REG1", `{"asset":"XMR.XMR","memo":"other","reference":"42"}`)
	if res := a.resolveRefundMemo(nil); res.Error == nil {
		t.Fatal("mismatched reference must error")
	}
	routes.set("/thorchain/memo/REG1", `{"asset":"XMR.XMR","memo":"`+want+`","reference":"42"}`)
	if res := a.resolveRefundMemo(nil); !res.Continue || a.memo != "r:42" {
		t.Fatalf("expected r:42, got %+v %q", res, a.memo)
	}
}

func TestInactiveSendInboundCov(t *testing.T) {
	user, _, client := newUserCov(t, "dog", common.BTCChain)
	a := newInactiveCov(common.BTCAsset)
	a.account = user
	a.inboundAddress = "bc-old"
	a.amount = cosmos.NewUint(9)
	a.memo = "M"
	if res := a.sendInactiveVaultInbound(nil); !res.Continue || a.inboundTxid != "TXID1" {
		t.Fatalf("expected send, got %+v", res)
	}
	if len(client.signed) != 1 || client.signed[0].ToAddress != "bc-old" || client.signed[0].Memo != "M" {
		t.Fatalf("unexpected tx %+v", client.signed)
	}
	client.bcastErr = errCov
	if res := a.sendInactiveVaultInbound(nil); res.Continue || res.Error == nil {
		t.Fatal("broadcast error must surface")
	}
}

func TestInactivePrepareHistoricalRecoveryCov(t *testing.T) {
	a := newInactiveCov(common.XMRAsset)
	if res := a.prepareHistoricalRecovery(nil); !res.Continue {
		t.Fatal("non-historical is a no-op")
	}
	a.requireHistoricalRecovery = true
	calls := 0
	a.recoveryGate = func(*OpConfig) OpResult { calls++; return OpResult{Continue: calls > 1} }
	if res := a.prepareHistoricalRecovery(nil); res.Continue || calls != 1 {
		t.Fatal("gate result must be returned")
	}
	if res := a.prepareHistoricalRecovery(nil); !res.Continue || calls != 2 {
		t.Fatal("same gate must be reused")
	}
}

func refundDetailsCov(vault, to string) string {
	return `{"tx":{"observed_pub_key":"` + vault + `"},"outbound_height":77,
		"actions":[{"chain":"XMR","to_address":"` + to + `","in_hash":"IN1","memo":"REFUND:IN1","coin":{"asset":"XMR.XMR","amount":"5"},"max_gas":[]}],
		"out_txs":[{"id":"OUT1","chain":"XMR","to_address":"` + to + `","coins":[{"asset":"XMR.XMR","amount":"5"}]}]}`
}

func TestInactiveVerifyRefundCov(t *testing.T) {
	routes := newRoutesCov(t, nil)
	user, _, _ := newUserCov(t, "dog", common.XMRChain)
	a := newInactiveCov(common.XMRAsset)
	a.account = user
	a.inboundTxid = "IN1"
	a.inboundVault = "frost"
	a.refundAddress = "refund"
	a.amount = cosmos.NewUint(10)

	if res := a.verifyRefund(&OpConfig{}); res.Finish {
		t.Fatal("stages error keeps pending")
	}
	routes.set("/thorchain/tx/stages/IN1", `{"outbound_signed":{"completed":false}}`)
	if res := a.verifyRefund(&OpConfig{}); res.Finish {
		t.Fatal("incomplete keeps pending")
	}
	routes.set("/thorchain/tx/stages/IN1", `{"outbound_signed":{"completed":true}}`)
	if res := a.verifyRefund(&OpConfig{}); res.Finish {
		t.Fatal("details error keeps pending")
	}

	// wrong vault: finish with validation error and release
	user.Acquire()
	routes.set("/thorchain/tx/details/IN1", refundDetailsCov("other", "refund"))
	if res := a.verifyRefund(&OpConfig{}); !res.Finish || res.Error == nil || user.IsLocked() {
		t.Fatalf("validation error expected, got %+v", res)
	}

	// valid refund
	user.Acquire()
	routes.set("/thorchain/tx/details/IN1", refundDetailsCov("frost", "refund"))
	if res := a.verifyRefund(&OpConfig{}); !res.Finish || res.Error != nil || user.IsLocked() {
		t.Fatalf("valid refund expected, got %+v", res)
	}
}

func TestInactiveVerifyHistoricalRecoveryCov(t *testing.T) {
	routes := newRoutesCov(t, nil)
	user, _, _ := newUserCov(t, "dog", common.XMRChain)
	node, nodeBridge, _ := newUserCov(t, "cat", "")
	cfg := &OpConfig{NodeUsers: []*User{node}}
	a := newInactiveCov(common.XMRAsset)
	a.requireHistoricalRecovery = true
	a.account = user
	a.inboundTxid = "IN1"
	a.inboundVault = "frost"
	a.inboundAddress = "xmr-vault"
	a.refundAddress = "refund"
	a.amount = cosmos.NewUint(10)
	a.recoveryMembers = map[string]string{"a": "Active", "b": "Active", "c": "Active", "d": "Standby"}
	routes.set("/thorchain/tx/stages/IN1", `{"outbound_signed":{"completed":true}}`)
	routes.set("/thorchain/tx/details/IN1", refundDetailsCov("frost", "refund"))

	// policy query failure: retry without releasing
	user.Acquire()
	nodeBridge.polErr = errCov
	if res := a.verifyRefund(cfg); res.Finish || res.Error == nil || !user.IsLocked() {
		t.Fatalf("policy error must retry, got %+v", res)
	}
	nodeBridge.polErr = nil

	// invalid policy: finish and release
	nodeBridge.policy = thorclient.KeysignPolicy{RequiredSigners: 3, Signers: common.PubKeys{"a", "b", "c"}}
	if res := a.verifyRefund(cfg); !res.Finish || res.Error == nil || user.IsLocked() {
		t.Fatalf("invalid policy must finish, got %+v", res)
	}
	nodeBridge.policy = thorclient.KeysignPolicy{RequiredSigners: 3, Signers: common.PubKeys{"a", "b", "d"}}

	details, err := a.historicalDetailsCov()
	if err != nil {
		t.Fatal(err)
	}

	// inbound voter lookup fails
	if res := a.verifyHistoricalRecovery(cfg, details); res.Error == nil || res.Finish {
		t.Fatalf("voter error must retry, got %+v", res)
	}
	// refund change reported as inbound to the vault
	routes.set("/thorchain/tx/details/OUT1", `{"tx":{"tx":{"chain":"XMR","to_address":"xmr-vault"}}}`)
	if res := a.verifyHistoricalRecovery(cfg, details); !res.Finish || res.Error == nil {
		t.Fatalf("false inbound must finish with error, got %+v", res)
	}
	routes.set("/thorchain/tx/details/OUT1", `{"tx":{"tx":{"chain":"XMR","to_address":"refund"}}}`)
	if res := a.verifyHistoricalRecovery(cfg, details); res.Error == nil {
		t.Fatal("outbound voter error must surface")
	}
	routes.set("/thorchain/tx/details/OUT1?outbound=true", `{"finalised_height":0}`)
	if res := a.verifyHistoricalRecovery(cfg, details); res.Continue || res.Finish || res.Error != nil {
		t.Fatalf("unfinalised outbound must wait, got %+v", res)
	}

	// Documents current behaviour: THORNode serialises int64 fields as JSON
	// strings, which encoding/json cannot decode into FinalisedHeight (suspected bug).
	routes.set("/thorchain/tx/details/OUT1?outbound=true", `{"finalised_height":"12"}`)
	if res := a.verifyHistoricalRecovery(cfg, details); res.Error == nil {
		t.Fatal("string-encoded finalised_height currently fails to decode")
	}

	routes.set("/thorchain/tx/details/OUT1?outbound=true", `{"finalised_height":12}`)
	user.Acquire()
	if res := a.verifyRefund(cfg); !res.Finish || res.Error != nil || user.IsLocked() {
		t.Fatalf("finalised historical refund must finish cleanly, got %+v", res)
	}
}

func (a *InactiveVaultRefundActor) historicalDetailsCov() (openapi.TxDetailsResponse, error) {
	var d openapi.TxDetailsResponse
	return d, json.Unmarshal([]byte(refundDetailsCov(a.inboundVault, string(a.refundAddress))), &d)
}
