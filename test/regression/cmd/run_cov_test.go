package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

////////////////////////////////////////////////////////////////////////////////////////
// Fake regtest environment
////////////////////////////////////////////////////////////////////////////////////////

// fakeRegtestCov wires the environment run() and export() expect: a fake
// `thornode` CLI on PATH, a fake long-running `cover-thornode` in
// REGTEST_BIN_DIR, and temporary mnt and home directories. No real node is run.
type fakeRegtestCov struct {
	mnt      string
	homeBase string
}

func lookPathCov(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not available: %v", name, err)
	}
	return p
}

func setupFakeRegtestCov(t *testing.T, blockDirs ...string) *fakeRegtestCov {
	t.Helper()
	mkdir := lookPathCov(t, "mkdir")
	cat := lookPathCov(t, "cat")
	sleep := lookPathCov(t, "sleep")

	f := &fakeRegtestCov{mnt: t.TempDir(), homeBase: t.TempDir()}
	t.Setenv("REGTEST_MNT_DIR", f.mnt)
	t.Setenv("REGTEST_HOME_BASE", f.homeBase)
	t.Setenv("DEBUG", "")
	t.Setenv("CI", "")
	t.Setenv("AUTO_UPDATE", "")
	t.Setenv("EXPORT", "")
	t.Setenv("IGNORE_FAILURES", "")
	t.Setenv("FAIL_FAST", "")
	t.Setenv("TIME_FACTOR", "")
	writeFileCov(t, filepath.Join(f.mnt, "priv_validator_key.json"), `{"fake":true}`)

	// blocks directories are normally created by create-blocks; suites without
	// it rely on render-config creating them so the blocks export can be read
	mkBlocks := ""
	for _, d := range blockDirs {
		mkBlocks += fmt.Sprintf(" \"$GOCOVERDIR/../blocks/%s\"", d)
	}

	// the thornode commands run with a minimal environment (no PATH), so the
	// script uses absolute paths for external tools
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  init)\n" +
		"    " + mkdir + " -p \"$HOME/.thornode/config\"\n" +
		"    printf '%s' '{\"chain_id\":\"thorchain\",\"app_state\":{\"thorchain\":{}}}' > \"$HOME/.thornode/config/genesis.json\"\n" +
		"    ;;\n" +
		"  render-config)\n"
	if mkBlocks != "" {
		script += "    " + mkdir + " -p" + mkBlocks + "\n"
	}
	script += "    ;;\n" +
		"  export) exec " + cat + " \"$FAKE_THORNODE_EXPORT\" ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "thornode"), []byte(script), 0o700); err != nil { //nolint:gosec // executable test fixture
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	regtestBin := t.TempDir()
	cover := "#!/bin/sh\nexec " + sleep + " 30\n"
	if err := os.WriteFile(filepath.Join(regtestBin, "cover-thornode"), []byte(cover), 0o700); err != nil { //nolint:gosec // executable test fixture
		t.Fatal(err)
	}
	t.Setenv("REGTEST_BIN_DIR", regtestBin)

	exportSrc := filepath.Join(t.TempDir(), "export.json")
	writeExportCov(t, exportSrc, balancedGenesisCov(t, balancedVaultBTCCov))
	t.Setenv("FAKE_THORNODE_EXPORT", exportSrc)

	return f
}

// consecutiveListenersCov listens on n consecutive localhost ports and returns
// the first. Connections are accepted by the kernel backlog, which is all the
// run() readiness probe needs.
func consecutiveListenersCov(t *testing.T, n int) int {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		first, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		base := portCov(t, first.Addr())
		listeners := []net.Listener{first}
		ok := true
		for i := 1; i < n; i++ {
			l, lerr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", base+i))
			if lerr != nil {
				ok = false
				break
			}
			listeners = append(listeners, l)
		}
		if ok {
			t.Cleanup(func() {
				for _, l := range listeners {
					_ = l.Close()
				}
			})
			return base
		}
		for _, l := range listeners {
			_ = l.Close()
		}
	}
	t.Skip("could not reserve consecutive ports")
	return 0
}

////////////////////////////////////////////////////////////////////////////////////////
// run()
////////////////////////////////////////////////////////////////////////////////////////

const runSuiteCov = `type: env
key: EXTRA
value: "1"
---
type: state
genesis:
  app_state:
    thorchain:
      reserve: "42"
---
type: create-blocks
count: 1
skip_invariants: true
---
type: check
endpoint: http://localhost:1317/thorchain/ok
---
type: check
endpoint: http://localhost:1317/thorchain/error
---
type: create-blocks
count: 1
---
type: fail-export-invariants
`

func TestRunFailingSuiteDebugCov(t *testing.T) {
	f := setupFakeRegtestCov(t)
	t.Setenv("DEBUG", "debug")

	b := &blockServerCov{height: "3", blockStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.Handle("/", b)
	mux.HandleFunc("/thorchain/ok", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/thorchain/error", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	})
	routine := startBlockServerCov(t, mux)

	work := t.TempDir()
	chdirCov(t, work)
	writeFileCov(t, filepath.Join(work, "suites", "grp", "run-debug.yaml"), runSuiteCov)

	retried := 0
	out := &bytes.Buffer{}
	failExport, err := run(out, "suites/grp/run-debug.yaml", routine, func(string) bool { retried++; return true }, make(chan struct{}))
	if err == nil || err.Error() != "unexpected status code: 500" {
		t.Fatalf("expected failing check error, got %v\n%s", err, out.String())
	}
	if !failExport {
		t.Fatal("expected fail-export-invariants flag to be returned")
	}
	if retried != 0 {
		t.Fatal("suite must not be retried outside CI")
	}

	home := filepath.Join(f.homeBase, fmt.Sprint(routine))
	genesis := map[string]any{}
	bz, rerr := os.ReadFile(filepath.Join(home, ".thornode", "config", "genesis.json"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if err = json.Unmarshal(bz, &genesis); err != nil {
		t.Fatal(err)
	}
	if mapCov(t, mapCov(t, genesis["app_state"])["thorchain"])["reserve"] != "42" {
		t.Fatalf("state operation not applied to genesis: %s", string(bz))
	}
	// in DEBUG mode the validator key is removed before restarting the node
	if pathExists(filepath.Join(home, ".thornode", "config", "priv_validator_key.json")) {
		t.Fatal("expected validator key to be removed in DEBUG mode")
	}
	if !pathExists(filepath.Join(f.mnt, "blocks", "grp", "run-debug", "003.json")) {
		t.Fatal("expected block export from create-blocks")
	}
	if !strings.Contains(out.String(), "operation failed") {
		t.Fatalf("expected operation failure log, got %s", out.String())
	}
}

func TestRunRetryInCIAutoUpdateCov(t *testing.T) {
	setupFakeRegtestCov(t)
	t.Setenv("CI", "1")
	t.Setenv("AUTO_UPDATE", "1")

	b := &blockServerCov{height: "1", blockStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.Handle("/", b)
	mux.HandleFunc("/thorchain/ok", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/thorchain/error", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	})
	routine := startBlockServerCov(t, mux)

	work := t.TempDir()
	chdirCov(t, work)
	writeFileCov(t, filepath.Join(work, "suites", "run-ci.yaml"), runSuiteCov)

	// the first failure is retried by re-running the suite, the second one
	// exhausts the retries and is returned
	var retriedPaths []string
	_, err := run(io.Discard, "suites/run-ci.yaml", routine, func(p string) bool {
		retriedPaths = append(retriedPaths, p)
		return len(retriedPaths) >= 2
	}, make(chan struct{}))
	if err == nil || err.Error() != "unexpected status code: 500" {
		t.Fatalf("expected failing check error, got %v", err)
	}
	if len(retriedPaths) != 2 || retriedPaths[0] != "suites/run-ci.yaml" {
		t.Fatalf("expected two retry checks, got %v", retriedPaths)
	}
}

const runAbortSuiteCov = `type: state
genesis: {}
---
type: create-blocks
count: 0
skip_invariants: true
---
type: create-blocks
count: 0
skip_invariants: true
---
type: create-blocks
count: 0
skip_invariants: true
`

func TestRunAbortCov(t *testing.T) {
	setupFakeRegtestCov(t)
	work := t.TempDir()
	chdirCov(t, work)
	writeFileCov(t, filepath.Join(work, "suites", "run-abort.yaml"), runAbortSuiteCov)

	// aborted before any operation runs
	abort := make(chan struct{})
	close(abort)
	failExport, err := run(io.Discard, "suites/run-abort.yaml", 0, func(string) bool { return true }, abort)
	if err != nil || failExport {
		t.Fatalf("aborted run should return (false, nil), got (%v, %v)", failExport, err)
	}

	// aborted once the node is up: the readiness probe connecting to the block
	// creation port closes the abort channel. The node process is left running
	// by run() on abort, so use a short-lived fake node here.
	shortBin := t.TempDir()
	cover := "#!/bin/sh\nexec " + lookPathCov(t, "sleep") + " 3\n"
	if err = os.WriteFile(filepath.Join(shortBin, "cover-thornode"), []byte(cover), 0o700); err != nil { //nolint:gosec // executable test fixture
		t.Fatal(err)
	}
	t.Setenv("REGTEST_BIN_DIR", shortBin)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	t.Setenv("REGTEST_CREATE_BLOCK_PORT_BASE", fmt.Sprint(portCov(t, l.Addr())))
	abort = make(chan struct{})
	go func() {
		conn, aerr := l.Accept()
		if aerr == nil {
			_ = conn.Close()
		}
		close(abort)
	}()

	t.Setenv("DEBUG", "")
	out := &bytes.Buffer{}
	failExport, err = run(out, "suites/run-abort.yaml", 0, func(string) bool { return true }, abort)
	if err != nil || failExport {
		t.Fatalf("aborted run should return (false, nil), got (%v, %v)", failExport, err)
	}
	if strings.Contains(out.String(), "All operations succeeded") {
		t.Fatalf("operations should have been aborted:\n%s", out.String())
	}
}

func TestCopyFileCov(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeFileCov(t, src, "content")
	dst := filepath.Join(dir, "dst")
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	bz, err := os.ReadFile(dst)
	if err != nil || string(bz) != "content" {
		t.Fatalf("unexpected copy: %q %v", bz, err)
	}
	if err = copyFile(filepath.Join(dir, "missing"), dst); err == nil {
		t.Fatal("expected missing source error")
	}
	if err = copyFile(src, filepath.Join(dir, "missing-dir", "dst")); err == nil {
		t.Fatal("expected missing destination directory error")
	}
	// reading a directory fails during the copy
	if err = copyFile(dir, filepath.Join(dir, "dst2")); err == nil {
		t.Fatal("expected copy error when source is a directory")
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// regressionTest + main()
////////////////////////////////////////////////////////////////////////////////////////

// captureStdoutCov captures os.Stdout written by fn.
func captureStdoutCov(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	var buf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&buf, r)
	}()
	defer func() {
		os.Stdout = orig
	}()
	fn()
	_ = w.Close()
	wg.Wait()
	_ = r.Close()
	return buf.String()
}

func TestRegressionTestBookkeepingCov(t *testing.T) {
	r := newRegressionTest([]string{"a", "b"}, 8)
	if r.parallelism != 2 || r.total != 2 || cap(r.sem) != 2 {
		t.Fatalf("parallelism must be capped to the number of files: %+v", r)
	}

	// retries are exhausted on the third attempt
	for i := 1; i <= 3; i++ {
		if got := r.doneWithRetries("a"); got != (i == 3) {
			t.Fatalf("attempt %d: unexpected retries exhausted=%v", i, got)
		}
	}
	if r.retryCount["a"] != 3 {
		t.Fatalf("unexpected retry count %d", r.retryCount["a"])
	}

	t.Setenv("FAIL_FAST", "")
	output := captureStdoutCov(t, func() {
		for i, f := range r.files {
			r.sem <- struct{}{}
			r.wg.Add(1)
			r.markStart(f, i)
		}
		r.markDone("a", bytes.NewBufferString("buffered-a\n"), true)
		r.markDone("b", bytes.NewBufferString("buffered-b\n"), false)
		r.wg.Wait()
		r.printResults(r.retryCount)
	})
	if len(r.succeeded) != 1 || r.succeeded[0] != "a" || len(r.failed) != 1 || r.failed[0] != "b" {
		t.Fatalf("unexpected results: succeeded=%v failed=%v", r.succeeded, r.failed)
	}
	if len(r.inProgress) != 0 || r.completed != 2 || len(r.sem) != 0 {
		t.Fatalf("unexpected state: inProgress=%v completed=%d sem=%d", r.inProgress, r.completed, len(r.sem))
	}
	for _, want := range []string{"buffered-a", "buffered-b", "Succeeded:", "- a", "Retried:", "- a: 3", "Failed:", "- b"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in output:\n%s", want, output)
		}
	}

	// FAIL_FAST closes the abort channel on failure
	t.Setenv("FAIL_FAST", "1")
	r = newRegressionTest([]string{"c"}, 1)
	r.sem <- struct{}{}
	r.wg.Add(1)
	r.markStart("c", 0)
	r.markDone("c", &bytes.Buffer{}, false)
	select {
	case <-r.abort:
	default:
		t.Fatal("expected abort to be closed with FAIL_FAST")
	}
}

func TestRegressionTestRunAbortedCov(t *testing.T) {
	r := newRegressionTest([]string{"never-run.yaml"}, 1)
	r.sem <- struct{}{} // all slots busy, so only the abort case can proceed
	close(r.abort)
	output := captureStdoutCov(t, r.run)
	if len(r.succeeded) != 0 || len(r.failed) != 0 || len(r.inProgress) != 0 {
		t.Fatal("no test should run once aborted")
	}
	if !strings.Contains(output, "Succeeded:") {
		t.Fatalf("expected results to be printed, got %s", output)
	}
}

const mainSuiteCov = `type: env
key: EXTRA
value: "1"
---
type: state
genesis:
  app_state:
    thorchain:
      reserve: "7"
`

// TestMainCov runs main() end to end over two state-only suites in parallel
// against the fake thornode environment.
func TestMainCov(t *testing.T) {
	f := setupFakeRegtestCov(t, "main-a", "nested/main-b")
	base := consecutiveListenersCov(t, 2)
	t.Setenv("REGTEST_CREATE_BLOCK_PORT_BASE", fmt.Sprint(base))
	t.Setenv("PARALLELISM", "2")
	t.Setenv("RUN", ` "main-a" , 'nested/main-b', '' ,`)

	work := t.TempDir()
	chdirCov(t, work)
	writeFileCov(t, filepath.Join(work, "suites", "main-a.yaml"), mainSuiteCov)
	writeFileCov(t, filepath.Join(work, "suites", "nested", "main-b.yaml"), mainSuiteCov)
	writeFileCov(t, filepath.Join(work, "suites", "skipped.yaml"), "type: bogus\n")
	writeFileCov(t, filepath.Join(work, "suites", "notes.txt"), "not a suite")
	// stale export without a suite is cleaned up
	stale := filepath.Join(f.mnt, "exports", "stale.json")
	writeFileCov(t, stale, "{}")

	output := captureStdoutCov(t, main)

	if pathExists(stale) {
		t.Fatal("expected stale export to be removed")
	}
	var exports []string
	_ = filepath.Walk(filepath.Join(f.mnt, "exports"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(f.mnt, p)
			exports = append(exports, rel)
		}
		return nil
	})
	sort.Strings(exports)
	want := []string{filepath.Join("exports", "main-a.json"), filepath.Join("exports", "nested", "main-b.json")}
	if strings.Join(exports, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected exports %v", exports)
	}
	for _, p := range []string{"main-a.json", filepath.Join("nested", "main-b.json")} {
		if !pathExists(filepath.Join(f.mnt, "blocks", p)) {
			t.Fatalf("expected blocks export %s", p)
		}
	}
	if !strings.Contains(output, "Succeeded:\x1b[0m 2") {
		t.Fatalf("expected 2 succeeded suites, got:\n%s", output)
	}
}
