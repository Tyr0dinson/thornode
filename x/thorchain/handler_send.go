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

	// disallow sends to modules, they should only be interacted with via deposit messages
	if IsModuleAccAddress(mgr.Keeper(), msg.ToAddress) {
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

	fmt.Println("In MsgSendHandleV116!")
	tck := k.(*thorchainkeeperv1.KVStore)
	fmt.Println("Unwrapped keeper in MsgSendHandleV116!")

	txDecoder := tck.GetTxDecoder()
	msgSendTx, err := txDecoder(ctx.TxBytes())
	if err != nil {
		return nil, err
	}

	fmt.Println("Unmarshalled TX in MsgSendHandleV116!")
	thorModuleAddr := k.GetModuleAccAddress(ModuleName)

	memo := ""
	txMemo, ok := msgSendTx.(sdk.TxWithMemo)
	if ok {
		fmt.Println("MsgSendHandleV116 is TxWithMemo:", memo)
		memo = txMemo.GetMemo()
	}

	fmt.Println("MsgSendHandleV116 Tx memo:", memo)

	// This should be treated as a Deposit
	if msg.ToAddress.Equals(thorModuleAddr) {
		fmt.Println("This should be a MsgDeposit!")
	} else {
		fmt.Println("This should be a MsgSend!")
	}

	err = mgr.Keeper().SendCoins(ctx, msg.FromAddress, msg.ToAddress, msg.Amount)
	if err != nil {
		return nil, err
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
