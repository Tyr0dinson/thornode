package thorchain

import (
	"fmt"
	"strconv"
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
		if unmarshalErr := k.Cdc().Unmarshal(iter.Value(), &source); unmarshalErr != nil {
			return false, fmt.Errorf("fail to unmarshal in-flight migrate source vault: %w", unmarshalErr)
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
			migration.TxOut = castInFlightMigrationTxOut(action)
			migration.DestinationVaultPubKey = findVaultByAddress(ctx, k, action.Chain, action.ToAddress)
		} else if !inVoter.Tx.IsEmpty() {
			migration.TxOut = castTxAsMigrationOut(inVoter.Tx, migration.SourceVaultPubKey)
			migration.DestinationVaultPubKey = findVaultByAddress(ctx, k, inVoter.Tx.Tx.Chain, inVoter.Tx.Tx.ToAddress)
		}

		migrations = append(migrations, migration)
	}

	return migrations, nil
}

func castInFlightMigrationTxOut(toi TxOutItem) *types.InFlightMigrationTxOut {
	aggregatorTargetLimit := ""
	if toi.AggregatorTargetLimit != nil {
		aggregatorTargetLimit = toi.AggregatorTargetLimit.String()
	}
	cloutSpent := ""
	if toi.CloutSpent != nil {
		cloutSpent = toi.CloutSpent.String()
	}

	return &types.InFlightMigrationTxOut{
		VaultPubKey:           toi.VaultPubKey.String(),
		VaultPubKeyEddsa:      toi.VaultPubKeyEddsa.String(),
		InHash:                toi.InHash.String(),
		OutHash:               toi.OutHash.String(),
		Chain:                 toi.Chain.String(),
		ToAddress:             toi.ToAddress.String(),
		Coin:                  castInFlightMigrationCoin(toi.Coin),
		MaxGas:                castInFlightMigrationGas(toi.MaxGas),
		GasRate:               strconv.FormatInt(toi.GasRate, 10),
		Memo:                  toi.Memo,
		OriginalMemo:          toi.OriginalMemo,
		Aggregator:            toi.Aggregator,
		AggregatorTargetAsset: toi.AggregatorTargetAsset,
		AggregatorTargetLimit: aggregatorTargetLimit,
		CloutSpent:            cloutSpent,
	}
}

func castTxAsMigrationOut(tx common.ObservedTx, sourceVaultPubKey string) *types.InFlightMigrationTxOut {
	coin := common.NoCoin
	if len(tx.Tx.Coins) > 0 {
		coin = tx.Tx.Coins[0]
	}

	return &types.InFlightMigrationTxOut{
		VaultPubKey: sourceVaultPubKey,
		InHash:      tx.Tx.ID.String(),
		OutHash:     tx.Tx.ID.String(),
		Chain:       tx.Tx.Chain.String(),
		ToAddress:   tx.Tx.ToAddress.String(),
		Coin:        castInFlightMigrationCoin(coin),
		MaxGas:      castInFlightMigrationGas(tx.Tx.Gas),
		Memo:        tx.Tx.Memo,
	}
}

func castInFlightMigrationCoin(coin common.Coin) *types.InFlightMigrationCoin {
	return &types.InFlightMigrationCoin{
		Asset:    coin.Asset.String(),
		Amount:   coin.Amount.String(),
		Decimals: strconv.FormatInt(coin.Decimals, 10),
	}
}

func castInFlightMigrationGas(gas common.Gas) []*types.InFlightMigrationCoin {
	ret := make([]*types.InFlightMigrationCoin, 0, len(gas))
	for _, coin := range gas {
		ret = append(ret, castInFlightMigrationCoin(coin))
	}
	return ret
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
