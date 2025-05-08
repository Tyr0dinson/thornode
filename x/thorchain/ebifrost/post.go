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
	if !simulate && !success {
		if _, ok := tx.(wInjectTx); !ok {
			return next(ctx, tx, simulate, success)
		}

		msgs := tx.GetMsgs()
		if len(msgs) == 0 {
			return next(ctx, tx, simulate, success)
		}

		msg := msgs[0]
		switch m := msg.(type) {
		case *types.MsgObservedTxQuorum:
			tx := m.QuoTx.ObsTx.Tx
			e.EnshrinedBifrost.logger.Warn(
				"POTENTIAL EBIFROST LEAK: inject quorum tx failed and not processed",
				"tx_id", tx.ID,
				"chain", tx.Chain,
			)
		case *types.MsgNetworkFeeQuorum:
			nf := m.QuoNetFee.NetworkFee
			e.EnshrinedBifrost.logger.Warn(
				"POTENTIAL EBIFROST LEAK: inject quorum net fee failed and not processed",
				"chain", nf.Chain,
				"height", nf.Height,
			)
		case *types.MsgSolvencyQuorum:
			s := m.QuoSolvency.Solvency
			e.EnshrinedBifrost.logger.Warn(
				"POTENTIAL EBIFROST LEAK: inject quorum solvency failed and not processed",
				"chain", s.Chain,
				"pub_key", s.PubKey,
				"height", s.Height,
				"coins", s.Coins.String(),
			)
		case *types.MsgErrataTxQuorum:
			etx := m.QuoErrata.ErrataTx
			e.EnshrinedBifrost.logger.Warn(
				"POTENTIAL EBIFROST LEAK: inject quorum errata tx failed and not processed",
				"tx_id", etx.Id,
				"chain", etx.Chain,
			)
		}
		return next(ctx, tx, simulate, success)
	}
	if simulate || !success {
		return next(ctx, tx, simulate, success)
	}

	if _, ok := tx.(wInjectTx); !ok {
		return next(ctx, tx, simulate, success)
	}

	// if the tx is a wInjectTx, then we need to inform enshrined bifrost that the tx has been processed.
	for _, msg := range tx.GetMsgs() {
		switch m := msg.(type) {
		case *types.MsgObservedTxQuorum:
			e.EnshrinedBifrost.MarkQuorumTxAttestationsConfirmed(ctx, m.QuoTx)
		case *types.MsgNetworkFeeQuorum:
			e.EnshrinedBifrost.MarkQuorumNetworkFeeAttestationsConfirmed(ctx, m.QuoNetFee)
		case *types.MsgSolvencyQuorum:
			e.EnshrinedBifrost.MarkQuorumSolvencyAttestationsConfirmed(ctx, m.QuoSolvency)
		case *types.MsgErrataTxQuorum:
			e.EnshrinedBifrost.MarkQuorumErrataTxAttestationsConfirmed(ctx, m.QuoErrata)
		}
	}

	return next(ctx, tx, simulate, success)
}
