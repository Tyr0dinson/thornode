package observer

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

func txInItemMatchesIgnoringXMRMetadata(existing, incoming *types.TxInItem) bool {
	if existing == nil || incoming == nil {
		return false
	}
	if existing.BlockHeight != incoming.BlockHeight ||
		!txInItemTxIDsEqual(existing.Tx, incoming.Tx) ||
		existing.Memo != incoming.Memo ||
		existing.Sender != incoming.Sender ||
		existing.To != incoming.To ||
		!existing.Coins.EqualsEx(incoming.Coins) ||
		!existing.Gas.Equals(incoming.Gas) ||
		!existing.ObservedVaultPubKey.Equals(incoming.ObservedVaultPubKey) ||
		existing.Aggregator != incoming.Aggregator ||
		existing.AggregatorTarget != incoming.AggregatorTarget {
		return false
	}
	if existing.AggregatorTargetLimit == nil || incoming.AggregatorTargetLimit == nil {
		return existing.AggregatorTargetLimit == incoming.AggregatorTargetLimit
	}
	return existing.AggregatorTargetLimit.Equal(*incoming.AggregatorTargetLimit)
}

func txInItemTxIDsEqual(a, b string) bool {
	aID, aErr := common.NewTxID(a)
	bID, bErr := common.NewTxID(b)
	if aErr != nil || bErr != nil {
		return a == b
	}
	return aID.Equals(bID)
}

func mergeXMRMetadataIntoTxInItem(existing, incoming *types.TxInItem) bool {
	if existing == nil || incoming == nil {
		return false
	}

	changed := false
	mergedTxKeys := common.MergeXMRStrings(existing.TxKeys, incoming.TxKeys)
	if !sameStringSlice(existing.TxKeys, mergedTxKeys) {
		existing.TxKeys = mergedTxKeys
		changed = true
	}

	if len(incoming.XmrKeyImages) > 0 && !common.XmrKeyImageProofsEqual(existing.XmrKeyImages, incoming.XmrKeyImages) {
		existing.XmrKeyImages = common.CanonicalXMRKeyImageProofs(incoming.XmrKeyImages)
		changed = true
	}
	return changed
}

func coalesceXMRMetadataDuplicateDeckItems(chain common.Chain, items []*types.TxInItem) ([]*types.TxInItem, bool) {
	if !chain.Equals(common.XMRChain) || len(items) < 2 {
		return items, false
	}

	out := items[:0]
	changed := false
	for _, item := range items {
		if item == nil {
			out = append(out, item)
			continue
		}
		merged := false
		for _, existing := range out {
			if !txInItemMatchesIgnoringXMRMetadata(existing, item) {
				continue
			}
			if isConsolidateSelfSendItem(existing) && isConsolidateSelfSendItem(item) {
				continue
			}
			if mergeXMRMetadataIntoTxInItem(existing, item) {
				changed = true
			}
			if item.CommittedUnFinalised && !existing.CommittedUnFinalised {
				existing.CommittedUnFinalised = true
				changed = true
			}
			merged = true
			changed = true
			break
		}
		if !merged {
			out = append(out, item)
		}
	}
	return out, changed
}

func isConsolidateSelfSendItem(item *types.TxInItem) bool {
	return item != nil &&
		item.Sender != "" &&
		item.Sender == item.To &&
		strings.EqualFold(item.Memo, "consolidate")
}

func filteredObservationForVault(item *types.TxInItem, vaultPubKey common.PubKey) *types.TxInItem {
	tx := item.Copy()
	metadataVault := tx.ObservedVaultPubKey
	tx.ObservedVaultPubKey = vaultPubKey
	if !metadataVault.IsEmpty() && !metadataVault.Equals(vaultPubKey) {
		tx.TxKeys = nil
		tx.XmrKeyImages = nil
	}
	return tx
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
