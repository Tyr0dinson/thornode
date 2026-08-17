package reporter

import (
	"time"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// runner periodically triggers one report pass for every configured chain.
// A single THORChain-paced ticker is sufficient because solvency reporting no
// longer depends on observing an exact external-chain block boundary.
func (d *dispatcher) runner() {
	logger := log.Logger.With().Str("module", "reporter_runner").Logger()
	logger.Info().Msg("runner started")
	defer func() {
		logger.Info().Msg("runner stopped")
	}()

	interval := constants.ThorchainBlockTime
	if interval <= 0 {
		logger.Error().Dur("interval", interval).Msg("reporter runner disabled: non-positive THORChain block time")
		<-d.stopper
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			d.processRunnerTick()
		case <-d.stopper:
			return
		}
	}
}

func (d *dispatcher) processRunnerTick() {
	for _, client := range d.chains {
		chain := client.GetChain()
		d.logger.Debug().Stringer("chain", chain).Msg("runner triggered check for chain")
		d.enqueueProcess(chain, fromRunner, 0)
	}
}
