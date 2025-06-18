// querier_vault.go contains all vault-related query handlers.
// This includes queries for individual vaults, Asgard vaults, and vault pubkeys.

package thorchain

import (
	"errors"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ============ Vault Queries ============

func (qs queryServer) queryVault(ctx cosmos.Context, req *types.QueryVaultRequest) (*types.QueryVaultResponse, error) {
	if len(req.PubKey) < 1 {
		return nil, errors.New("missing vault pub_key parameter")
	}
	pubkey, err := common.NewPubKey(req.PubKey)
	if err != nil {
		return nil, fmt.Errorf("%s is invalid pubkey", req.PubKey)
	}
	v, err := qs.mgr.Keeper().GetVault(ctx, pubkey)
	if err != nil {
		return nil, fmt.Errorf("fail to get vault with pubkey(%s),err:%w", pubkey, err)
	}
	if v.IsEmpty() {
		return nil, errors.New("vault not found")
	}

	resp := types.QueryVaultResponse{
		BlockHeight:           v.BlockHeight,
		PubKey:                v.PubKey.String(),
		Coins:                 v.Coins,
		Type:                  v.Type.String(),
		Status:                v.Status.String(),
		StatusSince:           v.StatusSince,
		Membership:            v.Membership,
		Chains:                v.Chains,
		InboundTxCount:        v.InboundTxCount,
		OutboundTxCount:       v.OutboundTxCount,
		PendingTxBlockHeights: v.PendingTxBlockHeights,
		Routers:               castVaultRouters(v.Routers),
		Addresses:             getVaultChainAddresses(ctx, v),
		Frozen:                v.Frozen,
	}
	return &resp, nil
}

func (qs queryServer) queryAsgardVaults(ctx cosmos.Context, _ *types.QueryAsgardVaultsRequest) (*types.QueryAsgardVaultsResponse, error) {
	vaults, err := qs.mgr.Keeper().GetAsgardVaults(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get asgard vaults: %w", err)
	}

	var vaultsWithFunds []*types.QueryVaultResponse
	for _, vault := range vaults {
		if vault.Status == InactiveVault {
			continue
		}
		if !vault.IsAsgard() {
			continue
		}
		// Being in a RetiringVault blocks a node from unbonding, so display them even if having no funds.
		if vault.HasFunds() || vault.Status == ActiveVault || vault.Status == RetiringVault {
			vaultsWithFunds = append(vaultsWithFunds, &types.QueryVaultResponse{
				BlockHeight:           vault.BlockHeight,
				PubKey:                vault.PubKey.String(),
				Coins:                 vault.Coins,
				Type:                  vault.Type.String(),
				Status:                vault.Status.String(),
				StatusSince:           vault.StatusSince,
				Membership:            vault.Membership,
				Chains:                vault.Chains,
				InboundTxCount:        vault.InboundTxCount,
				OutboundTxCount:       vault.OutboundTxCount,
				PendingTxBlockHeights: vault.PendingTxBlockHeights,
				Routers:               castVaultRouters(vault.Routers),
				Frozen:                vault.Frozen,
				Addresses:             getVaultChainAddresses(ctx, vault),
			})
		}
	}

	return &types.QueryAsgardVaultsResponse{AsgardVaults: vaultsWithFunds}, nil
}

func (qs queryServer) queryVaultsPubkeys(ctx cosmos.Context, _ *types.QueryVaultsPubkeysRequest) (*types.QueryVaultsPubkeysResponse, error) {
	var resp types.QueryVaultsPubkeysResponse
	resp.Asgard = make([]*types.VaultInfo, 0)
	resp.Inactive = make([]*types.VaultInfo, 0)
	iter := qs.mgr.Keeper().GetVaultIterator(ctx)

	active, err := qs.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return nil, err
	}
	cutOffAge := ctx.BlockHeight() - config.GetThornode().VaultPubkeysCutoffBlocks
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var vault Vault
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &vault); err != nil {
			ctx.Logger().Error("fail to unmarshal vault", "error", err)
			return nil, fmt.Errorf("fail to unmarshal vault: %w", err)
		}
		if vault.IsAsgard() {
			switch vault.Status {
			case ActiveVault, RetiringVault:
				resp.Asgard = append(resp.Asgard, &types.VaultInfo{
					PubKey:  vault.PubKey.String(),
					Routers: castVaultRouters(vault.Routers),
				})
			case InactiveVault:
				// skip inactive vaults that have never received an inbound
				if vault.InboundTxCount == 0 {
					continue
				}

				// skip inactive vaults older than the cutoff age
				if vault.BlockHeight < cutOffAge {
					continue
				}

				activeMembers, err := vault.GetMembers(active.GetNodeAddresses())
				if err != nil {
					ctx.Logger().Error("fail to get active members of vault", "error", err)
					continue
				}
				allMembers := vault.Membership
				if HasSuperMajority(len(activeMembers), len(allMembers)) {
					resp.Inactive = append(resp.Inactive, &types.VaultInfo{
						PubKey:  vault.PubKey.String(),
						Routers: castVaultRouters(vault.Routers),
					})
				}
			}
		}
	}
	return &resp, nil
}