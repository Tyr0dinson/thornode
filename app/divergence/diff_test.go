package divergence

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func hexKey(s string) string { return hex.EncodeToString([]byte(s)) }

func ci(h int64, app string, stores ...string) *CommitInfoResponse {
	c := &CommitInfoResponse{Height: h, AppHash: app}
	for i := 0; i+1 < len(stores); i += 2 {
		c.Stores = append(c.Stores, StoreHash{Name: stores[i], Hash: stores[i+1]})
	}
	return c
}

func TestComputeDiffStoresAndKeys(t *testing.T) {
	big := bytes.Repeat([]byte{0xab}, 100)
	local := &WriteSet{Height: 5, ResultsHash: "R1", TxResults: []TxResult{{GasUsed: 1}, {GasUsed: 2}}, Pairs: []KVPair{
		{Store: "thorchain", Key: hexKey("pool//BTC.BTC"), Value: big},
		{Store: "thorchain", Key: hexKey("vault//x"), Delete: true},
		{Store: "thorchain", Key: hexKey("same/"), Value: []byte("s")},
		{Store: "bank", Key: hexKey("b"), Value: []byte("1")}, // equal store hash: ignored
	}}
	ref := &WriteSet{Height: 5, ResultsHash: "R2", TxResults: []TxResult{{GasUsed: 1}}, Pairs: []KVPair{
		{Store: "thorchain", Key: hexKey("pool//BTC.BTC"), Value: []byte("other")},
		{Store: "thorchain", Key: hexKey("vault//x"), Value: []byte("kept")},
		{Store: "thorchain", Key: hexKey("same/"), Value: []byte("s")},
		{Store: "bank", Key: hexKey("b"), Value: []byte("2")},
		{Store: "wasm", Key: hexKey("w"), Value: []byte("w")},
	}}
	d := ComputeDiff(DiffInput{
		Height:          5,
		Reference:       "http://ref",
		LocalCommit:     ci(5, "AA", "bank", "B1", "thorchain", "T1", "only_local", "X"),
		LocalPrevCommit: ci(4, "P"),
		LocalWriteSet:   local,
		RefCommit:       ci(5, "BB", "bank", "B1", "thorchain", "T2", "wasm", "W"),
		RefPrevCommit:   ci(4, "p"),
		RefWriteSet:     ref,
	})
	require.False(t, d.AppHash.Agrees)
	require.True(t, d.Previous.Agrees) // case-insensitive
	require.Equal(t, []string{"only_local", "thorchain", "wasm"}, d.StoreNames())
	require.Equal(t, "", d.Stores[2].Local)
	require.True(t, d.WriteSetsCompared)
	require.Equal(t, 3, d.TotalKeyDiffs)
	require.Equal(t, "pool//BTC.BTC", d.KeyDiffs[0].Text)
	require.Equal(t, "value_differs", d.KeyDiffs[0].Kind)
	require.Equal(t, "prefixPool", d.KeyDiffs[0].PrefixName)
	require.True(t, d.KeyDiffs[0].Local.Truncated)
	require.Len(t, d.KeyDiffs[0].Local.Hex, 2*valuePreviewBytes)
	require.Equal(t, 100, d.KeyDiffs[0].Local.Len)
	require.Equal(t, "delete_differs", d.KeyDiffs[1].Kind)
	require.True(t, d.KeyDiffs[1].Local.Deleted)
	require.Equal(t, "wasm", d.KeyDiffs[2].Store)
	require.Equal(t, "only_reference", d.KeyDiffs[2].Kind)
	require.False(t, d.KeyDiffs[2].Local.Written)
	require.Equal(t, []TxResultDiff{{Index: 1, Local: &TxResult{GasUsed: 2}}}, d.TxResultDiffs)
	require.Equal(t, "R1", d.LocalResultsHash)
	require.Equal(t, "R2", d.ReferenceResultsHash)
	// only_local has no differing writes: noted
	require.Len(t, d.Notes, 1)
	require.Contains(t, d.Notes[0], "store only_local hash differs but this height's writes to it are identical")

	top := d.TopKeys(2)
	require.Equal(t, []string{"thorchain:pool//BTC.BTC(value_differs)", "thorchain:vault//x(delete_differs)"}, top)
}

func TestComputeDiffPartialInputs(t *testing.T) {
	d := ComputeDiff(DiffInput{Height: 5})
	require.False(t, d.AppHash.Agrees)
	require.Nil(t, d.Previous)
	require.Empty(t, d.Stores)
	require.Equal(t, []string{
		"commit info missing on one side: store hashes not compared",
		"no write set on either side: key-level diff skipped",
	}, d.Notes)

	d = ComputeDiff(DiffInput{Height: 5, RefWriteSet: &WriteSet{}})
	require.Contains(t, d.Notes, "local write set missing: key-level diff skipped")
	d = ComputeDiff(DiffInput{Height: 5, LocalWriteSet: &WriteSet{}})
	require.Contains(t, d.Notes[1], "reference write set missing")

	// previous height disagrees: the divergence started earlier
	d = ComputeDiff(DiffInput{Height: 5, LocalPrevCommit: ci(4, "A"), RefPrevCommit: ci(4, "B")})
	require.False(t, d.Previous.Agrees)
	require.Contains(t, d.Notes[0], "already differs at height 4")

	// no commit info but both write sets: every store compared; binary keys
	// fall back to hex in TopKeys
	d = ComputeDiff(DiffInput{
		Height:        5,
		LocalWriteSet: &WriteSet{Pairs: []KVPair{{Store: "bank", Key: "02ff", Value: []byte{1}}}},
		RefWriteSet:   &WriteSet{Pairs: []KVPair{{Store: "bank", Key: "02FF", Value: []byte{2}}}},
	})
	require.Equal(t, 1, d.TotalKeyDiffs)
	require.Equal(t, []string{"bank:02ff(value_differs)"}, d.TopKeys(5))
	require.Equal(t, "1B:"+d.KeyDiffs[0].Local.SHA256, valueSummary(d.KeyDiffs[0].Local))
	require.Equal(t, "-", valueSummary(ValueInfo{}))
	require.Equal(t, "deleted", valueSummary(ValueInfo{Written: true, Deleted: true}))
}

func TestComputeDiffCapsKeyDiffs(t *testing.T) {
	var local []KVPair
	for i := 0; i < maxKeyDiffs+5; i++ {
		local = append(local, KVPair{Store: "s", Key: hex.EncodeToString([]byte{byte(i >> 8), byte(i)}), Value: []byte{1}})
	}
	d := ComputeDiff(DiffInput{Height: 2, LocalWriteSet: &WriteSet{Pairs: local}, RefWriteSet: &WriteSet{}})
	require.Equal(t, maxKeyDiffs+5, d.TotalKeyDiffs)
	require.Len(t, d.KeyDiffs, maxKeyDiffs)
	require.Contains(t, strings.Join(d.Notes, "\n"), fmt.Sprintf("showing the first %d of %d key diffs", maxKeyDiffs, maxKeyDiffs+5))
}

func TestComputeDiffCapDoesNotHideStoresWrites(t *testing.T) {
	// bank sorts first and fills every slot; thorchain's 5 diffs are cut by the
	// cap but exist, so it must not get the "writes are identical" note.
	// mimir's hash differs with no differing writes: it must get the note.
	var local []KVPair
	for i := 0; i < maxKeyDiffs+500; i++ {
		local = append(local, KVPair{Store: "bank", Key: hex.EncodeToString([]byte{byte(i >> 8), byte(i)}), Value: []byte{1}})
	}
	for i := 0; i < 5; i++ {
		local = append(local, KVPair{Store: "thorchain", Key: hex.EncodeToString([]byte{byte(i)}), Value: []byte{1}})
	}
	commit := func(salt string) *CommitInfoResponse {
		return &CommitInfoResponse{Height: 2, Stores: []StoreHash{
			{Name: "bank", Hash: "B" + salt},
			{Name: "mimir", Hash: "M" + salt},
			{Name: "thorchain", Hash: "T" + salt},
		}}
	}
	d := ComputeDiff(DiffInput{
		Height:        2,
		LocalCommit:   commit("1"),
		RefCommit:     commit("2"),
		LocalWriteSet: &WriteSet{Pairs: local},
		RefWriteSet:   &WriteSet{},
	})
	require.Equal(t, []string{"bank", "mimir", "thorchain"}, d.StoreNames())
	require.Equal(t, maxKeyDiffs+505, d.TotalKeyDiffs)
	require.Len(t, d.KeyDiffs, maxKeyDiffs)
	notes := strings.Join(d.Notes, "\n")
	require.NotContains(t, notes, "store thorchain hash differs but")
	require.NotContains(t, notes, "store bank hash differs but")
	require.Contains(t, notes, "store mimir hash differs but this height's writes to it are identical")
}

func TestReferenceURL(t *testing.T) {
	require.Equal(t, "http://h:1317/thorchain/debug/x", referenceURL("http://h:1317", "/thorchain/debug/x"))
	require.Equal(t, "http://h:1317/thorchain/debug/x", referenceURL(" http://h:1317/ ", "/thorchain/debug/x"))
	require.Equal(t, "http://h/api/thorchain/debug/x", referenceURL("http://h/api/thorchain", "/thorchain/debug/x"))
}

func TestRunReferenceDiffReferenceMissingWriteSet(t *testing.T) {
	// the reference only keeps 1 write set and moved on past height 3
	local, ref, srv := divergedPair(t, 1)
	refHash := appHashAt(t, ref, 3)
	ref.commit(t, 4, "", nil)
	dir, err := local.d.Dump(context.Background(), Trigger{Source: SourceConsensus, Err: appHashErr(appHashAt(t, local, 3), refHash)})
	require.NoError(t, err)
	d, _, err := RunReferenceDiff(context.Background(), srv.Client(), dir, srv.URL, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"thorchain"}, d.StoreNames())
	require.False(t, d.WriteSetsCompared)
	require.True(t, strings.HasPrefix(d.Notes[0], "reference write set: "), d.Notes)
}

func TestRunReferenceDiffForkedReference(t *testing.T) {
	local, ref, srv := divergedPair(t, 20)
	// the failing header's network hash is neither node's
	dir, err := local.d.Dump(context.Background(), Trigger{Source: SourceConsensus, Err: appHashErr(appHashAt(t, local, 3), "0011")})
	require.NoError(t, err)
	d, _, err := RunReferenceDiff(context.Background(), nil, dir, srv.URL, 0)
	require.NoError(t, err)
	require.Contains(t, strings.Join(d.Notes, "\n"), "reference may itself be on a fork")
	_ = ref

	// an explicit earlier height that agrees
	d, _, err = RunReferenceDiff(context.Background(), srv.Client(), dir, srv.URL, 2)
	require.NoError(t, err)
	require.True(t, d.AppHash.Agrees)
	require.Empty(t, d.Stores)
	require.Equal(t, 0, d.TotalKeyDiffs)
}

func TestRunReferenceDiffErrors(t *testing.T) {
	_, _, err := RunReferenceDiff(context.Background(), nil, t.TempDir(), "http://x", 0)
	require.Error(t, err) // no summary.json

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, SummaryFileName), []byte("{"), 0o600))
	_, err = ReadSummary(dir)
	require.ErrorContains(t, err, "decode summary.json")

	require.NoError(t, writeJSON(filepath.Join(dir, SummaryFileName), &Summary{DivergentHeight: 3}))
	_, _, err = RunReferenceDiff(context.Background(), nil, dir, "", 0)
	require.ErrorContains(t, err, "no reference API")

	// reference down: notes, still a diff.json
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, writeSetsDir), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, writeSetsDir, writeSetFileName(3)), []byte("bad"), 0o600))
	d, _, err := RunReferenceDiff(context.Background(), srv.Client(), dir, srv.URL, 0)
	require.NoError(t, err)
	joined := strings.Join(d.Notes, "\n")
	require.Contains(t, joined, "local write set unreadable")
	require.Contains(t, joined, "HTTP 500: nope")
	_, err = os.Stat(filepath.Join(dir, DiffFileName))
	require.NoError(t, err)

	// a local write set with commit info stands in for missing summary commit info
	require.NoError(t, writeGzipJSON(filepath.Join(dir, writeSetsDir, writeSetFileName(3)), &WriteSet{Height: 3, AppHash: "AA", CommitInfo: []StoreHash{{Name: "s", Hash: "1"}}}))
	d, _, err = RunReferenceDiff(context.Background(), srv.Client(), dir, srv.URL, 0)
	require.NoError(t, err)
	require.Equal(t, "AA", d.AppHash.Local)
}

func TestRunReferenceDiffKeepsCrashTimeDiff(t *testing.T) {
	local, _, srv := divergedPair(t, 20)
	dir, err := local.d.Dump(context.Background(), Trigger{Source: SourceConsensus, Err: appHashErr(appHashAt(t, local, 3), "0011")})
	require.NoError(t, err)
	crashPath := filepath.Join(dir, DiffFileName)
	crash, err := os.ReadFile(crashPath)
	require.NoError(t, err)
	require.Contains(t, string(crash), `"write_sets_compared": true`)

	// the reference is gone (or pruned, or the URL is wrong): the rerun gets
	// its own file and the crash-time key-level diff stays
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "pruned", http.StatusBadRequest)
	}))
	defer down.Close()
	d, path, err := RunReferenceDiff(context.Background(), down.Client(), dir, down.URL, 0)
	require.NoError(t, err)
	require.False(t, d.WriteSetsCompared)
	require.NotEqual(t, crashPath, path)
	require.Regexp(t, `/diff-3-\d+\.json$`, path)
	_, path2, err := RunReferenceDiff(context.Background(), down.Client(), dir, down.URL, 0)
	require.NoError(t, err)
	require.NotEqual(t, path, path2)
	got, err := os.ReadFile(crashPath)
	require.NoError(t, err)
	require.Equal(t, crash, got)

	// the same reference with the same evidence still refreshes diff.json
	_, path, err = RunReferenceDiff(context.Background(), srv.Client(), dir, srv.URL, 0)
	require.NoError(t, err)
	require.Equal(t, crashPath, path)

	// another height never touches diff.json, and keeps its own best diff
	d, path, err = RunReferenceDiff(context.Background(), srv.Client(), dir, srv.URL, 2)
	require.NoError(t, err)
	require.True(t, d.WriteSetsCompared)
	require.Equal(t, filepath.Join(dir, "diff-2.json"), path)
	_, path, err = RunReferenceDiff(context.Background(), down.Client(), dir, down.URL, 2)
	require.NoError(t, err)
	require.Regexp(t, `/diff-2-\d+\.json$`, path)
	got, err = os.ReadFile(crashPath)
	require.NoError(t, err)
	require.Equal(t, crash, got)
}

func TestDiffPath(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	main := filepath.Join(dir, DiffFileName)
	weak := &Diff{Height: 3, Reference: "a"}
	withHash := &Diff{Height: 3, Reference: "b", AppHash: HeightCheck{Reference: "AA"}}

	require.Equal(t, main, diffPath(dir, 3, 3, weak, now)) // nothing there yet
	require.NoError(t, writeJSON(main, weak))
	require.Equal(t, main, diffPath(dir, 3, 3, weak, now))     // same evidence, same reference
	require.Equal(t, main, diffPath(dir, 3, 3, withHash, now)) // more evidence, any reference
	require.NoError(t, writeJSON(main, withHash))
	require.Equal(t, filepath.Join(dir, "diff-3-1000.json"), diffPath(dir, 3, 3, weak, now))
	other := &Diff{Height: 3, Reference: "c", AppHash: HeightCheck{Reference: "AA"}}
	require.Equal(t, filepath.Join(dir, "diff-3-1000.json"), diffPath(dir, 3, 3, other, now)) // same evidence, other reference
	// same-second reruns do not collide
	require.NoError(t, writeJSON(filepath.Join(dir, "diff-3-1000.json"), weak))
	require.Equal(t, filepath.Join(dir, "diff-3-1000-1.json"), diffPath(dir, 3, 3, weak, now))

	// an undecodable or other-height diff.json is kept
	require.NoError(t, os.WriteFile(main, []byte("{"), 0o600))
	require.Equal(t, filepath.Join(dir, "diff-3-1000-1.json"), diffPath(dir, 3, 3, withHash, now))
	require.NoError(t, writeJSON(main, &Diff{Height: 7}))
	require.Equal(t, filepath.Join(dir, "diff-3-1000-1.json"), diffPath(dir, 3, 3, withHash, now))

	// a non-divergent height uses diff-<height>.json
	require.Equal(t, filepath.Join(dir, "diff-2.json"), diffPath(dir, 2, 3, weak, now))
}
