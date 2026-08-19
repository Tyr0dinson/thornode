package observer

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// we have one semaphore per peer (per active validator node), so we don't need to prune that often
// but we do need to prune it, because we don't want to keep semaphores of nodes that aren't
// online anymore around forever.
const semaphorePruneInterval = 5 * time.Minute

type AttestationBatcher struct {
	observedTxBatch []*common.AttestTx
	networkFeeBatch []*common.AttestNetworkFee
	solvencyBatch   []*common.AttestSolvency
	errataTxBatch   []*common.AttestErrataTx
	priceFeedBatch  []*common.AttestPriceFeed

	batchPool sync.Pool

	mu             sync.Mutex
	batchInterval  time.Duration
	maxBatchSize   int64
	maxBatchSizeMu sync.Mutex
	peerTimeout    time.Duration // Timeout for peer send

	getActiveValidators func() map[peer.ID]bool

	lastBatchSent time.Time
	batchTicker   *time.Ticker
	forceSendChan chan struct{} // Channel to signal immediate send

	host    host.Host
	logger  zerolog.Logger
	metrics *batchMetrics

	// xmrErrataBarrier is shared with AttestationGossip. Batch dequeue and
	// transmission take its reader so durable XMR errata cleanup can purge a
	// queued stale observation without racing an already-dequeued broadcast.
	xmrErrataBarrier *sync.RWMutex
	// beforeXMRBarrierRLock is a deterministic revalidation-test hook.
	// Production leaves it nil.
	beforeXMRBarrierRLock func()

	peerMgr           *peerManager
	broadcastToPeerFn func(context.Context, peer.ID, []byte)
}

// Metrics for the batcher
type batchMetrics struct {
	BatchSends      prometheus.Counter
	BatchClears     prometheus.Counter
	MessagesBatched *prometheus.CounterVec // By type
	BatchSize       prometheus.Histogram
	BatchSendTime   prometheus.Histogram
}

// NewAttestationBatcher creates a new instance of AttestationBatcher
func NewAttestationBatcher(
	host host.Host,
	logger zerolog.Logger,
	m *metrics.Metrics,
	batchInterval time.Duration,
	maxBatchSize int64,
	peerTimeout time.Duration,
	peerConcurrentSends int,
) *AttestationBatcher {
	// Create batch metrics
	batchMetrics := &batchMetrics{
		BatchSends:      m.GetCounter(metrics.BatchSends),
		BatchClears:     m.GetCounter(metrics.BatchClears),
		MessagesBatched: m.GetCounterVec(metrics.MessagesBatched),
		BatchSize:       m.GetHistograms(metrics.BatchSize),
		BatchSendTime:   m.GetHistograms(metrics.BatchSendTime),
	}

	logger = logger.With().Str("module", "attestation_batcher").Logger()

	b := &AttestationBatcher{
		// Initialize with empty slices with initial capacity
		observedTxBatch: make([]*common.AttestTx, 0, maxBatchSize),
		networkFeeBatch: make([]*common.AttestNetworkFee, 0, maxBatchSize),
		solvencyBatch:   make([]*common.AttestSolvency, 0, maxBatchSize),
		errataTxBatch:   make([]*common.AttestErrataTx, 0, maxBatchSize),

		batchInterval: batchInterval,
		maxBatchSize:  maxBatchSize,
		peerTimeout:   peerTimeout,

		peerMgr: newPeerManager(logger, peerConcurrentSends),

		lastBatchSent: time.Time{}, // Zero time

		host:    host,
		logger:  logger,
		metrics: batchMetrics,

		forceSendChan: make(chan struct{}, 1), // Buffer of 1 to avoid blocking
	}

	// sync.Pool for reusing AttestationBatch objects to reduce memory allocations.
	b.batchPool = sync.Pool{New: b.newAttestationBatch}

	return b
}

func (b *AttestationBatcher) getMaxBatchSize() int64 {
	b.maxBatchSizeMu.Lock()
	defer b.maxBatchSizeMu.Unlock()
	return b.maxBatchSize
}

func (b *AttestationBatcher) newAttestationBatch() interface{} {
	maxBatchSize := b.getMaxBatchSize()
	return &common.AttestationBatch{
		AttestTxs:         make([]*common.AttestTx, 0, maxBatchSize), // Preallocate with maxBatchSize capacity
		AttestNetworkFees: make([]*common.AttestNetworkFee, 0, maxBatchSize),
		AttestSolvencies:  make([]*common.AttestSolvency, 0, maxBatchSize),
		AttestErrataTxs:   make([]*common.AttestErrataTx, 0, maxBatchSize),
	}
}

func (b *AttestationBatcher) setActiveValGetter(getter func() map[peer.ID]bool) {
	b.getActiveValidators = getter
}

// updateMaxBatchSize updates the maximum batch size at runtime.
func (b *AttestationBatcher) updateMaxBatchSize(newSize int64) {
	if newSize <= 0 {
		b.logger.Error().Int64("new_size", newSize).Msg("invalid batch size, must be greater than 0")
		return
	}

	b.maxBatchSizeMu.Lock()
	defer b.maxBatchSizeMu.Unlock()

	oldSize := b.maxBatchSize
	// Set the new max batch size
	b.maxBatchSize = newSize

	b.logger.Info().Int64("old_size", oldSize).Int64("new_size", newSize).Msg("max batch size updated")
}

func (b *AttestationBatcher) Start(ctx context.Context) {
	b.batchTicker = time.NewTicker(b.batchInterval)

	defer func() {
		b.batchTicker.Stop()
		close(b.forceSendChan)
	}()

	for {
		select {
		case <-ctx.Done():
			// Context is done, exit the loop
			// don't need to worry about sending any batches,
			// as next startup will send from the deck again.
			return
		case <-b.batchTicker.C:
			b.sendBatches(ctx, false)
		case <-b.forceSendChan:
			b.sendBatches(ctx, true)
		}
	}
}

type attestationBatchSelection struct {
	max         int64
	txCount     int64
	containsXMR bool
}

type preparedAttestationBatch struct {
	batch             *common.AttestationBatch
	clear             bool
	containsXMR       bool
	startedAt         time.Time
	releaseXMRBarrier func()
}

func (p *preparedAttestationBatch) releaseBarrier() {
	if p.releaseXMRBarrier == nil {
		return
	}
	p.releaseXMRBarrier()
	p.releaseXMRBarrier = nil
}

func (b *AttestationBatcher) sendBatches(ctx context.Context, force bool) {
	prepared, ok := b.prepareNextBatch(force)
	if !ok {
		return
	}
	defer prepared.releaseBarrier()

	batch := prepared.batch
	txCount := len(batch.AttestTxs)
	nfCount := len(batch.AttestNetworkFees)
	solvencyCount := len(batch.AttestSolvencies)
	errataCount := len(batch.AttestErrataTxs)
	priceFeedCount := len(batch.AttestPriceFeeds)

	if prepared.containsXMR {
		// The scanner's default durable errata handoff budget is two
		// THORChain blocks. Give every peer one shared deadline capped at one
		// block so cleanup still has time to acquire the writer barrier.
		timeout := constants.ThorchainBlockTime
		if b.peerTimeout > 0 {
			timeout = min(b.peerTimeout, timeout)
		}
		broadcastCtx, cancelBroadcast := context.WithTimeout(ctx, timeout)
		b.broadcastToAllPeers(broadcastCtx, *batch)
		cancelBroadcast()
	} else {
		b.broadcastToAllPeers(ctx, *batch)
	}
	prepared.releaseBarrier()

	// Return the batch to the pool after clearing it.
	batch.AttestTxs = batch.AttestTxs[:0]
	batch.AttestNetworkFees = batch.AttestNetworkFees[:0]
	batch.AttestSolvencies = batch.AttestSolvencies[:0]
	batch.AttestErrataTxs = batch.AttestErrataTxs[:0]
	batch.AttestPriceFeeds = batch.AttestPriceFeeds[:0]
	b.batchPool.Put(batch)

	batchDuration := time.Since(prepared.startedAt)
	b.metrics.MessagesBatched.WithLabelValues("observed_tx").Add(float64(txCount))
	b.metrics.MessagesBatched.WithLabelValues("network_fee").Add(float64(nfCount))
	b.metrics.MessagesBatched.WithLabelValues("solvency").Add(float64(solvencyCount))
	b.metrics.MessagesBatched.WithLabelValues("errata_tx").Add(float64(errataCount))
	b.metrics.MessagesBatched.WithLabelValues("price_feed").Add(float64(priceFeedCount))
	b.metrics.BatchSendTime.Observe(batchDuration.Seconds())
	b.metrics.BatchSends.Inc()

	if prepared.clear {
		b.logger.Debug().Msg("attestation batches cleared")
		b.metrics.BatchClears.Inc()
		return
	}
	b.lastBatchSent = time.Now()
}

// prepareNextBatch owns the two-phase batch lock transition. It returns with
// b.mu released and, for XMR batches, a reader-barrier lease held until the
// caller completes the bounded network fan-out.
func (b *AttestationBatcher) prepareNextBatch(force bool) (*preparedAttestationBatch, bool) {
	b.mu.Lock()
	selection, ok := b.nextBatchSelectionLocked(force)
	if !ok {
		b.mu.Unlock()
		return nil, false
	}

	var releaseXMRBarrier func()
	if selection.containsXMR && b.xmrErrataBarrier != nil {
		// Preserve the global lock order (barrier -> batch mutex), then
		// re-check after any writer that won this race purged tombstones.
		b.mu.Unlock()
		if b.beforeXMRBarrierRLock != nil {
			b.beforeXMRBarrierRLock()
		}
		b.xmrErrataBarrier.RLock()
		b.mu.Lock()

		selection, ok = b.nextBatchSelectionLocked(force)
		if !ok {
			b.mu.Unlock()
			b.xmrErrataBarrier.RUnlock()
			return nil, false
		}
		if selection.containsXMR {
			releaseXMRBarrier = b.xmrErrataBarrier.RUnlock
		} else {
			// Cleanup removed the XMR item before dequeue. The surviving batch
			// is independent from the barrier while b.mu keeps it stable.
			b.xmrErrataBarrier.RUnlock()
		}
	}

	startedAt := time.Now()
	prepared := b.dequeueSelectedBatchLocked(selection)
	prepared.startedAt = startedAt
	prepared.releaseXMRBarrier = releaseXMRBarrier
	b.mu.Unlock()
	return prepared, true
}

// nextBatchSelectionLocked inspects the exact bounded tx slice that the next
// dequeue will consume. The caller must hold b.mu.
func (b *AttestationBatcher) nextBatchSelectionLocked(force bool) (attestationBatchSelection, bool) {
	hasMessages := len(b.observedTxBatch) > 0 ||
		len(b.networkFeeBatch) > 0 ||
		len(b.solvencyBatch) > 0 ||
		len(b.errataTxBatch) > 0 ||
		len(b.priceFeedBatch) > 0
	timeThresholdMet := time.Since(b.lastBatchSent) >= b.batchInterval
	if !hasMessages || (!timeThresholdMet && !force) {
		return attestationBatchSelection{}, false
	}

	maxBatchSize := b.getMaxBatchSize()
	txCount := min(int64(len(b.observedTxBatch)), maxBatchSize)
	return attestationBatchSelection{
		max:         maxBatchSize,
		txCount:     txCount,
		containsXMR: containsXMRObservedTx(b.observedTxBatch[:txCount]),
	}, true
}

// dequeueSelectedBatchLocked copies and removes one selected batch. The caller
// must hold b.mu and must have selected from the current queue state.
func (b *AttestationBatcher) dequeueSelectedBatchLocked(
	selection attestationBatchSelection,
) *preparedAttestationBatch {
	maxBatchSize := selection.max
	networkFeeCount := min(int64(len(b.networkFeeBatch)), maxBatchSize)
	solvencyCount := min(int64(len(b.solvencyBatch)), maxBatchSize)
	errataCount := min(int64(len(b.errataTxBatch)), maxBatchSize)
	priceFeedCount := min(int64(len(b.priceFeedBatch)), maxBatchSize)
	clear := int64(len(b.observedTxBatch)) <= maxBatchSize &&
		int64(len(b.networkFeeBatch)) <= maxBatchSize &&
		int64(len(b.solvencyBatch)) <= maxBatchSize &&
		int64(len(b.errataTxBatch)) <= maxBatchSize &&
		int64(len(b.priceFeedBatch)) <= maxBatchSize

	batch, ok := b.batchPool.Get().(*common.AttestationBatch)
	if !ok {
		batch = &common.AttestationBatch{
			AttestTxs:         make([]*common.AttestTx, 0, maxBatchSize),
			AttestNetworkFees: make([]*common.AttestNetworkFee, 0, maxBatchSize),
			AttestSolvencies:  make([]*common.AttestSolvency, 0, maxBatchSize),
			AttestErrataTxs:   make([]*common.AttestErrataTx, 0, maxBatchSize),
			AttestPriceFeeds:  make([]*common.AttestPriceFeed, 0, maxBatchSize),
		}
	}

	batch.AttestTxs = append(batch.AttestTxs[:0], b.observedTxBatch[:selection.txCount]...)
	batch.AttestNetworkFees = append(batch.AttestNetworkFees[:0], b.networkFeeBatch[:networkFeeCount]...)
	batch.AttestSolvencies = append(batch.AttestSolvencies[:0], b.solvencyBatch[:solvencyCount]...)
	batch.AttestErrataTxs = append(batch.AttestErrataTxs[:0], b.errataTxBatch[:errataCount]...)
	batch.AttestPriceFeeds = append(batch.AttestPriceFeeds[:0], b.priceFeedBatch[:priceFeedCount]...)

	b.observedTxBatch = b.observedTxBatch[selection.txCount:]
	b.networkFeeBatch = b.networkFeeBatch[networkFeeCount:]
	b.solvencyBatch = b.solvencyBatch[solvencyCount:]
	b.errataTxBatch = b.errataTxBatch[errataCount:]
	b.priceFeedBatch = b.priceFeedBatch[priceFeedCount:]

	return &preparedAttestationBatch{
		batch:       batch,
		clear:       clear,
		containsXMR: selection.containsXMR,
	}
}

func containsXMRObservedTx(txs []*common.AttestTx) bool {
	for _, tx := range txs {
		if tx != nil && tx.ObsTx.Tx.Chain.Equals(common.XMRChain) {
			return true
		}
	}
	return false
}

// AddObservedTx adds an observed transaction attestation to the batch
func (b *AttestationBatcher) AddObservedTx(tx common.AttestTx) {
	maxBatchSize := b.getMaxBatchSize()

	b.mu.Lock()
	defer b.mu.Unlock()

	b.observedTxBatch = append(b.observedTxBatch, &tx)

	// If we've reached the maximum batch size, trigger an immediate send
	if int64(len(b.observedTxBatch)) >= maxBatchSize {
		b.logger.Debug().
			Int("batch_size", len(b.observedTxBatch)).
			Msg("observed tx batch reached max size, triggering immediate send")

		// Use a separate goroutine to avoid deadlock since sendBatches also acquires the mu
		go b.triggerBatchSend()
	}
}

func (b *AttestationBatcher) removeObservedTxsForErrata(errata common.ErrataTx, orphanHeight int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	filtered := b.observedTxBatch[:0]
	for _, tx := range b.observedTxBatch {
		if tx == nil || !tx.ObsTx.Tx.Chain.Equals(errata.Chain) || !tx.ObsTx.Tx.ID.Equals(errata.Id) {
			filtered = append(filtered, tx)
			continue
		}
		if errata.Chain.Equals(common.XMRChain) && orphanHeight > 0 {
			minedHeight, err := tx.ObsTx.CanonicalMinedHeight()
			if err == nil && minedHeight != orphanHeight {
				filtered = append(filtered, tx)
			}
		}
	}
	b.observedTxBatch = filtered
}

// AddNetworkFee adds a network fee attestation to the batch
func (b *AttestationBatcher) AddNetworkFee(fee common.AttestNetworkFee) {
	maxBatchSize := b.getMaxBatchSize()

	b.mu.Lock()
	defer b.mu.Unlock()

	b.networkFeeBatch = append(b.networkFeeBatch, &fee)

	// If we've reached the maximum batch size, trigger an immediate send
	if int64(len(b.networkFeeBatch)) >= maxBatchSize {
		b.logger.Debug().
			Int("batch_size", len(b.networkFeeBatch)).
			Msg("network fee batch reached max size, triggering immediate send")

		go b.triggerBatchSend()
	}
}

// AddSolvency adds a solvency attestation to the batch
func (b *AttestationBatcher) AddSolvency(solvency common.AttestSolvency) {
	maxBatchSize := b.getMaxBatchSize()

	b.mu.Lock()
	defer b.mu.Unlock()

	b.solvencyBatch = append(b.solvencyBatch, &solvency)

	// If we've reached the maximum batch size, trigger an immediate send
	if int64(len(b.solvencyBatch)) >= maxBatchSize {
		b.logger.Debug().
			Int("batch_size", len(b.solvencyBatch)).
			Msg("solvency batch reached max size, triggering immediate send")

		go b.triggerBatchSend()
	}
}

// AddErrataTx adds an errata transaction attestation to the batch
func (b *AttestationBatcher) AddErrataTx(errata common.AttestErrataTx) {
	maxBatchSize := b.getMaxBatchSize()

	b.mu.Lock()
	defer b.mu.Unlock()
	for _, queued := range b.errataTxBatch {
		if queued == nil || queued.ErrataTx == nil || errata.ErrataTx == nil ||
			queued.Attestation == nil || errata.Attestation == nil {
			continue
		}
		if queued.ErrataTx.Equals(errata.ErrataTx) &&
			bytes.Equal(queued.Attestation.PubKey, errata.Attestation.PubKey) {
			// Durable outbox retries may call AttestErrata every second while
			// waiting for the committed-quorum event. Keep at most one copy of
			// this node's identical vote in the current batch.
			return
		}
	}

	b.errataTxBatch = append(b.errataTxBatch, &errata)

	// If we've reached the maximum batch size, trigger an immediate send
	if int64(len(b.errataTxBatch)) >= maxBatchSize {
		b.logger.Debug().
			Int("batch_size", len(b.errataTxBatch)).
			Msg("errata tx batch reached max size, triggering immediate send")

		go b.triggerBatchSend()
	}
}

// AddPriceFeed adds an price feed attestation to the batch
func (b *AttestationBatcher) AddPriceFeed(priceFeed common.AttestPriceFeed) {
	maxBatchSize := b.getMaxBatchSize()

	b.mu.Lock()
	defer b.mu.Unlock()

	b.priceFeedBatch = append(b.priceFeedBatch, &priceFeed)

	// If we've reached the maximum batch size, trigger an immediate send
	if int64(len(b.priceFeedBatch)) >= maxBatchSize {
		b.logger.Debug().
			Int("batch_size", len(b.priceFeedBatch)).
			Msg("price feed batch reached max size, triggering immediate send")

		go b.triggerBatchSend()
	}
}

// triggerBatchSend triggers an immediate batch send outside the regular interval
func (b *AttestationBatcher) triggerBatchSend() {
	select {
	case b.forceSendChan <- struct{}{}:
		// Successfully triggered a send
	default:
		// Channel is full, a send is already pending
	}
}

// broadcastToAllPeers sends the batch payload to all connected peers. Batches
// carrying XMR observations wait for every bounded peer attempt so the caller's
// errata read barrier covers the actual write, not merely goroutine creation.
func (b *AttestationBatcher) broadcastToAllPeers(ctx context.Context, batch common.AttestationBatch) {
	// Marshal the batch
	payload, err := batch.Marshal()
	if err != nil {
		b.logger.Error().Err(err).Msg("failed to marshal attestation batch")
		return
	}

	peers := b.host.Peerstore().Peers()

	if b.getActiveValidators == nil {
		b.logger.Warn().Msg("active validator getter not set – skipping broadcast")
		return
	}
	// Skip self, send to all other active vals that we are peered with
	activeVals := b.getActiveValidators()
	var peersToSend []peer.ID
	for _, peerID := range peers {
		if peerID == b.host.ID() {
			continue
		}
		if _, ok := activeVals[peerID]; !ok {
			continue
		}
		peersToSend = append(peersToSend, peerID)
	}

	if len(peersToSend) == 0 {
		b.logger.Debug().Msg("no peers to broadcast to")
		return
	}

	b.logger.Debug().
		Int("peer_count", len(peersToSend)).
		Int("payload_bytes", len(payload)).
		Msg("broadcasting attestation batch to peers")

	waitForCompletion := false
	for _, tx := range batch.AttestTxs {
		if tx != nil && tx.ObsTx.Tx.Chain.Equals(common.XMRChain) {
			waitForCompletion = true
			break
		}
	}
	var sends sync.WaitGroup
	if waitForCompletion {
		sends.Add(len(peersToSend))
	}
	for _, p := range peersToSend {
		go func(remote peer.ID) {
			if waitForCompletion {
				defer sends.Done()
			}
			if b.broadcastToPeerFn != nil {
				b.broadcastToPeerFn(ctx, remote, payload)
			} else {
				b.broadcastToPeer(ctx, remote, payload)
			}
		}(p)
	}
	if waitForCompletion {
		sends.Wait()
	}

	b.logger.Debug().Msg("broadcast initiated to all peers")
}

func (b *AttestationBatcher) broadcastToPeer(ctx context.Context, peer peer.ID, payload []byte) {
	// Limit the number of concurrent sends to this peer
	sem, err := b.peerMgr.acquireContext(ctx, peer)
	if err != nil {
		b.logger.Error().Err(err).Msgf("fail to acquire semaphore for peer: %s", peer)
		return
	}

	defer b.peerMgr.release(sem)

	b.logger.Debug().Msgf("starting attestation send to peer: %s", peer)

	// Create a context with timeout for this specific peer
	peerCtx, cancel := context.WithTimeout(ctx, b.peerTimeout)
	defer cancel()
	stream, err := b.host.NewStream(peerCtx, peer, batchedAttestationProtocol)
	if err != nil {
		b.logger.Error().Err(err).Msgf("fail to create stream to peer: %s", peer)
		return
	}

	b.sendPayloadToStream(peerCtx, stream, payload)

	b.logger.Debug().Msgf("completed attestation send to peer: %s", peer)
}

func (b *AttestationBatcher) sendPayloadToStream(ctx context.Context, stream network.Stream, payload []byte) {
	peer := stream.Conn().RemotePeer()
	logger := b.logger.With().Str("remote_peer", peer.String()).Logger()

	defer closeStream(logger, stream)

	if err := p2p.WriteStreamWithBufferWithContext(ctx, payload, stream); err != nil {
		b.logger.Error().Err(err).Msgf("fail to write payload to peer: %s", peer)
		return
	}

	// Wait for acknowledgment
	reply, err := p2p.ReadStreamWithBufferWithContext(ctx, stream)
	if err != nil {
		b.logger.Error().Err(err).Msgf("fail to read reply from peer: %s", peer)
		return
	}

	if string(reply) != p2p.StreamMsgDone {
		b.logger.Error().Msgf("unexpected reply from peer: %s", peer)
		return
	}

	b.logger.Debug().Msgf("attestation sent to peer: %s", peer)
}
