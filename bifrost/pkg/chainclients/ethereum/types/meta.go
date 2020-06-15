package types

import (
	"math/big"

	"github.com/ethereum/go-ethereum/core/types"
)

// BlockMeta is a structure to store the blocks bifrost scanned
type BlockMeta struct {
	PreviousHash string            `json:"previous_hash"`
	Height       int64             `json:"height"`
	BlockHash    string            `json:"block_hash"`
	Transactions []TransactionMeta `json:"transactions"`
}

type TransactionMeta struct {
	Hash        string   `json:"hash"`
	Value       *big.Int `json:"value"`
	BlockHeight int64    `json:"block_height"`
}

type TokenMeta struct {
	Symbol  string `json":symbool"`
	Address string `json:"address"`
}

// NewBlockMeta create a new instance of BlockMeta
func NewBlockMeta(block *types.Block) *BlockMeta {
	txsMeta := make([]TransactionMeta, 0)
	for _, tx := range block.Transactions() {
		txsMeta = append(txsMeta, TransactionMeta{
			Hash:        tx.Hash().Hex(),
			Value:       tx.Value(),
			BlockHeight: int64(block.NumberU64()),
		})
	}
	return &BlockMeta{
		PreviousHash: block.ParentHash().Hex(),
		Height:       int64(block.NumberU64()),
		BlockHash:    block.Hash().Hex(),
		Transactions: txsMeta,
	}
}

// NewTokenMeta create a new instance of TokenMeta
func NewTokenMeta(symbol, address string) *TokenMeta {
	return &TokenMeta{
		Symbol:  symbol,
		Address: address,
	}
}
