package divergence

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func runDiffCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := DiffCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestDiffCmd(t *testing.T) {
	local, ref, srv := divergedPair(t, 20)
	// bundle written without a reference configured, as on most nodes
	local.d.cfg.ReferenceAPI = ""
	dir, err := local.d.Dump(context.Background(), Trigger{Source: SourceConsensus, Err: appHashErr(appHashAt(t, local, 3), appHashAt(t, ref, 3))})
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, DiffFileName))
	require.True(t, os.IsNotExist(err))

	_, err = runDiffCmd(t, "--bundle", dir)
	require.ErrorContains(t, err, "--reference is required")

	out, err := runDiffCmd(t, "--bundle", dir, "--reference", srv.URL, "--top", "1")
	require.NoError(t, err)
	require.Contains(t, out, "height 3: local app hash")
	require.Contains(t, out, "height 2: agree: true")
	require.Contains(t, out, "store thorchain differs")
	require.Contains(t, out, "results hash: local")
	require.Contains(t, out, "tx 0 result differs")
	require.Contains(t, out, "key diffs: 3")
	require.Contains(t, out, "only_local thorchain lp//BTC.BTC/thor1x [prefixLiquidityProvider]")
	require.NotContains(t, out, "pool//BTC.BTC") // --top 1
	require.Contains(t, out, "wrote "+filepath.Join(dir, DiffFileName))
	_, err = os.Stat(filepath.Join(dir, DiffFileName))
	require.NoError(t, err)

	// reference taken from the summary; an explicit height; notes printed
	s, err := ReadSummary(dir)
	require.NoError(t, err)
	s.ReferenceAPI = srv.URL
	require.NoError(t, writeJSON(filepath.Join(dir, SummaryFileName), s))
	out, err = runDiffCmd(t, "--bundle", dir, "--height", "2")
	require.NoError(t, err)
	require.Contains(t, out, "height 2: local app hash")
	require.Contains(t, out, "(agree: true)")
	require.Contains(t, out, "key diffs: 0")

	// a reference without the debug routes: the notes say why
	out, err = runDiffCmd(t, "--bundle", dir, "--reference", srv.URL+"/nothing")
	require.NoError(t, err)
	require.Contains(t, out, "note: reference commit info: ")

	_, err = runDiffCmd(t, "--bundle", filepath.Join(dir, "missing"))
	require.Error(t, err)
	_, err = runDiffCmd(t, "--bundle", filepath.Join(dir, "missing"), "--reference", srv.URL)
	require.Error(t, err)
	_, err = runDiffCmd(t)
	require.ErrorContains(t, err, "bundle")
}
