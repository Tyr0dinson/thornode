package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// OperatorRotateHandler is the handler to process MsgOperatorRotate.
type OperatorRotateHandler struct {
	mgr Manager
}

// NewOperatorRotateHandler creates a new instance of OperatorRotateHandler.
func NewOperatorRotateHandler(mgr Manager) OperatorRotateHandler {
	return OperatorRotateHandler{
		mgr: mgr,
	}
}

// Run is the main entry point for OperatorRotateHandler.
func (h OperatorRotateHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgOperatorRotate)
	if !ok {
		return nil, errInvalidMessage
	}

	err := h.validate(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("MsgOperatorRotate failed validation", "error", err)
		return nil, err
	}

	err = h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgOperatorRotate", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, err
}

func (h OperatorRotateHandler) validate(ctx cosmos.Context, msg MsgOperatorRotate) error {
	return msg.ValidateBasic()
}

func (h OperatorRotateHandler) handle(ctx cosmos.Context, msg MsgOperatorRotate) error {
	// check mimir if rotate is halted
	haltOperatorRotate := h.mgr.Keeper().GetConfigInt64(ctx, constants.HaltOperatorRotate)
	if haltOperatorRotate > 0 {
		return fmt.Errorf("rotate is halted")
	}

	// rotating to the current operator is a no-op; reject it explicitly so an
	// Active operator does not hit the misleading "rotate to operator with
	// active node" error below.
	if msg.OperatorAddress.Equals(msg.Signer) {
		return fmt.Errorf("new operator is the current operator")
	}

	// Rotation may run while the operator's nodes are Active, but not during the
	// churn window when vault membership is (re)computed, so it cannot be used to
	// steer vault placement (the reason the Active guard originally existed).
	if err := h.churnWindowGuard(ctx); err != nil {
		return err
	}

	// find nodes operated by the signer
	iter := h.mgr.Keeper().GetNodeAccountIterator(ctx)
	defer iter.Close()
	rotateNodes := NodeAccounts{}
	for ; iter.Valid(); iter.Next() {
		var na NodeAccount
		if err := h.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &na); err != nil {
			return fmt.Errorf("fail to unmarshal node account, %w", err)
		}

		// skip empty node accounts
		if na.IsEmpty() {
			continue
		}

		// rotation not allowed if any of the new operator nodes are active:
		// merging into an operator that already has an Active node would collapse
		// the per-operator price-feed vote grouping.
		if na.Status == NodeActive && na.BondAddress.Equals(common.Address(msg.OperatorAddress.String())) {
			return fmt.Errorf("cannot rotate to operator with active node: %s", na.NodeAddress)
		}

		// filter by bond address (the signer must be the current operator)
		if !na.BondAddress.Equals(common.Address(msg.Signer.String())) {
			continue
		}

		rotateNodes = append(rotateNodes, na)
	}

	if len(rotateNodes) == 0 {
		return fmt.Errorf("no nodes found for operator %s", msg.Signer)
	}

	// rotate each node
	for _, node := range rotateNodes {
		if err := h.rotate(ctx, msg.OperatorAddress, node); err != nil {
			return err
		}
	}

	return nil
}

// churnWindowGuard refuses operator rotation during the churn window, so a
// rotation cannot relabel operator identity just before a keygen reads the
// per-operator grouping to place nodes across vaults. It blocks while a keygen
// is pending or a vault migration is in progress — even while churning is
// halted, since a keygen scheduled before the halt can still finalize — and,
// only when churning is not halted, in the second half of the churn cycle as
// the next churn approaches.
func (h OperatorRotateHandler) churnWindowGuard(ctx cosmos.Context) error {
	k := h.mgr.Keeper()

	// A live pending Asgard keygen blocks rotation EVEN while churning is
	// halted: HaltChurning stops a new churn being scheduled, but a keygen
	// scheduled before the halt can still finalize and activate its vault
	// (handler_tss and validator EndBlock are not halt-gated), so rotating into
	// a node already selected for that keygen would merge operators after vault
	// placement. hasPendingKeygen counts only keygens still inside their
	// result-validity window, so a stale failed-round InitVault does not block
	// rotation for the whole halt.
	if vm, ok := h.mgr.ValidatorMgr().(*ValidatorMgr); ok {
		pending, err := vm.hasPendingKeygen(ctx, AsgardKeygen)
		if err != nil {
			return err
		}
		if pending {
			return fmt.Errorf("rotate not allowed while a keygen is pending")
		}
	}

	// vault migration in progress: a retiring vault still holding real funds
	// (also enforced while halted, for the same post-placement-merge reason).
	retiringVaults, err := k.GetAsgardVaultsByStatus(ctx, RetiringVault)
	if err != nil {
		return err
	}
	for _, vault := range retiringVaults {
		if vault.HasFundsAboveDust() {
			return fmt.Errorf("rotate not allowed while a vault migration is in progress")
		}
	}

	// near a scheduled churn: refuse in the second half of the churn cycle as
	// the next keygen approaches. HaltChurning exempts ONLY this cutoff, since a
	// halt genuinely stops the next churn being scheduled. No churnInterval > 0
	// guard: a zero interval still schedules retry keygens, so it must fail
	// closed; getLastChurnHeight also returns 0 on a read failure, failing
	// closed (refuse), the safe direction.
	halt, err := k.GetMimir(ctx, "HaltChurning")
	if err == nil && halt > 0 && halt <= ctx.BlockHeight() {
		return nil
	}
	lastChurnHeight := getLastChurnHeight(ctx, k)
	churnInterval := k.GetConfigInt64(ctx, constants.ChurnInterval)
	if ctx.BlockHeight() > lastChurnHeight+churnInterval/2 {
		return fmt.Errorf("rotate not allowed in the second half of the churn cycle")
	}
	return nil
}

func (h OperatorRotateHandler) rotate(ctx cosmos.Context, operator cosmos.AccAddress, nodeAcc NodeAccount) error {
	currentOperator, err := nodeAcc.BondAddress.AccAddress()
	if err != nil {
		return ErrInternal(err, "fail to get bond address")
	}

	// get current bond provider records
	bp, err := h.mgr.Keeper().GetBondProviders(ctx, nodeAcc.NodeAddress)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get bond providers(%s)", nodeAcc.NodeAddress))
	}

	// check for duplicate bond provider before making changes
	if bp.Has(operator) {
		return fmt.Errorf("operator %s is already a bond provider for node %s", operator, nodeAcc.NodeAddress)
	}

	// call passiveBackfill before mutating BondAddress so it reads the correct operator
	err = passiveBackfill(ctx, h.mgr, nodeAcc, &bp)
	if err != nil {
		return err
	}

	// rotate the operator address after passiveBackfill
	nodeAcc.BondAddress = common.Address(operator.String())

	// update the corresponding bond provider record
	updated := false
	for i, provider := range bp.Providers {
		if provider.BondAddress.Equals(currentOperator) {
			// Rotation changes the controlling key for the existing operator provider;
			// preserve its bond and payout configuration.
			bp.Providers[i].BondAddress = operator
			updated = true
		}
	}
	if !updated && len(bp.Providers) > 0 {
		return fmt.Errorf("no bond provider matches current operator %s for node %s", currentOperator, nodeAcc.NodeAddress)
	}

	// store updated bond provider records
	err = h.mgr.Keeper().SetBondProviders(ctx, bp)
	if err != nil {
		return ErrInternal(err, "fail to save bond providers")
	}

	// store updated node account
	err = h.mgr.Keeper().SetNodeAccount(ctx, nodeAcc)
	if err != nil {
		return ErrInternal(err, "fail to save node account")
	}

	rotateEvent := NewEventOperatorRotate(currentOperator, nodeAcc.NodeAddress, operator)
	if err := h.mgr.EventMgr().EmitEvent(ctx, rotateEvent); err != nil {
		ctx.Logger().Error("fail to emit rotate event", "error", err)
	}

	return nil
}
