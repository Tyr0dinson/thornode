package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ObservedTxOutQuorumHandler to handle MsgObservedTxOut
type ObservedTxOutQuorumHandler struct {
	mgr Manager
}

// NewObservedTxOutQuorumHandler create a new instance of ObservedTxOutQuorumHandler
func NewObservedTxOutQuorumHandler(mgr Manager) ObservedTxOutQuorumHandler {
	return ObservedTxOutQuorumHandler{
		mgr: mgr,
	}
}

// Run is the main entry point of ObservedTxOutQuorumHandler
func (h ObservedTxOutQuorumHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*types.MsgObservedTxOutQuorum)
	if !ok {
		return nil, errInvalidMessage
	}
	err := h.validate(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("MsgObservedTxOutQuorum failed validation", "error", err)
		return nil, err
	}

	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to handle MsgObservedTxOutQuorum message", "error", err)
	}
	return result, err
}

func (h ObservedTxOutQuorumHandler) validate(ctx cosmos.Context, msg types.MsgObservedTxOutQuorum) error {
	// ValidateBasic is also executed in message service router's handler and isn't versioned there
	return msg.ValidateBasic()
}

func (h ObservedTxOutQuorumHandler) handle(ctx cosmos.Context, msg types.MsgObservedTxOutQuorum) (*cosmos.Result, error) {
	activeNodeAccounts, err := h.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return nil, wrapError(ctx, err, "fail to get list of active node accounts")
	}
	handler := NewInternalHandler(h.mgr)

	tx := msg.Tx.Tx

	k := h.mgr.Keeper()

	voter, err := ensureVaultAndGetTxOutVoter(ctx, k, tx.ObservedPubKey, tx.Tx.ID, msg.GetSigners(), tx.KeysignMs)
	if err != nil {
		ctx.Logger().Error("fail to ensure vault and get tx out voter", "error", err)
		return &cosmos.Result{}, nil
	}

	signBz, err := msg.Tx.Tx.Tx.Marshal()
	if err != nil {
		ctx.Logger().Error("fail to marshal tx sign payload", "error", err)
		return &cosmos.Result{}, nil
	}

	var isQuorum bool
	var accAddrs []cosmos.AccAddress
	for _, att := range msg.Tx.Attestations {
		accAddr, err := verifyQuorumTxAttestation(activeNodeAccounts, signBz, att)
		if err != nil {
			ctx.Logger().Error("fail to verify quorum tx out attestation", "error", err)
			continue
		}
		accAddrs = append(accAddrs, accAddr)

		voter, isQuorum = processTxOutAttestation(ctx, h.mgr, voter, activeNodeAccounts, tx, accAddr)
	}

	if err := handleObservedTxOutQuorum(ctx, h.mgr, msg.Signer, activeNodeAccounts, handler, tx, voter, accAddrs, isQuorum); err != nil {
		ctx.Logger().Error("fail to handle observed tx out quorum", "error", err)
	}

	return &cosmos.Result{}, nil
}
