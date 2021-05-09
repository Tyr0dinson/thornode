package thorchain

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

var IsValidTHORNameV1 = regexp.MustCompile(`^[a-zA-Z0-9+_-]+$`).MatchString

// ManageTHORNameHandler a handler to process MsgNetworkFee messages
type ManageTHORNameHandler struct {
	keeper keeper.Keeper
	mgr    Manager
}

// NewManageTHORNameHandler create a new instance of network fee handler
func NewManageTHORNameHandler(keeper keeper.Keeper, mgr Manager) ManageTHORNameHandler {
	return ManageTHORNameHandler{keeper: keeper, mgr: mgr}
}

// Run is the main entry point for network fee logic
func (h ManageTHORNameHandler) Run(ctx cosmos.Context, m cosmos.Msg, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	msg, ok := m.(*MsgManageTHORName)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg, version, constAccessor); err != nil {
		ctx.Logger().Error("MsgManageTHORName failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg, version, constAccessor)
	if err != nil {
		ctx.Logger().Error("fail to process MsgManageTHORName", "error", err)
	}
	return result, err
}

func (h ManageTHORNameHandler) validate(ctx cosmos.Context, msg MsgManageTHORName, version semver.Version, constAccessor constants.ConstantValues) error {
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.validateV1(ctx, msg, constAccessor)
	}
	return errBadVersion
}

func (h ManageTHORNameHandler) validateV1(ctx cosmos.Context, msg MsgManageTHORName, constAccessor constants.ConstantValues) error {
	return h.validateCurrent(ctx, msg, constAccessor)
}

func (h ManageTHORNameHandler) validateNameV1(n string) error {
	// validate THORName
	if len(n) > 30 {
		return errors.New("THORName cannot exceed 30 characters")
	}
	if !IsValidTHORNameV1(n) {
		return errors.New("invalid THORName")
	}
	return nil
}

func (h ManageTHORNameHandler) validateCurrent(ctx cosmos.Context, msg MsgManageTHORName, constAccessor constants.ConstantValues) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	exists := h.keeper.THORNameExists(ctx, msg.Name, common.THORChain)

	if !exists {
		// thornode doesn't appear to exist, lets validate the name
		if err := h.validateNameV1(msg.Name); err != nil {
			return err
		}
		registrationFee := constAccessor.GetInt64Value(constants.TNSRegisterFee)
		if msg.Coin.Amount.LTE(cosmos.NewUint(uint64(registrationFee))) {
			return fmt.Errorf("not enough funds")
		}
	} else {
		name, err := h.keeper.GetTHORName(ctx, msg.Name, common.THORChain)
		if err != nil {
			return err
		}

		// if this thorname is already owned, check signer has ownership. If
		// expiration is past, allow different user to take ownership
		if !name.Address.IsEmpty() {
			acc, err := name.Address.AccAddress()
			if err != nil {
				ctx.Logger().Error("fail to get THORName AccAddress", "error", err)
				return err
			}
			if !acc.Equals(msg.Signer) && common.BlockHeight(ctx) <= name.ExpireBlockHeight {
				ctx.Logger().Error("no authorization", "owner", name.Address)
				return fmt.Errorf("no authorization: owned by %s", name.Address)
			}
		}

		// ensure user isn't inflating their expire block height artificaially
		if name.ExpireBlockHeight < msg.ExpireBlockHeight {
			return errors.New("cannot artificially inflate expire block height")
		}
	}

	return nil
}

// handle process MsgManageTHORName
func (h ManageTHORNameHandler) handle(ctx cosmos.Context, msg MsgManageTHORName, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.handleV1(ctx, msg, version, constAccessor)
	}
	return nil, errBadVersion
}

// handleV1 process MsgManageTHORName
func (h ManageTHORNameHandler) handleV1(ctx cosmos.Context, msg MsgManageTHORName, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	return h.handleCurrent(ctx, msg, version, constAccessor)
}

func (h ManageTHORNameHandler) handleCurrent(ctx cosmos.Context, msg MsgManageTHORName, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	var err error

	// TODO: write keeper unit tests

	if err := nativeGasFee(ctx, msg.GetSigners()[0], h.keeper, constAccessor); err != nil {
		return nil, err
	}

	tn := NewTHORName(msg.Name, msg.Chain, msg.Address, 0)
	exists := h.keeper.THORNameExists(ctx, msg.Name, msg.Chain)
	if exists {
		tn, err = h.keeper.GetTHORName(ctx, msg.Name, msg.Chain)
		if err != nil {
			return nil, err
		}
	}

	registrationFeePaid := cosmos.ZeroUint()
	fundPaid := cosmos.ZeroUint()

	// check if user is trying to extend expiration
	if !msg.Coin.Amount.IsZero() {
		// check that THORName is still valid, can't top up an invalid THORName
		if err := h.validateNameV1(msg.Name); err != nil {
			return nil, err
		}
		if !exists {
			// minus registration fee
			registrationFee := constAccessor.GetInt64Value(constants.TNSRegisterFee)
			msg.Coin.Amount = common.SafeSub(msg.Coin.Amount, cosmos.NewUint(uint64(registrationFee)))
			registrationFeePaid = cosmos.NewUint(uint64(registrationFee))
		}
		feePerBlock := constAccessor.GetInt64Value(constants.TNSFeePerBlock)
		fundPaid = msg.Coin.Amount
		addBlocks := int64(msg.Coin.Amount.Uint64()) / feePerBlock
		tn.ExpireBlockHeight += addBlocks
		h.keeper.SetTHORName(ctx, tn)
	}

	// check if we need to reduce the expire time, upon user request
	if msg.ExpireBlockHeight > 0 && msg.ExpireBlockHeight < tn.ExpireBlockHeight {
		tn.ExpireBlockHeight = msg.ExpireBlockHeight
		h.keeper.SetTHORName(ctx, tn)
	}

	// update address
	if !msg.Address.IsEmpty() {
		tn, _ := h.keeper.GetTHORName(ctx, msg.Name, msg.Chain)
		tn.Address = msg.Address
		h.keeper.SetTHORName(ctx, tn)
	}

	evt := NewEventTHORName(tn.Name, tn.Chain, tn.Address, registrationFeePaid, fundPaid, tn.ExpireBlockHeight, msg.GetSigners()[0])
	if err := h.mgr.EventMgr().EmitEvent(ctx, evt); nil != err {
		ctx.Logger().Error("fail to emit THORName event", "error", err)
	}

	return &cosmos.Result{}, nil
}
