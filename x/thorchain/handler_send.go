package thorchain

import (
	"errors"
	"fmt"

	"github.com/blang/semver"
	sdk "github.com/cosmos/cosmos-sdk/types"
	bank "github.com/cosmos/cosmos-sdk/x/bank/types"
	thorchainkeeperv1 "gitlab.com/thorchain/thornode/x/thorchain/keeper/v1"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

// NewSendHandler create a new instance of SendHandler
func NewSendHandler(mgr Manager) BaseHandler[*MsgSend] {
	return BaseHandler[*MsgSend]{
		mgr:    mgr,
		logger: MsgSendLogger,
		validators: NewValidators[*MsgSend]().
			Register("1.130.0", MsgSendValidateV130),
		handlers: NewHandlers[*MsgSend]().
			Register("1.116.0", MsgSendHandleV116),
	}
}

func MsgSendValidateV130(ctx cosmos.Context, mgr Manager, msg *MsgSend) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	k := mgr.Keeper()
	isThorModAddr := msg.ToAddress.Equals(k.GetModuleAccAddress(ModuleName))

	// disallow sends to modules, they should only be interacted with via deposit messages.
	// the exception is a MsgSend with memo-based MsgDeposit is allowed to the thor module address
	if !isThorModAddr && IsModuleAccAddress(k, msg.ToAddress) {
		return fmt.Errorf("cannot use MsgSend for Module transactions, use MsgDeposit instead")
	}

	// TODO on hard fork move to ValidateBasic
	// send only allowed with one coin
	if len(msg.Amount) != 1 {
		return errors.New("only one coin is allowed")
	}

	return nil
}

func MsgSendLogger(ctx cosmos.Context, msg *MsgSend) {
	ctx.Logger().Info("receive MsgSend", "from", msg.FromAddress, "to", msg.ToAddress, "coins", msg.Amount)
}

func MsgSendHandleV116(ctx cosmos.Context, mgr Manager, msg *MsgSend) (*cosmos.Result, error) {
	k := mgr.Keeper()

	if k.IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgSend while THORChain is halted")
	}

	tck, ok := k.(*thorchainkeeperv1.KVStore)
	if !ok {
		return nil, fmt.Errorf("keeper error - handler MsgSend")
	}

	txDecoder := tck.GetTxDecoder()
	msgSendOrDepositTx, err := txDecoder(ctx.TxBytes())
	if err != nil {
		return nil, err
	}

	thorModuleAddr := k.GetModuleAccAddress(ModuleName)

	// MsgSend to the thorchain module address is treated as a MsgDeposit for client compatibility reasons.
	// In this case, the memo will be used like in any other MsgDeposit.
	if msg.ToAddress.Equals(thorModuleAddr) {
		memo := ""
		txMemo, ok := msgSendOrDepositTx.(sdk.TxWithMemo)
		if ok {
			memo = txMemo.GetMemo()
		}

		coinSdk := msg.Amount[0]
		// TODO: Is this the correct logic for converting sdk Coin to common.Asset?
		thorAssetDenom, err := common.NewAsset(coinSdk.Denom)
		if err != nil {
			return nil, err
		}

		coin := common.NewCoin(thorAssetDenom, sdk.NewUintFromBigInt(coinSdk.Amount.BigInt()))
		msgDeposit := NewMsgDeposit(common.Coins{coin}, memo, msg.GetSigners()[0])
		externalHandler := tck.GetExternalHandler()
		_, err = externalHandler(ctx, msgDeposit)
		if err != nil {
			return nil, err
		}
	} else {
		err = mgr.Keeper().SendCoins(ctx, msg.FromAddress, msg.ToAddress, msg.Amount)
		if err != nil {
			return nil, err
		}
	}

	return &cosmos.Result{}, nil
}

// SendAnteHandler called by the ante handler to gate mempool entry
// and also during deliver. Store changes will persist if this function
// succeeds, regardless of the success of the transaction.
func SendAnteHandler(ctx cosmos.Context, v semver.Version, k keeper.Keeper, msg MsgSend) error {
	return k.DeductNativeTxFeeFromAccount(ctx, msg.GetSigners()[0])
}

// TODO probably belongs elsewhere
func BankSendAnteHandler(ctx cosmos.Context, v semver.Version, k keeper.Keeper, msg bank.MsgSend) error {
	return k.DeductNativeTxFeeFromAccount(ctx, msg.GetSigners()[0])
}
