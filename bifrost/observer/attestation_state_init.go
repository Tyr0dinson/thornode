package observer

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/common"
)

// askForAttestationState collects one parallel group. A failed peer never
// cancels other imports; the owner waits for the whole group before retrying.
func (s *AttestationGossip) askForAttestationState(ctx context.Context, peers ...peer.ID) attestationStateGroupResult {
	outcomes := make(chan attestationStateTransferResult, len(peers))
	for _, id := range peers {
		go func() { outcomes <- s.askPeerForState(ctx, id) }()
	}
	var group attestationStateGroupResult
	for range peers {
		result := <-outcomes
		switch {
		case !result.completed:
			group.failed++
			if result.items > 0 {
				group.partial++
			}
		case result.items == 0:
			group.empty++
		default:
			group.nonEmpty++
		}
	}
	s.logger.Info().Int("peers", len(peers)).
		Int("completed_empty", group.empty).Int("completed_non_empty", group.nonEmpty).
		Int("failed", group.failed).Int("partial", group.partial).
		Str("outcome", group.outcome()).Msg("startup attestation state group completed")
	return group
}

func (s *AttestationGossip) sendAttestationState(stream network.Stream, deadline time.Time) error {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	return s.sendAttestationStateWithContext(ctx, stream)
}

func (s *AttestationGossip) sendAttestationStateWithContext(ctx context.Context, stream network.Stream) error {
	deadline, _ := ctx.Deadline()
	remotePeer := stream.Conn().RemotePeer()

	s.logger.Debug().Str("peer", remotePeer.String()).Msg("sending attestation state")
	// Snapshot under the map/state locks. Each frame revalidates durable XMR
	// admission later; no transport operation holds a cleanup reader.
	if err := lockAttestationContext(ctx, &s.mu); err != nil {
		return err
	}
	// Count exported chunks, not stored buckets: empty and fully committed
	// state carries no evidence for the receiver and does not use the budget.
	remainingItems := maxAttestationStateItemsPerTransfer

	// Collect all QuorumTxs
	allQuorumTxs := make([]*common.QuorumTx, 0, min(len(s.observedTxs), remainingItems))
	for k, ots := range s.observedTxs {
		if err := lockAttestationContext(ctx, &ots.mu); err != nil {
			s.mu.Unlock()
			return err
		}
		chunks, err := copyStateSyncAttestations(ots, remainingItems)
		if err != nil {
			exportErr := fmt.Errorf("transaction %s/%s: %w", ots.Item.ObservedTx.Tx.Chain, ots.Item.ObservedTx.Tx.ID, err)
			ots.mu.Unlock()
			s.mu.Unlock()
			return s.stateExportError(exportErr)
		}
		if len(chunks) == 0 {
			ots.mu.Unlock()
			continue
		}

		for _, attestations := range chunks {
			quorumTxs := &common.QuorumTx{
				ObsTx:                  *ots.Item.ObservedTx,
				Inbound:                k.Inbound,
				AllowFutureObservation: k.AllowFutureObservation,
				Attestations:           attestations,
			}
			allQuorumTxs = append(allQuorumTxs, quorumTxs)
		}

		remainingItems -= len(chunks)
		ots.mu.Unlock()
	}

	allNetworkFees := make([]*common.QuorumNetworkFee, 0, min(len(s.networkFees), remainingItems))
	for _, nf := range s.networkFees {
		if err := lockAttestationContext(ctx, &nf.mu); err != nil {
			s.mu.Unlock()
			return err
		}
		chunks, err := copyStateSyncAttestations(nf, remainingItems)
		if err != nil {
			exportErr := fmt.Errorf("network fee %s/%d: %w", nf.Item.Chain, nf.Item.Height, err)
			nf.mu.Unlock()
			s.mu.Unlock()
			return s.stateExportError(exportErr)
		}
		if len(chunks) == 0 {
			nf.mu.Unlock()
			continue
		}
		nfCopy := &common.NetworkFee{
			Chain:           nf.Item.Chain,
			Height:          nf.Item.Height,
			TransactionSize: nf.Item.TransactionSize,
			TransactionRate: nf.Item.TransactionRate,
		}
		for _, attestations := range chunks {
			allNetworkFees = append(allNetworkFees, &common.QuorumNetworkFee{
				NetworkFee:   nfCopy,
				Attestations: attestations,
			})
		}

		remainingItems -= len(chunks)
		nf.mu.Unlock()
	}

	allSolvencies := make([]*common.QuorumSolvency, 0, min(len(s.solvencies), remainingItems))
	for _, solvency := range s.solvencies {
		if err := lockAttestationContext(ctx, &solvency.mu); err != nil {
			s.mu.Unlock()
			return err
		}
		chunks, err := copyStateSyncAttestations(solvency, remainingItems)
		if err != nil {
			exportErr := fmt.Errorf("solvency %s/%s: %w", solvency.Item.Chain, solvency.Item.Id, err)
			solvency.mu.Unlock()
			s.mu.Unlock()
			return s.stateExportError(exportErr)
		}
		if len(chunks) == 0 {
			solvency.mu.Unlock()
			continue
		}
		sCopy := &common.Solvency{
			Id:     solvency.Item.Id,
			Chain:  solvency.Item.Chain,
			Height: solvency.Item.Height,
			PubKey: solvency.Item.PubKey,
			Coins:  solvency.Item.Coins.Copy(),
		}
		for _, attestations := range chunks {
			allSolvencies = append(allSolvencies, &common.QuorumSolvency{
				Solvency:     sCopy,
				Attestations: attestations,
			})
		}

		remainingItems -= len(chunks)
		solvency.mu.Unlock()
	}

	allErrataTxs := make([]*common.QuorumErrataTx, 0, min(len(s.errataTxs), remainingItems))
	for _, errataTx := range s.errataTxs {
		if err := lockAttestationContext(ctx, &errataTx.mu); err != nil {
			s.mu.Unlock()
			return err
		}
		chunks, err := copyStateSyncAttestations(errataTx, remainingItems)
		if err != nil {
			exportErr := fmt.Errorf("errata %s/%s: %w", errataTx.Item.Chain, errataTx.Item.Id, err)
			errataTx.mu.Unlock()
			s.mu.Unlock()
			return s.stateExportError(exportErr)
		}
		if len(chunks) == 0 {
			errataTx.mu.Unlock()
			continue
		}
		eTxCopy := &common.ErrataTx{
			Chain: errataTx.Item.Chain,
			Id:    errataTx.Item.Id,
		}
		for _, attestations := range chunks {
			allErrataTxs = append(allErrataTxs, &common.QuorumErrataTx{
				ErrataTx:     eTxCopy,
				Attestations: attestations,
			})
		}

		remainingItems -= len(chunks)
		errataTx.mu.Unlock()
	}

	allPriceFeeds := make([]*common.QuorumPriceFeed, 0, min(len(s.priceFeeds), remainingItems))
	for _, priceFeed := range s.priceFeeds {
		if err := lockAttestationContext(ctx, &priceFeed.mu); err != nil {
			s.mu.Unlock()
			return err
		}
		chunks, err := copyStateSyncAttestations(priceFeed, remainingItems)
		if err != nil {
			exportErr := fmt.Errorf("price feed %d: %w", priceFeed.Item.Time, err)
			priceFeed.mu.Unlock()
			s.mu.Unlock()
			return s.stateExportError(exportErr)
		}
		if len(chunks) == 0 {
			priceFeed.mu.Unlock()
			continue
		}
		pfCopy := &common.PriceFeed{
			Time:    priceFeed.Item.Time,
			Rates:   priceFeed.Item.Rates,
			Version: bytes.Clone(priceFeed.Item.Version),
		}
		for _, attestations := range chunks {
			allPriceFeeds = append(allPriceFeeds, &common.QuorumPriceFeed{
				PriceFeed:    pfCopy,
				Attestations: attestations,
			})
		}

		remainingItems -= len(chunks)
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
	if len(batches) > maxAttestationStateBatches {
		return s.stateExportError(fmt.Errorf("local state has %d batches, maximum %d", len(batches), maxAttestationStateBatches))
	}
	// Validate the complete export before announcing it. The receiver accepts
	// these same bounds; nothing is dropped to make an oversized snapshot fit.
	for i, batch := range batches {
		if batch.Size() > p2p.MaxPayload-1 {
			return s.stateExportError(fmt.Errorf("local state batch %d exceeds wire payload maximum", i))
		}
		if err := validateAttestationStateBatch(batch); err != nil {
			return s.stateExportError(fmt.Errorf("local state batch %d: %w", i, err))
		}
	}

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
		return err
	}

	// Wait for acknowledgment
	ack, err := p2p.ReadStreamWithBufferLimit(stream, uint32(len(streamAckHeader)))
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to read ack after batch begin")
		return err
	}

	if totalBatches == 0 {
		// Receiver replied "done" because there are no batches to transfer.
		// Validate and exit cleanly - falling through to the streamAckBegin
		// check below would log a false "unexpected response to batch begin"
		// error in this success path.
		if string(ack) != p2p.StreamMsgDone {
			return fmt.Errorf("unexpected final response: %q", ack)
		}
		return nil
	}

	if string(ack) != streamAckBegin {
		s.logger.Error().Str("response", string(ack)).Msg("unexpected response to batch begin")
		return fmt.Errorf("unexpected state acknowledgement: %q", ack)
	}

	// Send batches
	for batchNum, batch := range batches {
		if err = s.sendAttestationStateBatchWithContext(ctx, stream, batchNum, batch, deadline); err != nil {
			s.logger.Error().Err(err).Int("batch", batchNum+1).Msg("failed to send state batch")
			return err
		}

		s.logger.Debug().
			Int("batch", batchNum+1).
			Int("total_batches", totalBatches).
			Msg("batch sent")
	}

	// Send batch end signal
	if err = p2p.WriteStreamWithBuffer(prefixBatchEnd, stream); err != nil {
		s.logger.Error().Err(err).Msg("failed to send batch end signal")
		return err
	}

	// The receiver sends done only after processing the final batch.
	var endAck []byte
	endAck, err = p2p.ReadStreamWithBufferLimitAndDeadline(stream, uint32(len(streamAckHeader)), deadline)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to read final ack")
		return err
	}
	if string(endAck) != p2p.StreamMsgDone {
		s.logger.Error().Str("response", string(endAck)).Msg("unexpected final response")
		return fmt.Errorf("unexpected state acknowledgement: %q", endAck)
	}

	s.logger.Debug().
		Int("total_txs", totalTxs).
		Int("total_network_fees", totalNfs).
		Int("total_solvencies", totalSolvencies).
		Int("total_errata_txs", totalErrataTxs).
		Int("total_price_feeds", totalPriceFeeds).
		Int("total_batches", totalBatches).
		Msg("successfully sent attestation state in batches")
	return nil
}

// sendAttestationStateBatch rechecks durable XMR admission for each frame.
// Transport and ACK waits hold no cleanup reader. A frame already prepared
// may arrive after cleanup; the receiving peer applies its own admission check.
// The sender owns one absolute deadline, including snapshot construction.
func (s *AttestationGossip) sendAttestationStateBatch(stream network.Stream, batchNum int, batch common.QuorumState, deadline time.Time) error {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	return s.sendAttestationStateBatchWithContext(ctx, stream, batchNum, batch, deadline)
}

func (s *AttestationGossip) sendAttestationStateBatchWithContext(ctx context.Context, stream network.Stream, batchNum int, batch common.QuorumState, deadline time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	hasXMR := false
	for _, tx := range batch.QuoTxs {
		if tx.ObsTx.Tx.Chain.Equals(common.XMRChain) {
			hasXMR = true
			break
		}
	}
	if hasXMR {
		if err := lockAttestationContext(ctx, &s.mu); err != nil {
			return err
		}
		kept := batch.QuoTxs[:0]
		for _, tx := range batch.QuoTxs {
			if tx.ObsTx.Tx.Chain.Equals(common.XMRChain) {
				if admitted, admissionErr := s.admitXMRObservationLocked(tx.ObsTx); admissionErr != nil || !admitted {
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

func (s *AttestationGossip) receiveBatchedAttestationState(stream network.Stream, initialData []byte) error {
	return s.receiveBatchedAttestationStateWithContext(context.Background(), stream, initialData)
}

func (s *AttestationGossip) receiveBatchedAttestationStateWithContext(ctx context.Context, stream network.Stream, initialData []byte) error {
	result, err := s.receiveAttestationStateTransfer(ctx, stream, initialData)
	if err != nil {
		return err
	}
	return result.incomplete
}

// Decode, preflight and import one bounded batch at a time. The same absolute
// deadline cancels transport, lock waits and queries; no batch workers outlive it.
func (s *AttestationGossip) receiveAttestationStateTransfer(parent context.Context, stream network.Stream, initialData []byte) (result attestationStateImportResult, receiveErr error) {
	ctx, stopDeadline := s.stateExchangeDeadline(parent, stream)
	defer stopDeadline()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	logger := s.logger.With().Str("peer", stream.Conn().RemotePeer().String()).Logger()
	if len(initialData) != 4 {
		return result, fmt.Errorf("invalid batch begin format")
	}
	totalBatches := binary.LittleEndian.Uint32(initialData)
	if totalBatches > maxAttestationStateBatches {
		return result, fmt.Errorf("batch count %d exceeds maximum %d", totalBatches, maxAttestationStateBatches)
	}
	if totalBatches == 0 {
		return result, p2p.WriteStreamWithBuffer([]byte(p2p.StreamMsgDone), stream)
	}
	if err := p2p.WriteStreamWithBuffer([]byte(streamAckBegin), stream); err != nil {
		return result, err
	}
	totalItems := 0
	for expected := uint32(0); expected < totalBatches; expected++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		message, err := p2p.ReadStreamWithBufferLimit(stream, 9)
		if err != nil {
			return result, fmt.Errorf("read batch header: %w", err)
		}
		if len(message) != 9 || message[0] != prefixBatchHeader[0] {
			return result, fmt.Errorf("invalid batch header format")
		}
		if number := binary.LittleEndian.Uint32(message[1:5]); number != expected {
			return result, fmt.Errorf("unexpected batch number: expected %d, got %d", expected, number)
		}
		dataSize := binary.LittleEndian.Uint32(message[5:9])
		if dataSize > p2p.MaxPayload-1 {
			return result, fmt.Errorf("batch data size %d exceeds maximum %d", dataSize, p2p.MaxPayload-1)
		}
		if err = p2p.WriteStreamWithBuffer([]byte(streamAckHeader), stream); err != nil {
			return result, err
		}
		// Enforce the declared size before allocating the framed data as well.
		message, err = p2p.ReadStreamWithBufferLimit(stream, dataSize+1)
		if err != nil {
			return result, fmt.Errorf("read batch data: %w", err)
		}
		if len(message) != int(dataSize)+1 || message[0] != prefixBatchData[0] {
			return result, fmt.Errorf("invalid batch data format or size")
		}
		var batch common.QuorumState
		if err = batch.Unmarshal(message[1:]); err != nil {
			return result, fmt.Errorf("unmarshal state batch: %w", err)
		}
		totalItems, err = validateAttestationStateTransferCount(totalItems, batch)
		if err != nil {
			return result, err
		}
		if err = validateAttestationStateBatch(batch); err != nil {
			return result, err
		}
		if err = p2p.WriteStreamWithBuffer([]byte(streamAckData), stream); err != nil {
			return result, err
		}
		// Preserve the data-ACK timing of honest senders. The processor also
		// preflights to protect direct callers; neither check verifies signatures.
		imported, err := s.importAttestationStateBatch(ctx, logger, batch)
		result.items += imported.items
		if result.incomplete == nil {
			result.incomplete = imported.incomplete
		}
		if err != nil {
			return result, err
		}
	}
	end, err := p2p.ReadStreamWithBufferLimit(stream, 1)
	if err != nil {
		return result, fmt.Errorf("read batch end: %w", err)
	}
	if !bytes.Equal(end, prefixBatchEnd) {
		return result, fmt.Errorf("expected batch end after %d batches", totalBatches)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, p2p.WriteStreamWithBuffer([]byte(p2p.StreamMsgDone), stream)
}

func (s *AttestationGossip) processAttestationStateBatch(logger zerolog.Logger, batch common.QuorumState) error {
	return s.processAttestationStateBatchWithContext(context.Background(), logger, batch)
}

func (s *AttestationGossip) processAttestationStateBatchWithContext(ctx context.Context, logger zerolog.Logger, batch common.QuorumState) error {
	result, err := s.importAttestationStateBatch(ctx, logger, batch)
	if err != nil {
		return err
	}
	return result.incomplete
}

func (s *AttestationGossip) importAttestationStateBatch(ctx context.Context, logger zerolog.Logger, batch common.QuorumState) (attestationStateImportResult, error) {
	var result attestationStateImportResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := validateAttestationStateBatch(batch); err != nil {
		return result, err
	}
	for _, qt := range batch.QuoTxs {
		itemAccepted := false
		for _, att := range qt.Attestations {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			logger.Debug().Msg("handling attestation from peer state dump")
			outcome := s.handleObservedTxAttestationFromStateSync(ctx, common.AttestTx{
				ObsTx:                  qt.ObsTx,
				Inbound:                qt.Inbound,
				Attestation:            att,
				AllowFutureObservation: qt.AllowFutureObservation,
			})
			result.record(outcome, &itemAccepted)
		}
	}
	for _, nf := range batch.QuoNetworkFees {
		itemAccepted := false
		for _, att := range nf.Attestations {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			logger.Debug().Msg("handling network fee from peer state dump")
			outcome := s.handleNetworkFeeAttestationWithStateSyncCancellation(ctx, common.AttestNetworkFee{
				NetworkFee:  nf.NetworkFee,
				Attestation: att,
			}, true)
			result.record(outcome, &itemAccepted)
		}
	}
	for _, solvency := range batch.QuoSolvencies {
		itemAccepted := false
		for _, att := range solvency.Attestations {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			logger.Debug().Msg("handling solvency from peer state dump")
			outcome := s.handleSolvencyAttestationWithStateSyncCancellation(ctx, common.AttestSolvency{
				Solvency:    solvency.Solvency,
				Attestation: att,
			}, true)
			result.record(outcome, &itemAccepted)
		}
	}
	for _, errataTx := range batch.QuoErrataTxs {
		itemAccepted := false
		for _, att := range errataTx.Attestations {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			logger.Debug().Msg("handling errata tx from peer state dump")
			outcome := s.handleErrataAttestationWithRetry(ctx, common.AttestErrataTx{
				ErrataTx:    errataTx.ErrataTx,
				Attestation: att,
			}, false, true)
			result.record(outcome, &itemAccepted)
		}
	}
	for _, priceFeed := range batch.QuoPriceFeeds {
		itemAccepted := false
		for _, att := range priceFeed.Attestations {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			logger.Debug().Msg("handling price feed from peer state dump")
			// Accepted price feeds keep their existing deferred send after a
			// successful transfer closes its context. Admission still uses ctx.
			outcome := s.handlePriceFeedAttestationWithSendContext(ctx, context.WithoutCancel(ctx), common.AttestPriceFeed{
				PriceFeed:   priceFeed.PriceFeed,
				Attestation: att,
			}, true)
			result.record(outcome, &itemAccepted)
		}
	}
	return result, ctx.Err()
}

// The attempt deadline starts before dialing and is inherited by the stream.
func (s *AttestationGossip) askPeerForState(ctx context.Context, id peer.ID) attestationStateTransferResult {
	ctx, cancel := s.stateExchangeContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return attestationStateTransferResult{err: err}
	}
	stream, err := s.host.NewStream(ctx, id, attestationStateProtocol)
	if err != nil {
		s.logger.Debug().Err(err).Str("peer", id.String()).Msg("attestation state dial failed")
		return attestationStateTransferResult{err: err}
	}
	return s.askStreamForStateWithContext(ctx, stream)
}

func (s *AttestationGossip) askStreamForState(stream network.Stream) attestationStateTransferResult {
	return s.askStreamForStateWithContext(context.Background(), stream)
}

func (s *AttestationGossip) askStreamForStateWithContext(parent context.Context, stream network.Stream) (result attestationStateTransferResult) {
	ctx, stopDeadline := s.stateExchangeDeadline(parent, stream)
	defer stopDeadline()
	// Stream cleanup follows wire completion; local import misses still retry.
	wireComplete := false
	defer func() {
		if wireComplete {
			closeStream(s.logger, stream)
		} else {
			_ = stream.Reset()
		}
		if result.err != nil {
			s.logger.Debug().Err(result.err).Int("partial_items", result.items).
				Str("peer", stream.Conn().RemotePeer().String()).Msg("attestation state transfer failed")
		}
	}()
	if result.err = p2p.WriteStreamWithBuffer(prefixSendState, stream); result.err != nil {
		return
	}
	initialData, err := p2p.ReadStreamWithBufferLimit(stream, 5)
	if err != nil {
		result.err = err
		return
	}
	if !bytes.HasPrefix(initialData, prefixBatchBegin) {
		result.err = fmt.Errorf("unexpected attestation state response format")
		return
	}
	imported, err := s.receiveAttestationStateTransfer(ctx, stream, initialData[len(prefixBatchBegin):])
	result.items = imported.items
	result.err = err
	wireComplete = err == nil
	if wireComplete {
		result.err = imported.incomplete
	}
	result.completed = result.err == nil
	return
}
