package evm

import (
	"fmt"
	"strings"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

// BlockMetaAccessor define methods need to access block meta storage
type BlockMetaAccessor interface {
	GetBlockMetas() ([]*evmtypes.BlockMeta, error)
	GetBlockMeta(height int64) (*evmtypes.BlockMeta, error)
	SaveBlockMeta(height int64, block *evmtypes.BlockMeta) error
	PruneBlockMeta(height int64) error
	AddSignedTxItem(item evmtypes.SignedTxItem) error
	RemoveSignedTxItem(hash string) error
	GetSignedTxItems() ([]evmtypes.SignedTxItem, error)
}

// RetainTransactionIdentity records sender/nonce from full block transactions,
// not from vault-output filtering. Missing legacy metadata remains unknown. An
// OP-stack deposit keeps its sender but no nonce: it has no nonce constraint and
// may later execute with another effective nonce, so a different spender cannot
// prove it invalid. Its sender still classifies it for failed-execution correction.
func RetainTransactionIdentity(meta *evmtypes.BlockMeta, block *etypes.Block, signer etypes.Signer) error {
	if block == nil {
		return fmt.Errorf("original transaction block is unavailable")
	}
	byHash := make(map[ecommon.Hash]*etypes.Transaction, len(block.Transactions()))
	for _, tx := range block.Transactions() {
		if tx == nil {
			return fmt.Errorf("original transaction block contains nil transaction")
		}
		byHash[tx.Hash()] = tx
	}
	for i := range meta.Transactions {
		tx := byHash[ecommon.HexToHash(meta.Transactions[i].Hash)]
		if tx == nil || signer == nil {
			return fmt.Errorf("original transaction %s is not in extracted block", meta.Transactions[i].Hash)
		}
		sender, err := signer.Sender(tx)
		if err != nil {
			return fmt.Errorf("original transaction sender: %w", err)
		}
		meta.Transactions[i].Sender = sender.Hex()
		if tx.Type() == etypes.DepositTxType {
			continue
		}
		nonce := tx.Nonce()
		meta.Transactions[i].Nonce = &nonce
	}
	return nil
}

// RetainInboundDirection classifies only an unambiguous external-sender deposit.
// Contract-attributed senders, internal transfers and missing legacy data hold.
// SaveMeta preserves the first classification for an exact occurrence.
func RetainInboundDirection(meta *evmtypes.BlockMeta, txIn stypes.TxIn, isVault func(string) bool) {
	for i := range meta.Transactions {
		tx := &meta.Transactions[i]
		matched := false
		inbound := true
		for _, item := range txIn.TxArray {
			if item == nil || ecommon.HexToHash(item.Tx) != ecommon.HexToHash(tx.Hash) {
				continue
			}
			matched = true
			// Use the same authenticated endpoint snapshot as extraction. A
			// registry prune before SaveMeta must not turn an internal transfer
			// into an inbound, or erase the classification of a real deposit.
			itemVault := func(address string) bool {
				for _, vault := range item.VaultAttribution {
					derived, err := vault.PubKey.GetAddress(txIn.Chain)
					if err == nil && strings.EqualFold(derived.String(), address) {
						return true
					}
				}
				return isVault != nil && isVault(address)
			}
			parsed, memoErr := memo.ParseMemo(common.LatestVersion, item.Memo)
			// A keeperless parse rejects THORName destinations and affiliates and
			// streaming limits, which THORNode still credits. Only an empty memo stays
			// ambiguous: a pruned vault's memoless outbound would look the same. A
			// memo that does parse must be an inbound type.
			inboundMemo := item.Memo != "" && (memoErr != nil || parsed.IsInbound())
			inbound = inbound && inboundMemo && !item.EVMFailedSynthetic && tx.Sender != "" &&
				strings.EqualFold(tx.Sender, item.Sender) && !itemVault(item.Sender) && itemVault(item.To)
		}
		tx.Inbound = matched && inbound
	}
}
