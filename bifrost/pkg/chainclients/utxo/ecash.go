package utxo

import (
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
