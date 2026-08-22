package monero

import "gitlab.com/thorchain/thornode/v3/common"

// ApplySpentOutputRefsWithOutcome keeps tests focused on retry and callback
// behavior without retaining an unguarded production entry point.
func (c *Client) ApplySpentOutputRefsWithOutcome(
	tx common.ObservedTx,
	onOutcome func(applied bool),
) {
	c.applySpentOutputRefs(tx, nil, nil, onOutcome)
}
