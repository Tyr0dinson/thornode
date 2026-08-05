package utxo

import (
	bchchaincfg "github.com/gcash/bchd/chaincfg"
	"github.com/gcash/bchutil"

	"gitlab.com/thorchain/thornode/v3/common"
)

// normalizeCashAddress returns THORChain's canonical prefixless CashAddr for
// BCH and XEC comparisons.
func (c *Client) normalizeCashAddress(addr string) string {
	var params *bchchaincfg.Params

	switch c.cfg.ChainID {
	case common.BCHChain:
		params = c.getChainCfgBCH()
	case common.XECChain:
		params = c.getChainCfgXEC()
	default:
		return addr
	}

	decoded, err := bchutil.DecodeAddress(addr, params)
	if err != nil {
		return addr
	}

	switch decoded.(type) {
	case *bchutil.AddressPubKeyHash, *bchutil.AddressScriptHash:
	default:
		return addr
	}

	return decoded.String()
}
