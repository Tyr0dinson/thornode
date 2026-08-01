package rpc

import (
	"fmt"
	"math"
	"math/big"
	"strconv"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// converter describes the decimal whole-coin amount format used by a
// specific RPC client. This is an RPC wire-format concern, independent of
// THORChain's internal 1e8 representation.
type converter struct {
	baseUnitsPerCoin cosmos.Uint
}

func newConverter(chain common.Chain) converter {
	return converter{
		baseUnitsPerCoin: cosmos.PowerOfTen(uint64(chain.GetGasAssetDecimal())),
	}
}

func (c converter) toBaseUnits(rpcAmount float64) (cosmos.Uint, error) {
	if math.IsNaN(rpcAmount) || math.IsInf(rpcAmount, 0) {
		return cosmos.Uint{}, fmt.Errorf("non-finite RPC amount: %v", rpcAmount)
	}
	if rpcAmount < 0 {
		return cosmos.Uint{}, fmt.Errorf("negative RPC amount: %v", rpcAmount)
	}

	value, ok := new(big.Rat).SetString(strconv.FormatFloat(rpcAmount, 'f', -1, 64))
	if !ok {
		return cosmos.Uint{}, fmt.Errorf("invalid RPC amount: %v", rpcAmount)
	}
	value.Mul(value, new(big.Rat).SetInt(c.baseUnitsPerCoin.BigInt()))
	if !value.IsInt() {
		return cosmos.Uint{}, fmt.Errorf("RPC amount %v is not representable in base units", rpcAmount)
	}

	baseUnits := value.Num()
	if baseUnits.BitLen() > 256 {
		return cosmos.Uint{}, fmt.Errorf("RPC amount %v overflows base units", rpcAmount)
	}
	return cosmos.NewUintFromBigInt(baseUnits), nil
}

func (c converter) fromBaseUnits(baseUnits cosmos.Uint) (float64, error) {
	if baseUnits.IsNil() {
		return 0, fmt.Errorf("base-unit amount is nil")
	}
	value := new(big.Rat).SetFrac(baseUnits.BigInt(), c.baseUnitsPerCoin.BigInt())
	rpcAmount, _ := value.Float64()
	return rpcAmount, nil
}
