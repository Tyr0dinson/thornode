package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// PriceFeedQuorum to handle MsgObservedTx
type PriceFeedQuorum struct {
	mgr Manager
}

// NewPriceFeedQuorum create a new instance of PriceFeedQuorum
func NewPriceFeedQuorum(mgr Manager) PriceFeedQuorum {
	return PriceFeedQuorum{
		mgr: mgr,
	}
}

// Run is the main entry point of PriceFeedQuorum
func (h PriceFeedQuorum) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
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

func (h PriceFeedQuorum) validate(ctx cosmos.Context, msg types.MsgPriceFeedQuorum) error {
	return msg.ValidateBasic()
}

func (h PriceFeedQuorum) handle(ctx cosmos.Context, msg types.MsgPriceFeedQuorum) (*cosmos.Result, error) {
	ctx.Logger().Info("handle PriceFeedQuorum", "rates", msg.QuoPriceFeed.PriceFeed.Rates)

	return &cosmos.Result{}, nil
}
