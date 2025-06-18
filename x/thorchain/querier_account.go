// querier_account.go contains all account and balance-related query handlers.
// This includes queries for module balances, accounts, THORNames, and RUNEPool.

package thorchain

import (
	"errors"
	"fmt"
	"strconv"

	sdkmath "cosmossdk.io/math"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ============ Module Balance Queries ============

func (qs queryServer) queryBalanceModule(ctx cosmos.Context, req *types.QueryBalanceModuleRequest) (*types.QueryBalanceModuleResponse, error) {
	moduleName := req.Name
	if len(moduleName) == 0 {
		moduleName = AsgardName
	}

	modAddr := qs.mgr.Keeper().GetModuleAccAddress(moduleName)
	bal := qs.mgr.Keeper().GetBalance(ctx, modAddr)
	balance := types.QueryBalanceModuleResponse{
		Name:    moduleName,
		Address: modAddr,
		Coins:   bal,
	}
	return &balance, nil
}

// ============ Account Queries ============

func (qs queryServer) queryAccount(ctx cosmos.Context, req *types.QueryAccountRequest) (*types.QueryAccountResponse, error) {
	addr, err := cosmos.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, cosmos.ErrUnknownRequest("invalid account address")
	}

	account := qs.mgr.Keeper().GetAccount(ctx, addr)
	if account == nil {
		return nil, errors.New("could not find account")
	}

	pubKey := ""
	if account.GetPubKey() != nil {
		pubKey = account.GetPubKey().String()
	}

	return &types.QueryAccountResponse{
		Address:       addr.String(),
		PubKey:        pubKey,
		AccountNumber: strconv.FormatUint(account.GetAccountNumber(), 10),
		Sequence:      strconv.FormatUint(account.GetSequence(), 10),
	}, nil
}

func (qs queryServer) queryBalances(ctx cosmos.Context, req *types.QueryBalancesRequest) (*types.QueryBalancesResponse, error) {
	addr, err := cosmos.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, cosmos.ErrUnknownRequest("invalid account address")
	}

	coins := qs.mgr.Keeper().GetBalance(ctx, addr)
	balances := make([]*types.Amount, len(coins))
	for i, coin := range coins {
		balances[i] = &types.Amount{
			Denom:  coin.Denom,
			Amount: coin.Amount.String(),
		}
	}

	return &types.QueryBalancesResponse{
		Balances: balances,
	}, nil
}

// ============ THORName Queries ============

func (qs queryServer) queryTHORName(ctx cosmos.Context, req *types.QueryThornameRequest) (*types.QueryThornameResponse, error) {
	name, err := qs.mgr.Keeper().GetTHORName(ctx, req.Name)
	if err != nil {
		return nil, ErrInternal(err, "fail to fetch THORName")
	}

	affRune := cosmos.ZeroUint()
	affCol, err := qs.mgr.Keeper().GetAffiliateCollector(ctx, name.Owner)
	if err == nil {
		affRune = affCol.RuneAmount
	}

	// convert to openapi types
	aliases := []*types.ThornameAlias{}
	for _, alias := range name.Aliases {
		aliases = append(aliases, &types.ThornameAlias{
			Chain:   alias.Chain.String(),
			Address: alias.Address.String(),
		})
	}

	threshold := cosmos.ZeroUint()
	if !name.PreferredAsset.IsEmpty() {
		paOf, err := qs.mgr.gasMgr.GetAssetOutboundFee(ctx, name.PreferredAsset, true)
		if err == nil {
			multiplier := qs.mgr.Keeper().GetConfigInt64(ctx, constants.PreferredAssetOutboundFeeMultiplier)
			threshold = paOf.MulUint64(uint64(multiplier))
		}
	}

	resp := types.QueryThornameResponse{
		Name:                            name.Name,
		ExpireBlockHeight:               name.ExpireBlockHeight,
		Owner:                           name.Owner.String(),
		PreferredAsset:                  name.PreferredAsset.String(),
		Aliases:                         aliases,
		AffiliateCollectorRune:          affRune.String(),
		PreferredAssetSwapThresholdRune: threshold.String(),
	}

	return &resp, nil
}

// ============ RUNEPool Queries ============

func (qs queryServer) queryRUNEPool(ctx cosmos.Context, _ *types.QueryRunePoolRequest) (*types.QueryRunePoolResponse, error) {
	// gather pol data
	pol, err := qs.mgr.Keeper().GetPOL(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get POL: %w", err)
	}
	polValue, err := polPoolValue(ctx, qs.mgr)
	if err != nil {
		return nil, fmt.Errorf("fail to fetch POL value: %w", err)
	}
	pnl := pol.PnL(polValue)

	// gather runepool data
	runePool, err := qs.mgr.Keeper().GetRUNEPool(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get RUNE pool: %w", err)
	}

	// calculate pending units
	runePoolValue, err := runePoolValue(ctx, qs.mgr)
	if err != nil {
		return nil, fmt.Errorf("fail to get rune pool value: %w", err)
	}
	pendingRune := qs.mgr.Keeper().GetRuneBalanceOfModule(ctx, RUNEPoolName)
	pendingUnits := common.GetSafeShare(pendingRune, runePoolValue, runePool.TotalUnits())

	// calculate provider shares
	providerValue := common.GetSafeShare(runePool.PoolUnits, runePool.TotalUnits(), runePoolValue)
	providerPnl := sdkmath.NewIntFromBigInt(providerValue.BigInt()).Sub(runePool.CurrentDeposit())

	// calculate reserve shares
	reserveValue := common.GetSafeShare(runePool.ReserveUnits, runePool.TotalUnits(), runePoolValue)
	reserveCurrentDeposit := pol.CurrentDeposit().
		Sub(runePool.CurrentDeposit()).
		Add(cosmos.NewIntFromBigInt(pendingRune.BigInt()))
	reservePnl := sdkmath.NewIntFromBigInt(reserveValue.BigInt()).Sub(reserveCurrentDeposit)

	result := types.QueryRunePoolResponse{
		Pol: &types.POL{
			RuneDeposited:  pol.RuneDeposited.String(),
			RuneWithdrawn:  pol.RuneWithdrawn.String(),
			Value:          polValue.String(),
			Pnl:            pnl.String(),
			CurrentDeposit: pol.CurrentDeposit().String(),
		},
		Providers: &types.RunePoolProviders{
			Units:          runePool.PoolUnits.String(),
			PendingUnits:   pendingUnits.String(),
			PendingRune:    pendingRune.String(),
			Value:          providerValue.String(),
			Pnl:            providerPnl.String(),
			CurrentDeposit: runePool.CurrentDeposit().String(),
		},
		Reserve: &types.RunePoolReserve{
			Units:          runePool.ReserveUnits.String(),
			Value:          reserveValue.String(),
			Pnl:            reservePnl.String(),
			CurrentDeposit: reserveCurrentDeposit.String(),
		},
	}

	return &result, nil
}

func (qs queryServer) queryRUNEProvider(ctx cosmos.Context, req *types.QueryRuneProviderRequest) (*types.QueryRuneProviderResponse, error) {
	if len(req.Address) == 0 {
		return nil, errors.New("address not provided")
	}
	addr, err := cosmos.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, errors.New("unable to decode address")
	}
	rp, err := qs.mgr.Keeper().GetRUNEProvider(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("unable to GetRUNEProvider: %s", err)
	}

	// get runepool value to determine current value and pnl
	runePool, err := qs.mgr.Keeper().GetRUNEPool(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get RUNE pool: %w", err)
	}
	runePoolValue, err := runePoolValue(ctx, qs.mgr)
	if err != nil {
		return nil, fmt.Errorf("fail to get rune pool value: %w", err)
	}
	providerValue := common.GetSafeShare(rp.Units, runePool.TotalUnits(), runePoolValue)
	providerPnl := providerValue.BigInt()
	providerPnl.Sub(providerPnl, rp.DepositAmount.BigInt())
	providerPnl.Add(providerPnl, rp.WithdrawAmount.BigInt())

	result := types.QueryRuneProviderResponse{
		RuneAddress:        rp.RuneAddress.String(),
		Units:              rp.Units.String(),
		Value:              providerValue.String(),
		Pnl:                providerPnl.String(),
		DepositAmount:      rp.DepositAmount.String(),
		WithdrawAmount:     rp.WithdrawAmount.String(),
		LastDepositHeight:  rp.LastDepositHeight,
		LastWithdrawHeight: rp.LastWithdrawHeight,
	}
	return &result, nil
}

func (qs queryServer) queryRUNEProviders(ctx cosmos.Context, _ *types.QueryRuneProvidersRequest) (*types.QueryRuneProvidersResponse, error) {
	// get runepool value to determine current value and pnl
	runePool, err := qs.mgr.Keeper().GetRUNEPool(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get RUNE pool: %w", err)
	}
	runePoolValue, err := runePoolValue(ctx, qs.mgr)
	if err != nil {
		return nil, fmt.Errorf("fail to get rune pool value: %w", err)
	}

	var providers []types.QueryRuneProviderResponse
	iterator := qs.mgr.Keeper().GetRUNEProviderIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var rp RUNEProvider
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &rp); err != nil {
			ctx.Logger().Error("fail to unmarshal RUNEProvider", "error", err)
			return nil, fmt.Errorf("fail to unmarshal RUNEProvider: %w", err)
		}

		providerValue := common.GetSafeShare(rp.Units, runePool.TotalUnits(), runePoolValue)
		providerPnl := providerValue.BigInt()
		providerPnl.Sub(providerPnl, rp.DepositAmount.BigInt())
		providerPnl.Add(providerPnl, rp.WithdrawAmount.BigInt())

		providers = append(providers, types.QueryRuneProviderResponse{
			RuneAddress:        rp.RuneAddress.String(),
			Units:              rp.Units.String(),
			Value:              providerValue.String(),
			Pnl:                providerPnl.String(),
			DepositAmount:      rp.DepositAmount.String(),
			WithdrawAmount:     rp.WithdrawAmount.String(),
			LastDepositHeight:  rp.LastDepositHeight,
			LastWithdrawHeight: rp.LastWithdrawHeight,
		})
	}

	result := types.QueryRuneProvidersResponse{
		Providers: make([]*types.QueryRuneProviderResponse, len(providers)),
	}
	for i := range providers {
		result.Providers[i] = &providers[i]
	}
	return &result, nil
}