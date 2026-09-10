package abci

import (
	"testing"

	"cosmossdk.io/log/v2"
	abci "github.com/cometbft/cometbft/abci/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

func TestProposalByteBudget(t *testing.T) {
	txs := [][]byte{{1, 2, 3}, {4, 5}}
	limit := cmttypes.ComputeProtoSizeForTxs([]cmttypes.Tx{txs[0], txs[1]})
	handler := ProposalHandler{prepareProposalHandler: func(ctx sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		// SDK v0.54 accounts for the protobuf envelope itself.
		selector := baseapp.NewDefaultTxSelector()
		for _, bz := range req.Txs {
			if selector.SelectTxForProposal(ctx, uint64(req.MaxTxBytes), 0, nil, bz) {
				break
			}
		}
		return &abci.ResponsePrepareProposal{Txs: selector.SelectedTxs(ctx)}, nil
	}}
	ctx := sdk.Context{}.WithLogger(log.NewNopLogger())
	response, err := handler.PrepareProposal(ctx, &abci.RequestPrepareProposal{Txs: txs, MaxTxBytes: limit})
	require.NoError(t, err)
	require.Equal(t, txs, response.Txs)
	response, err = handler.PrepareProposal(ctx, &abci.RequestPrepareProposal{Txs: txs, MaxTxBytes: limit - 1})
	require.NoError(t, err)
	require.Equal(t, txs[:1], response.Txs)
}
