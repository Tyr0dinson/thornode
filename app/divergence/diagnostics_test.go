package divergence

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
)

// node is a Diagnostics with fake commit info and a settable height.
type node struct {
	d       *Diagnostics
	commits *fakeCommits
	logger  *recLogger
	height  int64
	home    string
}

func newNode(t *testing.T, cfg Config) *node {
	t.Helper()
	n := &node{commits: newFakeCommits(), logger: newRecLogger(), home: t.TempDir()}
	d, err := New(Options{
		Config:      cfg,
		HomeDir:     n.home,
		Logger:      n.logger,
		Commits:     n.commits,
		LastHeight:  func() int64 { return n.height },
		Fingerprint: map[string]string{"inter-block-cache": "true"},
	})
	require.NoError(t, err)
	n.d = d
	t.Cleanup(d.Close)
	return n
}

// commit records height h. salt changes the thorchain store hash, extra adds
// writes to the thorchain store.
func (n *node) commit(t *testing.T, h int64, salt string, txs []*abci.ExecTxResult, extra ...*storetypes.StoreKVPair) {
	t.Helper()
	ci := n.commits.set(h, "", "acc", "bank")
	thor := n.commits.set(h, salt, "thorchain")
	ci.StoreInfos = append(ci.StoreInfos, thor.StoreInfos...)
	n.commits.mu.Lock()
	n.commits.infos[h] = ci
	n.commits.mu.Unlock()

	cs := append([]*storetypes.StoreKVPair{
		kvp("bank", "balance", fmt.Sprint(h)),
		kvp("thorchain", "network//", fmt.Sprint(h)),
	}, extra...)
	n.d.Tracker().BeginBlock()
	commitBlock(t, n.d.Recorder(), h, txs, cs)
	n.d.Tracker().EndBlock()
	n.height = h
}

func (n *node) serve(t *testing.T) *httptest.Server {
	t.Helper()
	r := mux.NewRouter()
	RegisterRoutes(r, n.commits, n.d)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func appHashErr(local, network string) string {
	return fmt.Sprintf("+2/3 committed an invalid block: wrong Block.Header.AppHash.  Expected %s, got %s", local, network)
}

// divergedPair builds a local node that diverged from the reference at height
// 3: same writes at 1 and 2, different thorchain writes and hash at 3.
func divergedPair(t *testing.T, refRetain int) (local, ref *node, srv *httptest.Server) {
	t.Helper()
	ref = newNode(t, Config{Enabled: true, RetainBlocks: refRetain})
	srv = ref.serve(t)
	local = newNode(t, Config{Enabled: true, RetainBlocks: 20, ReferenceAPI: srv.URL})
	txs := []*abci.ExecTxResult{{GasWanted: 10, GasUsed: 5}}
	for h := int64(1); h <= 2; h++ {
		local.commit(t, h, "", txs)
		ref.commit(t, h, "", txs)
	}
	local.commit(t, 3, "local", []*abci.ExecTxResult{{GasWanted: 10, GasUsed: 6}},
		kvp("thorchain", "pool//BTC.BTC", "local-value"),
		kvp("thorchain", "lp//BTC.BTC/thor1x", "only-local"))
	ref.commit(t, 3, "", txs,
		kvp("thorchain", "pool//BTC.BTC", "ref-value"),
		del("thorchain", "txout//3"))
	return local, ref, srv
}

func appHashAt(t *testing.T, n *node, h int64) string {
	t.Helper()
	ws, err := ReadWriteSetFile(filepath.Join(n.d.Recorder().WriteSetDir(), writeSetFileName(h)))
	require.NoError(t, err)
	return ws.AppHash
}

func TestDumpWritesBundleAndDiff(t *testing.T) {
	local, ref, srv := divergedPair(t, 20)
	localHash, refHash := appHashAt(t, local, 3), appHashAt(t, ref, 3)
	require.NotEqual(t, localHash, refHash)

	dir, err := local.d.Dump(context.Background(), Trigger{Source: SourceConsensus, Err: appHashErr(localHash, refHash)})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(filepath.Base(dir), "bundle-3-"), dir)
	require.Equal(t, BundleDir(local.home), filepath.Dir(dir))

	s, err := ReadSummary(dir)
	require.NoError(t, err)
	require.Equal(t, SourceConsensus, s.Source)
	require.Equal(t, int64(4), s.FailingHeight)
	require.Equal(t, int64(3), s.DivergentHeight)
	require.Equal(t, int64(3), s.LastCommittedHeight)
	require.Equal(t, &Mismatch{Type: HashTypeAppHash, Local: localHash, Network: refHash}, s.Mismatch)
	require.NotNil(t, s.LocalHashMatchesRecord)
	require.True(t, *s.LocalHashMatchesRecord)
	require.Equal(t, []int64{1, 2, 3}, s.WriteSetHeights)
	require.Len(t, s.CommitInfo, 3)
	require.Len(t, s.OverlapByHeight, 3)
	require.Equal(t, "true", s.Config["inter-block-cache"])
	require.Equal(t, srv.URL, s.ReferenceAPI)
	require.Equal(t, DiffCommand(dir, srv.URL), s.DiffCommand)
	require.NotEmpty(t, s.Build.GoVersion)
	require.Positive(t, s.Process.PID)
	require.Empty(t, s.Errors)
	for _, h := range s.WriteSetHeights {
		_, err = os.Stat(filepath.Join(dir, writeSetsDir, writeSetFileName(h)))
		require.NoError(t, err)
	}

	bz, err := os.ReadFile(filepath.Join(dir, DiffFileName))
	require.NoError(t, err)
	var d Diff
	require.NoError(t, json.Unmarshal(bz, &d))
	require.Equal(t, int64(3), d.Height)
	require.False(t, d.AppHash.Agrees)
	require.NotNil(t, d.Previous)
	require.True(t, d.Previous.Agrees)
	require.Equal(t, []string{"thorchain"}, d.StoreNames())
	require.True(t, d.WriteSetsCompared)
	require.Equal(t, 3, d.TotalKeyDiffs)
	kinds := map[string]string{}
	for _, k := range d.KeyDiffs {
		kinds[k.Text] = k.Kind
	}
	require.Equal(t, map[string]string{
		"lp//BTC.BTC/thor1x": "only_local",
		"pool//BTC.BTC":      "value_differs",
		"txout//3":           "only_reference",
	}, kinds)
	require.Len(t, d.TxResultDiffs, 1)
	require.NotEqual(t, d.LocalResultsHash, d.ReferenceResultsHash)

	kvs := local.logger.find(MsgDivergenceBundle)
	require.NotNil(t, kvs)
	require.Equal(t, dir, kv(kvs, "bundle"))
	require.Equal(t, "thorchain", kv(kvs, "divergent_stores"))
	require.Equal(t, 3, kv(kvs, "key_diffs"))
	require.Contains(t, kv(kvs, "top_keys"), "thorchain:pool//BTC.BTC(value_differs)")
	require.Equal(t, s.DiffCommand, kv(kvs, "next"))
	require.Equal(t, int64(4), kv(kvs, "failing_height"))

	// same height again: no second bundle
	again, err := local.d.Dump(context.Background(), Trigger{Source: SourceBlocksync})
	require.NoError(t, err)
	require.Equal(t, dir, again)

	// a restarted process (new Diagnostics, same home) finds it on disk
	d2, err := New(Options{Config: Config{Enabled: true, RetainBlocks: 20}, HomeDir: local.home, LastHeight: func() int64 { return 3 }})
	require.NoError(t, err)
	defer d2.Close()
	again, err = d2.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.NoError(t, err)
	require.Equal(t, dir, again)
	matches, _ := filepath.Glob(filepath.Join(BundleDir(local.home), "bundle-*"))
	require.Len(t, matches, 1)
}

func TestDumpLastResultsHash(t *testing.T) {
	n := newNode(t, Config{Enabled: true, RetainBlocks: 5})
	txs := []*abci.ExecTxResult{{Code: 1, GasWanted: 3, GasUsed: 2}}
	n.commit(t, 1, "", txs)
	ws, err := ReadWriteSetFile(filepath.Join(n.d.Recorder().WriteSetDir(), writeSetFileName(1)))
	require.NoError(t, err)
	errText := fmt.Sprintf("wrong Block.Header.LastResultsHash.  Expected %s, got %s", ws.ResultsHash, "00")
	dir, err := n.d.Dump(context.Background(), Trigger{Source: SourceBlocksync, Err: errText})
	require.NoError(t, err)
	s, err := ReadSummary(dir)
	require.NoError(t, err)
	require.Equal(t, HashTypeLastResultsHash, s.Mismatch.Type)
	require.True(t, *s.LocalHashMatchesRecord)
	require.Equal(t, DiffCommand(dir, ""), s.DiffCommand)
	require.Contains(t, s.DiffCommand, "<reference-api-url>")
	_, err = os.Stat(filepath.Join(dir, DiffFileName))
	require.True(t, os.IsNotExist(err))
	kvs := n.logger.find(MsgDivergenceBundle)
	require.Equal(t, HashTypeLastResultsHash, kv(kvs, "type"))
	require.Nil(t, kv(kvs, "diff"))
}

func TestDumpWithEmptyRingUsesCommitInfoFallback(t *testing.T) {
	n := newNode(t, Config{Enabled: true, RetainBlocks: 5})
	n.commits.set(9, "", testStores...)
	n.commits.set(10, "", testStores...)
	n.height = 10
	dir, err := n.d.Dump(context.Background(), Trigger{Source: SourceConsensus, Err: "some other panic"})
	require.NoError(t, err)
	s, err := ReadSummary(dir)
	require.NoError(t, err)
	require.Nil(t, s.Mismatch)
	require.Nil(t, s.LocalHashMatchesRecord)
	require.Empty(t, s.WriteSetHeights)
	require.Len(t, s.CommitInfo, 2)
	require.Equal(t, int64(9), s.CommitInfo[0].Height)
	// not a hash mismatch: bundled, but not announced as a divergence
	require.NotNil(t, n.logger.find(MsgConsensusFailureBundle))
	require.Nil(t, n.logger.find(MsgDivergenceBundle))

	// commit info missing for the fallback heights is reported, not fatal
	m := newNode(t, Config{Enabled: true, RetainBlocks: 5})
	m.height = 4
	dir, err = m.d.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.NoError(t, err)
	s, err = ReadSummary(dir)
	require.NoError(t, err)
	require.Len(t, s.Errors, 2)
}

func TestDumpReportsDiffError(t *testing.T) {
	n := newNode(t, Config{Enabled: true, RetainBlocks: 5, ReferenceAPI: "http://127.0.0.1:1"})
	n.commit(t, 1, "", nil)
	// divergent height 0 (failing height 1): RunReferenceDiff refuses it
	n.height = 0
	_, err := n.d.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.NoError(t, err)
	kvs := n.logger.find(MsgConsensusFailureBundle)
	require.Contains(t, kv(kvs, "diff_err"), "no divergent height")
}

func TestDumpFailsOnUnwritableDir(t *testing.T) {
	n := newNode(t, Config{Enabled: true, RetainBlocks: 5})
	n.height = 2
	// a file where the bundle directory should go
	require.NoError(t, os.RemoveAll(n.d.bundleDir))
	require.NoError(t, os.WriteFile(n.d.bundleDir, nil, 0o600))
	_, err := n.d.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.Error(t, err)
	require.NotNil(t, n.logger.find("divergence bundle failed"))

	// the failed dump releases its reservation: once the disk is fixed, the
	// same height is retried in the same process (the blocksync path)
	_, done := n.d.alreadyDumped(2)
	require.False(t, done)
	require.NoError(t, os.Remove(n.d.bundleDir))
	require.NoError(t, os.MkdirAll(n.d.bundleDir, 0o755))
	dir, err := n.d.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.NoError(t, err)
	require.NotEmpty(t, dir)
	_, err = ReadSummary(dir)
	require.NoError(t, err)
}

func TestDumpRetriesIncompleteBundle(t *testing.T) {
	n := newNode(t, Config{Enabled: true, RetainBlocks: 5})
	n.commit(t, 1, "", nil)
	n.commit(t, 2, "", nil)
	// an earlier run was killed mid-dump: the directory and a write set exist,
	// summary.json does not (only its temp file)
	partial := filepath.Join(n.d.bundleDir, "bundle-2-1")
	require.NoError(t, os.MkdirAll(filepath.Join(partial, writeSetsDir), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(partial, SummaryFileName+".tmp"), []byte("{"), 0o600))
	_, err := ReadSummary(partial)
	require.Error(t, err)

	dir, err := n.d.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.NoError(t, err)
	require.NotEqual(t, partial, dir)
	s, err := ReadSummary(dir)
	require.NoError(t, err)
	require.Equal(t, int64(2), s.DivergentHeight)
	_, err = os.Stat(filepath.Join(dir, SummaryFileName+".tmp"))
	require.True(t, os.IsNotExist(err))
	// the abandoned attempt is deleted, not left to fill the disk
	_, err = os.Stat(partial)
	require.True(t, os.IsNotExist(err))

	// the complete bundle now suppresses a restarted process's retry
	d2, err := New(Options{Config: Config{Enabled: true, RetainBlocks: 5}, HomeDir: n.home, LastHeight: func() int64 { return 2 }})
	require.NoError(t, err)
	defer d2.Close()
	again, err := d2.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.NoError(t, err)
	require.Equal(t, dir, again)
}

func TestDumpRetriesBundleWithFailedCopy(t *testing.T) {
	n := newNode(t, Config{Enabled: true, RetainBlocks: 5})
	n.commit(t, 1, "", nil)
	n.commit(t, 2, "", nil)
	// height 1's write set cannot be copied into the bundle
	orig := failHook
	defer func() { failHook = orig }()
	failHook = func(path string) error {
		if filepath.Base(path) == writeSetFileName(1) {
			return os.ErrPermission
		}
		return nil
	}

	first, err := n.d.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.NoError(t, err)
	s, err := ReadSummary(first)
	require.NoError(t, err)
	require.False(t, s.Complete)
	require.NotEmpty(t, s.Errors)
	require.Equal(t, false, kv(n.logger.find(MsgConsensusFailureBundle), "complete"))
	// kept as evidence, but the height is not marked done
	_, done := n.d.alreadyDumped(2)
	require.False(t, done)

	// a restarted process retries too, within the same second: it gets its
	// own directory rather than writing into the first attempt's
	failHook = nil
	d2, err := New(Options{Config: Config{Enabled: true, RetainBlocks: 5}, HomeDir: n.home, LastHeight: func() int64 { return 2 }})
	require.NoError(t, err)
	defer d2.Close()
	second, err := d2.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	s, err = ReadSummary(second)
	require.NoError(t, err)
	require.True(t, s.Complete)
	require.Empty(t, s.Errors)
	// the complete bundle supersedes the incomplete one
	matches, _ := filepath.Glob(filepath.Join(BundleDir(n.home), "bundle-2-*"))
	require.Equal(t, []string{second}, matches)
	again, err := d2.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.NoError(t, err)
	require.Equal(t, second, again)
}

// An incomplete retry must not delete an earlier incomplete bundle: that one
// may hold a write set the retry could not copy. Huginn's reproduction.
func TestDumpIncompleteRetryKeepsEarlierBundle(t *testing.T) {
	n := newNode(t, Config{Enabled: true, RetainBlocks: 5})
	n.commit(t, 1, "", nil)
	n.commit(t, 2, "", nil)
	orig := failHook
	defer func() { failHook = orig }()
	failOn := func(h int64) func(string) error {
		return func(path string) error {
			if filepath.Base(path) == writeSetFileName(h) {
				return os.ErrPermission
			}
			return nil
		}
	}

	// first attempt keeps the divergent height 2, loses height 1
	failHook = failOn(1)
	first, err := n.d.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.NoError(t, err)
	_, err = ReadWriteSetFile(filepath.Join(first, writeSetsDir, writeSetFileName(2)))
	require.NoError(t, err)

	// the retry loses height 2: both incomplete bundles stay
	failHook = failOn(2)
	second, err := n.d.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	s, err := ReadSummary(second)
	require.NoError(t, err)
	require.False(t, s.Complete)
	_, err = ReadWriteSetFile(filepath.Join(first, writeSetsDir, writeSetFileName(2)))
	require.NoError(t, err, "an incomplete retry deleted the only bundled copy of the divergent write set")

	// a complete attempt then replaces both
	failHook = nil
	third, err := n.d.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.NoError(t, err)
	matches, _ := filepath.Glob(filepath.Join(BundleDir(n.home), "bundle-2-*"))
	require.Equal(t, []string{third}, matches)
}

// A failure that never clears cannot add a bundle per retry: after
// maxIncompleteBundles incomplete bundles the height counts as done.
func TestDumpCapsIncompleteBundles(t *testing.T) {
	n := newNode(t, Config{Enabled: true, RetainBlocks: 5})
	n.commit(t, 1, "", nil)
	n.commit(t, 2, "", nil)
	orig := failHook
	defer func() { failHook = orig }()
	failHook = func(path string) error {
		if filepath.Base(path) == writeSetFileName(1) {
			return os.ErrPermission
		}
		return nil
	}

	var last string
	for i := 0; i < maxIncompleteBundles; i++ {
		dir, err := n.d.Dump(context.Background(), Trigger{Source: SourceBlocksync})
		require.NoError(t, err)
		require.NotEqual(t, last, dir)
		last = dir
	}
	_, done := n.d.alreadyDumped(2)
	require.False(t, done)

	// the next trigger, and a restarted process, stop at the cap
	again, err := n.d.Dump(context.Background(), Trigger{Source: SourceBlocksync})
	require.NoError(t, err)
	require.NotEmpty(t, again)
	require.NotNil(t, n.logger.find("divergence bundle retry limit reached: keeping the incomplete bundles"))
	_, done = n.d.alreadyDumped(2)
	require.True(t, done)
	d2, err := New(Options{Config: Config{Enabled: true, RetainBlocks: 5}, HomeDir: n.home, LastHeight: func() int64 { return 2 }})
	require.NoError(t, err)
	defer d2.Close()
	_, err = d2.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.NoError(t, err)
	matches, _ := filepath.Glob(filepath.Join(BundleDir(n.home), "bundle-2-*"))
	require.Len(t, matches, maxIncompleteBundles)
}

func TestNewestBundle(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	require.NoError(t, os.Mkdir(a, 0o755))
	require.NoError(t, os.Mkdir(b, 0o755))
	require.NoError(t, os.Chtimes(a, time.Unix(200, 0), time.Unix(200, 0)))
	require.NoError(t, os.Chtimes(b, time.Unix(100, 0), time.Unix(100, 0)))
	require.Equal(t, a, newestBundle([]string{b, a}))
	require.Equal(t, b, newestBundle([]string{b, filepath.Join(dir, "missing")}))
}

func TestNewBundleDirIsUniquePerAttempt(t *testing.T) {
	n := newNode(t, Config{Enabled: true, RetainBlocks: 5})
	now := time.Unix(100, 0)
	a, err := n.d.newBundleDir(7, now)
	require.NoError(t, err)
	b, err := n.d.newBundleDir(7, now)
	require.NoError(t, err)
	c, err := n.d.newBundleDir(7, now)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(n.d.bundleDir, "bundle-7-100"), a)
	require.Equal(t, a+"-1", b)
	require.Equal(t, a+"-2", c)
}

func TestDumpFlushTimeoutIsIncomplete(t *testing.T) {
	n := newNode(t, Config{Enabled: true, RetainBlocks: 5})
	n.commit(t, 1, "", nil)
	// a block still queued in the recorder when the dump starts
	n.d.recorder.queued.Add(1)
	defer n.d.recorder.queued.Add(-1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	dir, err := n.d.Dump(ctx, Trigger{Source: SourceConsensus})
	require.NoError(t, err)
	s, err := ReadSummary(dir)
	require.NoError(t, err)
	require.False(t, s.Complete)
	require.Contains(t, strings.Join(s.Errors, "\n"), "recorder flush")
	_, done := n.d.alreadyDumped(1)
	require.False(t, done)
}

func TestDumpListFailureIsIncomplete(t *testing.T) {
	n := newNode(t, Config{Enabled: true, RetainBlocks: 5})
	n.commit(t, 1, "", nil)
	// the ring cannot be listed (a file where its directory should be)
	ws := n.d.Recorder().WriteSetDir()
	require.NoError(t, os.RemoveAll(ws))
	require.NoError(t, os.WriteFile(ws, nil, 0o600))
	dir, err := n.d.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.NoError(t, err)
	s, err := ReadSummary(dir)
	require.NoError(t, err)
	require.False(t, s.Complete)
	require.Contains(t, strings.Join(s.Errors, "\n"), "list write sets")
}

func TestDumpRemovesBundleWhenSummaryFails(t *testing.T) {
	n := newNode(t, Config{Enabled: true, RetainBlocks: 5})
	n.commit(t, 1, "", nil)
	n.height = 1
	// the summary write fails after the write sets were copied (disk full)
	orig := failHook
	defer func() { failHook = orig }()
	failHook = func(path string) error {
		if filepath.Base(path) == SummaryFileName {
			return os.ErrPermission
		}
		return nil
	}
	_, err := n.d.Dump(context.Background(), Trigger{Source: SourceConsensus})
	require.ErrorIs(t, err, os.ErrPermission)
	matches, _ := filepath.Glob(filepath.Join(BundleDir(n.home), "bundle-1-*"))
	require.Empty(t, matches)
	_, done := n.d.alreadyDumped(1)
	require.False(t, done)
}

func TestDumpFetchesDivergentCommitInfoWhenWriteSetMissing(t *testing.T) {
	ref := newNode(t, Config{Enabled: true, RetainBlocks: 20})
	srv := ref.serve(t)
	local := newNode(t, Config{Enabled: true, RetainBlocks: 20, ReferenceAPI: srv.URL})
	for _, n := range []*node{local, ref} {
		n.commit(t, 9, "", nil)
	}
	ref.commit(t, 10, "", nil)
	// height 10 committed locally with a different thorchain hash, but the
	// recorder dropped its write set (queue full or write error): the ring
	// holds only height 9
	ci := local.commits.set(10, "", "acc", "bank")
	thor := local.commits.set(10, "local", "thorchain")
	ci.StoreInfos = append(ci.StoreInfos, thor.StoreInfos...)
	local.commits.mu.Lock()
	local.commits.infos[10] = ci
	local.commits.mu.Unlock()
	local.height = 10
	localHash := strings.ToUpper(hex.EncodeToString(ci.Hash()))

	dir, err := local.d.Dump(context.Background(), Trigger{Source: SourceConsensus, Err: appHashErr(localHash, appHashAt(t, ref, 10))})
	require.NoError(t, err)
	s, err := ReadSummary(dir)
	require.NoError(t, err)
	require.Equal(t, []int64{9}, s.WriteSetHeights)
	heights := []int64{}
	for _, c := range s.CommitInfo {
		heights = append(heights, c.Height)
	}
	require.Equal(t, []int64{9, 10}, heights)
	require.NotNil(t, s.LocalHashMatchesRecord)
	require.True(t, *s.LocalHashMatchesRecord)

	d, _, err := RunReferenceDiff(context.Background(), srv.Client(), dir, srv.URL, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"thorchain"}, d.StoreNames())
}

func TestHandleConsensusFailure(t *testing.T) {
	SetActive(nil)
	HandleConsensusFailure("x", time.Second) // nothing registered: no-op
	HandleBlocksyncError("wrong Block.Header.AppHash.  Expected AA, got BB")

	n := newNode(t, Config{Enabled: true, RetainBlocks: 5})
	n.commit(t, 1, "", nil)
	SetActive(n.d)
	require.Equal(t, n.d, Active())
	HandleConsensusFailure(appHashErr(appHashAt(t, n, 1), "BB"), 10*time.Second)
	matches, _ := filepath.Glob(filepath.Join(BundleDir(n.home), "bundle-1-*"))
	require.Len(t, matches, 1)
	n.d.Close()
	require.Nil(t, Active())
}

type slowCommits struct{ release chan struct{} }

func (s slowCommits) GetCommitInfo(int64) (*storetypes.CommitInfo, error) {
	<-s.release
	return nil, fmt.Errorf("no commit info found")
}

func TestHandleConsensusFailureTimesOut(t *testing.T) {
	logger := newRecLogger()
	sc := slowCommits{release: make(chan struct{})}
	home := t.TempDir()
	d, err := New(Options{Config: Config{Enabled: true, RetainBlocks: 2}, HomeDir: home, Logger: logger, Commits: sc, LastHeight: func() int64 { return 5 }})
	require.NoError(t, err)
	defer d.Close()
	SetActive(d)
	defer SetActive(nil)
	start := time.Now()
	HandleConsensusFailure("x", 50*time.Millisecond)
	require.Less(t, time.Since(start), 5*time.Second)
	require.NotNil(t, logger.find("divergence bundle timed out"))

	// let the abandoned dump finish before the temp dir is removed
	close(sc.release)
	require.Eventually(t, func() bool {
		return logger.find(MsgConsensusFailureBundle) != nil
	}, 10*time.Second, 10*time.Millisecond)
}

func TestHandleBlocksyncErrorOncePerHeight(t *testing.T) {
	n := newNode(t, Config{Enabled: true, RetainBlocks: 5})
	n.commit(t, 1, "", nil)
	SetActive(n.d)
	defer SetActive(nil)

	HandleBlocksyncError("some unrelated validation error") // ignored
	errText := "wrong Block.Header.AppHash.  Expected " + appHashAt(t, n, 1) + ", got BB"
	HandleBlocksyncError(errText)
	require.Eventually(t, func() bool {
		return n.logger.find(MsgDivergenceBundle) != nil
	}, 10*time.Second, 10*time.Millisecond)
	for i := 0; i < 5; i++ {
		HandleBlocksyncError(errText)
	}
	time.Sleep(50 * time.Millisecond)
	matches, _ := filepath.Glob(filepath.Join(BundleDir(n.home), "bundle-*"))
	require.Len(t, matches, 1)
	s, err := ReadSummary(matches[0])
	require.NoError(t, err)
	require.Equal(t, SourceBlocksync, s.Source)
}

func TestNilDiagnostics(t *testing.T) {
	var d *Diagnostics
	require.Nil(t, d.Recorder())
	require.Nil(t, d.Tracker())
	d.Close()
}

func TestNewDiagnosticsError(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, "data"), nil, 0o600))
	_, err := New(Options{Config: DefaultConfig(), HomeDir: home})
	require.Error(t, err)
}

func TestCopyFileErrors(t *testing.T) {
	dir := t.TempDir()
	require.Error(t, copyFile(filepath.Join(dir, "missing"), filepath.Join(dir, "out")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "in"), []byte("x"), 0o600))
	require.Error(t, copyFile(filepath.Join(dir, "in"), filepath.Join(dir, "missing", "out")))
	require.NoError(t, copyFile(filepath.Join(dir, "in"), filepath.Join(dir, "out")))
	bz, err := os.ReadFile(filepath.Join(dir, "out"))
	require.NoError(t, err)
	require.True(t, bytes.Equal([]byte("x"), bz))
	require.Error(t, writeJSON(filepath.Join(dir, "x.json"), func() {}))
}

func TestEndpoints(t *testing.T) {
	n := newNode(t, Config{Enabled: true, RetainBlocks: 5})
	n.commit(t, 1, "", nil, kvp("thorchain", "pool//BTC.BTC", "v"))
	srv := n.serve(t)

	ci, err := FetchCommitInfo(context.Background(), srv.Client(), srv.URL, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), ci.Height)
	require.Equal(t, appHashAt(t, n, 1), ci.AppHash)
	require.Len(t, ci.Stores, 3)

	// trailing /thorchain on the base is accepted
	ws, err := FetchWriteSet(context.Background(), srv.Client(), srv.URL+"/thorchain/", 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), ws.Height)
	require.Len(t, ws.Pairs, 3)

	get := func(path string, gzipped bool) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if gzipped {
			req.Header.Set("Accept-Encoding", "gzip")
		} else {
			req.Header.Set("Accept-Encoding", "identity")
		}
		resp, rtErr := http.DefaultTransport.RoundTrip(req)
		require.NoError(t, rtErr)
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	resp := get("/thorchain/debug/writeset/1", true)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
	resp = get("/thorchain/debug/writeset/1", false)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, resp.Header.Get("Content-Encoding"))
	got, err := decodeWriteSet(resp.Body, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), got.Height)

	require.Equal(t, http.StatusNotFound, get("/thorchain/debug/writeset/2", false).StatusCode)
	require.Equal(t, http.StatusNotFound, get("/thorchain/debug/commit_info/2", false).StatusCode)
	require.Equal(t, http.StatusBadRequest, get("/thorchain/debug/commit_info/abc", false).StatusCode)
	require.Equal(t, http.StatusBadRequest, get("/thorchain/debug/writeset/0", false).StatusCode)
	_, err = FetchWriteSet(context.Background(), srv.Client(), srv.URL, 2)
	require.ErrorIs(t, err, ErrNotFound)

	// a corrupt ring file
	require.NoError(t, os.WriteFile(filepath.Join(n.d.Recorder().WriteSetDir(), writeSetFileName(7)), []byte("x"), 0o600))
	require.Equal(t, http.StatusInternalServerError, get("/thorchain/debug/writeset/7", false).StatusCode)

	// diagnostics disabled / no commit source
	r := mux.NewRouter()
	RegisterRoutes(r, nil, nil)
	off := httptest.NewServer(r)
	defer off.Close()
	resp2, err := http.Get(off.URL + "/thorchain/debug/writeset/1")
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusNotFound, resp2.StatusCode)
	_, err = FetchCommitInfo(context.Background(), off.Client(), off.URL, 1)
	require.ErrorContains(t, err, "HTTP 503")
	_, err = FetchCommitInfo(context.Background(), off.Client(), "http://[::1", 1)
	require.Error(t, err)
}
