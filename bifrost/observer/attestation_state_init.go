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

	s.stateInitMu.Lock()
	s.stateInitPeers = make(map[peer.ID]bool, len(peers))
	for _, peer := range peers {
		s.stateInitPeers[peer] = true
	}
	s.stateInitMu.Unlock()
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
	// Reset the state init peers
	s.stateInitMu.Lock()
	s.stateInitPeers = nil
	s.stateInitMu.Unlock()
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

func (s *AttestationGossip) sendAttestationState(stream network.Stream) {
	remotePeer := stream.Conn().RemotePeer()
	logger := s.logger.With().Str("peer", remotePeer.String()).Logger()
	logger.Debug().Msg("sending attestation state")

	// This is a total transfer budget, not a rolling per-frame timeout. State
	// containing XMR observations holds xmrErrataBarrier.RLock until the peer has
	// acknowledged the final frame, so a peer which acknowledges just often
	// enough must not postpone durable errata cleanup indefinitely. The handler
	// also applies the stream deadline before parsing the request; this watchdog
	// makes direct callers and the complete send phase independently bounded.
	timeout := s.config.PeerTimeout
	if timeout <= 0 {
		timeout = defaultPeerTimeout
	}
	transferTimeout := time.AfterFunc(timeout, func() {
		logger.Warn().Dur("timeout", timeout).Msg("resetting timed-out attestation state transfer")
		if err := stream.Reset(); err != nil {
			logger.Debug().Err(err).Msg("failed to reset timed-out attestation state stream")
		}
	})
	defer transferTimeout.Stop()

	s.xmrErrataBarrier.RLock()
	xmrBarrierHeld := true
	defer func() {
		if xmrBarrierHeld {
			s.xmrErrataBarrier.RUnlock()
		}
	}()
	s.mu.Lock()

	// Collect all QuorumTxs
	allQuorumTxs := make([]*common.QuorumTx, 0, len(s.observedTxs))
	containsXMR := false
	for k, ots := range s.observedTxs {
		ots.mu.Lock()

		quorumTxs := &common.QuorumTx{
			ObsTx:                  *ots.Item.ObservedTx,
			Inbound:                k.Inbound,
			AllowFutureObservation: k.AllowFutureObservation,
			Attestations:           ots.AttestationsCopy(),
		}
		allQuorumTxs = append(allQuorumTxs, quorumTxs)
		containsXMR = containsXMR || quorumTxs.ObsTx.Tx.Chain.Equals(common.XMRChain)

		ots.mu.Unlock()
	}

	allNetworkFees := make([]*common.QuorumNetworkFee, 0, len(s.networkFees))
	for _, nf := range s.networkFees {
		nf.mu.Lock()
		nfCopy := &common.NetworkFee{
			Chain:           nf.Item.Chain,
			Height:          nf.Item.Height,
			TransactionSize: nf.Item.TransactionSize,
			TransactionRate: nf.Item.TransactionRate,
		}
		allNetworkFees = append(allNetworkFees, &common.QuorumNetworkFee{
			NetworkFee:   nfCopy,
			Attestations: nf.AttestationsCopy(),
		})
		nf.mu.Unlock()
	}

	allSolvencies := make([]*common.QuorumSolvency, 0, len(s.solvencies))
	for _, solvency := range s.solvencies {
		solvency.mu.Lock()
		sCopy := &common.Solvency{
			Id:     solvency.Item.Id,
			Chain:  solvency.Item.Chain,
			Height: solvency.Item.Height,
			PubKey: solvency.Item.PubKey,
			Coins:  solvency.Item.Coins.Copy(),
		}
		allSolvencies = append(allSolvencies, &common.QuorumSolvency{
			Solvency:     sCopy,
			Attestations: solvency.AttestationsCopy(),
		})
		solvency.mu.Unlock()
	}

	allErrataTxs := make([]*common.QuorumErrataTx, 0, len(s.errataTxs))
	for _, errataTx := range s.errataTxs {
		errataTx.mu.Lock()
		eTxCopy := &common.ErrataTx{
			Chain: errataTx.Item.Chain,
			Id:    errataTx.Item.Id,
		}
		allErrataTxs = append(allErrataTxs, &common.QuorumErrataTx{
			ErrataTx:     eTxCopy,
			Attestations: errataTx.AttestationsCopy(),
		})
		errataTx.mu.Unlock()
	}

	allPriceFeeds := make([]*common.QuorumPriceFeed, 0, len(s.priceFeeds))
	for _, priceFeed := range s.priceFeeds {
		priceFeed.mu.Lock()
		pfCopy := &common.PriceFeed{
			Time:  priceFeed.Item.Time,
			Rates: priceFeed.Item.Rates,
		}
		allPriceFeeds = append(allPriceFeeds, &common.QuorumPriceFeed{
			PriceFeed:    pfCopy,
			Attestations: priceFeed.AttestationsCopy(),
		})
		priceFeed.mu.Unlock()
	}

	s.mu.Unlock()
	if !containsXMR {
		s.xmrErrataBarrier.RUnlock()
		xmrBarrierHeld = false
	}

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
	if err := p2p.WriteStreamWithBuffer(append(prefixBatchBegin, beginInfo...), stream); err != nil {
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
		startIdx := batchNum * maxQuorumTxsPerBatch
		endIdx := startIdx + maxQuorumTxsPerBatch

		var batchTxs []*common.QuorumTx
		var batchNfs []*common.QuorumNetworkFee
		var batchSolvencies []*common.QuorumSolvency
		var batchErrataTxs []*common.QuorumErrataTx
		var batchPriceFeeds []*common.QuorumPriceFeed

		if totalTxs > 0 && startIdx < totalTxs {
			if endIdx > totalTxs {
				endIdx = totalTxs
			}
			batchTxs = allQuorumTxs[startIdx:endIdx]
		}

		if totalNfs > 0 && startIdx < totalNfs {
			if endIdx > totalNfs {
				endIdx = totalNfs
			}
			batchNfs = allNetworkFees[startIdx:endIdx]
		}

		if totalSolvencies > 0 && startIdx < totalSolvencies {
			if endIdx > totalSolvencies {
				endIdx = totalSolvencies
			}
			batchSolvencies = allSolvencies[startIdx:endIdx]
		}

		if totalErrataTxs > 0 && startIdx < totalErrataTxs {
			if endIdx > totalErrataTxs {
				endIdx = totalErrataTxs
			}
			batchErrataTxs = allErrataTxs[startIdx:endIdx]
		}

		if totalPriceFeeds > 0 && startIdx < totalPriceFeeds {
			if endIdx > totalPriceFeeds {
				endIdx = totalPriceFeeds
			}
			batchPriceFeeds = allPriceFeeds[startIdx:endIdx]
		}

		// Create batch with current set of QuorumTxs
		batch := common.QuorumState{
			QuoTxs:         batchTxs,
			QuoNetworkFees: batchNfs,
			QuoSolvencies:  batchSolvencies,
			QuoErrataTxs:   batchErrataTxs,
			QuoPriceFeeds:  batchPriceFeeds,
		}

		// Marshal the batch (handle empty batches specially)
		var batchData []byte
		if len(batch.QuoTxs) == 0 {
			// For empty batches, use an empty buffer rather than marshaling an empty struct
			batchData = []byte{}
		} else {
			batchData, err = batch.Marshal()
			if err != nil {
				s.logger.Error().Err(err).Int("batch", batchNum+1).Msg("failed to marshal batch")
				return
			}
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

	total := 0
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
		if err := p2p.WriteStreamWithBuffer([]byte(streamAckData), stream); err != nil {
			return fmt.Errorf("failed to acknowledge batch data: %w", err)
		}

		// In receiveBatchedAttestationState function
		if len(batchData) == 0 {
			// This is an empty batch, no need to unmarshal
			continue
		}

		// Unmarshal the batch
		var batch common.QuorumState
		if err := batch.Unmarshal(batchData); err != nil {
			return fmt.Errorf("failed to unmarshal batch: %w", err)
		}

		if err := s.processAttestationStateBatch(ctx, logger, batch); err != nil {
			return fmt.Errorf("failed to process batch %d: %w", batchNum, err)
		}

		total += len(batch.QuoTxs)

		logger.Debug().
			Int("batch", batchNum+1).
			Int("total_batches", totalBatches).
			Int("batch_size", len(batch.QuoTxs)).
			Int("total_collected", total).
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

	logger.Debug().Int("total_txs", total).Msg("finished receiving attestation state")

	return nil
}

func (s *AttestationGossip) processAttestationStateBatch(ctx context.Context, logger zerolog.Logger, batch common.QuorumState) error {
	for _, qt := range batch.QuoTxs {
		for _, att := range qt.Attestations {
			if err := ctx.Err(); err != nil {
				return err
			}
			logger.Debug().Msg("handling attestation from peer state dump")
			s.handleObservedTxAttestation(ctx, common.AttestTx{
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

	defer closeStream(s.logger, stream)
	stateCtx, cancel := s.attestationStateContext(ctx, stream)
	defer cancel()

	if err := p2p.WriteStreamWithBuffer(prefixSendState, stream); err != nil {
		s.logger.Error().Err(err).Msgf("fail to write payload to peer: %s", peer)
		return
	}

	// Read initial response from peer
	initialData, err := p2p.ReadStreamWithBuffer(stream)
	if err != nil {
		s.logger.Error().Err(err).Msgf("fail to read reply from peer: %s", peer)
		return
	}

	// If it's not the batch begin prefix, it's an error
	if !bytes.HasPrefix(initialData, prefixBatchBegin) {
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
	}
}
