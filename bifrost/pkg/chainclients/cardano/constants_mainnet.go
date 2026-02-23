//go:build !mocknet
// +build !mocknet

package cardano

import "github.com/blinklabs-io/gouroboros/ledger/common"

const (
	networkId    = common.AddressNetworkMainnet
	networkMagic = 764824073
)
