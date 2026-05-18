package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func splitAsgardGroupCount(validatorCount int, asgardSize int64) int {
	if validatorCount <= 0 || asgardSize <= 0 {
		return 0
	}
	groupCount := validatorCount / int(asgardSize)
	if validatorCount%int(asgardSize) > 0 {
		groupCount++
	}
	return groupCount
}

func filterVaultsByChain(vaults Vaults, chain common.Chain) Vaults {
	filtered := make(Vaults, 0, len(vaults))
	for _, vault := range vaults {
		if vaultCanSignChain(vault, chain) {
			filtered = append(filtered, vault)
		}
	}
	return filtered
}

func vaultCanSignChain(vault Vault, chain common.Chain) bool {
	if !vault.HasChain(chain) {
		return false
	}
	if _, err := vault.GetAddress(chain); err != nil {
		return false
	}
	return true
}

func preferredActiveVaultsForChain(vaults Vaults, chain common.Chain) Vaults {
	return filterVaultsByChain(vaults, chain)
}

func filterActiveVaultsForOutboundChain(vaults Vaults, chain common.Chain) Vaults {
	filtered := preferredActiveVaultsForChain(vaults, chain)
	if chain.Equals(common.XMRChain) || len(filtered) > 0 {
		return filtered
	}
	return vaults
}

func keygenBlockHasType(keygenBlock KeygenBlock, keygenType types.KeygenType) bool {
	for _, keygen := range keygenBlock.Keygens {
		if keygen.Type == keygenType {
			return true
		}
	}
	return false
}

func keygenMatchesVault(keygen Keygen, vault Vault) bool {
	if !vault.MembershipEquals(keygen.GetMembers()) {
		return false
	}
	return keygen.Type == AsgardKeygen
}

func keygenTypeReady(keygenBlock KeygenBlock, readyVaults Vaults, keygenType types.KeygenType) bool {
	seen := false
	for _, keygen := range keygenBlock.Keygens {
		if keygen.Type != keygenType {
			continue
		}
		seen = true
		matched := false
		for _, vault := range readyVaults {
			if keygenMatchesVault(keygen, vault) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return seen
}

func filterVaultsByKeygenType(vaults Vaults, keygenBlock KeygenBlock, keygenType types.KeygenType) Vaults {
	filtered := make(Vaults, 0, len(vaults))
	for _, vault := range vaults {
		for _, keygen := range keygenBlock.Keygens {
			if keygen.Type != keygenType {
				continue
			}
			if keygenMatchesVault(keygen, vault) {
				filtered = append(filtered, vault)
				break
			}
		}
	}
	return filtered
}

func selectActiveVaultForChain(ctx cosmos.Context, k keeper.Keeper, active Vaults, chain common.Chain, signingTransactionPeriod int64) (Vault, error) {
	if chain.Equals(common.THORChain) {
		return k.GetMostSecure(ctx, active, signingTransactionPeriod), nil
	}
	filtered := preferredActiveVaultsForChain(active, chain)
	if len(filtered) == 0 {
		return Vault{}, fmt.Errorf("no active vault for chain %s", chain)
	}
	return k.GetMostSecure(ctx, filtered, signingTransactionPeriod), nil
}
