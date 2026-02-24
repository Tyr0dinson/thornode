package cardano

// estimated tx size with
// 5 inputs, 2 outputs (1 base + 1 enterprise), 150 char memo
// 1.5 factor covers, more inputs or longer memos
// precise tx fee will be calculated on actual tx creation
const estimatedTxSize = 577

const transactionTimeout = 20

const networkFeeUpdatePeriod = 100

// minimum ADA per UTXO (~ 0.86 ADA on mainnet)
const dustThreshold = uint64(1e6)
