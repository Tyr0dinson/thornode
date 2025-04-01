package ada

import (
	"sync"

	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/rs/zerolog/log"
)

var (
	txInBlock = sync.Map{} // used to track seen transactions
)

func chainSyncRollForwardHandler(
	ctx chainsync.CallbackContext,
	blockType uint,
	blockData any,
	tip chainsync.Tip,
) error {
	switch block := blockData.(type) {
	case ledger.Block:
		for _, tx := range block.Transactions() {
			txInBlock.Store(tx.Hash(), struct{}{})
		}
	case ledger.BlockHeader:
		log.Warn().Msg("block header, n2c expects a full block")
	}
	return nil
}

func chainSyncRollBackwardHandler(
	ctx chainsync.CallbackContext,
	point ocommon.Point,
	tip chainsync.Tip,
) error {
	return nil
}
