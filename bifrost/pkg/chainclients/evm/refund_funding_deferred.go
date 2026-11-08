package evm

import (
	"context"

	"gitlab.com/thorchain/thornode/v3/common"
)

// ValidateRefundFunding is a deferred no-op for EVM sources. The real EVM
// funding check depends on the EVM observation rework and lands in a follow-up
// (parity C4b), so EVM-funded refunds keep their current unguarded behaviour
// until then. The method exists so the signer's refund-funding guard recognises
// the EVM client as a declared validator rather than failing closed on a missing
// checker and blocking every EVM refund.
func (c *EVMClient) ValidateRefundFunding(_ context.Context, _ common.ObservedTx) error {
	return nil
}
