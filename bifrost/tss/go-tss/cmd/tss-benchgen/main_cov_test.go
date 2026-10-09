package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"
)

// exitCodeCov is panicked by the exit seam so main/usage stop where the real
// binary would terminate.
type exitCodeCov int

// filePreParamsCov decodes test_data/preParam_test.data (one hex-encoded JSON
// LocalPreParams per line); each set is distinct and v3-valid.
func filePreParamsCov(t *testing.T) []keygen.LocalPreParams {
	t.Helper()
	raw, err := os.ReadFile("../../test_data/preParam_test.data")
	require.NoError(t, err)
	var out []keygen.LocalPreParams
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		bz, err := hex.DecodeString(strings.TrimSpace(line))
		require.NoError(t, err)
		var p keygen.LocalPreParams
		require.NoError(t, json.Unmarshal(bz, &p))
		require.True(t, p.ValidateWithProof())
		out = append(out, p)
	}
	require.GreaterOrEqual(t, len(out), 2)
	return out
}

// usePerPartyPreParamsCov makes every party use its own pre-params set.
func usePerPartyPreParamsCov(t *testing.T) {
	t.Helper()
	pps := filePreParamsCov(t)
	old := partyPreParams
	t.Cleanup(func() { partyPreParams = old })
	partyPreParams = func(i int) keygen.LocalPreParams { return pps[i] }
}

// runMainCov runs main with args, capturing stdout/stderr and the exit code.
func runMainCov(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	oldArgs, oldFlags, oldUsage, oldExit, oldStderr := os.Args, flag.CommandLine, flag.Usage, exit, os.Stderr
	defer func() {
		os.Args, flag.CommandLine, flag.Usage, exit, os.Stderr = oldArgs, oldFlags, oldUsage, oldExit, oldStderr
	}()
	os.Args = append([]string{"tss-benchgen"}, args...)
	flag.CommandLine = flag.NewFlagSet("tss-benchgen", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	exit = func(c int) { panic(exitCodeCov(c)) }

	er, ew, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = ew
	errDone := make(chan string)
	go func() {
		b, _ := io.ReadAll(er)
		errDone <- string(b)
	}()

	code = -1
	stdout = captureStdout(t, func() {
		defer func() {
			r := recover()
			ec, ok := r.(exitCodeCov)
			require.True(t, ok, "unexpected panic: %v", r)
			code = int(ec)
		}()
		main()
	})
	require.NoError(t, ew.Close())
	stderr = <-errDone
	require.NoError(t, er.Close())
	return code, stdout, stderr
}

func TestDefaultSeamsCov(t *testing.T) {
	// default behaviour: every party gets the embedded pre-params
	require.Equal(t, 0, partyPreParams(0).NTildei.Cmp(preParamTestData.NTildei))
	require.Equal(t, 0, partyPreParams(5).NTildei.Cmp(preParamTestData.NTildei))
	require.Equal(t, fmt.Sprintf("%p", os.Exit), fmt.Sprintf("%p", exit))
}

// TestMainSuccessCov runs one full 2-party keygen (the only success path).
// It needs per-party pre-params: by default every party shares the embedded
// set, which tss-lib v3 rejects (see TestPreParamsFixtureLoaded/TestRunKeyGen).
func TestMainSuccessCov(t *testing.T) {
	usePerPartyPreParamsCov(t)
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0)) // main calls runtime.GOMAXPROCS(-procs)
	procs := runtime.NumCPU()
	dir := filepath.Join(t.TempDir(), "shares")
	code, out, _ := runMainCov(t, "-q", "2", "-n", "2", "-procs", strconv.Itoa(procs), dir)
	require.Equal(t, 0, code)
	require.Contains(t, out, fmt.Sprintf("Max go procs (threads): %d", procs))
	require.Contains(t, out, "ECDSA/GG20 Benchmark Tool - KeyGen")
	require.Contains(t, out, fmt.Sprintf("Done. 2 shares written to `%s`.", dir))
	require.Regexp(t, `Finished in [\d,]+ ms\.`, out)
	require.Contains(t, out, "Progress: ")

	// runKeyGen completed: every party wrote a share for the same group key
	s0, s1 := readShare(t, dir, 0), readShare(t, dir, 1)
	require.NotNil(t, s0.ECDSAPub)
	require.True(t, s0.ECDSAPub.Equals(s1.ECDSAPub), "parties must agree on the group key")
	require.NotEqual(t, 0, s0.ShareID.Cmp(s1.ShareID))
	require.Len(t, s0.Ks, 2)
}

func TestMainArgErrorsCov(t *testing.T) {
	existing := t.TempDir()
	for _, tc := range []struct {
		name    string
		args    []string
		code    int
		stdout  string
		stderr  string
		noUsage bool
	}{
		{name: "n zero", args: []string{"-n", "0", "d"}, code: 1, stdout: "Error: n must be greater than 0", noUsage: true},
		{name: "q one", args: []string{"-q", "1", "d"}, code: 1, stdout: "Error: n must be greater than 0", noUsage: true},
		{name: "n below q", args: []string{"-q", "3", "-n", "2", "d"}, code: 1, stdout: "q cannot be less than n", noUsage: true},
		{name: "missing dir", args: []string{"-q", "2", "-n", "2"}, code: 2, stderr: "usage: tss-benchgen [-flag=value, ...] datadir"},
		{name: "bad flag", args: []string{"-bogus"}, code: 2, stderr: "usage: tss-benchgen"},
		{name: "dir exists", args: []string{existing}, code: 1, stdout: "already exists, delete it first", noUsage: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := runMainCov(t, tc.args...)
			require.Equal(t, tc.code, code)
			require.Contains(t, out, tc.stdout)
			require.Contains(t, errOut, tc.stderr)
			if tc.noUsage {
				require.Empty(t, errOut)
			}
			require.NotContains(t, out, "Generating")
		})
	}
}
