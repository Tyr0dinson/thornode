package evm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"golang.org/x/sync/singleflight"
)

type TransactionLocationState uint8

const (
	TransactionUnknown TransactionLocationState = iota
	TransactionPending
	TransactionAbsent
	TransactionCanonical
	TransactionFailed
)

type TransactionLocation struct {
	State       TransactionLocationState
	Transaction *etypes.Transaction
	Height      int64
	BlockHash   string
}

type TransactionLocationRPC interface {
	GetTransaction(string) (*etypes.Transaction, bool, error)
	GetReceipt(string) (*etypes.Receipt, error)
	GetHeader(int64) (*etypes.Header, error)
	BlockHashRPC
}

// LocateTransaction distinguishes canonical inclusion from pending and unknown
// state. Neither absence nor failed execution proves a different nonce spender.
func LocateTransaction(rpc TransactionLocationRPC, hash string) (TransactionLocation, error) {
	unknown := TransactionLocation{State: TransactionUnknown}
	tx, pending, err := rpc.GetTransaction(hash)
	if errors.Is(err, ethereum.NotFound) {
		return TransactionLocation{State: TransactionAbsent}, nil
	}
	if err != nil {
		return unknown, fmt.Errorf("transaction lookup: %w", err)
	}
	if tx == nil || tx.Hash() != ecommon.HexToHash(hash) {
		return unknown, errors.New("transaction lookup is inconsistent")
	}
	if pending {
		return TransactionLocation{State: TransactionPending, Transaction: tx}, nil
	}
	receipt, err := rpc.GetReceipt(hash)
	if err != nil {
		return unknown, fmt.Errorf("transaction receipt: %w", err)
	}
	if receipt == nil || receipt.BlockNumber == nil || !receipt.BlockNumber.IsInt64() || receipt.BlockNumber.Sign() < 0 || receipt.BlockHash == (ecommon.Hash{}) || receipt.TxHash != tx.Hash() {
		return unknown, errors.New("transaction receipt has no reliable canonical location")
	}
	if len(receipt.PostState) > 0 {
		return unknown, errors.New("historical receipt has no execution-status proof")
	}
	header, err := rpc.GetHeader(receipt.BlockNumber.Int64())
	if err != nil {
		return unknown, fmt.Errorf("receipt canonical header: %w", err)
	}
	if header == nil || header.Number == nil || header.Number.Cmp(receipt.BlockNumber) != 0 {
		return unknown, errors.New("receipt does not match the canonical header")
	}
	same, err := SameBlock(rpc, receipt.BlockHash, header.Hash().Hex(), header.Number.Int64())
	if err != nil {
		return unknown, fmt.Errorf("receipt canonical header: %w", err)
	}
	if !same {
		return unknown, errors.New("receipt does not match the canonical header")
	}
	state := TransactionCanonical
	switch receipt.Status {
	case etypes.ReceiptStatusSuccessful:
	case etypes.ReceiptStatusFailed:
		state = TransactionFailed
	default:
		return unknown, errors.New("receipt execution status is invalid")
	}
	return TransactionLocation{State: state, Transaction: tx, Height: header.Number.Int64(), BlockHash: header.Hash().Hex()}, nil
}

type CanonicalHeaderRPC interface {
	GetHeader(int64) (*etypes.Header, error)
	GetTipHeader() (*etypes.Header, error)
}

// ContextualCanonicalHeaderRPC lets the production adapter honor the caller's
// cancellation and deadline in addition to the configured RPC timeout.
type ContextualCanonicalHeaderRPC interface {
	GetHeaderContext(context.Context, int64) (*etypes.Header, error)
	GetTipHeaderContext(context.Context) (*etypes.Header, error)
}

type CanonicalEvidence struct {
	CheckedAt    time.Time
	TipCheckedAt time.Time
	Height       int64
	BlockHash    string
	TipHash      string
	TipHeight    int64
	Generation   uint64
}

type canonicalCacheEntry struct {
	evidence CanonicalEvidence
}

// CanonicalValidator coalesces concurrent checks of a pending block and retains
// at most 256 approvals. Every call brackets even a cache hit with fresh tip
// reads. RPC deadlines belong to the adapter. Invalidate also rejects work which
// started before an occurrence invalidation, even if its RPC later succeeds.
// Observer integration must retain the returned generation with its exact local
// signed-payload record; a peer's imported signature cannot create that record.
type CanonicalValidator struct {
	rpc         CanonicalHeaderRPC
	freshness   time.Duration
	mu          sync.Mutex
	generation  uint64
	invalidated chan struct{}
	cache       map[int64]canonicalCacheEntry
	flights     singleflight.Group
	now         func() time.Time
	slots       chan struct{}
	// A scheduling hint only: it carries no canonical or signing permission.
	readinessAfter int64
}

// NewCanonicalValidator bounds evidence reuse and concurrent RPC work. The cache
// is only a local optimization; every approval still closes its canonical tip view.
func NewCanonicalValidator(rpc CanonicalHeaderRPC, interval time.Duration) *CanonicalValidator {
	if interval <= 0 || interval > 2*time.Second {
		interval = 2 * time.Second
	}
	return &CanonicalValidator{rpc: rpc, freshness: interval, cache: make(map[int64]canonicalCacheEntry), now: time.Now, slots: make(chan struct{}, 16), invalidated: make(chan struct{}), readinessAfter: -1}
}

// Invalidate rejects earlier generations and wakes waiters on the old view.
// Recovery writers use this fence before changing what a finalizer may complete.
func (v *CanonicalValidator) Invalidate() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.invalidateLocked()
}

func (v *CanonicalValidator) invalidateLocked() {
	if v.invalidated != nil {
		close(v.invalidated)
	}
	v.invalidated = make(chan struct{})
	v.generation++
	clear(v.cache)
}

func (v *CanonicalValidator) Generation() uint64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.generation
}

func (v *CanonicalValidator) Current(generation uint64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.generation == generation
}

func (v *CanonicalValidator) CachedBlocks() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.cache)
}

func (v *CanonicalValidator) Validate(height int64, expectedHash string) (CanonicalEvidence, error) {
	return v.ValidateContext(context.Background(), height, expectedHash)
}

// ValidateContext coalesces checks only for the same height, hash and generation.
// Cancellation stops this caller's wait, and an invalidation defeats a late RPC result.
func (v *CanonicalValidator) ValidateContext(ctx context.Context, height int64, expectedHash string) (CanonicalEvidence, error) {
	if err := ctx.Err(); err != nil {
		return CanonicalEvidence{}, err
	}
	if height < 0 {
		return CanonicalEvidence{}, errors.New("invalid observation height")
	}
	if expectedHash == "" {
		return CanonicalEvidence{}, fmt.Errorf("%w: missing original block anchor", stypes.ErrObservedTxOrphaned)
	}
	v.mu.Lock()
	generation := v.generation
	v.mu.Unlock()
	key := fmt.Sprintf("%d/%s/%d", height, strings.ToLower(expectedHash), generation)
	flight := v.flights.DoChan(key, func() (any, error) {
		select {
		case v.slots <- struct{}{}:
			defer func() { <-v.slots }()
		default:
			return nil, errors.New("canonical validation concurrency limit reached")
		}
		return v.validate(ctx, height, expectedHash, generation)
	})
	var result singleflight.Result
	select {
	case result = <-flight:
	case <-ctx.Done():
		return CanonicalEvidence{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return CanonicalEvidence{}, err
	}
	if result.Err != nil {
		return CanonicalEvidence{}, result.Err
	}
	if err := ctx.Err(); err != nil {
		return CanonicalEvidence{}, err
	}
	evidence, ok := result.Val.(CanonicalEvidence)
	if !ok || !v.Current(evidence.Generation) {
		return CanonicalEvidence{}, errors.New("canonical validation generation changed")
	}
	return evidence, nil
}

// ErrStaleScannerGeneration reports a scanner download that a reorg or recovery
// request overtook while it was in flight. It is not an RPC failure: the
// scanner downloads the height again rather than backing off.
var ErrStaleScannerGeneration = errors.New("scanner download generation changed")

// downloadFailureHeights bounds the heights DownloadFailures remembers: far
// above the heights a prefetch window keeps failing at once, so a retried height
// is not logged again.
const downloadFailureHeights = 256

// DownloadFailures remembers which heights' download failures a scanner has
// logged. The block scanner only reports a height still unavailable after a
// minute; this lets a scanner log the first download failure of each height at
// once, in whatever order prefetch workers or a rewind hit them, without logging
// every retry. The zero value is ready to use.
type DownloadFailures struct {
	mu      sync.Mutex
	heights map[int64]struct{}
	order   []int64 // a ring in insertion order; next is the oldest once full
	next    int
}

// First reports whether height's download failure is not logged yet, and
// records it. Past downloadFailureHeights it forgets the oldest height logged.
func (d *DownloadFailures) First(height int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, logged := d.heights[height]; logged {
		return false
	}
	if d.heights == nil {
		d.heights = make(map[int64]struct{}, downloadFailureHeights)
	}
	if len(d.order) < downloadFailureHeights {
		d.order = append(d.order, height)
	} else {
		delete(d.heights, d.order[d.next])
		d.order[d.next] = height
		d.next = (d.next + 1) % downloadFailureHeights
	}
	d.heights[height] = struct{}{}
	return true
}

// ScannerEvidence binds a download to an independently checked canonical header.
// An unclosed view never populates the authorization cache. A successful full
// closing proof may be reused there without renewing the original header age;
// signing and settlement still require their own full authorization checks.
type ScannerEvidence struct {
	Generation uint64
	height     int64
	blockHash  string
	tipHeight  int64
	tipHash    ecommon.Hash
	checkedAt  time.Time
}

// FetchScannerBlock retains the generation across the full-block download and
// checks its height before extraction. PrepareScannerBlock must independently
// check the numbered header after pure receipt retrieval, outside scanMu.
func (v *CanonicalValidator) FetchScannerBlock(height int64, fetch func(int64) (*etypes.Block, error)) (*etypes.Block, ScannerEvidence, error) {
	evidence := ScannerEvidence{Generation: v.Generation(), height: height}
	if height < 0 {
		return nil, evidence, errors.New("invalid scanner height")
	}
	block, err := fetch(height)
	if err != nil {
		return nil, evidence, err
	}
	if block == nil || block.Number() == nil || !block.Number().IsInt64() || block.Number().Int64() != height {
		return nil, evidence, errors.New("scanner block height is inconsistent")
	}
	if !v.Current(evidence.Generation) {
		return nil, evidence, ErrStaleScannerGeneration
	}
	evidence.blockHash = block.Hash().Hex()
	return block, evidence, nil
}

// PrepareScannerBlock opens the canonical proof after the download. The separate
// numbered-header lookup is necessary even with equal bracketing tips: an old
// response can have been captured on a temporary fork which has already reverted.
// RevalidateScannerBlock closes this view after stateful extraction and on every
// finalization page, including empty and skipped deliveries.
func (v *CanonicalValidator) PrepareScannerBlock(evidence ScannerEvidence) (ScannerEvidence, error) {
	if evidence.blockHash == "" {
		return evidence, errors.New("scanner download has no anchor")
	}
	if !v.Current(evidence.Generation) {
		return evidence, ErrStaleScannerGeneration
	}
	ctx := context.Background()
	tip, err := v.readTip(ctx)
	if err != nil {
		return evidence, fmt.Errorf("scanner opening tip: %w", err)
	}
	if tip == nil || tip.Number == nil || !tip.Number.IsInt64() || tip.Number.Int64() < evidence.height {
		return evidence, errors.New("scanner opening tip is below block")
	}
	header, err := v.readHeader(ctx, evidence.height)
	if err != nil {
		return evidence, fmt.Errorf("scanner occurrence header: %w", err)
	}
	if header == nil || header.Number == nil || !header.Number.IsInt64() || header.Number.Int64() != evidence.height {
		return evidence, errors.New("scanner occurrence header is inconsistent")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.generation != evidence.Generation {
		return evidence, fmt.Errorf("%w during preparation", ErrStaleScannerGeneration)
	}
	if !strings.EqualFold(header.Hash().Hex(), evidence.blockHash) {
		v.invalidateLocked()
		return evidence, fmt.Errorf("%w: original scanner block is not canonical", stypes.ErrObservedTxOrphaned)
	}
	evidence.tipHeight, evidence.tipHash, evidence.checkedAt = tip.Number.Int64(), tip.Hash(), v.now()
	return evidence, nil
}

// ValidateScannerBlock refreshes a view after extraction itself detected a reorg.
// The scanner holds scanMu and has already rejected an invalidated download.
func (v *CanonicalValidator) ValidateScannerBlock(height int64, hash string) (ScannerEvidence, error) {
	fresh, err := v.Validate(height, hash)
	return ScannerEvidence{Generation: fresh.Generation, height: height, blockHash: hash, tipHeight: fresh.TipHeight, tipHash: ecommon.HexToHash(fresh.TipHash), checkedAt: fresh.CheckedAt}, err
}

// RevalidateScannerBlock closes the original canonical view with a fresh tip.
// Growth also requires the original tip's numbered anchor. A changed/expired
// view falls back to the full proof, never to an unchecked height or empty result.
// Reuse relies on the same coherent canonical RPC view as Validate's tip cache.
func (v *CanonicalValidator) RevalidateScannerBlock(evidence ScannerEvidence) (ScannerEvidence, error) {
	if evidence.blockHash == "" || !v.Current(evidence.Generation) {
		return evidence, errors.New("stale scanner block generation or anchor")
	}
	if v.now().Before(evidence.checkedAt.Add(v.freshness)) {
		select {
		case v.slots <- struct{}{}:
		default:
			return evidence, errors.New("canonical validation concurrency limit reached")
		}
		ctx := context.Background()
		err := closeCanonicalTip(evidence.tipHeight, evidence.tipHash,
			func() (*etypes.Header, error) { return v.readTip(ctx) },
			func(height int64) (*etypes.Header, error) { return v.readHeader(ctx, height) })
		<-v.slots
		v.mu.Lock()
		if v.generation != evidence.Generation {
			v.mu.Unlock()
			return evidence, errors.New("stale scanner block generation")
		}
		checkedAt := v.now()
		if err == nil && checkedAt.Before(evidence.checkedAt.Add(v.freshness)) {
			// This is the same independent header + closed tip proof as Validate.
			// Preserve its age, and do not displace a newer proof with an older page.
			if entry, present := v.cache[evidence.height]; !present || !entry.evidence.CheckedAt.After(evidence.checkedAt) {
				v.cacheEvidenceLocked(CanonicalEvidence{Height: evidence.height, BlockHash: evidence.blockHash, TipHeight: evidence.tipHeight, TipHash: evidence.tipHash.Hex(), Generation: evidence.Generation, CheckedAt: evidence.checkedAt, TipCheckedAt: checkedAt})
			}
			v.mu.Unlock()
			return evidence, nil
		}
		v.mu.Unlock()
	}
	fresh, err := v.Validate(evidence.height, evidence.blockHash)
	if err != nil {
		return evidence, err
	}
	if fresh.Generation != evidence.Generation {
		return evidence, errors.New("stale scanner block generation")
	}
	evidence.tipHeight, evidence.tipHash, evidence.checkedAt = fresh.TipHeight, ecommon.HexToHash(fresh.TipHash), fresh.CheckedAt
	return evidence, nil
}

// PrepareObservationReadinessDetailed checks original block associations without
// requiring maturity. One fresh tip pair covers a bounded, rotating height page;
// unchecked or failed heights stay uncertain. The result cannot authorize a final
// vote or submission.
func (v *CanonicalValidator) PrepareObservationReadinessDetailed(ctx context.Context, occurrences []stypes.ObservationOccurrence, chain common.Chain) (stypes.ObservationReadiness, error) {
	result := stypes.ObservationReadiness{}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	anchored := false
	for _, occurrence := range occurrences {
		if !occurrence.Chain.Equals(chain) || occurrence.TxID.IsEmpty() || occurrence.Height < 0 || occurrence.ConfirmationRequired < 0 {
			return stypes.ObservationReadiness{}, errors.New("invalid canonical readiness occurrence")
		}
		anchored = anchored || strings.TrimSpace(occurrence.BlockHash) != ""
	}
	if !anchored {
		result.Orphaned = append(result.Orphaned, occurrences...)
		return result, nil
	}
	if v == nil || v.rpc == nil {
		return result, errors.New("canonical readiness evidence unavailable")
	}
	select {
	case v.slots <- struct{}{}:
		defer func() { <-v.slots }()
	default:
		return result, errors.New("canonical readiness concurrency limit reached")
	}
	generation := v.Generation()
	if generation == math.MaxUint64 {
		return result, errors.New("canonical readiness generation exhausted")
	}
	tipStarted := time.Now()
	tip, err := v.readTip(ctx)
	rpcCost := time.Since(tipStarted)
	if err != nil {
		return result, fmt.Errorf("canonical readiness tip: %w", err)
	}
	if tip == nil || tip.Number == nil || !tip.Number.IsInt64() || tip.Number.Sign() < 0 {
		return result, errors.New("canonical readiness tip is inconsistent")
	}
	tipHeight := tip.Number.Int64()
	tipHash := tip.Hash().Hex()
	blocks := v.prepareReadinessBlocks(ctx, occurrences, tipHeight, tipHash, generation, rpcCost)
	entries := make(map[int64]CanonicalEvidence)
	replaced := false
	for _, occurrence := range occurrences {
		if strings.TrimSpace(occurrence.BlockHash) == "" {
			result.Orphaned = append(result.Orphaned, occurrence)
			continue
		}
		block, checked := blocks[occurrence.Height]
		if !checked || block.err != nil {
			result.Uncertain = append(result.Uncertain, occurrence)
			continue
		}
		if !strings.EqualFold(block.evidence.BlockHash, occurrence.BlockHash) {
			result.Orphaned = append(result.Orphaned, occurrence)
			replaced = true
			continue
		}
		if block.fresh {
			entries[occurrence.Height] = block.evidence
		}
	}
	if err = closeCanonicalTip(tipHeight, ecommon.HexToHash(tipHash), func() (*etypes.Header, error) { return v.readTip(ctx) }, func(height int64) (*etypes.Header, error) { return v.readHeader(ctx, height) }); err != nil {
		return stypes.ObservationReadiness{}, err
	}
	if err = ctx.Err(); err != nil {
		return stypes.ObservationReadiness{}, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return stypes.ObservationReadiness{}, err
	}
	if generation != v.generation {
		return stypes.ObservationReadiness{}, errors.New("canonical readiness generation changed")
	}
	if replaced {
		v.invalidateLocked()
		return result, nil
	}
	for _, evidence := range entries {
		v.cacheEvidenceLocked(evidence)
	}
	return result, nil
}

const canonicalReadinessBlocks = 16

type canonicalReadinessBlock struct {
	evidence CanonicalEvidence
	fresh    bool
	err      error
}

// prepareReadinessBlocks rotates across distinct pending heights. After the first
// successful height, reserve two calls at the slowest RPC cost seen in this pass
// plus scheduling margin: a growing tip needs both a closing tip and anchor read.
// Unchecked heights remain uncertain. Nothing is published until the caller closes
// the canonical snapshot and checks its generation; final signing still revalidates.
func (v *CanonicalValidator) prepareReadinessBlocks(ctx context.Context, occurrences []stypes.ObservationOccurrence, tipHeight int64, tipHash string, generation uint64, rpcCost time.Duration) map[int64]canonicalReadinessBlock {
	unique := make(map[int64]struct{})
	for _, occurrence := range occurrences {
		if occurrence.Height <= tipHeight && strings.TrimSpace(occurrence.BlockHash) != "" {
			unique[occurrence.Height] = struct{}{}
		}
	}
	heights := make([]int64, 0, len(unique))
	for height := range unique {
		heights = append(heights, height)
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	v.mu.Lock()
	after := v.readinessAfter
	v.mu.Unlock()
	start := sort.Search(len(heights), func(i int) bool { return heights[i] > after })
	prepared := false
	blocks := make(map[int64]canonicalReadinessBlock)
	for index := 0; index < min(canonicalReadinessBlocks, len(heights)); index++ {
		if ctx.Err() != nil {
			break
		}
		height := heights[(start+index)%len(heights)]
		v.mu.Lock()
		cached, present := v.cache[height]
		usable := present && cached.evidence.Generation == generation && cached.evidence.TipHash == tipHash && v.now().Sub(cached.evidence.CheckedAt) < v.freshness
		v.mu.Unlock()
		block := canonicalReadinessBlock{}
		windowExpired := false
		if usable {
			block.evidence = cached.evidence
		} else {
			headerCtx := ctx
			cancel := func() {}
			if deadline, bounded := ctx.Deadline(); prepared && bounded {
				// Do not start another estimated RPC unless it and both closing
				// reads fit. Keep at least 10ms for local classification/scheduling,
				// or 25% of a call for slower daemons. This is a per-pass estimate,
				// not permission to accept an incomplete or stale closing proof.
				reserve := 2*rpcCost + max(10*time.Millisecond, rpcCost/4)
				if time.Until(deadline) <= reserve+rpcCost {
					break
				}
				headerCtx, cancel = context.WithDeadline(ctx, deadline.Add(-reserve))
			}
			started := time.Now()
			header, headerErr := v.readHeader(headerCtx, height)
			rpcCost = max(rpcCost, time.Since(started))
			windowExpired = headerCtx.Err() != nil && ctx.Err() == nil
			cancel()
			block.err = headerErr
			if headerErr == nil {
				if header == nil || header.Number == nil || !header.Number.IsInt64() || header.Number.Int64() != height {
					block.err = errors.New("canonical readiness block is inconsistent")
				} else {
					block.evidence = CanonicalEvidence{Height: height, BlockHash: header.Hash().Hex(), TipHash: tipHash, TipHeight: tipHeight, Generation: generation, CheckedAt: v.now()}
					block.fresh = true
				}
			}
		}
		// Give the first successful height the full caller budget. A slower
		// individual header can still fit when the closing reads are cheap.
		prepared = prepared || block.err == nil
		blocks[height] = block
		if block.err != nil && windowExpired {
			// A later header lost only its reserved work window. Make it the
			// first candidate next time, with the full caller budget. A parent
			// deadline still advances past a genuinely blocked first header.
			break
		}
		v.mu.Lock()
		v.readinessAfter = height
		v.mu.Unlock()
	}
	return blocks
}

// validate brackets the occurrence lookup with a canonical tip check. A cache hit
// keeps its original evidence age; refreshing the tip must not renew an old header.
func (v *CanonicalValidator) validate(ctx context.Context, height int64, expectedHash string, generation uint64) (CanonicalEvidence, error) {
	tip, err := v.readTip(ctx)
	if err != nil {
		return CanonicalEvidence{}, fmt.Errorf("canonical tip: %w", err)
	}
	if tip == nil || tip.Number == nil || !tip.Number.IsInt64() || tip.Number.Sign() < 0 || tip.Number.Int64() < height {
		return CanonicalEvidence{}, errors.New("canonical tip is below occurrence")
	}
	tipHeight, tipHash := tip.Number.Int64(), tip.Hash().Hex()
	mismatch := false
	v.mu.Lock()
	entry, cached := v.cache[height]
	cached = cached && entry.evidence.Generation == generation && entry.evidence.TipHash == tipHash && strings.EqualFold(entry.evidence.BlockHash, expectedHash) && v.now().Sub(entry.evidence.CheckedAt) < v.freshness
	v.mu.Unlock()
	evidence := entry.evidence
	if !cached {
		header, headerErr := v.readHeader(ctx, height)
		if headerErr != nil {
			return CanonicalEvidence{}, fmt.Errorf("canonical occurrence header: %w", headerErr)
		}
		if header == nil || header.Number == nil || !header.Number.IsInt64() || header.Number.Int64() != height {
			return CanonicalEvidence{}, errors.New("canonical occurrence header is inconsistent")
		}
		mismatch = !strings.EqualFold(header.Hash().Hex(), expectedHash)
		evidence = CanonicalEvidence{Height: height, BlockHash: header.Hash().Hex(), TipHash: tipHash, TipHeight: tipHeight, Generation: generation, CheckedAt: v.now()}
	}
	if err = closeCanonicalTip(tipHeight, ecommon.HexToHash(tipHash), func() (*etypes.Header, error) { return v.readTip(ctx) }, func(height int64) (*etypes.Header, error) { return v.readHeader(ctx, height) }); err != nil {
		return CanonicalEvidence{}, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CanonicalEvidence{}, err
	}
	if v.generation != generation {
		return CanonicalEvidence{}, errors.New("canonical validation generation changed")
	}
	evidence.TipCheckedAt = v.now()
	if !evidence.TipCheckedAt.Before(evidence.CheckedAt.Add(v.freshness)) {
		return CanonicalEvidence{}, errors.New("canonical block evidence expired during validation")
	}
	if mismatch {
		v.invalidateLocked()
		return CanonicalEvidence{}, fmt.Errorf("%w: original occurrence is not canonical", stypes.ErrObservedTxOrphaned)
	}
	if !cached {
		v.cacheEvidenceLocked(evidence)
	}
	return evidence, nil
}

// cacheEvidenceLocked evicts the oldest approval when full. Eviction only forces
// a new proof; it never discards a recovery obligation.
func (v *CanonicalValidator) cacheEvidenceLocked(evidence CanonicalEvidence) {
	if _, present := v.cache[evidence.Height]; !present && len(v.cache) >= 256 {
		var oldestHeight int64
		var oldest time.Time
		for height, entry := range v.cache {
			if oldest.IsZero() || entry.evidence.CheckedAt.Before(oldest) {
				oldestHeight, oldest = height, entry.evidence.CheckedAt
			}
		}
		delete(v.cache, oldestHeight)
	}
	v.cache[evidence.Height] = canonicalCacheEntry{evidence: evidence}
}

func (v *CanonicalValidator) readTip(ctx context.Context) (*etypes.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if rpc, ok := v.rpc.(ContextualCanonicalHeaderRPC); ok {
		return rpc.GetTipHeaderContext(ctx)
	}
	return v.rpc.GetTipHeader()
}

func (v *CanonicalValidator) readHeader(ctx context.Context, height int64) (*etypes.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if rpc, ok := v.rpc.(ContextualCanonicalHeaderRPC); ok {
		return rpc.GetHeaderContext(ctx, height)
	}
	return v.rpc.GetHeader(height)
}

// TransactionNonce returns the account nonce a conflicting transaction consumed.
// An OP-stack deposit reports Nonce() as zero; its effective nonce is known only
// when decoded from RPC JSON, and an unknown one cannot prove or refute a nonce
// conflict. A deposit is never an original here: it has no nonce constraint.
func TransactionNonce(tx *etypes.Transaction) (uint64, bool) {
	nonce := tx.EffectiveNonce()
	if nonce == nil {
		return 0, false
	}
	return *nonce, true
}

// FindNonceConflict inspects every raw transaction, including transactions with
// no vault output. The caller must verify this block's canonical anchor and the
// original transaction's absence separately. Returning nil is a hold, including
// when the original transaction itself is present and consumed the nonce.
func FindNonceConflict(block *etypes.Block, original evmtypes.TransactionMeta, signer etypes.Signer) (*etypes.Transaction, error) {
	if block == nil || signer == nil || original.Sender == "" || original.Nonce == nil || original.Hash == "" {
		return nil, errors.New("nonce conflict requires retained original sender, nonce and hash")
	}
	var conflict *etypes.Transaction
	for _, tx := range block.Transactions() {
		if tx == nil {
			return nil, errors.New("nil transaction in nonce-conflict block")
		}
		sender, err := signer.Sender(tx)
		if err != nil {
			return nil, fmt.Errorf("nonce conflict sender: %w", err)
		}
		if tx.Hash() == ecommon.HexToHash(original.Hash) {
			return nil, nil
		}
		nonce, known := TransactionNonce(tx)
		if !known || !strings.EqualFold(sender.Hex(), original.Sender) || nonce != *original.Nonce {
			continue
		}
		if conflict != nil && conflict.Hash() != tx.Hash() {
			return nil, errors.New("multiple nonce spenders in purported canonical block")
		}
		conflict = tx
	}
	return conflict, nil
}

// ConflictConfirmed applies the actual policy requirement, with the original
// observation's stored requirement as a floor. Unknown legacy requirements hold.
func ConflictConfirmed(original evmtypes.TransactionMeta, height, tip, effectiveRequired int64) bool {
	if original.ConfirmationRequired == nil || *original.ConfirmationRequired < 0 || effectiveRequired < 0 || height < 0 || tip < height {
		return false
	}
	required := max(effectiveRequired, *original.ConfirmationRequired)
	return tip-height >= required
}

// ObservationFinaliseHeight recovers the common deadline from a mined deck.
// Each item's requirement is that deadline minus its own original mined height.
func ObservationFinaliseHeight(txIn stypes.TxIn) (int64, error) {
	if len(txIn.TxArray) == 0 || txIn.TxArray[0] == nil {
		return 0, errors.New("observation has no mined deadline")
	}
	height := txIn.TxArray[0].BlockHeight
	if height < 0 || txIn.ConfirmationRequired < 0 || txIn.ConfirmationRequired > math.MaxInt64-height {
		return 0, errors.New("invalid observation finalise height")
	}
	return height + txIn.ConfirmationRequired, nil
}

// ValidateObservation combines canonical evidence with the retained requirement and
// committed scan position. Readiness preparation alone cannot authorize this final proof.
func ValidateObservation(ctx context.Context, validator *CanonicalValidator, occurrence stypes.ObservationOccurrence, chain common.Chain, committedHeight, minimum int64, immediateFinality bool) (stypes.ObservationValidation, error) {
	if !occurrence.Chain.Equals(chain) || occurrence.TxID.IsEmpty() || occurrence.Height < 0 || occurrence.ConfirmationRequired < 0 {
		return stypes.ObservationValidation{}, errors.New("observation does not satisfy chain policy")
	}
	generation := validator.Generation()
	evidence, err := validator.ValidateContext(ctx, occurrence.Height, occurrence.BlockHash)
	if err != nil {
		return stypes.ObservationValidation{}, err
	}
	if occurrence.ConfirmationRequired < minimum {
		return stypes.ObservationValidation{}, errors.New("observation requirement is below the chain floor")
	}

	if !immediateFinality && (committedHeight < occurrence.Height || committedHeight-occurrence.Height < occurrence.ConfirmationRequired || evidence.TipHeight-occurrence.Height < occurrence.ConfirmationRequired) {
		return stypes.ObservationValidation{}, errors.New("canonical observation has insufficient committed confirmations")
	}
	if evidence.Generation != generation || !validator.Current(generation) {
		return stypes.ObservationValidation{}, errors.New("observation validation generation changed")
	}
	if !validator.now().Before(evidence.CheckedAt.Add(validator.freshness)) {
		return stypes.ObservationValidation{}, errors.New("canonical block evidence expired before final proof")
	}
	return stypes.ObservationValidation{Occurrence: occurrence, TipHash: evidence.TipHash, TipHeight: evidence.TipHeight, Generation: evidence.Generation, CheckedAt: evidence.CheckedAt, TipCheckedAt: evidence.TipCheckedAt, ExpiresAt: evidence.CheckedAt.Add(validator.freshness)}, nil
}

// closeCanonicalTip accepts forward growth only while the original tip remains
// canonical. Evidence and maturity remain bounded by that original snapshot.
func closeCanonicalTip(height int64, hash ecommon.Hash, readTip func() (*etypes.Header, error), readHeader func(int64) (*etypes.Header, error)) error {
	latest, err := readTip()
	if err != nil {
		return fmt.Errorf("canonical closing tip: %w", err)
	}
	if latest == nil || latest.Number == nil || !latest.Number.IsInt64() || latest.Number.Int64() < height {
		return errors.New("canonical closing tip is inconsistent")
	}
	if latest.Number.Int64() == height {
		if latest.Hash() != hash {
			return errors.New("canonical snapshot replaced")
		}
		return nil
	}
	anchor, err := readHeader(height)
	if err != nil {
		return fmt.Errorf("canonical closing anchor: %w", err)
	}
	if anchor == nil || anchor.Number == nil || !anchor.Number.IsInt64() || anchor.Number.Int64() != height || anchor.Hash() != hash {
		return errors.New("canonical snapshot replaced during growth")
	}
	return nil
}
