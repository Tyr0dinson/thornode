package utxo

import (
	bchchaincfg "github.com/gcash/bchd/chaincfg"
	"github.com/gcash/bchutil"

	"gitlab.com/thorchain/thornode/v3/common"
)

// normalizeCashAddress returns THORChain's canonical representation for
// CashAddr comparisons: BCH omits the prefix while XEC retains it.
func (c *Client) normalizeCashAddress(addr string) string {
	var (
		params     *bchchaincfg.Params
		withPrefix bool
	)

	switch c.cfg.ChainID {
	case common.BCHChain:
		params = c.getChainCfgBCH()
	case common.XECChain:
		params = c.getChainCfgXEC()
		withPrefix = true
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

	normalized := decoded.String()
	if withPrefix {
		return params.CashAddressPrefix + ":" + normalized
	}
	return normalized
}
