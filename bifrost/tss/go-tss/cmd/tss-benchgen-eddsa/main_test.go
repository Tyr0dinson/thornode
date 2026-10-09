package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/eddsa/keygen"
	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/tss"
)

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

func readShare(t *testing.T, dir string, i int) keygen.LocalPartySaveData {
	t.Helper()
	bz, err := os.ReadFile(makeKeyGenDataFilePath(dir, i))
	require.NoError(t, err)
	var key keygen.LocalPartySaveData
	require.NoError(t, json.Unmarshal(bz, &key))
	return key
}

func TestMakeKeyGenDataFilePath(t *testing.T) {
	require.Equal(t, "d/7.json", makeKeyGenDataFilePath("d", 7))
}

func TestProgress(t *testing.T) {
	setUpProgress(5)
	require.Equal(t, float64(4), nMinus1)
	require.Equal(t, float64(16), expectedIncomingMsgs)
	require.Equal(t, float64(-1), receivedIncomingMsgs)

	require.Equal(t, "\rProgress: 0%... ", captureStdout(t, incrementAndDisplayProgress))
	require.Equal(t, "\rProgress: 6%... ", captureStdout(t, incrementAndDisplayProgress))
	receivedIncomingMsgs = 50
	require.Equal(t, "\rProgress: 100%... ", captureStdout(t, incrementAndDisplayProgress))
}

func TestTryWriteKeyGenDataFile(t *testing.T) {
	dir := t.TempDir()
	data := keygen.NewLocalPartySaveData(2)
	data.ShareID = tss.GenerateTestPartyIDs(1)[0].KeyInt()

	tryWriteKeyGenDataFile(dir, 0, data)
	got := readShare(t, dir, 0)
	require.Equal(t, 0, data.ShareID.Cmp(got.ShareID))

	// an existing fixture is never overwritten
	require.NoError(t, os.WriteFile(makeKeyGenDataFilePath(dir, 1), []byte("keep"), 0o600))
	out := captureStdout(t, func() { tryWriteKeyGenDataFile(dir, 1, data) })
	require.Contains(t, out, "Fixture file already exists for party 1")
	bz, err := os.ReadFile(makeKeyGenDataFilePath(dir, 1))
	require.NoError(t, err)
	require.Equal(t, "keep", string(bz))

	// unwritable location panics
	require.Panics(t, func() { tryWriteKeyGenDataFile(filepath.Join(dir, "missing"), 0, data) })
}

func TestRunKeyGen(t *testing.T) {
	dir := t.TempDir()
	out := captureStdout(t, func() { runKeyGen(dir, 1, 3) })
	require.Contains(t, out, "Progress: 100%")

	k0, k1, k2 := readShare(t, dir, 0), readShare(t, dir, 1), readShare(t, dir, 2)
	require.NotNil(t, k0.EDDSAPub)
	require.True(t, k0.EDDSAPub.Equals(k1.EDDSAPub), "all parties must share the same public key")
	require.True(t, k0.EDDSAPub.Equals(k2.EDDSAPub), "all parties must share the same public key")
	require.NotEqual(t, 0, k0.ShareID.Cmp(k1.ShareID))
	require.Len(t, k0.Ks, 3)
}

// longDirCov returns a not-yet-existing directory path whose length leaves
// room for os.Mkdir but not for "<dir>/<i>.json" (Linux PATH_MAX is 4096), so
// writing the first share fails deterministically, even when run as root.
func longDirCov(t *testing.T) string {
	t.Helper()
	const pathMax = 4096
	dir := t.TempDir()
	for len(dir)+1+200+1+20 < pathMax-8 {
		dir = filepath.Join(dir, strings.Repeat("d", 200))
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))
	// final component brings the length to pathMax-4: Mkdir fits, "/0.json" does not
	last := pathMax - 4 - len(dir) - 1
	require.Positive(t, last)
	require.LessOrEqual(t, last, 255)
	dir = filepath.Join(dir, strings.Repeat("s", last))
	require.Len(t, dir, pathMax-4)
	return dir
}

func TestMainRunsKeyGen(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on Linux PATH_MAX")
	}
	dir := longDirCov(t)

	oldArgs, oldFlags := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFlags }()
	os.Args = []string{"tss-benchgen-eddsa", "-q", "2", "-n", "2", "-procs", "2", dir}
	flag.CommandLine = flag.NewFlagSet("tss-benchgen-eddsa", flag.ContinueOnError)

	// main creates the directory and runs the full ceremony; writing the
	// first share then fails and panics instead of reaching os.Exit.
	var recovered interface{}
	out := captureStdout(t, func() {
		defer func() { recovered = recover() }()
		main()
	})
	require.NotNil(t, recovered)
	require.Contains(t, fmt.Sprint(recovered), "unable to open fixture file")
	require.Contains(t, out, "Generating 2 shares, quorum=2...")
	require.Contains(t, out, "Max go procs (threads): 2")
	fi, err := os.Stat(dir)
	require.NoError(t, err)
	require.True(t, fi.IsDir(), "main must create the output directory")
}

func TestMainMkdirFails(t *testing.T) {
	// parent directory is missing: the "already exists" check passes, but
	// os.Mkdir (not MkdirAll) fails and main panics before any keygen work.
	dir := filepath.Join(t.TempDir(), "missing", "shares")

	oldArgs, oldFlags := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFlags }()
	os.Args = []string{"tss-benchgen-eddsa", "-q", "2", "-n", "2", dir}
	flag.CommandLine = flag.NewFlagSet("tss-benchgen-eddsa", flag.ContinueOnError)

	var recovered interface{}
	out := captureStdout(t, func() {
		defer func() { recovered = recover() }()
		main()
	})
	require.NotNil(t, recovered)
	require.Contains(t, fmt.Sprint(recovered), "no such file or directory")
	require.NotContains(t, out, "Generating")
}
