package types

import (
	"github.com/ethereum/go-ethereum/core/types"

	stypes "gitlab.com/thorchain/thornode/bifrost/thorclient/types"
)

// BlockMeta is a structure to store the blocks bifrost scanned
type BlockMeta struct {
	PreviousHash string            `json:"previous_hash"`
	Height       int64             `json:"height"`
	BlockHash    string            `json:"block_hash"`
	Transactions []TransactionMeta `json:"transactions"`
}

type TransactionMeta struct {
	Hash        string `json:"hash"`
	BlockHeight int64  `json:"block_height"`
}

type TokenMeta struct {
	Symbol  string `json:"symbool"`
	Address string `json:"address"`
}

// NewBlockMeta create a new instance of BlockMeta
func NewBlockMeta(block *types.Header, txIn stypes.TxIn) *BlockMeta {
	txsMeta := make([]TransactionMeta, 0)
	for _, tx := range txIn.TxArray {
		txsMeta = append(txsMeta, TransactionMeta{
			Hash:        tx.Tx,
			BlockHeight: block.Number.Int64(),
		})
	}
	return &BlockMeta{
		PreviousHash: block.ParentHash.Hex(),
		Height:       block.Number.Int64(),
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
