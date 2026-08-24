package keeperv1

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
	"unicode"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// CircuitBreakerRoutes returns the keeper's circuit breaker invariant routes.
func (k KVStore) CircuitBreakerRoutes() []common.CircuitBreakerRoute {
	return []common.CircuitBreakerRoute{
		common.NewCircuitBreakerRoute("AsgardSolvency", AsgardSolvencyCircuitBreaker(k)),
		common.NewCircuitBreakerRoute("BondSolvency", BondSolvencyCircuitBreaker(k)),
		common.NewCircuitBreakerRoute("VaultDeficit", VaultDeficitCircuitBreaker(k)),
	}
}

// foldKey maps every rune to the smallest rune in its unicode simple-fold
// orbit — the equivalence strings.EqualFold uses — so grouping by foldKey
// merges at least everything Asset.Equals merges, even for fold-orbit runes
// that ToUpper would keep distinct.
func foldKey(s string) string {
	return strings.Map(func(r rune) rune {
		folded := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < folded {
				folded = f
			}
		}
		return folded
	}, s)
}

// vaultAggregationOverflows reports whether summing any asset's duplicate
// coin or deficit entries would exceed the 256-bit Uint range, which would
// panic the checked arithmetic inside ValidateDeficitInvariant. Grouping by
// foldKey over Asset.String() is at least as coarse as Asset.Equals (which
// folds case and ignores nothing String() omits beyond Ticker), so every
// sum the shared helpers attempt is bounded by a guard group; over-grouping
// can at worst produce an alert-only indeterminate finding. The map is only
// used for lookup, never iterated.
func vaultAggregationOverflows(vault Vault) (string, bool) {
	for _, coins := range []common.Coins{vault.Coins, vault.Deficit} {
		totals := make(map[string]*big.Int)
		for _, coin := range coins {
			asset := foldKey(coin.Asset.String())
			total, ok := totals[asset]
			if !ok {
				total = new(big.Int)
				totals[asset] = total
			}
			total.Add(total, coin.Amount.BigInt())
			if total.BitLen() > 256 {
				return coin.Asset.String(), true
			}
		}
	}
	return "", false
}

// recordInsolvency compares actual against expected for one asset and tracks
// the worst underfunding on the check. Solvent and overfunded balances do not
// trigger the circuit breaker. Binary invariants whose expected balance is
// zero are handled explicitly by their route.
func recordInsolvency(result *common.CircuitBreakerCheck, worstBps *cosmos.Uint, asset string, actual, expected cosmos.Uint) {
	if expected.IsZero() || actual.GTE(expected) {
		return
	}
	bps := common.CalculateInsolvencyBps(actual, expected)
	result.Msgs = append(result.Msgs, fmt.Sprintf("insolvent: %s (expected %s, actual %s)", asset, expected, actual))
	if bps.GT(*worstBps) {
		*worstBps = bps
		result.Actual = actual
		result.Expected = expected
		result.Asset = asset
	}
}

// AsgardSolvencyCircuitBreaker checks the asgard module balance against expected
// pool and swap queue coins. It reports the worst underfunding; provable
// violations set Broken (halt-eligible), while unreadable or unevaluable
// state sets Indeterminate (reported, never halts).
func AsgardSolvencyCircuitBreaker(k KVStore) common.CircuitBreakerInvariant {
	return func(ctx cosmos.Context) common.CircuitBreakerCheck {
		result := common.CircuitBreakerCheck{
			Actual:   cosmos.ZeroUint(),
			Expected: cosmos.ZeroUint(),
		}

		// Malformed queue records are deleted by FetchQueue earlier in the
		// same EndBlock, before this route can observe them. Consume the
		// purge signal so the deletion is still surfaced on-chain. An
		// unreadable counter is itself indeterminate state and is preserved
		// rather than cleared.
		if purged, lastHeight, purgeErr := k.ConsumeMalformedSwapPurge(ctx); purgeErr != nil {
			result.Indeterminate = true
			result.Msgs = append(result.Msgs, fmt.Sprintf("fail to read malformed swap purge counter: %s", purgeErr))
		} else if purged > 0 {
			result.Indeterminate = true
			result.Msgs = append(result.Msgs, fmt.Sprintf("%d malformed swap queue records deleted, most recently at height %d", purged, lastHeight))
		}

		// Accumulate expected balances per native denom in arbitrary
		// precision — stored magnitudes may be corrupt, and checked Uint
		// arithmetic would panic EndBlock instead of producing a finding.
		// The map is used only for accumulation; results are materialized
		// in sorted denom order below, preserving consensus determinism.
		expectedTotals := make(map[string]*big.Int)
		denoms := make([]string, 0, 8)
		addExpected := func(denom string, amounts ...cosmos.Uint) {
			total, ok := expectedTotals[denom]
			if !ok {
				total = new(big.Int)
				expectedTotals[denom] = total
				// track denoms at insertion so the map is never iterated
				denoms = append(denoms, denom)
			}
			for _, amount := range amounts {
				total.Add(total, amount.BigInt())
			}
		}

		// sum all rune liquidity on pools, including pending
		pools, err := k.GetPools(ctx)
		if err != nil {
			// solvency cannot be determined — report without asserting a
			// violated invariant
			result.Indeterminate = true
			result.Msgs = append(result.Msgs, fmt.Sprintf("fail to get pools: %s", err))
			return result
		}
		for _, pool := range pools {
			switch {
			case pool.Asset.IsSyntheticAsset():
				addExpected(pool.Asset.Native(), pool.BalanceAsset)
			case !pool.Asset.IsDerivedAsset():
				addExpected(common.RuneAsset().Native(), pool.BalanceRune, pool.PendingInboundRune)

				if pool.Asset.IsTCY() {
					addExpected(common.TCY.Native(), pool.BalanceAsset, pool.PendingInboundAsset)
				}

				if pool.Asset.IsWhitelisted() {
					addExpected(pool.Asset.Native(), pool.BalanceAsset, pool.PendingInboundAsset)
				}
			}
		}

		processSwaps := func(ctx cosmos.Context, k KVStore, swapIter cosmos.Iterator) {
			defer swapIter.Close()

			for ; swapIter.Valid(); swapIter.Next() {
				var swap MsgSwap
				if unmarshalErr := k.Cdc().Unmarshal(swapIter.Value(), &swap); unmarshalErr != nil {
					// unreadable record — no invariant is evaluated, and the
					// system itself tolerates undecodable records, so this
					// is reported without being halt-eligible
					result.Indeterminate = true
					result.Msgs = append(result.Msgs, fmt.Sprintf("fail to unmarshal swap record: %s", unmarshalErr))
					continue
				}

				if len(swap.Tx.Coins) != 1 {
					// malformed queue state — expected balances are unknowable
					result.Broken = true
					result.Msgs = append(result.Msgs, fmt.Sprintf("wrong number of coins for swap: %d, %s", len(swap.Tx.Coins), swap.Tx.ID))
					continue
				}

				// L1-to-L1 swaps contribute nothing to module balances (the
				// accumulation below is native-gated), but their streaming
				// state is still validated — corrupt accounting is a proven
				// violation regardless of which balances it touches.
				coin := swap.Tx.Coins[0]

				// Legacy V1 swaps track progress in a separate StreamingSwap
				// record (NewMsgSwap embeds State for them too, but V1
				// execution never updates it). V2 swaps track progress in the
				// embedded State — except old-format V2 records, whose
				// embedded deposit is unset and whose progress lives in the
				// separate record, handled by the fallback below.
				swapIn := cosmos.ZeroUint()
				swapOut := cosmos.ZeroUint()
				deposit := cosmos.ZeroUint()
				hasStream := false
				stateLabel := ""
				if swap.IsLegacyStreaming() {
					ss, ssErr := k.GetStreamingSwap(ctx, swap.Tx.ID)
					if ssErr != nil {
						// unreadable stream state — expected balances are
						// unknowable, reported without a proven violation
						result.Indeterminate = true
						result.Msgs = append(result.Msgs, fmt.Sprintf("fail to get streaming swap %s: %s", swap.Tx.ID, ssErr))
						continue
					}
					// a missing record loads as zero deposit and zero in
					hasStream = !ss.Deposit.IsZero() || !ss.In.IsZero()
					swapIn, swapOut, deposit = ss.In, ss.Out, ss.Deposit
					stateLabel = "streaming state for swap"
				} else if swap.State != nil {
					hasStream = swap.IsStreaming()
					swapIn, swapOut, deposit = swap.State.In, swap.State.Out, swap.State.Deposit
					stateLabel = "swap state for"
					if hasStream && deposit.IsZero() && swapIn.IsZero() && swapOut.IsZero() {
						// Old-format V2 fallback, matching
						// calculateNetworkSolvency: without it a valid record
						// reads as an inconsistent stream (false Broken) and
						// its produced output is dropped from expected
						// balances. Gated on IsStreaming so a non-streaming
						// swap sharing a TxID with a V1 main swap cannot
						// borrow the wrong record, and on the embedded
						// progress being entirely zero — genuine old-format
						// records carry none, while a deposit-less record
						// with nonzero In or Out is contradictory and must
						// fall through to report Broken rather than be
						// misread as a not-yet-started stream.
						ss, ssErr := k.GetStreamingSwap(ctx, swap.Tx.ID)
						if ssErr != nil {
							// unreadable stream state — expected balances are
							// unknowable, reported without a proven violation
							result.Indeterminate = true
							result.Msgs = append(result.Msgs, fmt.Sprintf("fail to get streaming swap %s: %s", swap.Tx.ID, ssErr))
							continue
						}
						// a missing record loads as zero deposit and zero in
						hasStream = !ss.Deposit.IsZero() || !ss.In.IsZero()
						swapIn, swapOut, deposit = ss.In, ss.Out, ss.Deposit
						stateLabel = "streaming state for swap"
					}
				}

				// Validate streaming accounting for every swap before
				// branching on the source asset. On the native-input side a
				// corrupted queue coin or an In beyond bounds would
				// understate the subtraction; for external inputs with a
				// native target the record's Out is credited to expected
				// balances below; and for trade or secured targets nothing
				// is credited, but provably impossible state still fails
				// closed. A present stream must match the queued coin even
				// when nothing has been swapped in yet.
				if hasStream && !coin.Amount.Equal(deposit) {
					result.Broken = true
					result.Msgs = append(result.Msgs, fmt.Sprintf("inconsistent %s %s: coin=%s deposit=%s in=%s", stateLabel, swap.Tx.ID, coin.Amount, deposit, swapIn))
					continue
				}
				if !swapIn.IsZero() && (swapIn.GT(deposit) || swapIn.GT(coin.Amount)) {
					result.Broken = true
					result.Msgs = append(result.Msgs, fmt.Sprintf("inconsistent %s %s: coin=%s deposit=%s in=%s", stateLabel, swap.Tx.ID, coin.Amount, deposit, swapIn))
					continue
				}

				if coin.IsNative() && !coin.Asset.IsTradeAsset() && !coin.Asset.IsSecuredAsset() {
					if !swapIn.IsZero() {
						coin.Amount = coin.Amount.Sub(swapIn)
					}
					addExpected(coin.Asset.Native(), coin.Amount)
				}

				if swap.TargetAsset.IsNative() && !swap.TargetAsset.IsTradeAsset() && !swap.TargetAsset.IsSecuredAsset() {
					if !swapOut.IsZero() {
						addExpected(swap.TargetAsset.Native(), swapOut)
					}
				}
			}
		}

		processSwaps(ctx, k, k.GetSwapQueueIterator(ctx))
		processSwaps(ctx, k, k.GetAdvSwapQueueItemIterator(ctx))

		asgardAddr := k.GetModuleAccAddress(AsgardName)
		asgardCoins := k.GetBalance(ctx, asgardAddr)

		// Compare expected denoms in sorted order and track the worst
		// insolvency, saturating aggregated totals at the 256-bit ceiling.
		// Extra module balances are overfunding and do not trigger the
		// circuit breaker.
		sort.Strings(denoms)

		worstBps := cosmos.ZeroUint()
		for _, denom := range denoms {
			actual := cosmos.ZeroUint()
			for _, ac := range asgardCoins {
				if ac.Denom == denom {
					actual = cosmos.NewUintFromBigInt(ac.Amount.BigInt())
					break
				}
			}
			expected := common.SaturatingUintFromBig(expectedTotals[denom])
			recordInsolvency(&result, &worstBps, denom, actual, expected)
		}

		return result
	}
}

// VaultDeficitCircuitBreaker enforces the structural VaultDeficitInvariant.
// A normal unsettled deficit is valid state. The route triggers only when a
// vault simultaneously records settled coins and deficit for the same asset,
// or when a vault record cannot be decoded.
func VaultDeficitCircuitBreaker(k KVStore) common.CircuitBreakerInvariant {
	return func(ctx cosmos.Context) common.CircuitBreakerCheck {
		result := common.CircuitBreakerCheck{
			Actual:   cosmos.ZeroUint(),
			Expected: cosmos.ZeroUint(),
		}

		iterator := k.GetVaultIterator(ctx)
		defer iterator.Close()
		for ; iterator.Valid(); iterator.Next() {
			var vault Vault
			if err := k.Cdc().Unmarshal(iterator.Value(), &vault); err != nil {
				// unreadable record — reported without a proven violation
				result.Indeterminate = true
				result.Msgs = append(result.Msgs, fmt.Sprintf("fail to unmarshal vault: %s", err))
				continue
			}

			// ValidateDeficitInvariant aggregates duplicate same-asset
			// entries with checked arithmetic; guard the aggregation here so
			// corrupt magnitudes report a finding instead of panicking
			// EndBlock.
			if asset, overflows := vaultAggregationOverflows(vault); overflows {
				result.Indeterminate = true
				result.Msgs = append(result.Msgs, fmt.Sprintf("vault %s aggregation overflows for %s", vault.PubKey, asset))
				continue
			}

			if err := vault.ValidateDeficitInvariant(); err == nil {
				continue
			} else {
				result.Broken = true
				result.Msgs = append(result.Msgs, fmt.Sprintf("vault_deficit: %s", err))
			}

			// Populate structured fields from the first offending asset. The
			// Broken flag, rather than a synthetic ratio, drives the finding.
			if result.Asset != "" {
				continue
			}
			for _, coin := range vault.Coins.NoneEmpty() {
				deficit := vault.GetDeficit(coin.Asset)
				if deficit.Amount.IsZero() {
					continue
				}
				result.Asset = coin.Asset.String()
				result.Actual = coin.Amount
				// The sum of corrupt magnitudes can exceed 256 bits — the
				// very state this route reports — so saturate rather than
				// panic EndBlock.
				result.Expected = common.SaturatingAddUint(coin.Amount, deficit.Amount)
				break
			}
		}

		return result
	}
}

// BondSolvencyCircuitBreaker checks the bond module balance against expected
// bonded rune and pending rewards.
func BondSolvencyCircuitBreaker(k KVStore) common.CircuitBreakerInvariant {
	return func(ctx cosmos.Context) common.CircuitBreakerCheck {
		result := common.CircuitBreakerCheck{
			Actual:   cosmos.ZeroUint(),
			Expected: cosmos.ZeroUint(),
		}

		bondedRune := cosmos.ZeroUint()
		naIter := k.GetNodeAccountIterator(ctx)
		defer naIter.Close()
		for ; naIter.Valid(); naIter.Next() {
			var na NodeAccount
			if err := k.Cdc().Unmarshal(naIter.Value(), &na); err != nil {
				// unreadable record — reported without a proven violation
				result.Indeterminate = true
				result.Msgs = append(result.Msgs, fmt.Sprintf("fail to unmarshal node account: %s", err))
				continue
			}
			// saturating: a corrupt bond magnitude must produce a finding,
			// not an EndBlock panic
			bondedRune = common.SaturatingAddUint(bondedRune, na.Bond)
		}

		network, err := k.GetNetwork(ctx)
		if err != nil {
			// pending bond rewards unknown — report without asserting a
			// violated invariant
			result.Indeterminate = true
			result.Msgs = append(result.Msgs, fmt.Sprintf("fail to get network: %s", err))
			return result
		}
		bondRewardRune := network.BondRewardRune

		bondModuleRune := k.GetBalanceOfModule(ctx, BondName, common.RuneAsset().Native())

		expectedRune := common.SaturatingAddUint(bondedRune, bondRewardRune)
		worstBps := cosmos.ZeroUint()
		recordInsolvency(&result, &worstBps, common.RuneAsset().Native(), bondModuleRune, expectedRune)

		return result
	}
}
