package utxo

import (
	"fmt"

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
	return c.toThorCoin(baseUnits), nil
}

// toThorCoin converts UTXO base units to THORChain's 1e8 representation. It
// preserves legacy zero decimal metadata for 8-decimal UTXO chains while
// retaining explicit non-default precision such as XEC's 2 decimals.
func (c *Client) toThorCoin(baseUnits cosmos.Uint) common.Coin {
	coin := c.cfg.ChainID.NativeGasToThorCoin(baseUnits)
	if coin.Decimals == cosmos.DefaultCoinDecimals {
		coin.Decimals = 0
	}
	return coin
}

// dustBaseUnits returns the configured chain's economic dust threshold in
// native base units, matching UTXO and wire-transaction amounts.
func (c *Client) dustBaseUnits() cosmos.Uint {
	return c.cfg.ChainID.ThorchainToNativeGasFloor(c.cfg.ChainID.DustThreshold())
}

func toUint64(amount cosmos.Uint) (uint64, error) {
	value, err := cosmos.UintToUint64(amount)
	if err != nil {
		return 0, fmt.Errorf("amount: %w", err)
	}
	return value, nil
}
