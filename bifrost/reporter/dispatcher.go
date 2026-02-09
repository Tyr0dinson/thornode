package reporter

import (
	"fmt"
	"sync"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// createReporters creates the reporter set for a chain client.
// At the moment only solvency reporting is supported.
func createReporters(client ChainClient, thorchainBridge ThorchainBridge) Reporters {
	// all chains report solvency
	reporters := Reporters{newSolvencyReporter(client, thorchainBridge)}

	// add other reporters here when implemented

	return reporters
}

// chainRequiresSolvencyBlocks identifies chains whose solvency cadence depends
// on the configured `solvency_blocks` value.
func chainRequiresSolvencyBlocks(chain common.Chain) bool {
	switch chain {
	case common.AVAXChain, common.BSCChain, common.BASEChain, common.XRPChain, common.SOLChain:
		return true
	default:
		return false
	}
}

type dispatcher struct {
	logger          zerolog.Logger
	reportQueue     chan ReportBatch
	stopper         chan struct{}
	chains          map[common.Chain]ChainClient
	thorchainBridge ThorchainBridge
	errCounter      *prometheus.CounterVec
	reporters       map[common.Chain]Reporters
	chainLocksMu    sync.RWMutex
	chainLocks      map[common.Chain]*sync.Mutex
}

// NewDispatcher creates and starts a reporter dispatcher for all configured chains.
// It initializes per-chain reporters, locking primitives, and async workers.
func NewDispatcher(chains map[common.Chain]ChainClient, thorchainBridge ThorchainBridge, errCounter *prometheus.CounterVec, stopper chan struct{}) Dispatcher {
	logger := log.Logger.With().Str("module", "reporter_dispatcher").Logger()
	reportQueue := make(chan ReportBatch)
	reporters := make(map[common.Chain]Reporters, len(chains))
	chainLocks := make(map[common.Chain]*sync.Mutex, len(chains))
	for _, client := range chains {
		chain := client.GetChain()
		if chainRequiresSolvencyBlocks(chain) && client.GetConfig().SolvencyBlocks <= 0 {
			logger.Warn().
				Stringer("chain", chain).
				Int64("solvency_blocks", client.GetConfig().SolvencyBlocks).
				Msg("invalid solvency_blocks: solvency reporting disabled for this chain")
		}
		reporters[chain] = createReporters(client, thorchainBridge)
		chainLocks[chain] = &sync.Mutex{}
	}

	d := &dispatcher{
		logger:          logger,
		stopper:         stopper,
		chains:          chains,
		thorchainBridge: thorchainBridge,
		errCounter:      errCounter,
		reportQueue:     reportQueue,
		reporters:       reporters,
		chainLocks:      chainLocks,
	}

	d.start()

	return d
}

// start launches dispatcher background workers:
// - periodic chain runner
// - outbound report queue processor
func (d *dispatcher) start() {
	go d.runner(
		// get chain-specific timing
		func(chain common.Chain) time.Duration {
			// You can define chain-specific check intervals here
			return constants.ThorchainBlockTime
		},
	)
	go d.processReportQueue()
}

// processReportQueue reads collected report batches and broadcasts them to THORChain.
func (d *dispatcher) processReportQueue() {
	d.logger.Info().Msg("reporter dispatcher (processReportQueue) started")
	defer func() {
		d.logger.Info().Msg("reporter dispatcher (processReportQueue) stopped")
	}()

	for {
		select {
		case <-d.stopper:
			return
		case reportBatch, more := <-d.reportQueue:
			if !more {
				return
			}
			// send all collected reporter messages to thorchain
			if err := d.sendCollectedToThorchain(reportBatch); err != nil {
				d.logger.Err(err).Msg("fail to send reports to thorchain")
			}
		}
	}
}

// Process schedules an immediate block-scanner-triggered processing pass for one chain.
func (d *dispatcher) Process(chain common.Chain, height int64) {
	go d.processInternal(chain, fromBlockScanner, height)
}

// processInternal can be invoked by the runner goroutine or by dispatcher.Process() (which spawns its own goroutine).
// Reporting is serialized per chain via a per-chain lock, so ShouldReport and Collect are also serialized per chain.
func (d *dispatcher) processInternal(chain common.Chain, from triggeredFrom, height int64) {
	logger := d.logger.With().Stringer("chain", chain).Stringer("from", from).Int64("height", height).Logger()

	lock := d.getChainLock(chain)
	lock.Lock()
	defer lock.Unlock()

	var err error
	var allMsgs, reportedMsgs ReportBatch
	reporters, ok := d.reporters[chain]
	if !ok {
		logger.Error().Msg("internal error: reporters not found for chain")
		return
	}
	for _, reporter := range reporters {
		if !reporter.ShouldReport(from, height) {
			continue
		}
		// collect all report messages from this reporter
		reportedMsgs, err = reporter.Collect(from)
		if err != nil {
			logger.Err(err).Stringer("reporter", reporter).Msg("fail to collect reports")
			continue
		}
		allMsgs = append(allMsgs, reportedMsgs...)
	}
	if len(allMsgs) > 0 {
		select {
		case d.reportQueue <- allMsgs:
			// Successfully sent the batch
		case <-d.stopper:
			logger.Debug().Msg("dispatcher stopped, dropping report batch")
			return
		case <-time.After(constants.ThorchainBlockTime):
			logger.Error().Msg("fail to send report batch to queue: timeout")
		}
	}
}

// sendCollectedToThorchain broadcasts each collected reporter message
// if the local THORNode is active.
func (d *dispatcher) sendCollectedToThorchain(allMsgs ReportBatch) error {
	nodeStatus, err := d.thorchainBridge.FetchNodeStatus()
	if err != nil {
		return fmt.Errorf("failed to get node status: %w", err)
	}

	if nodeStatus != types.NodeStatus_Active {
		return nil
	}

	for _, msg := range allMsgs {
		if msg == nil {
			d.logger.Error().Msg("skip nil report message")
			continue
		}
		if err := d.sendMsgToThorchain(msg); err != nil {
			if d.errCounter != nil {
				d.errCounter.WithLabelValues("fail_to_broadcast_report", "").Inc()
			}
			d.logger.Error().Err(err).Str("msg_type", sdk.MsgTypeURL(msg)).Msg("fail to broadcast report tx")
			continue
		}
		d.logger.Info().Str("msg_type", sdk.MsgTypeURL(msg)).Msg("report message sent to thorchain")
	}
	return nil
}

// sendMsgToThorchain broadcasts a reporter message to thornode.
func (d *dispatcher) sendMsgToThorchain(msg sdk.Msg) error {
	if _, err := d.thorchainBridge.Broadcast(msg); err != nil {
		return fmt.Errorf("fail to broadcast report message: %w", err)
	}
	return nil
}

// getChainLock returns a stable per-chain mutex used to serialize reporting work.
// It uses double-checked locking to lazily create missing locks.
func (d *dispatcher) getChainLock(chain common.Chain) *sync.Mutex {
	// Check if lock already present
	d.chainLocksMu.RLock()
	lock, ok := d.chainLocks[chain]
	d.chainLocksMu.RUnlock()
	if ok {
		return lock
	}

	// Lock not present so upgrade to write lock to create one
	d.chainLocksMu.Lock()
	defer d.chainLocksMu.Unlock()
	// Another goroutine might have created the lock
	if lock, ok = d.chainLocks[chain]; ok {
		return lock
	}

	// No lock yet, create and store a new one for this chain
	lock = &sync.Mutex{}
	d.chainLocks[chain] = lock
	return lock
}
