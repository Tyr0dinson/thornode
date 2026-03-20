package tss

import (
	"strings"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/common"
)

func canonicalTssRound(round string, algo common.SigningAlgo) string {
	if round == "" {
		return ""
	}

	switch algo {
	case common.SigningAlgoSecp256k1:
		switch {
		case strings.HasSuffix(round, "KGRound1Message"):
			return tssMessages.KEYGEN1
		case strings.HasSuffix(round, "KGRound2Message1"):
			return tssMessages.KEYGEN2aUnicast
		case strings.HasSuffix(round, "KGRound2Message2"):
			return tssMessages.KEYGEN2b
		case strings.HasSuffix(round, "KGRound3Message"):
			return tssMessages.KEYGEN3
		case strings.HasSuffix(round, "SignRound1Message1"):
			return tssMessages.KEYSIGN1aUnicast
		case strings.HasSuffix(round, "SignRound1Message2"):
			return tssMessages.KEYSIGN1b
		case strings.HasSuffix(round, "SignRound2Message"):
			return tssMessages.KEYSIGN2Unicast
		case strings.HasSuffix(round, "SignRound3Message"):
			return tssMessages.KEYSIGN3
		case strings.HasSuffix(round, "SignRound4Message"):
			return tssMessages.KEYSIGN4
		case strings.HasSuffix(round, "SignRound5Message"):
			return tssMessages.KEYSIGN5
		case strings.HasSuffix(round, "SignRound6Message"):
			return tssMessages.KEYSIGN6
		case strings.HasSuffix(round, "SignRound7Message"):
			return tssMessages.KEYSIGN7
		}
	case common.SigningAlgoEd25519:
		switch {
		case strings.HasSuffix(round, "KGRound1Message"):
			return tssMessages.EDDSAKEYGEN1
		case strings.HasSuffix(round, "KGRound2Message1"):
			return tssMessages.EDDSAKEYGEN2a
		case strings.HasSuffix(round, "KGRound2Message2"):
			return tssMessages.EDDSAKEYGEN2b
		case strings.HasSuffix(round, "SignRound1Message"):
			return tssMessages.EDDSAKEYSIGN1
		case strings.HasSuffix(round, "SignRound2Message"):
			return tssMessages.EDDSAKEYSIGN2
		case strings.HasSuffix(round, "SignRound3Message"):
			return tssMessages.EDDSAKEYSIGN3
		}
	}

	return round
}
