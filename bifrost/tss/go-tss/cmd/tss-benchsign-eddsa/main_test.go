package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/eddsa/keygen"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
)

// fixtureSrcDir holds thornode-format (KeygenLocalState) EdDSA keyshares for
// a 4-party, threshold-2 key.
const fixtureSrcDir = "../../test_data/keysign_data/eddsa"

// writeBenchFixtures unwraps n thornode keyshares into the raw tss-lib
// LocalPartySaveData layout the bench tool expects (<dir>/<i>.json). The bench
// tool pairs the i-th file with the i-th party ID sorted by ShareID (as
// tss-benchgen writes them), so the shares are written in ShareID order.
func writeBenchFixtures(t *testing.T, n int) string {
	t.Helper()
	type share struct {
		raw []byte
		key keygen.LocalPartySaveData
	}
	shares := make([]share, 0, 4)
	for i := 0; i < 4; i++ {
		raw, err := os.ReadFile(filepath.Join(fixtureSrcDir, fmt.Sprintf("%d.json", i)))
		require.NoError(t, err)
		var state storage.KeygenLocalState
		require.NoError(t, json.Unmarshal(raw, &state))
		var key keygen.LocalPartySaveData
		require.NoError(t, json.Unmarshal(state.LocalData, &key))
		shares = append(shares, share{raw: state.LocalData, key: key})
	}
	sort.Slice(shares, func(i, j int) bool { return shares[i].key.ShareID.Cmp(shares[j].key.ShareID) < 0 })
	dir := t.TempDir()
	for i := 0; i < n; i++ {
		require.NoError(t, os.WriteFile(makeKeyGenDataFilePath(dir, i), shares[i].raw, 0o600))
	}
	return dir
}

// captureStdout runs fn with os.Stdout redirected and returns what was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() {
		os.Stdout = old
	}()
	fn()
	require.NoError(t, w.Close())
	out := <-done
	require.NoError(t, r.Close())
	return out
}

func TestMakeKeyGenDataFilePath(t *testing.T) {
	require.Equal(t, "/tmp/x/3.json", makeKeyGenDataFilePath("/tmp/x", 3))
}

func TestLoadKeyGenData(t *testing.T) {
	dir := writeBenchFixtures(t, 3)

	keys, pids, err := loadKeyGenData(dir, 3)
	require.NoError(t, err)
	require.Len(t, keys, 3)
	require.Len(t, pids, 3)
	for i, pid := range pids {
		require.Equal(t, i, pid.Index)
		require.Equal(t, pid.Id, pid.Moniker)
	}

	// optional start offset skips the first shares and shifts monikers
	keys, pids, err = loadKeyGenData(dir, 3, 1)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	monikers := []string{pids[0].Moniker, pids[1].Moniker}
	require.ElementsMatch(t, []string{"2", "3"}, monikers)

	// missing share
	_, _, err = loadKeyGenData(dir, 4)
	require.ErrorContains(t, err, "could not open the test fixture for party 3")

	// corrupt share
	require.NoError(t, os.WriteFile(makeKeyGenDataFilePath(dir, 0), []byte("{bad"), 0o600))
	_, _, err = loadKeyGenData(dir, 1)
	require.ErrorContains(t, err, "could not unmarshal fixture data for party 0")
}

func TestRunSign(t *testing.T) {
	// runSign panics on any protocol or signature verification failure, so
	// returning normally means a valid signature was produced and verified.
	dir := writeBenchFixtures(t, 3)
	require.NotPanics(t, func() { runSign(dir, 2) })

	// insufficient shares on disk
	require.Panics(t, func() { runSign(t.TempDir(), 2) })

	// Shares on disk out of ShareID order: each party is paired with another
	// party's ID, so the ceremony must fail rather than yield a signature.
	swapped := writeBenchFixtures(t, 3)
	a, b := makeKeyGenDataFilePath(swapped, 0), makeKeyGenDataFilePath(swapped, 2)
	tmp := filepath.Join(swapped, "tmp")
	require.NoError(t, os.Rename(a, tmp))
	require.NoError(t, os.Rename(b, a))
	require.NoError(t, os.Rename(tmp, b))
	require.Panics(t, func() { runSign(swapped, 2) })
}

func TestPrintSummary(t *testing.T) {
	results := [][]result{
		{{quorum: 2, duration: 1000 * time.Millisecond}, {quorum: 3, duration: 3000 * time.Millisecond}},
		{{quorum: 2, duration: 2000 * time.Millisecond}, {quorum: 3, duration: 5000 * time.Millisecond}},
	}
	out := captureStdout(t, func() { printSummary(results) })
	require.Contains(t, out, "QUORUM")
	require.Contains(t, out, "RUN 1")
	require.Contains(t, out, "RUN 2")
	require.Contains(t, out, "MEAN")
	require.Contains(t, out, "1,500 ms")
	require.Contains(t, out, "4,000 ms")
	require.Equal(t, 1, strings.Count(out, "5,000 ms"))
}

func TestMainReachesSigning(t *testing.T) {
	// Shares exist on disk (so all of main's pre-flight checks pass) but are
	// corrupt, so the first signing run panics instead of reaching os.Exit.
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		require.NoError(t, os.WriteFile(makeKeyGenDataFilePath(dir, i), []byte("{bad"), 0o600))
	}
	oldArgs, oldFlags := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFlags }()
	os.Args = []string{"tss-benchsign-eddsa", "-q", "3", "-n", "3", "-r", "1", "-procs", "2", dir}
	flag.CommandLine = flag.NewFlagSet("tss-benchsign-eddsa", flag.ContinueOnError)

	var recovered interface{}
	out := captureStdout(t, func() {
		defer func() { recovered = recover() }()
		main()
	})
	require.NotNil(t, recovered)
	require.Contains(t, fmt.Sprint(recovered), "could not unmarshal fixture data for party 0")
	require.Contains(t, out, "Will test quorums 3-3 in 1 runs")
	require.Contains(t, out, "Max go procs (threads): 2")
	require.Contains(t, out, "Signing run 1...")
}
