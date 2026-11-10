package evm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

type cleanupBlockKey struct {
	height int64
	hash   string
}

// ReconcileBlockOccurrences durably retains every old occurrence before one
// acknowledged local-only handoff per block. The canonical replacement is
// extracted by an ordered rewind; no old item or requirement is kept/relabelled.
func ReconcileBlockOccurrences(journal *OccurrenceJournal, rpc CanonicalHeaderRPC, view *CanonicalValidator, queue chan<- stypes.ErrataBlock, timeout time.Duration) ([]int64, error) {
	generation := view.Generation()
	tip, err := rpc.GetTipHeader()
	if err != nil {
		return nil, err
	}
	if tip == nil || tip.Number == nil || !tip.Number.IsInt64() || tip.Number.Sign() < 0 {
		return nil, errors.New("reorg tip unavailable")
	}
	tipHeight, tipHash := tip.Number.Int64(), tip.Hash()
	metas, err := journal.accessor.GetBlockMetas()
	if err != nil {
		return nil, err
	}
	var dirty []int64
	targets := make(map[int64]string)
	groups := make(map[cleanupBlockKey][]stypes.ObservationOccurrence)
	var empty []cleanupBlockKey
	for _, meta := range metas {
		if meta.Height > tipHeight {
			return nil, errors.New("reorg metadata is above canonical snapshot")
		}
		header, headerErr := rpc.GetHeader(meta.Height)
		if headerErr != nil {
			return nil, headerErr
		}
		if header == nil || header.Number == nil || !header.Number.IsInt64() || header.Number.Int64() != meta.Height {
			return nil, errors.New("inconsistent canonical reorg header")
		}
		blockChanged := meta.BlockHash == "" || !strings.EqualFold(meta.BlockHash, header.Hash().Hex())
		changed := blockChanged
		for _, tx := range meta.Transactions {
			if tx.BlockHash != "" && strings.EqualFold(tx.BlockHash, header.Hash().Hex()) {
				continue
			}
			// A hashless legacy entry in an unchanged block is not displaced. It stays
			// in its metadata until SaveMeta re-extracts it from this exact block.
			if tx.BlockHash == "" && !blockChanged {
				continue
			}
			changed = true
			// Legacy metadata has no immutable transaction anchor. Never relabel it
			// with a current height-keyed meta hash.
			key := cleanupBlockKey{meta.Height, strings.ToLower(tx.BlockHash)}
			occurrence := stypes.ObservationOccurrence{Chain: journal.chain, TxID: common.TxID(tx.Hash), Height: meta.Height, BlockHash: tx.BlockHash}
			if tx.ConfirmationRequired != nil {
				occurrence.ConfirmationRequired = *tx.ConfirmationRequired
			}
			groups[key] = append(groups[key], occurrence)
		}
		if changed {
			dirty = append(dirty, meta.Height)
			targets[meta.Height] = header.Hash().Hex()
			if len(meta.Transactions) == 0 {
				empty = append(empty, cleanupBlockKey{meta.Height, meta.BlockHash})
			}
		}
	}
	if err = closeCanonicalTip(tipHeight, tipHash, rpc.GetTipHeader, rpc.GetHeader); err != nil {
		return nil, err
	}
	if !view.Current(generation) {
		return nil, errors.New("reorg classification generation changed")
	}

	if err = journal.retainClassification(metas, targets); err != nil {
		return nil, err
	}
	sort.Slice(empty, func(a, b int) bool { return empty[a].height < empty[b].height })
	for _, key := range empty {
		if err = journal.RequestBlock(key.height, key.hash); err != nil {
			return nil, err
		}
	}
	var ordered []stypes.ObservationOccurrence
	for _, occurrences := range groups {
		ordered = append(ordered, occurrences...)
	}
	sort.Slice(ordered, func(a, b int) bool {
		x, y := ordered[a], ordered[b]
		if x.Height != y.Height {
			return x.Height < y.Height
		}
		if !strings.EqualFold(x.BlockHash, y.BlockHash) {
			return strings.ToLower(x.BlockHash) < strings.ToLower(y.BlockHash)
		}
		return x.TxID.String() < y.TxID.String()
	})
	if err = journal.RequestAll(ordered, false); err != nil {
		return nil, err
	}

	sort.Slice(dirty, func(a, b int) bool { return dirty[a] < dirty[b] })
	if len(dirty) > 0 {
		if err = journal.RewindSearch(dirty[0]); err != nil {
			return nil, err
		}
	}
	if err = DrainOccurrenceCleanup(journal, view, queue, timeout); err != nil {
		return nil, err
	}
	next, ok, err := journal.NextRescan()
	if err != nil {
		return nil, err
	}
	if ok {
		return []int64{next}, nil
	}
	return nil, nil
}

// DrainOccurrenceCleanup sends local invalidations, not network corrections.
// Only durable acknowledgement permits the scanner to make that work replayable.
func DrainOccurrenceCleanup(journal *OccurrenceJournal, view *CanonicalValidator, queue chan<- stypes.ErrataBlock, timeout time.Duration) error {
	records, err := journal.cleanupRequests()
	if err != nil {
		return err
	}
	groups := make(map[cleanupBlockKey][]stypes.ObservationOccurrence)
	snapshots := make(map[cleanupBlockKey][]OccurrenceRequest)
	for _, record := range records {
		if record.Cleaned || record.Original.Hash == "" {
			continue
		}
		key := cleanupBlockKey{record.Original.BlockHeight, strings.ToLower(record.Original.BlockHash)}
		occurrence := stypes.ObservationOccurrence{Chain: journal.chain, TxID: common.TxID(record.Original.Hash), Height: key.height, BlockHash: key.hash}
		if record.Original.ConfirmationRequired != nil {
			occurrence.ConfirmationRequired = *record.Original.ConfirmationRequired
		}
		groups[key] = append(groups[key], occurrence)
		snapshots[key] = append(snapshots[key], record)
	}
	keys := make([]cleanupBlockKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(a, b int) bool {
		if keys[a].height != keys[b].height {
			return keys[a].height < keys[b].height
		}
		return keys[a].hash < keys[b].hash
	})
	for _, key := range keys {
		occurrences := groups[key]
		block := stypes.ErrataBlock{Height: key.height, BlockHash: key.hash, LocalOnly: true}
		block.RetainRequirements = func(removed []stypes.ObservationOccurrence) error {
			return journal.RetainCleanupRequirements(removed)
		}

		for _, occurrence := range occurrences {
			block.Txs = append(block.Txs, stypes.ErrataTx{Chain: journal.chain, TxID: occurrence.TxID})
		}
		view.Invalidate()
		if err = DeliverOccurrenceBlock(queue, block, timeout); err != nil {
			return err
		}
		if err = journal.acknowledgeCleanup(snapshots[key]); err != nil {
			return err
		}
	}
	return nil
}

// DeliverOccurrenceBlock waits for a durable acknowledgement within one deadline.
// Queue admission, a closed channel and a timeout are not proof of persistence.
func DeliverOccurrenceBlock(queue chan<- stypes.ErrataBlock, block stypes.ErrataBlock, timeout time.Duration) error {
	if queue == nil {
		return errors.New("Observer cleanup queue is unavailable")
	}
	if timeout <= 0 || timeout > 12*time.Second {
		timeout = 12 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	block.Persisted = make(chan error, 1)
	select {
	case queue <- block:
	case <-ctx.Done():
		return fmt.Errorf("%w: Observer handoff timed out", btypes.ErrUnavailableBlock)
	}
	select {
	case err, ok := <-block.Persisted:
		if !ok {
			return fmt.Errorf("%w: Observer acknowledgment closed without a result", btypes.ErrUnavailableBlock)
		}
		return err
	case <-ctx.Done():
		return fmt.Errorf("%w: Observer acknowledgment timed out", btypes.ErrUnavailableBlock)
	}
}
