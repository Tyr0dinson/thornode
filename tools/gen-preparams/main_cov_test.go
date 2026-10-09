package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"
)

const genPreParamsCovRunMainEnv = "GEN_PREPARAMS_COV_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(genPreParamsCovRunMainEnv) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// genPreParamsCovFixture loads the precomputed preparams committed for the
// go-tss tests (one hex-encoded JSON LocalPreParams per line).
func genPreParamsCovFixture(t *testing.T) []*keygen.LocalPreParams {
	t.Helper()
	f, err := os.Open("../../bifrost/tss/go-tss/test_data/preParam_test.data")
	require.NoError(t, err)
	defer f.Close()
	var out []*keygen.LocalPreParams
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		bz, err := hex.DecodeString(line)
		require.NoError(t, err)
		pp := new(keygen.LocalPreParams)
		require.NoError(t, json.Unmarshal(bz, pp))
		out = append(out, pp)
	}
	require.NoError(t, sc.Err())
	require.NotEmpty(t, out)
	return out
}

// genPreParamsCovStub swaps the generator for one returning the fixture in
// order (wrapping around) and records the requested timeouts.
func genPreParamsCovStub(t *testing.T, pps []*keygen.LocalPreParams, err error) *[]time.Duration {
	t.Helper()
	old := generatePreParams
	t.Cleanup(func() { generatePreParams = old })
	var timeouts []time.Duration
	generatePreParams = func(timeout time.Duration, _ ...int) (*keygen.LocalPreParams, error) {
		timeouts = append(timeouts, timeout)
		if err != nil {
			return nil, err
		}
		return pps[(len(timeouts)-1)%len(pps)], nil
	}
	return &timeouts
}

func TestGenPreParamsRunUsageCov(t *testing.T) {
	timeouts := genPreParamsCovStub(t, nil, errors.New("must not be called"))
	for _, arg := range []string{"abc", "0", "-3"} {
		var stdout, stderr bytes.Buffer
		require.Equal(t, 2, run([]string{arg}, &stdout, &stderr), arg)
		require.Empty(t, stdout.String())
		require.Equal(t, "usage: gen-preparams [n]\n", stderr.String())
	}
	require.Empty(t, *timeouts)
}

func TestGenPreParamsRunGenerateErrorCov(t *testing.T) {
	timeouts := genPreParamsCovStub(t, nil, errors.New("timed out"))
	var stdout, stderr bytes.Buffer
	require.Equal(t, 1, run(nil, &stdout, &stderr))
	require.Empty(t, stdout.String())
	require.Equal(t, "party 0: timed out\n", stderr.String())
	require.Equal(t, []time.Duration{20 * time.Minute}, *timeouts)
}

func TestGenPreParamsRunValidationFailureCov(t *testing.T) {
	pps := genPreParamsCovFixture(t)
	// drop the Paillier primes (on a freshly decoded copy) so
	// ValidateWithProof rejects the second party
	bad := genPreParamsCovFixture(t)[1]
	bad.PaillierSK.P, bad.PaillierSK.Q = nil, nil
	genPreParamsCovStub(t, []*keygen.LocalPreParams{pps[0], bad}, nil)

	var stdout, stderr bytes.Buffer
	require.Equal(t, 1, run([]string{"3"}, &stdout, &stderr))
	// party 0 is emitted before party 1 fails
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	require.Len(t, lines, 1)
	require.Contains(t, stderr.String(), "party 0 generated in ")
	require.True(t, strings.HasSuffix(stderr.String(), "party 1: generated preParams failed ValidateWithProof\n"), stderr.String())
}

func TestGenPreParamsRunSuccessCov(t *testing.T) {
	pps := genPreParamsCovFixture(t)
	for _, pp := range pps {
		require.True(t, pp.ValidateWithProof())
	}

	for _, tc := range []struct {
		args []string
		n    int
	}{
		{args: nil, n: 4}, // default party count
		{args: []string{"2"}, n: 2},
	} {
		timeouts := genPreParamsCovStub(t, pps, nil)
		var stdout, stderr bytes.Buffer
		require.Equal(t, 0, run(tc.args, &stdout, &stderr))
		require.Len(t, *timeouts, tc.n)

		lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
		require.Len(t, lines, tc.n)
		for i, line := range lines {
			// each line round-trips back to the generated preparams
			bz, err := hex.DecodeString(line)
			require.NoError(t, err)
			got := new(keygen.LocalPreParams)
			require.NoError(t, json.Unmarshal(bz, got))
			want := pps[i%len(pps)]
			require.Equal(t, 0, got.NTildei.Cmp(want.NTildei), "party %d", i)
			require.True(t, got.ValidateWithProof(), "party %d", i)
			require.Contains(t, stderr.String(), fmt.Sprintf("party %d generated in ", i))
		}
	}
}

func TestGenPreParamsMainUsageCov(t *testing.T) {
	// main exits the process, so run it in a re-executed test binary; a usage
	// error returns before any (slow) generation happens
	cmd := exec.Command(os.Args[0], "nope") // #nosec G204 -- re-executes this test binary
	cmd.Env = append(os.Environ(), genPreParamsCovRunMainEnv+"=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 2, exitErr.ExitCode())
	require.Contains(t, stderr.String(), "usage: gen-preparams [n]")
}
