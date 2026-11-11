package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	dbm "github.com/cosmos/cosmos-db"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/app/divergence"
)

func enableDivergence(t *testing.T, app *THORChainApp, opts simtestutil.AppOptionsMap) string {
	t.Helper()
	home := t.TempDir()
	if opts == nil {
		opts = simtestutil.AppOptionsMap{}
	}
	require.NoError(t, app.EnableDivergenceDiagnostics(home, opts))
	t.Cleanup(app.divergence.Close)
	return home
}

func commitEmptyBlock(t *testing.T, app *THORChainApp, h int64) []byte {
	t.Helper()
	_, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: h, Time: replayTime.Add(time.Duration(h) * time.Second),
		Hash: bytes.Repeat([]byte{byte(h)}, 32), NextValidatorsHash: bytes.Repeat([]byte{2}, 32),
	})
	require.NoError(t, err)
	_, err = app.Commit()
	require.NoError(t, err)
	return bytes.Clone(app.LastCommitID().Hash)
}

func flushDivergence(t *testing.T, app *THORChainApp) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, app.divergence.Recorder().Flush(ctx))
}

// TestDivergenceDiagnosticsKeepAppHash runs the same blocks with and without
// the recorder: identical app hashes, and the recorder's write sets and
// commit info match what the app committed.
func TestDivergenceDiagnosticsKeepAppHash(t *testing.T) {
	plain := replayApp(t, dbm.NewMemDB())
	recorded := replayApp(t, dbm.NewMemDB())
	home := enableDivergence(t, recorded, nil)
	require.Equal(t, recorded.divergence, divergence.Active())

	hashPlain := commitReplayGenesis(t, plain, replayGenesis(t, plain, 17, false))
	hashRecorded := commitReplayGenesis(t, recorded, replayGenesis(t, recorded, 17, false))
	require.Equal(t, hashPlain, hashRecorded)
	for h := int64(2); h <= 3; h++ {
		require.Equal(t, commitEmptyBlock(t, plain, h), commitEmptyBlock(t, recorded, h))
	}
	require.False(t, recorded.divergence.Tracker().InBlock())
	flushDivergence(t, recorded)

	wsDir := filepath.Join(divergence.DataDir(home), "writesets")
	ws1, err := divergence.ReadWriteSetFile(filepath.Join(wsDir, "1.json.gz"))
	require.NoError(t, err)
	ci1, err := recorded.commitInfoSource().GetCommitInfo(1)
	require.NoError(t, err)
	require.Equal(t, strings.ToUpper(hex.EncodeToString(ci1.Hash())), ws1.AppHash)
	require.NotEmpty(t, ws1.CommitInfo)
	// genesis writes land in block 1's change set
	pool := hex.EncodeToString([]byte("pool//BTC.BTC"))
	found := false
	for _, p := range ws1.Pairs {
		if p.Store == "thorchain" && p.Key == pool {
			found = true
		}
	}
	require.True(t, found, "block 1 write set lacks the genesis BTC pool")
	require.Positive(t, ws1.Stores["thorchain"].Writes)

	ws3, err := divergence.ReadWriteSetFile(filepath.Join(wsDir, "3.json.gz"))
	require.NoError(t, err)
	require.Equal(t, strings.ToUpper(hex.EncodeToString(recorded.LastCommitID().Hash)), ws3.AppHash)

	// the debug endpoints serve the same data
	r := mux.NewRouter()
	divergence.RegisterRoutes(r, recorded.commitInfoSource(), recorded.divergence)
	srv := httptest.NewServer(r)
	defer srv.Close()
	ci, err := divergence.FetchCommitInfo(context.Background(), srv.Client(), srv.URL, 3)
	require.NoError(t, err)
	require.Equal(t, ws3.AppHash, ci.AppHash)

	// gRPC/ABCI query counting goes through the app's Query override
	_, err = recorded.Query(context.Background(), &abci.RequestQuery{Path: "/app/version"})
	require.NoError(t, err)
	require.Equal(t, uint64(1), recorded.divergence.Tracker().Totals().Query)
}

// TestDivergenceDiagnosticsFindsDivergingKeys diverges two apps through their
// genesis and checks that a bundle diffed against the other names the
// thorchain store and the pool keys.
func TestDivergenceDiagnosticsFindsDivergingKeys(t *testing.T) {
	ref := replayApp(t, dbm.NewMemDB())
	enableDivergence(t, ref, nil)
	commitReplayGenesis(t, ref, replayGenesis(t, ref, 17, false))
	flushDivergence(t, ref)
	r := mux.NewRouter()
	divergence.RegisterRoutes(r, ref.commitInfoSource(), ref.divergence)
	srv := httptest.NewServer(r)
	defer srv.Close()

	local := replayApp(t, dbm.NewMemDB())
	home := enableDivergence(t, local, simtestutil.AppOptionsMap{divergence.FlagReferenceAPI: srv.URL, divergence.FlagRetainBlocks: 5})
	localHash := commitReplayGenesis(t, local, replayGenesis(t, local, 18, false))
	flushDivergence(t, local)

	errText := fmt.Sprintf("+2/3 committed an invalid block: wrong Block.Header.AppHash.  Expected %X, got %X", localHash, ref.LastCommitID().Hash)
	divergence.HandleConsensusFailure(errText, divergence.DefaultDumpTimeout)

	matches, err := filepath.Glob(filepath.Join(divergence.BundleDir(home), "bundle-1-*"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	s, err := divergence.ReadSummary(matches[0])
	require.NoError(t, err)
	require.Equal(t, int64(2), s.FailingHeight)
	require.True(t, *s.LocalHashMatchesRecord)
	require.Equal(t, "5", s.Config[divergence.FlagRetainBlocks])

	bz, err := os.ReadFile(filepath.Join(matches[0], divergence.DiffFileName))
	require.NoError(t, err)
	var d divergence.Diff
	require.NoError(t, json.Unmarshal(bz, &d))
	require.False(t, d.AppHash.Agrees)
	require.Contains(t, d.StoreNames(), "thorchain")
	require.True(t, d.WriteSetsCompared)
	pools := 0
	for _, k := range d.KeyDiffs {
		if k.Store == "thorchain" && k.PrefixName == "prefixPool" {
			require.Equal(t, "value_differs", k.Kind)
			pools++
		}
	}
	require.Equal(t, 4, pools, "%+v", d.KeyDiffs)
}

func TestEnableDivergenceDiagnosticsConfig(t *testing.T) {
	app := replayApp(t, dbm.NewMemDB())
	require.NoError(t, app.EnableDivergenceDiagnostics(t.TempDir(), simtestutil.AppOptionsMap{divergence.FlagEnabled: false}))
	require.Nil(t, app.divergence)
	require.ErrorContains(t, app.EnableDivergenceDiagnostics(t.TempDir(), simtestutil.AppOptionsMap{divergence.FlagEnabled: "maybe"}), divergence.FlagEnabled)

	// an unusable home: error, diagnostics stay off
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, "data"), nil, 0o600))
	require.Error(t, app.EnableDivergenceDiagnostics(home, nil))
	require.Nil(t, app.divergence)

	// with diagnostics off the hooks are no-ops and blocks still commit
	commitReplayGenesis(t, app, replayGenesis(t, app, 17, false))
	_, err := app.Query(context.Background(), &abci.RequestQuery{Path: "/app/version"})
	require.NoError(t, err)
	require.NoError(t, app.Close())
}
