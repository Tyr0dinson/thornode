package common

import bchchaincfg "github.com/gcash/bchd/chaincfg"

// GetXECAddressParams returns BCH chain params with the XEC cashaddr prefix
// mapped for the current THORChain network.
func GetXECAddressParams(network ChainNetwork) *bchchaincfg.Params {
	switch network {
	case TestNet:
		params := bchchaincfg.TestNet3Params
		params.CashAddressPrefix = "ectest"
		return &params
	case MockNet:
		params := bchchaincfg.RegressionNetParams
		params.CashAddressPrefix = "ecregtest"
		return &params
	case MainNet, StageNet, ChainNet:
		params := bchchaincfg.MainNetParams
		params.CashAddressPrefix = "ecash"
		return &params
	default:
		return nil
	}
}
