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
	// Broken marks conditions where a tolerance comparison is meaningless and
	// the circuit breaker must trigger: binary checks, state corruption, and
	// indeterminate results.
	Broken bool
}

// CircuitBreakerInvariant is a function that checks solvency and returns
// actual vs expected values rather than a simple broken bool.
type CircuitBreakerInvariant func(ctx cosmos.Context) CircuitBreakerCheck

// CalculateInsolvencyBps returns underfunding in basis points of expected.
// Solvent and overfunded balances return zero. The intermediate calculation
// uses big.Int so valid 256-bit Uint values cannot overflow before division.
func CalculateInsolvencyBps(actual, expected cosmos.Uint) cosmos.Uint {
	if expected.IsZero() || actual.GTE(expected) {
		return cosmos.ZeroUint()
	}

	deficit := new(big.Int).Sub(expected.BigInt(), actual.BigInt())
	numerator := new(big.Int).Mul(deficit, big.NewInt(10000))
	return cosmos.NewUintFromBigInt(numerator.Quo(numerator, expected.BigInt()))
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
