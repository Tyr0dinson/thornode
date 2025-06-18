// querier_node.go contains all node and network-related query handlers.
// This includes queries for individual nodes, all nodes, network state, and ban information.

package thorchain

import (
	"errors"
	"fmt"
	"sort"

	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ============ Network Query ============

func (qs queryServer) queryNetwork(ctx cosmos.Context, _ *types.QueryNetworkRequest) (*types.QueryNetworkResponse, error) {
	data, err := qs.mgr.Keeper().GetNetwork(ctx)
	if err != nil {
		ctx.Logger().Error("fail to get network", "error", err)
		return nil, fmt.Errorf("fail to get network: %w", err)
	}

	vaults, err := qs.mgr.Keeper().GetAsgardVaultsByStatus(ctx, RetiringVault)
	if err != nil {
		return nil, fmt.Errorf("fail to get retiring vaults: %w", err)
	}
	vaultsMigrating := (len(vaults) != 0)

	nodeAccounts, err := qs.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get active validators: %w", err)
	}

	_, availablePoolsRune, err := getAvailablePoolsRune(ctx, qs.mgr.Keeper())
	if err != nil {
		return nil, fmt.Errorf("fail to get available pools rune: %w", err)
	}
	vaultsLiquidityRune, err := getVaultsLiquidityRune(ctx, qs.mgr.Keeper())
	if err != nil {
		return nil, fmt.Errorf("fail to get vaults liquidity rune: %w", err)
	}
	effectiveSecurityBond := getEffectiveSecurityBond(nodeAccounts)

	targetOutboundFeeSurplus := qs.mgr.Keeper().GetConfigInt64(ctx, constants.TargetOutboundFeeSurplusRune)
	maxMultiplierBasisPoints := qs.mgr.Keeper().GetConfigInt64(ctx, constants.MaxOutboundFeeMultiplierBasisPoints)
	minMultiplierBasisPoints := qs.mgr.Keeper().GetConfigInt64(ctx, constants.MinOutboundFeeMultiplierBasisPoints)
	outboundFeeMultiplier := qs.mgr.gasMgr.CalcOutboundFeeMultiplier(ctx, cosmos.NewUint(uint64(targetOutboundFeeSurplus)), cosmos.NewUint(data.OutboundGasSpentRune), cosmos.NewUint(data.OutboundGasWithheldRune), cosmos.NewUint(uint64(maxMultiplierBasisPoints)), cosmos.NewUint(uint64(minMultiplierBasisPoints)))

	assets := qs.mgr.Keeper().GetAnchors(ctx, common.TOR)
	median := qs.mgr.Keeper().AnchorMedian(ctx, assets).QuoUint64(constants.DollarMulti)

	result := types.QueryNetworkResponse{
		// Due to using openapi. this will be displayed in alphabetical order,
		// so its schema (and order here) should also be in alphabetical order.
		BondRewardRune:        data.BondRewardRune.String(),
		TotalBondUnits:        data.TotalBondUnits.String(),
		AvailablePoolsRune:    availablePoolsRune.String(),
		VaultsLiquidityRune:   vaultsLiquidityRune.String(),
		EffectiveSecurityBond: effectiveSecurityBond.String(),
		TotalReserve:          qs.mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).String(),
		VaultsMigrating:       vaultsMigrating,
		GasSpentRune:          cosmos.NewUint(data.OutboundGasSpentRune).String(),
		GasWithheldRune:       cosmos.NewUint(data.OutboundGasWithheldRune).String(),
		OutboundFeeMultiplier: outboundFeeMultiplier.String(),
		NativeTxFeeRune:       qs.mgr.Keeper().GetNativeTxFee(ctx).String(),
		NativeOutboundFeeRune: qs.mgr.Keeper().GetOutboundTxFee(ctx).String(),
		TnsRegisterFeeRune:    qs.mgr.Keeper().GetTHORNameRegisterFee(ctx).String(),
		TnsFeePerBlockRune:    qs.mgr.Keeper().GetTHORNamePerBlockFee(ctx).String(),
		RunePriceInTor:        dollarsPerRuneIgnoreHalt(ctx, qs.mgr.Keeper()).String(),
		TorPriceInRune:        runePerDollarIgnoreHalt(ctx, qs.mgr.Keeper()).String(),
		TorPriceHalted:        median.IsZero(),
	}

	return &result, nil
}

// ============ Node Queries ============

// queryNode return the Node information related to the request node address
// /thorchain/node/{nodeaddress}
func (qs queryServer) queryNode(ctx cosmos.Context, req *types.QueryNodeRequest) (*types.QueryNodeResponse, error) {
	if len(req.Address) == 0 {
		return nil, errors.New("node address not provided")
	}
	nodeAddress := req.Address
	addr, err := cosmos.AccAddressFromBech32(nodeAddress)
	if err != nil {
		return nil, cosmos.ErrUnknownRequest("invalid account address")
	}

	nodeAcc, err := qs.mgr.Keeper().GetNodeAccount(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("fail to get node accounts: %w", err)
	}

	slashPts, err := qs.mgr.Keeper().GetNodeAccountSlashPoints(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("fail to get node slash points: %w", err)
	}
	jail, err := qs.mgr.Keeper().GetNodeAccountJail(ctx, nodeAcc.NodeAddress)
	if err != nil {
		return nil, fmt.Errorf("fail to get node jail: %w", err)
	}

	bp, err := qs.mgr.Keeper().GetBondProviders(ctx, nodeAcc.NodeAddress)
	if err != nil {
		return nil, fmt.Errorf("fail to get bond providers: %w", err)
	}
	bp.Adjust(nodeAcc.Bond)

	active, err := qs.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get all active node account: %w", err)
	}

	result := types.QueryNodeResponse{
		NodeAddress: nodeAcc.NodeAddress.String(),
		Status:      nodeAcc.Status.String(),
		PubKeySet: common.PubKeySet{
			Secp256k1: common.PubKey(nodeAcc.PubKeySet.Secp256k1.String()),
			Ed25519:   common.PubKey(nodeAcc.PubKeySet.Ed25519.String()),
		},
		ValidatorConsPubKey: nodeAcc.ValidatorConsPubKey,
		ActiveBlockHeight:   nodeAcc.ActiveBlockHeight,
		StatusSince:         nodeAcc.StatusSince,
		NodeOperatorAddress: nodeAcc.BondAddress.String(),
		TotalBond:           nodeAcc.Bond.String(),
		SignerMembership:    nodeAcc.GetSignerMembership().Strings(),
		RequestedToLeave:    nodeAcc.RequestedToLeave,
		ForcedToLeave:       nodeAcc.ForcedToLeave,
		LeaveHeight:         int64(nodeAcc.LeaveScore), // OpenAPI can only represent uint64 as int64
		Maintenance:         nodeAcc.Maintenance,
		IpAddress:           nodeAcc.IPAddress,
		Version:             nodeAcc.GetVersion().String(),
		CurrentAward:        cosmos.ZeroUint().String(), // Default display for if not overwritten.
	}
	result.PeerId = getPeerIDFromPubKey(nodeAcc.PubKeySet.Secp256k1)
	result.SlashPoints = slashPts

	result.Jail = &types.NodeJail{
		// Since redundant, leave out the node address
		ReleaseHeight: jail.ReleaseHeight,
		Reason:        jail.Reason,
	}

	var providers []*types.NodeBondProvider
	// Leave this nil (null rather than []) if the source is nil.
	if bp.Providers != nil {
		providers = make([]*types.NodeBondProvider, len(bp.Providers))
		for i, p := range bp.Providers {
			providers[i] = &types.NodeBondProvider{
				BondAddress: p.BondAddress.String(),
				Bond:        p.Bond.String(),
			}
		}
	}

	result.BondProviders = &types.NodeBondProviders{
		// Since redundant, leave out the node address
		NodeOperatorFee: bp.NodeOperatorFee.String(),
		Providers:       providers,
	}

	// CurrentAward is an estimation of reward for node in active status
	// Node in other status should not have current reward
	if nodeAcc.Status == NodeActive && !nodeAcc.Bond.IsZero() {
		network, err := qs.mgr.Keeper().GetNetwork(ctx)
		if err != nil {
			return nil, fmt.Errorf("fail to get network: %w", err)
		}
		vaults, err := qs.mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
		if err != nil {
			return nil, fmt.Errorf("fail to get active vaults: %w", err)
		}
		if len(vaults) == 0 {
			return nil, fmt.Errorf("no active vaults")
		}

		totalEffectiveBond, bondHardCap := getTotalEffectiveBond(active)

		lastChurnHeight := vaults[0].StatusSince

		reward, err := getNodeCurrentRewards(ctx, qs.mgr, nodeAcc, lastChurnHeight, network.BondRewardRune, totalEffectiveBond, bondHardCap)
		if err != nil {
			return nil, fmt.Errorf("fail to get current node rewards: %w", err)
		}

		result.CurrentAward = reward.String()
	}

	// TODO: Represent this map as the field directly, instead of making an array?
	// It would then always be represented in alphabetical order.
	chainHeights, err := qs.mgr.Keeper().GetLastObserveHeight(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("fail to get last observe chain height: %w", err)
	}
	// analyze-ignore(map-iteration)
	for c, h := range chainHeights {
		result.ObserveChains = append(result.ObserveChains, &types.ChainHeight{
			Chain:  c.String(),
			Height: h,
		})
	}

	preflightCheckResult, err := getNodePreflightResult(ctx, qs.mgr, nodeAcc)
	if err != nil {
		ctx.Logger().Error("fail to get node preflight result", "error", err)
	} else {
		result.PreflightStatus = &preflightCheckResult
	}

	return &result, nil
}

// queryNodes return all the nodes that has bond
// /thorchain/nodes
func (qs queryServer) queryNodes(ctx cosmos.Context, _ *types.QueryNodesRequest) (*types.QueryNodesResponse, error) {
	nodeAccounts, err := qs.mgr.Keeper().ListValidatorsWithBond(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get node accounts: %w", err)
	}

	active, err := qs.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get all active node account: %w", err)
	}

	network, err := qs.mgr.Keeper().GetNetwork(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get network: %w", err)
	}

	vaults, err := qs.mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return nil, fmt.Errorf("fail to get active vaults: %w", err)
	}
	if len(vaults) == 0 {
		return nil, fmt.Errorf("no active vaults")
	}

	totalEffectiveBond, bondHardCap := getTotalEffectiveBond(active)

	lastChurnHeight := vaults[0].StatusSince
	result := make([]*types.QueryNodeResponse, len(nodeAccounts))
	for i, na := range nodeAccounts {
		if na.RequestedToLeave && na.Bond.LTE(cosmos.NewUint(common.One)) {
			// ignore the node , it left and also has very little bond
			// Set the default display for fields which would otherwise be "".
			result[i] = &types.QueryNodeResponse{
				Status:          types.NodeStatus_Unknown.String(),
				TotalBond:       cosmos.ZeroUint().String(),
				BondProviders:   &types.NodeBondProviders{NodeOperatorFee: cosmos.ZeroUint().String()},
				Version:         semver.MustParse("0.0.0").String(),
				CurrentAward:    cosmos.ZeroUint().String(),
				PreflightStatus: &types.NodePreflightStatus{Status: types.NodeStatus_Unknown.String()},
			}
			continue
		}

		slashPts, err := qs.mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
		if err != nil {
			return nil, fmt.Errorf("fail to get node slash points: %w", err)
		}

		result[i] = &types.QueryNodeResponse{
			NodeAddress: na.NodeAddress.String(),
			Status:      na.Status.String(),
			PubKeySet: common.PubKeySet{
				Secp256k1: common.PubKey(na.PubKeySet.Secp256k1.String()),
				Ed25519:   common.PubKey(na.PubKeySet.Ed25519.String()),
			},
			ValidatorConsPubKey: na.ValidatorConsPubKey,
			ActiveBlockHeight:   na.ActiveBlockHeight,
			StatusSince:         na.StatusSince,
			NodeOperatorAddress: na.BondAddress.String(),
			TotalBond:           na.Bond.String(),
			SignerMembership:    na.GetSignerMembership().Strings(),
			RequestedToLeave:    na.RequestedToLeave,
			ForcedToLeave:       na.ForcedToLeave,
			LeaveHeight:         int64(na.LeaveScore), // OpenAPI can only represent uint64 as int64
			Maintenance:         na.Maintenance,
			MissingBlocks:       int64(na.MissingBlocks),
			IpAddress:           na.IPAddress,
			Version:             na.GetVersion().String(),
			CurrentAward:        cosmos.ZeroUint().String(), // Default display for if not overwritten.
		}
		result[i].PeerId = getPeerIDFromPubKey(na.PubKeySet.Secp256k1)
		result[i].SlashPoints = slashPts
		if na.Status == NodeActive {
			reward, err := getNodeCurrentRewards(ctx, qs.mgr, na, lastChurnHeight, network.BondRewardRune, totalEffectiveBond, bondHardCap)
			if err != nil {
				return nil, fmt.Errorf("fail to get current node rewards: %w", err)
			}

			result[i].CurrentAward = reward.String()
		}

		jail, err := qs.mgr.Keeper().GetNodeAccountJail(ctx, na.NodeAddress)
		if err != nil {
			return nil, fmt.Errorf("fail to get node jail: %w", err)
		}
		result[i].Jail = &types.NodeJail{
			// Since redundant, leave out the node address
			ReleaseHeight: jail.ReleaseHeight,
			Reason:        jail.Reason,
		}

		// TODO: Represent this map as the field directly, instead of making an array?
		// It would then always be represented in alphabetical order.
		chainHeights, err := qs.mgr.Keeper().GetLastObserveHeight(ctx, na.NodeAddress)
		if err != nil {
			return nil, fmt.Errorf("fail to get last observe chain height: %w", err)
		}
		// analyze-ignore(map-iteration)
		for c, h := range chainHeights {
			result[i].ObserveChains = append(result[i].ObserveChains, &types.ChainHeight{
				Chain:  c.String(),
				Height: h,
			})
		}

		preflightCheckResult, err := getNodePreflightResult(ctx, qs.mgr, na)
		if err != nil {
			ctx.Logger().Error("fail to get node preflight result", "error", err)
		} else {
			result[i].PreflightStatus = &preflightCheckResult
		}

		bp, err := qs.mgr.Keeper().GetBondProviders(ctx, na.NodeAddress)
		if err != nil {
			ctx.Logger().Error("fail to get bond providers", "error", err)
		}
		bp.Adjust(na.Bond)

		var providers []*types.NodeBondProvider
		// Leave this nil (null rather than []) if the source is nil.
		if bp.Providers != nil {
			providers = make([]*types.NodeBondProvider, len(bp.Providers))
			for i := range bp.Providers {
				providers[i] = &types.NodeBondProvider{
					BondAddress: bp.Providers[i].BondAddress.String(),
					Bond:        bp.Providers[i].Bond.String(),
				}
			}
		}

		result[i].BondProviders = &types.NodeBondProviders{
			// Since redundant, leave out the node address
			NodeOperatorFee: bp.NodeOperatorFee.String(),
			Providers:       providers,
		}
	}

	return &types.QueryNodesResponse{Nodes: result}, nil
}

// ============ Ban Query ============

func (qs queryServer) queryBan(ctx cosmos.Context, req *types.QueryBanRequest) (*types.BanVoter, error) {
	if len(req.Address) == 0 {
		return nil, errors.New("node address not available")
	}
	addr, err := cosmos.AccAddressFromBech32(req.Address)
	if err != nil {
		ctx.Logger().Error("invalid node address", "error", err)
		return nil, fmt.Errorf("invalid node address: %w", err)
	}

	ban, err := qs.mgr.Keeper().GetBanVoter(ctx, addr)
	if err != nil {
		ctx.Logger().Error("fail to get ban voter", "error", err)
		return nil, fmt.Errorf("fail to get ban voter: %w", err)
	}

	return &ban, nil
}

