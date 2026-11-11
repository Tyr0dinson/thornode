package divergence

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmttypes "github.com/cometbft/cometbft/types"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

const (
	digestsLogName = "digests.log"
	writeSetsDir   = "writesets"
	// queueSize bounds the blocks waiting for the background writer. A full
	// queue drops the block's record (counted) instead of blocking Commit.
	queueSize = 16
	// abbrev is how many hex chars of store hashes go in digests.log. The full
	// values are in the write sets and in the commit info endpoint.
	abbrev = 16
)

// maxDigestLogBytes rotates digests.log to digests.log.1 (one generation
// kept). A line is ~1 KB, so the pair holds roughly 3-6 days of blocks. A var
// so tests can shrink it.
var maxDigestLogBytes int64 = 32 << 20

// CommitInfoSource reads committed store hashes; *rootmulti.Store implements it.
type CommitInfoSource interface {
	GetCommitInfo(ver int64) (*storetypes.CommitInfo, error)
}

// Recorder is a streaming ABCIListener that, after every Commit, appends one
// line of per-store hashes and write digests to digests.log and keeps the
// full write set of the last RetainBlocks heights under writesets/.
//
// It never fails or blocks consensus: ListenCommit only hands the change set to
// a background writer (dropping it if the queue is full), both hooks recover
// panics, and both always return nil.
type Recorder struct {
	dir     string
	wsDir   string
	retain  int64
	logger  log.Logger
	commits CommitInfoSource
	tracker *Tracker

	mu      sync.Mutex
	pending *finalized

	jobs    chan *job
	queued  atomic.Int64
	dropped atomic.Uint64
	done    chan struct{}
	closed  atomic.Bool

	logMu   sync.Mutex
	logFile *os.File
	logSize int64

	lastMu      sync.Mutex
	lastErrTime time.Time
}

type finalized struct {
	height    int64
	appHash   []byte
	txResults []*abci.ExecTxResult
}

type job struct {
	height    int64
	appHash   []byte
	txResults []*abci.ExecTxResult
	changeSet []*storetypes.StoreKVPair
	overlap   Overlap
}

var _ storetypes.ABCIListener = (*Recorder)(nil)

// NewRecorder creates dir/writesets and dir/digests.log and starts the writer.
func NewRecorder(dir string, retain int, logger log.Logger, commits CommitInfoSource, tracker *Tracker) (*Recorder, error) {
	if retain < 1 {
		retain = 1
	}
	if logger == nil {
		logger = log.NewNopLogger()
	}
	r := &Recorder{
		dir:     dir,
		wsDir:   filepath.Join(dir, writeSetsDir),
		retain:  int64(retain),
		logger:  logger,
		commits: commits,
		tracker: tracker,
		jobs:    make(chan *job, queueSize),
		done:    make(chan struct{}),
	}
	if err := os.MkdirAll(r.wsDir, 0o755); err != nil {
		return nil, err
	}
	if err := r.openLog(); err != nil {
		return nil, err
	}
	go r.run()
	return r, nil
}

// Dir is the divergence data directory (<home>/data/divergence).
func (r *Recorder) Dir() string { return r.dir }

// WriteSetDir is the write-set ring directory.
func (r *Recorder) WriteSetDir() string { return r.wsDir }

// Dropped is how many blocks were not recorded because the queue was full.
func (r *Recorder) Dropped() uint64 { return r.dropped.Load() }

// ListenFinalizeBlock keeps the block's tx results for ListenCommit.
func (r *Recorder) ListenFinalizeBlock(_ context.Context, req abci.RequestFinalizeBlock, res abci.ResponseFinalizeBlock) (err error) {
	defer r.recoverPanic("ListenFinalizeBlock")
	r.mu.Lock()
	r.pending = &finalized{height: req.Height, appHash: res.AppHash, txResults: res.TxResults}
	r.mu.Unlock()
	return nil
}

// ListenCommit queues the committed change set for the background writer.
// BaseApp.Commit calls it after the root store committed (so the commit info
// for the height is already flushed) and before Commit returns.
func (r *Recorder) ListenCommit(ctx context.Context, _ abci.ResponseCommit, changeSet []*storetypes.StoreKVPair) (err error) {
	defer r.recoverPanic("ListenCommit")
	r.mu.Lock()
	p := r.pending
	r.pending = nil
	r.mu.Unlock()

	j := &job{changeSet: changeSet, overlap: r.tracker.TakeBlock()}
	if sdkCtx, ok := ctx.(sdk.Context); ok {
		j.height = sdkCtx.BlockHeight()
	}
	if p != nil && (j.height == 0 || p.height == j.height) {
		j.height = p.height
		j.appHash = p.appHash
		j.txResults = p.txResults
	}
	if j.height <= 0 || r.closed.Load() {
		return nil
	}
	r.queued.Add(1)
	select {
	case r.jobs <- j:
	default:
		r.queued.Add(-1)
		r.dropped.Add(1)
		r.logErr("divergence recorder queue full, block not recorded", fmt.Errorf("height %d", j.height))
	}
	return nil
}

// Flush waits until every queued block is on disk, or ctx is done.
func (r *Recorder) Flush(ctx context.Context) error {
	t := time.NewTicker(5 * time.Millisecond)
	defer t.Stop()
	for r.queued.Load() > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	return nil
}

// Close stops the writer after draining the queue.
func (r *Recorder) Close() {
	if r.closed.Swap(true) {
		return
	}
	close(r.jobs)
	<-r.done
	r.logMu.Lock()
	if r.logFile != nil {
		_ = r.logFile.Close()
		r.logFile = nil
	}
	r.logMu.Unlock()
}

func (r *Recorder) run() {
	defer close(r.done)
	for j := range r.jobs {
		r.process(j)
		r.queued.Add(-1)
	}
}

func (r *Recorder) process(j *job) {
	defer r.recoverPanic("process")
	ws := r.buildWriteSet(j)

	if err := writeGzipJSON(filepath.Join(r.wsDir, writeSetFileName(ws.Height)), ws); err != nil {
		r.logErr("divergence recorder failed to write write set", err)
	}
	r.prune(ws.Height)
	r.appendDigest(ws)
}

func (r *Recorder) buildWriteSet(j *job) *WriteSet {
	pairs := netPairs(j.changeSet)
	ws := &WriteSet{
		Height:    j.height,
		Stores:    storeStats(pairs),
		TxResults: toTxResults(j.txResults),
		Overlap:   j.overlap,
		Pairs:     toKVPairs(pairs),
	}
	if j.txResults != nil {
		ws.ResultsHash = strings.ToUpper(hex.EncodeToString(cmttypes.NewResults(j.txResults).Hash()))
	}
	if r.commits != nil {
		ci, err := r.commits.GetCommitInfo(j.height)
		if err != nil {
			r.logErr("divergence recorder failed to read commit info", err)
		} else {
			ws.CommitInfo = commitInfoHashes(ci)
			ws.AppHash = strings.ToUpper(hex.EncodeToString(ci.Hash()))
		}
	}
	if ws.AppHash == "" && len(j.appHash) > 0 {
		ws.AppHash = strings.ToUpper(hex.EncodeToString(j.appHash))
	}
	return ws
}

// prune deletes ring files outside (height-retain, height]. Files above height
// are stale leftovers of a rollback.
func (r *Recorder) prune(height int64) {
	hs, err := listWriteSetHeights(r.wsDir)
	if err != nil {
		r.logErr("divergence recorder failed to list write sets", err)
		return
	}
	for _, h := range hs {
		if h <= height-r.retain || h > height {
			if err := os.Remove(filepath.Join(r.wsDir, writeSetFileName(h))); err != nil && !os.IsNotExist(err) {
				r.logErr("divergence recorder failed to prune write set", err)
			}
		}
	}
}

// digestLine is one line of digests.log. Store hashes and digests are
// abbreviated to 16 hex chars; the app hash is in full.
type digestLine struct {
	Height      int64                `json:"height"`
	Time        int64                `json:"time"`
	AppHash     string               `json:"app_hash"`
	Commit      map[string]string    `json:"commit"`
	Writes      map[string]StoreStat `json:"writes"`
	Txs         int                  `json:"txs"`
	ResultsHash string               `json:"results_hash"`
	Overlap     Overlap              `json:"overlap"`
	Dropped     uint64               `json:"dropped,omitempty"`
}

func abbreviate(s string) string {
	if len(s) > abbrev {
		return s[:abbrev]
	}
	return s
}

func (r *Recorder) appendDigest(ws *WriteSet) {
	line := digestLine{
		Height:      ws.Height,
		Time:        time.Now().Unix(),
		AppHash:     ws.AppHash,
		Commit:      make(map[string]string, len(ws.CommitInfo)),
		Writes:      make(map[string]StoreStat, len(ws.Stores)),
		Txs:         len(ws.TxResults),
		ResultsHash: abbreviate(ws.ResultsHash),
		Overlap:     ws.Overlap,
		Dropped:     r.dropped.Load(),
	}
	for _, sh := range ws.CommitInfo {
		line.Commit[sh.Name] = abbreviate(sh.Hash)
	}
	for name, st := range ws.Stores {
		st.Digest = abbreviate(st.Digest)
		line.Writes[name] = st
	}
	bz, err := json.Marshal(line)
	if err != nil {
		r.logErr("divergence recorder failed to encode digest", err)
		return
	}
	bz = append(bz, '\n')

	r.logMu.Lock()
	defer r.logMu.Unlock()
	if r.logFile == nil {
		return
	}
	if r.logSize+int64(len(bz)) > maxDigestLogBytes {
		r.rotateLocked()
		if r.logFile == nil {
			return
		}
	}
	n, err := r.logFile.Write(bz)
	r.logSize += int64(n)
	if err != nil {
		r.logErr("divergence recorder failed to append digest", err)
	}
}

func (r *Recorder) openLog() error {
	f, err := os.OpenFile(filepath.Join(r.dir, digestsLogName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.logFile, r.logSize = f, st.Size()
	return nil
}

func (r *Recorder) rotateLocked() {
	_ = r.logFile.Close()
	r.logFile = nil
	path := filepath.Join(r.dir, digestsLogName)
	if err := os.Rename(path, path+".1"); err != nil {
		r.logErr("divergence recorder failed to rotate digests.log", err)
	}
	if err := r.openLog(); err != nil {
		r.logErr("divergence recorder failed to reopen digests.log", err)
	}
}

// logErr logs at most one error per minute so a full disk cannot flood logs.
func (r *Recorder) logErr(msg string, err error) {
	r.lastMu.Lock()
	now := time.Now()
	if now.Sub(r.lastErrTime) < time.Minute {
		r.lastMu.Unlock()
		return
	}
	r.lastErrTime = now
	r.lastMu.Unlock()
	r.logger.Error(msg, "err", err)
}

func (r *Recorder) recoverPanic(where string) {
	if p := recover(); p != nil {
		r.logger.Error("divergence recorder panic recovered", "where", where, "panic", fmt.Sprint(p))
	}
}
