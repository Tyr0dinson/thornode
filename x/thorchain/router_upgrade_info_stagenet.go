//go:build stagenet
// +build stagenet

package thorchain

// RouterV6.1 stagenet deployment (CREATE2, same address on ETH/AVAX/BSC/BASE):
//
//	https://etherscan.io/address/0x0DC6108C9225Ce93Da589B4CE83c104b34693117
//	https://snowtrace.io/address/0x0DC6108C9225Ce93Da589B4CE83c104b34693117
//	https://bscscan.com/address/0x0DC6108C9225Ce93Da589B4CE83c104b34693117
//	https://basescan.org/address/0x0DC6108C9225Ce93Da589B4CE83c104b34693117
//
// *OldRouter holds the router currently active on our vault (seeded at
// genesis via DefaultGenesisState().ChainContracts — see
// default_chain_contracts_stagenet.go). *NewRouter is the upgrade target
// consumed by RouterUpgradeController when MIMIR_UPGRADE_CONTRACT_<CHAIN>
// is set. With no upgrade planned, newRouter is intentionally empty — if
// the mimir is mistakenly set, common.NewAddress("") errors and the
// controller short-circuits.
var (
	ethOldRouter = `0x0DC6108C9225Ce93Da589B4CE83c104b34693117`
	ethNewRouter = ``

	avaxOldRouter = `0x0DC6108C9225Ce93Da589B4CE83c104b34693117`
	avaxNewRouter = ``

	bscOldRouter = `0x0DC6108C9225Ce93Da589B4CE83c104b34693117`
	bscNewRouter = ``

	baseOldRouter = `0x0DC6108C9225Ce93Da589B4CE83c104b34693117`
	baseNewRouter = ``
)
