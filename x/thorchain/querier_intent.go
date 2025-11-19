package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (qs queryServer) queryIntent(ctx cosmos.Context, req *types.QueryIntentRequest) (*types.QueryIntentResponse, error) {
	// Convert string to TxID
	txID := common.TxID(req.TxId)

	// Get the intent
	intent, err := qs.mgr.Keeper().GetIntent(ctx, txID)
	if err != nil {
		return nil, fmt.Errorf("intent not found: %w", err)
	}

	// Get all solve bids for this intent
	solves, err := qs.mgr.Keeper().GetIntentSolvesByIntentID(ctx, txID)
	if err != nil {
		return nil, fmt.Errorf("failed to get intent solves: %w", err)
	}

	return &types.QueryIntentResponse{
		Intent: intent,
		Solves: solves,
	}, nil
}

func (qs queryServer) queryActiveIntents(ctx cosmos.Context, _ *types.QueryActiveIntentsRequest) (*types.QueryActiveIntentsResponse, error) {
	// Get all active intents
	activeIntents, err := qs.mgr.Keeper().GetActiveIntents(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get active intents: %w", err)
	}

	// Fetch each intent with its solves
	var intentsWithSolves []types.IntentWithSolves
	for _, intent := range activeIntents {
		// Get all solve bids for this intent
		solves, err := qs.mgr.Keeper().GetIntentSolvesByIntentID(ctx, intent.TxID)
		if err != nil {
			// Log error but use empty solves list
			ctx.Logger().Error("failed to get intent solves", "txid", intent.TxID, "error", err)
			solves = []types.IntentSolve{}
		}

		intentsWithSolves = append(intentsWithSolves, types.IntentWithSolves{
			Intent: intent,
			Solves: solves,
		})
	}

	return &types.QueryActiveIntentsResponse{
		Intents: intentsWithSolves,
	}, nil
}
