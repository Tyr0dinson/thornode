package common

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

const (
	maxPrecisionAfterZeros = 4
	maxIntegerLength       = 18
)

func NewOraclePrice(number *big.Float) (*OraclePrice, error) {
	if number == nil {
		return nil, fmt.Errorf("number is nil")
	}

	parts := strings.Split(number.Text('f', -1), ".")

	if len(parts) > 2 {
		return nil, fmt.Errorf("too many parts")
	}

	// doesn't fit into uint64
	if len(parts[0]) > maxIntegerLength {
		return nil, fmt.Errorf("number too big")
	}

	integer := parts[0]
	decimals := 0
	remain := maxIntegerLength - len(parts[0])

	if len(parts) == 2 {
		zeros := 0
		index := 0

		for i := 0; i < remain && i < len(parts[1]); i++ {
			index++

			// don't count zeros, if there has been a different number already
			// eg.: 1.0020304 -> 1.00203
			if parts[1][i] == '0' && zeros == i {
				zeros++
			} else if index >= zeros+maxPrecisionAfterZeros {
				break
			}
		}

		// only add fraction part, if there are not only zeros
		if index > zeros {
			// remove trailing zeros
			fraction := strings.TrimRight(parts[1][:index], "0")
			integer += fraction
			decimals = len(fraction)
		}
	}

	amount, err := strconv.ParseUint(integer, 10, 64)
	if err != nil {
		return nil, err
	}

	return &OraclePrice{
		Amount:   amount,
		Decimals: uint32(decimals),
	}, nil
}

func (p *OraclePrice) BigFloat() *big.Float {
	decimals := big.NewInt(int64(p.Decimals))
	divisor := new(big.Int).Exp(big.NewInt(10), decimals, nil)
	amount := new(big.Float).SetUint64(p.Amount)

	return new(big.Float).Quo(amount, new(big.Float).SetInt(divisor))
}

// OraclePriceRatioOutput returns floor(amount * source USD price / target USD
// price). It uses exact rational arithmetic and rejects invalid prices.
func OraclePriceRatioOutput(amount cosmos.Uint, srcPrice, tgtPrice string) (cosmos.Uint, bool) {
	// Bound input length before parsing prices.
	if len(srcPrice) > 64 || len(tgtPrice) > 64 {
		return cosmos.ZeroUint(), false
	}
	src, ok := new(big.Rat).SetString(srcPrice) // nolint:gosec // G113 mitigated by length bound above
	if !ok || src.Sign() <= 0 {
		return cosmos.ZeroUint(), false
	}
	tgt, ok := new(big.Rat).SetString(tgtPrice) // nolint:gosec // G113 mitigated by length bound above
	if !ok || tgt.Sign() <= 0 {
		return cosmos.ZeroUint(), false
	}
	out := new(big.Rat).SetInt(amount.BigInt())
	out.Mul(out, src)
	out.Quo(out, tgt)
	floored := new(big.Int).Quo(out.Num(), out.Denom())
	return cosmos.NewUintFromBigInt(floored), true
}
