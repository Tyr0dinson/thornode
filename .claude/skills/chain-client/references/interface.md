# Chain Client Interfaces

This document describes the chain client contract, the shared scanner contract, and the interfaces used by unified solvency reporting. Read the linked source files for the authoritative signatures.

## ChainClient Interface

**File**: `bifrost/pkg/chainclients/shared/types/types.go`

```go
type ChainClient interface {
    // Start starts the chain client with the given queues and shared reporter dispatcher.
    Start(
        globalTxsQueue chan types.TxIn,
        globalErrataQueue chan types.ErrataBlock,
        globalNetworkFeeQueue chan common.NetworkFee,
        reporterDispatcher reporter.Dispatcher,
    )

    // Stop stops the chain client.
    Stop()

    // IsBlockScannerHealthy returns true if the block scanner is healthy.
    IsBlockScannerHealthy() bool

    // SignTx returns the signed transaction, a checkpoint, and an instant observation.
    // The 3rd return value (*TxInItem) enables instant observations.
    SignTx(tx types.TxOutItem, height int64) (signedTx []byte, checkpoint []byte, observation *types.TxInItem, err error)

    // BroadcastTx broadcasts the transaction and returns the transaction hash.
    BroadcastTx(_ types.TxOutItem, _ []byte) (string, error)

    // GetHeight returns the current height of the chain.
    GetHeight() (int64, error)

    // GetAddress returns the address for the given public key.
    GetAddress(poolPubKey common.PubKey) string

    // GetAccount returns the account for the given public key.
    GetAccount(poolPubKey common.PubKey, height *big.Int) (common.Account, error)

    // GetVaultAccount returns the chain-specific account for the given vault.
    GetVaultAccount(vault ttypes.Vault, height *big.Int) (common.Account, error)

    // GetAccountByAddress returns the account for the given address.
    GetAccountByAddress(address string, height *big.Int) (common.Account, error)

    // GetChain returns the chain.
    GetChain() common.Chain

    // GetConfig returns the chain configuration.
    GetConfig() config.BifrostChainConfiguration

    // OnObservedTxIn is called when a new observed tx is received.
    OnObservedTxIn(txIn types.TxInItem, blockHeight int64)

    // GetConfirmationCount returns the confirmation count for the given tx.
    GetConfirmationCount(txIn types.TxIn) int64

    // ConfirmationCountReady returns true if the confirmation count is ready.
    ConfirmationCountReady(txIn types.TxIn) bool

    // GetBlockScannerHeight returns block scanner height for chain.
    GetBlockScannerHeight() (int64, error)

    // GetGasEstimateForSolvency returns the gas fee estimate used by solvency checks.
    GetGasEstimateForSolvency() cosmos.Uint

    // GetLatestTxForVault returns last observed and broadcasted tx for a vault.
    GetLatestTxForVault(vault string) (string, string, error)

    // RollbackBlockScanner rolls back the block scanner to re-observe recent blocks.
    RollbackBlockScanner() error
}
```

### Method Implementation Notes

| Method                      | Key Behavior                                                                                                                                                         |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Start`                     | Store queue channels, start TSS keysigner and scanner, pass the observer's reporter dispatcher to the scanner                                                        |
| `Stop`                      | Close stop channel, wait on WaitGroup, stop block scanner and TSS signer                                                                                             |
| `SignTx`                    | Validate tx, construct chain-specific tx, sign via TSS or local key, return signed bytes + observation                                                               |
| `BroadcastTx`               | Send signed tx to chain RPC, mark as signed in signer cache, return txid                                                                                             |
| `GetConfirmationCount`      | Return 0 for instant-finality chains, N for probabilistic chains                                                                                                     |
| `OnObservedTxIn`            | Record observation in signer cache for tracking                                                                                                                      |
| `GetLatestTxForVault`       | Query signer cache for latest observed/broadcast tx hashes                                                                                                           |
| `GetVaultAccount`           | Resolve the vault's chain-specific address/key and balances at the requested height using the client's query semantics; preserve native and supported token balances |
| `GetGasEstimateForSolvency` | Return the chain-specific fee estimate in the units expected by `bifrost/reporter/reporter_solvency.go`; retain existing fee initialization and reserve rules        |

## BlockScannerFetcher Interface

**File**: `bifrost/blockscanner/blockscanner.go`

```go
type BlockScannerFetcher interface {
    // FetchMemPool scans the mempool at the given height.
    FetchMemPool(height int64) (types.TxIn, error)

    // FetchTxs scans a block at fetchHeight (chainHeight is the latest chain height).
    FetchTxs(fetchHeight, chainHeight int64) (types.TxIn, error)

    // GetHeight returns current block height.
    GetHeight() (int64, error)

    // GetNetworkFee returns current network fee details.
    GetNetworkFee() (transactionSize, transactionFeeRate uint64)
}
```

This interface is implemented by the client or a separate scanner struct (e.g., `EVMScanner`, `CosmosBlockScanner`) passed to `blockscanner.NewBlockScanner()`. Solana uses its own `SOLScanner` lifecycle and wires the reporter dispatcher directly into that scanner.

## Unified Solvency Reporter

**File**: `bifrost/reporter/types.go`

The reporter package consumes a smaller `reporter.ChainClient` interface. All of its methods are also part of the full chain client contract:

```go
// In package reporter.
type ChainClient interface {
    GetHeight() (int64, error)
    GetBlockScannerHeight() (int64, error)
    GetGasEstimateForSolvency() cosmos.Uint
    GetAccount(poolPubKey common.PubKey, height *big.Int) (common.Account, error)
    GetVaultAccount(vault ttypes.Vault, height *big.Int) (common.Account, error)
    GetChain() common.Chain
    GetConfig() config.BifrostChainConfiguration
    IsBlockScannerHealthy() bool
}

type Dispatcher interface {
    Process(chain common.Chain, height int64)
}
```

Existing clients use a compile-time assertion such as `var _ reporter.ChainClient = (*Client)(nil)`. `ttypes.Vault` is the vault type from `x/thorchain/types/type_vault.go`; the full vault lets clients select the appropriate chain address, signing key, and balance reconciliation rules.

### Lifecycle and Reporting

1. The observer creates one dispatcher in `bifrost/observer/observe.go`, supplying the configured clients, a started-client check, and its solvency attester. It passes that dispatcher to each client's `Start()` method.
2. Clients using the shared block scanner forward it to `blockScanner.Start(globalTxsQueue, globalNetworkFeeQueue, reporterDispatcher)`. The scanner triggers `Process(chain, height)` after persisting block progress near the tip. Solana passes it through `solScanner.setReporterDispatcher` and triggers reporting from its own scanner.
3. `bifrost/reporter/dispatcher.go` serializes collection per chain. `bifrost/reporter/runner.go` also schedules periodic checks for started clients; runner reporting is gated by the active chain/solvency halt state before resolving the external height.
4. `bifrost/reporter/reporter_solvency.go` owns cadence and canonical report heights, queries `GetVaultAccount` for relevant Asgard vaults, and applies the chain's gas estimate and solvency rules. Preserve its Solana slot-boundary and TRON finality adjustments when extending chain support.
5. Collected reports go through the observer's `SolvencyAttester.AttestSolvency` path, preserving validator eligibility and supermajority attestation before submission to THORChain.

For a client using the shared scanner, the reporter wiring in `Start()` looks like:

```go
func (c *Client) Start(
    globalTxsQueue chan types.TxIn,
    globalErrataQueue chan types.ErrataBlock,
    globalNetworkFeeQueue chan common.NetworkFee,
    reporterDispatcher reporter.Dispatcher,
) {
    // Retain the client's TSS startup and errata wiring here.
    c.blockScanner.Start(globalTxsQueue, globalNetworkFeeQueue, reporterDispatcher)
}
```

Keep solvency scheduling and submission in the shared reporter. Its periodic runner can emit all-solvent reports for auto-unhalt only when the scanner is unhealthy and every relevant vault was successfully checked. Account failures or deferred balance reconciliation must not allow partial results to clear a halt.
