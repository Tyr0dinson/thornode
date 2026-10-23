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

## Rollback

While an inbound is unfinalized, its swap owns every pool it touched. Other
unfinalized swaps through those pools wait for it to finalize or roll back. A
swap whose inbound is already final, or an operation that permanently changes
the pool (adding or withdrawing liquidity, errata, gas accounting), takes the
pool back first: it rolls the owner back, or completes it if its inbound has
since confirmed. While a deferred pool cycle is being retried, even confirmed
swaps through an owned pool wait.

Each speculative leg records the exact balance change it made to each pool,
including the system fee taken from pool depth. Rollback subtracts exactly those
recorded changes. No reverse swap runs, no RUNE moves between pools, and AMM
pricing has no effect on the outcome. Changes the swap did not make, such as
block rewards paid to the pool while it was owned, stay in the pool.

An inbound that already reached finality may have released its pools, so other
swaps may have changed them since. In the rare cases where such an inbound is
rolled back (for example, an errata after finality), a cross-asset output is
unwound with a reverse AMM swap and the recovered source asset is donated back to
its pool. Price impact applies on this path.

See the [rollback implementation](../../x/thorchain/handler_observed_tx_helpers.go)
and the [feature merge request](https://gitlab.com/thorchain/thornode/-/merge_requests/4342).
