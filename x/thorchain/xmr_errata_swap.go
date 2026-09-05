package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// revertedXMRSwap is a terminal execution/settlement barrier, not a swap
// failure. Ordinary failure handling would refund input which no longer exists.
// A failed voter read also defers execution without a payout or refund.
func revertedXMRSwap(ctx cosmos.Context, k keeper.Keeper, msg MsgSwap) (bool, error) {
	if !msg.Tx.Chain.Equals(common.XMRChain) {
		return false, nil
	}
	voter, err := k.GetObservedTxInVoter(ctx, msg.Tx.ID)
	if err != nil {
		return false, err
	}
	return voter.Reverted, nil
}
