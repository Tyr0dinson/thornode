package thorchain

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// deduplicateAttestations returns attestations deduplicated by pubkey, capped at maxCount.
// This prevents a malicious block proposer from including thousands of duplicate attestations
// to cause excessive signature verification and KV store operations.
func deduplicateAttestations(attestations []*common.Attestation, maxCount int) []*common.Attestation {
	if len(attestations) == 0 {
		return attestations
	}
	seen := make(map[string]struct{})
	result := make([]*common.Attestation, 0, min(len(attestations), maxCount))
	for _, att := range attestations {
		if att == nil {
			continue
		}
		key := string(att.PubKey)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, att)
		if len(result) >= maxCount {
			break
		}
	}
	return result
}

func verifyQuorumAttestation(activeNodeAccounts NodeAccounts, signBz []byte, att *common.Attestation) (cosmos.AccAddress, error) {
	if att == nil {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "quorum", "attestation_rejected"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "nil")},
		)
		return nil, fmt.Errorf("attestation is nil")
	}
	if len(att.PubKey) == 0 {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "quorum", "attestation_rejected"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "empty_pubkey")},
		)
		return nil, fmt.Errorf("pubkey is empty")
	}
	if len(att.Signature) == 0 {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "quorum", "attestation_rejected"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "empty_signature")},
		)
		return nil, fmt.Errorf("signature is empty")
	}

	pk := secp256k1.PubKey{Key: att.PubKey}

	bech32Pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, &pk)
	if err != nil {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "quorum", "attestation_rejected"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "bech32_fail")},
		)
		return nil, fmt.Errorf("fail to get bech32 pub key: %w", err)
	}

	// check if the signer is an active node account
	found := false
	for _, validator := range activeNodeAccounts {
		if bech32Pub == validator.PubKeySet.Secp256k1.String() {
			found = true
			break
		}
	}
	if !found {
		// can occur if a node account churns out before the tx is processed.
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "quorum", "attestation_rejected"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "not_active_node")},
		)
		return nil, fmt.Errorf("signer is not an active node account: %s", pk.String())
	}

	if !pk.VerifySignature(signBz, att.Signature) {
		// A node signing payloads the rest of the network cannot verify is on a
		// mismatched build or malfunctioning; its observations are discarded.
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "quorum", "attestation_rejected"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "bad_signature")},
		)
		return nil, fmt.Errorf("failed to verify signature: %s", pk.String())
	}

	return cosmos.AccAddress(pk.Address()), nil
}
