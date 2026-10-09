package core

import (
	"encoding/json"
	"errors"
	"fmt"
	rand "math/rand/v2"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// fork D helpers
////////////////////////////////////////////////////////////////////////////////////////

const (
	covDPathNodes   = "/thorchain/nodes"
	covDPathVaults  = "/thorchain/vaults/asgard"
	covDPathMimir   = "/thorchain/mimir"
	covDPathNetwork = "/thorchain/network"
	covDPathInbound = "/thorchain/inbound_addresses"
	covDPathPools   = "/thorchain/pools"
	covDActive      = "Active"
	covDStandby     = "Standby"
	covDActiveVault = "ActiveVault"
	covDRetiringVlt = "RetiringVault"
	covDPeerID      = "peer"
	covDValidatorIP = "172.19.0.2"
)

func covDRNG() *rand.Rand { return rand.New(rand.NewPCG(7, 9)) }

func covDThorAddr(t *testing.T, u *User) string {
	t.Helper()
	addr, err := u.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		t.Fatal(err)
	}
	return addr.String()
}

// covDNode returns a thornode API node entry for the user with the given status.
func covDNode(t *testing.T, u *User, status string) map[string]interface{} {
	t.Helper()
	return map[string]interface{}{
		"node_address": covDThorAddr(t, u),
		"status":       status,
		"peer_id":      covDPeerID,
		"ip_address":   covDValidatorIP,
		"pub_key_set":  map[string]string{"secp256k1": u.PubKey(common.THORChain).String()},
	}
}

func covDNodes(t *testing.T, users []*User, statuses ...string) []map[string]interface{} {
	t.Helper()
	nodes := make([]map[string]interface{}, 0, len(users))
	for i, u := range users {
		nodes = append(nodes, covDNode(t, u, statuses[i]))
	}
	return nodes
}

// covDReadyEnv points the XMR FROST churn readiness gate at the fake API: all
// bifrost/frost health endpoints and the monero daemon RPC are served by it.
func covDReadyEnv(t *testing.T, api *covThornode, moneroHeight int64) {
	t.Helper()
	t.Setenv("SIMULATION_BIFROST_READINESS_ENDPOINTS", api.URL()+"/p2pid")
	t.Setenv("SIMULATION_XMR_FROST_READINESS_ENDPOINTS", api.URL()+"/health")
	t.Setenv("SIMULATION_XMR_DAEMON_RPC", api.URL()+"/json_rpc")
	api.JSON("/p2pid", "ok").JSON("/health", "ok")
	api.JSON("/json_rpc", fmt.Sprintf(`{"result":{"count":%d}}`, moneroHeight))
}

func covDInbound(chain common.Chain, address, router, pubkey string) map[string]interface{} {
	m := map[string]interface{}{"chain": chain.String(), "address": address, "halted": false}
	if router != "" {
		m["router"] = router
	}
	if pubkey != "" {
		m["pub_key"] = pubkey
	}
	return m
}

func covDMimirMsgs(t *testing.T, b *covBridge) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, msg := range b.Msgs {
		m, ok := msg.(*stypes.MsgMimir)
		if !ok {
			t.Fatalf("unexpected msg %T", msg)
		}
		out[strings.ToUpper(m.Key)] = m.Value
	}
	return out
}

func covDFailAfter(n int) func(...sdk.Msg) (common.TxID, error) {
	count := 0
	return func(...sdk.Msg) (common.TxID, error) {
		count++
		if count > n {
			return "", errors.New("broadcast rejected")
		}
		return common.TxID(fmt.Sprintf("%064d", count)), nil
	}
}

var errBoomCovD = errors.New("boom")

func covDDecodeVault(t *testing.T, v interface{}) openapi.Vault {
	t.Helper()
	var vault openapi.Vault
	if err := json.Unmarshal([]byte(covMarshal(t, v)), &vault); err != nil {
		t.Fatal(err)
	}
	return vault
}

func covDNoop() *zerolog.Logger {
	l := zerolog.Nop()
	return &l
}

////////////////////////////////////////////////////////////////////////////////////////
// ScheduleChurn
////////////////////////////////////////////////////////////////////////////////////////

func TestScheduleChurnErrorsCovD(t *testing.T) {
	api := newCovThornode(t)
	active, _ := newCovUser(t, 12, nil)
	standby, _ := newCovUser(t, 13, nil)
	cfg := &OpConfig{NodeUsers: []*User{active, standby}}

	if _, err := ScheduleChurn(covDNoop(), cfg); err == nil || !strings.Contains(err.Error(), "get nodes") {
		t.Fatalf("expected node lookup failure, got %v", err)
	}

	api.JSON(covDPathNodes, covDNodes(t, []*User{active, standby}, covDStandby, covDStandby))
	if _, err := ScheduleChurn(covDNoop(), cfg); err == nil || !strings.Contains(err.Error(), "no simulation node is active") {
		t.Fatalf("expected no active voter, got %v", err)
	}

	api.JSON(covDPathNodes, covDNodes(t, []*User{active, standby}, covDActive, covDStandby))
	active.Thorchain.(*covBridge).HeightFn = func() (int64, error) { return 0, errors.New("no height") }
	if _, err := ScheduleChurn(covDNoop(), cfg); err == nil || !strings.Contains(err.Error(), "block height") {
		t.Fatalf("expected height failure, got %v", err)
	}
	active.Thorchain.(*covBridge).HeightFn = nil

	if _, err := ScheduleChurn(covDNoop(), cfg); err == nil || !strings.Contains(err.Error(), "get vaults") {
		t.Fatalf("expected vault failure, got %v", err)
	}

	api.JSON(covDPathVaults, []map[string]interface{}{{"status": covDRetiringVlt, "status_since": 5}})
	if _, err := ScheduleChurn(covDNoop(), cfg); err == nil || !strings.Contains(err.Error(), "no active vault") {
		t.Fatalf("expected missing active vault, got %v", err)
	}

	api.JSON(covDPathVaults, []map[string]interface{}{{"status": covDActiveVault, "status_since": 50}})
	active.Thorchain.(*covBridge).BroadcastFn = covDFailAfter(0)
	if _, err := ScheduleChurn(covDNoop(), cfg); err == nil || !strings.Contains(err.Error(), "broadcast ChurnInterval=58") {
		t.Fatalf("expected broadcast failure, got %v", err)
	}
	if standby.Thorchain.(*covBridge).MsgCount() != 0 {
		t.Fatal("standby node must not vote")
	}
}

func TestScheduleChurnVotesFromActiveNodesCovD(t *testing.T) {
	api := newCovThornode(t)
	dog, dogBridge := newCovUser(t, 12, nil)
	cat, catBridge := newCovUser(t, 13, nil)
	fox, foxBridge := newCovUser(t, 14, nil)
	cfg := &OpConfig{NodeUsers: []*User{dog, cat, fox}}
	api.JSON(covDPathNodes, covDNodes(t, []*User{dog, cat, fox}, covDActive, covDActive, covDStandby))
	api.JSON(covDPathVaults, []map[string]interface{}{
		{"status": covDActiveVault, "status_since": 40},
		{"status": covDActiveVault, "status_since": 50},
	})
	// current height 100, last churn 50 -> interval 58, scheduled 108
	api.JSON(covDPathMimir, map[string]int64{"CHURNINTERVAL": 58})

	height, err := ScheduleChurn(covDNoop(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if height != 108 {
		t.Fatalf("scheduled height = %d, want 108", height)
	}
	for _, b := range []*covBridge{dogBridge, catBridge} {
		if got := covDMimirMsgs(t, b)["CHURNINTERVAL"]; got != 58 {
			t.Fatalf("active voter sent CHURNINTERVAL=%d", got)
		}
	}
	if foxBridge.MsgCount() != 0 {
		t.Fatal("standby node voted")
	}
	if api.Hits(covDPathMimir) != 1 {
		t.Fatalf("expected the applied mimir to be seen on the first poll, got %d polls", api.Hits(covDPathMimir))
	}
}

func TestWaitChurnIntervalAppliedStopsAtScheduledHeightCovD(t *testing.T) {
	api := newCovThornode(t)
	node, bridge := newCovUser(t, 12, nil)
	api.Status(covDPathMimir, http.StatusBadRequest, "nope")
	calls := 0
	bridge.HeightFn = func() (int64, error) {
		calls++
		return 120, nil
	}
	waitChurnIntervalApplied(covDNoop(), node, 58, 108)
	if calls != 1 || api.Hits(covDPathMimir) != 1 {
		t.Fatalf("expected a single poll once the scheduled height passed, got height=%d mimir=%d", calls, api.Hits(covDPathMimir))
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// ChurnActor
////////////////////////////////////////////////////////////////////////////////////////

func TestChurnActorConstructionCovD(t *testing.T) {
	a := NewChurnActor(covDRNG())
	if a.Name != "Churn" || len(a.Ops) != 2 || a.Timeout.Minutes() != 35 {
		t.Fatalf("unexpected churn actor %+v", a)
	}
	skip := NewChurnActorWithSkip(covDRNG(), func() string { return "" })
	if len(skip.Ops) != 2 {
		t.Fatal("expected two ops")
	}
}

func TestChurnActorStartChurnCovD(t *testing.T) {
	api := newCovThornode(t)
	covDReadyEnv(t, api, 42)
	dog, dogBridge := newCovUser(t, 12, nil)
	cat, _ := newCovUser(t, 13, nil)
	cfg := &OpConfig{NodeUsers: []*User{dog, cat}}
	a := &ChurnActor{Actor: *NewActor("Churn", covDRNG()), skipReason: func() string { return "" }}

	// node lookup failure retries
	if res := a.startChurn(cfg); res.Continue || res.Finish {
		t.Fatalf("node lookup failure: %+v", res)
	}

	// too few nodes fails closed
	api.JSON(covDPathNodes, covDNodes(t, []*User{dog}, covDActive))
	if res := a.startChurn(cfg); !res.Finish || res.Error == nil {
		t.Fatalf("single node must fail: %+v", res)
	}

	// already churned
	api.JSON(covDPathNodes, covDNodes(t, []*User{dog, cat}, covDActive, covDActive))
	if res := a.startChurn(cfg); !res.Finish || res.Error == nil || !strings.Contains(res.Error.Error(), "one active node") {
		t.Fatalf("two active nodes must fail: %+v", res)
	}

	// readiness gate blocks (frost endpoint unhealthy)
	api.JSON(covDPathNodes, covDNodes(t, []*User{dog, cat}, covDActive, covDStandby))
	api.Status("/health", http.StatusServiceUnavailable, "starting")
	if res := a.startChurn(cfg); res.Continue || res.Finish {
		t.Fatalf("readiness gate must block: %+v", res)
	}
	if dogBridge.MsgCount() != 0 {
		t.Fatal("mimirs sent before readiness")
	}
	api.JSON("/health", "ok")

	// node locked
	if !dog.Acquire() {
		t.Fatal("acquire")
	}
	if res := a.startChurn(cfg); res.Continue || res.Finish {
		t.Fatalf("locked node must retry: %+v", res)
	}
	dog.Release()

	// schedule failure (no vaults)
	if res := a.startChurn(cfg); res.Continue || res.Finish {
		t.Fatalf("schedule failure must retry: %+v", res)
	}
	if dog.IsLocked() {
		t.Fatal("node lock leaked")
	}

	api.JSON(covDPathVaults, []map[string]interface{}{{"status": covDActiveVault, "status_since": 50}})
	api.JSON(covDPathMimir, map[string]int64{"CHURNINTERVAL": 58})

	// churn mimir broadcast failure (schedule vote succeeds, first churn mimir fails)
	dogBridge.BroadcastFn = covDFailAfter(1)
	if res := a.startChurn(cfg); res.Continue || res.Finish {
		t.Fatalf("mimir broadcast failure must retry: %+v", res)
	}

	dogBridge.BroadcastFn = nil
	dogBridge.Msgs = nil
	if res := a.startChurn(cfg); !res.Continue || res.Finish {
		t.Fatalf("expected churn start: %+v", res)
	}
	got := covDMimirMsgs(t, dogBridge)
	want := map[string]int64{
		"CHURNINTERVAL":            58,
		"NUMBEROFNEWNODESPERCHURN": xmrPostChurnNewNodesPerChurn,
		"CHURNRETRYINTERVAL":       xmrPostChurnRetryInterval,
		"HALTCHURNING":             0,
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("mimir %s=%d, want %d (all %v)", k, got[k], v, got)
		}
	}
}

func TestChurnActorWaitForChurnCompleteCovD(t *testing.T) {
	api := newCovThornode(t)
	dog, _ := newCovUser(t, 12, nil)
	cat, _ := newCovUser(t, 13, nil)
	a := &ChurnActor{Actor: *NewActor("Churn", covDRNG())}

	if res := a.waitForChurnComplete(nil); res.Continue || res.Finish {
		t.Fatalf("network failure: %+v", res)
	}
	api.JSON(covDPathNetwork, map[string]interface{}{"vaults_migrating": true})
	if res := a.waitForChurnComplete(nil); res.Continue || res.Finish {
		t.Fatalf("nodes failure: %+v", res)
	}
	api.JSON(covDPathNodes, covDNodes(t, []*User{dog, cat}, covDActive, covDStandby))
	if res := a.waitForChurnComplete(nil); res.Continue || res.Finish {
		t.Fatalf("churn not started: %+v", res)
	}
	api.JSON(covDPathNodes, covDNodes(t, []*User{dog, cat}, covDActive, covDActive))
	if res := a.waitForChurnComplete(nil); res.Continue || res.Finish {
		t.Fatalf("migrating must wait: %+v", res)
	}
	api.JSON(covDPathNetwork, map[string]interface{}{"vaults_migrating": false})
	if res := a.waitForChurnComplete(nil); !res.Continue || res.Finish {
		t.Fatalf("churn end must continue: %+v", res)
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// RagnarokPoolActor
////////////////////////////////////////////////////////////////////////////////////////

func TestRagnarokPoolActorCovD(t *testing.T) {
	api := newCovThornode(t)
	dog, dogBridge := newCovUser(t, 12, nil)
	cat, catBridge := newCovUser(t, 13, nil)
	cfg := &OpConfig{NodeUsers: []*User{dog, cat}}

	actor := NewRagnarokPoolActor(common.DOGEAsset, covDRNG())
	if actor.Name != "Ragnarok-DOGE.DOGE" || len(actor.Ops) != 2 {
		t.Fatalf("unexpected actor %+v", actor)
	}
	a := &RagnarokPoolActor{Actor: *NewActor("r", covDRNG()), asset: common.DOGEAsset}

	if res := a.sendMimir(cfg); res.Continue || res.Finish {
		t.Fatalf("nodes failure: %+v", res)
	}

	api.JSON(covDPathNodes, covDNodes(t, []*User{dog, cat}, covDActive, covDStandby))
	dogBridge.BroadcastFn = covDFailAfter(0)
	if res := a.sendMimir(cfg); res.Continue || res.Finish {
		t.Fatalf("broadcast failure: %+v", res)
	}

	dogBridge.BroadcastFn = nil
	dogBridge.Msgs = nil
	if res := a.sendMimir(cfg); !res.Continue {
		t.Fatalf("expected continue: %+v", res)
	}
	if got := covDMimirMsgs(t, dogBridge); got["RAGNAROK-DOGE-DOGE"] != 1 || len(got) != 1 {
		t.Fatalf("unexpected ragnarok mimirs %v", got)
	}
	if catBridge.MsgCount() != 0 {
		t.Fatal("inactive node must be skipped")
	}

	// verifyPoolRemoval
	if res := a.verifyPoolRemoval(cfg); res.Continue || res.Finish {
		t.Fatalf("pools failure: %+v", res)
	}
	api.JSON(covDPathPools, []map[string]string{{"asset": "BTC.BTC"}, {"asset": "DOGE.DOGE"}})
	if res := a.verifyPoolRemoval(cfg); res.Continue || res.Finish {
		t.Fatalf("pool still present: %+v", res)
	}
	api.JSON(covDPathPools, []map[string]string{{"asset": "BTC.BTC"}})
	if res := a.verifyPoolRemoval(cfg); !res.Finish || res.Error != nil {
		t.Fatalf("pool removed must finish: %+v", res)
	}
}
