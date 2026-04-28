package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func networkHasMigratingVaults(ctx cosmos.Context, k keeper.Keeper) (bool, error) {
	retiringVaults, err := k.GetAsgardVaultsByStatus(ctx, RetiringVault)
	if err != nil {
		return false, fmt.Errorf("fail to get retiring vaults: %w", err)
	}
	if len(retiringVaults) > 0 {
		return true, nil
	}

	return hasInFlightMigrate(ctx, k, nil)
}

func nodeHasMigratingVaults(ctx cosmos.Context, k keeper.Keeper, memberPubKey common.PubKey) (bool, error) {
	retiringVaults, err := k.GetAsgardVaultsByStatus(ctx, RetiringVault)
	if err != nil {
		return false, fmt.Errorf("fail to get retiring vaults: %w", err)
	}
	for _, vault := range retiringVaults {
		if vault.GetMembership().Contains(memberPubKey) {
			return true, nil
		}
	}

	return hasInFlightMigrate(ctx, k, func(v Vault) bool {
		return v.GetMembership().Contains(memberPubKey)
	})
}

func hasInFlightMigrate(ctx cosmos.Context, k keeper.Keeper, matchVault func(Vault) bool) (bool, error) {
	iter := k.GetInFlightMigrateIterator(ctx)
	if iter == nil {
		return false, nil
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var source types.ProtoString
		if err := k.Cdc().Unmarshal(iter.Value(), &source); err != nil {
			return false, fmt.Errorf("fail to unmarshal in-flight migrate source vault: %w", err)
		}
		if source.GetValue() == "" {
			continue
		}

		sourcePubKey, err := common.NewPubKey(source.GetValue())
		if err != nil {
			return false, fmt.Errorf("fail to parse in-flight migrate source vault pubkey: %w", err)
		}

		vault, err := k.GetVault(ctx, sourcePubKey)
		if err != nil {
			return false, fmt.Errorf("fail to get source vault for migrate guard: %w", err)
		}
		if vault.Status != RetiringVault {
			continue
		}

		if matchVault == nil {
			return true, nil
		}

		if matchVault(vault) {
			return true, nil
		}
	}

	return false, nil
}
