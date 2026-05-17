package tss

import (
	"fmt"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// KeysignError is a custom error create to include which party to blame
type KeysignError struct {
	Blame types.Blame
}

// PaillierValidationSecurityError marks a TSS keysign refusal caused by unsafe
// Paillier localstate material. Callers can use this type to trigger the
// operator-visible emergency path without duplicating chain-client logic.
type PaillierValidationSecurityError struct {
	PoolPubKey string
	Reason     string
	Blame      types.Blame
}

// NewKeysignError create a new instance of KeysignError
func NewKeysignError(blame types.Blame) KeysignError {
	return KeysignError{
		Blame: blame,
	}
}

// NewPaillierValidationSecurityError creates a typed security error for a
// Paillier validation failure discovered before local signing.
func NewPaillierValidationSecurityError(poolPubKey, reason string, blame types.Blame) PaillierValidationSecurityError {
	return PaillierValidationSecurityError{
		PoolPubKey: poolPubKey,
		Reason:     reason,
		Blame:      blame,
	}
}

// Error implement error interface
func (k KeysignError) Error() string {
	return fmt.Sprintf("fail to complete TSS keysign, reason:%s, round:%s, culprit:%+v", k.Blame.FailReason, k.Blame.Round, k.Blame.BlameNodes)
}

// Error implement error interface.
func (e PaillierValidationSecurityError) Error() string {
	return fmt.Sprintf("security critical TSS Paillier validation failure, pool_pub_key:%s, reason:%s, culprit:%+v", e.PoolPubKey, e.Reason, e.Blame.BlameNodes)
}

// Unwrap preserves compatibility with existing callers that inspect
// KeysignError for blame reporting.
func (e PaillierValidationSecurityError) Unwrap() error {
	return NewKeysignError(e.Blame)
}

func (k KeysignError) IsRound7() bool {
	round := types.CanonicalTssRound(k.Blame.Round, "")
	// Ed25519 reaches its terminal keysign phase at round 3, which is handled the
	// same way as secp256k1 round 7 by the retry/freeze paths.
	return round == tssMessages.KEYSIGN7 || round == tssMessages.EDDSAKEYSIGN3
}
