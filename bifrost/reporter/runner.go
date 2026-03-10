package reporter

import (
	"time"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// runner periodically triggers per-chain report processing using a chain-specific
// interval callback. Each chain ticker enqueues directly to its per-chain worker queue.
func (d *dispatcher) runner(getBackoffForChain func(chain common.Chain) time.Duration) {
	logger := log.Logger.With().Str("module", "reporter_runner").Logger()
	logger.Info().Msg("runner started")
	defer func() {
		d.wg.Done()
		logger.Info().Msg("runner stopped")
	}()

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
		// Launch a goroutine for each chain ticker.
		ticker := time.NewTicker(interval)
		// Track each per-chain ticker separately so dispatcher shutdown waits for them too.
		d.wg.Add(1)
		go func(ch common.Chain, ticker *time.Ticker) {
			defer d.wg.Done()
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					// Check a specific chain.
					d.logger.Debug().Stringer("chain", ch).Msg("runner triggered check for chain")
					d.enqueueProcess(ch, fromRunner, 0)
				case <-d.stopper:
					return
				}
			}
		}(client.GetChain(), ticker)
	}

	<-d.stopper
}
