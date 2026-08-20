package common

import bchchaincfg "github.com/gcash/bchd/chaincfg"

// CashAddrPrefixes returns the address prefixes accepted for a CashAddr chain
// on the given network. These are separate from AddressPrefix: IsValidAddress
// treats that value as part of the address text, while CashAddr addresses used
// in memos are prefixless.
func CashAddrPrefixes(chain Chain, network ChainNetwork) []string {
	switch {
	case chain.Equals(BCHChain):
		switch network {
		case TestNet:
			return []string{bchchaincfg.TestNet3Params.CashAddressPrefix}
		case MockNet:
			return []string{
				bchchaincfg.TestNet3Params.CashAddressPrefix,
				bchchaincfg.RegressionNetParams.CashAddressPrefix,
			}
		case MainNet, StageNet, ChainNet:
			return []string{bchchaincfg.MainNetParams.CashAddressPrefix}
		}
	case chain.Equals(XECChain):
		if params := GetXECAddressParams(network); params != nil {
			return []string{params.CashAddressPrefix}
		}
	}

	return nil
}
