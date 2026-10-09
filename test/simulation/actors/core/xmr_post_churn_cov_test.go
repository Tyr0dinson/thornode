package core

import (
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
	"gitlab.com/thorchain/thornode/v3/constants"
	simmonero "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/monero"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const (
	covDXMRBefore = "xmr-before"
	covDXMRAfter  = "xmr-after"
)

// covDXMRKeys derives secp256k1 and ed25519 keys for mnemonic idx, as NewUser does.
func covDXMRKeys(t *testing.T, idx int) *thorclient.Keys {
	t.Helper()
	mnemonic := covMnemonics[idx]
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kr := keyring.NewInMemory(cdc, func(options *keyring.Options) {
		options.SupportedAlgos = keyring.SigningAlgoList{hd.Secp256k1, ed25519.Ed25519}
	})
	name := strings.Split(mnemonic, " ")[0]
	if _, err := kr.NewAccount(name, mnemonic, "", cmd.THORChainHDPath, hd.Secp256k1); err != nil {
		t.Fatal(err)
	}
	if _, err := kr.NewAccount(ed25519.SignerNameEDDSA(name), mnemonic, "", ed25519.HDPath, ed25519.Ed25519); err != nil {
		t.Fatal(err)
	}
	return thorclient.NewKeysWithKeybase(kr, name, "")
}

// covDMoneroClient builds a real simulation Monero client whose FROST balance
// endpoint is the fake API.
func covDMoneroClient(t *testing.T, api *covThornode, idx int) *simmonero.Client {
	t.Helper()
	t.Setenv("XMR_FROST_ENDPOINT", api.URL())
	client, err := simmonero.NewClient(common.XMRChain, api.URL(), covDXMRKeys(t, idx))
	if err != nil {
		t.Fatal(err)
	}
	return client.(*simmonero.Client)
}

// covDBFTNodes returns MinimumNodesForBFT validator users.
func covDBFTNodes(t *testing.T) []*User {
	t.Helper()
	minimum := int(constants.NewConstantValue().GetInt64Value(constants.MinimumNodesForBFT))
	users := make([]*User, 0, minimum)
	for i := 0; i < minimum; i++ {
		u, _ := newCovUser(t, 12+i%6, nil)
		users = append(users, u)
	}
	return users
}

func covDStatuses(n int, status string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = status
	}
	return out
}

func covDXMRVault(users []*User, address, amount string) map[string]interface{} {
	members := make([]string, 0, len(users))
	for _, u := range users {
		members = append(members, u.PubKey(common.THORChain).String())
	}
	return map[string]interface{}{
		"status":        covDActiveVault,
		"status_since":  50,
		"pub_key_frost": "frost-key",
		"membership":    members,
		"addresses":     []map[string]string{{"chain": "XMR", "address": address}},
		"coins":         []map[string]string{{"asset": "XMR.XMR", "amount": amount}},
	}
}

func TestXMRPostChurnConstructionCovD(t *testing.T) {
	a := NewXMRPostChurnActor(covDRNG())
	if len(a.Ops) != 7 || !strings.Contains(a.Ops[0].String(), "captureActiveInbound") || !strings.Contains(a.Ops[1].String(), "startChurn") {
		t.Fatalf("unexpected ops %v", a.Ops)
	}
	before, after := NewXMRChurnOutChecks(covDRNG())
	if before.Timeout != after.Timeout || len(before.Ops) != 1 {
		t.Fatalf("unexpected churn-out checks %+v %+v", before, after)
	}
}

func TestXMRPostChurnCaptureActiveInboundCovD(t *testing.T) {
	api := newCovThornode(t)
	nodes := covDBFTNodes(t)
	a := newXMRPostChurnActor(covDRNG())

	// plain capture: query failure retries
	if res := a.captureActiveInbound(nil); res.Continue || res.Finish {
		t.Fatalf("query failure: %+v", res)
	}
	api.JSON(covDPathInbound, []interface{}{covDInbound(common.XMRChain, covDXMRBefore, "", "")})
	if res := a.captureActiveInbound(nil); !res.Continue || a.inboundBefore != covDXMRBefore {
		t.Fatalf("expected capture: %+v %s", res, a.inboundBefore)
	}

	// churn-out capture requires matching membership
	before, after := NewXMRChurnOutChecks(covDRNG())
	_ = after
	if res := before.Ops[0](nil); res.Continue {
		t.Fatalf("nodes failure: %+v", res)
	}
	api.JSON(covDPathNodes, covDNodes(t, nodes, covDStatuses(len(nodes), covDActive)...))
	if res := before.Ops[0](nil); res.Continue {
		t.Fatalf("vaults failure: %+v", res)
	}
	api.JSON(covDPathVaults, []interface{}{covDXMRVault(nodes[:len(nodes)-1], covDXMRBefore, "100")})
	if res := before.Ops[0](nil); res.Continue {
		t.Fatalf("membership mismatch must wait: %+v", res)
	}
	api.JSON(covDPathVaults, []interface{}{covDXMRVault(nodes, covDXMRBefore, "100")})
	if res := before.Ops[0](nil); !res.Continue {
		t.Fatalf("matching membership must capture: %+v", res)
	}
}

func TestXMRPostChurnStartChurnCovD(t *testing.T) {
	api := newCovThornode(t)
	covDReadyEnv(t, api, 7)
	dog, bridge := newCovUser(t, 12, nil)
	cat, _ := newCovUser(t, 13, nil)
	cfg := &OpConfig{NodeUsers: []*User{dog, cat}}
	a := newXMRPostChurnActor(covDRNG())

	if res := a.startChurn(cfg); res.Continue || res.Finish {
		t.Fatalf("nodes failure: %+v", res)
	}
	api.JSON(covDPathNodes, covDNodes(t, []*User{dog, cat}, covDActive, covDActive))
	if res := a.startChurn(cfg); !res.Continue {
		t.Fatalf("already churned must continue: %+v", res)
	}
	if bridge.MsgCount() != 0 {
		t.Fatal("no mimirs expected when already churned")
	}

	api.JSON(covDPathNodes, covDNodes(t, []*User{dog, cat}, covDActive, covDStandby))
	api.JSON("/json_rpc", `{"result":{"count":0}}`)
	if res := a.startChurn(cfg); res.Continue || res.Finish {
		t.Fatalf("monero height zero must block: %+v", res)
	}
	api.JSON("/json_rpc", `{"result":{"count":7}}`)

	if !dog.Acquire() {
		t.Fatal("acquire")
	}
	if res := a.startChurn(cfg); res.Continue || res.Finish {
		t.Fatalf("locked node: %+v", res)
	}
	dog.Release()

	bridge.HeightFn = func() (int64, error) { return 0, errBoomCovD }
	if res := a.startChurn(cfg); res.Continue || res.Finish {
		t.Fatalf("height failure: %+v", res)
	}
	bridge.HeightFn = nil

	if res := a.startChurn(cfg); res.Continue || res.Finish {
		t.Fatalf("vault failure: %+v", res)
	}
	api.JSON(covDPathVaults, []map[string]interface{}{{"status": covDRetiringVlt}})
	if res := a.startChurn(cfg); res.Continue || res.Finish {
		t.Fatalf("no active vault: %+v", res)
	}
	api.JSON(covDPathVaults, []map[string]interface{}{{"status": covDActiveVault, "status_since": 30}})

	for n := 0; n < 4; n++ {
		bridge.BroadcastFn = covDFailAfter(n)
		if res := a.startChurn(cfg); res.Continue || res.Finish {
			t.Fatalf("broadcast failure after %d: %+v", n, res)
		}
	}

	bridge.BroadcastFn = nil
	bridge.Msgs = nil
	if res := a.startChurn(cfg); !res.Continue {
		t.Fatalf("expected churn start: %+v", res)
	}
	got := covDMimirMsgs(t, bridge)
	want := map[string]int64{
		"CHURNINTERVAL":            80, // 100 - 30 + 10
		"CHURNRETRYINTERVAL":       xmrPostChurnRetryInterval,
		"NUMBEROFNEWNODESPERCHURN": xmrPostChurnNewNodesPerChurn,
		"HALTCHURNING":             0,
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected mimirs %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("mimir %s=%d, want %d", k, got[k], v)
		}
	}
	if dog.IsLocked() {
		t.Fatal("node leaked")
	}
}

func TestXMRPostChurnWaitForVaultRotationCovD(t *testing.T) {
	api := newCovThornode(t)
	nodes := covDBFTNodes(t)
	monero := covDMoneroClient(t, api, 0)
	xmrUser, _ := newCovUser(t, 0, map[common.Chain]LiteChainClient{common.XMRChain: monero})
	cfg := &OpConfig{Users: []*User{xmrUser}}
	vaultPub := nodes[0].PubKey(common.XMRChain)
	balancePath := "/v1/monero/balance/" + vaultPub.String()

	a := newXMRPostChurnActor(covDRNG())
	a.inboundBefore = covDXMRBefore

	if res := a.waitForVaultRotation(cfg); res.Continue || res.Finish {
		t.Fatalf("nodes failure: %+v", res)
	}
	api.JSON(covDPathNodes, covDNodes(t, nodes[:1], covDActive))
	if res := a.waitForVaultRotation(cfg); res.Continue || res.Finish {
		t.Fatalf("single active node must wait: %+v", res)
	}
	api.JSON(covDPathNodes, covDNodes(t, nodes, covDStatuses(len(nodes), covDActive)...))
	if res := a.waitForVaultRotation(cfg); res.Continue || res.Finish {
		t.Fatalf("inbound failure: %+v", res)
	}
	api.JSON(covDPathInbound, []interface{}{covDInbound(common.XMRChain, covDXMRBefore, "", vaultPub.String())})
	if res := a.waitForVaultRotation(cfg); res.Continue || res.Finish {
		t.Fatalf("unrotated inbound must wait: %+v", res)
	}
	api.JSON(covDPathInbound, []interface{}{covDInbound(common.XMRChain, covDXMRAfter, "", vaultPub.String())})
	if res := a.waitForVaultRotation(cfg); res.Continue || res.Finish {
		t.Fatalf("vault failure: %+v", res)
	}
	api.JSON(covDPathVaults, []interface{}{covDXMRVault(nodes, covDXMRAfter, "0")})
	if res := a.waitForVaultRotation(cfg); res.Continue || res.Finish {
		t.Fatalf("unfunded vault must wait: %+v", res)
	}
	api.JSON(covDPathVaults, []interface{}{covDXMRVault(nodes, covDXMRAfter, "500")})

	// no monero client available
	if res := a.waitForVaultRotation(&OpConfig{}); res.Continue || res.Finish {
		t.Fatalf("missing monero client must wait: %+v", res)
	}
	// balance endpoint returns no spendable funds
	api.JSON(balancePath, `{"balance":5000000,"unlocked_balance":0}`)
	if res := a.waitForVaultRotation(cfg); res.Continue || res.Finish {
		t.Fatalf("unspendable vault must wait: %+v", res)
	}
	api.JSON(balancePath, `{"balance":5000000,"unlocked_balance":5000000}`)
	if res := a.waitForVaultRotation(cfg); !res.Continue {
		t.Fatalf("expected rotation complete: %+v", res)
	}
	if a.inboundAfter != covDXMRAfter || !a.expectedOutboundVault.Equals(vaultPub) {
		t.Fatalf("unexpected rotation state after=%s vault=%s", a.inboundAfter, a.expectedOutboundVault)
	}

	// churn-out verification requires member identities to match
	_, after := NewXMRChurnOutChecks(covDRNG())
	api.JSON(covDPathVaults, []interface{}{covDXMRVault(nodes[:len(nodes)-1], covDXMRAfter, "500")})
	if res := after.Ops[0](cfg); res.Continue || res.Finish || res.Error != nil {
		t.Fatalf("membership mismatch must wait: %+v", res)
	}
	api.JSON(covDPathNodes, covDNodes(t, nodes[:2], covDActive, covDActive))
	if res := after.Ops[0](cfg); res.Continue {
		t.Fatalf("churn-out requires BFT minimum active nodes: %+v", res)
	}
}

func TestVaultHasPositiveCoinCovD(t *testing.T) {
	for _, tc := range []struct {
		amount string
		want   bool
	}{{"1", true}, {"0", false}, {"nan", false}} {
		v := covDXMRVault(nil, "a", tc.amount)
		vault := covDDecodeVault(t, v)
		if got := vaultHasPositiveCoin(vault, common.XMRAsset); got != tc.want {
			t.Fatalf("amount %s: got %v", tc.amount, got)
		}
	}
	if vaultHasPositiveCoin(covDDecodeVault(t, covDXMRVault(nil, "a", "1")), common.BTCAsset) {
		t.Fatal("missing asset must not be positive")
	}
}
