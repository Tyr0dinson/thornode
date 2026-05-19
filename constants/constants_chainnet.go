//go:build chainnet
// +build chainnet

package constants

import (
	"os"
	"strconv"
)

func chainnetEnvInt64(name string, fallback int64) int64 {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func init() {
	int64Overrides = map[ConstantName]int64{
		DesiredValidatorSet:        chainnetEnvInt64("THOR_CHAINNET_DESIRED_VALIDATOR_SET", 6),
		AsgardSize:                 chainnetEnvInt64("THOR_CHAINNET_ASGARD_SIZE", 6),
		ChurnInterval:              10,
		ChurnRetryInterval:         chainnetEnvInt64("THOR_CHAINNET_CHURN_RETRY_INTERVAL", 10),
		ChurnMigrateRounds:         2,
		FundMigrationInterval:      20,
		JailTimeKeygen:             chainnetEnvInt64("THOR_CHAINNET_JAIL_TIME_KEYGEN", 11),
		JailTimeKeysign:            chainnetEnvInt64("THOR_CHAINNET_JAIL_TIME_KEYSIGN", 11),
		OperationalVotesMin:        1,
		MinRunePoolDepth:           1_00000000,
		MinimumBondInRune:          200_000_00000000,
		PoolCycle:                  720,
		EmissionCurve:              8,
		SigningTransactionPeriod:   chainnetEnvInt64("THOR_CHAINNET_SIGNING_TRANSACTION_PERIOD", 30),
		KeygenRetryInterval:        chainnetEnvInt64("THOR_CHAINNET_KEYGEN_RETRY_INTERVAL", 10),
		NumberOfNewNodesPerChurn:   chainnetEnvInt64("THOR_CHAINNET_NUMBER_OF_NEW_NODES_PER_CHURN", 5),
		P2PGateDisabled:            1,
		MintSynths:                 1,
		BurnSynths:                 1,
		MaxRuneSupply:              500_000_000_00000000,
		MultipleAffiliatesMaxCount: 5,
	}
	stringOverrides = map[ConstantName]string{
		DevFundAddress:       "cthor1jydyvc8wt84jcdl6qex0ps9czfmcaqurljevkt",
		MarketingFundAddress: "cthor15wayjcl0ecdnqzjx9a3f9zll6pzvphl6we8k6j",
		OverSolvencyAddress:  "cthor1jydyvc8wt84jcdl6qex0ps9czfmcaqurljevkt",
	}
}
