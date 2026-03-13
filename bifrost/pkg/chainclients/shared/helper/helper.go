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
	asgardMtx       sync.Mutex
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
		asgardMtx:       sync.Mutex{},
	}
}

func (h *Helper) UpdateAsgardAddresses() error {
	h.asgardMtx.Lock()
	defer h.asgardMtx.Unlock()

	if time.Since(h.asgardTimestamp) < constants.ThorchainBlockTime && h.asgardAddresses != nil {
		return nil
	}

	addresses, err := h.bridge.GetAsgardAddresses(h.chain)
	if err != nil {
		return err
	}

	// ensure we don't overwrite with empty list
	if len(addresses) > 0 {
		h.asgardAddresses = make(map[string]bool)
		for _, address := range addresses {
			h.asgardAddresses[address.String()] = true
		}

		h.asgardTimestamp = time.Now()
	}

	return nil
}

func (h *Helper) IsAsgardAddress(address string) bool {
	err := h.UpdateAsgardAddresses()
	if err != nil {
		h.logger.Err(err).Msg("failed to get asgard addresses")
		return false
	}

	h.asgardMtx.Lock()
	defer h.asgardMtx.Unlock()

	_, found := h.asgardAddresses[address]
	return found
}
