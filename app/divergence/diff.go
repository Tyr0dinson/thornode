package divergence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// valuePreviewBytes is how much of a value diff.json shows (as hex).
	valuePreviewBytes = 64
	// maxKeyDiffs caps the key diffs written to diff.json; TotalKeyDiffs has
	// the full count.
	maxKeyDiffs = 1000
	// maxResponseBytes caps a reference response body.
	maxResponseBytes = 256 << 20

	DiffFileName    = "diff.json"
	SummaryFileName = "summary.json"
)

// ErrNotFound is returned when the reference does not have the height.
var ErrNotFound = errors.New("not found on reference")

// CommitInfoResponse is the body of GET /thorchain/debug/commit_info/{height}.
type CommitInfoResponse struct {
	Height  int64       `json:"height"`
	AppHash string      `json:"app_hash"`
	Stores  []StoreHash `json:"stores"`
}

// StoreDiff is a store whose commit hash differs at the height.
type StoreDiff struct {
	Name      string `json:"name"`
	Local     string `json:"local"`
	Reference string `json:"reference"`
}

// ValueInfo previews one side of a key diff.
type ValueInfo struct {
	// Written is false when this side did not write the key at the height.
	Written bool   `json:"written"`
	Deleted bool   `json:"deleted,omitempty"`
	Len     int    `json:"len,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	// Hex is the first 64 bytes of the value.
	Hex       string `json:"hex,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// KeyDiff is one key written differently by this node and the reference.
type KeyDiff struct {
	Store string `json:"store"`
	Key   string `json:"key"`
	KeyInfo
	// Kind is only_local, only_reference, value_differs or delete_differs.
	Kind      string    `json:"kind"`
	Local     ValueInfo `json:"local"`
	Reference ValueInfo `json:"reference"`
}

// TxResultDiff is a tx whose result fields differ.
type TxResultDiff struct {
	Index     int       `json:"index"`
	Local     *TxResult `json:"local,omitempty"`
	Reference *TxResult `json:"reference,omitempty"`
}

// HeightCheck compares the app hash at one height.
type HeightCheck struct {
	Height    int64  `json:"height"`
	Local     string `json:"local"`
	Reference string `json:"reference"`
	Agrees    bool   `json:"agrees"`
}

// Diff is diff.json: how this node's divergent height differs from a reference.
type Diff struct {
	Height    int64  `json:"height"`
	Reference string `json:"reference"`

	AppHash HeightCheck `json:"app_hash"`
	// Previous checks height-1, which should agree if Height is the first
	// divergent height.
	Previous *HeightCheck `json:"previous,omitempty"`
	// Stores are the stores whose commit hash differs at Height.
	Stores []StoreDiff `json:"stores"`

	LocalResultsHash     string         `json:"local_results_hash,omitempty"`
	ReferenceResultsHash string         `json:"reference_results_hash,omitempty"`
	TxResultDiffs        []TxResultDiff `json:"tx_result_diffs,omitempty"`

	// WriteSetsCompared is true when both write sets were available.
	WriteSetsCompared bool      `json:"write_sets_compared"`
	TotalKeyDiffs     int       `json:"total_key_diffs"`
	KeyDiffs          []KeyDiff `json:"key_diffs"`
	Notes             []string  `json:"notes,omitempty"`
}

// DiffInput is everything ComputeDiff compares; any reference part may be nil.
type DiffInput struct {
	Height          int64
	Reference       string
	LocalCommit     *CommitInfoResponse
	LocalPrevCommit *CommitInfoResponse
	LocalWriteSet   *WriteSet
	RefCommit       *CommitInfoResponse
	RefPrevCommit   *CommitInfoResponse
	RefWriteSet     *WriteSet
}

// ComputeDiff compares this node's commit info and write set at a height with
// the reference's.
func ComputeDiff(in DiffInput) *Diff {
	d := &Diff{Height: in.Height, Reference: in.Reference, Stores: []StoreDiff{}, KeyDiffs: []KeyDiff{}}
	d.AppHash = HeightCheck{Height: in.Height}
	if in.LocalCommit != nil {
		d.AppHash.Local = in.LocalCommit.AppHash
	}
	if in.RefCommit != nil {
		d.AppHash.Reference = in.RefCommit.AppHash
	}
	d.AppHash.Agrees = d.AppHash.Local != "" && strings.EqualFold(d.AppHash.Local, d.AppHash.Reference)

	if in.LocalPrevCommit != nil && in.RefPrevCommit != nil {
		d.Previous = &HeightCheck{
			Height:    in.Height - 1,
			Local:     in.LocalPrevCommit.AppHash,
			Reference: in.RefPrevCommit.AppHash,
			Agrees:    strings.EqualFold(in.LocalPrevCommit.AppHash, in.RefPrevCommit.AppHash),
		}
		if !d.Previous.Agrees {
			d.Notes = append(d.Notes, fmt.Sprintf("app hash already differs at height %d: the divergence started earlier, diff that height (--height)", in.Height-1))
		}
	}

	// Stores whose commit hash differs; a store present on one side only counts.
	differs := map[string]bool{}
	if in.LocalCommit != nil && in.RefCommit != nil {
		ref := map[string]string{}
		for _, s := range in.RefCommit.Stores {
			ref[s.Name] = s.Hash
		}
		seen := map[string]bool{}
		for _, s := range in.LocalCommit.Stores {
			seen[s.Name] = true
			if !strings.EqualFold(s.Hash, ref[s.Name]) {
				d.Stores = append(d.Stores, StoreDiff{Name: s.Name, Local: s.Hash, Reference: ref[s.Name]})
				differs[s.Name] = true
			}
		}
		for _, s := range in.RefCommit.Stores {
			if !seen[s.Name] {
				d.Stores = append(d.Stores, StoreDiff{Name: s.Name, Reference: s.Hash})
				differs[s.Name] = true
			}
		}
		sort.Slice(d.Stores, func(i, j int) bool { return d.Stores[i].Name < d.Stores[j].Name })
	} else {
		d.Notes = append(d.Notes, "commit info missing on one side: store hashes not compared")
	}

	if in.LocalWriteSet != nil {
		d.LocalResultsHash = in.LocalWriteSet.ResultsHash
	}
	if in.RefWriteSet != nil {
		d.ReferenceResultsHash = in.RefWriteSet.ResultsHash
	}
	if in.LocalWriteSet == nil || in.RefWriteSet == nil {
		switch {
		case in.LocalWriteSet == nil && in.RefWriteSet == nil:
			d.Notes = append(d.Notes, "no write set on either side: key-level diff skipped")
		case in.LocalWriteSet == nil:
			d.Notes = append(d.Notes, "local write set missing: key-level diff skipped")
		default:
			d.Notes = append(d.Notes, "reference write set missing (not retained there; raise its divergence.retain_blocks): key-level diff skipped")
		}
		return d
	}
	d.WriteSetsCompared = true
	d.TxResultDiffs = diffTxResults(in.LocalWriteSet.TxResults, in.RefWriteSet.TxResults)

	// Restrict to the differing stores when the store hashes were compared:
	// equal store hashes imply equal state, so other stores can only show
	// writes that did not change anything.
	filter := func(string) bool { return true }
	if in.LocalCommit != nil && in.RefCommit != nil {
		filter = func(s string) bool { return differs[s] }
	}
	keys := diffPairs(in.LocalWriteSet.Pairs, in.RefWriteSet.Pairs, filter)
	d.TotalKeyDiffs = len(keys)
	// From the full list: diffPairs sorts by store, so the cap below can cut a
	// later store's diffs entirely, and it must not then read as "identical".
	keyStores := map[string]bool{}
	for _, k := range keys {
		keyStores[k.Store] = true
	}
	if len(keys) > maxKeyDiffs {
		d.Notes = append(d.Notes, fmt.Sprintf("showing the first %d of %d key diffs (sorted by store, then key)", maxKeyDiffs, len(keys)))
		keys = keys[:maxKeyDiffs]
	}
	d.KeyDiffs = keys

	for _, s := range d.Stores {
		if !keyStores[s.Name] {
			d.Notes = append(d.Notes, fmt.Sprintf("store %s hash differs but this height's writes to it are identical: the state difference predates this height, or is below the KV layer (IAVL tree shape/version)", s.Name))
		}
	}
	return d
}

type sideValue struct {
	value   []byte
	deleted bool
}

func indexPairs(pairs []KVPair, filter func(string) bool) map[string]map[string]sideValue {
	out := map[string]map[string]sideValue{}
	for _, p := range pairs {
		if !filter(p.Store) {
			continue
		}
		m := out[p.Store]
		if m == nil {
			m = map[string]sideValue{}
			out[p.Store] = m
		}
		m[strings.ToLower(p.Key)] = sideValue{value: p.Value, deleted: p.Delete}
	}
	return out
}

func diffPairs(local, ref []KVPair, filter func(string) bool) []KeyDiff {
	l := indexPairs(local, filter)
	r := indexPairs(ref, filter)
	stores := map[string]bool{}
	for s := range l {
		stores[s] = true
	}
	for s := range r {
		stores[s] = true
	}
	storeNames := make([]string, 0, len(stores))
	for s := range stores {
		storeNames = append(storeNames, s)
	}
	sort.Strings(storeNames)

	var out []KeyDiff
	for _, store := range storeNames {
		keySet := map[string]bool{}
		for k := range l[store] {
			keySet[k] = true
		}
		for k := range r[store] {
			keySet[k] = true
		}
		keys := make([]string, 0, len(keySet))
		for k := range keySet {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			lv, lok := l[store][k]
			rv, rok := r[store][k]
			var kind string
			switch {
			case lok && !rok:
				kind = "only_local"
			case !lok && rok:
				kind = "only_reference"
			case lv.deleted != rv.deleted:
				kind = "delete_differs"
			case !bytes.Equal(lv.value, rv.value):
				kind = "value_differs"
			default:
				continue
			}
			raw, _ := hex.DecodeString(k)
			out = append(out, KeyDiff{
				Store:     store,
				Key:       k,
				KeyInfo:   DecodeKey(store, raw),
				Kind:      kind,
				Local:     previewValue(lv, lok),
				Reference: previewValue(rv, rok),
			})
		}
	}
	return out
}

func previewValue(v sideValue, written bool) ValueInfo {
	if !written {
		return ValueInfo{}
	}
	if v.deleted {
		return ValueInfo{Written: true, Deleted: true}
	}
	sum := sha256.Sum256(v.value)
	vi := ValueInfo{Written: true, Len: len(v.value), SHA256: hex.EncodeToString(sum[:8])}
	show := v.value
	if len(show) > valuePreviewBytes {
		show = show[:valuePreviewBytes]
		vi.Truncated = true
	}
	vi.Hex = hex.EncodeToString(show)
	return vi
}

func diffTxResults(local, ref []TxResult) []TxResultDiff {
	n := len(local)
	if len(ref) > n {
		n = len(ref)
	}
	var out []TxResultDiff
	for i := 0; i < n; i++ {
		var l, r *TxResult
		if i < len(local) {
			l = &local[i]
		}
		if i < len(ref) {
			r = &ref[i]
		}
		if l != nil && r != nil && *l == *r {
			continue
		}
		out = append(out, TxResultDiff{Index: i, Local: l, Reference: r})
	}
	return out
}

// TopKeys renders the first n key diffs as short strings for a log line.
func (d *Diff) TopKeys(n int) []string {
	var out []string
	for i, k := range d.KeyDiffs {
		if i >= n {
			break
		}
		name := k.Key
		if k.Text != "" {
			name = k.Text
		}
		out = append(out, fmt.Sprintf("%s:%s(%s)", k.Store, name, k.Kind))
	}
	return out
}

// StoreNames returns the differing store names.
func (d *Diff) StoreNames() []string {
	out := make([]string, 0, len(d.Stores))
	for _, s := range d.Stores {
		out = append(out, s.Name)
	}
	return out
}

// ---------------------------------------------------------------------------
// Reference fetching
// ---------------------------------------------------------------------------

// referenceURL joins a THORNode API base URL and an API path; a base that
// already ends in /thorchain is accepted too.
func referenceURL(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	base = strings.TrimSuffix(base, "/thorchain")
	return base + path
}

func fetchJSON(ctx context.Context, c *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %w", url, ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(v)
}

// FetchCommitInfo fetches the reference's commit info at height.
func FetchCommitInfo(ctx context.Context, c *http.Client, base string, height int64) (*CommitInfoResponse, error) {
	ci := &CommitInfoResponse{}
	if err := fetchJSON(ctx, c, referenceURL(base, "/thorchain/debug/commit_info/"+strconv.FormatInt(height, 10)), ci); err != nil {
		return nil, err
	}
	return ci, nil
}

// FetchWriteSet fetches the reference's write set at height.
func FetchWriteSet(ctx context.Context, c *http.Client, base string, height int64) (*WriteSet, error) {
	ws := &WriteSet{}
	if err := fetchJSON(ctx, c, referenceURL(base, "/thorchain/debug/writeset/"+strconv.FormatInt(height, 10)), ws); err != nil {
		return nil, err
	}
	return ws, nil
}

// ReadSummary reads a bundle's summary.json.
func ReadSummary(bundleDir string) (*Summary, error) {
	bz, err := os.ReadFile(filepath.Join(bundleDir, SummaryFileName))
	if err != nil {
		return nil, err
	}
	s := &Summary{}
	if err = json.Unmarshal(bz, s); err != nil {
		return nil, fmt.Errorf("decode %s: %w", SummaryFileName, err)
	}
	return s, nil
}

// diffEvidence ranks how much a diff learned from its reference: 2 with both
// write sets compared (key-level), 1 with the reference app hash, 0 with
// nothing from the reference.
func diffEvidence(d *Diff) int {
	switch {
	case d.WriteSetsCompared:
		return 2
	case d.AppHash.Reference != "":
		return 1
	default:
		return 0
	}
}

// diffPath picks where a diff of height goes. The divergent height's diff is
// diff.json, any other height's is diff-<height>.json. An existing file is
// replaced only by a diff with more evidence, or the same evidence from the
// same reference; otherwise (say the reference has since pruned the height,
// or a different or wrong reference) the new diff gets its own
// diff-<height>-<unix>[-<n>].json, so a rerun never loses the crash-time
// key-level diff.
func diffPath(bundleDir string, height, divergent int64, d *Diff, now time.Time) string {
	path := filepath.Join(bundleDir, DiffFileName)
	if height != divergent {
		path = filepath.Join(bundleDir, fmt.Sprintf("diff-%d.json", height))
	}
	bz, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return path
	}
	if err == nil {
		old := &Diff{}
		// an unreadable or undecodable diff is kept as it is
		if json.Unmarshal(bz, old) == nil && old.Height == height {
			newRank, oldRank := diffEvidence(d), diffEvidence(old)
			if newRank > oldRank || (newRank == oldRank && old.Reference == d.Reference) {
				return path
			}
		}
	}
	base := filepath.Join(bundleDir, fmt.Sprintf("diff-%d-%d", height, now.Unix()))
	for i := 0; ; i++ {
		p := base + ".json"
		if i > 0 {
			p = fmt.Sprintf("%s-%d.json", base, i)
		}
		// any Stat error but "exists" ends the search; writeJSON reports it
		if _, err := os.Stat(p); err != nil {
			return p
		}
	}
}

// RunReferenceDiff diffs a bundle against a reference node and writes the
// diff into the bundle (see diffPath for which file), returning it and the
// path written. Height 0 means the bundle's divergent height. Reference
// errors become notes so a partial diff is still written.
func RunReferenceDiff(ctx context.Context, c *http.Client, bundleDir, reference string, height int64) (*Diff, string, error) {
	s, err := ReadSummary(bundleDir)
	if err != nil {
		return nil, "", err
	}
	if height <= 0 {
		height = s.DivergentHeight
	}
	if height <= 0 {
		return nil, "", fmt.Errorf("bundle has no divergent height; pass a height")
	}
	if reference == "" {
		return nil, "", fmt.Errorf("no reference API given")
	}
	if c == nil {
		c = http.DefaultClient
	}

	in := DiffInput{
		Height:          height,
		Reference:       reference,
		LocalCommit:     s.commitAt(height),
		LocalPrevCommit: s.commitAt(height - 1),
	}
	var notes []string
	ws, wsErr := ReadWriteSetFile(filepath.Join(bundleDir, writeSetsDir, writeSetFileName(height)))
	switch {
	case wsErr == nil:
		in.LocalWriteSet = ws
		if in.LocalCommit == nil && len(ws.CommitInfo) > 0 {
			in.LocalCommit = &CommitInfoResponse{Height: height, AppHash: ws.AppHash, Stores: ws.CommitInfo}
		}
	case !os.IsNotExist(wsErr):
		notes = append(notes, "local write set unreadable: "+wsErr.Error())
	}
	if in.RefCommit, err = FetchCommitInfo(ctx, c, reference, height); err != nil {
		notes = append(notes, "reference commit info: "+err.Error())
	}
	if height > 1 {
		if in.RefPrevCommit, err = FetchCommitInfo(ctx, c, reference, height-1); err != nil {
			notes = append(notes, "reference commit info (previous height): "+err.Error())
		}
	}
	if in.RefWriteSet, err = FetchWriteSet(ctx, c, reference, height); err != nil {
		notes = append(notes, "reference write set: "+err.Error())
	}

	d := ComputeDiff(in)
	d.Notes = append(notes, d.Notes...)
	if s.Mismatch != nil && s.Mismatch.Type == HashTypeAppHash && d.AppHash.Reference != "" && height == s.DivergentHeight {
		if !strings.EqualFold(d.AppHash.Reference, s.Mismatch.Network) {
			d.Notes = append(d.Notes, "reference app hash differs from the network hash in the failing header: the reference may itself be on a fork")
		}
	}
	path := diffPath(bundleDir, height, s.DivergentHeight, d, time.Now())
	if err = writeJSON(path, d); err != nil {
		return nil, "", err
	}
	return d, path, nil
}
