package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/rs/zerolog"
)

func writeFileCov(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRegtestEnvHelpersCov(t *testing.T) {
	t.Setenv("REGTEST_MNT_DIR", "")
	if got := regtestMntDir(); got != "/mnt" {
		t.Fatalf("default mnt dir: %s", got)
	}
	t.Setenv("REGTEST_MNT_DIR", "/tmp/foo")
	if got := regtestMntDir(); got != "/tmp/foo" {
		t.Fatalf("override mnt dir: %s", got)
	}

	t.Setenv("REGTEST_CREATE_BLOCK_PORT_BASE", "")
	if got := regtestCreateBlockPort(3); got != 8083 {
		t.Fatalf("default create block port: %d", got)
	}
	t.Setenv("REGTEST_CREATE_BLOCK_PORT_BASE", "9000")
	if got := regtestCreateBlockPort(3); got != 9003 {
		t.Fatalf("override create block port: %d", got)
	}
	t.Setenv("REGTEST_CREATE_BLOCK_PORT_BASE", "garbage")
	if got := regtestCreateBlockPort(3); got != 8083 {
		t.Fatalf("invalid base must fall back to default: %d", got)
	}

	t.Setenv("REGTEST_HOME_BASE", "")
	if got := regtestHome(2); got != "/2" {
		t.Fatalf("default home: %s", got)
	}
	t.Setenv("REGTEST_HOME_BASE", "/base")
	if got := regtestHome(2); got != "/base/2" {
		t.Fatalf("override home: %s", got)
	}

	t.Setenv("TIME_FACTOR", "")
	if got := getTimeFactor(); got != 1 {
		t.Fatalf("default time factor: %d", got)
	}
	t.Setenv("TIME_FACTOR", "4")
	if got := getTimeFactor(); got != 4 {
		t.Fatalf("time factor: %d", got)
	}
}

func TestDeepMergeCov(t *testing.T) {
	a := map[string]any{
		"keep":   "a",
		"scalar": "old",
		"nested": map[string]any{"x": 1, "y": 2},
		"list": []any{
			map[string]any{"address": "a1", "amount": "1", "other": "keep"},
			map[string]any{"address": "a2", "amount": "2"},
			"not-a-map",
		},
		"replaced_list": "string-not-list",
		"replaced_map":  "string-not-map",
	}
	b := map[string]any{
		"scalar": "new",
		"nested": map[string]any{"y": 3, "z": 4},
		"list": []any{
			"also-not-a-map",
			map[string]any{"address": "a1", "amount": "10"},
			map[string]any{"address": "a3", "amount": "3"},
			map[string]any{"no_key": true},
		},
		"replaced_list": []any{1},
		"replaced_map":  map[string]any{"k": "v"},
		"new_list":      []any{"n"},
	}

	got := deepMerge(a, b, "address", "denom")

	want := map[string]any{
		"keep":   "a",
		"scalar": "new",
		"nested": map[string]any{"x": 1, "y": 3, "z": 4},
		"list": []any{
			map[string]any{"address": "a1", "amount": "10", "other": "keep"},
			map[string]any{"address": "a2", "amount": "2"},
			"not-a-map",
			"also-not-a-map",
			map[string]any{"address": "a3", "amount": "3"},
			map[string]any{"no_key": true},
		},
		"replaced_list": []any{1},
		"replaced_map":  map[string]any{"k": "v"},
		"new_list":      []any{"n"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected merge result:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestDumpAndDrainLogsCov(t *testing.T) {
	logs := make(chan string, 5)
	logs <- "one"
	logs <- "two"
	out := &bytes.Buffer{}
	dumpLogs(out, logs)
	if out.String() != "one\ntwo\n" {
		t.Fatalf("unexpected dumped logs: %q", out.String())
	}
	if len(logs) != 0 {
		t.Fatalf("expected logs to be consumed")
	}

	// DEBUG set: drainLogs must leave the logs untouched
	logs <- "kept"
	t.Setenv("DEBUG", "1")
	drainLogs(logs)
	if len(logs) != 1 {
		t.Fatalf("drainLogs must skip draining in DEBUG mode")
	}

	// DEBUG unset: drainLogs consumes everything
	t.Setenv("DEBUG", "")
	logs <- "more"
	start := time.Now()
	drainLogs(logs)
	if len(logs) != 0 {
		t.Fatalf("expected logs to be drained, %d left", len(logs))
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("drainLogs took too long")
	}
}

// TestProcessRunningCov documents that processRunning always reports false: it
// signals with os.Signal(nil), which os.Process.Signal rejects as an unsupported
// signal type, even for a live process.
func TestProcessRunningCov(t *testing.T) {
	if processRunning(os.Getpid()) {
		t.Fatal("processRunning unexpectedly reported the test process as running")
	}

	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	if processRunning(cmd.Process.Pid) {
		t.Fatal("processRunning unexpectedly reported a live child as running")
	}
}

func TestConsoleLoggerCov(t *testing.T) {
	out := &bytes.Buffer{}
	l := consoleLogger(out)
	l.Info().Msg("hello-console")
	if !strings.Contains(out.String(), "hello-console") {
		t.Fatalf("expected log line, got %q", out.String())
	}
}

func TestPathExistsCov(t *testing.T) {
	dir := t.TempDir()
	if !pathExists(dir) {
		t.Fatal("dir should exist")
	}
	if pathExists(filepath.Join(dir, "missing")) {
		t.Fatal("missing path should not exist")
	}
	// a path below a regular file yields ENOTDIR, which is not IsNotExist
	f := filepath.Join(dir, "file")
	writeFileCov(t, f, "x")
	if pathExists(filepath.Join(f, "child")) {
		t.Fatal("child of a file should not exist")
	}
}

func TestBlockCountCov(t *testing.T) {
	ops := []Operation{
		&OpCreateBlocks{Count: 2},
		&OpEnv{},
		&OpCreateBlocks{Count: 3},
		&OpCheck{},
	}
	if got := blockCount(ops); got != 5 {
		t.Fatalf("expected 5 blocks, got %d", got)
	}
	if got := blockCount(nil); got != 0 {
		t.Fatalf("expected 0 blocks, got %d", got)
	}
}

const parseOpsSuiteCov = `type: env
key: FOO
value: bar
---
type: env
key: BAZ
value: qux
---
type: state
genesis:
  app_state:
    bank:
      balances:
        - address: {{ addr_thor_dog }}
---
type: create-blocks
count: 2
---
type: check
endpoint: http://localhost:1317/thorchain/pool/${ASSET=BTC.BTC}
asserts:
  - .asset == "${ASSET}"
  - .txid == "{{ observe_txid 1 }}"
---
type: create-blocks
count: 1
skip_invariants: true
---
type: fail-export-invariants
`

func TestParseOpsCov(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suite.yaml")
	writeFileCov(t, path, parseOpsSuiteCov)

	tmpls := template.Must(templates.Clone())
	ops, opLines, env, failExport := parseOps(zerolog.New(io.Discard), path, tmpls, []string{"A=B"})

	if !failExport {
		t.Fatal("expected fail-export-invariants to be extracted")
	}
	if !reflect.DeepEqual(env, []string{"A=B", "FOO=bar", "BAZ=qux"}) {
		t.Fatalf("unexpected env: %v", env)
	}
	if len(ops) != 4 {
		t.Fatalf("expected 4 ops, got %d", len(ops))
	}
	wantTypes := []string{"state", "create-blocks", "check", "create-blocks"}
	for i, op := range ops {
		if op.OpType() != wantTypes[i] {
			t.Fatalf("op %d: expected %s, got %s", i, wantTypes[i], op.OpType())
		}
	}

	// line numbers are the first line of each non-env operation
	if !reflect.DeepEqual(opLines, []int{9, 16, 19, 25, 29}) {
		t.Fatalf("unexpected op lines: %v", opLines)
	}

	state := asCov[*OpState](t, ops[0])
	bal := mapCov(t, asCov[[]any](t, mapCov(t, mapCov(t, state.Genesis["app_state"])["bank"])["balances"])[0])
	if bal["address"] != templateAddress["addr_thor_dog"] {
		t.Fatalf("template address not rendered: %v", bal["address"])
	}

	if cb := asCov[*OpCreateBlocks](t, ops[1]); cb.Count != 2 || cb.SkipInvariants {
		t.Fatalf("unexpected create-blocks op: %+v", cb)
	}
	if cb := asCov[*OpCreateBlocks](t, ops[3]); cb.Count != 1 || !cb.SkipInvariants {
		t.Fatalf("unexpected create-blocks op: %+v", cb)
	}

	check := asCov[*OpCheck](t, ops[2])
	if check.Endpoint != "http://localhost:1317/thorchain/pool/BTC.BTC" {
		t.Fatalf("variable not expanded in endpoint: %s", check.Endpoint)
	}
	if check.Status != 200 {
		t.Fatalf("expected default status 200, got %d", check.Status)
	}
	if check.Asserts[0] != `.asset == "BTC.BTC"` {
		t.Fatalf("variable not expanded in assert: %s", check.Asserts[0])
	}
	if check.Asserts[1] != `.txid == "0000000000000000000000000000000000000000000000000000000000000001"` {
		t.Fatalf("observe_txid not rendered: %s", check.Asserts[1])
	}
	if blockCount(ops) != 3 {
		t.Fatalf("expected 3 blocks, got %d", blockCount(ops))
	}
}

func TestParseOpsNoFailExportCov(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suite.yaml")
	writeFileCov(t, path, "type: create-blocks\ncount: 1\n")
	ops, opLines, env, failExport := parseOps(zerolog.New(io.Discard), path, template.Must(templates.Clone()), nil)
	if failExport {
		t.Fatal("unexpected fail export flag")
	}
	if len(ops) != 1 || len(opLines) != 1 || len(env) != 0 {
		t.Fatalf("unexpected parse: ops=%d lines=%v env=%v", len(ops), opLines, env)
	}
}

func TestCleanExportsCov(t *testing.T) {
	mnt := t.TempDir()
	work := t.TempDir()
	t.Setenv("REGTEST_MNT_DIR", mnt)
	chdirCov(t, work)

	// suite that exists
	writeFileCov(t, filepath.Join(work, "suites", "group", "alive.yaml"), "type: create-blocks\n")

	alive := filepath.Join(mnt, "exports", "group", "alive.json")
	dead := filepath.Join(mnt, "exports", "group", "dead.json")
	aliveBlocks := filepath.Join(mnt, "blocks", "group", "alive.json")
	deadBlocks := filepath.Join(mnt, "blocks", "dead.json")
	singleBlock := filepath.Join(mnt, "blocks", "gone", "001.json")
	for _, p := range []string{alive, dead, aliveBlocks, deadBlocks, singleBlock} {
		writeFileCov(t, p, "{}")
	}

	cleanExports()

	for _, p := range []string{alive, aliveBlocks, singleBlock} {
		if !pathExists(p) {
			t.Fatalf("expected %s to be kept", p)
		}
	}
	for _, p := range []string{dead, deadBlocks} {
		if pathExists(p) {
			t.Fatalf("expected %s to be removed", p)
		}
	}
}
