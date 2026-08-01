package utxo

import (
	"fmt"
	"math"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// rpcAmountToThorCoin is a scanner/account boundary: the RPC client first
// decodes its wire amount to chain base units, then common normalizes them to
// THORChain's 1e8 representation.
func (c *Client) rpcAmountToThorCoin(rpcAmount float64) (common.Coin, error) {
	baseUnits, err := c.rpc.ToBaseUnits(rpcAmount)
	if err != nil {
		return common.Coin{}, err
	}
	return c.cfg.ChainID.NativeGasToThorCoin(baseUnits), nil
}

func toUint64(amount cosmos.Uint) (uint64, error) {
	if amount.IsNil() {
		return 0, fmt.Errorf("amount is nil")
	}
	if amount.GT(cosmos.NewUint(math.MaxUint64)) {
		return 0, fmt.Errorf("amount does not fit uint64: %s", amount.String())
	}
	return amount.Uint64(), nil
}

func toInt64(amount cosmos.Uint) (int64, error) {
	if amount.IsNil() {
		return 0, fmt.Errorf("amount is nil")
	}
	if amount.GT(cosmos.NewUint(math.MaxInt64)) {
		return 0, fmt.Errorf("amount does not fit int64: %s", amount.String())
	}
	return int64(amount.Uint64()), nil
}
