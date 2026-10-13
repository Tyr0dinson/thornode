package types

import (
	"testing"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/common"
)

// FROST ceremonies already use stable round names; they must pass through the
// normalizer unchanged under the taproot algo tag, and the keysign rounds must
// remain valid blame rounds.
func TestCanonicalTssRoundFrostUnderTaprootAlgo(t *testing.T) {
	for _, round := range []string{
		tssMessages.FROSTKEYSIGNPREPROCESS,
		tssMessages.FROSTKEYSIGNSHARE,
		tssMessages.FROSTKEYSIGNRESULT,
		"frost_dkg_round1",
		"frost_dkg_round2",
		"frost_dkg_round3",
		"frost_dkg_result",
	} {
		if got := CanonicalTssRound(round, common.SigningAlgoSecp256k1Taproot); got != round {
			t.Fatalf("CanonicalTssRound(%s) = %s", round, got)
		}
	}
	for _, round := range []string{tssMessages.FROSTKEYSIGNPREPROCESS, tssMessages.FROSTKEYSIGNSHARE, tssMessages.FROSTKEYSIGNRESULT} {
		if got, err := NormalizeTssKeysignRound(round); err != nil || got != round {
			t.Fatalf("NormalizeTssKeysignRound(%s) = %s, %v", round, got, err)
		}
	}
}
