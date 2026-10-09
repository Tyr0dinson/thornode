package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	txIDACov = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	txIDBCov = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
)

// balancedGenesisCov builds a thorchain genesis where pool + outbound + trade +
// secured depths equal the vault depths after streaming/advanced swap queue
// adjustments. vaultBTC can be changed to introduce a discrepancy.
func balancedGenesisCov(t *testing.T, vaultBTC uint64) map[string]any {
	t.Helper()

	btcPool := types.NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceAsset = math.NewUint(1000)
	ethPool := types.NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceAsset = math.NewUint(500)

	vault := types.Vault{
		PubKey: common.PubKey(templatePubKey["pubkey_dog"]),
		Coins: common.Coins{
			common.NewCoin(common.BTCAsset, math.NewUint(vaultBTC)),
			common.NewCoin(common.ETHAsset, math.NewUint(550)),
			common.NewCoin(common.DOGEAsset, math.NewUint(12345)), // no pool, ignored
		},
	}

	swapTx := common.Tx{
		ID:    common.TxID(txIDACov),
		Coins: common.Coins{common.NewCoin(common.BTCAsset, math.NewUint(300))},
	}
	streaming := types.StreamingSwap{
		TxID:        common.TxID(txIDACov),
		Deposit:     math.NewUint(300),
		In:          math.NewUint(100),
		Out:         math.NewUint(50),
		TradeTarget: math.ZeroUint(),
	}
	otherSwap := types.MsgSwap{
		Tx:          common.Tx{ID: common.TxID(txIDBCov), Coins: common.Coins{common.NewCoin(common.BTCAsset, math.NewUint(1))}},
		TargetAsset: common.ETHAsset,
	}
	advSwap := types.MsgSwap{
		Tx:          common.Tx{ID: common.TxID(txIDBCov), Coins: common.Coins{common.NewCoin(common.BTCAsset, math.NewUint(70))}},
		TargetAsset: common.ETHAsset,
	}

	gs := &thorchain.GenesisState{}
	gs.Pools = []types.Pool{btcPool, ethPool}
	gs.Vaults = types.Vaults{vault}
	gs.TxOuts = []types.TxOut{{
		Height:  1,
		TxArray: []types.TxOutItem{{Chain: common.BTCChain, Coin: common.NewCoin(common.BTCAsset, math.NewUint(200))}},
	}}
	gs.TradeUnits = []types.TradeUnit{{Asset: common.BTCAsset.GetTradeAsset(), Units: math.NewUint(1), Depth: math.NewUint(100)}}
	gs.SecuredAssets = []types.SecuredAsset{{Asset: common.BTCAsset.GetSecuredAsset(), Depth: math.NewUint(50)}}
	gs.StreamingSwaps = []types.StreamingSwap{streaming}
	gs.SwapQueueItems = []types.MsgSwap{{Tx: swapTx, TargetAsset: common.ETHAsset}, otherSwap}
	gs.AdvSwapQueueItems = []types.MsgSwap{advSwap, {}}

	bz, err := encodingConfig.Codec.MarshalJSON(gs)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err = json.Unmarshal(bz, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// pool BTC total = 1000 pool + 200 outbound + 100 trade + 50 secured = 1350.
// vault BTC must be 1350 + 200 (streaming deposit-in) + 70 (adv queue) = 1620.
const balancedVaultBTCCov = 1620

func TestCheckExportInvariantsCov(t *testing.T) {
	wrap := func(tc map[string]any) map[string]any {
		return map[string]any{"app_state": map[string]any{"thorchain": tc}}
	}

	out := &bytes.Buffer{}
	if err := checkExportInvariants(out, wrap(balancedGenesisCov(t, balancedVaultBTCCov))); err != nil {
		t.Fatalf("balanced genesis should pass: %v\n%s", err, out.String())
	}

	out.Reset()
	err := checkExportInvariants(out, wrap(balancedGenesisCov(t, balancedVaultBTCCov-10)))
	if err == nil || err.Error() != "pool discrepancy" {
		t.Fatalf("expected pool discrepancy, got %v", err)
	}
	if !strings.Contains(out.String(), "pool has 10 more than its vaults") {
		t.Fatalf("expected pool surplus log, got %s", out.String())
	}
	if !strings.Contains(out.String(), "outbound: 200") {
		t.Fatalf("expected outbound debug log, got %s", out.String())
	}

	out.Reset()
	err = checkExportInvariants(out, wrap(balancedGenesisCov(t, balancedVaultBTCCov+7)))
	if err == nil {
		t.Fatal("expected pool discrepancy")
	}
	if !strings.Contains(out.String(), "vaults have 7 more than their pool") {
		t.Fatalf("expected vault surplus log, got %s", out.String())
	}
}

func TestDeleteEmptyHelpersCov(t *testing.T) {
	if !isEmptyJSONList(nil) || !isEmptyJSONList([]interface{}{}) {
		t.Fatal("nil and empty lists are empty")
	}
	if isEmptyJSONList([]interface{}{1}) || isEmptyJSONList("x") {
		t.Fatal("non-empty or non-list values are not empty lists")
	}

	value := map[string]interface{}{
		"spent_output_refs": []interface{}{},
		"xmr_tx_key":        "  ",
		"keep":              "x",
		"children": []interface{}{
			map[string]interface{}{"spent_output_refs": nil, "xmr_tx_key": "abc"},
			map[string]interface{}{"spent_output_refs": []interface{}{"ref"}},
		},
	}
	deleteEmptyXMROutboundMetadata(value)
	want := map[string]interface{}{
		"keep": "x",
		"children": []interface{}{
			map[string]interface{}{"xmr_tx_key": "abc"},
			map[string]interface{}{"spent_output_refs": []interface{}{"ref"}},
		},
	}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("unexpected xmr cleanup: %#v", value)
	}

	tc := map[string]any{
		"pol_reserve_deposits":   []interface{}{},
		"stable_reserve_credits": nil,
		"vaults": []interface{}{
			map[string]interface{}{"deficit": nil, "pub_key": "a"},
			map[string]interface{}{"deficit": "5", "pub_key": "b"},
		},
	}
	deleteEmptyPOLReserveDeposits(tc)
	deleteEmptyStableReserveCredits(tc)
	deleteEmptyVaultDeficits(tc)
	wantTC := map[string]any{
		"vaults": []interface{}{
			map[string]interface{}{"pub_key": "a"},
			map[string]interface{}{"deficit": "5", "pub_key": "b"},
		},
	}
	if !reflect.DeepEqual(tc, wantTC) {
		t.Fatalf("unexpected thorchain cleanup: %#v", tc)
	}

	// non-empty values are kept
	tc = map[string]any{
		"pol_reserve_deposits":   []interface{}{"x"},
		"stable_reserve_credits": []interface{}{"y"},
	}
	deleteEmptyPOLReserveDeposits(tc)
	deleteEmptyStableReserveCredits(tc)
	if len(tc) != 2 {
		t.Fatalf("non-empty lists must be kept: %#v", tc)
	}

	blocks := []openapi.BlockResponse{{Txs: []openapi.BlockTx{{Tx: map[string]interface{}{"xmr_tx_key": ""}}}}}
	deleteEmptyBlockXMROutboundMetadata(blocks)
	if _, ok := blocks[0].Txs[0].Tx["xmr_tx_key"]; ok {
		t.Fatal("expected empty xmr_tx_key to be removed from block tx")
	}
}

// fakeThornodeCov installs a fake `thornode` binary at the front of PATH that
// prints the file named by FAKE_THORNODE_EXPORT for `thornode export`.
func fakeThornodeCov(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  export) exec cat \"$FAKE_THORNODE_EXPORT\" ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "thornode"), []byte(script), 0o700); err != nil { //nolint:gosec // executable test fixture
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

func writeExportCov(t *testing.T, path string, thorchainState map[string]any) {
	t.Helper()
	tc := map[string]any{}
	for k, v := range thorchainState {
		tc[k] = v
	}
	tc["store_version"] = "999"
	tc["pol_reserve_deposits"] = []any{}
	tc["stable_reserve_credits"] = []any{}
	tc["node_accounts"] = []any{map[string]any{"node_address": templateAddress["addr_thor_dog"], "version": "3.0.0"}}
	exp := map[string]any{
		"app_version":  "3.0.0",
		"genesis_time": "now",
		"app_state":    map[string]any{"thorchain": tc},
	}
	bz, err := json.Marshal(exp)
	if err != nil {
		t.Fatal(err)
	}
	writeFileCov(t, path, string(bz))
}

func writeBlockCov(t *testing.T, dir string, height int64, memo string) {
	t.Helper()
	block := openapi.BlockResponse{
		Header: openapi.BlockResponseHeader{Height: height, ChainId: "thorchain"},
		Txs: []openapi.BlockTx{{
			Hash: "HASH",
			Tx:   map[string]interface{}{"memo": memo, "spent_output_refs": []interface{}{}},
		}},
	}
	bz, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	writeFileCov(t, filepath.Join(dir, strings.Repeat("0", 2)+string(rune('0'+height))+".json"), string(bz))
}

func TestExportCov(t *testing.T) {
	mnt := t.TempDir()
	t.Setenv("REGTEST_MNT_DIR", mnt)
	t.Setenv("REGTEST_HOME_BASE", t.TempDir())
	t.Setenv("EXPORT", "")
	fakeThornodeCov(t)

	exportSrc := filepath.Join(t.TempDir(), "export.json")
	t.Setenv("FAKE_THORNODE_EXPORT", exportSrc)

	suite := "suites/group/my-test.yaml"
	blocksDir := filepath.Join(mnt, "blocks", "group", "my-test")
	writeBlockCov(t, blocksDir, 1, "a")
	writeBlockCov(t, blocksDir, 2, "b")

	balanced := balancedGenesisCov(t, balancedVaultBTCCov)
	writeExportCov(t, exportSrc, balanced)

	// first export writes the blocks and state exports
	out := &bytes.Buffer{}
	if err := export(out, suite, 0, false); err != nil {
		t.Fatalf("first export failed: %v\n%s", err, out.String())
	}

	blocksPath := filepath.Join(mnt, "blocks", "group", "my-test.json")
	var blocks []openapi.BlockResponse
	bz, err := os.ReadFile(blocksPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(bz, &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || blocks[0].Header.Height != 1 || blocks[1].Txs[0].Tx["memo"] != "b" {
		t.Fatalf("unexpected blocks export: %s", string(bz))
	}
	if _, ok := blocks[0].Txs[0].Tx["spent_output_refs"]; ok {
		t.Fatal("empty spent_output_refs should be removed from block exports")
	}

	exportPath := filepath.Join(mnt, "exports", "group", "my-test.json")
	written := map[string]any{}
	bz, err = os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(bz, &written); err != nil {
		t.Fatal(err)
	}
	if _, ok := written["app_version"]; ok {
		t.Fatal("app_version should be stripped")
	}
	if _, ok := written["genesis_time"]; ok {
		t.Fatal("genesis_time should be stripped")
	}
	tc := mapCov(t, mapCov(t, written["app_state"])["thorchain"])
	for _, k := range []string{"store_version", "pol_reserve_deposits", "stable_reserve_credits"} {
		if _, ok := tc[k]; ok {
			t.Fatalf("%s should be stripped", k)
		}
	}
	if _, ok := mapCov(t, asCov[[]any](t, tc["node_accounts"])[0])["version"]; ok {
		t.Fatal("node account version should be stripped")
	}

	// second export with identical data matches existing exports
	out.Reset()
	if err = export(out, suite, 0, false); err != nil {
		t.Fatalf("matching export failed: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "State export matches expected") ||
		!strings.Contains(out.String(), "Blocks export matches expected") {
		t.Fatalf("expected match logs, got %s", out.String())
	}

	// changed state is reported
	changed := balancedGenesisCov(t, balancedVaultBTCCov)
	changed["reserve"] = "123"
	writeExportCov(t, exportSrc, changed)
	out.Reset()
	if err = export(out, suite, 0, false); err == nil || err.Error() != "exports differ" {
		t.Fatalf("expected exports differ, got %v", err)
	}

	// broken export invariants are returned (with export diff logged) unless allowed
	writeExportCov(t, exportSrc, balancedGenesisCov(t, balancedVaultBTCCov+1))
	out.Reset()
	if err = export(out, suite, 0, false); err == nil || err.Error() != "pool discrepancy" {
		t.Fatalf("expected pool discrepancy, got %v", err)
	}
	if !strings.Contains(out.String(), "exports differ") {
		t.Fatalf("expected export diff to be logged, got %s", out.String())
	}

	// with fail-export-invariants and EXPORT set the export is overwritten
	t.Setenv("EXPORT", "1")
	out.Reset()
	if err = export(out, suite, 0, true); err != nil {
		t.Fatalf("expected EXPORT overwrite to succeed: %v", err)
	}
	t.Setenv("EXPORT", "")
	out.Reset()
	if err = export(out, suite, 0, true); err != nil {
		t.Fatalf("expected overwritten export to match: %v\n%s", err, out.String())
	}

	// changed blocks are reported before the state export is checked
	writeBlockCov(t, blocksDir, 3, "c")
	out.Reset()
	if err = export(out, suite, 0, true); err == nil || err.Error() != "blocks differ" {
		t.Fatalf("expected blocks differ, got %v", err)
	}
}

func TestCheckExportChangesUndecodableCov(t *testing.T) {
	// an undecodable existing export is logged and then reported as a difference
	path := filepath.Join(t.TempDir(), "bad.json")
	writeFileCov(t, path, "not json")
	out := &bytes.Buffer{}
	err := checkExportChanges(out, map[string]any{"a": 1.0}, path)
	if err == nil || err.Error() != "exports differ" {
		t.Fatalf("expected exports differ, got %v", err)
	}
	if !strings.Contains(out.String(), "failed to decode existing export") {
		t.Fatalf("expected decode error log, got %s", out.String())
	}

	blocksPath := filepath.Join(t.TempDir(), "bad-blocks.json")
	writeFileCov(t, blocksPath, "not json")
	out.Reset()
	err = checkBlocksChanges(out, []openapi.BlockResponse{{}}, blocksPath)
	if err == nil || err.Error() != "blocks differ" {
		t.Fatalf("expected blocks differ, got %v", err)
	}
	if !strings.Contains(out.String(), "failed to decode existing blocks") {
		t.Fatalf("expected decode error log, got %s", out.String())
	}
}
