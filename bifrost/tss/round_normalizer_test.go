package tss

import (
	"testing"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestCanonicalTssRoundSecp256k1(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"thorchain.tsslib.ecdsa.keygen.KGRound1Message":     tssMessages.KEYGEN1,
		"thorchain.tsslib.ecdsa.keygen.KGRound2Message1":    tssMessages.KEYGEN2aUnicast,
		"thorchain.tsslib.ecdsa.signing.SignRound1Message1": tssMessages.KEYSIGN1aUnicast,
		"thorchain.tsslib.ecdsa.signing.SignRound2Message":  tssMessages.KEYSIGN2Unicast,
		tssMessages.KEYSIGN7:                                tssMessages.KEYSIGN7,
		"thorchain.tsslib.ecdsa.signing.SignRound7Message":  tssMessages.KEYSIGN7,
		"unrelated-round":                                   "unrelated-round",
	}

	for in, want := range tests {
		if got := canonicalTssRound(in, common.SigningAlgoSecp256k1); got != want {
			t.Errorf("canonicalTssRound(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalTssRoundEd25519(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"thorchain.tsslib.eddsa.keygen.KGRound1Message":    tssMessages.EDDSAKEYGEN1,
		"thorchain.tsslib.eddsa.keygen.KGRound2Message2":   tssMessages.EDDSAKEYGEN2b,
		"thorchain.tsslib.eddsa.signing.SignRound1Message": tssMessages.EDDSAKEYSIGN1,
		"thorchain.tsslib.eddsa.signing.SignRound3Message": tssMessages.EDDSAKEYSIGN3,
		tssMessages.EDDSAKEYSIGN2:                          tssMessages.EDDSAKEYSIGN2,
		"unrelated-round":                                  "unrelated-round",
	}

	for in, want := range tests {
		if got := canonicalTssRound(in, common.SigningAlgoEd25519); got != want {
			t.Errorf("canonicalTssRound(%q) = %q, want %q", in, got, want)
		}
	}
}
