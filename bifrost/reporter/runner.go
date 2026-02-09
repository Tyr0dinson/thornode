package reporter

import (
	"time"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// runner periodically triggers per-chain report processing using a chain-specific
// interval callback. It fans-in ticker events for all chains and dispatches
// processing work asynchronously.
func (d *dispatcher) runner(getBackoffForChain func(chain common.Chain) time.Duration) {
	logger := log.Logger.With().Str("module", "reporter_runner").Logger()
	logger.Info().Msg("runner started")
	defer func() {
		d.wg.Done()
		logger.Info().Msg("runner stopped")
	}()

	// A single channel to receive ticks from all chain checkers
	tickChan := make(chan common.Chain, len(d.chains))

	// Create a ticker for each chain
	for _, client := range d.chains {
		// Determine the appropriate backoff/interval for this specific chain
		interval := getBackoffForChain(client.GetChain())
		if interval <= 0 {
			d.logger.Warn().
				Stringer("chain", client.GetChain()).
				Dur("interval", interval).
				Msg("non-positive interval; defaulting to ThorchainBlockTime")
			interval = constants.ThorchainBlockTime
		}
		// Launch a goroutine for each chain.
		ticker := time.NewTicker(interval)
		go func(ch common.Chain, ticker *time.Ticker) {
			for {
				select {
				case <-ticker.C:
					// attempt to enqueue, abort if stopping
					select {
					case tickChan <- ch:
					case <-d.stopper:
						ticker.Stop()
						return
					}
				case <-d.stopper:
					ticker.Stop()
					return
				}
			}
		}(client.GetChain(), ticker)
	}

	// Process ticks
	for {
		select {
		case <-d.stopper:
			return
		case chainToProcess := <-tickChan:
			// Check a specific chain
			d.logger.Debug().Stringer("chain", chainToProcess).Msg("runner triggered check for chain")
			d.wg.Add(1)
			go d.processInternal(chainToProcess, fromRunner, 0)
		}
	}
}
