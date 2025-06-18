// querier_trade.go contains all trading-related query handlers.
// This includes queries for trade units, trade accounts, streaming swaps, and secured assets.

package thorchain

import (
	"errors"
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ============ Trade Unit Queries ============

func (qs queryServer) queryTradeUnit(ctx cosmos.Context, req *types.QueryTradeUnitRequest) (*types.QueryTradeUnitResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to parse asset", "error", err)
		return nil, fmt.Errorf("could not parse asset: %w", err)
	}

	tu, err := qs.mgr.Keeper().GetTradeUnit(ctx, asset)
	if err != nil {
		return nil, fmt.Errorf("trade unit not found: %w", err)
	}

	resp := types.QueryTradeUnitResponse{
		Asset: tu.Asset.String(),
		Units: tu.Units.String(),
		Depth: tu.Depth.String(),
	}
	return &resp, nil
}

func (qs queryServer) queryTradeUnits(ctx cosmos.Context, _ *types.QueryTradeUnitsRequest) (*types.QueryTradeUnitsResponse, error) {
	units := make([]*types.QueryTradeUnitResponse, 0)
	iterator := qs.mgr.Keeper().GetTradeUnitIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var tu TradeUnit
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &tu); err != nil {
			ctx.Logger().Error("fail to unmarshal trade unit", "error", err)
			return nil, fmt.Errorf("fail to unmarshal trade unit: %w", err)
		}

		units = append(units, &types.QueryTradeUnitResponse{
			Asset: tu.Asset.String(),
			Units: tu.Units.String(),
			Depth: tu.Depth.String(),
		})
	}

	return &types.QueryTradeUnitsResponse{TradeUnits: units}, nil
}

// ============ Trade Account Queries ============

func (qs queryServer) queryTradeAccounts(ctx cosmos.Context, req *types.QueryTradeAccountsRequest) (*types.QueryTradeAccountsResponse, error) {
	var asset common.Asset
	if len(req.Asset) > 0 {
		var err error
		asset, err = common.NewAsset(req.Asset)
		if err != nil {
			ctx.Logger().Error("fail to parse asset", "error", err)
			return nil, fmt.Errorf("could not parse asset: %w", err)
		}
	}

	var address common.Address
	if len(req.Address) > 0 {
		var err error
		address, err = common.NewAddress(req.Address)
		if err != nil {
			ctx.Logger().Error("invalid address", "address", req.Address, "error", err)
			return nil, fmt.Errorf("invalid address: %w", err)
		}
	}

	accounts, err := qs.mgr.Keeper().GetTradeAccountsByAssetAndAddress(ctx, asset, address)
	if err != nil {
		return nil, fmt.Errorf("fail to get trade accounts: %w", err)
	}

	tas := make([]*types.QueryTradeAccountResponse, len(accounts))
	for i, account := range accounts {
		tas[i] = &types.QueryTradeAccountResponse{
			Asset:           account.Asset.String(),
			Units:           account.Units.String(),
			Owner:           account.Owner.String(),
			LastAddHeight:   account.LastAddHeight,
			LastWithdrawHeight: account.LastWithdrawHeight,
		}
	}

	return &types.QueryTradeAccountsResponse{TradeAccounts: tas}, nil
}

func (qs queryServer) queryTradeAccount(ctx cosmos.Context, req *types.QueryTradeAccountRequest) (*types.QueryTradeAccountsResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to parse asset", "error", err)
		return nil, fmt.Errorf("could not parse asset: %w", err)
	}

	if len(req.Address) == 0 {
		return nil, errors.New("address not provided")
	}
	address, err := common.NewAddress(req.Address)
	if err != nil {
		ctx.Logger().Error("invalid address", "address", req.Address, "error", err)
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	tradeAccount, err := qs.mgr.Keeper().GetTradeAccount(ctx, address, asset)
	if err != nil {
		return nil, fmt.Errorf("fail to get trade account: %w", err)
	}

	return &types.QueryTradeAccountsResponse{
		TradeAccounts: []*types.QueryTradeAccountResponse{
			{
				Asset:           tradeAccount.Asset.String(),
				Units:           tradeAccount.Units.String(),
				Owner:           tradeAccount.Owner.String(),
				LastAddHeight:   tradeAccount.LastAddHeight,
				LastWithdrawHeight: tradeAccount.LastWithdrawHeight,
			},
		},
	}, nil
}

// ============ Secured Asset Queries ============

func (qs queryServer) querySecuredAssets(ctx cosmos.Context, req *types.QuerySecuredAssetsRequest) (*types.QuerySecuredAssetsResponse, error) {
	var addr common.Address
	if len(req.Address) > 0 {
		var err error
		addr, err = common.NewAddress(req.Address)
		if err != nil {
			ctx.Logger().Error("invalid address", "address", req.Address, "error", err)
			return nil, fmt.Errorf("invalid address: %w", err)
		}
	}

	iterator := qs.mgr.Keeper().GetSecuredAssetIterator(ctx)
	defer iterator.Close()
	sas := make([]*types.QuerySecuredAssetResponse, 0)
	for ; iterator.Valid(); iterator.Next() {
		var sa SecuredAsset
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &sa); err != nil {
			ctx.Logger().Error("fail to unmarshal secured asset", "error", err)
			return nil, fmt.Errorf("fail to unmarshal secured asset: %w", err)
		}
		if !addr.IsEmpty() && !sa.Owner.Equals(addr) {
			continue
		}
		sas = append(sas, &types.QuerySecuredAssetResponse{
			Asset:              sa.Asset.String(),
			Units:              sa.Units.String(),
			Owner:              sa.Owner.String(),
			LastAddHeight:      sa.LastAddHeight,
			LastWithdrawHeight: sa.LastWithdrawHeight,
		})
	}

	return &types.QuerySecuredAssetsResponse{SecuredAssets: sas}, nil
}

func (qs queryServer) querySecuredAsset(ctx cosmos.Context, req *types.QuerySecuredAssetRequest) (*types.QuerySecuredAssetResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to parse asset", "error", err)
		return nil, fmt.Errorf("could not parse asset: %w", err)
	}

	if len(req.Address) == 0 {
		return nil, errors.New("address not provided")
	}
	address, err := common.NewAddress(req.Address)
	if err != nil {
		ctx.Logger().Error("invalid address", "address", req.Address, "error", err)
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	sa, err := qs.mgr.Keeper().GetSecuredAsset(ctx, address, asset)
	if err != nil {
		return nil, fmt.Errorf("fail to get secured asset: %w", err)
	}

	return &types.QuerySecuredAssetResponse{
		Asset:              sa.Asset.String(),
		Units:              sa.Units.String(),
		Owner:              sa.Owner.String(),
		LastAddHeight:      sa.LastAddHeight,
		LastWithdrawHeight: sa.LastWithdrawHeight,
	}, nil
}

// ============ Streaming Swap Queries ============

func (qs queryServer) queryStreamingSwaps(ctx cosmos.Context, _ *types.QueryStreamingSwapsRequest) (*types.QueryStreamingSwapsResponse, error) {
	// get all swap iterator
	iterator := qs.mgr.Keeper().GetStreamingSwapIterator(ctx)
	defer iterator.Close()
	var result []*types.QueryStreamingSwapResponse
	for ; iterator.Valid(); iterator.Next() {
		var swap StreamingSwap
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &swap); err != nil {
			ctx.Logger().Error("fail to unmarshal streaming swap", "error", err)
			return nil, fmt.Errorf("fail to unmarshal streaming swap: %w", err)
		}
		result = append(result, newStreamingSwap(swap))
	}
	return &types.QueryStreamingSwapsResponse{StreamingSwaps: result}, nil
}

func (qs queryServer) queryStreamingSwap(ctx cosmos.Context, req *types.QueryStreamingSwapRequest) (*types.QueryStreamingSwapResponse, error) {
	if len(req.TxId) == 0 {
		return nil, errors.New("tx id not provided")
	}
	hash, err := common.NewTxID(req.TxId)
	if err != nil {
		ctx.Logger().Error("fail to parse tx id", "error", err)
		return nil, fmt.Errorf("fail to parse tx id: %w", err)
	}
	swap, err := qs.mgr.Keeper().GetStreamingSwap(ctx, hash)
	if err != nil {
		ctx.Logger().Error("fail to get streaming swap", "error", err)
		return nil, fmt.Errorf("fail to get streaming swap: %w", err)
	}
	return newStreamingSwap(swap), nil
}

func (qs queryServer) querySwapperClout(ctx cosmos.Context, req *types.QuerySwapperCloutRequest) (*types.SwapperClout, error) {
	if len(req.Address) == 0 {
		return nil, errors.New("address not provided")
	}

	addr, err := common.NewAddress(req.Address)
	if err != nil {
		ctx.Logger().Error("fail to parse address", "error", err, "address", req.Address)
		return nil, fmt.Errorf("fail to parse address: %w", err)
	}

	clout, err := qs.mgr.Keeper().GetSwapperClout(ctx, addr)
	if err != nil {
		// don't error when clout doesn't exist
		return &types.SwapperClout{
			Address:        req.Address,
			Score:          "0",
			Spent:          "0",
			Reclaimed:      "0",
			LastSpentHeight: 0,
			LastReclaimHeight: 0,
		}, nil
	}

	// cast int64 to string to support openapi swagger
	return &types.SwapperClout{
		Address:        addr.String(),
		Score:          clout.Score.String(),
		Spent:          clout.Spent.String(),
		Reclaimed:      clout.Reclaimed.String(),
		LastSpentHeight: clout.LastSpentHeight,
		LastReclaimHeight: clout.LastReclaimHeight,
	}, nil
}