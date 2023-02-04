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

// LoanRepaymentHandler a handler to process bond
type LoanRepaymentHandler struct {
	mgr Manager
}

// NewLoanRepaymentHandler create new LoanRepaymentHandler
func NewLoanRepaymentHandler(mgr Manager) LoanRepaymentHandler {
	return LoanRepaymentHandler{
		mgr: mgr,
	}
}

// Run execute the handler
func (h LoanRepaymentHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgLoanRepayment)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("receive MsgLoanRepayment",
		"owner", msg.Owner,
		"asset", msg.CollateralAsset,
		"coin", msg.Coin.String(),
		"min_out", msg.MinOut.String(),
	)

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

func (h LoanRepaymentHandler) validate(ctx cosmos.Context, msg MsgLoanRepayment) error {
	version := h.mgr.GetVersion()
	if version.GTE(semver.MustParse("1.106.0")) {
		return h.validateV106(ctx, msg)
	}
	return errBadVersion
}

func (h LoanRepaymentHandler) validateV106(ctx cosmos.Context, msg MsgLoanRepayment) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	pauseLoans := fetchConfigInt64(ctx, h.mgr, constants.PauseLoans)
	if pauseLoans > 0 {
		return fmt.Errorf("loans are currently paused")
	}

	if !h.mgr.Keeper().PoolExist(ctx, msg.CollateralAsset) {
		ctx.Logger().Error("pool does not exist", "asset", msg.CollateralAsset)
		return fmt.Errorf("pool does not exist")
	}

	loan, err := h.mgr.Keeper().GetLoan(ctx, msg.CollateralAsset, msg.Owner)
	if err != nil {
		ctx.Logger().Error("fail to get loan", "error", err)
		return err
	}

	if loan.Debt().IsZero() {
		return fmt.Errorf("loan contains no debt to pay off")
	}

	maturity := fetchConfigInt64(ctx, h.mgr, constants.LoanRepaymentMaturity)
	if loan.LastOpenHeight+maturity > ctx.BlockHeight() {
		return fmt.Errorf("loan repayment is unavailable: loan hasn't reached maturity")
	}

	return nil
}

func (h LoanRepaymentHandler) handle(ctx cosmos.Context, msg MsgLoanRepayment) error {
	version := h.mgr.GetVersion()
	if version.GTE(semver.MustParse("1.106.0")) {
		return h.handleV106(ctx, msg)
	}
	return errBadVersion
}

func (h LoanRepaymentHandler) handleV106(ctx cosmos.Context, msg MsgLoanRepayment) error {
	// if the inbound asset is TOR, then lets repay the loan. If not, lets
	// swap first and try again later
	if msg.Coin.Asset.Equals(common.TOR) {
		return h.repayV106(ctx, msg)
	} else {
		return h.swapV106(ctx, msg)
	}
}

func (h LoanRepaymentHandler) repayV106(ctx cosmos.Context, msg MsgLoanRepayment) error {
	// collect data
	lendAddr, err := h.mgr.Keeper().GetModuleAddress(LendingName)
	if err != nil {
		ctx.Logger().Error("fail to get lending address", "error", err)
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

	redeem := common.GetSafeShare(msg.Coin.Amount, loan.Debt(), loan.Collateral())
	if redeem.IsZero() {
		return fmt.Errorf("redeem cannot be zero")
	}

	// update Loan record
	loan.DebtDown = loan.DebtDown.Add(msg.Coin.Amount)
	loan.CollateralDown = loan.CollateralDown.Add(redeem)
	loan.LastRepayHeight = ctx.BlockHeight()

	// burn TOR coins
	if err := h.mgr.Keeper().SendFromModuleToModule(ctx, LendingName, ModuleName, common.NewCoins(msg.Coin)); err != nil {
		ctx.Logger().Error("fail to move coins during loan repayment", "error", err)
		return err
	} else {
		err := h.mgr.Keeper().BurnFromModule(ctx, ModuleName, msg.Coin)
		if err != nil {
			ctx.Logger().Error("fail to burn coins during loan repayment", "error", err)
			return err
		}
	}

	// Construct a fake TX hash, doesn't seem like i can get the real one
	// from cosmos
	sb := strings.Builder{}
	sb.WriteString("send back collateral")
	sb.WriteString(msg.Owner.String())
	sb.WriteString(msg.CollateralAsset.String())
	sb.WriteString(msg.Coin.String())
	sb.WriteString(msg.Signer.String())
	sb.WriteString(fmt.Sprintf("%d", ctx.BlockHeight()))
	hash := sha256.New()
	_, err = hash.Write([]byte(sb.String()))
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

	fakeGas := common.NewCoin(msg.Coin.Asset, cosmos.OneUint())
	tx := common.NewTx(txID, lendAddr, lendAddr, common.NewCoins(common.NewCoin(msg.CollateralAsset, redeem)), common.Gas{fakeGas}, "noop")
	swapMsg := NewMsgSwap(tx, msg.CollateralAsset, msg.Owner, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, 0, msg.Signer)
	if err := h.mgr.Keeper().SetSwapQueueItem(ctx, *swapMsg, 0); err != nil {
		ctx.Logger().Error("fail to add swap to queue", "error", err)
		return err
	}

	// update kvstore
	h.mgr.Keeper().SetLoan(ctx, loan)
	h.mgr.Keeper().SetTotalCollateral(ctx, msg.CollateralAsset, common.SafeSub(totalCollateral, redeem))

	// emit events and metrics
	evt := NewEventLoanRepayment(redeem, msg.Coin.Amount, msg.CollateralAsset, msg.Owner)
	if err := h.mgr.EventMgr().EmitEvent(ctx, evt); nil != err {
		ctx.Logger().Error("fail to emit loan open event", "error", err)
	}

	return nil
}

func (h LoanRepaymentHandler) swapV106(ctx cosmos.Context, msg MsgLoanRepayment) error {
	lendAddr, err := h.mgr.Keeper().GetModuleAddress(LendingName)
	if err != nil {
		ctx.Logger().Error("fail to get lending address", "error", err)
		return err
	}

	// Construct a fake TX hash, doesn't seem like i can get the real one
	// from cosmos
	sb := strings.Builder{}
	sb.WriteString("swap repayment")
	sb.WriteString(msg.Owner.String())
	sb.WriteString(msg.CollateralAsset.String())
	sb.WriteString(msg.Coin.String())
	sb.WriteString(msg.Signer.String())
	sb.WriteString(fmt.Sprintf("%d", ctx.BlockHeight()))
	hash := sha256.New()
	_, err = hash.Write([]byte(sb.String()))
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

	memo := fmt.Sprintf("loan-:%s:%s", msg.CollateralAsset, msg.Owner)
	fakeGas := common.NewCoin(msg.Coin.Asset, cosmos.OneUint())
	tx := common.NewTx(txID, lendAddr, lendAddr, common.NewCoins(msg.Coin), common.Gas{fakeGas}, memo)
	swapMsg := NewMsgSwap(tx, common.TOR, lendAddr, cosmos.ZeroUint(), lendAddr, cosmos.ZeroUint(), "", "", nil, 0, msg.Signer)
	if err := h.mgr.Keeper().SetSwapQueueItem(ctx, *swapMsg, 0); err != nil {
		ctx.Logger().Error("fail to add swap to queue", "error", err)
		return err
	}

	return nil
}
