package abci

import (
	abci "github.com/cometbft/cometbft/abci/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	enshrinedbifrost "gitlab.com/thorchain/thornode/v3/x/thorchain/enshrined_bifrost"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type ProposalHandler struct {
	keeper  *keeper.Keeper
	bifrost *enshrinedbifrost.EnshrinedBifrost

	prepareProposalHandler sdk.PrepareProposalHandler
	processProposalHandler sdk.ProcessProposalHandler
}

func NewProposalHandler(
	k *keeper.Keeper,
	b *enshrinedbifrost.EnshrinedBifrost,
	nextPrepareProposalHandler sdk.PrepareProposalHandler,
	nextProcessProposalHandler sdk.ProcessProposalHandler,
) *ProposalHandler {
	return &ProposalHandler{
		keeper:                 k,
		bifrost:                b,
		prepareProposalHandler: nextPrepareProposalHandler,
		processProposalHandler: nextProcessProposalHandler,
	}
}

func (h *ProposalHandler) PrepareProposal(ctx sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
	injectTxs := h.bifrost.ProposalInjectTxs(ctx)

	if len(injectTxs) > 0 {
		req.Txs = append(injectTxs, req.Txs...)
	}

	return h.prepareProposalHandler(ctx, req)
}

func (h *ProposalHandler) ProcessProposal(ctx sdk.Context, req *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
	return h.processProposalHandler(ctx, req)
}
