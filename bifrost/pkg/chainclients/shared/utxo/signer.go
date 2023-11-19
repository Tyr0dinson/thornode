package utxo

import (
	"errors"
	"fmt"

	"github.com/hashicorp/go-multierror"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
)

// BatchTxInfo contains per-transaction data for batch signed transactions
type BatchTxInfo struct {
	OutputAmount uint64 `json:"output_amount"` // Amount sent in this output (in sats)
	UnbatchedGas uint64 `json:"unbatched_gas"` // Gas this tx would use if sent alone (in sats)
	OutputIndex  int    `json:"output_index"`  // Index of the customer output in the wire transaction
}

// SignCheckpoint is used to checkpoint the built transaction before signing, for use in
// round 7 signing errors which must reuse the same inputs.
type SignCheckpoint struct {
	UnsignedTx        []byte           `json:"unsigned_tx"`
	IndividualAmounts map[string]int64 `json:"individual_amounts"`
	// Bitcoin wire encoding omits Zcash's expiry and consensus branch.
	// Preserve both so retries sign the original digest.
	ZecExpiryHeight      uint32        `json:"zec_expiry_height,omitempty"`
	ZecConsensusBranchID uint32        `json:"zec_consensus_branch_id,omitempty"`
	BatchTxInfos         []BatchTxInfo `json:"batch_tx_infos,omitempty"`
}

func PostKeysignFailure(
	thorchainBridge thorclient.ThorchainBridge,
	tx stypes.TxOutItem,
	logger zerolog.Logger,
	utxoErr error,
) error {
	// PostKeysignFailure only once per SignTx, to not broadcast duplicate messages.
	var keysignError tss.KeysignError
	if errors.As(utxoErr, &keysignError) {
		if len(keysignError.Blame.BlameNodes) == 0 {
			// TSS doesn't know which node to blame
			utxoErr = multierror.Append(utxoErr, fmt.Errorf("fail to sign UTXO"))
			return fmt.Errorf("fail to sign the message: %w", utxoErr)
		}

		// key sign error forward the keysign blame to thorchain
		txID, err := thorchainBridge.PostKeysignFailure(keysignError.Blame, tx.Height, tx.GetMemo(), tx.Coins, tx.VaultPubKey)
		if err != nil {
			logger.Error().Err(err).Msg("fail to post keysign failure to thorchain")
			utxoErr = multierror.Append(utxoErr, fmt.Errorf("fail to post keysign failure to THORChain: %w", err))
			return fmt.Errorf("fail to sign the message: %w", utxoErr)
		}
		logger.Info().Str("tx_id", txID.String()).Msgf("post keysign failure to thorchain")
	}
	return utxoErr
}
