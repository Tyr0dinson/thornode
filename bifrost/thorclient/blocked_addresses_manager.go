package thorclient

import (
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// BlockListManager provides functionality to manage and retrieve blocked addresses
type BlockListManager interface {
	IsAddressBlocked(chain common.Chain, address string) bool
}

// BlockListMgr implements BlockListManager interface
type BlockListMgr struct {
	bridge    ThorchainBridge
	logger    zerolog.Logger
	lastCheck time.Time
	lock      *sync.Mutex
	addresses map[string]types.BlockListAddresses
}

// NewBlockListMgr creates a new instance of BlockListMgr
func NewBlockListMgr(bridge ThorchainBridge) *BlockListMgr {
	return &BlockListMgr{
		bridge: bridge,
		logger: log.With().Str("module", "block_list_mgr").Logger(),
		lock:   &sync.Mutex{},
	}
}

func (blm *BlockListMgr) updateBlockList() {
	blm.lock.Lock()
	defer blm.lock.Unlock()

	addresses, err := blm.bridge.GetBlockListAddresses()
	if err != nil {
		blm.logger.Err(err).Msgf("fail to get block list addresses: %s", err)
		return
	}
	blm.addresses = addresses
}

// IsAddressBlocked checks if a given address is blocked for a specific chain
func (blm *BlockListMgr) IsAddressBlocked(chain common.Chain, address string) bool {
	duration := time.Since(blm.lastCheck)
	if duration > constants.ThorchainBlockTime {
		blm.updateBlockList()
		blm.lastCheck = time.Now()
	}

	chainAddresses, ok := blm.addresses[chain.String()]
	if !ok {
		return false
	}

	for _, blockedAddr := range chainAddresses.Addresses {
		if blockedAddr == address {
			return true
		}
	}
	return false
}
