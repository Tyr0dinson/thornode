package app

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/types"
)

// THORChainFeeDecorator ensure the sender has enough RUNE balance to pay for transaction fee
type THORChainFeeDecorator struct {
	ak         ante.AccountKeeper
	bankKeeper bankkeeper.ViewKeeper
}

// NewTHORChainFeeDecorator create a new instance of THORChainFeeDecorator
func NewTHORChainFeeDecorator(ak ante.AccountKeeper, bk bankkeeper.ViewKeeper) THORChainFeeDecorator {
	return THORChainFeeDecorator{
		ak:         ak,
		bankKeeper: bk,
	}
}

func (fd THORChainFeeDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (newCtx sdk.Context, err error) {
	// Hard coded here , as this one doesn't have access to constants
	runeFee, err := common.NewCoin(common.RuneNative, cosmos.NewUint(2_000000)).Native()
	if err != nil {
		return ctx, fmt.Errorf("fail to create native RUNE fee,err:%w", err)
	}
	for _, msg := range tx.GetMsgs() {
		switch msg.(type) {
		case *types.MsgSend, *types.MsgDeposit:
			for _, signer := range msg.GetSigners() {
				if !fd.bankKeeper.HasBalance(ctx, signer, runeFee) {
					return ctx, sdkerrors.Wrapf(sdkerrors.ErrInsufficientFee, "account: %s doesn't have enough to pay for RUNE transaction fee", signer.String())
				}
			}
		}
	}

	return next(ctx, tx, simulate)
}
