package thorchain

import (
	"fmt"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TxOutFailHandler processes MsgTxOutFail messages originating from Bifrost.
type TxOutFailHandler struct {
	mgr Manager
}

// NewTxOutFailHandler creates a new TxOutFailHandler instance.
func NewTxOutFailHandler(mgr Manager) TxOutFailHandler {
	return TxOutFailHandler{mgr: mgr}
}

// Run is the main entry point for TxOutFailHandler.
func (h TxOutFailHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgTxOutFail)
	if !ok {
		return nil, errInvalidMessage
	}

	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgTxOutFail failed validation", "error", err)
		return nil, err
	}

	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("failed to process MsgTxOutFail", "error", err)
	}
	return result, err
}

func (h TxOutFailHandler) validate(ctx cosmos.Context, msg MsgTxOutFail) error {
	// ValidateBasic also executes in the message service router; keep here for parity.
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// Ensure the referenced TxOut exists and references the same inbound hash.
	_, _, item, err := h.getTxOutItem(ctx, msg)
	if err != nil {
		return err
	}

	// Authorize signer: active nodes always allowed, otherwise they must be members of the vault.
	if !isSignedByActiveNodeAccounts(ctx, h.mgr.Keeper(), msg.GetSigners()) {
		if !isVaultMember(ctx, h.mgr.Keeper(), item.VaultPubKey, msg.Signer) {
			return cosmos.ErrUnauthorized(fmt.Sprintf("%s is not authorized", msg.Signer))
		}
	}

	return nil
}

func (h TxOutFailHandler) handle(ctx cosmos.Context, msg MsgTxOutFail) (*cosmos.Result, error) {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("3.0.0")):
		return h.handleCurrent(ctx, msg)
	default:
		return nil, errBadVersion
	}
}

func (h TxOutFailHandler) handleCurrent(ctx cosmos.Context, msg MsgTxOutFail) (*cosmos.Result, error) {
	txOut, idx, item, err := h.getTxOutItem(ctx, msg)
	if err != nil {
		return nil, err
	}

	voterID := types.TxOutFailVoterID(msg.Height, msg.Index, msg.InHash)
	voter, err := h.mgr.Keeper().GetTxOutFailVoter(ctx, voterID)
	if err != nil {
		return nil, wrapError(ctx, err, "fail to fetch tx out fail voter")
	}
	if voter.Empty() {
		voter = types.NewTxOutFailVoter(msg.Height, msg.Index, msg.InHash)
	}
	voter.FailReason = msg.FailReason
	voter.Fatal = msg.Fatal

	_ = voter.Sign(msg.Signer)
	h.mgr.Keeper().SetTxOutFailVoter(ctx, voter)

	vault, err := h.mgr.Keeper().GetVault(ctx, item.VaultPubKey)
	if err != nil {
		return nil, wrapError(ctx, err, "fail to get vault")
	}
	if vault.IsEmpty() {
		return &cosmos.Result{}, nil
	}

	vaultMembers, err := h.vaultNodeAccounts(ctx, vault)
	if err != nil {
		return nil, err
	}

	if !voter.HasConsensus(vaultMembers) {
		return &cosmos.Result{}, nil
	}

	if !item.FatalFailure {
		item.FatalFailure = true
		item.FailReason = msg.FailReason
		txOut.TxArray[idx] = item
		if err := h.mgr.Keeper().SetTxOut(ctx, txOut); err != nil {
			return nil, wrapError(ctx, err, "fail to persist fatal outbound")
		}

		etx := common.Tx{
			ID:          item.InHash,
			Chain:       item.Chain,
			FromAddress: common.NoAddress,
			ToAddress:   item.ToAddress,
			Coins:       []common.Coin{item.Coin},
			Gas:         item.MaxGas,
			Memo:        item.Memo,
		}
		if err := h.mgr.EventMgr().EmitEvent(ctx, NewEventSecurity(etx, fmt.Sprintf("txout marked fatal: %s", msg.FailReason))); err != nil {
			ctx.Logger().Error("fail to emit security event", "error", err)
		}
	}

	ctx.Logger().Warn("marked outbound as fatal",
		"in_hash", item.InHash,
		"height", msg.Height,
		"index", msg.Index,
		"reason", msg.FailReason,
	)

	return &cosmos.Result{}, nil
}

func (h TxOutFailHandler) getTxOutItem(ctx cosmos.Context, msg MsgTxOutFail) (*TxOut, int, TxOutItem, error) {
	txOut, err := h.mgr.Keeper().GetTxOut(ctx, msg.Height)
	if err != nil {
		return nil, 0, TxOutItem{}, wrapError(ctx, err, "fail to get tx out")
	}
	if txOut == nil {
		return nil, 0, TxOutItem{}, wrapError(ctx, fmt.Errorf("tx out not found"), "fail to get tx out")
	}

	idx := int(msg.Index)
	if idx < 0 || idx >= len(txOut.TxArray) {
		return nil, 0, TxOutItem{}, wrapError(ctx, fmt.Errorf("txout index out of range"), "invalid tx out index")
	}
	item := txOut.TxArray[idx]
	if !item.InHash.Equals(msg.InHash) {
		return nil, 0, TxOutItem{}, wrapError(ctx, fmt.Errorf("in hash mismatch"), "invalid tx out in hash")
	}
	return txOut, idx, item, nil
}

func (h TxOutFailHandler) vaultNodeAccounts(ctx cosmos.Context, vault Vault) (NodeAccounts, error) {
	var accounts NodeAccounts
	for _, member := range vault.GetMembership() {
		addr, err := member.GetThorAddress()
		if err != nil {
			return nil, wrapError(ctx, err, "fail to get thor address")
		}
		na, err := h.mgr.Keeper().GetNodeAccount(ctx, addr)
		if err != nil {
			return nil, wrapError(ctx, err, "fail to get node account")
		}
		accounts = append(accounts, na)
	}
	return accounts, nil
}

func isVaultMember(ctx cosmos.Context, k keeper.Keeper, vaultPubKey common.PubKey, signer cosmos.AccAddress) bool {
	vault, err := k.GetVault(ctx, vaultPubKey)
	if err != nil || vault.IsEmpty() {
		return false
	}
	for _, member := range vault.GetMembership() {
		addr, err := member.GetThorAddress()
		if err != nil {
			continue
		}
		if addr.Equals(signer) {
			return true
		}
	}
	return false
}

// TxOutFailAnteHandler gates mempool entry for MsgTxOutFail.
func TxOutFailAnteHandler(ctx cosmos.Context, v semver.Version, k keeper.Keeper, msg MsgTxOutFail) (cosmos.Context, error) {
	return activeNodeAccountsSignerPriority(ctx, k, msg.GetSigners())
}
