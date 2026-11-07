package thorclient

import (
	"context"
	"fmt"

	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// GetRefundFundingVoter bounds the source-voter request for signing.
func (b *thorchainBridge) GetRefundFundingVoter(ctx context.Context, txID string) (*stypes.QueryObservedTxVoter, bool, error) {
	return b.getObservedTxVoterUncached(ctx, txID, b.getThorChainURL(fmt.Sprintf(TxDetailEndpoint, txID)))
}
