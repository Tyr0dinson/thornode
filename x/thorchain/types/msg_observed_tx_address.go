package types

import "gitlab.com/thorchain/thornode/v3/common"

func observedTxAddressMatchesPubKey(chain common.Chain, pubKey common.PubKey, address common.Address) bool {
	obAddr, err := pubKey.GetAddress(chain)
	if err == nil && address.Equals(obAddr) {
		return true
	}
	if chain.Equals(common.BTCChain) {
		taprootAddr, err := pubKey.GetTaprootAddress(chain)
		if err == nil && address.Equals(taprootAddr) {
			return true
		}
	}
	return false
}
