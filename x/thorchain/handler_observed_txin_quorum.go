package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ObservedTxInQuorumHandler to handle MsgObservedTxIn
type ObservedTxInQuorumHandler struct {
	mgr Manager
}

// NewObservedTxInQuorumHandler create a new instance of ObservedTxInQuorumHandler
func NewObservedTxInQuorumHandler(mgr Manager) ObservedTxInQuorumHandler {
	return ObservedTxInQuorumHandler{
		mgr: mgr,
	}
}

// Run is the main entry point of ObservedTxInQuorumHandler
func (h ObservedTxInQuorumHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*types.MsgObservedTxInQuorum)
	if !ok {
		return nil, errInvalidMessage
	}
	err := h.validate(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("MsgObservedTxInQuorum failed validation", "error", err)
		return nil, err
	}

	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to handle MsgObservedTxInQuorum message", "error", err)
	}
	return result, err
}

func (h ObservedTxInQuorumHandler) validate(ctx cosmos.Context, msg types.MsgObservedTxInQuorum) error {
	// ValidateBasic is also executed in message service router's handler and isn't versioned there
	return msg.ValidateBasic()
}

func (h ObservedTxInQuorumHandler) handle(ctx cosmos.Context, msg types.MsgObservedTxInQuorum) (*cosmos.Result, error) {
	k := h.mgr.Keeper()

	activeNodeAccounts, err := k.ListActiveValidators(ctx)
	if err != nil {
		return nil, wrapError(ctx, err, "fail to get list of active node accounts")
	}
	handler := NewInternalHandler(h.mgr)

	tx := msg.Tx.Tx

	// check we are sending to a valid vault
	voter, err := ensureVaultAndGetTxInVoter(ctx, tx.ObservedPubKey, tx.Tx.ID, k)
	if err != nil {
		ctx.Logger().Error("fail to ensure vault and get tx in voter", "error", err)
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
			ctx.Logger().Error("fail to verify quorum tx in attestation", "error", err)
			continue
		}

		accAddrs = append(accAddrs, accAddr)

		voter, isQuorum = processTxInAttestation(ctx, h.mgr, voter, activeNodeAccounts, tx, accAddr)
	}

	if err := handleObservedTxInQuorum(ctx, h.mgr, msg.Signer, activeNodeAccounts, handler, tx, voter, accAddrs, isQuorum); err != nil {
		ctx.Logger().Error("fail to handle observed tx in quorum", "error", err)
	}

	return &cosmos.Result{}, nil
}
