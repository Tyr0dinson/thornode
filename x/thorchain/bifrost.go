package thorchain

import (
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

type EnshrinedBifrost struct {
	cdc codec.Codec
}

func NewBifrost(cdc codec.Codec) *EnshrinedBifrost {
	return &EnshrinedBifrost{
		cdc: cdc,
	}
}

// ProposalInjectTxs is intended to be called by the current proposing validator during PrepareProposal
// and will return a list of in-quorum transactions to be included in the next block.
func (b *EnshrinedBifrost) ProposalInjectTxs(ctx sdk.Context) [][]byte {
	var injectTxs [][]byte

	return injectTxs
}
