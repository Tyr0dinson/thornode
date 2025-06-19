package thorchain

import (
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	"time"
)

// PriceFeedQuorum to handle MsgObservedTx
type PriceFeedQuorumHandler struct {
	mgr Manager
}

// NewPriceFeedQuorumHandler create a new instance of PriceFeedQuorumHandler
func NewPriceFeedQuorumHandler(mgr Manager) PriceFeedQuorumHandler {
	return PriceFeedQuorumHandler{
		mgr: mgr,
	}
}

// Run is the main entry point of PriceFeedQuorumHandler
func (h PriceFeedQuorumHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*types.MsgPriceFeedQuorum)
	if !ok {
		return nil, errInvalidMessage
	}

	err := h.validate(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("MsgPriceFeedQuorum failed validation", "error", err)
		return nil, err
	}

	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to handle MsgPriceFeedQuorum message", "error", err)
	}
	return result, err
}

func (h PriceFeedQuorumHandler) validate(ctx cosmos.Context, msg types.MsgPriceFeedQuorum) error {
	return msg.ValidateBasic()
}

func (h PriceFeedQuorumHandler) handle(ctx cosmos.Context, msg types.MsgPriceFeedQuorum) (*cosmos.Result, error) {
	active, err := h.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		err = wrapError(ctx, err, "fail to list active validators")
		return nil, err
	}

	pf := msg.QuoPriceFeed.PriceFeed

	now := time.Now().Format("15:04:05.000")
	ts := time.UnixMilli(pf.Time).Format("15:04:05.000")

	ctx.Logger().Info("handle price feed", "toracle", ts, "tnow", now, "node", msg.Signer.String())

	signBz, err := pf.GetSignablePayload()
	if err != nil {
		ctx.Logger().Error("fail to get signable price feed payload", "error", err)
		return &cosmos.Result{}, nil
	}

	atts := msg.QuoPriceFeed.Attestations

	if len(atts) == 0 {
		ctx.Logger().Error("no attestation found")
		return &cosmos.Result{}, nil
	}

	if len(atts) > 1 {
		ctx.Logger().Error("found more than one attestation", "error", err, "attestations", len(msg.QuoPriceFeed.Attestations))
	}

	att := atts[0]

	_, err = verifyQuorumAttestation(active, signBz, att)
	if err != nil {
		ctx.Logger().Error("fail to verify quorum price feed attestation", "error", err)
		return &cosmos.Result{}, nil
	}

	pk := secp256k1.PubKey{Key: att.PubKey}
	err = h.mgr.Keeper().SetPriceFeed(ctx, PriceFeed{
		Node:   cosmos.AccAddress(pk.Address()),
		Height: ctx.BlockHeight(),
		Rates:  pf.Rates,
	})

	if err != nil {
		ctx.Logger().Error("fail to set price feed", "error", err)
	}

	return &cosmos.Result{}, nil
}
