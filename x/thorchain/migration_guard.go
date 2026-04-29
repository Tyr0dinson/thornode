package thorchain

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
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
		txIDString := strings.TrimPrefix(string(iter.Key()), "in_flight_migrate/")
		txIDString = strings.TrimPrefix(txIDString, "/")
		txID, err := common.NewTxID(txIDString)
		if err != nil {
			return false, fmt.Errorf("fail to parse in-flight migrate tx id: %w", err)
		}
		inVoter, err := k.GetObservedTxInVoter(ctx, txID)
		if err != nil {
			return false, fmt.Errorf("fail to get observed tx in voter for migrate guard: %w", err)
		}
		if inVoter.UpdatedVault {
			continue
		}

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
		// In-flight migrate guards must remain active after a source vault flips
		// inactive, until the matching inbound migrate credit clears the index.
		// Only active vaults are stale guards from already-settled migrations.
		if vault.Status == ActiveVault {
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

func getInFlightMigrations(ctx cosmos.Context, k keeper.Keeper) ([]*types.InFlightMigration, error) {
	migrations := make([]*types.InFlightMigration, 0)
	iter := k.GetInFlightMigrateIterator(ctx)
	if iter == nil {
		return migrations, nil
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		txIDString := strings.TrimPrefix(string(iter.Key()), "in_flight_migrate/")
		txIDString = strings.TrimPrefix(txIDString, "/")
		migration := &types.InFlightMigration{TxId: txIDString}

		var source types.ProtoString
		if err := k.Cdc().Unmarshal(iter.Value(), &source); err != nil {
			return nil, fmt.Errorf("fail to unmarshal in-flight migrate source vault: %w", err)
		}
		migration.SourceVaultPubKey = source.GetValue()

		if source.GetValue() != "" {
			sourcePubKey, err := common.NewPubKey(source.GetValue())
			if err != nil {
				return nil, fmt.Errorf("fail to parse in-flight migrate source vault pubkey: %w", err)
			}

			vault, err := k.GetVault(ctx, sourcePubKey)
			if err != nil {
				return nil, fmt.Errorf("fail to get source vault for migrate guard: %w", err)
			}
			migration.SourceVaultStatus = vault.Status.String()
		}

		txID, err := common.NewTxID(txIDString)
		if err != nil {
			return nil, fmt.Errorf("fail to parse in-flight migrate tx id: %w", err)
		}
		inVoter, err := k.GetObservedTxInVoter(ctx, txID)
		if err != nil {
			return nil, fmt.Errorf("fail to get observed tx in voter for in-flight migrate: %w", err)
		}
		migration.UpdatedVault = inVoter.UpdatedVault

		voter, err := k.GetObservedTxOutVoter(ctx, txID)
		if err != nil {
			return nil, fmt.Errorf("fail to get observed tx out voter for in-flight migrate: %w", err)
		}
		if len(voter.Actions) > 0 {
			action := voter.Actions[0]
			for _, candidate := range voter.Actions {
				memo, err := mem.ParseMemo(k.GetVersion(), candidate.Memo)
				if err == nil && memo.IsType(mem.TxMigrate) {
					action = candidate
					break
				}
			}
			migration.TxOut = castTxOutItem(action, voter.Height)
			migration.DestinationVaultPubKey = findVaultByAddress(ctx, k, action.Chain, action.ToAddress)
		} else if !inVoter.Tx.IsEmpty() {
			migration.TxOut = castTxAsMigrationOut(inVoter.Tx, migration.SourceVaultPubKey, inVoter.Height)
			migration.DestinationVaultPubKey = findVaultByAddress(ctx, k, inVoter.Tx.Tx.Chain, inVoter.Tx.Tx.ToAddress)
		}

		migrations = append(migrations, migration)
	}

	return migrations, nil
}

func castTxAsMigrationOut(tx common.ObservedTx, sourceVaultPubKey string, height int64) *types.QueryTxOutItem {
	coin := common.NoCoin
	if len(tx.Tx.Coins) > 0 {
		coin = tx.Tx.Coins[0]
	}

	return &types.QueryTxOutItem{
		Height:      height,
		VaultPubKey: sourceVaultPubKey,
		InHash:      tx.Tx.ID.String(),
		OutHash:     tx.Tx.ID.String(),
		Chain:       tx.Tx.Chain.String(),
		ToAddress:   tx.Tx.ToAddress.String(),
		Coin:        &coin,
		MaxGas:      tx.Tx.Gas,
		Memo:        tx.Tx.Memo,
	}
}

func findVaultByAddress(ctx cosmos.Context, k keeper.Keeper, chain common.Chain, address common.Address) string {
	if chain.IsEmpty() || address.IsEmpty() {
		return ""
	}

	for _, status := range []VaultStatus{ActiveVault, RetiringVault, InactiveVault} {
		vaults, err := k.GetAsgardVaultsByStatus(ctx, status)
		if err != nil {
			ctx.Logger().Error("fail to get vaults while resolving migration destination", "status", status, "error", err)
			continue
		}
		for _, vault := range vaults {
			vaultAddress, err := vault.GetAddress(chain)
			if err != nil {
				continue
			}
			if vaultAddress.Equals(address) {
				return vault.PubKey.String()
			}
		}
	}

	return ""
}
