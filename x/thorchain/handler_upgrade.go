package thorchain

import (
	"bytes"
	"fmt"
	"strconv"

	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"

	"github.com/blang/semver"
	upgradetypes "github.com/cosmos/cosmos-sdk/x/upgrade/types"
)

// ProposeUpgradeHandler is to handle the ProposeUpgrade message
type ProposeUpgradeHandler struct {
	mgr Manager
}

// NewProposeUpgradeHandler create new instance of ProposeUpgradeHandler
func NewProposeUpgradeHandler(mgr Manager) ProposeUpgradeHandler {
	return ProposeUpgradeHandler{
		mgr: mgr,
	}
}

// Run is the main entry point to execute upgrade proposal logic
func (h ProposeUpgradeHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgProposeUpgrade)
	if !ok {
		return nil, errInvalidMessage
	}

	u := msg.Upgrade

	ctx.Logger().Info(
		"Validator propose upgrade",
		"thor_address", msg.Signer.String(),
		"name", msg.Name,
		"height", u.Height,
		"info", u.Info,
	)

	if err := h.validate(ctx, msg); err != nil {
		ctx.Logger().Error("msg propose upgrade failed validation", "error", err)
		return nil, err
	}

	if err := h.handle(ctx, msg); err != nil {
		ctx.Logger().Error("failed to process msg propose upgrade", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h ProposeUpgradeHandler) validate(ctx cosmos.Context, msg *MsgProposeUpgrade) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.136.0")):
		return h.validateV136(ctx, msg)
	}
	return errBadVersion
}

func (h ProposeUpgradeHandler) validateV136(ctx cosmos.Context, msg *MsgProposeUpgrade) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	if err := signedByActiveNodeAccount(ctx, h.mgr.Keeper(), msg.Signer); err != nil {
		return cosmos.ErrUnauthorized(err.Error())
	}

	if ctx.BlockHeight() <= msg.Upgrade.Height {
		return cosmos.ErrUnknownRequest("upgrade height must be in the future")
	}

	return nil
}

func (h ProposeUpgradeHandler) handle(ctx cosmos.Context, msg *MsgProposeUpgrade) error {
	u := msg.Upgrade
	name := msg.Name
	k := h.mgr.Keeper()

	if err := k.ProposeUpgrade(ctx, name, u); err != nil {
		return fmt.Errorf("failed to propose upgrade: %w", err)
	}

	ctx.EventManager().EmitEvent(
		cosmos.NewEvent("propose_upgrade",
			cosmos.NewAttribute("thor_address", msg.Signer.String()),
			cosmos.NewAttribute("name", name),
			cosmos.NewAttribute("height", strconv.FormatInt(u.Height, 10)),
			cosmos.NewAttribute("info", u.Info),
		),
	)

	if err := k.ApproveUpgrade(ctx, msg.Signer, name); err != nil {
		return fmt.Errorf("failed to approve upgrade: %w", err)
	}

	ctx.EventManager().EmitEvent(
		cosmos.NewEvent("approve_upgrade",
			cosmos.NewAttribute("thor_address", msg.Signer.String()),
			cosmos.NewAttribute("name", name),
		),
	)

	return scheduleUpgradeIfNecessary(ctx, k, name)
}

// ApproveUpgradeHandler is to handle the ApproveUpgrade message
type ApproveUpgradeHandler struct {
	mgr Manager
}

// NewApproveUpgradeHandler create new instance of ApproveUpgradeHandler
func NewApproveUpgradeHandler(mgr Manager) ApproveUpgradeHandler {
	return ApproveUpgradeHandler{
		mgr: mgr,
	}
}

// Run it the main entry point to execute Version logic
func (h ApproveUpgradeHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgApproveUpgrade)
	if !ok {
		return nil, errInvalidMessage
	}

	ctx.Logger().Info(
		"Validator approving upgrade",
		"thor_address", msg.Signer.String(),
		"name", msg.Name,
	)

	if err := h.validate(ctx, msg); err != nil {
		ctx.Logger().Error("msg approve upgrade failed validation", "error", err)
		return nil, err
	}

	if err := h.handle(ctx, msg); err != nil {
		ctx.Logger().Error("failed to process msg approve upgrade", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h ApproveUpgradeHandler) validate(ctx cosmos.Context, msg *MsgApproveUpgrade) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.136.0")):
		return h.validateV136(ctx, msg)
	}
	return errBadVersion
}

func (h ApproveUpgradeHandler) validateV136(ctx cosmos.Context, msg *MsgApproveUpgrade) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	if err := signedByActiveNodeAccount(ctx, h.mgr.Keeper(), msg.Signer); err != nil {
		return cosmos.ErrUnauthorized(err.Error())
	}

	return nil
}

func (h ApproveUpgradeHandler) handle(ctx cosmos.Context, msg *MsgApproveUpgrade) error {
	k := h.mgr.Keeper()
	name := msg.Name

	if err := k.ApproveUpgrade(ctx, msg.Signer, name); err != nil {
		return fmt.Errorf("failed to approve upgrade: %w", err)
	}

	ctx.EventManager().EmitEvent(
		cosmos.NewEvent("approve_upgrade",
			cosmos.NewAttribute("thor_address", msg.Signer.String()),
			cosmos.NewAttribute("name", name),
		),
	)

	return scheduleUpgradeIfNecessary(ctx, k, name)
}

// RejectUpgradeHandler is to handle the RejectUpgrade message
type RejectUpgradeHandler struct {
	mgr Manager
}

// NewRejectUpgradeHandler create new instance of RejectUpgradeHandler
func NewRejectUpgradeHandler(mgr Manager) RejectUpgradeHandler {
	return RejectUpgradeHandler{
		mgr: mgr,
	}
}

// Run it the main entry point to execute Version logic
func (h RejectUpgradeHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgRejectUpgrade)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info(
		"Validator rejecting upgrade",
		"thor_address", msg.Signer.String(),
		"name", msg.Name,
	)
	if err := h.validate(ctx, msg); err != nil {
		ctx.Logger().Error("msg reject upgrade failed validation", "error", err)
		return nil, err
	}
	if err := h.handle(ctx, msg); err != nil {
		ctx.Logger().Error("failed to process msg reject upgrade", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h RejectUpgradeHandler) validate(ctx cosmos.Context, msg *MsgRejectUpgrade) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.136.0")):
		return h.validateV136(ctx, msg)
	}
	return errBadVersion
}

func (h RejectUpgradeHandler) validateV136(ctx cosmos.Context, msg *MsgRejectUpgrade) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	if err := signedByActiveNodeAccount(ctx, h.mgr.Keeper(), msg.Signer); err != nil {
		return cosmos.ErrUnauthorized(err.Error())
	}

	return nil
}

func (h RejectUpgradeHandler) handle(ctx cosmos.Context, msg *MsgRejectUpgrade) error {
	k := h.mgr.Keeper()
	name := msg.Name

	if err := k.RejectUpgrade(ctx, msg.Signer, name); err != nil {
		return fmt.Errorf("failed to reject upgrade: %w", err)
	}

	ctx.EventManager().EmitEvent(
		cosmos.NewEvent("reject_upgrade",
			cosmos.NewAttribute("thor_address", msg.Signer.String()),
			cosmos.NewAttribute("name", name),
		),
	)

	return clearUpgradeIfNecessary(ctx, k, name)
}

func scheduleUpgradeIfNecessary(ctx cosmos.Context, k keeper.Keeper, name string) error {
	upgradePlan, upgradeScheduled := k.GetUpgradePlan(ctx)
	if upgradeScheduled && upgradePlan.Name == name {
		// already scheduled
		return nil
	}

	u, err := k.GetProposedUpgrade(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to get proposed upgrade: %w", err)
	}

	upgradeApproved, err := upgradeApprovedByMajority(ctx, k, name)
	if err != nil {
		return fmt.Errorf("failed to determine if upgrade is approved by majority threshold of validators: %w", err)
	}

	if upgradeApproved {
		// upgrade approval is now over the majority threshold
		k.ScheduleUpgrade(ctx, upgradetypes.Plan{
			Name:   name,
			Height: u.Height,
			Info:   u.Info,
		})
	}

	return nil
}

func clearUpgradeIfNecessary(ctx cosmos.Context, k keeper.Keeper, name string) error {
	upgradePlan, upgradeScheduled := k.GetUpgradePlan(ctx)
	if !upgradeScheduled || (upgradeScheduled && upgradePlan.Name != name) {
		// upgrade by this name not scheduled
		return nil
	}

	upgradeApproved, err := upgradeApprovedByMajority(ctx, k, name)
	if err != nil {
		return fmt.Errorf("failed to determine if upgrade is approved by majority threshold of validators: %w", err)
	}

	if !upgradeApproved {
		// upgrade approval dropped below the majority threshold. upgrade plan was on chain, so cancel it.
		k.ClearUpgradePlan(ctx)
	}

	return nil
}

// upgradeApprovedByMajority returns true and no error if the upgrade is approved by 2/3 of Validators
func upgradeApprovedByMajority(ctx cosmos.Context, k keeper.Keeper, name string) (bool, error) {
	iterA := k.GetNodeAccountIterator(ctx)
	defer iterA.Close()

	totalActiveBond := cosmos.ZeroUint()
	bonds := make(map[string]cosmos.Uint)

	for ; iterA.Valid(); iterA.Next() {
		var na NodeAccount
		if err := k.Cdc().Unmarshal(iterA.Value(), &na); err != nil {
			return false, fmt.Errorf("failed to unmarshal: %w", err)
		}

		if na.IsEmpty() || na.Status != NodeActive || na.Type != NodeTypeValidator {
			continue
		}

		totalActiveBond = totalActiveBond.Add(na.Bond)
		bonds[na.NodeAddress.String()] = na.Bond
	}

	bondThreshold := totalActiveBond.MulUint64(2).QuoUint64(3)

	iterV := k.GetUpgradeVoteIterator(ctx, name)
	defer iterV.Close()

	totalApprovedBond := cosmos.ZeroUint()

	for ; iterV.Valid(); iterV.Next() {
		key, vote := iterV.Key(), iterV.Value()
		if !bytes.Equal(vote, []byte{0x1}) {
			continue
		}

		addrSplit := bytes.Split(key, []byte("/"))
		addr := cosmos.AccAddress(addrSplit[len(addrSplit)-1])

		bond, ok := bonds[addr.String()]
		if !ok {
			// this could happen if a validator votes and then becomes inactive
			continue
		}

		totalApprovedBond = totalApprovedBond.Add(bond)

		if totalApprovedBond.GTE(bondThreshold) {
			return true, nil
		}
	}

	return false, nil
}
