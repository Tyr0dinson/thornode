package thorchain

import (
	"errors"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	se "github.com/cosmos/cosmos-sdk/types/errors"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/constants"
)

// THORChain error code start at 101
const (
	// CodeBadVersion error code for bad version
	CodeBadVersion            uint32 = 101
	CodeInvalidMessage        uint32 = 102
	CodeConstantsNotAvailable uint32 = 103
	CodeInvalidVault          uint32 = 104
	CodeInvalidMemo           uint32 = 105
	CodeValidationError       uint32 = 106
	CodeInvalidPoolStatus     uint32 = 107

	CodeSwapFail                 uint32 = 108
	CodeSwapFailTradeTarget      uint32 = 109
	CodeSwapFailNotEnoughFee     uint32 = 110
	CodeSwapFailZeroEmitAsset    uint32 = 111
	CodeSwapFailPoolNotExist     uint32 = 112
	CodeSwapFailInvalidAmount    uint32 = 113
	CodeSwapFailInvalidBalance   uint32 = 114
	CodeSwapFailNotEnoughBalance uint32 = 115

	CodeStakeFailValidation    uint32 = 120
	CodeFailGetStaker          uint32 = 122
	CodeStakeMismatchAssetAddr uint32 = 123
	CodeStakeInvalidPoolAsset  uint32 = 124
	CodeStakeRUNEOverLimit     uint32 = 125
	CodeStakeRUNEMoreThanBond  uint32 = 126

	CodeUnstakeFailValidation uint32 = 130
	CodeFailAddOutboundTx     uint32 = 131
	CodeFailSaveEvent         uint32 = 132
	CodeStakerNotExist        uint32 = 133
	CodeNoStakeUnitLeft       uint32 = 135
	CodeUnstakeWithin24Hours  uint32 = 136
	CodeUnstakeFail           uint32 = 137
	CodeEmptyChain            uint32 = 138
	CodeFailEventManager      uint32 = 139
)

var (
	notAuthorized          = fmt.Errorf("not authorized")
	errInvalidVersion      = fmt.Errorf("bad version")
	errBadVersion          = se.Register(DefaultCodespace, CodeBadVersion, errInvalidVersion.Error())
	errInvalidMessage      = se.Register(DefaultCodespace, CodeInvalidMessage, "invalid message")
	errConstNotAvailable   = se.Register(DefaultCodespace, CodeConstantsNotAvailable, "constant values not available")
	errFailGetEventManager = se.Register(DefaultCodespace, CodeFailEventManager, "fail to get event manager")
	errInvalidMemo         = se.Register(DefaultCodespace, CodeInvalidMemo, "invalid memo")
)

// NewExternalHandler returns a handler for "thorchain" type messages.
func NewExternalHandler(keeper Keeper,
	versionedTxOutStore VersionedTxOutStore,
	validatorMgr VersionedValidatorManager,
	versionedVaultManager VersionedVaultManager,
	versionedObserverManager VersionedObserverManager,
	versionedGasMgr VersionedGasManager,
	versionedEventManager VersionedEventManager) sdk.Handler {
	handlerMap := getHandlerMapping(keeper, versionedTxOutStore, validatorMgr, versionedVaultManager, versionedObserverManager, versionedGasMgr, versionedEventManager)

	return func(ctx sdk.Context, msg sdk.Msg) (*sdk.Result, error) {
		ctx = ctx.WithEventManager(sdk.NewEventManager())
		version := keeper.GetLowestActiveVersion(ctx)
		constantValues := constants.GetConstantValues(version)
		if constantValues == nil {
			return nil, errConstNotAvailable
		}
		h, ok := handlerMap[msg.Type()]
		if !ok {
			errMsg := fmt.Sprintf("Unrecognized thorchain Msg type: %v", msg.Type())
			return nil, common.ErrUnknownRequest(errMsg)
		}
		result, err := h.Run(ctx, msg, version, constantValues)
		if err != nil {
			return nil, err
		}
		if len(ctx.EventManager().Events()) > 0 {
			result.Events = result.Events.AppendEvents(ctx.EventManager().Events())
		}
		return result, nil
	}
}

func getHandlerMapping(keeper Keeper,
	versionedTxOutStore VersionedTxOutStore,
	validatorMgr VersionedValidatorManager,
	versionedVaultManager VersionedVaultManager,
	versionedObserverManager VersionedObserverManager,
	versionedGasMgr VersionedGasManager,
	versionedEventManager VersionedEventManager) map[string]MsgHandler {
	// New arch handlers
	m := make(map[string]MsgHandler)
	m[MsgTssPool{}.Type()] = NewTssHandler(keeper, versionedVaultManager, versionedEventManager)
	m[MsgSetNodeKeys{}.Type()] = NewSetNodeKeysHandler(keeper)
	m[MsgSetVersion{}.Type()] = NewVersionHandler(keeper)
	m[MsgSetIPAddress{}.Type()] = NewIPAddressHandler(keeper)
	m[MsgNativeTx{}.Type()] = NewNativeTxHandler(keeper, versionedObserverManager, versionedTxOutStore, validatorMgr, versionedVaultManager, versionedGasMgr, versionedEventManager)
	m[MsgObservedTxIn{}.Type()] = NewObservedTxInHandler(keeper, versionedObserverManager, versionedTxOutStore, validatorMgr, versionedVaultManager, versionedGasMgr, versionedEventManager)
	m[MsgObservedTxOut{}.Type()] = NewObservedTxOutHandler(keeper, versionedObserverManager, versionedTxOutStore, validatorMgr, versionedVaultManager, versionedGasMgr, versionedEventManager)
	m[MsgTssKeysignFail{}.Type()] = NewTssKeysignHandler(keeper, versionedEventManager)
	m[MsgErrataTx{}.Type()] = NewErrataTxHandler(keeper, versionedEventManager)
	m[MsgSend{}.Type()] = NewSendHandler(keeper)
	m[MsgMimir{}.Type()] = NewMimirHandler(keeper)
	return m
}

// NewInternalHandler returns a handler for "thorchain" internal type messages.
func NewInternalHandler(keeper Keeper,
	versionedTxOutStore VersionedTxOutStore,
	validatorMgr VersionedValidatorManager,
	versionedVaultManager VersionedVaultManager,
	versionedObserverManager VersionedObserverManager,
	versionedGasMgr VersionedGasManager,
	versionedEventManager VersionedEventManager) sdk.Handler {
	handlerMap := getInternalHandlerMapping(keeper, versionedTxOutStore, validatorMgr, versionedVaultManager, versionedObserverManager, versionedGasMgr, versionedEventManager)

	return func(ctx sdk.Context, msg sdk.Msg) (*sdk.Result, error) {
		version := keeper.GetLowestActiveVersion(ctx)
		constantValues := constants.GetConstantValues(version)
		if constantValues == nil {
			return nil, errConstNotAvailable
		}
		h, ok := handlerMap[msg.Type()]
		if !ok {
			errMsg := fmt.Sprintf("Unrecognized thorchain Msg type: %v", msg.Type())
			return nil, common.ErrUnknownRequest(errMsg)
		}
		return h.Run(ctx, msg, version, constantValues)
	}
}

func getInternalHandlerMapping(keeper Keeper,
	versionedTxOutStore VersionedTxOutStore,
	validatorMgr VersionedValidatorManager,
	versionedVaultManager VersionedVaultManager,
	versionedObserverManager VersionedObserverManager,
	versionedGasMgr VersionedGasManager,
	versionedEventManager VersionedEventManager) map[string]MsgHandler {
	// New arch handlers
	m := make(map[string]MsgHandler)
	m[MsgOutboundTx{}.Type()] = NewOutboundTxHandler(keeper, versionedEventManager)
	m[MsgYggdrasil{}.Type()] = NewYggdrasilHandler(keeper, versionedTxOutStore, validatorMgr, versionedEventManager)
	m[MsgSwap{}.Type()] = NewSwapHandler(keeper, versionedTxOutStore, versionedEventManager)
	m[MsgReserveContributor{}.Type()] = NewReserveContributorHandler(keeper, versionedEventManager)
	m[MsgBond{}.Type()] = NewBondHandler(keeper, versionedEventManager)
	m[MsgLeave{}.Type()] = NewLeaveHandler(keeper, validatorMgr, versionedTxOutStore, versionedEventManager)
	m[MsgAdd{}.Type()] = NewAddHandler(keeper, versionedEventManager)
	m[MsgSetUnStake{}.Type()] = NewUnstakeHandler(keeper, versionedTxOutStore, versionedEventManager)
	m[MsgSetStakeData{}.Type()] = NewStakeHandler(keeper, versionedEventManager)
	m[MsgRefundTx{}.Type()] = NewRefundHandler(keeper, versionedEventManager)
	m[MsgMigrate{}.Type()] = NewMigrateHandler(keeper, versionedEventManager)
	m[MsgRagnarok{}.Type()] = NewRagnarokHandler(keeper, versionedEventManager)
	m[MsgSwitch{}.Type()] = NewSwitchHandler(keeper, versionedTxOutStore)
	return m
}

func fetchMemo(ctx sdk.Context, constAccessor constants.ConstantValues, keeper Keeper, tx common.Tx) string {
	if len(tx.Memo) > 0 {
		return tx.Memo
	}

	var memo string
	// attempt to pull memo from tx marker
	hash := tx.Hash()
	marks, err := keeper.ListTxMarker(ctx, hash)
	if err != nil {
		ctx.Logger().Error("fail to get tx marker", "error", err)
	}
	if len(marks) > 0 {
		// filter out expired tx markers
		period := constAccessor.GetInt64Value(constants.SigningTransactionPeriod) * 3
		marks = marks.FilterByMinHeight(ctx.BlockHeight() - period)

		// if we still have a marker, add the memo
		if len(marks) > 0 {
			var mark TxMarker
			mark, marks = marks.Pop()
			memo = mark.Memo
		}

		// update our marker list
		if err := keeper.SetTxMarkers(ctx, hash, marks); err != nil {
			ctx.Logger().Error("fail to set tx markers", "error", err)
		}
	}
	return memo
}

func processOneTxIn(ctx sdk.Context, keeper Keeper, tx ObservedTx, signer sdk.AccAddress) (sdk.Msg, error) {
	if len(tx.Tx.Coins) == 0 {
		return nil, common.ErrUnknownRequest("no coin found")
	}

	memo, err := ParseMemo(tx.Tx.Memo)
	if err != nil {
		ctx.Logger().Error("fail to parse memo", "error", err)
		return nil, se.Wrapf(errInvalidMemo, "fail to parse memo: %s", err)
	}
	// THORNode should not have one tx across chain, if it is cross chain it should be separate tx
	var newMsg sdk.Msg
	// interpret the memo and initialize a corresponding msg event
	switch m := memo.(type) {
	case StakeMemo:
		newMsg, err = getMsgStakeFromMemo(ctx, m, tx, signer)
		if err != nil {
			return nil, se.Wrapf(errInvalidMemo, "invalid stake memo: %s", err)
		}

	case UnstakeMemo:
		newMsg, err = getMsgUnstakeFromMemo(m, tx, signer)
		if err != nil {
			return nil, se.Wrapf(errInvalidMemo, "invalid withdraw memo: %s", err)
		}
	case SwapMemo:
		newMsg, err = getMsgSwapFromMemo(m, tx, signer)
		if err != nil {
			return nil, se.Wrapf(errInvalidMemo, "invalid swap memo: %s", err)
		}
	case AddMemo:
		newMsg, err = getMsgAddFromMemo(m, tx, signer)
		if err != nil {
			return nil, se.Wrapf(errInvalidMemo, "invalid add memo: %s", err)
		}
	case GasMemo:
		newMsg, err = getMsgNoOpFromMemo(tx, signer)
		if err != nil {
			return nil, se.Wrapf(errInvalidMemo, "invalid noop memo: %s", err)
		}
	case RefundMemo:
		newMsg, err = getMsgRefundFromMemo(m, tx, signer)
		if err != nil {
			return nil, se.Wrapf(errInvalidMemo, "invalid refund memo: %s", err)
		}
	case OutboundMemo:
		newMsg, err = getMsgOutboundFromMemo(m, tx, signer)
		if err != nil {
			return nil, se.Wrapf(errInvalidMemo, "invalid outbound memo: %s", err)
		}
	case MigrateMemo:
		newMsg, err = getMsgMigrateFromMemo(m, tx, signer)
		if err != nil {
			return nil, se.Wrapf(errInvalidMemo, "invalid migrate memo: %s", err)
		}
	case BondMemo:
		newMsg, err = getMsgBondFromMemo(m, tx, signer)
		if err != nil {
			return nil, se.Wrapf(errInvalidMemo, "invalid bond memo: %s", err)
		}
	case RagnarokMemo:
		newMsg, err = getMsgRagnarokFromMemo(m, tx, signer)
		if err != nil {
			return nil, se.Wrapf(errInvalidMemo, "invalid ragnarok memo: %s", err)
		}
	case LeaveMemo:
		newMsg = NewMsgLeave(tx.Tx, signer)
	case YggdrasilFundMemo:
		newMsg = NewMsgYggdrasil(tx.Tx, tx.ObservedPubKey, m.GetBlockHeight(), true, tx.Tx.Coins, signer)
	case YggdrasilReturnMemo:
		newMsg = NewMsgYggdrasil(tx.Tx, tx.ObservedPubKey, m.GetBlockHeight(), false, tx.Tx.Coins, signer)
	case ReserveMemo:
		res := NewReserveContributor(tx.Tx.FromAddress, tx.Tx.Coins[0].Amount)
		newMsg = NewMsgReserveContributor(tx.Tx, res, signer)
	case SwitchMemo:
		newMsg = NewMsgSwitch(tx.Tx, memo.GetDestination(), signer)
	default:
		return nil, errInvalidMemo
	}

	if err := newMsg.ValidateBasic(); err != nil {
		return nil, se.Wrapf(errInvalidMemo, "invalid message: %s", err)
	}
	return newMsg, nil
}

func getMsgNoOpFromMemo(tx ObservedTx, signer sdk.AccAddress) (sdk.Msg, error) {
	for _, coin := range tx.Tx.Coins {
		if !coin.Asset.IsBNB() {
			return nil, errors.New("only accepts BNB coins")
		}
	}
	return NewMsgNoOp(tx, signer), nil
}

func getMsgSwapFromMemo(memo SwapMemo, tx ObservedTx, signer sdk.AccAddress) (sdk.Msg, error) {
	if len(tx.Tx.Coins) > 1 {
		return nil, errors.New("not expecting multiple coins in a swap")
	}
	if memo.Destination.IsEmpty() {
		memo.Destination = tx.Tx.FromAddress
	}

	coin := tx.Tx.Coins[0]
	if memo.Asset.Equals(coin.Asset) {
		return nil, fmt.Errorf("swap from %s to %s is noop, refund", memo.Asset.String(), coin.Asset.String())
	}

	// Looks like at the moment THORNode can only process ont ty
	return NewMsgSwap(tx.Tx, memo.GetAsset(), memo.Destination, memo.SlipLimit, signer), nil
}

func getMsgUnstakeFromMemo(memo UnstakeMemo, tx ObservedTx, signer sdk.AccAddress) (sdk.Msg, error) {
	withdrawAmount := sdk.NewUint(MaxUnstakeBasisPoints)
	if len(memo.GetAmount()) > 0 {
		withdrawAmount = sdk.NewUintFromString(memo.GetAmount())
	}
	return NewMsgSetUnStake(tx.Tx, tx.Tx.FromAddress, withdrawAmount, memo.GetAsset(), signer), nil
}

func getMsgStakeFromMemo(ctx sdk.Context, memo StakeMemo, tx ObservedTx, signer sdk.AccAddress) (sdk.Msg, error) {
	// when staker stake to a pool ,usually it will be two coins, RUNE and the asset of the pool.
	// if it is multi-chain , like NOT Binance chain , it is using two asymmetric staking
	if len(tx.Tx.Coins) > 2 {
		return nil, errors.New("not expecting more than two coins in a stake")
	}
	runeAmount := sdk.ZeroUint()
	assetAmount := sdk.ZeroUint()
	asset := memo.GetAsset()
	if asset.IsEmpty() {
		return nil, errors.New("unable to determine the intended pool for this stake")
	}
	// There is no dedicate pool for RUNE ,because every pool will have RUNE , that's by design
	if asset.IsRune() {
		return nil, errors.New("invalid pool asset")
	}
	// Extract the Rune amount and the asset amount from the transaction. At least one of them must be
	// nonzero. If THORNode saw two types of coins, one of them must be the asset coin.
	for _, coin := range tx.Tx.Coins {
		ctx.Logger().Info("coin", "asset", coin.Asset.String(), "amount", coin.Amount.String())
		if coin.Asset.IsRune() {
			runeAmount = coin.Amount
		}
		if asset.Equals(coin.Asset) {
			assetAmount = coin.Amount
		}
	}

	if runeAmount.IsZero() && assetAmount.IsZero() {
		return nil, errors.New("did not find any valid coins for stake")
	}

	// when THORNode receive two coins, but THORNode didn't find the coin specify by asset, then user might send in the wrong coin
	if assetAmount.IsZero() && len(tx.Tx.Coins) == 2 {
		return nil, fmt.Errorf("did not find %s ", asset)
	}

	runeAddr := tx.Tx.FromAddress
	assetAddr := memo.GetDestination()
	// this is to cover multi-chain scenario, for example BTC , staker who would like to stake in BTC pool,  will have to complete
	// the stake operation by sending in two asymmetric stake tx, one tx on BTC chain with memo stake:BTC:<RUNE address> ,
	// and another one on Binance chain with stake:BTC , with only RUNE as the coin
	// Thorchain will use the <RUNE address> to match these two together , and consider it as one stake.
	if !runeAddr.IsChain(common.RuneAsset().Chain) {
		runeAddr = memo.GetDestination()
		assetAddr = tx.Tx.FromAddress
	} else {
		// if it is on BNB chain , while the asset addr is empty, then the asset addr is runeAddr
		if assetAddr.IsEmpty() {
			assetAddr = runeAddr
		}
	}

	return NewMsgSetStakeData(
		tx.Tx,
		asset,
		runeAmount,
		assetAmount,
		runeAddr,
		assetAddr,
		signer,
	), nil
}

func getMsgAddFromMemo(memo AddMemo, tx ObservedTx, signer sdk.AccAddress) (sdk.Msg, error) {
	runeAmount := sdk.ZeroUint()
	assetAmount := sdk.ZeroUint()
	for _, coin := range tx.Tx.Coins {
		if coin.Asset.IsRune() {
			runeAmount = coin.Amount
		} else if memo.GetAsset().Equals(coin.Asset) {
			assetAmount = coin.Amount
		}
	}
	return NewMsgAdd(
		tx.Tx,
		memo.GetAsset(),
		runeAmount,
		assetAmount,
		signer,
	), nil
}

func getMsgRefundFromMemo(memo RefundMemo, tx ObservedTx, signer sdk.AccAddress) (sdk.Msg, error) {
	return NewMsgRefundTx(
		tx,
		memo.GetTxID(),
		signer,
	), nil
}

func getMsgOutboundFromMemo(memo OutboundMemo, tx ObservedTx, signer sdk.AccAddress) (sdk.Msg, error) {
	return NewMsgOutboundTx(
		tx,
		memo.GetTxID(),
		signer,
	), nil
}

func getMsgMigrateFromMemo(memo MigrateMemo, tx ObservedTx, signer sdk.AccAddress) (sdk.Msg, error) {
	return NewMsgMigrate(tx, memo.GetBlockHeight(), signer), nil
}

func getMsgRagnarokFromMemo(memo RagnarokMemo, tx ObservedTx, signer sdk.AccAddress) (sdk.Msg, error) {
	return NewMsgRagnarok(tx, memo.GetBlockHeight(), signer), nil
}

func getMsgBondFromMemo(memo BondMemo, tx ObservedTx, signer sdk.AccAddress) (sdk.Msg, error) {
	runeAmount := sdk.ZeroUint()
	for _, coin := range tx.Tx.Coins {
		if coin.Asset.IsRune() {
			runeAmount = coin.Amount
		}
	}
	if runeAmount.IsZero() {
		return nil, errors.New("RUNE amount is 0")
	}
	return NewMsgBond(tx.Tx, memo.GetAccAddress(), runeAmount, tx.Tx.FromAddress, signer), nil
}
