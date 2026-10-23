package types

import (
	"fmt"
	"math/big"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// LUVI (Liquidity Unit Value Index) measures pool value per liquidity unit with
// price removed: sqrt(A*R) / units. Classic LUVI breaks once a pool has synths,
// because synths are a liability the LPs owe that is repriced by every swap.
//
// This file implements the synth-aware family of indices:
//
//	Q      = sqrt(A*R) - S*sqrt(p)/2 = sqrt(A*R) * (2A - S) / (2A)   (signed LP equity)
//	LUVI*  = Q/L + C,  dC = ((S_prev + S_new) / 2) * (sqrt(p_new) - sqrt(p_prev)) / (2 * L_new)
//	dG     = dQ + ((S_prev + S_new) / 4) * (sqrt(p_new) - sqrt(p_prev)) - dL * LUVI_chain_prev
//
// where p = R/A and LUVI_chain = sqrt(A*R)/PoolUnits is the classic index as
// the chain computes it (synth-unit clamp included). G is valid in every
// regime, including overhang (S >= 2A); LUVI* is only meaningful below it.
// Below overhang dG == L_new * dLUVI*; with no synths dG == L_new * dLUVI.
//
// Everything is computed with big.Int fixed-point arithmetic so results are
// deterministic. The functions here are read-only: nothing is stored on chain.

// LuviPrecision is the number of decimal places used by the internal
// fixed-point representation. Values exported as cosmos.Dec are truncated to
// that type's 18 decimal places.
const LuviPrecision = 36

var (
	luviScale   = new(big.Int).Exp(big.NewInt(10), big.NewInt(LuviPrecision), nil)
	luviScaleSq = new(big.Int).Mul(luviScale, luviScale)
	luviDecDiv  = new(big.Int).Exp(big.NewInt(10), big.NewInt(LuviPrecision-18), nil)
)

// LuviState is a snapshot of the pool quantities the LUVI indices depend on.
// PoolUnits must be the on-chain value (LPUnits + SynthUnits after CalcUnits),
// clamp included.
type LuviState struct {
	BalanceRune  cosmos.Uint
	BalanceAsset cosmos.Uint
	LPUnits      cosmos.Uint
	SynthSupply  cosmos.Uint
	PoolUnits    cosmos.Uint
}

// NewLuviState builds a snapshot from a pool and its synth supply. The pool is
// copied, so the caller's SynthUnits are left untouched.
func NewLuviState(pool Pool, synthSupply cosmos.Uint) LuviState {
	p := pool
	p.CalcUnits(synthSupply)
	return LuviState{
		BalanceRune:  pool.BalanceRune,
		BalanceAsset: pool.BalanceAsset,
		LPUnits:      pool.LPUnits,
		SynthSupply:  synthSupply,
		PoolUnits:    p.GetPoolUnits(),
	}
}

// IsValid reports whether every index is defined for this state: both depths
// and the LP units must be non-zero.
func (s LuviState) IsValid() bool {
	return !s.BalanceRune.IsZero() && !s.BalanceAsset.IsZero() && !s.LPUnits.IsZero() && !s.PoolUnits.IsZero()
}

// IsOverhang reports whether synths are at or above twice the asset depth,
// where on-chain synth units are clamped and LP equity is non-positive.
func (s LuviState) IsOverhang() bool {
	return s.SynthSupply.GTE(s.BalanceAsset.MulUint64(2))
}

// SynthPerPoolDepthBps returns S / (2A) in basis points (0 when A is zero).
func (s LuviState) SynthPerPoolDepthBps() cosmos.Uint {
	if s.BalanceAsset.IsZero() {
		return cosmos.ZeroUint()
	}
	return s.SynthSupply.MulUint64(10_000).Quo(s.BalanceAsset.MulUint64(2))
}

// sqrtAR returns floor(sqrt(A*R) * 10^LuviPrecision).
func (s LuviState) sqrtAR() *big.Int {
	x := new(big.Int).Mul(s.BalanceAsset.BigInt(), s.BalanceRune.BigInt())
	x.Mul(x, luviScaleSq)
	return x.Sqrt(x)
}

// sqrtPrice returns sqrt(R/A) scaled by 10^LuviPrecision (0 when A is zero).
func (s LuviState) sqrtPrice() *big.Int {
	if s.BalanceAsset.IsZero() {
		return new(big.Int)
	}
	return new(big.Int).Quo(s.sqrtAR(), s.BalanceAsset.BigInt())
}

// lpEquity returns the signed LP equity Q = sqrt(A*R)*(2A - S)/(2A), scaled.
func (s LuviState) lpEquity() *big.Int {
	if s.BalanceAsset.IsZero() {
		return new(big.Int)
	}
	twoA := new(big.Int).Lsh(s.BalanceAsset.BigInt(), 1)
	num := new(big.Int).Sub(twoA, s.SynthSupply.BigInt())
	num.Mul(num, s.sqrtAR())
	return num.Quo(num, twoA)
}

// chainLuvi returns the classic on-chain index sqrt(A*R)/PoolUnits, scaled.
func (s LuviState) chainLuvi() *big.Int {
	if s.PoolUnits.IsZero() {
		return new(big.Int)
	}
	return new(big.Int).Quo(s.sqrtAR(), s.PoolUnits.BigInt())
}

// lpEquityPerUnit returns Q/L, scaled (0 when L is zero).
func (s LuviState) lpEquityPerUnit() *big.Int {
	if s.LPUnits.IsZero() {
		return new(big.Int)
	}
	return new(big.Int).Quo(s.lpEquity(), s.LPUnits.BigInt())
}

// SqrtAR returns sqrt(A*R) as a Dec.
func (s LuviState) SqrtAR() cosmos.Dec { return luviToDec(s.sqrtAR()) }

// LPEquity returns the signed LP equity Q. It is negative in overhang.
func (s LuviState) LPEquity() cosmos.Dec { return luviToDec(s.lpEquity()) }

// LPEquityPerUnit returns Q/L. Below overhang this equals ChainLuvi (up to
// on-chain rounding of synth units); in overhang it is negative.
func (s LuviState) LPEquityPerUnit() cosmos.Dec { return luviToDec(s.lpEquityPerUnit()) }

// ChainLuvi returns the classic index sqrt(A*R)/PoolUnits as the chain values
// a pool unit, clamp included.
func (s LuviState) ChainLuvi() cosmos.Dec { return luviToDec(s.chainLuvi()) }

func luviToDec(x *big.Int) cosmos.Dec {
	return sdkmath.LegacyNewDecFromBigIntWithPrec(new(big.Int).Quo(x, luviDecDiv), 18)
}

// LuviStep describes the change between two consecutive observations.
type LuviStep struct {
	// DeltaG is the change in hedged LP-side equity, in sqrt(asset*RUNE) units.
	DeltaG cosmos.Dec
	// DeltaC is the change in the per-unit synth repricing term of LUVI*.
	DeltaC cosmos.Dec
	// NormalizedDeltaG is DeltaG / sqrt(A*R) of the previous state. Alert on this.
	NormalizedDeltaG cosmos.Dec
	// LuviStar is LUVI* after the step (only meaningful below overhang).
	LuviStar cosmos.Dec
	// Overhang reports whether either end of the step is in overhang.
	Overhang bool
}

// LuviTracker accumulates C and G across consecutive observations of a pool.
// Observe must be called after every change to the pool's depths, synth supply
// or LP units (or at least once per block, and every block in which the synth
// supply or LP units change), in order.
type LuviTracker struct {
	prev LuviState
	c    *big.Int
	g    *big.Int
}

// NewLuviTracker starts tracking from an initial state.
func NewLuviTracker(initial LuviState) (*LuviTracker, error) {
	if !initial.IsValid() {
		return nil, fmt.Errorf("luvi: invalid initial state")
	}
	return &LuviTracker{prev: initial, c: new(big.Int), g: new(big.Int)}, nil
}

// Observe records the next state and returns the step since the previous one.
func (t *LuviTracker) Observe(next LuviState) (LuviStep, error) {
	if !next.IsValid() {
		return LuviStep{}, fmt.Errorf("luvi: invalid state")
	}
	prev := t.prev
	sumS := new(big.Int).Add(prev.SynthSupply.BigInt(), next.SynthSupply.BigInt())
	dSqrtP := new(big.Int).Sub(next.sqrtPrice(), prev.sqrtPrice())
	reprice := new(big.Int).Mul(sumS, dSqrtP) // (S_prev + S_new) * dsqrt(p), scaled

	// dC = reprice / (4 * L_new)
	dC := new(big.Int).Quo(reprice, new(big.Int).Lsh(next.LPUnits.BigInt(), 2))

	// dG = dQ + reprice/4 - dL * LUVI_chain(prev)
	dG := new(big.Int).Sub(next.lpEquity(), prev.lpEquity())
	dG.Add(dG, new(big.Int).Quo(reprice, big.NewInt(4)))
	// dL * sqrt(AR)_prev / P_prev, multiplied before dividing: in overhang the
	// pool units can reach ~1e38, which would truncate LUVI_chain to few digits.
	dLv := new(big.Int).Sub(next.LPUnits.BigInt(), prev.LPUnits.BigInt())
	dLv.Mul(dLv, prev.sqrtAR())
	dG.Sub(dG, dLv.Quo(dLv, prev.PoolUnits.BigInt()))

	t.c.Add(t.c, dC)
	t.g.Add(t.g, dG)
	t.prev = next

	norm := new(big.Int).Mul(dG, luviScale)
	norm.Quo(norm, prev.sqrtAR())

	star := new(big.Int).Add(next.lpEquityPerUnit(), t.c)
	return LuviStep{
		DeltaG:           luviToDec(dG),
		DeltaC:           luviToDec(dC),
		NormalizedDeltaG: luviToDec(norm),
		LuviStar:         luviToDec(star),
		Overhang:         prev.IsOverhang() || next.IsOverhang(),
	}, nil
}

// C returns the accumulated synth repricing term of LUVI*.
func (t *LuviTracker) C() cosmos.Dec { return luviToDec(t.c) }

// G returns the accumulated raw hedged equity change. Its unit is worth a
// different amount of RUNE as price and pool size change, so alert on each
// step's NormalizedDeltaG rather than reading G as a return.
func (t *LuviTracker) G() cosmos.Dec { return luviToDec(t.g) }

// LuviStar returns LUVI* = Q/L + C for the most recent state.
func (t *LuviTracker) LuviStar() cosmos.Dec {
	return luviToDec(new(big.Int).Add(t.prev.lpEquityPerUnit(), t.c))
}

// CheckLPUnitLedger verifies that the change in LP units between two
// observations equals the units reported by that block's add-liquidity and
// withdraw events. G cannot see units over-minted while a pool is in overhang
// (they are nearly worthless until it leaves overhang), so run this alongside.
func CheckLPUnitLedger(prevUnits, nextUnits cosmos.Uint, added, withdrawn []cosmos.Uint) error {
	expected := new(big.Int).Set(prevUnits.BigInt())
	for _, u := range added {
		expected.Add(expected, u.BigInt())
	}
	for _, u := range withdrawn {
		expected.Sub(expected, u.BigInt())
	}
	if expected.Cmp(nextUnits.BigInt()) != 0 {
		return fmt.Errorf("luvi: LP units %s do not match events (expected %s)", nextUnits, expected)
	}
	return nil
}
