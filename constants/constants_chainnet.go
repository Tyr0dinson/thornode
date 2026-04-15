//go:build chainnet
// +build chainnet

package constants

func init() {
	int64Overrides = map[ConstantName]int64{
		DesiredValidatorSet:        6,
		ChurnInterval:              80,
		ChurnRetryInterval:         10,
		FundMigrationInterval:      30,
		ChurnMigrateRounds:         2,
		SigningTransactionPeriod:    40,
		JailTimeKeygen:             10,
		JailTimeKeysign:            10,
		MinimumBondInRune:          1,
		MinimumNodesForBFT:         2,
		P2PGateDisabled:            1,
		EmissionCurve:              1_000_000_000_000_000_000,
		MaxOutboundAttempts:        1000,
		MaxRuneSupply:              500_000_000_00000000,
		OperationalVotesMin:        1,
		BankSendEnabled:            1,
		MultipleAffiliatesMaxCount: 5,
	}
	stringOverrides = map[ConstantName]string{
		DevFundAddress:       "cthor1jydyvc8wt84jcdl6qex0ps9czfmcaqurljevkt",
		MarketingFundAddress: "cthor15wayjcl0ecdnqzjx9a3f9zll6pzvphl6we8k6j",
		OverSolvencyAddress:  "cthor1jydyvc8wt84jcdl6qex0ps9czfmcaqurljevkt",
	}
}
