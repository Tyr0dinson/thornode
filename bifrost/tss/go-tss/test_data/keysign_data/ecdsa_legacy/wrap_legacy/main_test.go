package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
)

// fixtureDir is the committed v3 ECDSA fixture directory, relative to this package.
const fixtureDir = "../../ecdsa"

func readStateCov(t *testing.T, path string) (storage.KeygenLocalState, keygen.LocalPartySaveData) {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var state storage.KeygenLocalState
	require.NoError(t, json.Unmarshal(raw, &state))
	var local keygen.LocalPartySaveData
	require.NoError(t, json.Unmarshal(state.LocalData, &local))
	return state, local
}

func TestConvertNullsSafePrimes(t *testing.T) {
	src := filepath.Join(fixtureDir, "0.json")
	dst := filepath.Join(t.TempDir(), "0.json")
	require.NoError(t, convert(src, dst))

	srcState, srcLocal := readStateCov(t, src)
	require.NotNil(t, srcLocal.PaillierSK.P)
	require.NotNil(t, srcLocal.PaillierSK.Q)

	dstState, dstLocal := readStateCov(t, dst)
	require.NotNil(t, dstLocal.PaillierSK)
	require.Nil(t, dstLocal.PaillierSK.P)
	require.Nil(t, dstLocal.PaillierSK.Q)
	// everything else is preserved
	require.Equal(t, srcState.PubKey, dstState.PubKey)
	require.Equal(t, srcState.ParticipantKeys, dstState.ParticipantKeys)
	require.Equal(t, 0, srcLocal.PaillierSK.N.Cmp(dstLocal.PaillierSK.N))
	require.Equal(t, 0, srcLocal.Xi.Cmp(dstLocal.Xi))
}

func TestConvertErrors(t *testing.T) {
	dir := t.TempDir()

	// missing source
	require.Error(t, convert(filepath.Join(dir, "missing.json"), filepath.Join(dir, "out.json")))

	// invalid wrapper JSON
	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{not json"), 0o600))
	require.ErrorContains(t, convert(bad, filepath.Join(dir, "out.json")), "unmarshal wrapper")

	// invalid local data
	badLocal := filepath.Join(dir, "badlocal.json")
	require.NoError(t, os.WriteFile(badLocal, []byte(`{"pub_key":"x","local_data":"bm90IGpzb24="}`), 0o600))
	require.ErrorContains(t, convert(badLocal, filepath.Join(dir, "out.json")), "unmarshal local data")

	// nil PaillierSK
	nilSK := filepath.Join(dir, "nilsk.json")
	require.NoError(t, os.WriteFile(nilSK, []byte(`{"pub_key":"x","local_data":"e30="}`), 0o600))
	require.ErrorContains(t, convert(nilSK, filepath.Join(dir, "out.json")), "nil PaillierSK")

	// unwritable destination (parent directory does not exist)
	require.Error(t, convert(filepath.Join(fixtureDir, "0.json"), filepath.Join(dir, "no", "such", "dir", "out.json")))
}

func TestMainWritesAllFixtures(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, srcDir)
	require.NoError(t, os.MkdirAll(src, 0o755))
	for _, name := range []string{"0.json", "1.json", "2.json", "3.json"} {
		raw, err := os.ReadFile(filepath.Join(fixtureDir, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(src, name), raw, 0o600))
	}

	t.Chdir(root)
	main()

	for _, name := range []string{"0.json", "1.json", "2.json", "3.json"} {
		_, local := readStateCov(t, filepath.Join(root, dstDir, name))
		require.Nil(t, local.PaillierSK.P, name)
		require.Nil(t, local.PaillierSK.Q, name)
	}
}
