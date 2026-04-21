//go:build stagenet
// +build stagenet

package thorchain

import "gitlab.com/thorchain/thornode/v3/common"

// defaultChainContracts is called from DefaultGenesisState to seed the
// ChainContract keeper at genesis so the first Asgard vault created by
// NetworkManager picks these up in its Routers field. On stagenet this
// points every EVM chain at RouterV6.1 (CREATE2-deployed at the same
// address on ETH/AVAX/BSC/BASE).
//
// Values are sourced from *OldRouter in router_upgrade_info_stagenet.go
// — the "current active router" half of the upgrade pair — so there's
// a single source of truth per chain.
func defaultChainContracts() []ChainContract {
	return []ChainContract{
		{Chain: common.ETHChain, Router: common.Address(ethOldRouter)},
		{Chain: common.AVAXChain, Router: common.Address(avaxOldRouter)},
		{Chain: common.BSCChain, Router: common.Address(bscOldRouter)},
		{Chain: common.BASEChain, Router: common.Address(baseOldRouter)},
	}
}
