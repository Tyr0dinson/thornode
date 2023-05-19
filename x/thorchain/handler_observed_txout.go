package thorchain

import (
	"context"
	"fmt"
	"strings"

	"github.com/armon/go-metrics"
	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

// ObservedTxOutHandler process MsgObservedTxOut messages
type ObservedTxOutHandler struct {
	mgr Manager
}

// NewObservedTxOutHandler create a new instance of ObservedTxOutHandler
func NewObservedTxOutHandler(mgr Manager) ObservedTxOutHandler {
	return ObservedTxOutHandler{
		mgr: mgr,
	}
}

// Run is the main entry point for ObservedTxOutHandler
func (h ObservedTxOutHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgObservedTxOut)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgObserveTxOut failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to handle MsgObserveTxOut", "error", err)
	}
	return result, err
}

func (h ObservedTxOutHandler) validate(ctx cosmos.Context, msg MsgObservedTxOut) error {
	version := h.mgr.GetVersion()
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.validateV1(ctx, msg)
	}
	return errInvalidVersion
}

func (h ObservedTxOutHandler) validateV1(ctx cosmos.Context, msg MsgObservedTxOut) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	if !isSignedByActiveNodeAccounts(ctx, h.mgr.Keeper(), msg.GetSigners()) {
		return cosmos.ErrUnauthorized(fmt.Sprintf("%+v are not authorized", msg.GetSigners()))
	}

	return nil
}

func (h ObservedTxOutHandler) handle(ctx cosmos.Context, msg MsgObservedTxOut) (*cosmos.Result, error) {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("1.128.0")):
		return h.handleV128(ctx, msg)
	case version.GTE(semver.MustParse("1.124.0")):
		return h.handleV124(ctx, msg)
	case version.GTE(semver.MustParse("1.112.0")):
		return h.handleV112(ctx, msg)
	case version.GTE(semver.MustParse("1.109.0")):
		return h.handleV109(ctx, msg)
	case version.GTE(semver.MustParse("1.96.0")):
		return h.handleV96(ctx, msg)
	case version.GTE(semver.MustParse("1.89.0")):
		return h.handleV89(ctx, msg)
	case version.GTE(semver.MustParse("0.58.0")):
		return h.handleV58(ctx, msg)
	}
	return nil, errBadVersion
}

func (h ObservedTxOutHandler) preflight(ctx cosmos.Context, voter ObservedTxVoter, nas NodeAccounts, tx ObservedTx, signer cosmos.AccAddress) (ObservedTxVoter, bool) {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("1.123.0")):
		return h.preflightV123(ctx, voter, nas, tx, signer)
	case version.GTE(semver.MustParse("1.116.0")):
		return h.preflightV116(ctx, voter, nas, tx, signer)
	case version.GTE(semver.MustParse("1.89.0")):
		return h.preflightV89(ctx, voter, nas, tx, signer)
	default:
		return h.preflightV1(ctx, voter, nas, tx, signer)
	}
}

func (h ObservedTxOutHandler) preflightV123(ctx cosmos.Context, voter ObservedTxVoter, nas NodeAccounts, tx ObservedTx, signer cosmos.AccAddress) (ObservedTxVoter, bool) {
	observeSlashPoints := h.mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	observeFlex := h.mgr.Keeper().GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
	ok := false

	slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
		telemetry.NewLabel("reason", "failed_observe_txout"),
		telemetry.NewLabel("chain", string(tx.Tx.Chain)),
	}))
	h.mgr.Slasher().IncSlashPoints(slashCtx, observeSlashPoints, signer)

	if err := h.mgr.Keeper().SetLastObserveHeight(ctx, tx.Tx.Chain, signer, tx.BlockHeight); err != nil {
		ctx.Logger().Error("fail to save last observe height", "error", err, "signer", signer, "chain", tx.Tx.Chain)
	}
	if !voter.Add(tx, signer) {
		// when the signer already sign it
		return voter, ok
	}
	parts := strings.Split(tx.Tx.Memo, ":")
	if len(parts) > 1 {
		inhash, err := common.NewTxID(parts[len(parts)-1])
		if err == nil {
			h.mgr.Keeper().SetObservedLink(ctx, inhash, tx.Tx.ID)
		}
	}
	if voter.HasFinalised(nas) {
		if voter.FinalisedHeight == 0 {
			ok = true
			voter.FinalisedHeight = ctx.BlockHeight()
			voter.Tx = voter.GetTx(nas)
			// tx has consensus now, so decrease the slashing point for all the signers whom voted for it
			h.mgr.Slasher().DecSlashPoints(slashCtx, observeSlashPoints, voter.GetConsensusSigners()...)

		} else if ctx.BlockHeight() <= (voter.FinalisedHeight+observeFlex) && voter.Tx.Equals(tx) {
			// event the tx had been processed , given the signer just a bit late , so we still take away their slash points
			h.mgr.Slasher().DecSlashPoints(slashCtx, observeSlashPoints, signer)
		}
	}
	h.mgr.Keeper().SetObservedTxOutVoter(ctx, voter)

	// Check to see if we have enough identical observations to process the transaction
	return voter, ok
}

// Handle a message to observe outbound tx
func (h ObservedTxOutHandler) handleV128(ctx cosmos.Context, msg MsgObservedTxOut) (*cosmos.Result, error) {
	activeNodeAccounts, err := h.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return nil, wrapError(ctx, err, "fail to get list of active node accounts")
	}

	handler := NewInternalHandler(h.mgr)

	for _, tx := range msg.Txs {
		// check we are sending from a valid vault
		if !h.mgr.Keeper().VaultExists(ctx, tx.ObservedPubKey) {
			ctx.Logger().Info("Not valid Observed Pubkey", tx.ObservedPubKey)
			continue
		}
		if tx.KeysignMs > 0 {
			keysignMetric, err := h.mgr.Keeper().GetTssKeysignMetric(ctx, tx.Tx.ID)
			if err != nil {
				ctx.Logger().Error("fail to get keysign metric", "error", err)
			} else {
				keysignMetric.AddNodeTssTime(msg.Signer, tx.KeysignMs)
				h.mgr.Keeper().SetTssKeysignMetric(ctx, keysignMetric)
			}
		}
		voter, err := h.mgr.Keeper().GetObservedTxOutVoter(ctx, tx.Tx.ID)
		if err != nil {
			ctx.Logger().Error("fail to get tx out voter", "error", err)
			continue
		}

		// check whether the tx has consensus
		voter, ok := h.preflight(ctx, voter, activeNodeAccounts, tx, msg.Signer)
		if !ok {
			if voter.FinalisedHeight == ctx.BlockHeight() {
				// we've already process the transaction, but we should still
				// update the observing addresses
				h.mgr.ObMgr().AppendObserver(tx.Tx.Chain, msg.GetSigners())
			}
			continue
		}
		ctx.Logger().Info("handleMsgObservedTxOut request", "Tx:", tx.String())

		// if memo isn't valid or its an inbound memo, slash the vault
		memo, _ := ParseMemoWithTHORNames(ctx, h.mgr.Keeper(), tx.Tx.Memo)
		if memo.IsEmpty() || memo.IsInbound() {
			vault, err := h.mgr.Keeper().GetVault(ctx, tx.ObservedPubKey)
			if err != nil {
				ctx.Logger().Error("fail to get vault", "error", err)
				continue
			}
			toSlash := make(common.Coins, len(tx.Tx.Coins))
			copy(toSlash, tx.Tx.Coins)
			toSlash = toSlash.Adds_deprecated(tx.Tx.Gas.ToCoins())

			slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
				telemetry.NewLabel("reason", "sent_extra_funds"),
				telemetry.NewLabel("chain", string(tx.Tx.Chain)),
			}))

			if err := h.mgr.Slasher().SlashVault(slashCtx, tx.ObservedPubKey, toSlash, h.mgr); err != nil {
				ctx.Logger().Error("fail to slash account for sending extra fund", "error", err)
			}
			vault.SubFunds(toSlash)
			if err := h.mgr.Keeper().SetVault(ctx, vault); err != nil {
				ctx.Logger().Error("fail to save vault", "error", err)
			}

			continue
		}

		txOut := voter.GetTx(activeNodeAccounts) // get consensus tx, in case our for loop is incorrect
		txOut.Tx.Memo = tx.Tx.Memo
		m, err := processOneTxIn(ctx, h.mgr.GetVersion(), h.mgr.Keeper(), txOut, msg.Signer)
		if err != nil || tx.Tx.Chain.IsEmpty() {
			ctx.Logger().Error("fail to process txOut",
				"error", err,
				"tx", tx.Tx.String())
			continue
		}

		// add addresses to observing addresses. This is used to detect
		// active/inactive observing node accounts
		h.mgr.ObMgr().AppendObserver(tx.Tx.Chain, txOut.GetSigners())

		// emit tss keysign metrics
		if tx.KeysignMs > 0 {
			keysignMetric, err := h.mgr.Keeper().GetTssKeysignMetric(ctx, tx.Tx.ID)
			if err != nil {
				ctx.Logger().Error("fail to get tss keysign metric", "error", err, "hash", tx.Tx.ID)
			} else {
				evt := NewEventTssKeysignMetric(keysignMetric.TxID, keysignMetric.GetMedianTime())
				if err := h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
					ctx.Logger().Error("fail to emit tss metric event", "error", err)
				}
			}
		}

		// Thanks to CacheContext, if the handler returns an error
		// it will have caused no state changes.
		// Take an error as a reason to reimburse the transaction's gas
		// together with it in the form of a bond slash,
		// and not from the Reserve.
		_, err = handler(ctx, m)
		if err != nil {
			ctx.Logger().Error("handler failed:", "error", err)

			// Set the reason according to the type of message.
			var reason string = ""
			switch msgPointer := m.(type) {
			case *MsgOutboundTx:
				reason = "failed_outbound"

				inTxID := msgPointer.InTxID
				// send security alert for events that are not evm burn
				if !isOutboundFakeGasTX(tx) {
					report := fmt.Sprintf("missing tx out in=%s", inTxID)
					if err := h.mgr.EventMgr().EmitEvent(ctx, NewEventSecurity(tx.Tx, report)); err != nil {
						ctx.Logger().Error("fail to emit security event", "error", err)
					}
				}
			case *MsgRefundTx:
				reason = "failed_outbound"

				inTxID := msgPointer.InTxID
				// send security alert for events that are not evm burn
				if !isOutboundFakeGasTX(tx) {
					report := fmt.Sprintf("missing tx out in=%s", inTxID)
					if err := h.mgr.EventMgr().EmitEvent(ctx, NewEventSecurity(tx.Tx, report)); err != nil {
						ctx.Logger().Error("fail to emit security event", "error", err)
					}
				}
			case *MsgConsolidate:
				reason = "failed_consolidation"
			case *MsgMigrate:
				reason = "failed_migration"
			case *MsgRagnarok:
				reason = "failed_ragnarok"
			}
			if err := h.slashV128(ctx, tx, reason); err != nil {
				ctx.Logger().Error("slash failed:", "error", err)
			}
			continue
		}

		vault, err := h.mgr.Keeper().GetVault(ctx, tx.ObservedPubKey)
		if err != nil {
			ctx.Logger().Error("fail to get vault", "error", err)
			continue
		}
		// Apply Gas fees
		if err := addGasFees(ctx, h.mgr, tx); err != nil {
			ctx.Logger().Error("fail to add gas fee", "error", err)
			continue
		}
		// addGasFees deducts from the vault, so Get the vault again.
		vault, err = h.mgr.Keeper().GetVault(ctx, tx.ObservedPubKey)
		if err != nil {
			ctx.Logger().Error("fail to get vault", "error", err)
			continue
		}

		voter.SetDone()
		h.mgr.Keeper().SetObservedTxOutVoter(ctx, voter)
		// process the msg first , and then deduct the fund from vault last
		// If sending from one of our vaults, decrement coins
		vault.SubFunds(tx.Tx.Coins)
		vault.OutboundTxCount++
		if vault.IsAsgard() && memo.IsType(TxMigrate) {
			// only remove the block height that had been specified in the memo
			vault.RemovePendingTxBlockHeights(memo.GetBlockHeight())
		}

		if !vault.HasFunds() && vault.Status == RetiringVault {
			// we have successfully removed all funds from a retiring vault,
			// mark it as inactive
			vault.Status = InactiveVault
		}
		// if the vault is frozen, then unfreeze it. Since we saw that a
		// transaction was signed
		for _, coin := range tx.Tx.Coins {
			for i := range vault.Frozen {
				if strings.EqualFold(coin.Asset.GetChain().String(), vault.Frozen[i]) {
					vault.Frozen = append(vault.Frozen[:i], vault.Frozen[i+1:]...)
					break
				}
			}
		}
		if err := h.mgr.Keeper().SetVault(ctx, vault); err != nil {
			ctx.Logger().Error("fail to save vault", "error", err)
			continue
		}
	}
	return &cosmos.Result{}, nil
}

// isOutboundFakeGasTX returns true if the observed outbound which is missing an inbound is a "fake" tx sent purposely by bifrost
// this occurs on EVM chains where an outbound cannot be sent generally due to out of gas failures. In these cases, bifrost
// signs an outbound with the gas spent on the failure so that it can be accounted for.
// Checks are:
// - must only have one coin in outbound
// - chain of coin must be an EVM chain
// - coin asset must be the gas asset
// - coin amount must be 1
func isOutboundFakeGasTX(tx ObservedTx) bool {
	isLenCoins1 := len(tx.Tx.Coins) == 1
	if !isLenCoins1 {
		return false
	}
	asset := tx.Tx.Coins[0].Asset
	isChainEVM := asset.Chain.IsEVM()
	if !isChainEVM {
		return false
	}
	gasAsset := asset.Chain.GetGasAsset()
	isAssetGasAsset := asset.Equals(gasAsset)
	if !isAssetGasAsset {
		return false
	}

	if !tx.Tx.Coins[0].Amount.Equal(types.NewUint(1)) {
		return false
	}

	return true
}

func (h ObservedTxOutHandler) slashV128(ctx cosmos.Context, tx ObservedTx, reason string) error {
	// NewCoins so that Adds_deprecated does not change the voter's coins.
	toSlash := common.NewCoins(tx.Tx.Coins...)
	toSlash = toSlash.Adds_deprecated(tx.Tx.Gas.ToCoins())

	if reason != "" {
		ctx = ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
			telemetry.NewLabel("reason", reason),
			telemetry.NewLabel("chain", string(tx.Tx.Chain)),
		}))
	}

	// Record the loss of the observed funds from the vault.
	vault, err := h.mgr.Keeper().GetVault(ctx, tx.ObservedPubKey)
	if err != nil {
		ctx.Logger().Error("fail to get vault", "error", err)
	} else {
		vault.SubFunds(toSlash)
		if err := h.mgr.Keeper().SetVault(ctx, vault); err != nil {
			ctx.Logger().Error("fail to save vault", "error", err)
		}
	}

	return h.mgr.Slasher().SlashVault(ctx, tx.ObservedPubKey, toSlash, h.mgr)
}

// ObservedTxOutAnteHandler called by the ante handler to gate mempool entry
// and also during deliver. Store changes will persist if this function
// succeeds, regardless of the success of the transaction.
func ObservedTxOutAnteHandler(ctx cosmos.Context, v semver.Version, k keeper.Keeper, msg MsgObservedTxOut) error {
	if !isSignedByActiveNodeAccounts(ctx, k, msg.GetSigners()) {
		return cosmos.ErrUnauthorized(fmt.Sprintf("%+v are not authorized", msg.GetSigners()))
	}
	return nil
}
