package tss

import (
	"fmt"

	tctypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

type SigningIntentType string

const (
	// SigningIntentTxOut authorizes signing only when the raw message belongs to
	// a TxOutItem that THORChain has scheduled for the same height and vault.
	SigningIntentTxOut SigningIntentType = "txout"
	// SigningIntentKeygenVerification is the narrow non-TxOut signing case used
	// during keygen to prove the newly created secp256k1 pool pubkey can sign.
	SigningIntentKeygenVerification SigningIntentType = "keygen_verification"
	// SigningIntentEVMUnstuck authorizes a gas-only EVM replacement transaction
	// that cancels a stuck outbound while preserving the original scheduled TxOut
	// as the source of authority.
	SigningIntentEVMUnstuck SigningIntentType = "evm_unstuck"
	// SigningIntentUTXOConsolidation authorizes the local UTXO maintenance path
	// that folds many vault UTXOs back into the same active Asgard vault.
	SigningIntentUTXOConsolidation SigningIntentType = "utxo_consolidation"
)

// SigningIntent binds a raw TSS signature request to the THORChain action that
// is allowed to produce it.
type SigningIntent struct {
	Type         SigningIntentType
	TxOut        tctypes.TxOutItem
	Height       int64
	KeygenPubKey common.PubKey
	MessageHash  []byte
}

// NewTxOutSigningIntent creates the authorization context for a normal outbound
// keysign. The height is kept explicitly because TxOutItem.Equals intentionally
// ignores height for deduplication.
func NewTxOutSigningIntent(tx tctypes.TxOutItem, height int64) SigningIntent {
	return SigningIntent{
		Type:   SigningIntentTxOut,
		TxOut:  tx,
		Height: height,
	}
}

// NewEVMUnstuckSigningIntent creates the authorization context for a gas-only
// replacement transaction used to unstick an already-scheduled EVM outbound.
func NewEVMUnstuckSigningIntent(tx tctypes.TxOutItem, height int64) SigningIntent {
	return SigningIntent{
		Type:   SigningIntentEVMUnstuck,
		TxOut:  tx,
		Height: height,
	}
}

// NewUTXOConsolidationSigningIntent creates the authorization context for a
// local vault self-send that consolidates UTXO dust/fragmentation.
func NewUTXOConsolidationSigningIntent(tx tctypes.TxOutItem, height int64) SigningIntent {
	return SigningIntent{
		Type:   SigningIntentUTXOConsolidation,
		TxOut:  tx,
		Height: height,
	}
}

// NewKeygenVerificationSigningIntent creates the limited authorization context
// for the keygen self-signature check.
func NewKeygenVerificationSigningIntent(poolPubKey common.PubKey) SigningIntent {
	return SigningIntent{
		Type:         SigningIntentKeygenVerification,
		KeygenPubKey: poolPubKey,
	}
}

// WithMessageHash binds an already-validated chain transaction to the exact raw
// message handed to TSS. This is used by EVM unstuck, where the wrapper can
// inspect the replacement transaction before only its hash reaches RemoteSign.
func (i SigningIntent) WithMessageHash(msg []byte) SigningIntent {
	i.MessageHash = append([]byte(nil), msg...)
	return i
}

// UnauthorizedSignRequestError marks signing requests refused before they reach
// the local TSS server.
type UnauthorizedSignRequestError struct {
	Reason string
}

// Error returns a stable, grep-friendly message for blocked signing attempts.
func (e UnauthorizedSignRequestError) Error() string {
	return fmt.Sprintf("unauthorized TSS signing request: %s", e.Reason)
}
