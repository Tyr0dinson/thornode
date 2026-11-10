package divergence

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"

	storetypes "cosmossdk.io/store/types"
)

func TestNetPairsDedupesAndSorts(t *testing.T) {
	cs := []*storetypes.StoreKVPair{
		kvp("thorchain", "pool//ETH.ETH", "1"),
		kvp("bank", "b", "x"),
		nil,
		kvp("thorchain", "pool//BTC.BTC", "1"),
		kvp("thorchain", "pool//ETH.ETH", "2"), // last write wins
		del("bank", "a"),
	}
	got := netPairs(cs)
	require.Len(t, got, 4)
	require.Equal(t, "bank", got[0].StoreKey)
	require.Equal(t, "a", string(got[0].Key))
	require.True(t, got[0].Delete)
	require.Equal(t, "b", string(got[1].Key))
	require.Equal(t, "pool//BTC.BTC", string(got[2].Key))
	require.Equal(t, "pool//ETH.ETH", string(got[3].Key))
	require.Equal(t, "2", string(got[3].Value))
}

func TestStoreStatsDigestIsOrderIndependent(t *testing.T) {
	a := storeStats(netPairs([]*storetypes.StoreKVPair{kvp("s", "k1", "v1"), kvp("s", "k2", "v2"), del("s", "k3"), kvp("t", "x", "y")}))
	b := storeStats(netPairs([]*storetypes.StoreKVPair{kvp("t", "x", "y"), del("s", "k3"), kvp("s", "k2", "v2"), kvp("s", "k1", "v1")}))
	require.Equal(t, a, b)
	require.Equal(t, 2, a["s"].Writes)
	require.Equal(t, 1, a["s"].Deletes)
	require.Equal(t, 1, a["t"].Writes)
	require.Len(t, a["s"].Digest, 64)

	// A changed value, a set-vs-delete, and a key/value boundary shift all
	// change the digest.
	c := storeStats(netPairs([]*storetypes.StoreKVPair{kvp("s", "k1", "v1"), kvp("s", "k2", "v3"), del("s", "k3")}))
	require.NotEqual(t, a["s"].Digest, c["s"].Digest)
	d := storeStats(netPairs([]*storetypes.StoreKVPair{kvp("s", "k1", "v1"), kvp("s", "k2", "v2"), kvp("s", "k3", "")}))
	require.NotEqual(t, a["s"].Digest, d["s"].Digest)
	e1 := digestPairs([]*storetypes.StoreKVPair{kvp("s", "ab", "c")})
	e2 := digestPairs([]*storetypes.StoreKVPair{kvp("s", "a", "bc")})
	require.NotEqual(t, e1, e2)
}

func TestToTxResults(t *testing.T) {
	got := toTxResults([]*abci.ExecTxResult{{Code: 3, Codespace: "sdk", GasWanted: 10, GasUsed: 7, Data: []byte("d")}, nil, {}})
	require.Len(t, got, 3)
	require.Equal(t, uint32(3), got[0].Code)
	require.Equal(t, int64(7), got[0].GasUsed)
	require.Len(t, got[0].DataHash, 64)
	require.Equal(t, TxResult{}, got[1])
	require.Empty(t, got[2].DataHash)
}

func TestWriteSetFileNames(t *testing.T) {
	h, ok := parseWriteSetFileName(writeSetFileName(123))
	require.True(t, ok)
	require.Equal(t, int64(123), h)
	for _, bad := range []string{"123.json", "abc.json.gz", "-1.json.gz", ".tmp-1"} {
		_, ok = parseWriteSetFileName(bad)
		require.False(t, ok, bad)
	}
	hs, err := listWriteSetHeights(filepath.Join(t.TempDir(), "missing"))
	require.NoError(t, err)
	require.Empty(t, hs)
}

func TestWriteGzipJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, writeSetFileName(5))
	ws := &WriteSet{Height: 5, AppHash: "AB", Pairs: []KVPair{{Store: "s", Key: "6b", Value: []byte{0, 1, 2}}}}
	require.NoError(t, writeGzipJSON(path, ws))
	got, err := ReadWriteSetFile(path)
	require.NoError(t, err)
	require.Equal(t, ws.Height, got.Height)
	require.Equal(t, ws.Pairs, got.Pairs)

	// no temp files left behind
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	require.Error(t, writeGzipJSON(filepath.Join(dir, "missing", "x.json.gz"), ws))
	_, err = ReadWriteSetFile(filepath.Join(dir, "nope.json.gz"))
	require.Error(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.json.gz"), []byte("not gzip"), 0o600))
	_, err = ReadWriteSetFile(filepath.Join(dir, "bad.json.gz"))
	require.Error(t, err)
	_, err = decodeWriteSet(strings.NewReader("{"), false)
	require.Error(t, err)
}

func TestRecorderRecordsBlocks(t *testing.T) {
	dir := t.TempDir()
	commits := newFakeCommits()
	tracker := NewTracker()
	r, err := NewRecorder(dir, 3, newRecLogger(), commits, tracker)
	require.NoError(t, err)
	defer r.Close()
	require.Equal(t, dir, r.Dir())

	txs := []*abci.ExecTxResult{{Code: 0, GasWanted: 100, GasUsed: 50}, {Code: 5, GasWanted: 10, GasUsed: 10}}
	for h := int64(1); h <= 6; h++ {
		commits.set(h, "", testStores...)
		tracker.BeginBlock()
		tracker.QueryStart()
		commitBlock(t, r, h, txs, []*storetypes.StoreKVPair{
			kvp("thorchain", "pool//BTC.BTC", "v"+string(rune('0'+h))),
			del("bank", "gone"),
		})
		tracker.EndBlock()
	}

	// ring keeps the last 3 heights
	hs, err := listWriteSetHeights(r.WriteSetDir())
	require.NoError(t, err)
	require.Equal(t, []int64{4, 5, 6}, hs)

	ws, err := ReadWriteSetFile(filepath.Join(r.WriteSetDir(), writeSetFileName(6)))
	require.NoError(t, err)
	ci, _ := commits.GetCommitInfo(6)
	require.Equal(t, strings.ToUpper(hex.EncodeToString(ci.Hash())), ws.AppHash)
	require.Len(t, ws.CommitInfo, 3)
	require.Equal(t, "acc", ws.CommitInfo[0].Name)
	require.Equal(t, strings.ToUpper(hex.EncodeToString(cmttypes.NewResults(txs).Hash())), ws.ResultsHash)
	require.Len(t, ws.TxResults, 2)
	require.Equal(t, 1, ws.Stores["thorchain"].Writes)
	require.Equal(t, 1, ws.Stores["bank"].Deletes)
	require.Equal(t, uint64(1), ws.Overlap.QueryOverlap)
	require.Len(t, ws.Pairs, 2)

	// one digest line per block
	f, err := os.Open(filepath.Join(dir, digestsLogName))
	require.NoError(t, err)
	defer f.Close()
	var lines []digestLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l digestLine
		require.NoError(t, json.Unmarshal(sc.Bytes(), &l))
		lines = append(lines, l)
	}
	require.Len(t, lines, 6)
	last := lines[5]
	require.Equal(t, int64(6), last.Height)
	require.Equal(t, ws.AppHash, last.AppHash)
	require.Len(t, last.Commit["thorchain"], abbrev)
	require.Equal(t, ws.Stores["thorchain"].Digest[:abbrev], last.Writes["thorchain"].Digest)
	require.Equal(t, 2, last.Txs)
	require.Equal(t, uint64(1), last.Overlap.Query)

	// a rollback leaves stale higher files; the next commit prunes them
	require.NoError(t, writeGzipJSON(filepath.Join(r.WriteSetDir(), writeSetFileName(50)), &WriteSet{Height: 50}))
	commits.set(7, "", testStores...)
	commitBlock(t, r, 7, nil, nil)
	hs, err = listWriteSetHeights(r.WriteSetDir())
	require.NoError(t, err)
	require.Equal(t, []int64{5, 6, 7}, hs)
	ws, err = ReadWriteSetFile(filepath.Join(r.WriteSetDir(), writeSetFileName(7)))
	require.NoError(t, err)
	require.Empty(t, ws.ResultsHash)
	require.Empty(t, ws.Pairs)
}

func TestRecorderWithoutCommitInfo(t *testing.T) {
	logger := newRecLogger()
	r, err := NewRecorder(t.TempDir(), 0, logger, newFakeCommits(), nil)
	require.NoError(t, err)
	defer r.Close()
	require.Equal(t, int64(1), r.retain)
	commitBlock(t, r, 9, nil, []*storetypes.StoreKVPair{kvp("s", "k", "v")})
	ws, err := ReadWriteSetFile(filepath.Join(r.WriteSetDir(), writeSetFileName(9)))
	require.NoError(t, err)
	require.Equal(t, "09", ws.AppHash) // FinalizeBlock's AppHash as the fallback
	require.Empty(t, ws.CommitInfo)
	require.NotNil(t, logger.find("divergence recorder failed to read commit info"))

	// nil commit source and nil logger also work
	r2, err := NewRecorder(t.TempDir(), 2, nil, nil, nil)
	require.NoError(t, err)
	defer r2.Close()
	commitBlock(t, r2, 1, nil, nil)
}

func TestRecorderHeightFromFinalizeWhenCtxIsPlain(t *testing.T) {
	r, err := NewRecorder(t.TempDir(), 5, nil, nil, nil)
	require.NoError(t, err)
	defer r.Close()
	require.NoError(t, r.ListenFinalizeBlock(context.Background(), abci.RequestFinalizeBlock{Height: 4}, abci.ResponseFinalizeBlock{}))
	require.NoError(t, r.ListenCommit(context.Background(), abci.ResponseCommit{}, nil))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, r.Flush(ctx))
	hs, err := listWriteSetHeights(r.WriteSetDir())
	require.NoError(t, err)
	require.Equal(t, []int64{4}, hs)

	// no height at all: nothing recorded
	require.NoError(t, r.ListenCommit(context.Background(), abci.ResponseCommit{}, nil))
	require.NoError(t, r.Flush(ctx))
	hs, _ = listWriteSetHeights(r.WriteSetDir())
	require.Equal(t, []int64{4}, hs)
}

func TestRecorderDropsWhenQueueFull(t *testing.T) {
	logger := newRecLogger()
	// no writer goroutine and an unbuffered queue: every block is dropped
	r := &Recorder{logger: logger, jobs: make(chan *job), done: make(chan struct{})}
	require.NoError(t, r.ListenCommit(sdkCtxAt(3), abci.ResponseCommit{}, nil))
	require.Equal(t, uint64(1), r.Dropped())
	require.Equal(t, int64(0), r.queued.Load())
	require.NotNil(t, logger.find("divergence recorder queue full, block not recorded"))

	// errors are rate limited to one a minute
	require.NoError(t, r.ListenCommit(sdkCtxAt(4), abci.ResponseCommit{}, nil))
	require.Len(t, logger.msgs, 1)

	ctx, cancel := context.WithCancel(context.Background())
	r.queued.Store(1)
	cancel()
	require.ErrorIs(t, r.Flush(ctx), context.Canceled)
}

type panicCommits struct{}

func (panicCommits) GetCommitInfo(int64) (*storetypes.CommitInfo, error) { panic("boom") }

func TestRecorderRecoversPanics(t *testing.T) {
	logger := newRecLogger()
	r, err := NewRecorder(t.TempDir(), 2, logger, panicCommits{}, nil)
	require.NoError(t, err)
	defer r.Close()
	commitBlock(t, r, 1, nil, nil) // Flush returns: the panicking job still counts as done
	kvs := logger.find("divergence recorder panic recovered")
	require.NotNil(t, kvs)
	require.Equal(t, "process", kv(kvs, "where"))
}

func TestRecorderCloseIsIdempotentAndStopsRecording(t *testing.T) {
	r, err := NewRecorder(t.TempDir(), 2, nil, nil, nil)
	require.NoError(t, err)
	r.Close()
	r.Close()
	require.NoError(t, r.ListenCommit(sdkCtxAt(1), abci.ResponseCommit{}, nil))
	require.Equal(t, int64(0), r.queued.Load())
	r.appendDigest(&WriteSet{Height: 1}) // log closed: no-op
}

func TestRecorderRotatesDigestLog(t *testing.T) {
	old := maxDigestLogBytes
	maxDigestLogBytes = 500
	defer func() { maxDigestLogBytes = old }()

	dir := t.TempDir()
	commits := newFakeCommits()
	r, err := NewRecorder(dir, 2, nil, commits, nil)
	require.NoError(t, err)
	defer r.Close()
	for h := int64(1); h <= 4; h++ {
		commits.set(h, "", testStores...)
		commitBlock(t, r, h, nil, nil)
	}
	_, err = os.Stat(filepath.Join(dir, digestsLogName+".1"))
	require.NoError(t, err)
	st, err := os.Stat(filepath.Join(dir, digestsLogName))
	require.NoError(t, err)
	require.LessOrEqual(t, st.Size(), int64(500))
}

func TestNewRecorderErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	_, err := NewRecorder(file, 2, nil, nil, nil)
	require.Error(t, err)

	// digests.log is a directory: open fails
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, digestsLogName), 0o755))
	_, err = NewRecorder(dir, 2, nil, nil, nil)
	require.Error(t, err)
}

// BenchmarkRecorderBlock measures the background cost of recording one block
// of 2000 net writes with 300 byte incompressible values (a busy block; a
// worst case for gzip), and the cost ListenCommit adds to Commit itself.
func BenchmarkRecorderBlock(b *testing.B) {
	commits := newFakeCommits()
	r, err := NewRecorder(b.TempDir(), 20, nil, commits, NewTracker())
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()
	cs := make([]*storetypes.StoreKVPair, 2000)
	for i := range cs {
		value := make([]byte, 0, 320)
		for seed := []byte{byte(i >> 8), byte(i)}; len(value) < 300; {
			sum := sha256.Sum256(seed)
			value = append(value, sum[:]...)
			seed = sum[:]
		}
		cs[i] = &storetypes.StoreKVPair{StoreKey: testStores[i%3], Key: []byte(hex.EncodeToString([]byte{byte(i >> 8), byte(i)}) + "/key/padding"), Value: value[:300]}
	}
	txs := make([]*abci.ExecTxResult, 50)
	for i := range txs {
		txs[i] = &abci.ExecTxResult{GasWanted: 100, GasUsed: 50}
	}
	b.Run("process", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			h := int64(i + 1)
			commits.set(h, "", testStores...)
			r.process(&job{height: h, txResults: txs, changeSet: cs})
		}
		st, err := os.Stat(filepath.Join(r.WriteSetDir(), writeSetFileName(int64(b.N))))
		if err == nil {
			b.ReportMetric(float64(st.Size()), "writeset_bytes")
		}
	})
	b.Run("listen_commit", func(b *testing.B) {
		// no writer: measures only what Commit itself waits for
		q := &Recorder{jobs: make(chan *job, b.N+1), done: make(chan struct{}), tracker: NewTracker()}
		ctx := sdkCtxAt(1)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = q.ListenFinalizeBlock(ctx, abci.RequestFinalizeBlock{Height: 1}, abci.ResponseFinalizeBlock{TxResults: txs})
			_ = q.ListenCommit(ctx, abci.ResponseCommit{}, cs)
		}
	})
}
