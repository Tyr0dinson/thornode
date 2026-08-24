package observer

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/common"
)

// askForAttestationState will ask other validators for their attestation state
func (s *AttestationGossip) askForAttestationState(ctx context.Context) {
	allPeers := s.host.Peerstore().Peers()
	if len(allPeers) == 0 {
		s.logger.Debug().Msg("no peers to ask for attestation state")
		return
	}
	peersWithoutMe := make([]peer.ID, 0, len(allPeers)-1)
	// Skip self, send to 3 active vals that we are peered with
	activeVals := s.getActiveValidators()
	for _, peerID := range allPeers {
		if peerID == s.host.ID() {
			continue
		}
		if _, ok := activeVals[peerID]; ok {
			peersWithoutMe = append(peersWithoutMe, peerID)
		}
	}

	if len(peersWithoutMe) == 0 {
		s.logger.Debug().Msg("no active val peers without me to ask for attestation state")
		return
	}
	// ask 3 random peers for their attestation state
	rand.Shuffle(len(peersWithoutMe), func(i, j int) {
		peersWithoutMe[i], peersWithoutMe[j] = peersWithoutMe[j], peersWithoutMe[i]
	})
	numPeers := s.config.AskPeers
	if len(peersWithoutMe) < numPeers {
		numPeers = len(peersWithoutMe)
	}
	peers := peersWithoutMe[:numPeers]

	timeout := s.config.PeerTimeout
	if timeout <= 0 {
		timeout = defaultPeerTimeout
	}
	stateCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(len(peers))
	for _, peer := range peers {
		go s.askPeerForState(stateCtx, peer, &wg)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-stateCtx.Done():
		s.logger.Warn().Err(stateCtx.Err()).Dur("timeout", timeout).Msg("stop waiting for attestation state peers")
	}
}

// attestationStateContext applies one absolute budget to a complete state
// exchange. The framed stream helpers install rolling per-frame deadlines, so
// reset the stream when this context expires to ensure trickled frames cannot
// keep churn-in blocked indefinitely.
func (s *AttestationGossip) attestationStateContext(
	parent context.Context,
	stream network.Stream,
) (context.Context, context.CancelFunc) {
	timeout := s.config.PeerTimeout
	if timeout <= 0 {
		timeout = defaultPeerTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	stopReset := context.AfterFunc(ctx, func() {
		_ = stream.Reset()
	})
	return ctx, func() {
		stopReset()
		cancel()
	}
}

type attestationStateAdmission struct {
	gossip *AttestationGossip
	peer   peer.ID
}

// acquireAttestationStateAdmission authorizes and reserves one inbound state
// request before its framed body is read. Active-validator eligibility and
// receive accounting are checked atomically under one lock scope.
func (s *AttestationGossip) acquireAttestationStateAdmission(
	ctx context.Context,
	remotePeer peer.ID,
) (*attestationStateAdmission, bool) {
	if err := ctx.Err(); err != nil {
		return nil, false
	}

	s.avMu.Lock()
	s.stateReceiveMu.Lock()
	activeValidator := s.activeVals[remotePeer]
	if !activeValidator ||
		s.stateReceiveActive >= maxConcurrentAttestationStateReceives ||
		s.stateReceiveByPeer[remotePeer] >= maxConcurrentAttestationStateReceivesPerPeer {
		s.stateReceiveMu.Unlock()
		s.avMu.Unlock()
		return nil, false
	}
	if s.stateReceiveByPeer == nil {
		s.stateReceiveByPeer = make(map[peer.ID]int)
	}
	s.stateReceiveActive++
	s.stateReceiveByPeer[remotePeer]++
	s.stateReceiveMu.Unlock()
	s.avMu.Unlock()

	return &attestationStateAdmission{
		gossip: s,
		peer:   remotePeer,
	}, true
}

func (a *attestationStateAdmission) release() {
	if a == nil || a.gossip == nil {
		return
	}
	a.gossip.stateReceiveMu.Lock()
	defer a.gossip.stateReceiveMu.Unlock()
	a.gossip.stateReceiveActive--
	remaining := a.gossip.stateReceiveByPeer[a.peer] - 1
	if remaining <= 0 {
		delete(a.gossip.stateReceiveByPeer, a.peer)
		return
	}
	a.gossip.stateReceiveByPeer[a.peer] = remaining
}

type attestationStateSnapshot struct {
	quorumTxs   []*common.QuorumTx
	networkFees []*common.QuorumNetworkFee
	solvencies  []*common.QuorumSolvency
	errataTxs   []*common.QuorumErrataTx
	priceFeeds  []*common.QuorumPriceFeed
	containsXMR bool
}

func (s attestationStateSnapshot) batch(batchNum, batchSize int) common.QuorumState {
	start := batchNum * batchSize
	batch := common.QuorumState{}

	if start < len(s.quorumTxs) {
		batch.QuoTxs = s.quorumTxs[start:min(start+batchSize, len(s.quorumTxs))]
	}
	if start < len(s.networkFees) {
		batch.QuoNetworkFees = s.networkFees[start:min(start+batchSize, len(s.networkFees))]
	}
	if start < len(s.solvencies) {
		batch.QuoSolvencies = s.solvencies[start:min(start+batchSize, len(s.solvencies))]
	}
	if start < len(s.errataTxs) {
		batch.QuoErrataTxs = s.errataTxs[start:min(start+batchSize, len(s.errataTxs))]
	}
	if start < len(s.priceFeeds) {
		batch.QuoPriceFeeds = s.priceFeeds[start:min(start+batchSize, len(s.priceFeeds))]
	}

	return batch
}

// snapshotAttestationState copies the current state without blocking while it
// holds the global state locks. Its release function keeps XMR errata cleanup
// ordered after any returned XMR snapshot until the caller finishes using it.
func (s *AttestationGossip) snapshotAttestationState(ctx context.Context) (attestationStateSnapshot, func(), error) {
	retry := time.NewTicker(5 * time.Millisecond)
	defer retry.Stop()

	// Never block on a downstream lock while holding an upstream one. On
	// contention, release all acquired locks before retrying so cancellation
	// remains effective.
	for {
		if err := ctx.Err(); err != nil {
			return attestationStateSnapshot{}, nil, err
		}
		if s.xmrErrataBarrier.TryRLock() {
			if s.mu.TryLock() {
				snapshot, ok := s.copyAttestationState(ctx)
				s.mu.Unlock()
				if ok {
					if snapshot.containsXMR {
						return snapshot, s.xmrErrataBarrier.RUnlock, nil
					}
					s.xmrErrataBarrier.RUnlock()
					return snapshot, func() {}, nil
				}
			}
			s.xmrErrataBarrier.RUnlock()
		}

		select {
		case <-ctx.Done():
			return attestationStateSnapshot{}, nil, ctx.Err()
		case <-retry.C:
		}
	}
}

// copyAttestationState is called with the global state mutex and XMR reader
// barrier held. A busy item aborts the partial copy so those upstream locks are
// released before retrying.
func (s *AttestationGossip) copyAttestationState(ctx context.Context) (attestationStateSnapshot, bool) {
	snapshot := attestationStateSnapshot{
		quorumTxs:   make([]*common.QuorumTx, 0, len(s.observedTxs)),
		networkFees: make([]*common.QuorumNetworkFee, 0, len(s.networkFees)),
		solvencies:  make([]*common.QuorumSolvency, 0, len(s.solvencies)),
		errataTxs:   make([]*common.QuorumErrataTx, 0, len(s.errataTxs)),
		priceFeeds:  make([]*common.QuorumPriceFeed, 0, len(s.priceFeeds)),
	}

	for k, state := range s.observedTxs {
		if ctx.Err() != nil || !state.mu.TryLock() {
			return attestationStateSnapshot{}, false
		}
		quorumTx := &common.QuorumTx{
			ObsTx:                  *state.Item.ObservedTx,
			Inbound:                k.Inbound,
			AllowFutureObservation: k.AllowFutureObservation,
			Attestations:           state.AttestationsCopy(),
		}
		state.mu.Unlock()
		snapshot.quorumTxs = append(snapshot.quorumTxs, quorumTx)
		snapshot.containsXMR = snapshot.containsXMR || quorumTx.ObsTx.Tx.Chain.Equals(common.XMRChain)
	}
	for _, state := range s.networkFees {
		if ctx.Err() != nil || !state.mu.TryLock() {
			return attestationStateSnapshot{}, false
		}
		item := *state.Item
		snapshot.networkFees = append(snapshot.networkFees, &common.QuorumNetworkFee{
			NetworkFee:   &item,
			Attestations: state.AttestationsCopy(),
		})
		state.mu.Unlock()
	}
	for _, state := range s.solvencies {
		if ctx.Err() != nil || !state.mu.TryLock() {
			return attestationStateSnapshot{}, false
		}
		item := *state.Item
		item.Coins = state.Item.Coins.Copy()
		snapshot.solvencies = append(snapshot.solvencies, &common.QuorumSolvency{
			Solvency:     &item,
			Attestations: state.AttestationsCopy(),
		})
		state.mu.Unlock()
	}
	for _, state := range s.errataTxs {
		if ctx.Err() != nil || !state.mu.TryLock() {
			return attestationStateSnapshot{}, false
		}
		item := *state.Item
		snapshot.errataTxs = append(snapshot.errataTxs, &common.QuorumErrataTx{
			ErrataTx:     &item,
			Attestations: state.AttestationsCopy(),
		})
		state.mu.Unlock()
	}
	for _, state := range s.priceFeeds {
		if ctx.Err() != nil || !state.mu.TryLock() {
			return attestationStateSnapshot{}, false
		}
		item := *state.Item
		snapshot.priceFeeds = append(snapshot.priceFeeds, &common.QuorumPriceFeed{
			PriceFeed:    &item,
			Attestations: state.AttestationsCopy(),
		})
		state.mu.Unlock()
	}
	return snapshot, true
}

// sendAttestationState streams one context-bounded state snapshot to a peer.
// An XMR snapshot retains the errata reader barrier through the transfer.
func (s *AttestationGossip) sendAttestationState(ctx context.Context, stream network.Stream) {
	remotePeer := stream.Conn().RemotePeer()
	logger := s.logger.With().Str("peer", remotePeer.String()).Logger()
	logger.Debug().Msg("sending attestation state")

	snapshot, releaseXMRBarrier, err := s.snapshotAttestationState(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("failed to snapshot attestation state")
		return
	}
	defer releaseXMRBarrier()
	allQuorumTxs := snapshot.quorumTxs
	allNetworkFees := snapshot.networkFees
	allSolvencies := snapshot.solvencies
	allErrataTxs := snapshot.errataTxs
	allPriceFeeds := snapshot.priceFeeds

	// Calculate total number of batches
	totalTxs := len(allQuorumTxs)
	totalTxBatches := (totalTxs + maxQuorumTxsPerBatch - 1) / maxQuorumTxsPerBatch

	totalNfs := len(allNetworkFees)
	totalNfBatches := (totalNfs + maxQuorumTxsPerBatch - 1) / maxQuorumTxsPerBatch

	totalSolvencies := len(allSolvencies)
	totalSolvencyBatches := (totalSolvencies + maxQuorumTxsPerBatch - 1) / maxQuorumTxsPerBatch

	totalErrataTxs := len(allErrataTxs)
	totalErrataBatches := (totalErrataTxs + maxQuorumTxsPerBatch - 1) / maxQuorumTxsPerBatch

	totalPriceFeeds := len(allPriceFeeds)
	totalPriceFeedBatches := (totalPriceFeeds + maxQuorumTxsPerBatch - 1) / maxQuorumTxsPerBatch

	totalBatches := max(
		totalTxBatches,
		totalNfBatches,
		totalSolvencyBatches,
		totalErrataBatches,
		totalPriceFeedBatches,
	)

	s.logger.Debug().
		Int("total_txs", totalTxs).
		Int("total_network_fees", totalNfs).
		Int("total_solvencies", totalSolvencies).
		Int("total_errata_txs", totalErrataTxs).
		Int("total_price_feeds", totalPriceFeeds).
		Int("total_batches", totalBatches).
		Int("max_per_batch", maxQuorumTxsPerBatch).
		Msg("sending attestation state in batches")

	// Send batch begin signal with total number of batches
	beginInfo := make([]byte, 4)
	binary.LittleEndian.PutUint32(beginInfo, uint32(totalBatches))
	if err = p2p.WriteStreamWithBuffer(append(prefixBatchBegin, beginInfo...), stream); err != nil {
		s.logger.Error().Err(err).Msg("failed to send batch begin signal")
		return
	}

	// Wait for acknowledgment
	ack, err := p2p.ReadStreamWithBuffer(stream)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to read ack after batch begin")
		return
	}

	if totalBatches == 0 {
		// Receiver replied "done" because there are no batches to transfer.
		// Validate and exit cleanly - falling through to the streamAckBegin
		// check below would log a false "unexpected response to batch begin"
		// error in this success path.
		if string(ack) != p2p.StreamMsgDone {
			s.logger.Error().Str("response", string(ack)).Msg("unexpected final response")
		}
		return
	}

	if string(ack) != streamAckBegin {
		s.logger.Error().Str("response", string(ack)).Msg("unexpected response to batch begin")
		return
	}

	// Send batches
	for batchNum := 0; batchNum < totalBatches; batchNum++ {
		batch := snapshot.batch(batchNum, maxQuorumTxsPerBatch)
		batchData, marshalErr := batch.Marshal()
		if marshalErr != nil {
			s.logger.Error().Err(marshalErr).Int("batch", batchNum+1).Msg("failed to marshal batch")
			return
		}

		// Send batch info (batch number and data size)
		batchInfo := make([]byte, 8) // 4 bytes for batch number, 4 bytes for data size
		binary.LittleEndian.PutUint32(batchInfo[0:4], uint32(batchNum))
		binary.LittleEndian.PutUint32(batchInfo[4:8], uint32(len(batchData)))

		// Send the batch data header
		if err = p2p.WriteStreamWithBuffer(append(prefixBatchHeader, batchInfo...), stream); err != nil {
			s.logger.Error().Err(err).Int("batch", batchNum+1).Msg("failed to send batch header")
			return
		}

		// Wait for acknowledgment of batch header
		var headerAck []byte
		headerAck, err = p2p.ReadStreamWithBuffer(stream)
		if err != nil {
			s.logger.Error().Err(err).Int("batch", batchNum+1).Msg("failed to read ack after batch header")
			return
		}
		if string(headerAck) != streamAckHeader {
			s.logger.Error().Str("response", string(headerAck)).Int("batch", batchNum+1).Msg("unexpected response to batch header")
			return
		}

		// Send the actual batch data
		if err = p2p.WriteStreamWithBuffer(append(prefixBatchData, batchData...), stream); err != nil {
			s.logger.Error().Err(err).Int("batch", batchNum+1).Msg("failed to send batch data")
			return
		}

		// Wait for acknowledgment of batch data
		var dataAck []byte
		dataAck, err = p2p.ReadStreamWithBuffer(stream)
		if err != nil {
			s.logger.Error().Err(err).Int("batch", batchNum+1).Msg("failed to read ack after batch data")
			return
		}
		if string(dataAck) != streamAckData {
			s.logger.Error().Str("response", string(dataAck)).Int("batch", batchNum+1).Msg("unexpected response to batch data")
			return
		}

		s.logger.Debug().
			Int("batch", batchNum+1).
			Int("total_batches", totalBatches).
			Int("batch_size", len(batch.QuoTxs)).
			Int("data_size_bytes", len(batchData)).
			Msg("batch sent")
	}

	// Send batch end signal
	if err = p2p.WriteStreamWithBuffer(prefixBatchEnd, stream); err != nil {
		s.logger.Error().Err(err).Msg("failed to send batch end signal")
		return
	}

	// Wait for the final acknowledgment
	var endAck []byte
	endAck, err = p2p.ReadStreamWithBuffer(stream)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to read final ack")
		return
	}
	if string(endAck) != p2p.StreamMsgDone {
		s.logger.Error().Str("response", string(endAck)).Msg("unexpected final response")
		return
	}

	s.logger.Debug().
		Int("total_txs", totalTxs).
		Int("total_network_fees", totalNfs).
		Int("total_solvencies", totalSolvencies).
		Int("total_errata_txs", totalErrataTxs).
		Int("total_price_feeds", totalPriceFeeds).
		Int("total_batches", totalBatches).
		Msg("successfully sent attestation state in batches")
}

// Add a function to handle receiving batched attestation state
func (s *AttestationGossip) receiveBatchedAttestationState(ctx context.Context, stream network.Stream, initialData []byte) error {
	remotePeer := stream.Conn().RemotePeer()
	logger := s.logger.With().Str("peer", remotePeer.String()).Str("who", "receiver").Logger()

	// Process batch begin
	if len(initialData) != 4 {
		return fmt.Errorf("invalid batch begin format")
	}

	declaredBatches := binary.LittleEndian.Uint32(initialData)
	if declaredBatches > maxAttestationStateBatches {
		return fmt.Errorf("attestation state batch count %d exceeds maximum %d", declaredBatches, maxAttestationStateBatches)
	}
	totalBatches := int(declaredBatches)
	logger.Debug().Int("total_batches", totalBatches).Msg("receiving batched attestation state")

	if totalBatches == 0 {
		// If there are no transactions, we still need to send the end signal
		if err := p2p.WriteStreamWithBuffer([]byte(p2p.StreamMsgDone), stream); err != nil {
			return fmt.Errorf("failed to send final ack: %w", err)
		}
		return nil
	}

	// Acknowledge receipt of begin signal
	if err := p2p.WriteStreamWithBuffer([]byte(streamAckBegin), stream); err != nil {
		return fmt.Errorf("failed to acknowledge batch begin: %w", err)
	}

	totalItems := 0
	// Receive exactly the declared batches in order. Processing inline keeps
	// per-stream work bounded without a worker pool or per-batch goroutines.
	for expectedBatchNum := range totalBatches {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("attestation state transfer timed out: %w", err)
		}

		// Read next message
		message, err := p2p.ReadStreamWithBuffer(stream)
		if err != nil {
			return fmt.Errorf("failed to read batch message: %w", err)
		}

		// Must be a batch data header
		if len(message) != 9 || !bytes.Equal(message[:1], prefixBatchHeader) {
			return fmt.Errorf("invalid batch header format for batch %d", expectedBatchNum)
		}

		// Extract batch information
		batchNum := int(binary.LittleEndian.Uint32(message[1:5]))
		if batchNum != expectedBatchNum {
			return fmt.Errorf("unexpected batch number: expected %d, got %d", expectedBatchNum, batchNum)
		}
		declaredDataSize := binary.LittleEndian.Uint32(message[5:9])
		if declaredDataSize > uint32(p2p.MaxPayload-1) {
			return fmt.Errorf("batch data size %d exceeds maximum %d", declaredDataSize, p2p.MaxPayload-1)
		}
		dataSize := int(declaredDataSize)

		logger.Debug().
			Int("batch", batchNum+1).
			Int("total_batches", totalBatches).
			Int("data_size_bytes", dataSize).
			Msg("receiving batch")

		// Acknowledge the batch header
		if err = p2p.WriteStreamWithBuffer([]byte(streamAckHeader), stream); err != nil {
			return fmt.Errorf("failed to acknowledge batch header: %w", err)
		}

		// Read the batch data
		message, err = p2p.ReadStreamWithBuffer(stream)
		if err != nil {
			return fmt.Errorf("failed to read batch data: %w", err)
		}
		if !bytes.HasPrefix(message, prefixBatchData) {
			return fmt.Errorf("invalid batch data format")
		}
		batchData := message[1:]

		logger.Debug().Int("len_bz", len(batchData)).Msg("received batch data")

		if len(batchData) != dataSize {
			return fmt.Errorf("batch data size mismatch: expected %d, got %d", dataSize, len(batchData))
		}

		// Acknowledge the batch data
		if writeErr := p2p.WriteStreamWithBuffer([]byte(streamAckData), stream); writeErr != nil {
			return fmt.Errorf("failed to acknowledge batch data: %w", writeErr)
		}

		// In receiveBatchedAttestationState function
		if len(batchData) == 0 {
			// This is an empty batch, no need to unmarshal
			continue
		}

		// Unmarshal the batch
		var batch common.QuorumState
		if unmarshalErr := batch.Unmarshal(batchData); unmarshalErr != nil {
			return fmt.Errorf("failed to unmarshal batch: %w", unmarshalErr)
		}

		totalItems, err = validateAttestationStateTransferCount(totalItems, batch)
		if err != nil {
			return err
		}
		if err := s.processAttestationStateBatch(ctx, logger, batch); err != nil {
			return fmt.Errorf("failed to process batch %d: %w", batchNum, err)
		}

		logger.Debug().
			Int("batch", batchNum+1).
			Int("total_batches", totalBatches).
			Int("batch_size", attestationStateBatchItemCount(batch)).
			Int("total_collected", totalItems).
			Msg("batch received")
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("attestation state transfer timed out: %w", err)
	}
	end, err := p2p.ReadStreamWithBuffer(stream)
	if err != nil {
		return fmt.Errorf("failed to read batch end: %w", err)
	}
	if !bytes.Equal(end, prefixBatchEnd) {
		return fmt.Errorf("expected batch end after %d batches", totalBatches)
	}
	if err = p2p.WriteStreamWithBuffer([]byte(p2p.StreamMsgDone), stream); err != nil {
		return fmt.Errorf("failed to send final ack: %w", err)
	}

	logger.Debug().Int("total_items", totalItems).Msg("finished receiving attestation state")

	return nil
}

func attestationStateBatchItemCount(batch common.QuorumState) int {
	return len(batch.QuoTxs) +
		len(batch.QuoNetworkFees) +
		len(batch.QuoSolvencies) +
		len(batch.QuoErrataTxs) +
		len(batch.QuoPriceFeeds)
}

func validateAttestationStateTransferCount(current int, batch common.QuorumState) (int, error) {
	next := current + attestationStateBatchItemCount(batch)
	if next > maxAttestationStateItemsPerTransfer {
		return current, fmt.Errorf(
			"attestation state item count %d exceeds transfer maximum %d",
			next,
			maxAttestationStateItemsPerTransfer,
		)
	}
	return next, nil
}

// validateAttestationStateBatch preflights structural bounds and required
// pointers in an untrusted decoded batch before any handler runs. This avoids
// panics and partial dispatch; handlers retain semantic and signature checks.
func validateAttestationStateBatch(ctx context.Context, batch common.QuorumState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	checkCount := func(name string, count int) error {
		if count > maxAttestationStateItemsPerType {
			return fmt.Errorf("%s count %d exceeds maximum %d", name, count, maxAttestationStateItemsPerType)
		}
		return nil
	}
	for _, field := range []struct {
		name  string
		count int
	}{
		{name: "quoTxs", count: len(batch.QuoTxs)},
		{name: "quoNetworkFees", count: len(batch.QuoNetworkFees)},
		{name: "quoSolvencies", count: len(batch.QuoSolvencies)},
		{name: "quoErrataTxs", count: len(batch.QuoErrataTxs)},
		{name: "quoPriceFeeds", count: len(batch.QuoPriceFeeds)},
	} {
		if err := checkCount(field.name, field.count); err != nil {
			return err
		}
	}
	checkAttestations := func(name string, item int, attestations []*common.Attestation) error {
		if len(attestations) > maxAttestationStateAttestationsPerItem {
			return fmt.Errorf(
				"%s[%d].attestations count %d exceeds maximum %d",
				name,
				item,
				len(attestations),
				maxAttestationStateAttestationsPerItem,
			)
		}
		for i, attestation := range attestations {
			if err := ctx.Err(); err != nil {
				return err
			}
			if attestation == nil {
				return fmt.Errorf("%s[%d].attestations[%d] is nil", name, item, i)
			}
		}
		return nil
	}

	for i, tx := range batch.QuoTxs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if tx == nil {
			return fmt.Errorf("quoTxs[%d] is nil", i)
		}
		if err := checkAttestations("quoTxs", i, tx.Attestations); err != nil {
			return err
		}
	}
	for i, networkFee := range batch.QuoNetworkFees {
		if err := ctx.Err(); err != nil {
			return err
		}
		if networkFee == nil {
			return fmt.Errorf("quoNetworkFees[%d] is nil", i)
		}
		if networkFee.NetworkFee == nil {
			return fmt.Errorf("quoNetworkFees[%d].networkFee is nil", i)
		}
		if err := checkAttestations("quoNetworkFees", i, networkFee.Attestations); err != nil {
			return err
		}
	}
	for i, solvency := range batch.QuoSolvencies {
		if err := ctx.Err(); err != nil {
			return err
		}
		if solvency == nil {
			return fmt.Errorf("quoSolvencies[%d] is nil", i)
		}
		if solvency.Solvency == nil {
			return fmt.Errorf("quoSolvencies[%d].solvency is nil", i)
		}
		if err := checkAttestations("quoSolvencies", i, solvency.Attestations); err != nil {
			return err
		}
	}
	for i, errata := range batch.QuoErrataTxs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if errata == nil {
			return fmt.Errorf("quoErrataTxs[%d] is nil", i)
		}
		if errata.ErrataTx == nil {
			return fmt.Errorf("quoErrataTxs[%d].errataTx is nil", i)
		}
		if err := checkAttestations("quoErrataTxs", i, errata.Attestations); err != nil {
			return err
		}
	}
	for i, priceFeed := range batch.QuoPriceFeeds {
		if err := ctx.Err(); err != nil {
			return err
		}
		if priceFeed == nil {
			return fmt.Errorf("quoPriceFeeds[%d] is nil", i)
		}
		if priceFeed.PriceFeed == nil {
			return fmt.Errorf("quoPriceFeeds[%d].priceFeed is nil", i)
		}
		for rateIndex, rate := range priceFeed.PriceFeed.Rates {
			if err := ctx.Err(); err != nil {
				return err
			}
			if rate == nil {
				return fmt.Errorf("quoPriceFeeds[%d].priceFeed.rates[%d] is nil", i, rateIndex)
			}
		}
		if err := checkAttestations("quoPriceFeeds", i, priceFeed.Attestations); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (s *AttestationGossip) processAttestationStateBatch(ctx context.Context, logger zerolog.Logger, batch common.QuorumState) error {
	// Preflight the whole batch before mutating any pool so a malformed later
	// record cannot leave a partially applied snapshot.
	if err := validateAttestationStateBatch(ctx, batch); err != nil {
		return err
	}
	for _, qt := range batch.QuoTxs {
		for _, att := range qt.Attestations {
			if err := ctx.Err(); err != nil {
				return err
			}
			logger.Debug().Msg("handling attestation from peer state dump")
			s.handleObservedTxAttestationFromStateSync(ctx, common.AttestTx{
				ObsTx:                  qt.ObsTx,
				Inbound:                qt.Inbound,
				Attestation:            att,
				AllowFutureObservation: qt.AllowFutureObservation,
			})
		}
	}
	for _, nf := range batch.QuoNetworkFees {
		for _, att := range nf.Attestations {
			if err := ctx.Err(); err != nil {
				return err
			}
			logger.Debug().Msg("handling network fee from peer state dump")
			s.handleNetworkFeeAttestation(ctx, common.AttestNetworkFee{
				NetworkFee:  nf.NetworkFee,
				Attestation: att,
			})
		}
	}
	for _, solvency := range batch.QuoSolvencies {
		for _, att := range solvency.Attestations {
			if err := ctx.Err(); err != nil {
				return err
			}
			logger.Debug().Msg("handling solvency from peer state dump")
			s.handleSolvencyAttestation(ctx, common.AttestSolvency{
				Solvency:    solvency.Solvency,
				Attestation: att,
			})
		}
	}
	for _, errataTx := range batch.QuoErrataTxs {
		for _, att := range errataTx.Attestations {
			if err := ctx.Err(); err != nil {
				return err
			}
			logger.Debug().Msg("handling errata tx from peer state dump")
			s.handleErrataAttestation(ctx, common.AttestErrataTx{
				ErrataTx:    errataTx.ErrataTx,
				Attestation: att,
			})
		}
	}
	for _, priceFeed := range batch.QuoPriceFeeds {
		for _, att := range priceFeed.Attestations {
			if err := ctx.Err(); err != nil {
				return err
			}
			logger.Debug().Msg("handling price feed from peer state dump")
			// Price-feed submission is deliberately delayed and does not block
			// this transfer. Preserve that work after the state-sync context is
			// canceled on a successful return.
			s.handlePriceFeedAttestation(context.WithoutCancel(ctx), common.AttestPriceFeed{
				PriceFeed:   priceFeed.PriceFeed,
				Attestation: att,
			})
		}
	}
	return ctx.Err()
}

// askPeerForState will ask a specific peer for its attestation state
func (s *AttestationGossip) askPeerForState(ctx context.Context, peer peer.ID, wg *sync.WaitGroup) {
	defer wg.Done()
	timeout := s.config.PeerTimeout
	if timeout <= 0 {
		timeout = defaultPeerTimeout
	}
	peerCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stream, err := s.host.NewStream(peerCtx, peer, attestationStateProtocol)
	if err != nil {
		s.logger.Error().Err(err).Msgf("fail to create stream to peer: %s", peer)
		return
	}

	s.askStreamForState(peerCtx, stream)
}

// askStreamForState will ask a specific stream for its attestation state
func (s *AttestationGossip) askStreamForState(ctx context.Context, stream network.Stream) {
	peer := stream.Conn().RemotePeer()

	stateCtx, cancel := s.attestationStateContext(ctx, stream)
	defer cancel()
	success := false
	defer func() {
		if success {
			closeStream(s.logger, stream)
			return
		}
		if err := stream.Reset(); err != nil {
			s.logger.Debug().Err(err).Msgf("fail to reset failed attestation state request to peer: %s", peer)
		}
	}()

	if err := p2p.WriteStreamWithBuffer(prefixSendState, stream); err != nil {
		s.logger.Error().Err(err).Msgf("fail to write payload to peer: %s", peer)
		return
	}

	// Read initial response from peer
	initialData, err := p2p.ReadStreamWithBufferLimitWithContext(
		stateCtx,
		stream,
		maxAttestationStateControlBytes,
	)
	if err != nil {
		s.logger.Error().Err(err).Msgf("fail to read reply from peer: %s", peer)
		return
	}

	// If it's not the batch begin prefix, it's an error
	if len(initialData) != int(maxAttestationStateControlBytes) || !bytes.HasPrefix(initialData, prefixBatchBegin) {
		s.logger.Error().Msgf("unexpected attestation state format from peer: %s", peer)
		return
	}

	// Process the batched attestation state
	// Strip the prefixBatchBegin byte before passing to the receiver, matching
	// the parallel call site in attestation_handlers.go which uses data[1:].
	// Without this, receiveBatchedAttestationState reads initialData[:4] as
	// totalBatches and gets garbage (prefix byte | first 3 bytes of real value),
	// breaking the handshake on every state-init exchange after a restart.
	if err := s.receiveBatchedAttestationState(stateCtx, stream, initialData[len(prefixBatchBegin):]); err != nil {
		s.logger.Error().Err(err).Msgf("fail to receive batched attestation state from peer: %s", peer)
		return
	}
	success = true
}
