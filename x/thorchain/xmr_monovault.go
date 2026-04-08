package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

func xmrMonovaultChains() common.Chains {
	return common.Chains{common.THORChain, common.XMRChain}
}

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

func hasDedicatedXMRMonovault(vaults Vaults) bool {
	for _, vault := range vaults {
		if vault.IsXMRMonovault() {
			return true
		}
	}
	return false
}

func shouldMaintainXMRMonovault(vaults Vaults, splitGroupCount int) bool {
	// TODO(XMR monovault): add an explicit vault-genesis/bootstrap path for the
	// first dedicated XMR monovault. This helper governs steady-state
	// maintenance, but the initial monovault still needs a deliberate kick-off
	// during genesis/upgrade so the dedicated family exists before churn logic
	// can preserve it.
	return splitGroupCount > 1 || hasDedicatedXMRMonovault(vaults)
}

func filterVaultsByChain(vaults Vaults, chain common.Chain) Vaults {
	filtered := make(Vaults, 0, len(vaults))
	for _, vault := range vaults {
		if vault.HasChain(chain) {
			filtered = append(filtered, vault)
		}
	}
	return filtered
}

func selectActiveVaultForChain(ctx cosmos.Context, k keeper.Keeper, active Vaults, chain common.Chain, signingTransactionPeriod int64) (Vault, error) {
	filtered := filterVaultsByChain(active, chain)
	if chain == common.THORChain {
		filtered = append(Vaults(nil), active...)
		regular := make(Vaults, 0, len(filtered))
		for _, vault := range filtered {
			if !vault.IsXMRMonovault() {
				regular = append(regular, vault)
			}
		}
		if len(regular) > 0 {
			filtered = regular
		}
	}
	if len(filtered) == 0 {
		return Vault{}, fmt.Errorf("no active vault for chain %s", chain)
	}
	return k.GetMostSecure(ctx, filtered, signingTransactionPeriod), nil
}
