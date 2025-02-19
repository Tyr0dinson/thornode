package ebifrost

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type EnshrinedBifrostPostDecorator struct {
	EnshrinedBifrost *EnshrinedBifrost
}

func NewEnshrineBifrostPostDecorator(eb *EnshrinedBifrost) *EnshrinedBifrostPostDecorator {
	return &EnshrinedBifrostPostDecorator{
		EnshrinedBifrost: eb,
	}
}

func (e *EnshrinedBifrostPostDecorator) PostHandle(ctx sdk.Context, tx sdk.Tx, simulate, success bool, next sdk.PostHandler) (newCtx sdk.Context, err error) {
	if simulate {
		return next(ctx, tx, simulate, success)
	}

	if !success {
		if _, ok := tx.(wInjectTx); ok {
			e.EnshrinedBifrost.logger.Error("InjectTx failed")
		}
		return next(ctx, tx, simulate, success)
	}

	// if the tx is a wInjectTx, then we need to inform enshrined bifrost that the tx has been processed.
	for _, msg := range tx.GetMsgs() {
		switch m := msg.(type) {
		case *types.MsgObservedTxInQuorum:
			e.EnshrinedBifrost.MarkQuorumTxAttestationsConfirmed(ctx, m.Tx)
		case *types.MsgObservedTxOutQuorum:
			e.EnshrinedBifrost.MarkQuorumTxAttestationsConfirmed(ctx, m.Tx)
		}
	}

	return next(ctx, tx, simulate, success)
}
