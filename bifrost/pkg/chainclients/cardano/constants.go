package cardano

// estimated tx size with
// 5 inputs, 2 outputs (1 base + 1 enterprise), 150 char memo
// 1.5 factor covers, more inputs or longer memos
// precise tx fee will be calculated on actual tx creation
const estimatedTxSize = 577

const txTimeoutBlocks = uint64(20)

const (
	networkFeeUpdatePeriod = 100
)
