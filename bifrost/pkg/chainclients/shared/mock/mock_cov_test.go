package mock

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
)

// setupFixturesCov builds a temp directory layout so that the relative fixture
// path used by getJson ("../../../../test/fixtures/") resolves to the repo
// fixtures, then chdirs into it.
func setupFixturesCov(t *testing.T) {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	repoFixtures, err := filepath.Abs(filepath.Join(wd, "../../../../../test/fixtures"))
	require.NoError(t, err)

	root := t.TempDir()
	fixtures := filepath.Join(root, "test", "fixtures")
	require.NoError(t, os.MkdirAll(fixtures, 0o755))
	links := map[string]string{
		"zec":         filepath.Join(repoFixtures, "zec"),
		"lastblock":   filepath.Join(repoFixtures, "endpoints", "lastblock"),
		"nodeaccount": filepath.Join(repoFixtures, "endpoints", "nodeaccount"),
		"tss":         filepath.Join(repoFixtures, "endpoints", "tss"),
	}
	for name, target := range links {
		require.NoError(t, os.Symlink(target, filepath.Join(fixtures, name)))
	}
	cwd := filepath.Join(root, "a", "b", "c", "d")
	require.NoError(t, os.MkdirAll(cwd, 0o755))
	t.Chdir(cwd)
}

func postCov(t *testing.T, url, body string) (int, []byte, error) {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body)) // nolint
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

func getCov(t *testing.T, url string) (int, []byte, error) {
	t.Helper()
	resp, err := http.Get(url) // nolint
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

func TestGetJsonPanicsOnMissingFileCov(t *testing.T) {
	t.Chdir(t.TempDir())
	require.Panics(t, func() { getJson("/does/not/exist") })
}

func TestNonUtxoChainReturnsEmptyCov(t *testing.T) {
	srv := NewChainRpc(common.ETHChain)
	defer srv.Close()
	code, body, err := postCov(t, srv.URL, `{"method":"eth_blockNumber"}`)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, body)
}

func TestUtxoSingleRequestsCov(t *testing.T) {
	setupFixturesCov(t)
	srv := NewChainRpc(common.ZECChain)
	defer srv.Close()

	cases := []struct {
		body   string
		check  func(t *testing.T, m map[string]any)
		id     float64
		method string
	}{
		{
			method: "getblockcount",
			body:   `{"method":"getblockcount","params":[],"id":7}`,
			id:     7,
			check: func(t *testing.T, m map[string]any) {
				require.InDelta(t, 3203735, m["result"], 0)
			},
		},
		{
			method: "getblockhash",
			body:   `{"method":"getblockhash","params":[12],"id":2}`,
			id:     2,
			check: func(t *testing.T, m map[string]any) {
				require.NotEmpty(t, m["result"])
			},
		},
		{
			method: "getblockhash exact",
			body:   `{"method":"getblockhash","params":[3203735],"id":3}`,
			id:     3,
			check: func(t *testing.T, m map[string]any) {
				require.NotEmpty(t, m["result"])
			},
		},
		{
			method: "getrawtransaction default",
			body:   `{"method":"getrawtransaction","params":["ffffffffffff"],"id":4}`,
			id:     4,
			check: func(t *testing.T, m map[string]any) {
				require.NotNil(t, m["result"])
			},
		},
		{
			method: "getrawtransaction a7e7ee",
			body:   `{"method":"getrawtransaction","params":["a7e7ee0000000000"],"id":5}`,
			id:     5,
			check: func(t *testing.T, m map[string]any) {
				require.NotNil(t, m["result"])
			},
		},
		{
			method: "getaddressutxos known",
			body:   `{"method":"getaddressutxos","params":["t1RMxNfNabcdef"],"id":6}`,
			id:     6,
			check: func(t *testing.T, m map[string]any) {
				require.NotNil(t, m["result"])
			},
		},
		{
			method: "getblock verbose",
			body:   `{"method":"getblock","params":["somehash",2],"id":8}`,
			id:     8,
			check: func(t *testing.T, m map[string]any) {
				require.NotNil(t, m["result"])
			},
		},
		{
			method: "getblock default verbosity",
			body:   `{"method":"getblock","params":["3203735"],"id":9}`,
			id:     9,
			check: func(t *testing.T, m map[string]any) {
				require.NotNil(t, m["result"])
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			code, body, err := postCov(t, srv.URL, tc.body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, code)
			var m map[string]any
			require.NoError(t, json.Unmarshal(body, &m))
			require.InDelta(t, tc.id, m["id"], 0)
			tc.check(t, m)
		})
	}
}

func TestUtxoUnknownAddressCov(t *testing.T) {
	setupFixturesCov(t)
	srv := NewChainRpc(common.ZECChain)
	defer srv.Close()
	code, body, err := postCov(t, srv.URL, `{"method":"getaddressutxos","params":["zzzzzzzzzz"],"id":1}`)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	require.JSONEq(t, `{"id":"","result":[],"error":null}`, string(body))
}

func TestUtxoBatchRequestCov(t *testing.T) {
	setupFixturesCov(t)
	srv := NewChainRpc(common.ZECChain)
	defer srv.Close()
	code, body, err := postCov(t, srv.URL,
		`[{"method":"getblockcount","params":[],"id":1},{"method":"getblockhash","params":[3203735],"id":2}]`)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	var arr []map[string]any
	require.NoError(t, json.Unmarshal(body, &arr))
	require.Len(t, arr, 2)
	require.InDelta(t, 1, arr[0]["id"], 0)
	require.InDelta(t, 2, arr[1]["id"], 0)
}

func TestUtxoBadRequestsAbortCov(t *testing.T) {
	setupFixturesCov(t)
	srv := NewChainRpc(common.ZECChain)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	defer srv.Close()
	// each of these panics in the handler, which net/http turns into an
	// aborted connection
	bodies := []string{
		`{not json`,
		`{"method":"getblockhash","params":["x"],"id":1}`,
		`{"method":"getrawtransaction","params":[1],"id":1}`,
		`{"method":"getaddressutxos","params":[],"id":1}`,
		`{"method":"getaddressutxos","params":[1],"id":1}`,
		`{"method":"getblock","params":[1],"id":1}`,
		`{"method":"nosuchmethod","params":[],"id":1}`,
	}
	for _, b := range bodies {
		_, _, err := postCov(t, srv.URL, b)
		require.Error(t, err, b)
	}
}

func TestThornodeApiCov(t *testing.T) {
	setupFixturesCov(t)
	srv := NewThornodeApi()
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	defer srv.Close()

	code, body, err := getCov(t, srv.URL+"/thorchain/node/abc")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	require.NotEmpty(t, body)

	_, body, err = getCov(t, srv.URL+"/auth/accounts/xyz")
	require.NoError(t, err)
	require.Contains(t, string(body), `"account_number":"0"`)

	_, body, err = getCov(t, srv.URL+"/thorchain/lastblock")
	require.NoError(t, err)
	require.Contains(t, string(body), "thorchain")

	_, body, err = getCov(t, srv.URL+"/thorchain/lastblock/zec")
	require.NoError(t, err)
	require.Contains(t, string(body), "ZEC")

	for _, k := range []string{"MaxUTXOsToSpend", "MaxConfirmations-ZEC", "ConfMultiplierBasisPoints-ZEC"} {
		_, body, err = getCov(t, srv.URL+"/thorchain/mimir/key/"+k)
		require.NoError(t, err)
		require.Equal(t, "-1", string(body))
	}

	_, body, err = getCov(t, srv.URL+"/thorchain/vaults")
	require.NoError(t, err)
	require.NotEmpty(t, body)

	// the version and vault sign paths write their text and then fall through
	// to writeJson with an empty file name, which panics and aborts the
	// connection (documents current mock behaviour)
	_, _, err = getCov(t, srv.URL+"/thorchain/version")
	require.Error(t, err)
	_, _, err = getCov(t, srv.URL+"/thorchain/vaults/tthorpub1addwnpepqwznsrgk2t5vn2cszr6ku6zned6tqxknugzw3vhdcjza284d7djp5rql6vn/sign")
	require.Error(t, err)

	_, _, err = getCov(t, srv.URL+"/not/implemented")
	require.Error(t, err)
}
