package common

import "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/firoutil"

func FIRONetwork() firoutil.Network {
	return firoNetwork(CurrentChainNetwork)
}

func firoNetwork(network ChainNetwork) firoutil.Network {
	switch network {
	case MainNet, StageNet, ChainNet:
		return firoutil.MainNet
	case MockNet:
		return firoutil.RegTest
	case TestNet:
		return firoutil.TestNet
	default:
		return ""
	}
}

func IsValidFIROAddress(address string) bool {
	for _, network := range []firoutil.Network{firoutil.MainNet, firoutil.TestNet, firoutil.RegTest} {
		_, err := firoutil.DecodeAddress(address, network)
		if err == nil {
			return true
		}
	}
	return false
}
