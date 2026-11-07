package solana

import (
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signing"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// outboundHaltHeightReader is implemented by the production thorchain bridge: the
// HaltOutbound mimirs and the Asgard vault set, both read as of a consensus height.
// Test bridges may not implement it; the durable serialize gate then skips no
// sibling for the halt (its behaviour before HaltOutbound existed).
type outboundHaltHeightReader interface {
	signing.MimirAtHeightReader
	GetAsgardsAtHeight(height int64) (ttypes.Vaults, error)
}

// outboundHaltSkipsAtHeight returns a predicate reporting whether HaltOutbound,
// as of the keysign height, stops a queued SOL sibling from being signed (an
// external outbound under HALTOUTBOUND or HaltOutboundSOL; see
// signing.IsInternalTxOut). Such a sibling never consumes the vault's durable
// nonce while the halt holds, so it must not hold a later durable outbound (a
// MIGRATE, say) behind it.
//
// Both reads are pinned to height, so every member of the keysign party decides
// from the same snapshot. They are made once, on the first call, and only when a
// sibling would otherwise hold tx back. A read error skips nothing: the gate
// then waits as before and retries (never a guess).
func (c *SOLClient) outboundHaltSkipsAtHeight(height int64) func(thorclient.PendingOutbound) bool {
	r, ok := c.bridge.(outboundHaltHeightReader)
	if !ok {
		return func(thorclient.PendingOutbound) bool { return false }
	}
	var (
		loaded bool
		halted bool
		vaults ttypes.Vaults
	)
	return func(p thorclient.PendingOutbound) bool {
		if !loaded {
			loaded = true
			h, _, err := signing.CheckOutboundHaltAtHeight(r, common.SOLChain, height)
			if err != nil {
				c.logger.Err(err).Int64("height", height).Msg("fail to read outbound halt for durable-nonce serialization; waiting")
				return false
			}
			if h {
				if vaults, err = r.GetAsgardsAtHeight(height); err != nil {
					c.logger.Err(err).Int64("height", height).Msg("fail to read asgard vaults for durable-nonce serialization; waiting")
					return false
				}
				halted = true
			}
		}
		if !halted {
			return false
		}
		return !signing.IsInternalTxOut(stypes.TxOutItem{
			Chain:        common.SOLChain,
			ToAddress:    common.Address(p.ToAddress),
			Memo:         p.Memo,
			OriginalMemo: p.OriginalMemo,
		}, vaults)
	}
}
