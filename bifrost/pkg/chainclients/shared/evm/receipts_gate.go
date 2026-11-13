package evm

import (
	etypes "github.com/ethereum/go-ethereum/core/types"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
)

// BlockMayHaveSignedTx checks whether a receipt is needed to release a failed
// outbound, even if the signing vault has since left the node's vault list.
func BlockMayHaveSignedTx(block *etypes.Block, cache *signercache.CacheManager) bool {
	if cache == nil {
		return false
	}
	for _, tx := range block.Transactions() {
		if tx == nil || tx.Type() == etypes.BlobTxType || tx.To() == nil {
			continue
		}
		if cache.MayHaveSignedTx(tx.Hash().Hex()) {
			return true
		}
	}
	return false
}
