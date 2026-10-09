package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"
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

// validPreParamsCov returns pre-params taken from a committed v3 keyshare
// fixture (which carries the safe primes and proof material).
func validPreParamsCov(t *testing.T) keygen.LocalPreParams {
	t.Helper()
	raw, err := os.ReadFile("../../test_data/keysign_data/ecdsa/0.json")
	require.NoError(t, err)
	var state storage.KeygenLocalState
	require.NoError(t, json.Unmarshal(raw, &state))
	var key keygen.LocalPartySaveData
	require.NoError(t, json.Unmarshal(state.LocalData, &key))
	require.True(t, key.LocalPreParams.ValidateWithProof())
	return key.LocalPreParams
}

func TestPreParamsFixtureLoaded(t *testing.T) {
	// init() decodes the embedded pre-params; they parse and pass basic
	// validation but fail tss-lib v3's ValidateWithProof (they predate it), so
	// keygen.NewLocalParty rejects them. This documents current behaviour:
	// tss-benchgen panics on start until preParamDataHex is regenerated.
	require.True(t, preParamTestData.Validate())
	require.False(t, preParamTestData.ValidateWithProof())
}

func TestMakeKeyGenDataFilePath(t *testing.T) {
	require.Equal(t, "d/7.json", makeKeyGenDataFilePath("d", 7))
}

func TestProgress(t *testing.T) {
	setUpProgress(3)
	require.Equal(t, float64(2), nMinus1)
	require.Equal(t, float64(8), expectedIncomingMsgs)
	require.Equal(t, float64(-1), receivedIncomingMsgs)

	out := captureStdout(t, incrementAndDisplayProgress)
	require.Equal(t, "\rProgress: 0%... ", out)
	out = captureStdout(t, incrementAndDisplayProgress)
	require.Equal(t, "\rProgress: 12%... ", out)

	// progress is capped at 100%
	receivedIncomingMsgs = 100
	out = captureStdout(t, incrementAndDisplayProgress)
	require.Equal(t, "\rProgress: 100%... ", out)
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
	oldPre := preParamTestData
	defer func() { preParamTestData = oldPre }()

	// With the embedded (pre-v3) pre-params the parties cannot be constructed.
	require.PanicsWithError(t,
		"`optionalPreParams` failed to validate; it might have been generated with an older version of tss-lib",
		func() { runKeyGen(t.TempDir(), 1, 2) })

	// With valid v3 pre-params the ceremony starts and messages are routed,
	// but runKeyGen hands the SAME pre-params to every party and tss-lib v3
	// rejects reused Paillier/ring-Pedersen parameters in round 2. The error
	// is surfaced through errCh and re-panicked; no share is written.
	preParamTestData = validPreParamsCov(t)
	dir := t.TempDir()
	var recovered interface{}
	out := captureStdout(t, func() {
		defer func() { recovered = recover() }()
		runKeyGen(dir, 1, 2)
	})
	require.NotNil(t, recovered)
	require.Contains(t, fmt.Sprint(recovered), "was already used by another party")
	require.Contains(t, out, "Starting... ")
	require.Contains(t, out, "Progress: ")
	_, err := os.Stat(makeKeyGenDataFilePath(dir, 0))
	require.True(t, os.IsNotExist(err))
}

func TestMainReachesKeyGen(t *testing.T) {
	// Swap in invalid pre-params so the first party construction panics
	// instead of main running to os.Exit.
	oldPre := preParamTestData
	oldArgs, oldFlags := os.Args, flag.CommandLine
	defer func() {
		preParamTestData = oldPre
		os.Args, flag.CommandLine = oldArgs, oldFlags
	}()
	preParamTestData = keygen.LocalPreParams{}

	dir := filepath.Join(t.TempDir(), "shares")
	os.Args = []string{"tss-benchgen", "-q", "2", "-n", "2", "-procs", "2", dir}
	flag.CommandLine = flag.NewFlagSet("tss-benchgen", flag.ContinueOnError)

	var recovered interface{}
	out := captureStdout(t, func() {
		defer func() { recovered = recover() }()
		main()
	})
	require.NotNil(t, recovered)
	require.Contains(t, fmt.Sprint(recovered), "failed to validate")
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
	os.Args = []string{"tss-benchgen", "-q", "2", "-n", "2", dir}
	flag.CommandLine = flag.NewFlagSet("tss-benchgen", flag.ContinueOnError)

	var recovered interface{}
	out := captureStdout(t, func() {
		defer func() { recovered = recover() }()
		main()
	})
	require.NotNil(t, recovered)
	require.Contains(t, fmt.Sprint(recovered), "no such file or directory")
	require.NotContains(t, out, "Generating")
}
