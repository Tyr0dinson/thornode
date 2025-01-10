package ton

import (
	"cosmossdk.io/math"
	"math/big"
)

func toThor(n *big.Int) math.Uint {
	return math.NewUintFromBigInt(n).QuoUint64(10)
}

func fromThor(n math.Uint) *big.Int {
	return n.MulUint64(10).BigInt()
}
