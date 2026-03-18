package cardano

import "time"

const (
	// estimated tx size with
	// 5 inputs, 2 outputs (1 base + 1 enterprise), 150 char memo
	// 1.5 factor covers, more inputs or longer memos
	// precise tx fee will be calculated on actual tx creation
	estimatedTxSize = 581

	// timeout in blocks, multiplied by the active-slots coefficient
	// to compute TTL in slots.
	transactionTimeout = 20

	networkFeeUpdatePeriod = 100

	// Cardano enforces a minimum ADA per UTXO output (size-dependent,
	// ~1 ADA for ADA-only outputs). Change below this is absorbed
	// into the fee.
	dustThreshold = uint64(1e6)

	// amount of smallest utxos that should be consumed first
	numSmallUtxos = 3

	// maximum allowed transaction size in bytes
	// usually less than 500, maximum supported by cardano 16KB
	maxTransactionSize = 2048

	// extra bytes added to calculated fees to mitigate rounding issues
	extraFeeBytes = 0

	maxRetryInterval = time.Minute
)
