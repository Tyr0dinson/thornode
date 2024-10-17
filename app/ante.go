package app

import (
	"errors"

	corestoretypes "cosmossdk.io/core/store"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"

	"gitlab.com/thorchain/thornode/x/thorchain"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"

	denomtypes "gitlab.com/thorchain/thornode/x/denom/types"
)

// HandlerOptions extend the SDK's AnteHandler options
type HandlerOptions struct {
	ante.HandlerOptions

	WasmConfig *wasmtypes.WasmConfig
	WasmKeeper *wasmkeeper.Keeper

	TXCounterStoreService corestoretypes.KVStoreService

	BypassMinFeeMsgTypes []string

	THORChainKeeper keeper.Keeper
}

// NewAnteHandler constructor
func NewAnteHandler(options HandlerOptions) (sdk.AnteHandler, error) {
	if options.AccountKeeper == nil {
		return nil, errors.New("account keeper is required for ante builder")
	}
	if options.BankKeeper == nil {
		return nil, errors.New("bank keeper is required for ante builder")
	}
	if options.SignModeHandler == nil {
		return nil, errors.New("sign mode handler is required for ante builder")
	}
	if options.WasmConfig == nil {
		return nil, errors.New("wasm config is required for ante builder")
	}
	if options.THORChainKeeper == nil {
		return nil, errors.New("thorchain keeper is required for ante builder")
	}
	// if options.TXCounterStoreService == nil {
	// 	return nil, errors.New("wasm store service is required for ante builder")
	// }

	blockedMsgDecorator := NewBlockMsgDecorator(
		// Block x/denom msg types from being called by an EOA. These should be called only by cosmwasm contracts
		sdk.MsgTypeURL(&denomtypes.MsgCreateDenom{}),
		sdk.MsgTypeURL(&denomtypes.MsgBurnTokens{}),
		sdk.MsgTypeURL(&denomtypes.MsgMintTokens{}),
		sdk.MsgTypeURL(&denomtypes.MsgChangeDenomAdmin{}),
	)

	anteDecorators := []sdk.AnteDecorator{
		ante.NewSetUpContextDecorator(), // outermost AnteDecorator. SetUpContext must be called first

		wasmkeeper.NewLimitSimulationGasDecorator(options.WasmConfig.SimulationGasLimit), // after setup context to enforce limits early
		wasmkeeper.NewCountTXDecorator(options.TXCounterStoreService),
		wasmkeeper.NewGasRegisterDecorator(options.WasmKeeper.GetGasRegister()),

		// replace gas meter immediately after setting up ctx
		thorchain.NewInfiniteGasDecorator(options.THORChainKeeper),
		ante.NewExtensionOptionsDecorator(options.ExtensionOptionChecker),
		ante.NewValidateBasicDecorator(),
		ante.NewTxTimeoutHeightDecorator(),
		ante.NewValidateMemoDecorator(options.AccountKeeper),
		ante.NewConsumeGasForTxSizeDecorator(options.AccountKeeper),
		ante.NewDeductFeeDecorator(options.AccountKeeper, options.BankKeeper, options.FeegrantKeeper, options.TxFeeChecker),

		// run thorchain-specific msg antes
		thorchain.NewAnteDecorator(options.THORChainKeeper),
		blockedMsgDecorator,

		ante.NewSetPubKeyDecorator(options.AccountKeeper), // SetPubKeyDecorator must be called before all signature verification decorators
		ante.NewValidateSigCountDecorator(options.AccountKeeper),
		ante.NewSigGasConsumeDecorator(options.AccountKeeper, options.SigGasConsumer),
		ante.NewSigVerificationDecorator(options.AccountKeeper, options.SignModeHandler),
		ante.NewIncrementSequenceDecorator(options.AccountKeeper),
	}

	return sdk.ChainAnteDecorators(anteDecorators...), nil
}
