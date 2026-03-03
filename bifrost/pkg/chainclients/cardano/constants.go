package cardano

import "time"

const (
	// estimated tx size with
	// 5 inputs, 2 outputs (1 base + 1 enterprise), 150 char memo
	// 1.5 factor covers, more inputs or longer memos
	// precise tx fee will be calculated on actual tx creation
	estimatedTxSize = 581

	transactionTimeout = 20

	networkFeeUpdatePeriod = 100

	// minimum ADA per UTXO (~ 0.86 ADA on mainnet)
	dustThreshold = uint64(1e6)

	// amount of smallest utxos that should be consumed first
	numSmallUtxos = 3

	// maximum allowed transaction size in bytes
	// usually less than 500, maximum supported by cardano 16KB
	maxTransactionSize = 2048

	// extra bytes added to calculated fees to mitigate rounding issues
	extraFeeBuffer = 0

	maxRetryInterval = time.Minute
)
