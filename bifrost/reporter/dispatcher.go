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

type reportTask struct {
	from   triggeredFrom
	height int64
}

const (
	chainTaskQueueSize = 64

	// - TRON uses height-19 for solvency reports.
	// - SOL rounds report height down to the nearest 10-slot boundary.
	tronSolvencyFinalityBlocks    = 19
	solanaSolvencyHeightFloorStep = 10
)

type dispatcher struct {
	logger          zerolog.Logger
	reportQueue     chan ReportBatch
	stopper         chan struct{}
	wg              *sync.WaitGroup
	chains          map[common.Chain]ChainClient
	thorchainBridge ThorchainBridge
	errCounter      *prometheus.CounterVec
	reporters       map[common.Chain]Reporters
	chainTaskQueues map[common.Chain]chan reportTask
}

// NewDispatcher creates and starts a reporter dispatcher for all configured chains.
// It initializes per-chain reporters and async workers.
func NewDispatcher(chains map[common.Chain]ChainClient, thorchainBridge ThorchainBridge, errCounter *prometheus.CounterVec, stopper chan struct{}, wg *sync.WaitGroup) Dispatcher {
	logger := log.Logger.With().Str("module", "reporter_dispatcher").Logger()
	reportQueueSize := len(chains)
	if reportQueueSize < 1 {
		reportQueueSize = 1
	}
	// Keep one batch slot per configured chain so collectors are less coupled to
	// THORChain RPC latency while still keeping the queue tightly bounded.
	reportQueue := make(chan ReportBatch, reportQueueSize)
	reporters := make(map[common.Chain]Reporters, len(chains))
	chainTaskQueues := make(map[common.Chain]chan reportTask, len(chains))
	for _, client := range chains {
		chain := client.GetChain()
		if chainRequiresSolvencyBlocks(chain) && client.GetConfig().SolvencyBlocks <= 0 {
			logger.Warn().
				Stringer("chain", chain).
				Int64("solvency_blocks", client.GetConfig().SolvencyBlocks).
				Msg("invalid solvency_blocks: solvency reporting disabled for this chain")
		}
		reporters[chain] = createReporters(client, thorchainBridge)
		chainTaskQueues[chain] = make(chan reportTask, chainTaskQueueSize)
	}

	d := &dispatcher{
		logger:          logger,
		stopper:         stopper,
		wg:              wg,
		chains:          chains,
		thorchainBridge: thorchainBridge,
		errCounter:      errCounter,
		reportQueue:     reportQueue,
		reporters:       reporters,
		chainTaskQueues: chainTaskQueues,
	}

	d.start()

	return d
}

// start launches dispatcher background workers:
// - periodic chain runner
// - outbound report queue processor
// - per-chain workers that serialize reporter work for each chain
func (d *dispatcher) start() {
	for chain, taskQueue := range d.chainTaskQueues {
		d.wg.Add(1)
		go d.processChainQueue(chain, taskQueue)
	}
	d.wg.Add(2)
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
		d.wg.Done()
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
	d.enqueueProcess(chain, fromBlockScanner, height)
}

func (d *dispatcher) processChainQueue(chain common.Chain, taskQueue <-chan reportTask) {
	defer d.wg.Done()

	logger := d.logger.With().Stringer("chain", chain).Logger()
	logger.Debug().Msg("chain reporter worker started")
	defer logger.Debug().Msg("chain reporter worker stopped")

	for {
		select {
		case <-d.stopper:
			return
		case task := <-taskQueue:
			d.processInternal(chain, task.from, task.height)
		}
	}
}

func (d *dispatcher) enqueueProcess(chain common.Chain, from triggeredFrom, height int64) {
	taskQueue, ok := d.chainTaskQueues[chain]
	if !ok {
		d.logger.Error().
			Stringer("chain", chain).
			Stringer("from", from).
			Int64("height", height).
			Msg("internal error: chain task queue not found")
		return
	}

	task := reportTask{from: from, height: height}
	select {
	case taskQueue <- task:
		return
	case <-d.stopper:
		return
	default:
	}

	// Backpressure policy:
	// - runner ticks are periodic and can be dropped when busy;
	// - block-scanner triggers should prefer the latest height, so drop one stale task and enqueue the latest.
	if from == fromRunner {
		d.logger.Debug().
			Stringer("chain", chain).
			Int("queue_len", len(taskQueue)).
			Msg("dropping runner task: chain task queue is full")
		return
	}

	select {
	case <-taskQueue:
	default:
	}
	select {
	case taskQueue <- task:
		d.logger.Warn().
			Stringer("chain", chain).
			Stringer("from", from).
			Int64("height", height).
			Msg("chain task queue full: dropped one stale task and enqueued latest")
	case <-d.stopper:
	default:
		d.logger.Warn().
			Stringer("chain", chain).
			Stringer("from", from).
			Int64("height", height).
			Msg("chain task queue full: dropping task")
	}
}

// processInternal processes one reporting task.
// This method is invoked by a dedicated per-chain worker, so ShouldReport and Collect
// are serialized per chain by design.
func (d *dispatcher) processInternal(chain common.Chain, from triggeredFrom, height int64) {
	logger := d.logger.With().Stringer("chain", chain).Stringer("from", from).Int64("height", height).Logger()

	var allMsgs, reportedMsgs ReportBatch
	taskHeight, err := d.resolveTaskHeight(chain, from, height)
	if err != nil {
		logger.Err(err).Msg("failed to resolve report height")
		return
	}
	reporters, ok := d.reporters[chain]
	if !ok {
		logger.Error().Msg("internal error: reporters not found for chain")
		return
	}
	for _, reporter := range reporters {
		if !reporter.ShouldReport(from, taskHeight) {
			continue
		}
		// collect all report messages from this reporter
		reportedMsgs, err = reporter.Collect(from, taskHeight)
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

func (d *dispatcher) resolveTaskHeight(chain common.Chain, from triggeredFrom, height int64) (int64, error) {
	if height > 0 {
		return height, nil
	}

	client, ok := d.chains[chain]
	if !ok {
		return 0, fmt.Errorf("chain client not found")
	}

	switch from {
	case fromRunner:
		h, err := client.GetHeight()
		if err != nil {
			return 0, fmt.Errorf("failed to get chain height: %w", err)
		}
		return h, nil
	case fromBlockScanner:
		h, err := client.GetBlockScannerHeight()
		if err != nil {
			return 0, fmt.Errorf("failed to get block scanner height: %w", err)
		}
		return h, nil
	default:
		return 0, fmt.Errorf("unknown trigger source: %s", from)
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
		d.logger.Info().
			Stringer("node_status", nodeStatus).
			Int("report_count", len(allMsgs)).
			Msg("skipping broadcast of collected reports to thorchain: local node is not active")
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
