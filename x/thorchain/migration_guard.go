package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
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
	iter := k.GetObservedTxOutVoterIterator(ctx)
	if iter == nil {
		return false, nil
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var voter ObservedTxVoter
		if err := k.Cdc().Unmarshal(iter.Value(), &voter); err != nil {
			return false, fmt.Errorf("fail to unmarshal observed tx out voter: %w", err)
		}
		if voter.Tx.IsEmpty() || voter.Height == 0 {
			continue
		}

		memo, err := ParseMemoWithTHORNames(ctx, k, voter.Tx.Tx.Memo)
		if err != nil || !memo.IsType(TxMigrate) {
			continue
		}

		if matchVault != nil {
			sourceVault, vaultErr := k.GetVault(ctx, voter.Tx.ObservedPubKey)
			if vaultErr != nil {
				return false, fmt.Errorf("fail to get source vault for migrate voter: %w", vaultErr)
			}
			if !matchVault(sourceVault) {
				continue
			}
		}

		inboundVoter, err := k.GetObservedTxInVoter(ctx, voter.TxID)
		if err != nil {
			return false, fmt.Errorf("fail to get observed tx in voter: %w", err)
		}
		if !inboundVoter.UpdatedVault {
			return true, nil
		}
	}

	return false, nil
}
