package keeperv1

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// GetVaultData retrieve vault data from key value store
func (k KVStore) GetVaultData(ctx cosmos.Context) (VaultData, error) {
	record := NewVaultData()
	_, err := k.get(ctx, k.GetKey(ctx, prefixVaultData, ""), &record)
	return record, err
}

// SetVaultData save the given vault data to key value store, it will overwrite existing vault
func (k KVStore) SetVaultData(ctx cosmos.Context, data VaultData) error {
	k.set(ctx, k.GetKey(ctx, prefixVaultData, ""), data)
	return nil
}

// AddFeeToReserve add fee to reserve, the fee is always in RUNE
func (k KVStore) AddFeeToReserve(ctx cosmos.Context, fee cosmos.Uint) error {
	vault, err := k.GetVaultData(ctx)
	if err != nil {
		return fmt.Errorf("fail to get vault: %w", err)
	}
	if common.RuneAsset().Chain.Equals(common.THORChain) {
		coin := common.NewCoin(common.RuneNative, fee)
		sdkErr := k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coin)
		if sdkErr != nil {
			return dbError(ctx, "fail to send fee to reserve", sdkErr)
		}
	} else {
		vault.TotalReserve = vault.TotalReserve.Add(fee)
	}
	return k.SetVaultData(ctx, vault)
}
