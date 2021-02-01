package thorchain

import (
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

// MintCompositeHandler is to process native messages on THORChain
type MintCompositeHandler struct {
	keeper keeper.Keeper
	mgr    Manager
}

// NewMintCompositeHandler create a new instance of MintCompositeHandler
func NewMintCompositeHandler(keeper keeper.Keeper, mgr Manager) MintCompositeHandler {
	return MintCompositeHandler{
		keeper: keeper,
		mgr:    mgr,
	}
}

// Run is the main entry of MintCompositeHandler
func (h MintCompositeHandler) Run(ctx cosmos.Context, m cosmos.Msg, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	msg, ok := m.(*MsgMintComposite)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg, version); err != nil {
		ctx.Logger().Error("MsgMintComposite failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg, version, constAccessor)
	if err != nil {
		ctx.Logger().Error("fail to process MsgMintComposite", "error", err)
		return nil, err
	}
	return result, nil
}

func (h MintCompositeHandler) validate(ctx cosmos.Context, msg MsgMintComposite, version semver.Version) error {
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.validateV1(ctx, msg)
	}
	return errInvalidVersion
}

func (h MintCompositeHandler) validateV1(ctx cosmos.Context, msg MsgMintComposite) error {
	return msg.ValidateBasic()
}

func (h MintCompositeHandler) handle(ctx cosmos.Context, msg MsgMintComposite, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgMintComposite", "from", msg.GetSigners()[0], "coin", msg.Coin, "id", msg.CompositeId)
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.handleV1(ctx, msg, version, constAccessor)
	}
	return nil, errInvalidVersion
}

func (h MintCompositeHandler) handleV1(ctx cosmos.Context, msg MsgMintComposite, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	// TODO
	return &cosmos.Result{}, nil
}

// BurnCompositeHandler is to process native messages on THORChain
type BurnCompositeHandler struct {
	keeper keeper.Keeper
	mgr    Manager
}

// NewBurnCompositeHandler create a new instance of BurnCompositeHandler
func NewBurnCompositeHandler(keeper keeper.Keeper, mgr Manager) BurnCompositeHandler {
	return BurnCompositeHandler{
		keeper: keeper,
		mgr:    mgr,
	}
}

// Run is the main entry of BurnCompositeHandler
func (h BurnCompositeHandler) Run(ctx cosmos.Context, m cosmos.Msg, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	msg, ok := m.(*MsgBurnComposite)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg, version); err != nil {
		ctx.Logger().Error("MsgBurnComposite failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg, version, constAccessor)
	if err != nil {
		ctx.Logger().Error("fail to process MsgBurnComposite", "error", err)
		return nil, err
	}
	return result, nil
}

func (h BurnCompositeHandler) validate(ctx cosmos.Context, msg MsgBurnComposite, version semver.Version) error {
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.validateV1(ctx, msg)
	}
	return errInvalidVersion
}

func (h BurnCompositeHandler) validateV1(ctx cosmos.Context, msg MsgBurnComposite) error {
	return msg.ValidateBasic()
}

func (h BurnCompositeHandler) handle(ctx cosmos.Context, msg MsgBurnComposite, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgBurnComposite", "from", msg.GetSigners()[0], "coin", msg.Coin)
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.handleV1(ctx, msg, version, constAccessor)
	}
	return nil, errInvalidVersion
}

func (h BurnCompositeHandler) handleV1(ctx cosmos.Context, msg MsgBurnComposite, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	// TODO
	return &cosmos.Result{}, nil
}
