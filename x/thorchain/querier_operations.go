// querier_operations.go contains all chain operations-related query handlers.
// This includes queries for inbound addresses, queues, outbounds, and chain heights.

package thorchain

import (
	"errors"
	"fmt"
	"sort"
	"strconv"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ============ Inbound Address Queries ============

func (qs queryServer) queryInboundAddresses(ctx cosmos.Context, _ *types.QueryInboundAddressesRequest) (*types.QueryInboundAddressesResponse, error) {
	active, err := qs.mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		ctx.Logger().Error("fail to get active vaults", "error", err)
		return nil, fmt.Errorf("fail to get active vaults: %w", err)
	}

	type address struct {
		Chain               common.Chain
		PubKey              common.PubKey
		Address             common.Address
		Router              common.Address
		RouterChainAddress  string
		Halted              bool
		GlobalTradingPaused bool
		ChainTradingPaused  bool
		ChainLpActionPaused bool
		GasRate             string
		GasRateUnits        string
		OutboundTxSize      string
		OutboundFee         string
		RemainingDeposits   string
		TrackingDeposits    bool
		ERC20Depositor      common.Address
	}

	var addresses []address
	constAccessor := qs.mgr.GetConstants()
	// analyze-ignore(map-iteration)
	for _, chain := range common.AllChains {
		if chain.IsTHORChain() {
			continue
		}

		chainDepositorValue := fmt.Sprintf("ChainDepositor%s", chain)
		chainDepositor, _ := qs.mgr.Keeper().GetMimir(ctx, chainDepositorValue)

		trackingDeposits := chainDepositor > 0

		maxActivePerPool := qs.mgr.Keeper().GetConfigInt64(ctx, constants.MaxThorchainHeightVariance)
		for _, vault := range active {
			if !vault.Contains(chain) {
				continue
			}

			vaultAddress, err := vault.PubKey.GetAddress(chain)
			if err != nil {
				ctx.Logger().Error("fail to get address for chain", "error", err)
				return nil, fmt.Errorf("fail to get address for chain: %w", err)
			}

			addr := address{
				Chain:               chain,
				PubKey:              vault.PubKey,
				Address:             vaultAddress,
				Halted:              qs.mgr.Keeper().IsChainHalted(ctx, chain),
				GlobalTradingPaused: qs.mgr.Keeper().IsGlobalTradingHalted(ctx) || qs.mgr.RagnarokInProgress(ctx),
				ChainTradingPaused:  qs.mgr.Keeper().IsChainTradingHalted(ctx, chain),
				ChainLpActionPaused: qs.mgr.Keeper().IsLPPaused(ctx, chain),
				TrackingDeposits:    trackingDeposits,
			}

			// Amount that is in the process of being deposited into the pool.
			remainingDeposits := cosmos.ZeroUint()

			outboundQueue, err := qs.mgr.Keeper().GetOutboundQueue(ctx)
			if err != nil {
				ctx.Logger().Error("fail to get outbound queue", "error", err)
				return nil, fmt.Errorf("fail to get outbound queue: %w", err)
			}

			// count deposits on the vault address
			iter := qs.mgr.Keeper().GetQuoteLoanOpenIterator(ctx)
			defer iter.Close()
			for ; iter.Valid(); iter.Next() {
				var loan types.Loan
				if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &loan); err != nil {
					ctx.Logger().Error("fail to unmarshal loan", "error", err)
					continue
				}
				if loan.Collateral().IsZero() || loan.Debt().IsZero() {
					continue
				}
				for _, coin := range loan.CollateralDeposited.GetCoins() {
					if coin.Asset.GetChain().Equals(chain) {
						remainingDeposits = remainingDeposits.Add(coin.Amount)
					}
				}
			}

			// count deposits in the outbound queue
			for i := range outboundQueue {
				if outboundQueue[i].InHash.IsEmpty() {
					// skip outbounds without an InHash
					continue
				}
				if outboundQueue[i].Chain.Equals(chain) && !outboundQueue[i].Coin.Asset.IsSyntheticAsset() {
					remainingDeposits = remainingDeposits.Add(outboundQueue[i].Coin.Amount)
				}
			}

			// count scheduled outbounds
			scheduledOutbounds, err := qs.mgr.Keeper().GetScheduledOutbound(ctx, chain)
			if err != nil {
				ctx.Logger().Error("fail to get scheduled outbound", "error", err)
				return nil, fmt.Errorf("fail to get scheduled outbound: %w", err)
			}
			for i := range scheduledOutbounds {
				if scheduledOutbounds[i].InHash.IsEmpty() {
					// skip outbounds without an InHash
					continue
				}
				if scheduledOutbounds[i].Chain.Equals(chain) && !scheduledOutbounds[i].Coin.Asset.IsSyntheticAsset() {
					remainingDeposits = remainingDeposits.Add(scheduledOutbounds[i].Coin.Amount)
				}
			}

			// count deposits in the streaming swaps
			iterator := qs.mgr.Keeper().GetStreamingSwapIterator(ctx)
			defer iterator.Close()
			for ; iterator.Valid(); iterator.Next() {
				var swap StreamingSwap
				if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &swap); err != nil {
					ctx.Logger().Error("fail to unmarshal streaming swap", "error", err)
					continue
				}
				if swap.IsDone() {
					continue
				}
				inCoin := common.NewCoin(swap.TargetAsset, swap.TradeTarget)
				if trackingDeposits && swap.In.GetChain().Equals(chain) {
					remainingDeposits = remainingDeposits.Add(swap.Deposit.Sub(swap.In))
				}
				if swap.Out.GetChain().Equals(chain) {
					remainingDeposits = remainingDeposits.Add(inCoin.Amount.Sub(swap.Out))
				}
			}

			addr.RemainingDeposits = remainingDeposits.String()

			// Retrieve the contracted address for the chain.
			if !vault.IsEmpty() {
				routers, err := vault.GetContract(chain)
				if err != nil {
					ctx.Logger().Error("fail to get contract for chain", "error", err)
					return nil, fmt.Errorf("fail to get contract for chain: %w", err)
				}
				if routers != nil && !routers.Router.IsEmpty() {
					addr.Router = routers.Router
				}
			}

			gasRate := qs.mgr.GasMgr().GetGasRate(ctx, chain)
			gasAsset := chain.GetGasAsset()

			if trackingDeposits && (chain.IsEVM() || chain.IsEVMExtra()) {
				erc20Depositor, err := qs.mgr.Keeper().GetChainDepositor(ctx, chain)
				if err == nil && !erc20Depositor.IsEmpty() {
					addr.ERC20Depositor = erc20Depositor
				}
			}

			var fee common.Coin
			if chain.IsBNBChain() {
				// BNB chain doesn't have min amount
				fee = common.NewCoin(gasAsset, qs.mgr.GasMgr().CalcOutboundFeeMultiplier(ctx, cosmos.NewUint(uint64(constAccessor.GetInt64Value(constants.TransactionFee)))).QuoUint64(2))
			} else {
				fee = common.NewCoin(gasAsset, qs.mgr.GasMgr().GetFee(ctx, chain, gasAsset))
			}
			addr.GasRate = gasRate.String()

			decimals := constAccessor.GetInt64Value(constants.NativeDecimals)
			if !gasAsset.Equals(common.RuneNative) {
				externalAsset, err := qs.mgr.Keeper().GetAsset(ctx, gasAsset)
				if err == nil {
					decimals = externalAsset.Decimals
				}
			}
			addr.GasRateUnits = gasAsset.WithDecimals(decimals).String()

			// outbound_fee: gas_rate * 3 (for BTC) / gas_rate * vault.MaxGas for others
			// Use CalcOutboundFeeMultiplier because BTC/UTXO OutboundTxSize varies,
			// and the transaction size to consolidate UTXOs affects the dust threshold.
			if !fee.Amount.IsZero() {
				outboundTxSize := constAccessor.GetInt64Value(constants.OutboundTransactionFee) / gasRate.Uint64()
				if chain.IsUTXO() {
					dustThreshold := constAccessor.GetInt64Value(constants.DustThreshold)
					// dustThreshold amount in gas asset
					dustThresholdCoin := common.NewCoin(gasAsset, cosmos.NewUint(uint64(dustThreshold)))
					if !chain.DustThreshold().IsZero() {
						dustThresholdCoin = chain.DustThreshold()
					}
					pool, err := qs.mgr.Keeper().GetPool(ctx, gasAsset)
					if err != nil {
						ctx.Logger().Error("fail to get pool", "asset", gasAsset, "error", err)
						return nil, fmt.Errorf("fail to get pool(%s): %w", gasAsset, err)
					}
					dustThresholdAsset := dustThresholdCoin.Amount

					// If pool is not available then the asset isn't perceived to have value, thus set to max.
					if pool.Status != PoolAvailable {
						dustThresholdAsset = cosmos.NewUint(constants.MaxUint)
					} else {
						dustThresholdAsset = pool.RuneValueInAsset(dustThresholdCoin.Amount)
					}
					// with an average of 1.5 inputs and 2 outputs, and 10 UTXOs needing to be consolidated for spending for each input, that is ~4000 vBytes.
					// From monitoring the BTC wallet lifecycle, it seems to consolidate and broadcast around 1MB worth of UTXOs, and with a vByte price of 25 sat/vByte, this is roughly 0.25 BTC.
					// So to pick a simple round number, if the UTXOs total are less than 1 BTC, then Asgard should consolidate.
					// But this concept applies to all UTXO chains, so convert to Rune and then to Asset for comparison.
					consolidationSize := sdk.NewUint(maxActivePerPool / 2).QuoUint64(1024).MulUint64(uint64(gasRate))
					// When the outboundTxSize is default (i.e., 1024), the number of vault UTXOs is fewer than maxActivePerPool / 2,
					// but when the vault has many UTXOs (e.g., 15,000 UTXOs), they may need consolidation and this will drive the multiplier above 3x.
					// Also, the more UTXOs to consolidate, the longer it takes and the higher a fee should be paid.
					// Calculate by adding 70 vBytes per UTXO.
					maxUTXOsToSpend := constAccessor.GetInt64Value(constants.MaxUTXOsToSpend)
					if utxos := int64(len(vault.PendingTxBlockHeights)); utxos > maxActivePerPool/2 {
						if utxos > maxUTXOsToSpend {
							utxos = maxUTXOsToSpend
						}
						outboundTxSize = (utxos * 70) / gasRate.Uint64()
					}

					minOutboundUTXO := cosmos.ZeroUint()
					// We don't know what coins will be spent, so this will underestimate the minimum UTXO size slightly. For gas assets, this will be accurate.
					// The underestimate is acceptable as UTXO consolidation is fairly cheap.
					minOutboundCoin := common.NewCoin(gasAsset, minOutboundUTXO)
					minOutboundUTXO = dustThresholdAsset.Add(minOutboundCoin.Amount.MulUint64(2).QuoUint64(3))
					addr.OutboundTxSize = strconv.FormatInt(outboundTxSize, 10)
					addr.OutboundFee = qs.mgr.GasMgr().CalcOutboundFeeMultiplier(ctx, cosmos.NewUint(uint64(outboundTxSize)).MulUint64(uint64(gasRate))).String()
				} else {
					addr.OutboundTxSize = strconv.FormatInt(outboundTxSize, 10)
					addr.OutboundFee = qs.mgr.GasMgr().CalcOutboundFeeMultiplier(ctx, cosmos.NewUint(uint64(outboundTxSize)).MulUint64(uint64(gasRate))).String()
				}
			}

			// RouterChainAddress has a different address for the Router on different chains.
			if !addr.Router.IsEmpty() {
				routerChainAddress, err := addr.Router.GetAddress(chain)
				if err != nil {
					ctx.Logger().Error("fail to get address for chain", "error", err)
					return nil, fmt.Errorf("fail to get address for chain: %w", err)
				}
				addr.RouterChainAddress = routerChainAddress.String()
			}

			addresses = append(addresses, addr)
		}
	}

	var result []*types.QueryInboundAddressesItem
	for _, item := range addresses {
		result = append(result, &types.QueryInboundAddressesItem{
			Chain:               item.Chain.String(),
			PubKey:              item.PubKey.String(),
			Address:             item.Address.String(),
			Router:              item.Router.String(),
			RouterChainAddress:  item.RouterChainAddress,
			Halted:              item.Halted,
			GlobalTradingPaused: item.GlobalTradingPaused,
			ChainTradingPaused:  item.ChainTradingPaused,
			ChainLpActionPaused: item.ChainLpActionPaused,
			GasRate:             item.GasRate,
			GasRateUnits:        item.GasRateUnits,
			OutboundTxSize:      item.OutboundTxSize,
			OutboundFee:         item.OutboundFee,
			RemainingDeposits:   item.RemainingDeposits,
			TrackingDeposits:    item.TrackingDeposits,
			Erc20Depositor:      item.ERC20Depositor.String(),
		})
	}

	return &types.QueryInboundAddressesResponse{InboundAddresses: result}, nil
}

// ============ Queue Queries ============

func (qs queryServer) queryQueue(ctx cosmos.Context, _ *types.QueryQueueRequest) (*types.QueryQueueResponse, error) {
	queueScheduledOutbounds := make([]*types.QueryQueueOutbound, 0)
	queuePendingOutbounds := make([]*types.QueryQueueOutbound, 0)

	signingTransPeriod := qs.mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	startHeight := ctx.BlockHeight() - signingTransPeriod
	// analyze-ignore(map-iteration)
	for h := startHeight; h <= ctx.BlockHeight(); h++ {
		txs, err := qs.mgr.Keeper().GetTxOut(ctx, h)
		if err != nil {
			ctx.Logger().Error("fail to get tx out array from key value store", "error", err)
			continue
		}
		// update block height
		if txs.IsEmpty() {
			// nothing , continue to the next block
			continue
		}
		for i, tx := range txs.TxArray {
			// if the outbound is in the future, it's a scheduled outbound
			if h > ctx.BlockHeight() {
				queueScheduledOutbounds = append(queueScheduledOutbounds, &types.QueryQueueOutbound{
					Height:   tx.OutboundHeight,
					Chain:    tx.Chain.String(),
					FromAddr: tx.FromAddress.String(),
					ToAddr:   tx.ToAddress.String(),
					Coin:     tx.Coin.String(),
					Memo:     tx.Memo,
					GasRate:  tx.GasRate,
					InHash:   tx.InHash.String(),
				})
			} else {
				queuePendingOutbounds = append(queuePendingOutbounds, &types.QueryQueueOutbound{
					Height:   h,
					Chain:    tx.Chain.String(),
					FromAddr: tx.FromAddress.String(),
					ToAddr:   tx.ToAddress.String(),
					Coin:     tx.Coin.String(),
					Memo:     tx.Memo,
					GasRate:  tx.GasRate,
					InHash:   tx.InHash.String(),
				})
			}
			// THORChain don't process outbound after the queue grow too large
			// here doesn't place a hard cap , but just filter out the outgoing transaction that would never been processed
			if i > qs.mgr.Keeper().GetConfigInt64(ctx, constants.MaxOutboundQueue) {
				break
			}
		}
		// given SigningTransactionPeriod is 300 , every second there are 3 tx , max will be 1K tx
		// asgard will not sign the tx anyway , so don't return it
		if len(queuePendingOutbounds) > 1000 {
			break
		}
	}

	sch, err := qs.mgr.Keeper().GetAllPendingScheduledOutbound(ctx)
	if err != nil {
		ctx.Logger().Error("fail to get all pending scheduled outbound", "error", err)
		return nil, fmt.Errorf("fail to get all pending scheduled outbound: %w", err)
	}
	for _, tx := range sch {
		queueScheduledOutbounds = append(queueScheduledOutbounds, &types.QueryQueueOutbound{
			Height:   tx.OutboundHeight,
			Chain:    tx.Chain.String(),
			FromAddr: tx.FromAddress.String(),
			ToAddr:   tx.ToAddress.String(),
			Coin:     tx.Coin.String(),
			Memo:     tx.Memo,
			GasRate:  tx.GasRate,
			InHash:   tx.InHash.String(),
		})
	}

	result := types.QueryQueueResponse{
		ScheduledOutboundValue: strconv.Itoa(len(queueScheduledOutbounds)),
		PendingOutboundValue:   strconv.Itoa(len(queuePendingOutbounds)),
		Scheduled:              queueScheduledOutbounds,
		Pending:                queuePendingOutbounds,
	}
	return &result, nil
}

func (qs queryServer) queryScheduledOutbound(ctx cosmos.Context, _ *types.QueryScheduledOutboundRequest) (*types.QueryOutboundResponse, error) {
	outs := make([]*types.TxOutItem, 0)
	scheduledOutbound, err := qs.mgr.Keeper().GetAllPendingScheduledOutbound(ctx)
	if err != nil {
		ctx.Logger().Error("fail to get scheduled outbound", "error", err)
		return nil, fmt.Errorf("fail to get scheduled outbound: %w", err)
	}

	// retrieve TxOutItems from scheduled outbound queue
	for _, item := range scheduledOutbound {
		outs = append(outs, castTxOutItem(item))
	}

	// sort by height ascending
	sort.SliceStable(outs, func(i, j int) bool {
		return outs[i].Height < outs[j].Height
	})

	return &types.QueryOutboundResponse{Outbounds: outs}, nil
}

func (qs queryServer) queryPendingOutbound(ctx cosmos.Context, _ *types.QueryPendingOutboundRequest) (*types.QueryOutboundResponse, error) {
	constAccessor := qs.mgr.GetConstants()
	signingTransPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)
	rescheduleCoalesceBlocks := constAccessor.GetInt64Value(constants.RescheduleCoalesceBlocks)
	startHeight := ctx.BlockHeight() - signingTransPeriod
	var outs []*types.TxOutItem
	// analyze-ignore(map-iteration)
	for height := startHeight; height <= ctx.BlockHeight(); height++ {
		txOut, err := qs.mgr.Keeper().GetTxOut(ctx, height)
		if err != nil {
			ctx.Logger().Error("fail to get tx out array from key value store", "error", err)
			continue
		}

		if height == ctx.BlockHeight() {
			// update block height
			if txOut.IsEmpty() {
				// nothing , continue to the next block
				continue
			}
		}

		for _, toi := range txOut.TxArray {
			// don't count already outstanding txns, as they shouldn't be rescheduled
			if toi.CloutSpent != nil {
				continue
			}
			outs = append(outs, castTxOutItem(&toi))
		}
	}

	// sort by height ascending
	sort.SliceStable(outs, func(i, j int) bool {
		return outs[i].Height < outs[j].Height
	})

	return &types.QueryOutboundResponse{Outbounds: outs}, nil
}

func (qs queryServer) querySwapQueue(ctx cosmos.Context, _ *types.QuerySwapQueueRequest) (*types.QuerySwapQueueResponse, error) {
	result := types.QuerySwapQueueResponse{}
	iterator := qs.mgr.Keeper().GetSwapQueueIterator(ctx)
	defer iterator.Close()
	count := 0
	for ; iterator.Valid(); iterator.Next() {
		var msg MsgSwap
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &msg); err != nil {
			continue
		}
		result.Swap = append(result.Swap, msg.Tx.ID.String())
		count++
		if count > 1000 {
			break
		}
	}
	return &result, nil
}

// ============ Outbound Fee Queries ============

func (qs queryServer) queryOutboundFees(ctx cosmos.Context, asset string) (*types.QueryOutboundFeesResponse, error) {
	// TODO: This is a candidate for Anon Function int he handler in the future
	assets := []common.Asset{}
	if len(asset) > 0 {
		asset, err := common.NewAsset(asset)
		if err != nil {
			ctx.Logger().Error("fail to parse asset", "error", err, "asset", asset)
			return nil, fmt.Errorf("fail to parse asset (%s): %w", asset, err)
		}
		assets = []common.Asset{asset}
	} else {
		// analyze-ignore(map-iteration)
		for _, chain := range common.AllChains {
			if chain == common.THORChain {
				continue
			}
			assets = append(assets, chain.GetGasAsset())
		}
	}

	result := make([]*types.QueryOutboundFeesItem, len(assets))
	for i := range assets {
		result[i] = &types.QueryOutboundFeesItem{}
		result[i].Asset = assets[i].String()
		fee, err := qs.mgr.GasMgr().GetAssetOutboundFee(ctx, assets[i], false)
		if err != nil {
			return nil, fmt.Errorf("fail to get asset outbound fee for asset (%s): %w", assets[i], err)
		}
		result[i].OutboundFee = fee.String()

		// get the gas asset pool
		pool, err := qs.mgr.Keeper().GetPool(ctx, assets[i])
		if err != nil {
			return nil, fmt.Errorf("fail to get gas asset pool for asset (%s): %w", assets[i], err)
		}

		// calculate the fee in rune
		if pool.IsAvailable() && !pool.BalanceRune.IsZero() {
			result[i].FeeWithheldRune = pool.AssetValueInRune(fee).String()
			_, affiliateFeeRune := pool.CalcAffiliateFeeBreakdown(cosmos.NewUint(750), fee)
			result[i].FeeSpentRune = affiliateFeeRune.String()
		} else {
			result[i].FeeWithheldRune = "0"
			result[i].FeeSpentRune = "0"
		}

		result[i].DynamicMultiplierBasisPoints = qs.mgr.gasMgr.CalcOutboundFeeMultiplier(
			ctx,
			cosmos.NewUint(qs.mgr.Keeper().GetConfigInt64(ctx, constants.TargetOutboundFeeSurplusRune)),
			cosmos.NewUint(qs.mgr.Keeper().GetConfigInt64(ctx, constants.MaxOutboundFeeMultiplierBasisPoints)),
			cosmos.NewUint(qs.mgr.Keeper().GetConfigInt64(ctx, constants.MinOutboundFeeMultiplierBasisPoints)),
		).String()
	}

	return &types.QueryOutboundFeesResponse{Fees: result}, nil
}

// ============ Last Block Heights Query ============

func (qs queryServer) queryLastBlockHeights(ctx cosmos.Context, chain string) (*types.QueryLastBlocksResponse, error) {
	var chains common.Chains
	if len(chain) > 0 {
		c, err := common.NewChain(chain)
		if err != nil {
			ctx.Logger().Error("invalid chain", "error", err, "chain", chain)
			return nil, fmt.Errorf("invalid chain: %w", err)
		}
		chains = append(chains, c)
	} else {
		// analyze-ignore(map-iteration)
		for _, c := range common.AllChains {
			if c == common.THORChain {
				continue
			}
			chains = append(chains, c)
		}
	}

	var result []*types.LastBlock
	for _, c := range chains {
		if c == common.THORChain {
			continue
		}
		lastObserve, err := qs.mgr.Keeper().GetLastObserveHeight(ctx, c)
		if err != nil {
			ctx.Logger().Error("fail to get last observe height", "error", err, "chain", c.String())
			continue
		}

		lastHeight, err := qs.mgr.Keeper().GetLastChainHeight(ctx, c)
		if err != nil {
			ctx.Logger().Error("fail to get last block height", "error", err, "chain", c.String())
			continue
		}

		result = append(result, &types.LastBlock{
			Chain:          c.String(),
			LastObservedIn: lastObserve,
			LastSignedOut:  lastHeight,
			Thorchain:      ctx.BlockHeight(),
		})
	}
	return &types.QueryLastBlocksResponse{LastBlocks: result}, nil
}