package thorchain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
)

// LoanOpenHandler a handler to process bond
type LoanOpenHandler struct {
	mgr Manager
}

// NewLoanOpenHandler create new LoanOpenHandler
func NewLoanOpenHandler(mgr Manager) LoanOpenHandler {
	return LoanOpenHandler{
		mgr: mgr,
	}
}

// Run execute the handler
func (h LoanOpenHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgLoanOpen)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("receive MsgLoanOpen",
		"owner", msg.Owner,
		"col_asset", msg.CollateralAsset,
		"col_amount", msg.CollateralAmount,
		"target_address", msg.TargetAddress,
		"target_asset", msg.TargetAsset)

	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg loan fail validation", "error", err)
		return nil, err
	}

	err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process msg loan", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h LoanOpenHandler) validate(ctx cosmos.Context, msg MsgLoanOpen) error {
	version := h.mgr.GetVersion()
	if version.GTE(semver.MustParse("1.103.0")) {
		return h.validateV103(ctx, msg)
	}
	return errBadVersion
}

func (h LoanOpenHandler) validateV103(ctx cosmos.Context, msg MsgLoanOpen) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	pauseLoans := fetchConfigInt64(ctx, h.mgr, constants.PauseLoans)
	if pauseLoans > 0 {
		return fmt.Errorf("loans are currently paused")
	}

	// Circuit Breaker: check if we're hit the max supply
	supply := h.mgr.Keeper().GetTotalSupply(ctx, common.RuneAsset())
	maxAmt, _ := h.mgr.Keeper().GetMimir(ctx, "MaxRuneSupply")
	if maxAmt > 0 && supply.GTE(cosmos.NewUint(uint64(maxAmt))) {
		return fmt.Errorf("loans are currently paused, due to rune supply cap")
	}

	// ensure collateral pool exists
	if !h.mgr.Keeper().PoolExist(ctx, msg.CollateralAsset) {
		return fmt.Errorf("collateral asset does not have a pool")
	}

	// ensure collateral is a gas asset
	if !msg.CollateralAsset.IsGasAsset() {
		return fmt.Errorf("collateral is not a gas asset")
	}

	// ensure target asset is a pool
	if !h.mgr.Keeper().PoolExist(ctx, msg.TargetAsset) {
		return fmt.Errorf("target asset does not have a pool")
	}

	return nil
}

func (h LoanOpenHandler) handle(ctx cosmos.Context, msg MsgLoanOpen) error {
	version := h.mgr.GetVersion()
	if version.GTE(semver.MustParse("1.103.0")) {
		return h.handleV103(ctx, msg)
	}
	return errBadVersion
}

func (h LoanOpenHandler) handleV103(ctx cosmos.Context, msg MsgLoanOpen) error {
	zero := cosmos.ZeroUint()
	pool, err := h.mgr.Keeper().GetPool(ctx, msg.CollateralAsset)
	if err != nil {
		ctx.Logger().Error("fail to get pool", "error", err)
		return err
	}
	loan, err := h.mgr.Keeper().GetLoan(ctx, msg.CollateralAsset, msg.Owner)
	if err != nil {
		ctx.Logger().Error("fail to get loan", "error", err)
		return err
	}
	totalCollateral, err := h.mgr.Keeper().GetTotalCollateral(ctx, msg.CollateralAsset)
	if err != nil {
		return err
	}

	// get configs
	minCR := fetchConfigInt64(ctx, h.mgr, constants.MinCR)
	maxCR := fetchConfigInt64(ctx, h.mgr, constants.MaxCR)
	multi := fetchConfigInt64(ctx, h.mgr, constants.CRPoolMultiplier)
	enableDerived := fetchConfigInt64(ctx, h.mgr, constants.EnableDerivedAssets)

	// calculate CR
	multiDepth := common.GetUncappedShare(cosmos.NewUint(uint64(multi)), cosmos.NewUint(10_000), pool.BalanceAsset)
	cr := h.getCR(totalCollateral.Add(msg.CollateralAmount), multiDepth, minCR, maxCR)

	price := DollarInRune(ctx, h.mgr).QuoUint64(constants.DollarMulti)
	if price.IsZero() {
		return fmt.Errorf("TOR price cannot be zero")
	}

	collateralValueInRune := pool.AssetValueInRune(msg.CollateralAmount)
	collateralValueInTOR := collateralValueInRune.Mul(price).QuoUint64(1e8)
	debt := collateralValueInTOR.Quo(cr).MulUint64(100)
	ctx.Logger().Info("Loan Details", "collateral", common.NewCoin(msg.CollateralAsset, msg.CollateralAmount), "debt", debt.Uint64(), "rune price", price.Uint64(), "colRune", collateralValueInRune.Uint64(), "colTOR", collateralValueInTOR.Uint64())

	// sanity checks
	if debt.IsZero() {
		return fmt.Errorf("debt cannot be zero")
	}

	// update Loan record
	loan.DebtUp = loan.DebtUp.Add(debt)
	loan.CollateralUp = loan.CollateralUp.Add(msg.CollateralAmount)
	loan.LastOpenHeight = ctx.BlockHeight()

	if msg.TargetAsset.Equals(common.TOR) && enableDerived > 0 {
		toi := TxOutItem{
			Chain:      msg.TargetAsset.GetChain(),
			ToAddress:  msg.TargetAddress,
			Coin:       common.NewCoin(common.TOR, debt),
			ModuleName: ModuleName,
		}
		ok, err := h.mgr.TxOutStore().TryAddTxOutItem(ctx, h.mgr, toi, zero)
		if err != nil {
			return err
		}
		if !ok {
			return errFailAddOutboundTx
		}
	} else {
		// Construct a fake TX hash, doesn't seem like i can get the real one
		// from cosmos
		sb := strings.Builder{}
		sb.WriteString(msg.Owner.String())
		sb.WriteString(msg.CollateralAsset.String())
		sb.WriteString(msg.CollateralAmount.String())
		sb.WriteString(msg.TargetAddress.String())
		sb.WriteString(msg.TargetAsset.String())
		sb.WriteString(msg.Signer.String())
		sb.WriteString(fmt.Sprintf("%d", ctx.BlockHeight()))
		hash := sha256.New()
		_, err := hash.Write([]byte(sb.String()))
		if err != nil {
			return fmt.Errorf("fail to get fictional txID: %w", err)
		}
		str := hex.EncodeToString(hash.Sum(nil))
		txID, err := common.NewTxID(str)
		if err != nil {
			return err
		}
		// ensure TxID does NOT have a collision with another swap, this could
		// happen if the user submits two identical loan requests in the same
		// block
		_, err = h.mgr.Keeper().GetSwapQueueItem(ctx, txID, 0)
		if err == nil { // err should be NOT NIL
			return fmt.Errorf("txn hash conflict")
		}

		torCoin := common.NewCoin(common.TOR, debt)

		if err := h.mgr.Keeper().MintToModule(ctx, ModuleName, torCoin); err != nil {
			return fmt.Errorf("fail to mint loan tor debt: %w", err)
		}
		if err := h.mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, LendingName, common.NewCoins(torCoin)); err != nil {
			return fmt.Errorf("fail to send TOR vault funds: %w", err)
		}

		lendingAddr, err := h.mgr.Keeper().GetModuleAddress(LendingName)
		if err != nil {
			ctx.Logger().Error("fail to get lending address", "error", err)
			return err
		}

		tx := common.NewTx(txID, lendingAddr, common.NoAddress, common.NewCoins(torCoin), nil, "Open Loan Swap")
		// we do NOT pass affiliate info here as it was already taken out on the swap of the collateral to derived asset
		swapMsg := NewMsgSwap(tx, msg.TargetAsset, msg.TargetAddress, zero, common.NoAddress, zero, msg.Aggregator, msg.AggregatorTargetAddress, &msg.AggregatorTargetLimit, 0, msg.Signer)
		if err := h.mgr.Keeper().SetSwapQueueItem(ctx, *swapMsg, 0); err != nil {
			ctx.Logger().Error("fail to add swap to queue", "error", err)
			return err
		}
	}

	// update kvstore
	h.mgr.Keeper().SetLoan(ctx, loan)
	h.mgr.Keeper().SetTotalCollateral(ctx, msg.CollateralAsset, totalCollateral.Add(msg.CollateralAmount))

	// emit events and metrics
	evt := NewEventLoanOpen(msg.CollateralAmount, cr, debt, msg.CollateralAsset, msg.TargetAsset, msg.Owner)
	if err := h.mgr.EventMgr().EmitEvent(ctx, evt); nil != err {
		ctx.Logger().Error("fail to emit loan open event", "error", err)
	}

	return nil
}

func (h LoanOpenHandler) getCR(a, b cosmos.Uint, minCR, maxCR int64) cosmos.Uint {
	// (maxCR * 100 -minCR * 100) / (b / a) + minCR * 100
	// We apply a 100 multiplier to get get two decimals points in the math (ie return 100.77 instead of 100)
	// NOTE: a should include the collateral currently being deposited
	crCalc := cosmos.NewUint(uint64(maxCR*100 - minCR*100))
	cr := common.GetUncappedShare(a, b, crCalc)
	return cr.AddUint64(uint64(minCR * 100))
}
