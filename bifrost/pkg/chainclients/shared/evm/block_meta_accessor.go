package evm

import (
	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
)

// BlockMetaUpdateFunc mutates a block meta snapshot before the accessor commits requested saves.
type BlockMetaUpdateFunc func(blockMeta *evmtypes.BlockMeta) (save bool, err error)

// SignedTxItemProcessFunc processes a signed tx item snapshot before the accessor removes requested items.
type SignedTxItemProcessFunc func(item evmtypes.SignedTxItem) (remove bool, stop bool, err error)

// BlockMetaAccessor define methods need to access block meta storage
type BlockMetaAccessor interface {
	GetBlockMetas() ([]*evmtypes.BlockMeta, error)
	GetBlockMeta(height int64) (*evmtypes.BlockMeta, error)
	SaveBlockMeta(height int64, block *evmtypes.BlockMeta) error
	AppendBlockMetaTransaction(height int64, tx evmtypes.TransactionMeta) (bool, error)
	UpdateBlockMetas(update BlockMetaUpdateFunc) error
	PruneBlockMeta(height int64) error
	AddSignedTxItem(item evmtypes.SignedTxItem) error
	RemoveSignedTxItem(hash string) error
	GetSignedTxItems() ([]evmtypes.SignedTxItem, error)
	ProcessSignedTxItems(process SignedTxItemProcessFunc) error
}
