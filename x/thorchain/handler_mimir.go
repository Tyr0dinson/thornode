package thorchain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

var (
	mimirValidKey    = regexp.MustCompile(`^[a-zA-Z-]+$`).MatchString
	mimirValidKeyV95 = regexp.MustCompile(constants.MimirKeyRegex).MatchString
)

// MimirHandler is to handle admin messages
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
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("1.114.0")):
		return h.validateV114(ctx, msg)
	case version.GTE(semver.MustParse("1.106.0")):
		return h.validateV106(ctx, msg)
	case version.GTE(semver.MustParse("1.95.0")):
		return h.validateV95(ctx, msg)
	case version.GTE(semver.MustParse("0.78.0")):
		return h.validateV78(ctx, msg)
	default:
		return errBadVersion
	}
}

func (h MimirHandler) validateV114(ctx cosmos.Context, msg MsgMimir) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}
	if !mimirValidKeyV95(msg.Key) || len(msg.Key) > 64 {
		return cosmos.ErrUnknownRequest("invalid mimir key")
	}
	if err := validateMimirAuth(ctx, h.mgr.Keeper(), msg); err != nil {
		return err
	}
	return nil
}

func (h MimirHandler) handle(ctx cosmos.Context, msg MsgMimir) error {
	ctx.Logger().Info("handleMsgMimir request", "node", msg.Signer, "key", msg.Key, "value", msg.Value)
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("1.133.0")):
		return h.handleV133(ctx, msg)
	case version.GTE(semver.MustParse("1.125.0")):
		return h.handleV125(ctx, msg)
	case version.GTE(semver.MustParse("1.124.0")):
		return h.handleV124(ctx, msg)
	case version.GTE(semver.MustParse("1.112.0")):
		return h.handleV112(ctx, msg)
	case version.GTE(semver.MustParse("1.92.0")):
		return h.handleV92(ctx, msg)
	case version.GTE(semver.MustParse("1.87.0")):
		return h.handleV87(ctx, msg)
	case version.GTE(semver.MustParse("0.81.0")):
		return h.handleV81(ctx, msg)
	}
	ctx.Logger().Error(errInvalidVersion.Error())
	return errBadVersion
}

func (h MimirHandler) handleV133(ctx cosmos.Context, msg MsgMimir) error {
	if mimirV2ValidKey(msg.Key) {
		return h.mVer2V125(ctx, msg)
	} else {
		return h.handleV112(ctx, msg)
	}
}

func validateMimirAuth(ctx cosmos.Context, k keeper.Keeper, msg MsgMimir) error {
	if isAdmin(msg.Signer) {
		// If the signer is an admin key, check the admin access controls for this mimir.
		if !isAdminAllowedForMimir(msg.Key) {
			return cosmos.ErrUnauthorized(fmt.Sprintf("%s cannot set this mimir key", msg.Signer))
		}
	} else if !isSignedByActiveNodeAccounts(ctx, k, msg.GetSigners()) {
		return cosmos.ErrUnauthorized(fmt.Sprintf("%s is not authorized", msg.Signer))
	}
	return nil
}

// MimirAnteHandler called by the ante handler to gate mempool entry
// and also during deliver. Store changes will persist if this function
// succeeds, regardless of the success of the transaction.
func MimirAnteHandler(ctx cosmos.Context, v semver.Version, k keeper.Keeper, msg MsgMimir) error {
	return validateMimirAuth(ctx, k, msg)
}
