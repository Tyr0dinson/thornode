package common

import (
	"math/big"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// borrowed and modified from cosmos types
// https://github.com/cosmos/cosmos-sdk/blob/v0.45.1/types/invariant.go
// https://github.com/cosmos/cosmos-sdk/blob/v0.45.1/x/crisis/types/route.go
//
// differences are:
// - array of messages for readability purposes
// - no module in routes, we use a single thorchain module

// An Invariant is a function which tests a particular invariant.
// The invariant returns a descriptive message about what happened
// and a boolean indicating whether the invariant has been broken.
type Invariant func(ctx cosmos.Context) (msg []string, broken bool)

// invariant route
type InvariantRoute struct {
	Route     string
	Invariant Invariant
}

// NewInvariantRoute - create an InvariantRoute object
func NewInvariantRoute(route string, invariant Invariant) InvariantRoute {
	return InvariantRoute{
		Route:     route,
		Invariant: invariant,
	}
}

// CircuitBreakerCheck is the result of a circuit breaker invariant check.
// It returns true actual vs expected values for the worst-case divergence.
type CircuitBreakerCheck struct {
	Actual   cosmos.Uint
	Expected cosmos.Uint
	Asset    string
	Msgs     []string
	// Broken marks a provably violated invariant: a condition a defined
	// invariant recognizes as impossible (inconsistent streaming accounting,
	// malformed queue records, vault coins coexisting with deficit). Broken
	// bypasses tolerance and is halt-eligible when auto-halt is armed.
	Broken bool
	// Indeterminate marks state the check could not read or evaluate:
	// decode failures, store read errors, conversion failures. No invariant
	// is proven violated — the check simply could not run to completion.
	// Indeterminate bypasses tolerance so the finding is always reported,
	// but it never halts: the system itself tolerates unreadable records,
	// and the breaker must not be stricter than the system it protects.
	Indeterminate bool
}

// CircuitBreakerInvariant is a function that checks solvency and returns
// actual vs expected values rather than a simple broken bool.
type CircuitBreakerInvariant func(ctx cosmos.Context) CircuitBreakerCheck

// CalculateInsolvencyBps returns underfunding in basis points of expected,
// rounded up. Solvent and overfunded balances return zero. Rounding up makes
// the strict tolerance comparison exact for integer tolerances — for any
// integer T, ceil(x) > T if and only if x > T — so a sub-BPS shortfall
// registers as one basis point instead of flooring to zero and escaping a
// zero tolerance, and fractional excess above a whole-BPS threshold is not
// forgiven. The intermediate calculation uses big.Int so valid 256-bit Uint
// values cannot overflow before division.
func CalculateInsolvencyBps(actual, expected cosmos.Uint) cosmos.Uint {
	if expected.IsZero() || actual.GTE(expected) {
		return cosmos.ZeroUint()
	}

	deficit := new(big.Int).Sub(expected.BigInt(), actual.BigInt())
	numerator := new(big.Int).Mul(deficit, big.NewInt(10000))
	quotient, remainder := new(big.Int).QuoRem(numerator, expected.BigInt(), new(big.Int))
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return cosmos.NewUintFromBigInt(quotient)
}

// maxUint256 is the largest value representable by cosmos.Uint.
var maxUint256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// SaturatingAddUint returns a+b, saturating at the maximum 256-bit value
// instead of panicking on overflow. Circuit breaker routes report corrupt
// state whose magnitudes may already be beyond arithmetic bounds; reported
// values must saturate rather than halt block production.
func SaturatingAddUint(a, b cosmos.Uint) cosmos.Uint {
	sum := new(big.Int).Add(a.BigInt(), b.BigInt())
	if sum.Cmp(maxUint256) > 0 {
		sum.Set(maxUint256)
	}
	return cosmos.NewUintFromBigInt(sum)
}

// CircuitBreakerRoute pairs a halt-worthy route name with its invariant.
type CircuitBreakerRoute struct {
	Route     string
	Invariant CircuitBreakerInvariant
}

// NewCircuitBreakerRoute creates a new CircuitBreakerRoute.
func NewCircuitBreakerRoute(route string, invariant CircuitBreakerInvariant) CircuitBreakerRoute {
	return CircuitBreakerRoute{
		Route:     route,
		Invariant: invariant,
	}
}
