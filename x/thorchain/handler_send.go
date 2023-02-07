package thorchain

import (
	"errors"
	"fmt"

	"github.com/cosmos/cosmos-sdk/x/gov/types"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
)

// NewSendHandler create a new instance of SendHandler
func NewSendHandler(mgr Manager) BaseHandler[*MsgSend] {
	return BaseHandler[*MsgSend]{
		mgr:    mgr,
		logger: MsgSend_logger,
		validators: NewValidators[*MsgSend]().
			Register("1.87.0", MsgSend_validateV87).
			Register("0.1.0", MsgSend_validateV1),
		handlers: NewHandlers[*MsgSend]().
			Register("0.1.0", MsgSend_handleV1),
	}
}

func MsgSend_validateV87(ctx cosmos.Context, mgr Manager, msg *MsgSend) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// disallow sends to modules, they should only be interacted with via deposit messages
	if msg.ToAddress.Equals(mgr.Keeper().GetModuleAccAddress(AsgardName)) ||
		msg.ToAddress.Equals(mgr.Keeper().GetModuleAccAddress(BondName)) ||
		msg.ToAddress.Equals(mgr.Keeper().GetModuleAccAddress(ReserveName)) ||
		msg.ToAddress.Equals(mgr.Keeper().GetModuleAccAddress(ModuleName)) {
		return errors.New("cannot use MsgSend for Module transactions, use MsgDeposit instead")
	}

	return nil
}

func MsgSend_logger(ctx cosmos.Context, msg *MsgSend) {
	ctx.Logger().Info("receive MsgSend", "from", msg.FromAddress, "to", msg.ToAddress, "coins", msg.Amount)
}

func MsgSend_handleV1(ctx cosmos.Context, mgr Manager, msg *MsgSend) (*cosmos.Result, error) {
	haltHeight, err := mgr.Keeper().GetMimir(ctx, "HaltTHORChain")
	if err != nil {
		return nil, fmt.Errorf("failed to get mimir setting: %w", err)
	}
	if haltHeight > 0 && ctx.BlockHeight() > haltHeight {
		return nil, fmt.Errorf("mimir has halted THORChain transactions")
	}

	nativeTxFee, err := mgr.Keeper().GetMimir(ctx, constants.NativeTransactionFee.String())
	if err != nil || nativeTxFee < 0 {
		nativeTxFee = mgr.GetConstants().GetInt64Value(constants.NativeTransactionFee)
	}

	gas := common.NewCoin(common.RuneNative, cosmos.NewUint(uint64(nativeTxFee)))
	gasFee, err := gas.Native()
	if err != nil {
		return nil, ErrInternal(err, "fail to get gas fee")
	}

	totalCoins := cosmos.NewCoins(gasFee).Add(msg.Amount...)
	if !mgr.Keeper().HasCoins(ctx, msg.FromAddress, totalCoins) {
		return nil, cosmos.ErrInsufficientCoins(err, "insufficient funds")
	}

	// send gas to reserve
	sdkErr := mgr.Keeper().SendFromAccountToModule(ctx, msg.FromAddress, ReserveName, common.NewCoins(gas))
	if sdkErr != nil {
		return nil, fmt.Errorf("unable to send gas to reserve: %w", sdkErr)
	}

	sdkErr = mgr.Keeper().SendCoins(ctx, msg.FromAddress, msg.ToAddress, msg.Amount)
	if sdkErr != nil {
		return nil, sdkErr
	}

	ctx.EventManager().EmitEvent(
		cosmos.NewEvent(
			cosmos.EventTypeMessage,
			cosmos.NewAttribute(cosmos.AttributeKeyModule, types.AttributeValueCategory),
		),
	)

	return &cosmos.Result{}, nil
}
