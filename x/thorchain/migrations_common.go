package thorchain

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// Migrate4to5 migrates from version 4 to 5.
func (m Migrator) Migrate4to5(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// Update all KVStore network fees from (Mainnet-only) legacy 1e8 TransactionFeeRate
	// to gas rate units TransactionFeeRate.
	for _, chain := range common.AllChains {
		_, gasRateUnitsPerOne := chain.GetGasUnits()

		if gasRateUnitsPerOne.Equal(cosmos.NewUint(common.One)) {
			// This is already in the right units.
			continue
		}

		if chain.IsTHORChain() {
			// GetGasUnits doesn't have a THORChain entry,
			// so in case of unintended effects skip this.
			continue
		}

		networkFee, err := m.mgr.Keeper().GetNetworkFee(ctx, chain)
		if err != nil {
			return err
		}
		networkFee.TransactionFeeRate = cosmos.NewUint(networkFee.TransactionFeeRate).Mul(gasRateUnitsPerOne).QuoUint64(common.One).Uint64()
		if err := m.mgr.Keeper().SaveNetworkFee(ctx, chain, networkFee); err != nil {
			return err
		}
	}

	return nil
}
