package evm

import (
	"encoding/binary"
	"errors"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/trie"
	shared "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/common"
)

// Bor historically appended a synthetic state-sync transaction to RPC blocks.
// Its receipt uses keccak256("matic-bor-receipt-" || uint64be(height) || hash),
// not the empty legacy transaction's RLP hash recomputed by our Go client.
// See 0xPolygon/bor v2.5.9 core/types/bor_receipt.go and internal/ethapi/bor_api.go.
// Preserve the existing filtering of that system receipt, without allowing an
// arbitrary unmatched receipt to bypass ordinary transaction completeness checks.
func validateBlockReceipts(chain common.Chain, block *etypes.Block, receipts []*etypes.Receipt) ([]*etypes.Receipt, error) {
	if block == nil || block.Number() == nil || !block.Number().IsUint64() {
		return nil, errors.New("block receipt anchor is invalid")
	}
	transactions := block.Transactions()
	expected := make(map[ecommon.Hash]*etypes.Transaction, len(transactions))
	var synthetic ecommon.Hash
	for index, tx := range transactions {
		if tx == nil {
			return nil, errors.New("nil block transaction")
		}
		hash := tx.Hash()
		if chain.Equals(common.POLChain) && index == len(transactions)-1 && isBorSyntheticTransaction(tx) {
			// This RPC-only transaction must be outside the consensus transaction
			// root. Compute the root only for this narrowly recognized Bor shape.
			root := etypes.DeriveSha(transactions[:index], trie.NewStackTrie(nil))
			if root != block.TxHash() {
				return nil, errors.New("Bor synthetic transaction is not an RPC-only append")
			}
			synthetic = borDerivedTransactionHash(block)
			hash = synthetic
		}
		if expected[hash] != nil {
			return nil, errors.New("duplicated block transaction")
		}
		expected[hash] = tx
	}
	ordinaryExpected := len(expected)
	if synthetic != (ecommon.Hash{}) {
		// Pre-Madhugiri Bor gets the appended transaction from its lookup
		// index and fetches its receipt separately. That receipt may be absent
		// (internal/ethapi/api.go:GetBlockReceipts in Bor v2.5.9). It is never
		// observed here, so require every ordinary receipt, not this optional
		// RPC-only receipt. A supplied system receipt must still validate.
		ordinaryExpected--
	}
	if len(receipts) < ordinaryExpected || len(receipts) > len(expected) {
		return nil, errors.New("block receipts are incomplete")
	}
	seen := make(map[ecommon.Hash]bool, len(receipts))
	ordinary := make([]*etypes.Receipt, 0, len(receipts))
	for _, receipt := range receipts {
		if receipt == nil || seen[receipt.TxHash] {
			return nil, errors.New("nil or duplicated block receipt")
		}
		seen[receipt.TxHash] = true
		if synthetic != (ecommon.Hash{}) && receipt.TxHash == synthetic {
			if err := validateBorReceipt(block, receipt); err != nil {
				return nil, err
			}
			continue
		}
		if err := shared.ValidateReceiptLocation(block, expected[receipt.TxHash], receipt); err != nil {
			return nil, err
		}
		ordinary = append(ordinary, receipt)
	}
	if len(ordinary) != ordinaryExpected {
		return nil, errors.New("ordinary block receipts are incomplete")
	}
	return ordinary, nil
}

func isBorSyntheticTransaction(tx *etypes.Transaction) bool {
	if tx.Type() != etypes.LegacyTxType || tx.To() == nil || *tx.To() != (ecommon.Address{}) || tx.Nonce() != 0 || tx.Gas() != 0 || tx.GasPrice().Sign() != 0 || tx.Value().Sign() != 0 || len(tx.Data()) != 0 {
		return false
	}
	v, r, s := tx.RawSignatureValues()
	return v.Sign() == 0 && r.Sign() == 0 && s.Sign() == 0
}

func borDerivedTransactionHash(block *etypes.Block) ecommon.Hash {
	var height [8]byte
	binary.BigEndian.PutUint64(height[:], block.NumberU64())
	return crypto.Keccak256Hash([]byte("matic-bor-receipt-"), height[:], block.Hash().Bytes())
}

func validateBorReceipt(block *etypes.Block, receipt *etypes.Receipt) error {
	index := uint(len(block.Transactions()) - 1)
	if receipt.BlockNumber == nil || receipt.BlockNumber.Cmp(block.Number()) != 0 || receipt.BlockHash != block.Hash() || receipt.TransactionIndex != index || receipt.Type != etypes.LegacyTxType || receipt.GasUsed != 0 || receipt.Status != etypes.ReceiptStatusSuccessful {
		return errors.New("Bor synthetic receipt does not belong to extracted block")
	}
	for _, entry := range receipt.Logs {
		if entry == nil || entry.Removed || entry.BlockHash != block.Hash() || entry.BlockNumber != block.NumberU64() || entry.TxHash != receipt.TxHash || entry.TxIndex != index {
			return errors.New("Bor synthetic receipt contains an inconsistent log")
		}
	}
	return nil
}
