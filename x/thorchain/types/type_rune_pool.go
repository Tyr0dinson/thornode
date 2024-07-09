package types

import (
	"gitlab.com/thorchain/thornode/common/cosmos"
)

func NewRUNEPool() RUNEPool {
	return RUNEPool{
		ReserveUnits: cosmos.ZeroUint(),
		PoolUnits:    cosmos.ZeroUint(),
	}
}

func (rp RUNEPool) TotalUnits() cosmos.Uint {
	return rp.ReserveUnits.Add(rp.PoolUnits)
}
