package divergence

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cosmossdk.io/log/v2"
)

const (
	SourceConsensus = "consensus"
	SourceBlocksync = "blocksync"

	// MsgDivergenceBundle is logged (ERROR) when a bundle is written for a
	// parsed AppHash/LastResultsHash mismatch; MsgConsensusFailureBundle for
	// any other consensus failure.
	MsgDivergenceBundle       = "APP HASH DIVERGENCE: diagnostics bundle written"
	MsgConsensusFailureBundle = "consensus failure: diagnostics bundle written"

	// DefaultDumpTimeout bounds a crash bundle, reference diff included.
	DefaultDumpTimeout = 30 * time.Second
	// flushTimeout bounds waiting for the recorder to write queued blocks.
	flushTimeout = 5 * time.Second
	// retainedCommitInfoFallback is how many heights of commit info, ending at
	// the divergent height, the summary always carries (on top of the retained
	// write-set heights), so a dropped or empty ring still leaves the diff its
	// store hashes.
	retainedCommitInfoFallback = 2
	// maxIncompleteBundles is how many incomplete bundles one height may
	// collect. Once it has that many, the height counts as done: a failure that
	// persists (an unreadable ring file, say) cannot add a bundle per blocksync
	// re-fetch or per crash-loop restart until the disk is full.
	maxIncompleteBundles = 3
)

var processStart = time.Now()

// Trigger describes what asked for a bundle.
type Trigger struct {
	Source string
	Err    string
}

// HeightCommit is the commit info at one height.
type HeightCommit struct {
	Height  int64       `json:"height"`
	AppHash string      `json:"app_hash"`
	Stores  []StoreHash `json:"stores"`
}

// HeightOverlap is the overlap counts recorded for one height.
type HeightOverlap struct {
	Height int64 `json:"height"`
	Overlap
}

// ProcessInfo describes the node process.
type ProcessInfo struct {
	PID           int     `json:"pid"`
	Start         string  `json:"start"`
	UptimeSeconds float64 `json:"uptime_seconds"`
}

// Summary is a bundle's summary.json.
type Summary struct {
	CreatedAt string    `json:"created_at"`
	Source    string    `json:"source"`
	Error     string    `json:"error"`
	Mismatch  *Mismatch `json:"mismatch,omitempty"`
	// FailingHeight is the block whose header failed validation.
	FailingHeight int64 `json:"failing_height"`
	// DivergentHeight is the committed height whose state differs (see
	// DivergentHeight); it equals LastCommittedHeight.
	DivergentHeight     int64 `json:"divergent_height"`
	LastCommittedHeight int64 `json:"last_committed_height"`
	// LocalHashMatchesRecord checks the parsed local hash against this node's
	// own record of DivergentHeight (commit info for AppHash, recorded tx
	// results for LastResultsHash). False means the height mapping is off.
	LocalHashMatchesRecord *bool `json:"local_hash_matches_record,omitempty"`

	CommitInfo      []HeightCommit    `json:"commit_info"`
	WriteSetHeights []int64           `json:"write_set_heights"`
	Build           BuildInfo         `json:"build"`
	Config          map[string]string `json:"config"`
	Process         ProcessInfo       `json:"process"`
	OverlapTotals   Overlap           `json:"overlap_totals"`
	OverlapByHeight []HeightOverlap   `json:"overlap_by_height"`
	RecorderDropped uint64            `json:"recorder_dropped"`
	ReferenceAPI    string            `json:"reference_api,omitempty"`
	DiffCommand     string            `json:"diff_command"`
	// Complete is false when the bundle may be missing a retained write set:
	// the recorder flush timed out, the ring could not be listed, or a copy
	// failed. An incomplete bundle does not stop a later trigger for the same
	// height from writing a new one (see claim), and is only removed once a
	// complete one replaces it.
	Complete bool     `json:"complete"`
	Errors   []string `json:"errors,omitempty"`
}

func (s *Summary) commitAt(h int64) *CommitInfoResponse {
	for _, c := range s.CommitInfo {
		if c.Height == h {
			return &CommitInfoResponse{Height: c.Height, AppHash: c.AppHash, Stores: c.Stores}
		}
	}
	return nil
}

// Options configures New.
type Options struct {
	Config Config
	// HomeDir is the node home. The recorder writes under
	// <HomeDir>/data/divergence and crash bundles go to <HomeDir>/divergence.
	HomeDir string
	Logger  log.Logger
	// Commits reads committed store hashes (the app's *rootmulti.Store).
	Commits CommitInfoSource
	// LastHeight returns the last committed height.
	LastHeight func() int64
	// Fingerprint is the node's consensus-adjacent config (see Fingerprint).
	Fingerprint map[string]string
	// HTTPClient fetches from the reference; nil uses a default client.
	HTTPClient *http.Client
}

// Diagnostics owns the recorder, the overlap tracker and the crash bundle.
type Diagnostics struct {
	cfg         Config
	dir         string
	bundleDir   string
	logger      log.Logger
	recorder    *Recorder
	tracker     *Tracker
	commits     CommitInfoSource
	lastHeight  func() int64
	fingerprint map[string]string
	client      *http.Client

	mu     sync.Mutex
	dumped map[int64]string
}

// DataDir returns <home>/data/divergence, where the recorder keeps its digests
// and write-set ring.
func DataDir(home string) string { return filepath.Join(home, "data", "divergence") }

// BundleDir returns <home>/divergence, where crash bundles go. It is outside
// <home>/data so that resetting the node (`unsafe-reset-all` removes the data
// directory) keeps the evidence.
func BundleDir(home string) string { return filepath.Join(home, "divergence") }

// New starts the recorder under <home>/data/divergence.
func New(o Options) (*Diagnostics, error) {
	if o.Logger == nil {
		o.Logger = log.NewNopLogger()
	}
	if o.LastHeight == nil {
		o.LastHeight = func() int64 { return 0 }
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: DefaultDumpTimeout}
	}
	d := &Diagnostics{
		cfg:         o.Config,
		dir:         DataDir(o.HomeDir),
		bundleDir:   BundleDir(o.HomeDir),
		logger:      o.Logger,
		tracker:     NewTracker(),
		commits:     o.Commits,
		lastHeight:  o.LastHeight,
		fingerprint: o.Fingerprint,
		client:      o.HTTPClient,
		dumped:      map[int64]string{},
	}
	rec, err := NewRecorder(d.dir, o.Config.RetainBlocks, o.Logger, o.Commits, d.tracker)
	if err != nil {
		return nil, err
	}
	d.recorder = rec
	return d, nil
}

// Recorder returns the streaming listener (nil on a nil receiver).
func (d *Diagnostics) Recorder() *Recorder {
	if d == nil {
		return nil
	}
	return d.recorder
}

// Tracker returns the overlap tracker (nil on a nil receiver; a nil Tracker is
// a no-op).
func (d *Diagnostics) Tracker() *Tracker {
	if d == nil {
		return nil
	}
	return d.tracker
}

// Close stops the recorder.
func (d *Diagnostics) Close() {
	if d == nil {
		return
	}
	if Active() == d {
		SetActive(nil)
	}
	d.recorder.Close()
}

// ---------------------------------------------------------------------------
// Process-wide registry. The start command's logger, which sees CometBFT's
// failure messages, is built before the app, so the app registers itself here.
// ---------------------------------------------------------------------------

var active atomic.Pointer[Diagnostics]

// SetActive registers d as the process's diagnostics (nil clears it).
func SetActive(d *Diagnostics) { active.Store(d) }

// Active returns the registered diagnostics, or nil.
func Active() *Diagnostics { return active.Load() }

// HandleConsensusFailure writes a bundle for a CometBFT consensus failure and
// returns when it is done or after timeout. The caller exits the process next.
func HandleConsensusFailure(errText string, timeout time.Duration) {
	d := Active()
	if d == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				d.logger.Error("divergence bundle panic recovered", "panic", fmt.Sprint(p))
			}
		}()
		_, _ = d.Dump(ctx, Trigger{Source: SourceConsensus, Err: errText})
	}()
	select {
	case <-done:
	case <-ctx.Done():
		d.logger.Error("divergence bundle timed out", "timeout", timeout.String())
	}
}

// HandleBlocksyncError writes a bundle, once per height and in the background,
// when a blocksync validation error is a header hash mismatch. Blocksync keeps
// its own behaviour (drop the peer, retry).
func HandleBlocksyncError(errText string) {
	if !IsHashMismatch(errText) {
		return
	}
	d := Active()
	if d == nil {
		return
	}
	if _, done := d.alreadyDumped(DivergentHeight(d.lastHeight() + 1)); done {
		return
	}
	go func() {
		defer func() {
			if p := recover(); p != nil {
				d.logger.Error("divergence bundle panic recovered", "panic", fmt.Sprint(p))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), DefaultDumpTimeout)
		defer cancel()
		_, _ = d.Dump(ctx, Trigger{Source: SourceBlocksync, Err: errText})
	}()
}

func (d *Diagnostics) alreadyDumped(h int64) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.dumped[h]
	return p, ok
}

// claim reserves height h for one bundle. It returns the existing bundle path
// and false when this process already wrote one (or is writing it), or a
// complete one is on disk from an earlier run (a crash loop restarts and fails
// on the same block every time). A bundle is complete once its summary.json
// exists with Complete set; that file is written last and atomically. When it
// takes the reservation, claim deletes earlier attempts for h that have no
// summary.json (the dump was cut off), so retries do not pile up on disk. A
// height that already has maxIncompleteBundles incomplete bundles is not
// retried either; the newest of them is returned.
func (d *Diagnostics) claim(h int64) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p, ok := d.dumped[h]; ok {
		return p, false
	}
	var incomplete []string
	for _, dir := range d.bundlesFor(h) {
		switch s, err := ReadSummary(dir); {
		case err == nil && s.Complete:
			d.dumped[h] = dir
			return dir, false
		case errors.Is(err, os.ErrNotExist):
			_ = os.RemoveAll(dir)
		default:
			incomplete = append(incomplete, dir)
		}
	}
	if len(incomplete) >= maxIncompleteBundles {
		newest := newestBundle(incomplete)
		d.dumped[h] = newest
		d.logger.Error("divergence bundle retry limit reached: keeping the incomplete bundles",
			"divergent_height", h, "incomplete", len(incomplete), "bundle", newest)
		return newest, false
	}
	d.dumped[h] = ""
	return "", true
}

// newestBundle returns the most recently modified of dirs (at least one).
func newestBundle(dirs []string) string {
	newest, newestMod := dirs[0], time.Time{}
	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err == nil && fi.ModTime().After(newestMod) {
			newest, newestMod = dir, fi.ModTime()
		}
	}
	return newest
}

// bundlesFor lists the bundle directories for height h.
func (d *Diagnostics) bundlesFor(h int64) []string {
	matches, _ := filepath.Glob(filepath.Join(d.bundleDir, fmt.Sprintf("bundle-%d-*", h)))
	return matches
}

// pruneSuperseded deletes the other bundles for height h once keep, a complete
// bundle, is written, so a height ends with one bundle however many attempts
// it took. It is never called for an incomplete bundle: an earlier attempt may
// hold a write set the new one could not copy.
func (d *Diagnostics) pruneSuperseded(h int64, keep string) {
	for _, dir := range d.bundlesFor(h) {
		if dir != keep {
			_ = os.RemoveAll(dir)
		}
	}
}

// release drops a reservation whose dump failed, so a later trigger retries.
func (d *Diagnostics) release(h int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.dumped, h)
}

// DiffCommand is the CLI line that diffs a bundle against a reference.
func DiffCommand(bundleDir, reference string) string {
	if reference == "" {
		reference = "<reference-api-url>"
	}
	return fmt.Sprintf("thornode debug divergence-diff --bundle %s --reference %s", bundleDir, reference)
}

// newBundleDir creates a fresh bundle-<h>-<unix>[-<n>] directory. Every
// attempt gets its own directory, so a retry within the same second never
// writes into, or cleans up, an earlier attempt's bundle.
func (d *Diagnostics) newBundleDir(h int64, now time.Time) (string, error) {
	if err := os.MkdirAll(d.bundleDir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Join(d.bundleDir, fmt.Sprintf("bundle-%d-%d", h, now.Unix()))
	for i := 0; ; i++ {
		dir := base
		if i > 0 {
			dir = fmt.Sprintf("%s-%d", base, i)
		}
		err := os.Mkdir(dir, 0o755)
		if err == nil {
			return dir, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
}

// Dump writes <home>/divergence/bundle-<divergent height>-<unix>/ with
// summary.json, the retained write sets and, when a reference API is
// configured, diff.json.
func (d *Diagnostics) Dump(ctx context.Context, trig Trigger) (string, error) {
	lastCommitted := d.lastHeight()
	failing := lastCommitted + 1
	divergent := DivergentHeight(failing)

	if existing, ok := d.claim(divergent); !ok {
		d.logger.Error("divergence bundle already written for this height", "divergent_height", divergent, "bundle", existing)
		return existing, nil
	}

	flushCtx, cancel := context.WithTimeout(ctx, flushTimeout)
	flushErr := d.recorder.Flush(flushCtx)
	cancel()
	if flushErr != nil {
		d.logger.Error("divergence recorder flush incomplete", "err", flushErr)
	}

	now := time.Now()
	bundle, err := d.newBundleDir(divergent, now)
	if err == nil {
		if err = os.Mkdir(filepath.Join(bundle, writeSetsDir), 0o755); err != nil {
			_ = os.RemoveAll(bundle)
		}
	}
	if err != nil {
		d.release(divergent)
		d.logger.Error("divergence bundle failed", "err", err)
		return "", err
	}
	s, listErr := d.buildSummary(trig, now, failing, divergent, lastCommitted, bundle)

	// A write set still queued, or a ring that could not be listed, may be
	// missing from the bundle, so it counts as incomplete and is retried.
	s.Complete = flushErr == nil && listErr == nil
	if flushErr != nil {
		s.Errors = append(s.Errors, "recorder flush: "+flushErr.Error())
	}
	for _, h := range s.WriteSetHeights {
		name := writeSetFileName(h)
		if err := copyFile(filepath.Join(d.recorder.WriteSetDir(), name), filepath.Join(bundle, writeSetsDir, name)); err != nil {
			s.Errors = append(s.Errors, "copy write set: "+err.Error())
			s.Complete = false
		}
	}
	// summary.json goes last: with Complete set it marks the bundle done
	// (claim). A bundle whose summary cannot be written is unreadable, so this
	// attempt's own directory is removed rather than left to fill the disk.
	if err := writeJSON(filepath.Join(bundle, SummaryFileName), s); err != nil {
		_ = os.RemoveAll(bundle)
		d.release(divergent)
		d.logger.Error("divergence bundle failed to write summary", "err", err)
		return "", err
	}
	d.mu.Lock()
	if s.Complete {
		d.pruneSuperseded(divergent, bundle)
		d.dumped[divergent] = bundle
	} else {
		// Keep this bundle as evidence, but let a later trigger retry.
		delete(d.dumped, divergent)
	}
	d.mu.Unlock()

	keyvals := []any{
		"source", trig.Source,
		"failing_height", failing,
		"divergent_height", divergent,
		"bundle", bundle,
		"complete", s.Complete,
	}
	if s.Mismatch != nil {
		keyvals = append(keyvals, "type", s.Mismatch.Type, "local", s.Mismatch.Local, "network", s.Mismatch.Network)
	}
	if d.cfg.ReferenceAPI != "" {
		diff, diffFile, err := RunReferenceDiff(ctx, d.client, bundle, d.cfg.ReferenceAPI, divergent)
		switch {
		case err != nil:
			keyvals = append(keyvals, "diff_err", err.Error())
		default:
			keyvals = append(keyvals,
				"divergent_stores", strings.Join(diff.StoreNames(), ","),
				"key_diffs", diff.TotalKeyDiffs,
				"top_keys", strings.Join(diff.TopKeys(10), " "),
				"diff", diffFile)
		}
	}
	keyvals = append(keyvals, "next", s.DiffCommand)
	// Every consensus failure gets a bundle, but only a parsed hash mismatch is
	// announced as a divergence; for other panics divergent_height is just the
	// last committed height.
	msg := MsgConsensusFailureBundle
	if s.Mismatch != nil {
		msg = MsgDivergenceBundle
	}
	d.logger.Error(msg, keyvals...)
	return bundle, nil
}

// buildSummary also returns the error from listing the write-set ring, which
// leaves the bundle incomplete; other errors only go to Summary.Errors.
func (d *Diagnostics) buildSummary(trig Trigger, now time.Time, failing, divergent, lastCommitted int64, bundle string) (*Summary, error) {
	s := &Summary{
		CreatedAt:           now.UTC().Format(time.RFC3339),
		Source:              trig.Source,
		Error:               trig.Err,
		FailingHeight:       failing,
		DivergentHeight:     divergent,
		LastCommittedHeight: lastCommitted,
		CommitInfo:          []HeightCommit{},
		WriteSetHeights:     []int64{},
		Build:               CurrentBuild(),
		Config:              d.fingerprint,
		Process: ProcessInfo{
			PID:           os.Getpid(),
			Start:         processStart.UTC().Format(time.RFC3339),
			UptimeSeconds: now.Sub(processStart).Seconds(),
		},
		OverlapTotals:   d.tracker.Totals(),
		OverlapByHeight: []HeightOverlap{},
		RecorderDropped: d.recorder.Dropped(),
		ReferenceAPI:    d.cfg.ReferenceAPI,
		DiffCommand:     DiffCommand(bundle, d.cfg.ReferenceAPI),
	}
	if m, ok := ParseMismatch(trig.Err); ok {
		s.Mismatch = m
	}

	heights, listErr := listWriteSetHeights(d.recorder.WriteSetDir())
	if listErr != nil {
		s.Errors = append(s.Errors, "list write sets: "+listErr.Error())
	}
	s.WriteSetHeights = append(s.WriteSetHeights, heights...)

	// The retained write-set heights, plus the divergent height and the one
	// before it: those commits are in the DB even when the recorder dropped or
	// failed to write their write sets, and the reference diff needs them.
	commitHeights := slices.Clone(heights)
	for h := divergent - retainedCommitInfoFallback + 1; h <= divergent; h++ {
		if h > 0 {
			commitHeights = append(commitHeights, h)
		}
	}
	slices.Sort(commitHeights)
	commitHeights = slices.Compact(commitHeights)
	for _, h := range commitHeights {
		if d.commits == nil {
			break
		}
		ci, err := d.commits.GetCommitInfo(h)
		if err != nil {
			s.Errors = append(s.Errors, fmt.Sprintf("commit info %d: %s", h, err))
			continue
		}
		s.CommitInfo = append(s.CommitInfo, HeightCommit{
			Height:  h,
			AppHash: strings.ToUpper(hex.EncodeToString(ci.Hash())),
			Stores:  commitInfoHashes(ci),
		})
	}

	var divergentWS *WriteSet
	for _, h := range heights {
		ws, err := ReadWriteSetFile(filepath.Join(d.recorder.WriteSetDir(), writeSetFileName(h)))
		if err != nil {
			s.Errors = append(s.Errors, fmt.Sprintf("read write set %d: %s", h, err))
			continue
		}
		s.OverlapByHeight = append(s.OverlapByHeight, HeightOverlap{Height: h, Overlap: ws.Overlap})
		if h == divergent {
			divergentWS = ws
		}
	}

	if s.Mismatch != nil {
		var recorded string
		switch s.Mismatch.Type {
		case HashTypeAppHash:
			if c := s.commitAt(divergent); c != nil {
				recorded = c.AppHash
			}
		case HashTypeLastResultsHash:
			if divergentWS != nil {
				recorded = divergentWS.ResultsHash
			}
		}
		if recorded != "" {
			ok := strings.EqualFold(recorded, s.Mismatch.Local)
			s.LocalHashMatchesRecord = &ok
		}
	}
	return s, listErr
}

// failHook lets tests fail a bundle file write (writeJSON, copyFile) by
// destination path; nil in production.
var failHook func(path string) error

// writeJSON writes v to path atomically (temp file, then rename), so a reader
// never sees a truncated file.
func writeJSON(path string, v any) error {
	if failHook != nil {
		if err := failHook(path); err != nil {
			return err
		}
	}
	bz, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bz, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	if failHook != nil {
		if err := failHook(dst); err != nil {
			return err
		}
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// parseHeight parses a positive height path parameter.
func parseHeight(s string) (int64, error) {
	h, err := strconv.ParseInt(s, 10, 64)
	if err != nil || h <= 0 {
		return 0, errors.New("height must be a positive integer")
	}
	return h, nil
}
