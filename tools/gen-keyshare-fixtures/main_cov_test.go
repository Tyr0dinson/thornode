package main

import (
	"bufio"
	"bytes"
	"crypto/elliptic"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"
	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/tss"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
)

const genKeyshareCovRunMainEnv = "GEN_KEYSHARE_FIXTURES_COV_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(genKeyshareCovRunMainEnv) == "1" {
		// fail preparams generation so main exits before touching outDir
		generatePreParams = func(time.Duration, ...int) (*keygen.LocalPreParams, error) {
			return nil, errors.New("no entropy")
		}
		main()
		return
	}
	os.Exit(m.Run())
}

// genKeyshareCovPreParams loads the precomputed preparams committed for the
// go-tss tests (one hex-encoded JSON LocalPreParams per line).
func genKeyshareCovPreParams(t *testing.T) []*keygen.LocalPreParams {
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
	require.Len(t, out, len(testPubKeys))
	return out
}

// genKeyshareCovStub makes generatePreParams hand out the given preparams
// (one per call, in call order) or fail with err.
func genKeyshareCovStub(t *testing.T, pps []*keygen.LocalPreParams, err error) *atomic.Int32 {
	t.Helper()
	old := generatePreParams
	t.Cleanup(func() { generatePreParams = old })
	next := make(chan *keygen.LocalPreParams, len(pps))
	for _, pp := range pps {
		next <- pp
	}
	calls := new(atomic.Int32)
	generatePreParams = func(timeout time.Duration, _ ...int) (*keygen.LocalPreParams, error) {
		defer calls.Add(1)
		if timeout != 20*time.Minute {
			return nil, errors.New("unexpected timeout")
		}
		if err != nil {
			return nil, err
		}
		return <-next, nil
	}
	return calls
}

// genKeyshareCovSessionNonce makes every party's parameters carry the same
// session nonce, which tss-lib v3 keygen requires (see the SessionNonce test).
func genKeyshareCovSessionNonce(t *testing.T) {
	t.Helper()
	old := newParameters
	t.Cleanup(func() { newParameters = old })
	newParameters = func(ec elliptic.Curve, ctx *tss.PeerContext, id *tss.PartyID, n, threshold int) *tss.Parameters {
		p := old(ec, ctx, id, n, threshold)
		p.SetSessionNonce(big.NewInt(42))
		return p
	}
}

// genKeyshareCovStdout captures what fn writes to os.Stdout.
func genKeyshareCovStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	require.NoError(t, w.Close())
	return <-done
}

func TestGenKeyshareFixturesRunCov(t *testing.T) {
	genKeyshareCovStub(t, genKeyshareCovPreParams(t), nil)
	genKeyshareCovSessionNonce(t)
	dir := filepath.Join(t.TempDir(), "out", "ecdsa") // created by run

	var runErr error
	out := genKeyshareCovStdout(t, func() { runErr = run(dir) })
	require.NoError(t, runErr)
	vaultPubKey := strings.TrimSpace(out)
	require.True(t, strings.HasPrefix(vaultPubKey, "thorpub"), vaultPubKey)

	sorted := append([]string(nil), testPubKeys...)
	sort.Strings(sorted)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, len(testPubKeys))

	seenShares := map[string]bool{}
	for i := range testPubKeys {
		bz, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(i)+".json"))
		require.NoError(t, err)
		var state storage.KeygenLocalState
		require.NoError(t, json.Unmarshal(bz, &state))
		require.Equal(t, vaultPubKey, state.PubKey)
		require.Equal(t, sorted, state.ParticipantKeys)
		// file i holds the keyshare of the i-th lexicographically sorted key
		require.Equal(t, sorted[i], state.LocalPartyKey)

		var save keygen.LocalPartySaveData
		require.NoError(t, json.Unmarshal(state.LocalData, &save))
		require.True(t, save.ValidateWithProof(), "party %d", i)
		pk, _, err := conversion.GetTssPubKeyECDSA(save.ECDSAPub)
		require.NoError(t, err)
		require.Equal(t, vaultPubKey, pk)
		share := save.Xi.String()
		require.False(t, seenShares[share], "duplicate share for party %d", i)
		seenShares[share] = true
	}
}

func TestGenKeyshareFixturesPreParamsErrorCov(t *testing.T) {
	calls := genKeyshareCovStub(t, nil, errors.New("timed out"))
	dir := filepath.Join(t.TempDir(), "out")
	err := run(dir)
	require.Error(t, err)
	require.Regexp(t, `^preParams\[[0-3]\]: timed out$`, err.Error())
	// run returns on the first failure; let the other generators finish
	// before the stub is restored
	require.Eventually(t, func() bool { return calls.Load() == int32(len(testPubKeys)) }, 5*time.Second, time.Millisecond)
	_, statErr := os.Stat(dir)
	require.True(t, os.IsNotExist(statErr), "nothing is written on failure")
}

// TestGenKeyshareFixturesNoSessionNonceCov documents current behaviour: the
// tool never sets a SessionNonce on the keygen parameters, which tss-lib v3
// requires, so as written every keygen aborts in round 1 and no fixture is
// written.
func TestGenKeyshareFixturesNoSessionNonceCov(t *testing.T) {
	genKeyshareCovStub(t, genKeyshareCovPreParams(t), nil)
	dir := filepath.Join(t.TempDir(), "out")
	err := run(dir)
	require.Error(t, err)
	require.True(t, strings.HasPrefix(err.Error(), "keygen: "), err.Error())
	require.Contains(t, err.Error(), "SessionNonce must be set")
	_, statErr := os.Stat(dir)
	require.True(t, os.IsNotExist(statErr), "nothing is written on failure")
}

func TestGenKeyshareFixturesMainDieCov(t *testing.T) {
	// main exits the process, so run it in a re-executed test binary
	cmd := exec.Command(os.Args[0]) // #nosec G204 -- re-executes this test binary
	cmd.Env = append(os.Environ(), genKeyshareCovRunMainEnv+"=1")
	cmd.Dir = t.TempDir()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, stderr.String())
	require.Equal(t, 1, exitErr.ExitCode())
	require.Regexp(t, `(?m)^error: preParams\[[0-3]\]: no entropy$`, stderr.String())
	_, statErr := os.Stat(filepath.Join(cmd.Dir, outDir))
	require.True(t, os.IsNotExist(statErr))
}
