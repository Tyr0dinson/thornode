package utxo

import (
	"fmt"
	"math"

	"github.com/btcsuite/btcutil"
	bchchaincfg "github.com/gcash/bchd/chaincfg"

	"gitlab.com/thorchain/thornode/v3/common"
)

func (c *Client) getChainCfgXEC() *bchchaincfg.Params {
	params := common.GetXECAddressParams(common.CurrentChainNetwork)
	if params == nil {
		c.log.Fatal().Msg("unsupported network")
	}
	return params
}

func rpcAmountToSatsForChain(chain common.Chain, value float64) (int64, error) {
	if chain == common.XECChain {
		return int64(math.Round(value * 100)), nil
	}

	amt, err := btcutil.NewAmount(value)
	if err != nil {
		return 0, err
	}
	return int64(amt), nil
}

func (c *Client) rpcAmountToSats(value float64) (int64, error) {
	return rpcAmountToSatsForChain(c.cfg.ChainID, value)
}

func (c *Client) satsToRPCAmount(sats int64) float64 {
	if c.cfg.ChainID == common.XECChain {
		return float64(sats) / 100
	}
	return btcutil.Amount(sats).ToBTC()
}

func (c *Client) rpcAmountToSatsU64(value float64) (uint64, error) {
	amt, err := c.rpcAmountToSats(value)
	if err != nil {
		return 0, err
	}
	if amt < 0 {
		return 0, fmt.Errorf("negative RPC amount: %f", value)
	}
	return uint64(amt), nil
}
