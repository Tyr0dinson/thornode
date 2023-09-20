//go:build !testnet && !stagenet && !mocknet
// +build !testnet,!stagenet,!mocknet

package thorchain

import (
	re "regexp"
	"strings"

	"github.com/blang/semver"
)

var adminMimirDenyList = []*re.Regexp{
	re.MustCompile("(?i)EmissionCurve"),
	re.MustCompile("(?i)IncentiveCurve"),
	re.MustCompile("(?i)BlocksPerYear"),
	re.MustCompile("(?i)MinimumBondInRune"),
	re.MustCompile("(?i)NumberOfNewNodesPerChurn"),
	re.MustCompile("(?i)AsgardSize"),
	re.MustCompile("(?i)FullImpLossProtectionBlocks"),
	re.MustCompile("(?i)DesiredValidatorSet"),
	re.MustCompile("(?i)MinimumNodesForBFT"),
	re.MustCompile("(?i)ObserveSlashPoints"),
	re.MustCompile("(?i)FailKeysignSlashPoints"),
	re.MustCompile("(?i)FailKeygenSlashPoints"),
	re.MustCompile("(?i)StagedPoolCost"),
	re.MustCompile("(?i)Ragnarok.*"),
	re.MustCompile("(?i)MinTxOutVolumeThreshold"),
	re.MustCompile("(?i)MaxTxOutOffset"),
	re.MustCompile("(?i)TxOutDelayRate"),
	re.MustCompile("(?i)TxOutDelayMax"),
	re.MustCompile("(?i)MaxRuneSupply"),
}

func satisfiesTorAnchorWhitelist(version semver.Version, key string) bool {
	switch {
	case version.GTE(semver.MustParse("1.122.0")):
		return satisfiesTorAnchorWhitelistV122(key)
	default:
		return true
	}
}

func satisfiesTorAnchorWhitelistV122(key string) bool {
	if !strings.HasPrefix(strings.ToUpper(key), "TORANCHOR") {
		return true
	}

	torAnchorWhitelist := []string{
		"TorAnchor-AVAX-USDC-0XB97EF9EF8734C71904D8002F8B6BC66DD9C48A6E",
		"TorAnchor-BNB-BUSD-BD1",
		"TorAnchor-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48",
		"TorAnchor-ETH-USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7",
	}

	for _, whitelistKey := range torAnchorWhitelist {
		if strings.EqualFold(key, whitelistKey) {
			return true
		}
	}
	return false
}
