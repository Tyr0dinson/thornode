package thorchain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

var mimirValidKey = regexp.MustCompile(constants.MimirKeyRegex).MatchString

// MimirHandler is to handle mimir messages
type MimirHandler struct {
	mgr Manager
}

// NewMimirHandler create new instance of MimirHandler
func NewMimirHandler(mgr Manager) MimirHandler {
	return MimirHandler{
		mgr: mgr,
	}
}

// Run is the main entry point to execute mimir logic
func (h MimirHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgMimir)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg mimir failed validation", "error", err)
		return nil, err
	}
	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process msg set mimir", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h MimirHandler) validate(ctx cosmos.Context, msg MsgMimir) error {
	// ValidateBasic is also executed in message service router's handler and isn't versioned there
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// Normalize the key once so the prefix check and the thorname slice
	// agree on casing without recomputing ToUpper at each touch point.
	upperKey := strings.ToUpper(msg.Key)
	isRevShare := strings.HasPrefix(upperKey, "REVSHARE-")

	// REVSHARE-<thorname> keys must be MimirKey-regex-compatible. Detect
	// REVSHARE-prefixed keys that would fail the generic regex check below
	// and return a targeted error so operators understand the cause.
	if isRevShare && !mimirValidKey(msg.Key) {
		return cosmos.ErrUnknownRequest("REVSHARE thorname must contain only alphanumerics or hyphens (no '_' or '+')")
	}

	if !mimirValidKey(msg.Key) || len(msg.Key) > constants.MaxMimirLength {
		return cosmos.ErrUnknownRequest("invalid mimir key")
	}
	if _, err := validateMimirAuth(ctx, h.mgr.Keeper(), msg); err != nil {
		return err
	}

	// Abstain only means something where voting is required, which is ADR keys. Elsewhere
	// it would just be an inert record that never counts and never purges.
	if msg.Value == constants.MimirAbstainValue && !isADRMimir(msg.Key) {
		return cosmos.ErrUnknownRequest("abstain is only valid on ADR mimirs")
	}

	// REVSHARE-<thorname> operational mimir validation.
	if isRevShare {
		// Resolve any Mimir-safe alias (e.g. SWAPKIT -> "-_") back to the real
		// thorname before the existence check, so affiliates whose thorname
		// can't appear in a Mimir key can still be voted on.
		thornameName := revShareAliasToThorname(upperKey[len("REVSHARE-"):])
		if !h.mgr.Keeper().THORNameExists(ctx, thornameName) {
			return cosmos.ErrUnknownRequest("REVSHARE mimir requires existing thorname")
		}
		if msg.Value < 0 || msg.Value > constants.RevShareMaxBps {
			return cosmos.ErrUnknownRequest(fmt.Sprintf("REVSHARE value must be between 0 and %d bps", constants.RevShareMaxBps))
		}
		return nil
	}

	// nothing else to verify if this is not a min slip mimir
	if !strings.HasSuffix(strings.ToUpper(msg.Key), "SLIPMINBPS") {
		return nil
	}

	// operational min slip validation
	minSlipBpsMax := h.mgr.Keeper().GetConfigInt64(ctx, constants.SlipMinBpsMax)
	if msg.Value > minSlipBpsMax {
		return cosmos.ErrUnknownRequest("min slip mimir exceeds SlipMinBpsMax")
	}
	return nil
}

func (h MimirHandler) handle(ctx cosmos.Context, msg MsgMimir) error {
	ctx.Logger().Info("handleMsgMimir request", "node", msg.Signer, "key", msg.Key, "value", msg.Value)

	// Get the current Mimir key value if it exists.
	currentMimirValue, _ := h.mgr.Keeper().GetMimir(ctx, msg.Key)
	// Here, an error is assumed to mean the Mimir key is currently unset.

	// Cost and emitting of SetNodeMimir, even if a duplicate
	// (for instance if needed to confirm a new supermajority after a node number decrease).
	nodeAccount, err := h.mgr.Keeper().GetNodeAccount(ctx, msg.Signer)
	if err != nil {
		ctx.Logger().Error("fail to get node account", "error", err, "address", msg.Signer.String())
		return cosmos.ErrUnauthorized(fmt.Sprintf("%s is not authorized", msg.Signer))
	}
	cost := h.mgr.Keeper().GetNativeTxFee(ctx)
	if nodeAccount.Status != NodeActive {
		// Non-active nodes pay a flat, deliberately higher cost per vote. validate()
		// has already rejected signers that cannot cover it, so it is never capped —
		// capping is what made repeat votes free once bond reached zero.
		cost = standbyMimirCost(ctx, h.mgr.Keeper())
		if cost.GT(nodeAccount.Bond) {
			return cosmos.ErrUnauthorized(fmt.Sprintf("%s has insufficient bond to vote on a mimir", msg.Signer))
		}
	} else if cost.GT(nodeAccount.Bond) {
		cost = nodeAccount.Bond
	}
	nodeAccount.Bond = common.SafeSub(nodeAccount.Bond, cost)
	if err = h.mgr.Keeper().SetNodeAccount(ctx, nodeAccount); err != nil {
		ctx.Logger().Error("fail to save node account", "error", err)
		return fmt.Errorf("fail to save node account: %w", err)
	}
	// move set mimir cost from bond module to reserve
	coin := common.NewCoin(common.RuneNative, cost)
	if !cost.IsZero() {
		if err = h.mgr.Keeper().SendFromModuleToModule(ctx, BondName, ReserveName, common.NewCoins(coin)); err != nil {
			ctx.Logger().Error("fail to transfer funds from bond to reserve", "error", err)
			return err
		}
	}
	if err = h.mgr.Keeper().SetNodeMimir(ctx, msg.Key, msg.Value, msg.Signer); err != nil {
		ctx.Logger().Error("fail to save node mimir", "error", err)
		return err
	}
	nodeMimirEvent := NewEventSetNodeMimir(strings.ToUpper(msg.Key), strconv.FormatInt(msg.Value, 10), msg.Signer.String())
	if err = h.mgr.EventMgr().EmitEvent(ctx, nodeMimirEvent); err != nil {
		ctx.Logger().Error("fail to emit set_node_mimir event", "error", err)
		return err
	}
	bondEvent := NewEventBond(cost, BondCost, common.Tx{}, &nodeAccount, nil)
	if err = h.mgr.EventMgr().EmitEvent(ctx, bondEvent); err != nil {
		ctx.Logger().Error("fail to emit bond event", "error", err)
		return err
	}

	// If the Mimir key is already the submitted value, don't do anything further.
	if msg.Value == currentMimirValue {
		return nil
	}

	nodeMimirs, err := h.mgr.Keeper().GetNodeMimirs(ctx, msg.Key)
	if err != nil {
		ctx.Logger().Error("fail to get node mimirs", "error", err)
		return err
	}
	activeNodes, err := h.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		ctx.Logger().Error("fail to list active validators", "error", err)
		return err
	}

	var effectiveValue int64
	if constants.IsOperationalMimir(msg.Key) {
		// A value of -1 indicates either a tie or that no values satisfy the required minimum votes.
		operationalVotesMin := h.mgr.Keeper().GetConfigInt64(ctx, constants.OperationalVotesMin)
		effectiveValue = nodeMimirs.ValueOfOperational(msg.Key, operationalVotesMin, activeNodes.GetNodeAddresses())
	} else {
		// Economic Mimir, so require supermajority to set.
		var currentlyHasSuperMajority bool
		effectiveValue, currentlyHasSuperMajority = nodeMimirs.HasSuperMajority(msg.Key, activeNodes.GetNodeAddresses())
		if !currentlyHasSuperMajority {
			effectiveValue = -1
		}
	}
	// If the effective value is negative (used to signal no effective value), change nothing.
	if effectiveValue < 0 {
		return nil
	}
	// If the current Mimir value is already the effective value, change nothing.
	if currentMimirValue == effectiveValue {
		return nil
	}
	// If the MsgMimir value doesn't match the effective value, change nothing.
	if msg.Value != effectiveValue {
		return nil
	}
	// Reaching this point indicates a new mimir value is to be set.
	h.mgr.Keeper().SetMimir(ctx, msg.Key, effectiveValue)
	// A deliberate governance change, as opposed to an automatic halt set by the
	// slasher or the solvency checker. The thornode_mimir_value gauge shows the
	// resulting value, but a change reverted inside one scrape interval is only
	// visible here.
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "mimir", "changed"},
		1,
		[]metrics.Label{telemetry.NewLabel("key", strings.ToUpper(msg.Key))},
	)
	mimirEvent := NewEventSetMimir(strings.ToUpper(msg.Key), strconv.FormatInt(effectiveValue, 10))
	if err = h.mgr.EventMgr().EmitEvent(ctx, mimirEvent); err != nil {
		return fmt.Errorf("fail to emit set_mimir event: %w", err)
	}

	return nil
}

func validateMimirAuth(ctx cosmos.Context, k keeper.Keeper, msg MsgMimir) (cosmos.Context, error) {
	// ADR mimirs are votable by standby and ready nodes too, since a standby node must
	// have voted before it is allowed to churn in. Their votes are stored but excluded
	// from consensus by NodeMimirs.countActive until they become active.
	if isADRMimir(msg.Key) {
		return adrMimirSignerPriority(ctx, k, msg)
	}
	return activeNodeAccountsSignerPriority(ctx, k, msg.GetSigners())
}

func adrMimirSignerPriority(ctx cosmos.Context, k keeper.Keeper, msg MsgMimir) (cosmos.Context, error) {
	signers := msg.GetSigners()
	unauthorized := cosmos.ErrUnauthorized(fmt.Sprintf("%+v are not authorized", signers))
	if len(signers) == 0 {
		return ctx, unauthorized
	}
	allActive := true
	for _, signer := range signers {
		na, err := k.GetNodeAccount(ctx, signer)
		if err != nil {
			ctx.Logger().Error("fail to get node account", "error", err, "address", signer.String())
			return ctx, unauthorized
		}
		if na.IsEmpty() || na.Type != NodeTypeValidator {
			return ctx, unauthorized
		}
		switch na.Status {
		case NodeActive:
			continue
		case NodeStandby, NodeReady:
			allActive = false
			// A non-active node's vote is what let it pass the churn-in preflight, and
			// nothing re-checks it before vault activation, so it can't be withdrawn.
			// Abstain (-2) is still allowed; it counts as a vote.
			if msg.Value < 0 && msg.Value != constants.MimirAbstainValue {
				return ctx, cosmos.ErrUnauthorized(fmt.Sprintf("%s cannot withdraw an ADR vote while not active", signer))
			}
		default:
			return ctx, unauthorized
		}
		// A non-active node must be able to pay the full standby vote cost up front.
		// handle() otherwise caps the charge at the remaining bond, which would make
		// votes free once bond hits zero.
		if na.Bond.LT(standbyMimirCost(ctx, k)) {
			return ctx, cosmos.ErrUnauthorized(fmt.Sprintf("%s has insufficient bond to vote on a mimir", signer))
		}
	}
	if !allActive {
		// Non-active nodes may only join a vote the active set has already started. A
		// proposal always originates from an active node, and a key with no active
		// voter can never become pending anyway, so this costs nothing legitimate while
		// keeping cheap standby accounts from minting unbounded ADR keys.
		if !adrKeyHasActiveVote(ctx, k, msg.Key) {
			return ctx, cosmos.ErrUnauthorized(fmt.Sprintf("no active node has voted on %s yet", msg.Key))
		}
		// Only active nodes get validator priority.
		return ctx, nil
	}
	return ctx.WithPriority(ActiveNodePriority), nil
}

// MimirAnteHandler called by the ante handler to gate mempool entry
// and also during deliver. Store changes will persist if this function
// succeeds, regardless of the success of the transaction.
func MimirAnteHandler(ctx cosmos.Context, v semver.Version, k keeper.Keeper, msg MsgMimir) (cosmos.Context, error) {
	return validateMimirAuth(ctx, k, msg)
}
