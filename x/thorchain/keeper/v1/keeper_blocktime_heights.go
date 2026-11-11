package keeperv1

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

// RefBlocksSince returns the wall-clock time since height, in reference blocks
// rounded down: ctx.BlockHeight() - height without a block time change. A
// lockup of N reference blocks from height is over once it reaches N.
func (k KVStore) RefBlocksSince(ctx cosmos.Context, height int64) int64 {
	refMs := constants.ThorchainBlockTime.Milliseconds()
	elapsed := k.GetBlockTimeHistory(ctx).ElapsedMs(height, ctx.BlockHeight(), refMs)
	if elapsed < 0 {
		// round toward negative infinity, as for a positive span
		return -((-elapsed + refMs - 1) / refMs)
	}
	return elapsed / refMs
}

// RefHeightAfter returns the first height at which refBlocks reference blocks
// of wall-clock time have passed since from, at the current block time beyond
// the current height: from + refBlocks without a block time change.
func (k KVStore) RefHeightAfter(ctx cosmos.Context, from, refBlocks int64) int64 {
	refMs := constants.ThorchainBlockTime.Milliseconds()
	if refBlocks > (1<<62)/refMs {
		refBlocks = (1 << 62) / refMs
	}
	return k.GetBlockTimeHistory(ctx).HeightAfter(from, refBlocks*refMs, refMs)
}

// convertStoredHeights moves the deadlines stored as future block heights when
// the block time changes from prevMs to newMs at this block, so each keeps the
// wall-clock time it had left: a deadline at D becomes the first height at
// which (D - (H-1)) blocks of prevMs have passed since H-1. Deadlines at or
// before this block are left alone, except observation tallies due in it
// (convertObservationTallies). It covers jail releases, the outbound
// delay queue (and its voters' outbound heights), THORName expiries, the
// limit-swap TTL index, the observation tally queue and guard refill heights,
// refund cleanup marker expiries, and the node chain-pause height.
func (k KVStore) convertStoredHeights(ctx cosmos.Context, prevMs, newMs int64) {
	height := ctx.BlockHeight()
	convert := func(deadline int64) int64 {
		return constants.ConvertHeight(height, deadline, prevMs, newMs)
	}

	k.convertJailReleaseHeights(ctx, convert)
	k.convertTxOutQueue(ctx, convert)
	k.convertTHORNameExpiries(ctx, convert)
	k.convertLimitSwapTTLs(ctx, convert)
	k.convertObservationTallies(ctx, convert)
	k.convertRefundCleanupExpiries(ctx, convert)
	k.convertObservationGuardFullAt(ctx, convert)

	if pause, err := k.GetMimir(ctx, "NodePauseChainGlobal"); err == nil && pause > height {
		k.SetMimir(ctx, "NodePauseChainGlobal", convert(pause))
	}
}

func (k KVStore) convertJailReleaseHeights(ctx cosmos.Context, convert func(int64) int64) {
	type entry struct {
		key  []byte
		jail Jail
	}
	var jails []entry
	iter := k.getIterator(ctx, prefixNodeJail)
	for ; iter.Valid(); iter.Next() {
		var jail Jail
		if err := k.cdc.Unmarshal(iter.Value(), &jail); err != nil {
			ctx.Logger().Error("fail to unmarshal jail", "error", err)
			continue
		}
		if converted := convert(jail.ReleaseHeight); converted != jail.ReleaseHeight {
			jail.ReleaseHeight = converted
			jails = append(jails, entry{key: append([]byte(nil), iter.Key()...), jail: jail})
		}
	}
	_ = iter.Close()
	for _, e := range jails {
		k.setJail(ctx, e.key, e.jail)
	}
}

func (k KVStore) convertTxOutQueue(ctx cosmos.Context, convert func(int64) int64) {
	height := ctx.BlockHeight()
	// the whole queue as scheduled under every earlier block time: outbounds
	// go up to TxOutDelayMax + MaxTxOutOffset ahead (as txOutScanEnd), and any
	// left unconverted would sit past a gap that scans of the queue stop at
	maxOffset := k.GetConfigInt64Ref(ctx, constants.MaxTxOutOffset)
	refWindow := max(k.GetConfigInt64Ref(ctx, constants.TxOutDelayMax), 0) + max(maxOffset, 0)
	scanEnd := k.RefWindowEnd(ctx, max(refWindow, constants.TxOutQueueScanBlocks))
	offsetEnd := k.RefWindowEnd(ctx, maxOffset)
	var queued []*TxOut
	for h := height + 1; h <= scanEnd; h++ {
		txOut, err := k.GetTxOut(ctx, h)
		if err != nil {
			ctx.Logger().Error("fail to get txout", "height", h, "error", err)
			continue
		}
		if len(txOut.TxArray) == 0 {
			if h > offsetEnd {
				break
			}
			continue
		}
		if convert(h) != h {
			queued = append(queued, txOut)
		}
	}
	// Each height keeps the wall-clock time it had left, except that past the
	// offset the queue stays contiguous: scans of the queue stop at the first
	// empty height past the offset, so a converted height there must follow the
	// previous queued height (or the offset) directly, or the outbounds after
	// the gap would be missed. Those outbounds go out a little sooner, never
	// later.
	targets := make(map[int64]int64, len(queued))
	prevTarget := height
	for _, txOut := range queued {
		target := min(convert(txOut.Height), max(prevTarget, offsetEnd)+1)
		targets[txOut.Height] = target
		prevTarget = target
	}
	// clear every moved height before refilling, as targets can be moved heights
	for _, txOut := range queued {
		_ = k.ClearTxOut(ctx, txOut.Height)
	}
	var inHashes []common.TxID
	seen := make(map[common.TxID]bool)
	for _, txOut := range queued {
		target := targets[txOut.Height]
		for _, item := range txOut.TxArray {
			if err := k.AppendTxOut(ctx, target, item); err != nil {
				ctx.Logger().Error("fail to move txout item", "from", txOut.Height, "to", target, "error", err)
			}
			if !item.InHash.IsEmpty() && !seen[item.InHash] {
				seen[item.InHash] = true
				inHashes = append(inHashes, item.InHash)
			}
		}
	}
	for _, hash := range inHashes {
		voter, err := k.GetObservedTxInVoter(ctx, hash)
		if err != nil {
			continue
		}
		if target, ok := targets[voter.OutboundHeight]; ok && target != voter.OutboundHeight {
			voter.OutboundHeight = target
			k.SetObservedTxInVoter(ctx, voter)
		}
	}
}

func (k KVStore) convertTHORNameExpiries(ctx cosmos.Context, convert func(int64) int64) {
	var names []THORName
	iter := k.GetTHORNameIterator(ctx)
	for ; iter.Valid(); iter.Next() {
		var name THORName
		if err := k.cdc.Unmarshal(iter.Value(), &name); err != nil {
			ctx.Logger().Error("fail to unmarshal thorname", "error", err)
			continue
		}
		if converted := convert(name.ExpireBlockHeight); converted != name.ExpireBlockHeight {
			name.ExpireBlockHeight = converted
			names = append(names, name)
		}
	}
	_ = iter.Close()
	for _, name := range names {
		k.SetTHORName(ctx, name)
	}
}

func (k KVStore) convertLimitSwapTTLs(ctx cosmos.Context, convert func(int64) int64) {
	type entry struct {
		height int64
		hashes []common.TxID
	}
	var entries []entry
	prefix := string(k.GetKey(prefixAdvSwapQueueTTL, ""))
	iter := k.getIterator(ctx, prefixAdvSwapQueueTTL)
	for ; iter.Valid(); iter.Next() {
		h, err := strconv.ParseInt(strings.TrimPrefix(string(iter.Key()), prefix), 10, 64)
		if err != nil || convert(h) == h {
			continue
		}
		var record ProtoStrings
		if err := k.cdc.Unmarshal(iter.Value(), &record); err != nil {
			ctx.Logger().Error("fail to unmarshal limit swap ttl", "height", h, "error", err)
			continue
		}
		e := entry{height: h}
		for _, s := range record.Value {
			if hash, err := common.NewTxID(s); err == nil {
				e.hashes = append(e.hashes, hash)
			}
		}
		entries = append(entries, e)
	}
	_ = iter.Close()
	for _, e := range entries {
		k.RemoveLimitSwapTTL(ctx, e.height)
	}
	// Group the moved hashes by destination, in the order the walk met them,
	// and read and write each destination once. Adding them one at a time
	// rewrote the growing list per hash, quadratic in a bucket's size. The
	// result is the same: the destination's own hashes first, then each
	// source's in key order, without hashes already there (TxID.Equals).
	var dests []int64
	moved := map[int64][]common.TxID{}
	for _, e := range entries {
		d := convert(e.height)
		if _, ok := moved[d]; !ok {
			dests = append(dests, d)
		}
		moved[d] = append(moved[d], e.hashes...)
	}
	for _, d := range dests {
		if len(moved[d]) == 0 {
			continue
		}
		hashes, err := k.GetLimitSwapTTL(ctx, d)
		if err != nil {
			ctx.Logger().Error("fail to move limit swap ttl", "to", d, "error", err)
			continue
		}
		seen := make(map[string]bool, len(hashes)+len(moved[d]))
		for _, hash := range hashes {
			seen[txIDFoldKey(hash)] = true
		}
		added := false
		for _, hash := range moved[d] {
			if key := txIDFoldKey(hash); !seen[key] {
				seen[key] = true
				hashes = append(hashes, hash)
				added = true
			}
		}
		if !added {
			continue
		}
		if err := k.SetLimitSwapTTL(ctx, d, hashes); err != nil {
			ctx.Logger().Error("fail to move limit swap ttl", "to", d, "error", err)
		}
	}
}

// txIDFoldKey maps a TxID to the smallest rune of each rune's simple case
// folding orbit, so two TxIDs share a key exactly when TxID.Equals
// (strings.EqualFold) holds; invalid UTF-8 becomes RuneError on both sides.
func txIDFoldKey(hash common.TxID) string {
	return strings.Map(func(r rune) rune {
		lowest := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			lowest = min(lowest, f)
		}
		return lowest
	}, hash.String())
}

// convertObservationTallies re-keys the queued observation tallies, which are
// keyed by due height, to the height their ObservationDelayFlexibility window
// now closes at: RefHeightAfter from the voter's consensus height, over the
// history that includes this change. That is the boundary late observations
// are refunded by (withinObservationFlex). Converting the stored height
// instead rounds up once per change, and after a change from a block time
// that does not divide the window, can close it blocks after the refund does.
// Tallies due in this block are recomputed too: they are scored in this
// block's EndBlock, after the change. An entry whose consensus height cannot
// be read keeps the converted height.
func (k KVStore) convertObservationTallies(ctx cosmos.Context, convert func(int64) int64) {
	type entry struct {
		key, newKey, value []byte
	}
	var entries []entry
	flex := max(k.GetConfigInt64Ref(ctx, constants.ObservationDelayFlexibility), 0)
	prefix := string(k.GetKey(prefixObservationTallyDue, ""))
	iter := k.getIterator(ctx, types.DbPrefix(prefix))
	for ; iter.Valid(); iter.Next() {
		dueStr, rest, _ := strings.Cut(strings.TrimPrefix(string(iter.Key()), prefix), "/")
		due, err := strconv.ParseInt(dueStr, 10, 64)
		// a tally due in this block is scored in its EndBlock, after this
		// change: a halving on the deadline moves its window on a block too
		if err != nil || due < ctx.BlockHeight() {
			continue
		}
		newDue := convert(due)
		var record ProtoStrings
		if err := k.cdc.Unmarshal(iter.Value(), &record); err == nil && len(record.Value) >= 3 {
			if consensus, err := strconv.ParseInt(record.Value[2], 10, 64); err == nil {
				newDue = max(k.RefHeightAfter(ctx, consensus, flex), ctx.BlockHeight())
			}
		}
		if newDue == due {
			continue
		}
		entries = append(entries, entry{
			key:    append([]byte(nil), iter.Key()...),
			newKey: []byte(fmt.Sprintf("%s%020d/%s", prefix, newDue, rest)),
			value:  append([]byte(nil), iter.Value()...),
		})
	}
	_ = iter.Close()
	store := k.kvStore(ctx)
	for _, e := range entries {
		store.Delete(e.key)
	}
	for _, e := range entries {
		store.Set(e.newKey, e.value)
	}
}

// convertRefundCleanupExpiries moves each refund cleanup marker's expiry, both
// the marker and its expiry index entry, to the converted height.
func (k KVStore) convertRefundCleanupExpiries(ctx cosmos.Context, convert func(int64) int64) {
	type entry struct {
		key, oldIndex string
		expiry        int64
	}
	var entries []entry
	prefix := string(prefixRefundCleanupExpiry)
	iter := k.getIterator(ctx, prefixRefundCleanupExpiry)
	for ; iter.Valid(); iter.Next() {
		expiryStr, key, _ := strings.Cut(strings.TrimPrefix(string(iter.Key()), prefix), "/")
		expiry, err := strconv.ParseInt(expiryStr, 10, 64)
		if err != nil || convert(expiry) == expiry {
			continue
		}
		entries = append(entries, entry{key: key, oldIndex: string(iter.Key()), expiry: expiry})
	}
	_ = iter.Close()
	for _, e := range entries {
		k.del(ctx, []byte(e.oldIndex))
	}
	for _, e := range entries {
		converted := convert(e.expiry)
		directKey := k.GetKey(prefixRefundCleanup, e.key)
		var stored int64
		if found, err := k.getInt64(ctx, directKey, &stored); err == nil && found && stored == e.expiry {
			k.setInt64(ctx, directKey, converted)
		}
		k.setInt64(ctx, refundCleanupExpiryKey(converted, e.key), 1)
	}
}

// convertObservationGuardFullAt moves each chain's missing-observation guard
// refill height.
func (k KVStore) convertObservationGuardFullAt(ctx cosmos.Context, convert func(int64) int64) {
	type entry struct {
		key    []byte
		fullAt int64
	}
	var entries []entry
	iter := k.getIterator(ctx, types.DbPrefix(k.GetKey(prefixObservationGuardFull, "")))
	for ; iter.Valid(); iter.Next() {
		var fullAt ProtoInt64
		if err := k.cdc.Unmarshal(iter.Value(), &fullAt); err != nil {
			ctx.Logger().Error("fail to unmarshal observation guard full-at", "error", err)
			continue
		}
		if converted := convert(fullAt.Value); converted != fullAt.Value {
			entries = append(entries, entry{key: append([]byte(nil), iter.Key()...), fullAt: converted})
		}
	}
	_ = iter.Close()
	for _, e := range entries {
		k.setInt64(ctx, e.key, e.fullAt)
	}
}
