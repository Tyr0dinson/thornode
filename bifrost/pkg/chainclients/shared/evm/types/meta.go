package types

import (
	"github.com/ethereum/go-ethereum/core/types"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
)

type SignedTxItem struct {
	Hash        string            `json:"hash,omitempty"`
	Height      int64             `json:"height,omitempty"`
	VaultPubKey string            `json:"vault_pub_key,omitempty"`
	TxOutItem   *stypes.TxOutItem `json:"tx_out_item,omitempty"`
}

// String implement fmt.Stringer
func (st SignedTxItem) String() string {
	return st.Hash
}

// BlockMeta is a structure to store the blocks bifrost scanned
type BlockMeta struct {
	PreviousHash string            `json:"previous_hash"`
	Height       int64             `json:"height"`
	BlockHash    string            `json:"block_hash"`
	Transactions []TransactionMeta `json:"transactions"`
}

// VaultAttribution is local network membership evidence captured during original
// extraction. The router travels with the key so replays cannot bypass the
// original vault/router association when the live pubkey manager prunes it.
type VaultAttribution = stypes.ObservationVaultAttribution

// TransactionMeta transaction meta data
type TransactionMeta struct {
	Hash        string `json:"hash"`
	BlockHeight int64  `json:"block_height"`
	// BlockHash is the original extracted occurrence, never a replacement label.
	BlockHash            string  `json:"block_hash,omitempty"`
	Sender               string  `json:"sender,omitempty"`
	Nonce                *uint64 `json:"nonce,omitempty"`
	ConfirmationRequired *int64  `json:"confirmation_required,omitempty"`
	// Inbound is positively classified at original extraction. False includes
	// legacy/ambiguous and vault transactions; a replacement cannot promote it.
	Inbound bool               `json:"inbound,omitempty"`
	Vaults  []VaultAttribution `json:"vaults,omitempty"`
}

// TokenMeta is a struct to store token meta data
type TokenMeta struct {
	Symbol  string `json:"symbol"`
	Address string `json:"address"`
	Decimal uint64 `json:"decimal"` // Decimal means the number of decimals https://docs.openzeppelin.com/contracts/3.x/api/token/erc20#ERC20-decimals--
}

// NewBlockMeta create a new instance of BlockMeta
func NewBlockMeta(block *types.Header, txIn stypes.TxIn) *BlockMeta {
	txsMeta := make([]TransactionMeta, 0, len(txIn.TxArray))
	for _, tx := range txIn.TxArray {
		// A readiness marker is not an extracted observation or new source evidence.
		if len(tx.EVMAttributionAddresses) != 0 || len(tx.EVMAttributionContracts) != 0 || tx.BlockHeight != block.Number.Int64() {
			continue
		}
		txsMeta = append(txsMeta, TransactionMeta{
			Hash:        tx.Tx,
			BlockHeight: tx.BlockHeight,
			BlockHash:   block.Hash().Hex(),
			Vaults:      append([]VaultAttribution(nil), tx.VaultAttribution...),
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
func NewTokenMeta(symbol, address string, decimal uint64) TokenMeta {
	return TokenMeta{
		Symbol:  symbol,
		Address: address,
		Decimal: decimal,
	}
}

// IsEmpty return true when both symbol and address are empty
func (tm TokenMeta) IsEmpty() bool {
	return tm.Symbol == "" && tm.Address == ""
}
