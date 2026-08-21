package observer

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/common"
)

// AttestObservedTx creates and broadcasts an attestation for an observed transaction
func (s *AttestationGossip) AttestObservedTx(ctx context.Context, obsTx *common.ObservedTx, inbound, allowFutureObservation bool) error {
	if obsTx == nil {
		return fmt.Errorf("observed tx is nil")
	}
	isActive := s.isActiveValidator(s.host.ID())

	signBz, err := obsTx.GetSignablePayloadWithInbound(inbound)
	if err != nil {
		return fmt.Errorf("fail to marshal tx sign payload: %w", err)
	}

	signature, err := s.privKey.Sign(signBz)
	if err != nil {
		return fmt.Errorf("fail to sign tx sign payload: %w", err)
	}

	msg := common.AttestTx{
		ObsTx:                  *obsTx,
		Inbound:                inbound,
		AllowFutureObservation: allowFutureObservation,
		Attestation: &common.Attestation{
			PubKey:    s.pubKey,
			Signature: signature,
		},
	}
	if !isActive {
		if !obsTx.Tx.Chain.Equals(common.XMRChain) || inbound || len(obsTx.SpentOutputRefs) == 0 {
			return fmt.Errorf("skipping attest observed tx: not active")
		}
		// A churned-out member may still certify a spend by a retiring vault it
		// historically controlled. Keep this out of observedTxs and the ordinary
		// batch protocol: it is a source-vault certificate share, never an active
		// validator observation vote.
		if err := s.acceptHistoricalXMRSpentRefCertificate(msg, ""); err != nil {
			return fmt.Errorf("skipping inactive historical XMR certificate: %w", err)
		}
		s.broadcastHistoricalXMRSpentRefCertificate(ctx, msg)
		return nil
	}

	// Handle the attestation locally first
	s.logger.Debug().Msg("handling attestation locally")
	if !s.handleObservedTxAttestationAndQueue(ctx, msg) {
		// A durable XMR errata barrier can reject a stale local observation.
		// Do not broadcast an attestation this node refused to admit itself:
		// peers which have not yet learned the same errata must not be asked to
		// accumulate a vote for the orphaned view.
		return nil
	}

	return nil
}

func closeStream(logger zerolog.Logger, stream network.Stream) {
	if err := stream.Close(); err != nil {
		logger.Error().Err(err).Msg("fail to close stream")
	}
	go func() {
		// Give some time for the remote side to process
		// NOTE: remove this after upgrading to libp2p v0.12.0+, because
		// then Close() will close both write and read sides of the stream.
		// In v0.11.0, it only closes the write side.
		time.Sleep(10 * time.Second)
		if err := stream.Reset(); err != nil {
			logger.Error().Err(err).Msg("fail to reset stream after delay")
		}
	}()
}

// AttestNetworkFee creates and broadcasts an attestation for a network fee
func (s *AttestationGossip) AttestNetworkFee(ctx context.Context, networkFee common.NetworkFee) error {
	if !s.isActiveValidator(s.host.ID()) {
		return fmt.Errorf("skipping attest network fee: not active")
	}

	signBz, err := networkFee.GetSignablePayload()
	if err != nil {
		return fmt.Errorf("fail to marshal network fee sign payload: %w", err)
	}

	signature, err := s.privKey.Sign(signBz)
	if err != nil {
		return fmt.Errorf("fail to sign network fee sign payload: %w", err)
	}

	msg := common.AttestNetworkFee{
		NetworkFee: &networkFee,
		Attestation: &common.Attestation{
			PubKey:    s.pubKey,
			Signature: signature,
		},
	}

	// Handle the attestation locally first
	s.logger.Debug().Msg("handling attestation locally")
	s.handleNetworkFeeAttestation(ctx, msg)

	s.batcher.AddNetworkFee(msg)

	return nil
}

// AttestSolvency creates and broadcasts an attestation for a solvency proof
func (s *AttestationGossip) AttestSolvency(ctx context.Context, solvency common.Solvency) error {
	if !s.isActiveValidator(s.host.ID()) {
		return fmt.Errorf("skipping attest solvency: not active")
	}

	signBz, err := solvency.GetSignablePayload()
	if err != nil {
		return fmt.Errorf("fail to marshal solvency sign payload: %w", err)
	}

	signature, err := s.privKey.Sign(signBz)
	if err != nil {
		return fmt.Errorf("fail to sign solvency sign payload: %w", err)
	}

	msg := common.AttestSolvency{
		Solvency: &solvency,
		Attestation: &common.Attestation{
			PubKey:    s.pubKey,
			Signature: signature,
		},
	}

	// Handle the attestation locally first
	s.logger.Debug().Msg("handling attestation locally")
	s.handleSolvencyAttestation(ctx, msg)

	s.batcher.AddSolvency(msg)

	return nil
}

// AttestErrata creates and broadcasts an attestation for an errata transaction
func (s *AttestationGossip) AttestErrata(ctx context.Context, errata common.ErrataTx) error {
	if !s.isActiveValidator(s.host.ID()) {
		return fmt.Errorf("skipping attest errata tx: not active")
	}

	signBz, err := errata.GetSignablePayload()
	if err != nil {
		return fmt.Errorf("fail to marshal errata sign payload: %w", err)
	}

	signature, err := s.privKey.Sign(signBz)
	if err != nil {
		return fmt.Errorf("fail to sign errata sign payload: %w", err)
	}

	msg := common.AttestErrataTx{
		ErrataTx: &errata,
		Attestation: &common.Attestation{
			PubKey:    s.pubKey,
			Signature: signature,
		},
	}

	// Handle the attestation locally first
	s.logger.Debug().Msg("handling attestation locally")
	// AttestErrata is invoked by the Observer's durable pending-tombstone
	// outbox. Unlike peer gossip, that retry source may re-arm an identical
	// uncommitted vote after the configured throttle interval.
	s.handlePendingErrataAttestation(ctx, msg)

	s.batcher.AddErrataTx(msg)

	return nil
}

// removeObservedTxsForErrata performs the one-time volatile half of Observer's
// initial errata cleanup. AttestErrata deliberately does not call this method:
// its durable outbox may retransmit the same vote until Thornode confirms it,
// and a retransmission must never delete an observation re-mined in the
// meantime. XMR cleanup is scoped to the orphaned height so the same immutable
// transaction can be observed at a new canonical location; legacy chains keep
// their historical TxID-wide behavior.
func (s *AttestationGossip) removeObservedTxsForErrata(errata common.ErrataTx, orphanHeight int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for k, state := range s.observedTxs {
		if !k.Chain.Equals(errata.Chain) || !k.ID.Equals(errata.Id) {
			continue
		}
		if errata.Chain.Equals(common.XMRChain) && orphanHeight > 0 &&
			state != nil && state.Item != nil && state.Item.ObservedTx != nil {
			minedHeight, err := state.Item.ObservedTx.CanonicalMinedHeight()
			if err == nil && minedHeight != orphanHeight {
				continue
			}
		}
		delete(s.observedTxs, k)
	}
	if s.batcher != nil {
		s.batcher.removeObservedTxsForErrata(errata, orphanHeight)
	}
}

// AttestPriceFeed creates and broadcasts an attestation for a price feed
func (s *AttestationGossip) AttestPriceFeed(ctx context.Context, priceFeed common.PriceFeed) error {
	if !s.isActiveValidator(s.host.ID()) {
		return fmt.Errorf("skipping attest price feed: not active")
	}

	signBz, err := priceFeed.GetSignablePayload()
	if err != nil {
		return fmt.Errorf("fail to marshal price feed sign payload: %w", err)
	}

	signature, err := s.privKey.Sign(signBz)
	if err != nil {
		return fmt.Errorf("fail to sign price feed sign payload: %w", err)
	}

	msg := common.AttestPriceFeed{
		PriceFeed: &priceFeed,
		Attestation: &common.Attestation{
			PubKey:    s.pubKey,
			Signature: signature,
		},
	}

	// Handle the attestation locally first
	s.logger.Debug().Msg("handling attestation locally")
	s.handlePriceFeedAttestation(ctx, msg)

	s.batcher.AddPriceFeed(msg)
	s.batcher.sendBatches(ctx, true)

	return nil
}

// handleStreamAttestationState handles incoming observed transaction streams
func (s *AttestationGossip) handleStreamAttestationState(stream network.Stream) {
	remotePeer := stream.Conn().RemotePeer()

	logger := s.logger.With().Str("remote_peer", remotePeer.String()).Logger()
	logger.Debug().Msg("reading attestation state message")

	stateCtx, cancel := s.attestationStateContext(context.Background(), stream)
	defer cancel()

	admission, admitted := s.acquireAttestationStateAdmission(stateCtx, remotePeer)
	if !admitted {
		logger.Warn().Msg("rejecting unauthorized or saturated attestation state stream")
		if err := stream.Reset(); err != nil {
			logger.Debug().Err(err).Msg("fail to reset rejected attestation state stream")
		}
		return
	}
	success := false
	defer func() {
		admission.release()
		if success {
			closeStream(logger, stream)
			return
		}
		if err := stream.Reset(); err != nil {
			logger.Debug().Err(err).Msg("fail to reset rejected attestation state stream")
		}
	}()

	// Read and process the message
	data, err := p2p.ReadStreamWithBufferLimitWithContext(
		stateCtx,
		stream,
		maxAttestationStateControlBytes,
	)
	if err != nil {
		if err != io.EOF {
			logger.Error().Err(err).Msg("fail to read payload from stream")
		}
		return
	}

	// Check message type and handle accordingly
	if len(data) == 0 {
		logger.Error().Msg("empty payload")
		return
	}

	// Handle based on prefix
	switch {
	case data[0] == prefixSendState[0]:
		// Send state request
		if len(data) != 1 {
			logger.Error().Msg("unexpected payload length for send state request")
			return
		}
		// A state snapshot may retain the XMR errata barrier for the whole
		// transfer. Serve one active-validator request globally and charge its
		// per-peer slot instead of letting requests queue behind shared locks.
		if !s.isActiveValidator(remotePeer) {
			logger.Warn().Msg("skipping attestation state request from non-active validator")
			return
		}
		if !s.stateSendActive.CompareAndSwap(false, true) {
			logger.Warn().Msg("skipping concurrent attestation state request")
			return
		}
		defer s.stateSendActive.Store(false)
		sem, err := s.peerMgr.acquireContext(stateCtx, remotePeer)
		if err != nil {
			logger.Warn().Err(err).Msg("skipping busy attestation state requester")
			return
		}
		defer s.peerMgr.release(sem)
		logger.Debug().Msg("handling send state request")
		s.sendAttestationState(stateCtx, stream)
		success = true

	case data[0] == prefixBatchBegin[0]:
		// Honest responses continue on the stream opened by askPeerForState
		// and are read directly by askStreamForState. A batch begin on a fresh
		// inbound stream is unsolicited and must not start another receiver.
		logger.Warn().Msg("rejecting unsolicited attestation state batch stream")

	default:
		logger.Error().Msgf("unknown message type: %d", data[0])
	}
}

func (s *AttestationGossip) handleStreamBatchedAttestations(stream network.Stream) {
	remotePeer := stream.Conn().RemotePeer()
	logger := s.logger.With().Str("remote_peer", remotePeer.String()).Logger()

	defer closeStream(logger, stream)

	if !s.isActiveValidator(remotePeer) {
		logger.Debug().Msg("skipping batched attestation from non-active validator")
		if err := p2p.WriteStreamWithBuffer([]byte(p2p.StreamMsgDone), stream); err != nil {
			logger.Error().Err(err).Msgf("fail to write reply to peer: %s", remotePeer)
		}
		return
	}

	sem, err := s.peerMgr.acquire(remotePeer)
	if err != nil {
		logger.Error().Err(err).Msgf("fail to acquire semaphore for peer: %s", remotePeer)
		if err = p2p.WriteStreamWithBuffer([]byte(p2p.StreamMsgDone), stream); err != nil {
			logger.Error().Err(err).Msgf("fail to write reply to peer: %s", remotePeer)
		}
		return
	}
	defer s.peerMgr.release(sem)

	logger.Debug().Msg("reading batched attestations message")

	// Read batch data
	data, err := p2p.ReadStreamWithBuffer(stream)
	if err != nil {
		if err != io.EOF {
			logger.Error().Err(err).Msg("fail to read payload from stream")
		}
		return
	}

	// Send acknowledgment
	if err := p2p.WriteStreamWithBuffer([]byte(p2p.StreamMsgDone), stream); err != nil {
		logger.Error().Err(err).Msg("fail to write acknowledgment")
		return
	}

	if len(data) == 0 {
		logger.Error().Msg("empty payload")
		return
	}

	// Unmarshal batch
	var batch common.AttestationBatch
	if err := batch.Unmarshal(data); err != nil {
		logger.Error().Err(err).Msg("fail to unmarshal attestation batch")
		return
	}

	// slight tolerance above max
	max := s.batcher.getMaxBatchSize()

	// Process each attestation in the batch
	for i, tx := range batch.AttestTxs {
		if int64(i) >= max {
			logger.Error().Msgf("tx batch size %d exceeds max size %d", len(batch.AttestTxs), max)
			break
		}
		if tx == nil {
			logger.Error().Msgf("tx is nil at index %d", i)
			continue
		}
		s.handleObservedTxAttestation(context.Background(), *tx)
	}

	for i, nf := range batch.AttestNetworkFees {
		if int64(i) >= max {
			logger.Error().Msgf("net fee batch size %d exceeds max size %d", len(batch.AttestNetworkFees), max)
			break
		}
		if nf == nil {
			logger.Error().Msgf("network fee is nil at index %d", i)
			continue
		}
		s.handleNetworkFeeAttestation(context.Background(), *nf)
	}

	for i, solvency := range batch.AttestSolvencies {
		if int64(i) >= max {
			logger.Error().Msgf("solvency batch size %d exceeds max size %d", len(batch.AttestSolvencies), max)
			break
		}
		if solvency == nil {
			logger.Error().Msgf("solvency is nil at index %d", i)
			continue
		}
		s.handleSolvencyAttestation(context.Background(), *solvency)
	}

	for i, errata := range batch.AttestErrataTxs {
		if int64(i) >= max {
			logger.Error().Msgf("errata batch size %d exceeds max size %d", len(batch.AttestErrataTxs), max)
			break
		}
		if errata == nil {
			logger.Error().Msgf("errata is nil at index %d", i)
			continue
		}
		s.handleErrataAttestation(context.Background(), *errata)
	}

	for i, priceFeed := range batch.AttestPriceFeeds {
		if int64(i) >= max {
			logger.Error().Msgf("price feed size %d exceeds max size %d", len(batch.AttestPriceFeeds), max)
			break
		}
		if priceFeed == nil {
			logger.Error().Msgf("price feed is nil at index %d", i)
			continue
		}
		s.handlePriceFeedAttestation(context.Background(), *priceFeed)
	}
}
