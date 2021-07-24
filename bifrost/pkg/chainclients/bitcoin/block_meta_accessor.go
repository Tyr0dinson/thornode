package bitcoin

import (
	"gitlab.com/thorchain/thornode/bifrost/types"
	"gitlab.com/thorchain/thornode/common"
)

type PruneBlockMetaCallback func(meta *BlockMeta) bool

// BlockMetaAccessor define methods need to access block meta storage
type BlockMetaAccessor interface {
	GetBlockMetas() ([]*BlockMeta, error)
	GetBlockMeta(height int64) (*BlockMeta, error)
	SaveBlockMeta(height int64, blockMeta *BlockMeta) error
	PruneBlockMeta(height int64, callback PruneBlockMetaCallback) error
	UpsertTransactionFee(fee float64, vSize int32) error
	GetTransactionFee() (float64, int32, error)
	TryAddToMemPoolCache(hash string) (bool, error)
	RemoveFromMemPoolCache(hash string) error
	TryAddToObservedTxCache(hash string) (bool, error)
	RemoveObservedTxCache(hash string) error
	GetVaultBalance(chain common.Chain, address common.Address) (types.VaultBalance, error)
	SetVaultBalance(bal types.VaultBalance) error
}
