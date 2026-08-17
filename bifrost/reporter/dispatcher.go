package reporter

import (
	"context"
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

type reportTask struct {
	from   triggeredFrom
	height int64
}

const (
	chainTaskQueueSize = 64

	// TRON uses height-19 for solvency reports.
	tronSolvencyFinalityBlocks = 19
)

type dispatcher struct {
	ctx              context.Context
	logger           zerolog.Logger
	reportQueue      chan ReportBatch
	stopper          chan struct{}
	wg               *sync.WaitGroup
	chains           map[common.Chain]ChainClient
	thorchainBridge  ThorchainBridge
	solvencyAttester SolvencyAttester
	errCounter       *prometheus.CounterVec
	reporters        map[common.Chain]Reporters
	chainTaskQueues  map[common.Chain]chan reportTask
}

// NewDispatcher creates and starts a reporter dispatcher for all configured chains.
// It initializes per-chain reporters and async workers.
func NewDispatcher(chains map[common.Chain]ChainClient, thorchainBridge ThorchainBridge, solvencyAttester SolvencyAttester, errCounter *prometheus.CounterVec, stopper chan struct{}, wg *sync.WaitGroup) Dispatcher {
	logger := log.Logger.With().Str("module", "reporter_dispatcher").Logger()
	ctx, cancel := context.WithCancel(context.Background())
	reportQueueSize := max(len(chains), 1)
	// Keep one batch slot per configured chain so collectors are less coupled to
	// THORChain RPC latency while still keeping the queue tightly bounded.
	reportQueue := make(chan ReportBatch, reportQueueSize)
	reporters := make(map[common.Chain]Reporters, len(chains))
	chainTaskQueues := make(map[common.Chain]chan reportTask, len(chains))
	for _, client := range chains {
		chain := client.GetChain()
		reporters[chain] = createReporters(client, thorchainBridge)
		chainTaskQueues[chain] = make(chan reportTask, chainTaskQueueSize)
	}

	d := &dispatcher{
		ctx:              ctx,
		logger:           logger,
		stopper:          stopper,
		wg:               wg,
		chains:           chains,
		thorchainBridge:  thorchainBridge,
		solvencyAttester: solvencyAttester,
		errCounter:       errCounter,
		reportQueue:      reportQueue,
		reporters:        reporters,
		chainTaskQueues:  chainTaskQueues,
	}

	d.wg.Go(func() {
		<-d.stopper
		cancel()
	})

	d.start()

	return d
}

// start launches dispatcher background workers:
// - periodic chain runner
// - outbound report queue processor
// - per-chain workers that serialize reporter work for each chain
func (d *dispatcher) start() {
	for chain, taskQueue := range d.chainTaskQueues {
		d.wg.Go(func() {
			d.processChainQueue(chain, taskQueue)
		})
	}
	// A runner is only needed when at least one chain can receive periodic work.
	if len(d.chains) > 0 {
		d.wg.Go(d.runner)
	}
	d.wg.Go(d.processReportQueue)
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
			d.sendCollectedToThorchain(reportBatch)
		}
	}
}

// Process schedules an immediate block-scanner-triggered processing pass for one chain.
func (d *dispatcher) Process(chain common.Chain, height int64) {
	d.enqueueProcess(chain, fromBlockScanner, height)
}

func (d *dispatcher) processChainQueue(chain common.Chain, taskQueue <-chan reportTask) {
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
	// - block-scanner triggers should prefer the latest height, so on overflow
	//   we best-effort remove one queued task and enqueue the latest.
	//
	// This is intentionally lossy: the queue can change between the failed send
	// above and the drain below, so the dropped task is not guaranteed to be the
	// one that made the queue look full or to be truly stale.
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
			Msg("chain task queue full: best-effort dropped one queued task and enqueued latest")
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

	var allMsgs ReportBatch
	reporters, ok := d.reporters[chain]
	if !ok {
		logger.Error().Msg("internal error: reporters not found for chain")
		return
	}
	if height <= 0 {
		eligibleReporters := make(Reporters, 0, len(reporters))
		for _, rep := range reporters {
			if rep.ShouldResolveHeight(from) {
				eligibleReporters = append(eligibleReporters, rep)
			}
		}
		if len(eligibleReporters) == 0 {
			return
		}
		reporters = eligibleReporters
	}

	taskHeight, err := d.resolveTaskHeight(chain, from, height)
	if err != nil {
		logger.Err(err).Msg("failed to resolve report height")
		return
	}
	for _, rep := range reporters {
		// Re-evaluate the full predicate after resolving the height so a halt
		// cleared while this task was queued or resolving is respected.
		if !rep.ShouldReport(from, taskHeight) {
			continue
		}
		// collect all report messages from this reporter
		reportedMsgs, collectErr := rep.Collect(from, taskHeight)
		if collectErr != nil {
			logger.Err(collectErr).Stringer("reporter", rep).Msg("fail to collect reports")
			continue
		}
		allMsgs = append(allMsgs, reportedMsgs...)
	}
	if len(allMsgs) > 0 {
		// Use an explicit timer so the timeout can be cleaned up immediately when
		// the batch is enqueued or shutdown wins the race.
		timer := time.NewTimer(constants.ThorchainBlockTime)
		defer func() {
			// Stop the timer on the non-timeout paths. If Stop returns false, the
			// timer has already fired, so best-effort drain the tick if it is still
			// pending on the channel.
			if timer.Stop() {
				return
			}

			select {
			case <-timer.C:
			default:
			}
		}()

		select {
		case d.reportQueue <- allMsgs:
			// Successfully sent the batch
		case <-d.stopper:
			logger.Debug().Msg("dispatcher stopped, dropping report batch")
			return
		case <-timer.C:
			if d.errCounter != nil {
				d.errCounter.WithLabelValues("fail_to_enqueue_report_batch", "").Inc()
			}
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

// sendCollectedToThorchain routes each collected reporter message through the
// observer attestation path, which owns active-validator eligibility checks.
func (d *dispatcher) sendCollectedToThorchain(allMsgs ReportBatch) {
	for _, msg := range allMsgs {
		if msg == nil {
			d.logger.Error().Msg("skip nil report message")
			continue
		}
		if err := d.sendMsg(msg); err != nil {
			if d.errCounter != nil {
				d.errCounter.WithLabelValues("fail_to_broadcast_solvency", "").Inc()
			}
			d.logger.Error().Err(err).Str("msg_type", sdk.MsgTypeURL(msg)).Msg("fail to attest solvency")
			continue
		}
		d.logger.Info().Str("msg_type", sdk.MsgTypeURL(msg)).Msg("report message sent to thorchain")
	}
}

// sendMsg routes reporter messages through the observer attestation path.
// Solvency is the only supported reporter today, so preserve the previous
// supermajority-gated observer behavior instead of directly broadcasting
// MsgSolvency transactions to THORChain.
func (d *dispatcher) sendMsg(msg sdk.Msg) error {
	solvencyMsg, ok := msg.(*types.MsgSolvency)
	if !ok {
		return fmt.Errorf("unsupported report message type: %s", sdk.MsgTypeURL(msg))
	}

	if d.solvencyAttester == nil {
		return fmt.Errorf("missing solvency attester for report message: %s", sdk.MsgTypeURL(msg))
	}

	ctx, cancel := context.WithTimeout(d.ctx, constants.ThorchainBlockTime)
	defer cancel()

	return d.solvencyAttester.AttestSolvency(ctx, common.Solvency{
		Chain:  solvencyMsg.Chain,
		PubKey: solvencyMsg.PubKey,
		Coins:  solvencyMsg.Coins,
		Height: solvencyMsg.Height,
	})
}
