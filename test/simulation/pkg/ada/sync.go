package ada

import (
	"fmt"
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
		// log.Info().Msgf("block received slot=%d, height=%d, hash=%s", block.SlotNumber(), block.BlockNumber(), block.Hash())
		for _, tx := range block.Transactions() {
			// log.Info().Msgf("tx in block slot=%d, height=%d, hash=%s", block.SlotNumber(), block.BlockNumber(), tx.Hash())
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
	fmt.Printf("roll backward: point = (%d, %x), tip = (%d, %x)\n",
		point.Slot, point.Hash,
		tip.Point.Slot, tip.Point.Hash,
	)
	return nil
}
