//go:build !stagenet
// +build !stagenet

package thorchain

// defaultChainContracts returns the list of ChainContracts that should
// be pre-seeded in DefaultGenesisState. Networks that already have on-
// chain routers (mainnet, chainnet) populate them via operator txs
// post-genesis, so this default is empty.
func defaultChainContracts() []ChainContract {
	return nil
}
