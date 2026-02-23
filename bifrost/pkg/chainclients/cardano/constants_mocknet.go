//go:build mocknet
// +build mocknet

package cardano

import "github.com/blinklabs-io/gouroboros/ledger/common"

const (
	networkId    = common.AddressNetworkTestnet
	networkMagic = 42
)
