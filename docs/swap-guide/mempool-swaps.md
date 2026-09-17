# Zero-confirmation (mempool) swaps

Eligible swaps can start executing before their inbound transaction reaches
finality. This includes transactions observed in a supported chain's mempool
and transactions included in a block but still waiting for enough confirmations.
The aim is to reduce swap execution time while withholding payouts until finality.

## How it works

1. Validators reach observation consensus on an eligible inbound. THORChain
   reserves capacity and queues the swap for early execution.
2. The swap can change pool balances, but its payouts and deferred accounting
   effects remain pending until the inbound reaches finality.
3. Finality allows settlement. If the inbound is invalidated or recovery requires
   cancellation, THORChain unwinds the executed portion and clears its pending
   work. Failed rollback attempts retain their state for retry.

`MempoolSwapCapTOR` limits total reserved value; `0` disables new mempool swaps.
`MempoolSwapPoolCapBPS` limits exposure per pool, and `MempoolSwapMaxCount` limits
the number of active lifecycle records. Swaps that cannot be admitted wait for
finality and follow normal processing.

## Accepted rollback behavior

Cross-asset rollback deliberately executes a reverse AMM swap and donates the
recovered source asset back to its pool. AMM pricing and rounding mean this does
not guarantee exact restoration of each pool's pre-swap RUNE balance. RUNE can
be redistributed between the affected pools.

**This price impact and RUNE redistribution are known and accepted consequences
of the design.** Exact per-pool RUNE restoration is not a requirement. Future
reviews should not report this documented behavior alone as a defect. Additional
economic consequences, incorrect supply accounting, duplicate execution, and
rollback failures remain valid concerns.

See the [rollback implementation](../../x/thorchain/handler_observed_tx_helpers.go)
and the [feature merge request](https://gitlab.com/thorchain/thornode/-/merge_requests/4342).
