package helper

import (
	"sync"
	"time"

	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type Helper struct {
	logger          zerolog.Logger
	chain           common.Chain
	bridge          thorclient.ThorchainBridge
	asgardAddresses map[string]bool
	asgardMtx       sync.RWMutex
	asgardTimestamp time.Time
}

func NewHelper(
	logger zerolog.Logger,
	chain common.Chain,
	bridge thorclient.ThorchainBridge,
) Helper {
	return Helper{
		logger:          logger.With().Str("module", "helper").Logger(),
		chain:           chain,
		bridge:          bridge,
		asgardAddresses: map[string]bool{},
		asgardMtx:       sync.RWMutex{},
	}
}

func (h *Helper) UpdateAsgardAddresses() error {
	h.asgardMtx.RLock()
	isValid := time.Since(h.asgardTimestamp) < constants.ThorchainBlockTime && h.asgardAddresses != nil
	h.asgardMtx.RUnlock()

	if isValid {
		return nil
	}

	addresses, err := h.bridge.GetAsgardAddresses(h.chain)
	if err != nil {
		return err
	}

	if len(addresses) == 0 {
		h.logger.Warn().Msg("No asgard addresses found")
	}

	asgardAddresses := make(map[string]bool)
	for _, address := range addresses {
		asgardAddresses[address.String()] = true
	}

	h.asgardMtx.Lock()
	defer h.asgardMtx.Unlock()

	h.asgardAddresses = asgardAddresses
	h.asgardTimestamp = time.Now()

	return nil
}

func (h *Helper) IsAsgardAddress(address string) bool {
	err := h.UpdateAsgardAddresses()
	if err != nil {
		h.logger.Warn().Err(err).Msg("failed to get asgard addresses")
		return false
	}

	h.asgardMtx.RLock()
	defer h.asgardMtx.RUnlock()

	_, found := h.asgardAddresses[address]
	return found
}
