package core

import (
	"errors"
	rand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func newCovAKeygenJail() *XMRKeygenJailActor {
	return &XMRKeygenJailActor{Actor: covAActor("keygen-jail")}
}

// covAReadyXMRFrostEnv points the XMR FROST churn readiness gate at a local
// httptest server whose health endpoints succeed and whose monero daemon
// reports a positive height.
func covAReadyXMRFrostEnv(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"0","result":{"count":42}}`))
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SIMULATION_BIFROST_READINESS_ENDPOINTS", srv.URL+"/p2pid")
	t.Setenv("SIMULATION_XMR_FROST_READINESS_ENDPOINTS", srv.URL+"/health")
	t.Setenv("SIMULATION_XMR_DAEMON_RPC", srv.URL+"/json_rpc")
}

func TestNewXMRKeygenJailActorCovA(t *testing.T) {
	a := NewXMRKeygenJailActor(rand.New(rand.NewPCG(1, 2)))
	if a.Name != "XMRKeygenJail" || len(a.Ops) != 3 || a.Timeout != 20*time.Minute {
		t.Fatalf("unexpected actor %s ops=%d timeout=%s", a.Name, len(a.Ops), a.Timeout)
	}
	if !strings.Contains(a.Ops[2].String(), "waitForKeygenJailSplit") {
		t.Fatalf("unexpected last op %s", a.Ops[2])
	}
}

func TestXMRKeygenJailCapturePreChurnVaultsCovA(t *testing.T) {
	api := newCovThornode(t)
	a := newCovAKeygenJail()
	covAAssertPending(t, "vaults error", a.capturePreChurnVaults(nil))
	api.JSON(covAVaultsPath, []openapi.Vault{{PubKey: covAStr("a"), Status: "ActiveVault"}, {PubKey: covAStr("b"), Status: "RetiringVault"}})
	covAAssertContinue(t, "capture", a.capturePreChurnVaults(nil))
	if len(a.vaultsBefore) != 2 || !a.vaultsBefore["a"] || !a.vaultsBefore["b"] {
		t.Fatalf("unexpected snapshot %v", a.vaultsBefore)
	}
}

func TestXMRKeygenJailStartChurnCovA(t *testing.T) {
	api := newCovThornode(t)
	users, bridges := covAValidators(t, 4)
	cfg := &simtypes.OpConfig{NodeUsers: users}
	a := newCovAKeygenJail()

	covAAssertPending(t, "nodes error", a.startChurn(cfg))

	api.JSON(covANodesPath, covANodes(t, users[:2], covAActiveNode))
	covAAssertFatal(t, "already churned", a.startChurn(cfg), "cluster already churned (2 active nodes)")

	// One active node; the other validators are not registered yet, so the
	// FROST readiness gate keeps the actor waiting without broadcasting.
	nodes := append(covANodes(t, users[:1], covAActiveNode), covANodes(t, users[1:], covAStandbyNode)...)
	api.JSON(covANodesPath, nodes[:2])
	covAAssertPending(t, "readiness", a.startChurn(cfg))
	if bridges[0].MsgCount() != 0 {
		t.Fatal("mimirs broadcast before readiness")
	}

	covAReadyXMRFrostEnv(t)
	api.JSON(covANodesPath, nodes)

	if !users[0].Acquire() {
		t.Fatal("acquire")
	}
	covAAssertPending(t, "node locked", a.startChurn(cfg))
	users[0].Release()

	bridges[0].BroadcastFn = func(...sdk.Msg) (common.TxID, error) { return "", errors.New("rejected") }
	covAAssertPending(t, "broadcast error", a.startChurn(cfg))
	if users[0].IsLocked() {
		t.Fatal("node lock leaked after broadcast failure")
	}

	bridges[0].BroadcastFn = nil
	bridges[0].Msgs = nil
	covAAssertContinue(t, "start churn", a.startChurn(cfg))
	wantKeys := []string{"NUMBEROFNEWNODESPERCHURN", "CHURNRETRYINTERVAL", "HALTCHURNING"}
	wantVals := []int64{xmrPostChurnNewNodesPerChurn, xmrPostChurnRetryInterval, 0}
	if len(bridges[0].Msgs) != 3 {
		t.Fatalf("expected 3 mimirs, got %d", len(bridges[0].Msgs))
	}
	for i, msg := range bridges[0].Msgs {
		m := msg.(*stypes.MsgMimir)
		if m.Key != wantKeys[i] || m.Value != wantVals[i] {
			t.Fatalf("mimir %d = %s=%d", i, m.Key, m.Value)
		}
	}
}

func TestXMRKeygenJailWaitForSplitCovA(t *testing.T) {
	api := newCovThornode(t)
	a := newCovAKeygenJail()
	a.vaultsBefore = map[string]bool{"old": true}

	covAAssertPending(t, "vaults error", a.waitForKeygenJailSplit(nil))

	oldXMR := openapi.Vault{PubKey: covAStr("old"), Addresses: []openapi.VaultAddress{{Chain: "XMR", Address: "old-xmr"}}}
	newBTC := openapi.Vault{PubKey: covAStr("new-btc"), Addresses: []openapi.VaultAddress{{Chain: "BTC", Address: "btc"}, {Chain: "XMR", Address: "  "}}}
	newXMR := openapi.Vault{PubKey: covAStr("new-xmr"), Addresses: []openapi.VaultAddress{{Chain: "BTC", Address: "btc"}, {Chain: "XMR", Address: "xmr-addr"}}}
	api.JSON(covAVaultsPath, []openapi.Vault{oldXMR, newBTC})
	covAAssertPending(t, "nodes error", a.waitForKeygenJailSplit(nil))

	reason := xmrKeygenJailReason
	other := "other"
	jailedNode := func(addr string, r *string) openapi.Node {
		return openapi.Node{NodeAddress: addr, Jail: openapi.NodeJail{Reason: r}}
	}

	// Neither half yet.
	api.JSON(covANodesPath, []openapi.Node{jailedNode("a", &other), jailedNode("b", nil)})
	covAAssertPending(t, "nothing", a.waitForKeygenJailSplit(nil))
	if !a.jailSeenAt.IsZero() {
		t.Fatal("jail grace started without a keygen jail")
	}

	// Jail without the vault starts the grace window, then fails after it.
	api.JSON(covANodesPath, []openapi.Node{jailedNode("a", &reason)})
	covAAssertPending(t, "jail only", a.waitForKeygenJailSplit(nil))
	if a.jailSeenAt.IsZero() {
		t.Fatal("jail grace window not started")
	}
	covAAssertPending(t, "within grace", a.waitForKeygenJailSplit(nil))
	a.jailSeenAt = time.Now().Add(-xmrKeygenJailVaultGrace - time.Minute)
	covAAssertFatal(t, "grace expired", a.waitForKeygenJailSplit(nil), "no new XMR vault activated")

	// Vault without jail keeps waiting.
	a.jailSeenAt = time.Time{}
	api.JSON(covAVaultsPath, []openapi.Vault{oldXMR, newBTC, newXMR})
	api.JSON(covANodesPath, []openapi.Node{jailedNode("a", nil)})
	covAAssertPending(t, "vault only", a.waitForKeygenJailSplit(nil))

	// Both halves (with multiple victims logged) pass.
	api.JSON(covANodesPath, []openapi.Node{jailedNode("a", &reason), jailedNode("b", &reason)})
	covAAssertContinue(t, "split", a.waitForKeygenJailSplit(nil))
	api.JSON(covANodesPath, []openapi.Node{jailedNode("a", &reason)})
	covAAssertContinue(t, "single victim", a.waitForKeygenJailSplit(nil))
}
