package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/util"
	"gitlab.com/thorchain/thornode/v3/tools/thorscan"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Test Harness
////////////////////////////////////////////////////////////////////////////////////////

// infoCovEnv is a hermetic environment: a fake thornode API, isolated storage,
// console notifications and captured logs.
type infoCovEnv struct {
	t      *testing.T
	srv    *httptest.Server
	logBuf *bytes.Buffer

	mu        sync.Mutex
	responses map[string]any
	requests  []string
}

// set registers a JSON response for a path ("thorchain/x") optionally pinned
// to a height ("thorchain/x?height=5").
func (e *infoCovEnv) set(key string, value any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.responses[key] = value
}

func (e *infoCovEnv) requested() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.requests...)
}

func newInfoCovEnv(t *testing.T) *infoCovEnv {
	t.Helper()
	e := &infoCovEnv{t: t, responses: map[string]any{}, logBuf: &bytes.Buffer{}}

	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		heightKey := path
		if h := r.URL.Query().Get("height"); h != "" {
			heightKey = path + "?height=" + h
		}
		e.mu.Lock()
		e.requests = append(e.requests, heightKey)
		v, ok := e.responses[heightKey]
		if !ok {
			v, ok = e.responses[path]
		}
		e.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if fn, isFn := v.(func(*http.Request) any); isFn {
			v = fn(r)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(e.srv.Close)

	origCfg := config.Get()
	origLogger := log.Logger
	origLevel := zerolog.GlobalLevel()
	t.Cleanup(func() {
		config.Set(origCfg)
		log.Logger = origLogger
		zerolog.SetGlobalLevel(origLevel)
	})

	cfg := origCfg
	cfg.StoragePath = t.TempDir()
	cfg.Endpoints.Thornode = e.srv.URL
	cfg.Endpoints.CacheSize = 100
	cfg.MaxRetries = 0
	cfg.Console = true
	cfg.Notifications.Info = config.Webhooks{Category: "Info"}
	cfg.Notifications.Security = config.Webhooks{Category: "Security"}
	cfg.Notifications.Activity = config.Webhooks{Category: "Activity"}
	config.Set(cfg)
	util.InitCache()

	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	log.Logger = zerolog.New(e.logBuf)
	return e
}

// infoCovStdout captures everything printed to stdout (console notifications)
// while fn runs.
func infoCovStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() {
			os.Stdout = orig
			_ = w.Close()
		}()
		fn()
	}()
	return infoCovANSI.ReplaceAllString(<-done, "")
}

var infoCovANSI = regexp.MustCompile("\x1b\\[[0-9;]*m")

func infoCovBlock(height int64, txs ...thorscan.BlockTx) *thorscan.BlockResponse {
	return &thorscan.BlockResponse{
		BlockResponse: openapi.BlockResponse{
			Header: openapi.BlockResponseHeader{Height: height},
		},
		Txs: txs,
	}
}

func infoCovTx(msgs []sdk.Msg, events ...map[string]string) thorscan.BlockTx {
	code := int64(0)
	tx := thorscan.BlockTx{
		BlockTx: openapi.BlockTx{
			Hash:   "INFOCOVHASH",
			Result: openapi.BlockTxResult{Code: &code, Events: events},
		},
	}
	if msgs != nil {
		tx.Tx = keysignFailureTestTx{msgs: msgs}
	}
	return tx
}

func infoCovAddr(b byte) sdk.AccAddress {
	return sdk.AccAddress(bytes.Repeat([]byte{b}, 20))
}

////////////////////////////////////////////////////////////////////////////////////////
// Version / Mimir
////////////////////////////////////////////////////////////////////////////////////////

func TestInfoCovVersion(t *testing.T) {
	e := newInfoCovEnv(t)
	block := infoCovBlock(10)
	block.BeginBlockEvents = []map[string]string{
		{"type": "other"},
		{"type": types.VersionEventType, "version": "3.99.0"},
	}
	out := infoCovStdout(t, func() { Version(block) })
	require.Contains(t, out, "Network Version Upgraded: 3.99.0")
	require.Equal(t, 1, strings.Count(e.logBuf.String(), "sending notifications"))
}

func TestInfoCovSetMimir(t *testing.T) {
	e := newInfoCovEnv(t)
	e.set("thorchain/mimir?height=99", map[string]int64{"EXISTING": 3})

	signer := infoCovAddr(7)
	nodeMsg := &thorchain.MsgMimir{Key: "EXISTING", Value: 4, Signer: signer}

	block := infoCovBlock(100,
		// node mimir changing an existing value
		infoCovTx([]sdk.Msg{nodeMsg}, map[string]string{"type": types.SetMimirEventType, "key": "EXISTING", "value": "4"}),
		// auto mimir (no MsgMimir) setting a new key; non-mimir event ignored
		infoCovTx([]sdk.Msg{}, map[string]string{"type": "transfer"}, map[string]string{"type": types.SetMimirEventType, "key": "NEWKEY", "value": "9"}),
	)
	out := infoCovStdout(t, func() { SetMimir(block) })
	require.Contains(t, out, "Mimir EXISTING Changed: 3 -> 4 (node)")
	require.Contains(t, out, "Mimir NEWKEY Set: 9 (auto)")

	// node pause / unpause
	signerStr := signer.String()
	short := signerStr[len(signerStr)-4:]
	pause := infoCovBlock(100,
		infoCovTx([]sdk.Msg{nodeMsg}, map[string]string{"type": types.SetMimirEventType, "key": "NODEPAUSECHAINGLOBAL", "value": "150"}),
	)
	out = infoCovStdout(t, func() { SetMimir(pause) })
	require.Contains(t, out, fmt.Sprintf("Node %s Paused: 50 blocks", short))

	unpause := infoCovBlock(200,
		infoCovTx([]sdk.Msg{nodeMsg}, map[string]string{"type": types.SetMimirEventType, "key": "NODEPAUSECHAINGLOBAL", "value": "150"}),
	)
	out = infoCovStdout(t, func() { SetMimir(unpause) })
	require.Contains(t, out, fmt.Sprintf("Node %s Unpaused", short))
}

func TestInfoCovFormatHelpersPanic(t *testing.T) {
	newInfoCovEnv(t)
	nodeMsg := &thorchain.MsgMimir{Key: "NODEPAUSECHAINGLOBAL", Signer: infoCovAddr(1)}
	tx := infoCovTx([]sdk.Msg{nodeMsg})

	// unparsable pause height
	require.Panics(t, func() {
		formatNodePauseMessage(10, tx, map[string]string{"value": "abc"})
	})
	// mimir lookup failure (no response registered)
	require.Panics(t, func() { formatMimirMessage(10, "auto", "X", "1") })
	// unparsable node mimir value
	require.Panics(t, func() { formatNodeMimirMessage(10, "thorAAAA", "X", "abc") })
	// node lookup failure
	require.Panics(t, func() { formatNodeMimirMessage(10, "thorAAAA", "X", "1") })
}

func TestInfoCovSetNodeMimir(t *testing.T) {
	e := newInfoCovEnv(t)
	e.set("thorchain/nodes?height=50", []map[string]any{
		{"node_address": "thor1nodeAAAA", "status": "Active"},
		{"node_address": "thor1nodeBBBB", "status": "Active"},
		{"node_address": "thor1nodeCCCC", "status": "Active"},
		{"node_address": "thor1nodeEEEE", "status": "Active"},
		{"node_address": "thor1nodeDDDD", "status": "Standby"},
	})
	e.set("thorchain/mimir/nodes_all?height=49", map[string]any{
		"mimirs": []map[string]any{
			{"key": "HALTX", "value": 5, "signer": "thor1nodeAAAA"}, // previous vote of the node
			{"key": "HALTX", "value": 7, "signer": "thor1nodeBBBB"}, // counted
			{"key": "HALTX", "value": 7, "signer": "thor1nodeEEEE"}, // counted (same value)
			{"key": "HALTX", "signer": "thor1nodeCCCC"},             // missing value counts as 0
			{"key": "HALTX", "value": 9, "signer": "thor1nodeDDDD"}, // non-active skipped
			{"key": "OTHER", "value": 1, "signer": "thor1nodeBBBB"}, // other key skipped
			{"value": 1, "signer": "thor1nodeBBBB"},                 // nil key skipped
			{"key": "HALTX", "value": 1},                            // nil signer skipped
		},
	})

	block := infoCovBlock(50, infoCovTx(nil,
		map[string]string{"type": "noise"},
		map[string]string{"type": types.SetNodeMimirEventType, "address": "thor1nodeAAAA", "key": "HALTX", "value": "7"},
	))
	out := infoCovStdout(t, func() { SetNodeMimir(block) })
	require.Contains(t, out, "Node Mimir: HALTX")
	require.Contains(t, out, "AAAA")
	require.Contains(t, out, "7 (change from 5)")
	require.Contains(t, out, "7: 3/4 (75.0%)")
	require.Contains(t, out, "0: 1/4 (25.0%)")
	require.NotContains(t, out, "9")

	// a first-time vote has no previous value
	title, fields := formatNodeMimirMessage(50, "thor1nodeZZZZ", "HALTX", "1")
	require.Equal(t, "Node Mimir: `HALTX`", title)
	v, _ := fields.Get("Value")
	require.Equal(t, "1", v)
}

////////////////////////////////////////////////////////////////////////////////////////
// Keygen Failure
////////////////////////////////////////////////////////////////////////////////////////

func TestInfoCovKeygenFailure(t *testing.T) {
	e := newInfoCovEnv(t)
	e.set("thorchain/nodes?height=77", []map[string]any{
		{"node_address": "thor1nodeAAAA", "node_operator_address": "thor1opr1111", "pub_key_set": map[string]any{"secp256k1": "pkA"}},
		{"node_address": "thor1nodeBBBB", "node_operator_address": "thor1opr2222", "pub_key_set": map[string]any{"secp256k1": "pkB"}},
		{"node_address": "thor1nodeCCCC", "node_operator_address": "thor1opr3333", "pub_key_set": map[string]any{"secp256k1": "pkC"}},
		{"node_address": "thor1nodeNOPK", "node_operator_address": "thor1opr4444"},
	})

	tssPool := &thorchain.MsgTssPool{PubKeys: []string{"pkA", "pkB", "pkC"}}
	event := map[string]string{
		"type":   types.TSSKeygenFailure,
		"height": "77",
		"reason": "timeout",
		"blame":  "thor1nodeAAAA, ,",
	}
	block := infoCovBlock(80,
		infoCovTx(nil, event), // failed decode tx is skipped
		infoCovTx([]sdk.Msg{tssPool}, map[string]string{"type": "noise"}, event),
	)
	out := infoCovStdout(t, func() { KeygenFailure(block) })
	require.Equal(t, 1, strings.Count(e.logBuf.String(), "Keygen Failure"))
	require.Contains(t, out, "Keygen Height")
	require.Contains(t, out, "timeout")
	require.Contains(t, out, "1111:AAAA")
	require.Contains(t, out, "BBBB")
	require.Contains(t, out, "CCCC")

	// failure paths panic
	bad := map[string]string{"type": types.TSSKeygenFailure, "height": "x"}
	require.Panics(t, func() { KeygenFailure(infoCovBlock(80, infoCovTx([]sdk.Msg{tssPool}, bad))) })

	missingNodes := map[string]string{"type": types.TSSKeygenFailure, "height": "78"}
	require.Panics(t, func() { KeygenFailure(infoCovBlock(80, infoCovTx([]sdk.Msg{tssPool}, missingNodes))) })

	noPool := map[string]string{"type": types.TSSKeygenFailure, "height": "77"}
	require.Panics(t, func() { KeygenFailure(infoCovBlock(80, infoCovTx([]sdk.Msg{}, noPool))) })
}

////////////////////////////////////////////////////////////////////////////////////////
// Churn
////////////////////////////////////////////////////////////////////////////////////////

func TestInfoCovChurnLifecycle(t *testing.T) {
	e := newInfoCovEnv(t)

	// old (keygen height 90) and new (110) node sets
	node := func(addr, status string, extra map[string]any) map[string]any {
		m := map[string]any{"node_address": addr, "status": status, "total_bond": "1000", "status_since": 100}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	e.set("thorchain/nodes?height=90", []map[string]any{
		node("thor1nodeKEEP", "Active", nil),
		node("thor1nodeWRST", "Active", map[string]any{"leave_height": 200, "slash_points": 200, "total_bond": "500", "active_block_height": 10}),
		node("thor1nodeLBND", "Active", map[string]any{"leave_height": 200, "slash_points": 1, "total_bond": "1", "active_block_height": 20}),
		node("thor1nodeOLDS", "Active", map[string]any{"leave_height": 200, "slash_points": 1, "active_block_height": 1, "status_since": 1}),
		node("thor1nodeLEAV", "Active", map[string]any{"leave_height": 200, "active_block_height": 30, "requested_to_leave": true}),
		node("thor1nodeMISS", "Active", map[string]any{"leave_height": 200, "active_block_height": 40, "missing_blocks": 1000}),
		node("thor1nodePLAN", "Active", map[string]any{"leave_height": 200, "active_block_height": 50}),
		node("thor1nodeLEFT", "Active", map[string]any{"leave_height": 50}),
		node("thor1nodeNOLH", "Active", nil), // dropped without leave height: ignored
		node("thor1nodeSTBY", "Standby", nil),
	})
	e.set("thorchain/nodes?height=110", []map[string]any{
		node("thor1nodeKEEP", "Active", nil),
		node("thor1nodeNEWN", "Active", nil),
		node("thor1nodeWRST", "Standby", nil),
	})
	e.set("thorchain/mimir?height=90", map[string]int64{"MISSINGBLOCKCHURNOUT": 100})
	e.set("thorchain/vaults/asgard?height=110", []map[string]any{
		{"status": "ActiveVault", "pub_key": "tthorpubECDA", "pub_key_eddsa": "tthorpubEDDA", "membership": []string{"a", "b", "c", "d"}},
		{"status": "ActiveVault", "pub_key": "tthorpubNOED"},
		{"status": "RetiringVault", "pub_key": "tthorpubRETI", "pub_key_eddsa": "tthorpubRETE"},
	})
	// no pending version, so no node is labelled for a low version
	e.set("thorchain/version?height=90", map[string]any{"current": "3.0.0"})
	var migrating atomic.Bool
	migrating.Store(true)
	e.set("thorchain/network", func(*http.Request) any {
		return map[string]any{"vaults_migrating": migrating.Load()}
	})

	// block 100: keyshare backups observed and keygen started
	backup := &thorchain.MsgTssPool{
		PoolPubKey:           "tthorpubECDA",
		PoolPubKeyEddsa:      "tthorpubEDDA",
		KeysharesBackup:      []byte{1, 2},
		KeysharesBackupEddsa: []byte{1, 2},
		Signer:               infoCovAddr(3),
	}
	noBackup := &thorchain.MsgTssPool{PoolPubKey: "tthorpubECDA", Signer: infoCovAddr(4)}
	out := infoCovStdout(t, func() {
		Churn(infoCovBlock(100,
			infoCovTx(nil, map[string]string{"type": "noise"}), // failed decode
			infoCovTx([]sdk.Msg{&thorchain.MsgMimir{}, backup, noBackup}, map[string]string{"type": types.TSSKeygenMetricEventType}),
			infoCovTx([]sdk.Msg{}, map[string]string{"type": types.TSSKeygenMetricEventType}), // already in keygen
		))
	})
	require.Equal(t, 1, strings.Count(out, "Keygen Started"))
	info := ChurnInfo{}
	require.NoError(t, util.Load("churn", &info))
	require.Equal(t, ChurnStateKeygen, info.State)
	require.Len(t, info.KeyshareBackups["tthorpubECDA"], 1)
	require.Len(t, info.KeyshareBackups["tthorpubEDDA"], 1)

	// block 110: active vault -> churn started, still migrating
	activeTx := infoCovTx([]sdk.Msg{&thorchain.MsgTssPool{Height: 90}}, map[string]string{"type": thorchain.EventTypeActiveVault})
	out = infoCovStdout(t, func() { Churn(infoCovBlock(110, activeTx)) })
	require.Contains(t, out, "Churn Started")
	require.Contains(t, out, "ECDA (ECDSA): 1/4 (25.00%)")
	require.Contains(t, out, "EDDA (EDDSA): 1/4 (25.00%)")
	require.NotContains(t, out, "RETI")
	require.Contains(t, out, "NEWN")
	// LBND stays unlabelled: the mocknet active set is below DesiredValidatorSet,
	// so the lowest-bond rule does not apply
	require.Contains(t, out, "LEAV (leave), MISS (missing), OLDS (oldest), WRST (worst), LEFT (leave), LBND, PLAN")
	require.NotContains(t, out, "NOLH")
	require.NotContains(t, out, "Churn Complete")
	require.NoError(t, util.Load("churn", &info))
	require.Equal(t, ChurnStateMigrating, info.State)

	// a second active vault event while migrating does not re-notify
	out = infoCovStdout(t, func() { Churn(infoCovBlock(110, activeTx)) })
	require.NotContains(t, out, "Churn Started")

	// block 111: migration finished -> churn complete and state reset
	migrating.Store(false)
	out = infoCovStdout(t, func() { Churn(infoCovBlock(111)) })
	require.Contains(t, out, "Churn Complete")
	info = ChurnInfo{}
	require.NoError(t, util.Load("churn", &info))
	require.Equal(t, ChurnStateComplete, info.State)
	require.Empty(t, info.KeyshareBackups)

	// active vault event without a TssPool message panics
	require.Panics(t, func() {
		Churn(infoCovBlock(112, infoCovTx([]sdk.Msg{}, map[string]string{"type": thorchain.EventTypeActiveVault})))
	})
}

func TestInfoCovChurnPanics(t *testing.T) {
	newInfoCovEnv(t)
	// network lookup failure
	require.Panics(t, func() { vaultsMigrating(5) })
	// node lookups failure
	require.Panics(t, func() { notifyChurnStarted(5, 4, nil) })
}

////////////////////////////////////////////////////////////////////////////////////////
// TOR Anchor Drift
////////////////////////////////////////////////////////////////////////////////////////

func TestInfoCovIsChainHalted(t *testing.T) {
	eth := common.ETHChain
	require.False(t, isChainHalted(map[string]int64{}, eth, 100))
	require.True(t, isChainHalted(map[string]int64{"HALTCHAINGLOBAL": 10}, eth, 100))
	require.False(t, isChainHalted(map[string]int64{"HALTCHAINGLOBAL": 101}, eth, 100))
	require.True(t, isChainHalted(map[string]int64{"NODEPAUSECHAINGLOBAL": 100}, eth, 100))
	require.True(t, isChainHalted(map[string]int64{"HALTETHCHAIN": 1}, eth, 100))
	require.True(t, isChainHalted(map[string]int64{"SOLVENCYHALTETHCHAIN": 1}, eth, 100))
	require.False(t, isChainHalted(map[string]int64{"SOLVENCYHALTBTCCHAIN": 1}, eth, 100))

	require.True(t, isChainTradingHalted(map[string]int64{"HALTETHTRADING": 5}, eth, 100))
	require.False(t, isChainTradingHalted(map[string]int64{"HALTETHTRADING": 500}, eth, 100))
	require.True(t, isChainTradingHalted(map[string]int64{"HALTETHCHAIN": 5}, eth, 100))
}

func TestInfoCovTORAnchorDrift(t *testing.T) {
	e := newInfoCovEnv(t)
	cfg := config.Get()
	cfg.TORAnchorCheckBlocks = 10
	cfg.Thresholds.TORAnchorDriftBasisPoints = 500
	config.Set(cfg)

	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	require.NoError(t, err)
	usdt, err := common.NewAsset("AVAX.USDT-0X9702230A8EA53601F5CD2DC00FDBC13D4DF4A8C7")
	require.NoError(t, err)
	halted, err := common.NewAsset("BSC.USDT-0X55D398326F99059FF775485246999027B3197955")
	require.NoError(t, err)

	mimirs := map[string]int64{
		"TORANCHOR-" + usdc.MimirString():   1,
		"TORANCHOR-" + usdt.MimirString():   1,
		"TORANCHOR-" + halted.MimirString(): 1,
		"HALTBSCTRADING":                    1,
	}
	pools := []map[string]any{
		{"asset": usdc.String(), "asset_tor_price": "100000000"},
		{"asset": usdt.String(), "asset_tor_price": "90000000"},
		{"asset": halted.String(), "asset_tor_price": "10000000"}, // halted, ignored
		{"asset": "BTC.BTC", "asset_tor_price": "6000000000000"},  // not an anchor
	}
	e.set("thorchain/mimir", mimirs)
	e.set("thorchain/pools", pools)

	// off-interval heights do nothing
	out := infoCovStdout(t, func() { TORAnchorDrift(infoCovBlock(15)) })
	require.Empty(t, out)
	require.Empty(t, e.requested())

	out = infoCovStdout(t, func() { TORAnchorDrift(infoCovBlock(20)) })
	require.Contains(t, out, "TOR Anchor Drift (10.00%)")
	require.Contains(t, out, "ETH.USDC")
	require.Contains(t, out, "AVAX.USDT")
	require.NotContains(t, out, "BSC.USDT")
	// AVAX.USDT (cheaper) sorted first
	require.Less(t, strings.Index(out, "AVAX.USDT"), strings.Index(out, "ETH.USDC"))
	// shorter asset names are left-padded to align
	require.Contains(t, out, " ETH.USDC")

	// within threshold: no alert
	e.set("thorchain/pools", []map[string]any{
		{"asset": usdc.String(), "asset_tor_price": "100000000"},
		{"asset": usdt.String(), "asset_tor_price": "99000000"},
	})
	out = infoCovStdout(t, func() { TORAnchorDrift(infoCovBlock(30)) })
	require.Empty(t, out)

	// bad pool asset panics
	e.set("thorchain/pools", []map[string]any{{"asset": "", "asset_tor_price": "1"}})
	require.Panics(t, func() { TORAnchorDrift(infoCovBlock(40)) })

	// missing pools / mimir panic
	e.set("thorchain/pools?height=50", "not-a-list")
	require.Panics(t, func() { TORAnchorDrift(infoCovBlock(50)) })
	e.set("thorchain/mimir?height=60", "not-a-map")
	require.Panics(t, func() { TORAnchorDrift(infoCovBlock(60)) })
}

////////////////////////////////////////////////////////////////////////////////////////
// Upgrades
////////////////////////////////////////////////////////////////////////////////////////

func TestInfoCovUpgradeProposalAndApproval(t *testing.T) {
	e := newInfoCovEnv(t)
	e.set("thorchain/upgrade_proposal/v9", map[string]any{
		"name": "v9", "approved_percent": "66.666", "validators_to_quorum": 2,
	})
	e.set("thorchain/upgrade_proposal/v10", map[string]any{
		"name": "v10", "approved_percent": "100", "validators_to_quorum": 0,
	})
	e.set("thorchain/upgrade_proposal/v11", map[string]any{"name": "v11"})
	e.set("thorchain/upgrade_proposal/v12", map[string]any{
		"name": "v12", "approved_percent": "abc", "validators_to_quorum": 1,
	})

	block := infoCovBlock(300, infoCovTx(nil,
		map[string]string{"type": "noise"},
		map[string]string{"type": "propose_upgrade", "name": "v9", "height": "400", "info": `{\"binaries\":{\"linux\":\"x\"}}`, "thor_address": "thor1propPROP"},
		map[string]string{"type": "propose_upgrade", "name": "vbad", "height": "400", "info": "not json", "thor_address": "thor1propBADI"},
		map[string]string{"type": "approve_upgrade", "name": "v9", "thor_address": "thor1apprAPPR"},
		map[string]string{"type": "reject_upgrade", "name": "v10", "thor_address": "thor1rejeREJE"},
		map[string]string{"type": "approve_upgrade", "name": "v11", "thor_address": "thor1nostNOST"},
	))
	out := infoCovStdout(t, func() { UpgradeProposalAndApproval(block) })
	require.Contains(t, out, "Upgrade Proposed: v9")
	require.Contains(t, out, "binaries:")
	require.Contains(t, out, "linux: x")
	require.Contains(t, out, "PROP")
	require.Contains(t, out, "Upgrade Proposed: vbad")
	require.Contains(t, e.logBuf.String(), "failed to decode event info")
	require.Contains(t, out, "Upgrade Approved: v9")
	require.Contains(t, out, "66.7%")
	require.Contains(t, out, "Approvals Required")
	require.Contains(t, out, "Upgrade Rejected: v10")
	require.Contains(t, out, "100.0%")
	require.Contains(t, out, "Upgrade Approved: v11")
	require.Contains(t, out, "NOST")
	require.Equal(t, 5, strings.Count(e.logBuf.String(), "sending notifications"))

	// unparsable approval percent panics
	require.Panics(t, func() {
		UpgradeProposalAndApproval(infoCovBlock(300, infoCovTx(nil,
			map[string]string{"type": "approve_upgrade", "name": "v12", "thor_address": "thor1xxxxXXXX"},
		)))
	})
	// missing proposal panics
	require.Panics(t, func() {
		UpgradeProposalAndApproval(infoCovBlock(300, infoCovTx(nil,
			map[string]string{"type": "approve_upgrade", "name": "missing", "thor_address": "thor1xxxxXXXX"},
		)))
	})
}

////////////////////////////////////////////////////////////////////////////////////////
// TSS Keysign Failure
////////////////////////////////////////////////////////////////////////////////////////

func TestInfoCovTssKeysignFailureSnapshotAndNetworkAlert(t *testing.T) {
	e := newInfoCovEnv(t)
	cfg := config.Get()
	cfg.Thresholds.TssKeysignFailure.WindowBlocks = 10
	cfg.Thresholds.TssKeysignFailure.Sigma = 0
	cfg.Thresholds.TssKeysignFailure.MinSnapshots = 2
	cfg.Thresholds.TssKeysignFailure.MaxSnapshots = 2
	cfg.Thresholds.TssKeysignFailure.MinFailures = 0
	config.Set(cfg)

	vault := "tthorpub1vaultVLT1"
	require.NoError(t, util.Store("tss_keysign_failure", TssKeysignFailureHistory{
		VaultPastCounts:    map[string][]int{vault: {0, 0}},
		NetworkPastCounts:  []int{0, 0},
		LastSnapshotHeight: 100,
	}))

	block := infoCovBlock(110, infoCovTx(nil,
		map[string]string{"type": types.TSSKeysignFailure, "pubkey": ""}, // missing vault: skipped
		map[string]string{"type": types.TSSKeysignFailure, "pubkey": vault, "round": tssMessages.KEYSIGN9, "blame": "a, b"},
	))
	out := infoCovStdout(t, func() { TssKeysignFailure(block) })
	require.Contains(t, e.logBuf.String(), "tss_keysign_failure event missing vault pubkey")
	require.Contains(t, out, "Network High TSS Keysign Failure Rate")
	require.Contains(t, out, "Vault High TSS Keysign Failure Rate")
	require.Contains(t, out, "(terminal)")

	history := TssKeysignFailureHistory{}
	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Equal(t, []int{0, 1}, history.NetworkPastCounts, "snapshot appended and trimmed to MaxSnapshots")
	require.Equal(t, []int{0, 1}, history.VaultPastCounts[vault])
	require.Equal(t, int64(110), history.LastSnapshotHeight)
}

func TestInfoCovKeysignSummaries(t *testing.T) {
	require.Equal(t, []string{"a", "b"}, parseKeysignBlameNodes(" a, ,b ,"))
	require.Empty(t, parseKeysignBlameNodes(""))

	require.Equal(t, "abc", shortKeysignPubKey("abc"))
	require.Equal(t, "WXYZ", shortKeysignPubKey("tthorpubWXYZ"))

	require.Equal(t, "", summarizeKeysignRounds(nil))
	rounds := summarizeKeysignRounds([]TssKeysignFailureRecord{
		{Round: tssMessages.KEYSIGN5},
		{Round: ""},
		{Round: ""},
		{Round: tssMessages.KEYSIGN9},
	})
	require.Equal(t,
		"`unknown`: 2\n"+
			fmt.Sprintf("`%s`: 1\n", tssMessages.KEYSIGN5)+
			fmt.Sprintf("`%s`: 1 (terminal)", tssMessages.KEYSIGN9),
		rounds)

	require.Equal(t, "", summarizeKeysignBlame([]TssKeysignFailureRecord{{}}))
	blame := summarizeKeysignBlame([]TssKeysignFailureRecord{
		{Blame: []string{"node0001", "node0002", "node0003", "node0004", "node0005", "node0006", "node0007"}},
		{Blame: []string{"node0007"}},
	})
	lines := strings.Split(blame, "\n")
	require.Len(t, lines, 6)
	require.Equal(t, "`0007`: 2", lines[0])
	require.Equal(t, "`0001`: 1", lines[1])
	require.Equal(t, "…and 2 more", lines[5])
}

func TestInfoCovNotifyTSSKeysignFailureMinimal(t *testing.T) {
	newInfoCovEnv(t)
	out := infoCovStdout(t, func() {
		notifyTSSKeysignFailure(5, "Network Thing", "", 3, 1, nil)
	})
	require.Contains(t, out, "Network Thing")
	require.Contains(t, out, "Failures (window)")
	require.NotContains(t, out, "Vault")
	require.NotContains(t, out, "Rounds")
	require.NotContains(t, out, "Blame")
}

////////////////////////////////////////////////////////////////////////////////////////
// ScanInfo
////////////////////////////////////////////////////////////////////////////////////////

func TestInfoCovScanInfoEmptyBlock(t *testing.T) {
	e := newInfoCovEnv(t)
	cfg := config.Get()
	cfg.TORAnchorCheckBlocks = 1000
	config.Set(cfg)

	block := infoCovBlock(7)
	block.BeginBlockEvents = []map[string]string{{"type": types.VersionEventType, "version": "1.2.3"}}
	out := infoCovStdout(t, func() { ScanInfo(block) })
	require.Contains(t, out, "Network Version Upgraded: 1.2.3")
	require.Empty(t, e.requested(), "an empty block off the anchor interval needs no API calls")

	// keysign history was persisted
	history := TssKeysignFailureHistory{}
	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Equal(t, int64(7), history.LastSnapshotHeight)
}
