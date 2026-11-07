package types

import (
	"context"
	"strings"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

// RefundFundingValidator proves the deposit behind an already queued refund on
// the source chain. Success must use current canonical inclusion, successful
// execution, matching deposited funds and that occurrence's confirmation policy.
// An error holds this signing/broadcast attempt; it never retires the refund.
type RefundFundingValidator interface {
	ValidateRefundFunding(context.Context, common.ObservedTx) error
}

// RefundFundingHintValidator can use retained, non-final observation heights to
// locate a source after its hash index expires. Hints never replace canonical
// inclusion, matching funds or confirmation checks.
type RefundFundingHintValidator interface {
	ValidateRefundFundingWithHints(context.Context, common.ObservedTx, []int64) error
}

// RefundFundingMatches binds the funds credited by THORChain to a freshly
// extracted deposit. Gas is deliberately excluded: execution gas can change when
// an EVM transaction is re-mined without changing the deposited funds. The
// source memo is excluded because THORNode resolves reference/memoless memos.
func RefundFundingMatches(original common.ObservedTx, actual *stypes.TxInItem) bool {
	return actual != nil && common.TxID(actual.Tx).Equals(original.Tx.ID) &&
		refundFundingAddressEquals(original.Tx.Chain, actual.Sender, original.Tx.FromAddress.String()) &&
		refundFundingAddressEquals(original.Tx.Chain, actual.To, original.Tx.ToAddress.String()) &&
		actual.Coins.EqualsEx(original.Tx.Coins)
}

func refundFundingAddressEquals(chain common.Chain, actual, expected string) bool {
	if chain.IsEVM() {
		return strings.EqualFold(actual, expected)
	}
	return actual == expected
}
