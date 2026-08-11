package utxo

import "gitlab.com/thorchain/thornode/v3/common/cosmos"

// capConfirmations limits a confirmation count when a positive maximum is set.
func capConfirmations(confirm cosmos.Uint, maximum int64) cosmos.Uint {
	if maximum <= 0 {
		return confirm
	}

	limit := cosmos.NewUint(uint64(maximum))
	if confirm.GT(limit) {
		return limit
	}

	return confirm
}
