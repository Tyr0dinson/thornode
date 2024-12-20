package thorchain

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

func (h SwapHandler) handleV3_0_0(ctx cosmos.Context, msg MsgSwap) (*cosmos.Result, error) {
	// test that the network we are running matches the destination network
	// Don't change msg.Destination here; this line was introduced to avoid people from swapping mainnet asset,
	// but using mocknet address.
	if !common.CurrentChainNetwork.SoftEquals(msg.Destination.GetNetwork(msg.Destination.GetChain())) {
		return nil, fmt.Errorf("address(%s) is not same network", msg.Destination)
	}

	synthVirtualDepthMult, err := h.mgr.Keeper().GetMimir(ctx, constants.VirtualMultSynthsBasisPoints.String())
	if synthVirtualDepthMult < 1 || err != nil {
		synthVirtualDepthMult = h.mgr.GetConstants().GetInt64Value(constants.VirtualMultSynthsBasisPoints)
	}

	dexAgg := ""
	dexAggTargetAsset := ""
	if len(msg.Aggregator) > 0 {
		dexAgg, err = FetchDexAggregator(h.mgr.GetVersion(), msg.TargetAsset.Chain, msg.Aggregator)
		if err != nil {
			return nil, err
		}
	}
	dexAggTargetAsset = msg.AggregatorTargetAddress

	swapper, err := GetSwapper(h.mgr.Keeper().GetVersion())
	if err != nil {
		return nil, err
	}

	swp := msg.GetStreamingSwap()
	if msg.IsStreaming() {
		if h.mgr.Keeper().StreamingSwapExists(ctx, msg.Tx.ID) {
			swp, err = h.mgr.Keeper().GetStreamingSwap(ctx, msg.Tx.ID)
			if err != nil {
				ctx.Logger().Error("fail to fetch streaming swap", "error", err)
				return nil, err
			}
		}

		// for first swap only, override interval and quantity (if needed)
		if swp.Count == 0 {
			// ensure interval is never larger than max length, override if so
			maxLength := h.mgr.Keeper().GetConfigInt64(ctx, constants.StreamingSwapMaxLength)
			if uint64(maxLength) < swp.Interval {
				swp.Interval = uint64(maxLength)
			}

			sourceAsset := msg.Tx.Coins[0].Asset
			targetAsset := msg.TargetAsset
			maxSwapQuantity, err := getMaxSwapQuantity(ctx, h.mgr, sourceAsset, targetAsset, swp)
			if err != nil {
				return nil, err
			}
			if swp.Quantity == 0 || swp.Quantity > maxSwapQuantity {
				swp.Quantity = maxSwapQuantity
			}
		}
		h.mgr.Keeper().SetStreamingSwap(ctx, swp)
		// hijack the inbound amount
		// NOTE: its okay if the amount is zero. The swap will fail as it
		// should, which will cause the swap queue manager later to send out
		// the In/Out amounts accordingly
		msg.Tx.Coins[0].Amount, msg.TradeTarget = swp.NextSize()
	}

	emit, _, swapErr := swapper.Swap(
		ctx,
		h.mgr.Keeper(),
		msg.Tx,
		msg.TargetAsset,
		msg.Destination,
		msg.TradeTarget,
		dexAgg,
		dexAggTargetAsset,
		msg.AggregatorTargetLimit,
		swp,
		synthVirtualDepthMult,
		h.mgr)
	if swapErr != nil {
		return nil, swapErr
	}

	// Check if swap is to AffiliateCollector Module, if so, add the accrued RUNE for the affiliate
	affColAddress, err := h.mgr.Keeper().GetModuleAddress(AffiliateCollectorName)
	if err != nil {
		ctx.Logger().Error("failed to retrieve AffiliateCollector module address", "error", err)
	}

	var affThorname *THORName
	var affCol AffiliateFeeCollector

	mem, parseMemoErr := ParseMemoWithTHORNames(ctx, h.mgr.Keeper(), msg.Tx.Memo)
	if parseMemoErr == nil {
		affThorname = mem.GetAffiliateTHORName()
	}

	if affThorname != nil && msg.Destination.Equals(affColAddress) && !msg.AffiliateAddress.IsEmpty() && msg.TargetAsset.IsRune() {
		// Add accrued RUNE for this affiliate
		affCol, err = h.mgr.Keeper().GetAffiliateCollector(ctx, affThorname.Owner)
		if err != nil {
			ctx.Logger().Error("failed to retrieve AffiliateCollector for thorname owner", "address", affThorname.Owner.String(), "error", err)
		} else {
			// The TargetAsset has already been established to be RUNE.
			transactionFee, err := h.mgr.GasMgr().GetAssetOutboundFee(ctx, common.RuneAsset(), true)
			if err != nil {
				ctx.Logger().Error("failed to get transaction fee", "error", err)
			} else {
				addRuneAmt := common.SafeSub(emit, transactionFee)
				affCol.RuneAmount = affCol.RuneAmount.Add(addRuneAmt)
				h.mgr.Keeper().SetAffiliateCollector(ctx, affCol)
			}
		}
	}

	// Check if swap to a synth would cause synth supply to exceed
	// MaxSynthPerPoolDepth cap
	// Ignore caps when the swap is streaming (its checked at the start of the
	// stream, not during)
	if msg.TargetAsset.IsSyntheticAsset() && !msg.IsStreaming() {
		err = isSynthMintPaused(ctx, h.mgr, msg.TargetAsset, emit)
		if err != nil {
			return nil, err
		}
	}

	if msg.IsStreaming() {
		// only increment In/Out if we have a successful swap
		swp.In = swp.In.Add(msg.Tx.Coins[0].Amount)
		swp.Out = swp.Out.Add(emit)
		h.mgr.Keeper().SetStreamingSwap(ctx, swp)
		if !swp.IsLastSwap() {
			// exit early so we don't execute follow-on handlers mid streaming swap. if this
			// is the last swap execute the follow-on handlers as swap count is incremented in
			// the swap queue manager
			return &cosmos.Result{}, nil
		}
		emit = swp.Out
	}

	// this is a preferred asset swap, so return early since there is no need to call any
	// downstream handlers
	if strings.HasPrefix(msg.Tx.Memo, PreferredAssetSwapMemoPrefix) && msg.Tx.FromAddress.Equals(affColAddress) {
		return &cosmos.Result{}, nil
	}

	if parseMemoErr != nil {
		ctx.Logger().Error("swap handler failed to parse memo", "memo", msg.Tx.Memo, "error", parseMemoErr)
		return nil, err
	}
	switch mem.GetType() {
	case TxAdd:
		m, ok := mem.(AddLiquidityMemo)
		if !ok {
			return nil, fmt.Errorf("fail to cast add liquidity memo")
		}
		m.Asset = fuzzyAssetMatch(ctx, h.mgr.Keeper(), m.Asset)
		msg.Tx.Coins = common.NewCoins(common.NewCoin(m.Asset, emit))
		obTx := ObservedTx{Tx: msg.Tx}
		msg, err := getMsgAddLiquidityFromMemo(ctx, m, obTx, msg.Signer)
		if err != nil {
			return nil, err
		}
		handler := NewAddLiquidityHandler(h.mgr)
		_, err = handler.Run(ctx, msg)
		if err != nil {
			ctx.Logger().Error("swap handler failed to add liquidity", "error", err)
			return nil, err
		}
	case TxLoanOpen:
		m, ok := mem.(LoanOpenMemo)
		if !ok {
			return nil, fmt.Errorf("fail to cast loan open memo")
		}
		m.Asset = fuzzyAssetMatch(ctx, h.mgr.Keeper(), m.Asset)
		msg.Tx.Coins = common.NewCoins(common.NewCoin(
			msg.TargetAsset, emit,
		))

		ctx = ctx.WithValue(constants.CtxLoanTxID, msg.Tx.ID)

		obTx := ObservedTx{Tx: msg.Tx}
		msg, err := getMsgLoanOpenFromMemo(ctx, h.mgr.Keeper(), m, obTx, msg.Signer, msg.Tx.ID)
		if err != nil {
			return nil, err
		}
		openLoanHandler := NewLoanOpenHandler(h.mgr)

		_, err = openLoanHandler.Run(ctx, msg) // fire and forget
		if err != nil {
			ctx.Logger().Error("swap handler failed to open loan", "error", err)
			return nil, err
		}
	case TxLoanRepayment:
		m, ok := mem.(LoanRepaymentMemo)
		if !ok {
			return nil, fmt.Errorf("fail to cast loan repayment memo")
		}
		m.Asset = fuzzyAssetMatch(ctx, h.mgr.Keeper(), m.Asset)

		ctx = ctx.WithValue(constants.CtxLoanTxID, msg.Tx.ID)

		msg, err := getMsgLoanRepaymentFromMemo(m, msg.Tx.FromAddress, common.NewCoin(common.TOR, emit), msg.Signer, msg.Tx.ID)
		if err != nil {
			return nil, err
		}
		repayLoanHandler := NewLoanRepaymentHandler(h.mgr)
		_, err = repayLoanHandler.Run(ctx, msg) // fire and forget
		if err != nil {
			ctx.Logger().Error("swap handler failed to repay loan", "error", err)
			return nil, err
		}
	}
	return &cosmos.Result{}, nil
}
