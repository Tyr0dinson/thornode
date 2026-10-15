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

// Match the processing budget used by bounded state-sync receivers. This is
// operational headroom, not a bound on signature work, lock waits or RPCs.
const (
	defaultStateSyncTimeout = 30 * time.Minute
	// Match S14: a single stalled peer cannot own both copies. This bounds
	// simultaneous copies, not the size of the legacy snapshot itself.
	maxConcurrentAttestationStateSnapshots = 2
)

func (s *AttestationGossip) stateSyncTimeout() time.Duration {
	if s.config.StateSyncTimeout > 0 {
		return s.config.StateSyncTimeout
	}
	return defaultStateSyncTimeout
}

func (s *AttestationGossip) acquireAttestationStateExport(remote peer.ID) bool {
	s.stateExportMu.Lock()
	defer s.stateExportMu.Unlock()
	if _, exists := s.stateExports[remote]; exists || len(s.stateExports) >= maxConcurrentAttestationStateSnapshots {
		return false
	}
	if s.stateExports == nil {
		s.stateExports = make(map[peer.ID]struct{})
	}
	s.stateExports[remote] = struct{}{}
	return true
}

// The owning handler releases capacity only when its work has returned.
// A stream reset cannot interrupt a local lock wait or release its copy early.
func (s *AttestationGossip) releaseAttestationStateExport(remote peer.ID) {
	s.stateExportMu.Lock()
	defer s.stateExportMu.Unlock()
	delete(s.stateExports, remote)
}

// One absolute transport budget prevents successive ACKs from refreshing the
// wait indefinitely. Reset interrupts I/O, not snapshot locks or processing.
func (s *AttestationGossip) stateExchangeDeadline(parent context.Context, stream network.Stream) (time.Time, func()) {
	ctx, cancel := context.WithTimeout(parent, s.stateSyncTimeout())
	stop := context.AfterFunc(ctx, func() { _ = stream.Reset() })
	deadline, _ := ctx.Deadline()
	return deadline, func() { stop(); cancel() }
}

// askForAttestationState collects one parallel group. A failed peer never
// cancels other imports; the owner waits for the whole group before retrying.
func (s *AttestationGossip) askForAttestationState(ctx context.Context, peers ...peer.ID) attestationStateGroupResult {
	if len(peers) == 0 {
		peers = s.attestationStatePeers()
		rand.Shuffle(len(peers), func(i, j int) { peers[i], peers[j] = peers[j], peers[i] })
		fanout := s.config.AskPeers
		if fanout <= 0 {
			fanout = defaultAskPeers
		}
		peers = peers[:min(len(peers), fanout)]
	}
	if len(peers) == 0 {
		return attestationStateGroupResult{}
	}
	s.stateInitMu.Lock()
	s.stateInitPeers = make(map[peer.ID]bool, len(peers))
	for _, remote := range peers {
		s.stateInitPeers[remote] = true
	}
	s.stateInitMu.Unlock()
	defer func() {
		s.stateInitMu.Lock()
		s.stateInitPeers = nil
		s.stateInitMu.Unlock()
	}()

	results := make(chan bool, len(peers))
	for _, remote := range peers {
		go func() { results <- s.askPeerForState(ctx, remote) }()
	}
	var group attestationStateGroupResult
	for range peers {
		if <-results {
			group.completed++
		} else {
			group.failed++
		}
	}
	return group
}

func (s *AttestationGossip) sendAttestationState(stream network.Stream) {
	remotePeer := stream.Conn().RemotePeer()
	if !s.acquireAttestationStateExport(remotePeer) {
		s.logger.Debug().Str("peer", remotePeer.String()).Msg("state export capacity exhausted")
		_ = stream.Reset()
		return
	}
	defer s.releaseAttestationStateExport(remotePeer)
	deadline, stopDeadline := s.stateExchangeDeadline(context.Background(), stream)
	defer stopDeadline()

	s.logger.Debug().Str("peer", remotePeer.String()).Msg("sending attestation state")
	// Snapshot under the map/state locks. Each frame revalidates durable XMR
	// admission later; no transport operation holds a cleanup reader.
	s.mu.Lock()

	// Collect all QuorumTxs
	allQuorumTxs := make([]*common.QuorumTx, 0, len(s.observedTxs))
	for k, ots := range s.observedTxs {
		ots.mu.Lock()

		quorumTxs := &common.QuorumTx{
			ObsTx:                  *ots.Item.ObservedTx,
			Inbound:                k.Inbound,
			AllowFutureObservation: k.AllowFutureObservation,
			Attestations:           ots.AttestationsCopy(),
		}
		allQuorumTxs = append(allQuorumTxs, quorumTxs)

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

	// Pack items into batches by serialized size so every frame stays under
	// the wire-level payload cap, regardless of item counts or how many
	// attestations each item carries.
	qb := &quorumStateBatcher{}
	for _, tx := range allQuorumTxs {
		qb.add(tx.Size(), func(qs *common.QuorumState) { qs.QuoTxs = append(qs.QuoTxs, tx) })
	}
	for _, nf := range allNetworkFees {
		qb.add(nf.Size(), func(qs *common.QuorumState) { qs.QuoNetworkFees = append(qs.QuoNetworkFees, nf) })
	}
	for _, solvency := range allSolvencies {
		qb.add(solvency.Size(), func(qs *common.QuorumState) { qs.QuoSolvencies = append(qs.QuoSolvencies, solvency) })
	}
	for _, errataTx := range allErrataTxs {
		qb.add(errataTx.Size(), func(qs *common.QuorumState) { qs.QuoErrataTxs = append(qs.QuoErrataTxs, errataTx) })
	}
	for _, priceFeed := range allPriceFeeds {
		qb.add(priceFeed.Size(), func(qs *common.QuorumState) { qs.QuoPriceFeeds = append(qs.QuoPriceFeeds, priceFeed) })
	}
	qb.flush()
	batches := qb.batches

	totalTxs := len(allQuorumTxs)
	totalNfs := len(allNetworkFees)
	totalSolvencies := len(allSolvencies)
	totalErrataTxs := len(allErrataTxs)
	totalPriceFeeds := len(allPriceFeeds)
	totalBatches := len(batches)

	s.logger.Debug().
		Int("total_txs", totalTxs).
		Int("total_network_fees", totalNfs).
		Int("total_solvencies", totalSolvencies).
		Int("total_errata_txs", totalErrataTxs).
		Int("total_price_feeds", totalPriceFeeds).
		Int("total_batches", totalBatches).
		Int("max_batch_bytes", maxAttestationBatchBytes).
		Msg("sending attestation state in batches")

	// Send batch begin signal with total number of batches
	beginInfo := make([]byte, 4)
	binary.LittleEndian.PutUint32(beginInfo, uint32(totalBatches))
	if err := p2p.WriteStreamWithBuffer(append(prefixBatchBegin, beginInfo...), stream); err != nil {
		s.logger.Error().Err(err).Msg("failed to send batch begin signal")
		return
	}

	// Wait for acknowledgment
	ack, err := p2p.ReadStreamWithBufferLimit(stream, uint32(len(streamAckHeader)))
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
	for batchNum, batch := range batches {
		if err = s.sendAttestationStateBatch(stream, batchNum, batch, deadline); err != nil {
			s.logger.Error().Err(err).Int("batch", batchNum+1).Msg("failed to send state batch")
			return
		}

		s.logger.Debug().
			Int("batch", batchNum+1).
			Int("total_batches", totalBatches).
			Msg("batch sent")
	}

	// Send batch end signal
	if err = p2p.WriteStreamWithBuffer(prefixBatchEnd, stream); err != nil {
		s.logger.Error().Err(err).Msg("failed to send batch end signal")
		return
	}

	// The receiver sends done only after processing the final batch.
	var endAck []byte
	endAck, err = p2p.ReadStreamWithBufferLimitAndDeadline(stream, uint32(len(streamAckHeader)), deadline)
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

// sendAttestationStateBatch rechecks durable XMR admission for each frame.
// Transport and ACK waits hold no cleanup reader. A frame already prepared
// may arrive after cleanup; the receiving peer applies its own admission check.
// The sender owns one absolute deadline, including snapshot construction.
func (s *AttestationGossip) sendAttestationStateBatch(stream network.Stream, batchNum int, batch common.QuorumState, deadline time.Time) error {
	hasXMR := false
	for _, tx := range batch.QuoTxs {
		if tx.ObsTx.Tx.Chain.Equals(common.XMRChain) {
			hasXMR = true
			break
		}
	}
	if hasXMR {
		s.mu.Lock()
		kept := batch.QuoTxs[:0]
		for _, tx := range batch.QuoTxs {
			if tx.ObsTx.Tx.Chain.Equals(common.XMRChain) {
				if !s.admitXMRObservationLocked(tx.ObsTx) {
					continue
				}
			}
			kept = append(kept, tx)
		}
		clear(batch.QuoTxs[len(kept):])
		batch.QuoTxs = kept
		s.mu.Unlock()
	}

	// Preserve announced frame count and numbering even if cleanup emptied a
	// frame. The receiver already accepts an empty QuorumState payload.
	batchData, err := batch.Marshal()
	if err != nil {
		return fmt.Errorf("marshal state batch: %w", err)
	}
	batchInfo := make([]byte, 8)
	binary.LittleEndian.PutUint32(batchInfo[0:4], uint32(batchNum))
	binary.LittleEndian.PutUint32(batchInfo[4:8], uint32(len(batchData)))
	if err = p2p.WriteStreamWithBuffer(append(prefixBatchHeader, batchInfo...), stream); err != nil {
		return fmt.Errorf("write state batch header: %w", err)
	}
	var headerAck []byte
	if batchNum == 0 {
		headerAck, err = p2p.ReadStreamWithBufferLimit(stream, uint32(len(streamAckHeader)))
	} else {
		// Inline receivers finish the previous batch before reading this header.
		// Earlier batches and snapshot construction consume the same budget.
		headerAck, err = p2p.ReadStreamWithBufferLimitAndDeadline(stream, uint32(len(streamAckHeader)), deadline)
	}
	if err != nil {
		return fmt.Errorf("read state batch header ACK: %w", err)
	}
	if string(headerAck) != streamAckHeader {
		return fmt.Errorf("unexpected state batch header ACK: %q", headerAck)
	}
	if err = p2p.WriteStreamWithBuffer(append(prefixBatchData, batchData...), stream); err != nil {
		return fmt.Errorf("write state batch data: %w", err)
	}

	dataAck, err := p2p.ReadStreamWithBufferLimit(stream, uint32(len(streamAckHeader)))
	if err != nil {
		return fmt.Errorf("read state batch data ACK: %w", err)
	}
	if string(dataAck) != streamAckData {
		return fmt.Errorf("unexpected state batch data ACK: %q", dataAck)
	}
	return nil
}

// Add a function to handle receiving batched attestation state
func (s *AttestationGossip) receiveBatchedAttestationState(stream network.Stream, initialData []byte) error {
	remotePeer := stream.Conn().RemotePeer()
	logger := s.logger.With().Str("peer", remotePeer.String()).Str("who", "receiver").Logger()

	// Process batch begin
	if len(initialData) < 4 {
		return fmt.Errorf("invalid batch begin format")
	}

	totalBatches := int(binary.LittleEndian.Uint32(initialData[:4]))
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
	var batchWG sync.WaitGroup
	defer batchWG.Wait()
	// Receive all batches
	for {
		// Read next message
		message, err := p2p.ReadStreamWithBuffer(stream)
		if err != nil {
			return fmt.Errorf("failed to read batch message: %w", err)
		}

		// Check if this is the end marker
		if len(message) >= 1 && bytes.Equal(message[:1], prefixBatchEnd) {
			batchWG.Wait()
			// Send final acknowledgment
			if err = p2p.WriteStreamWithBuffer([]byte(p2p.StreamMsgDone), stream); err != nil {
				return fmt.Errorf("failed to send final ack: %w", err)
			}
			break
		}

		// Must be a batch data header
		if len(message) < 9 || !bytes.Equal(message[:1], prefixBatchHeader) {
			return fmt.Errorf("invalid batch header format")
		}

		// Extract batch information
		batchNum := int(binary.LittleEndian.Uint32(message[1:5]))
		dataSize := int(binary.LittleEndian.Uint32(message[5:9]))

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

		// Add the QuorumTxs to our collection
		batchWG.Add(1)
		go func(batch common.QuorumState) {
			defer batchWG.Done()
			s.processAttestationStateBatch(logger, batch)
		}(batch)

		total += len(batch.QuoTxs)

		logger.Debug().
			Int("batch", batchNum+1).
			Int("total_batches", totalBatches).
			Int("batch_size", len(batch.QuoTxs)).
			Int("total_collected", total).
			Msg("batch received")
	}

	logger.Debug().Int("total_txs", total).Msg("finished receiving attestation state")

	return nil
}

func (s *AttestationGossip) processAttestationStateBatch(logger zerolog.Logger, batch common.QuorumState) {
	ctx := context.TODO()
	for _, qt := range batch.QuoTxs {
		for _, att := range qt.Attestations {
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
			logger.Debug().Msg("handling network fee from peer state dump")
			s.handleNetworkFeeAttestation(ctx, common.AttestNetworkFee{
				NetworkFee:  nf.NetworkFee,
				Attestation: att,
			})
		}
	}
	for _, solvency := range batch.QuoSolvencies {
		for _, att := range solvency.Attestations {
			logger.Debug().Msg("handling solvency from peer state dump")
			s.handleSolvencyAttestation(ctx, common.AttestSolvency{
				Solvency:    solvency.Solvency,
				Attestation: att,
			})
		}
	}
	for _, errataTx := range batch.QuoErrataTxs {
		for _, att := range errataTx.Attestations {
			logger.Debug().Msg("handling errata tx from peer state dump")
			s.handleErrataAttestation(ctx, common.AttestErrataTx{
				ErrataTx:    errataTx.ErrataTx,
				Attestation: att,
			})
		}
	}
	for _, priceFeed := range batch.QuoPriceFeeds {
		for _, att := range priceFeed.Attestations {
			logger.Debug().Msg("handling price feed from peer state dump")
			s.handlePriceFeedAttestation(ctx, common.AttestPriceFeed{
				PriceFeed:   priceFeed.PriceFeed,
				Attestation: att,
			})
		}
	}
}

// The attempt budget includes dialing. Cancellation resets transport, while
// the existing asynchronous receiver still joins any processing it has started.
func (s *AttestationGossip) askPeerForState(parent context.Context, remote peer.ID) bool {
	ctx, cancel := context.WithTimeout(parent, s.stateSyncTimeout())
	defer cancel()
	if ctx.Err() != nil {
		return false
	}
	stream, err := s.host.NewStream(ctx, remote, attestationStateProtocol)
	if err != nil {
		s.logger.Error().Err(err).Msgf("fail to create stream to peer: %s", remote)
		return false
	}
	stop := context.AfterFunc(ctx, func() { _ = stream.Reset() })
	defer stop()
	return s.askStreamForState(stream) && ctx.Err() == nil
}

// askStreamForState will ask a specific stream for its attestation state
func (s *AttestationGossip) askStreamForState(stream network.Stream) bool {
	peer := stream.Conn().RemotePeer()

	defer closeStream(s.logger, stream)

	if err := p2p.WriteStreamWithBuffer(prefixSendState, stream); err != nil {
		s.logger.Error().Err(err).Msgf("fail to write payload to peer: %s", peer)
		return false
	}

	// Read initial response from peer
	initialData, err := p2p.ReadStreamWithBuffer(stream)
	if err != nil {
		s.logger.Error().Err(err).Msgf("fail to read reply from peer: %s", peer)
		return false
	}

	// If it's not the batch begin prefix, it's an error
	if !bytes.HasPrefix(initialData, prefixBatchBegin) {
		s.logger.Error().Msgf("unexpected attestation state format from peer: %s", peer)
		return false
	}

	// Process the batched attestation state
	// Strip the prefixBatchBegin byte before passing to the receiver, matching
	// the parallel call site in attestation_handlers.go which uses data[1:].
	// Without this, receiveBatchedAttestationState reads initialData[:4] as
	// totalBatches and gets garbage (prefix byte | first 3 bytes of real value),
	// breaking the handshake on every state-init exchange after a restart.
	if err := s.receiveBatchedAttestationState(stream, initialData[len(prefixBatchBegin):]); err != nil {
		s.logger.Error().Err(err).Msgf("fail to receive batched attestation state from peer: %s", peer)
		return false
	}
	return true
}
