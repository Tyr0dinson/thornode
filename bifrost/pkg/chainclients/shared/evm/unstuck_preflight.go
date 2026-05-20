package evm

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
)

// IsEVMUnstuckTxOutSignable checks THORChain's current /keysign payload before
// the caller asks TSS to sign an EVM replacement transaction.
func IsEVMUnstuckTxOutSignable(bridge thorclient.ThorchainBridge, tx *stypes.TxOutItem) (bool, string, error) {
	if tx == nil {
		return false, "missing txout item", nil
	}
	if tx.Height <= 0 {
		return false, "missing txout height", nil
	}
	if tx.VaultPubKey.IsEmpty() {
		return false, "missing txout signing pubkey", nil
	}

	keysign, err := bridge.GetKeysign(tx.Height, tx.VaultPubKey.String())
	if err != nil {
		return false, "", fmt.Errorf("failed to fetch scheduled keysign: %w", err)
	}

	for _, item := range keysign.TxArray {
		if !item.TxOutItem(tx.Height).Equals(*tx) {
			continue
		}
		if !item.OutHash.IsEmpty() {
			return false, "scheduled txout already has an out hash", nil
		}
		return true, "", nil
	}

	return false, "txout was not found in scheduled keysign", nil
}
