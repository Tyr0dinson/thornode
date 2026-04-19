package observer

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const priceFeedsUpdateDelay = time.Millisecond * 100

const (
	maxXMRKeyImages = 64
)

func requireXMROutboundTxKeys(obsTx common.ObservedTx, inbound bool) error {
	if inbound || !obsTx.Tx.Chain.Equals(common.XMRChain) {
		return nil
	}
	if len(obsTx.TxKeys) == 0 {
		return fmt.Errorf("xmr outbound attestation requires tx keys for %s", obsTx.Tx.ID)
	}
	return nil
}

func requireXMRFinalInboundKeyImages(obsTx common.ObservedTx, inbound bool) error {
	if !inbound || !obsTx.Tx.Chain.Equals(common.XMRChain) || !obsTx.IsFinal() {
		return nil
	}
	if len(obsTx.XmrKeyImages) == 0 {
		return fmt.Errorf("xmr finalized inbound attestation requires key images for %s", obsTx.Tx.ID)
	}
	if len(obsTx.XmrKeyImages) > maxXMRKeyImages {
		return fmt.Errorf("xmr finalized inbound attestation has too many key images for %s", obsTx.Tx.ID)
	}
	return nil
}

func requireXMRAttestationMetadata(obsTx common.ObservedTx, inbound bool) error {
	if err := requireXMROutboundTxKeys(obsTx, inbound); err != nil {
		return err
	}
	return requireXMRFinalInboundKeyImages(obsTx, inbound)
}

func (s *AttestationGossip) requireXMRFinalInboundPropagation(obsTx common.ObservedTx, inbound bool) error {
	if !inbound || !obsTx.Tx.Chain.Equals(common.XMRChain) || !obsTx.IsFinal() {
		return nil
	}
	if s.observerValidateInboundXMRKeyImages == nil {
		return nil
	}
	if err := s.observerValidateInboundXMRKeyImages(obsTx); err != nil {
		return fmt.Errorf("xmr finalized inbound attestation is not ready for %s: %w", obsTx.Tx.ID, err)
	}
	return nil
}

func (s *AttestationGossip) requireXMRFinalOutboundPropagation(obsTx common.ObservedTx, inbound bool) error {
	if inbound || !obsTx.Tx.Chain.Equals(common.XMRChain) || !obsTx.IsFinal() {
		return nil
	}
	if s.observerValidateXMRTx == nil {
		return nil
	}
	if err := s.observerValidateXMRTx(obsTx); err != nil {
		return fmt.Errorf("xmr finalized outbound attestation is not ready for %s: %w", obsTx.Tx.ID, err)
	}
	return nil
}

func useActiveValidatorQuorumForObservedTx(obsTx common.ObservedTx, inbound bool) bool {
	return !inbound && obsTx.Tx.Chain.Equals(common.XMRChain) && len(obsTx.TxKeys) > 0
}

// handleObservedTxAttestation processes attestations for observed transactions
func (s *AttestationGossip) handleObservedTxAttestation(ctx context.Context, tx common.AttestTx) {
	obsTx := tx.ObsTx
	if err := requireXMRAttestationMetadata(obsTx, tx.Inbound); err != nil {
		s.logger.Warn().
			Err(err).
			Str("tx_id", obsTx.Tx.ID.String()).
			Msg("dropping xmr attestation without required metadata")
		return
	}

	k, err := observedTxKey(obsTx, tx.AllowFutureObservation, tx.Inbound)
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to derive observed tx attestation key")
		return
	}

	s.mu.Lock()
	state, ok := s.observedTxs[k]
	if !ok {
		state = s.observedTxsPool.NewAttestationState(&obsTx)
		s.observedTxs[k] = state
	}

	// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
	// This order must be maintained everywhere to avoid deadlock.
	state.mu.Lock()
	s.mu.Unlock()

	defer state.mu.Unlock()

	// Add the attestation
	if err := state.AddAttestation(tx.Attestation); err != nil {
		s.logger.Error().Err(err).Msg("fail to add attestation")
		return
	}

	// Non-signing nodes cannot independently observe XMR outbounds via
	// their scanner (the sidecar only stores OutgoingTransferRecord during
	// signing). They must receive tx keys from a signing peer, validate the
	// outbound locally via the sidecar, and only then co-attest. Missing or
	// invalid tx keys are a hard blocker for outbound attestation propagation.
	var shouldValidateAndCoAttestOutbound bool
	isXMROutboundWithTxKeys := obsTx.Tx.Chain.Equals(common.XMRChain) &&
		!tx.Inbound &&
		len(obsTx.TxKeys) > 0
	isXMROutboundWithKeyImages := isXMROutboundWithTxKeys &&
		obsTx.IsFinal() &&
		len(obsTx.XmrKeyImages) > 0 &&
		len(obsTx.XmrKeyImages) <= maxXMRKeyImages
	if isXMROutboundWithTxKeys && !bytes.Equal(tx.Attestation.PubKey, s.pubKey) {
		shouldValidateAndCoAttestOutbound = !hasLocalObservedTxAttestation(state, s.pubKey)
	}

	// Final XMR inbound observations carry the key-image proof set. Non-signing
	// nodes should only co-attest after they validate and apply the proofs
	// locally, so they never sign a forged or incomplete final observation.
	var shouldValidateAndCoAttestInbound bool
	isXMRInboundWithKeyImages := obsTx.Tx.Chain.Equals(common.XMRChain) &&
		tx.Inbound &&
		obsTx.IsFinal() &&
		len(obsTx.XmrKeyImages) > 0 &&
		len(obsTx.XmrKeyImages) <= maxXMRKeyImages
	if isXMRInboundWithKeyImages && !bytes.Equal(tx.Attestation.PubKey, s.pubKey) {
		shouldValidateAndCoAttestInbound = !hasLocalObservedTxAttestation(state, s.pubKey)
	}

	// INFO-level breadcrumb for XMR final attestations so we can see which
	// peers' attestations actually reach this node's gossip handler. Narrow
	// to XMR final observations to keep log volume tolerable.
	if obsTx.Tx.Chain.Equals(common.XMRChain) &&
		((tx.Inbound && obsTx.IsFinal() && len(obsTx.XmrKeyImages) > 0) ||
			(!tx.Inbound && len(obsTx.TxKeys) > 0)) {
		fromLocal := bytes.Equal(tx.Attestation.PubKey, s.pubKey)
		s.logger.Info().
			Str("tx_id", obsTx.Tx.ID.String()).
			Bool("inbound", tx.Inbound).
			Bool("final", obsTx.IsFinal()).
			Int("xmr_key_images", len(obsTx.XmrKeyImages)).
			Int("tx_keys", len(obsTx.TxKeys)).
			Bool("from_local", fromLocal).
			Bool("should_co_attest_inbound", shouldValidateAndCoAttestInbound).
			Bool("should_co_attest_outbound", shouldValidateAndCoAttestOutbound).
			Int("attestation_count", state.AttestationCount()).
			Msg("received XMR attestation")
	}

	// Determine the number of validators needed for attestation
	var total int
	if k.AllowFutureObservation && !useActiveValidatorQuorumForObservedTx(obsTx, k.Inbound) {
		keysignParty, err := s.getKeysignParty(obsTx.ObservedPubKey)
		if err != nil {
			s.logger.Error().Err(err).Msg("fail to get key sign party")
			return
		}
		total = len(keysignParty)
	} else {
		total = s.activeValidatorCount()
	}

	hasSuperMajority := types.HasSuperMajority(state.AttestationCount(), total)

	// If we have a supermajority, send to thornode
	if hasSuperMajority {
		s.logger.Debug().Msgf("has supermajority: %d/%d", state.AttestationCount(), total)

		s.sendObservedTxAttestationsToThornode(ctx, obsTx, state, k.Inbound, k.AllowFutureObservation, true)
	} else {
		s.logger.Debug().Msgf("observed tx attestation received - %s, id: %s, inbound: %t, final: %t, quorum: %d/%d",
			k.Chain, k.ID, k.Inbound, k.Finalized, state.AttestationCount(), total)
	}

	// Co-attest to XMR outbounds only after tx-key validation succeeds. This
	// prevents non-signers from blindly propagating forged outbound
	// observations.
	if shouldValidateAndCoAttestOutbound {
		cp := obsTx
		if isXMROutboundWithKeyImages {
			s.scheduleOutboundXMRKeyImageValidation(ctx, cp, tx.AllowFutureObservation)
		} else {
			s.validateXMRObservationAsync(cp, func() {
				if err := s.attestObservedTxWithFutureFlag(ctx, &cp, false, tx.AllowFutureObservation); err != nil {
					s.logger.Err(err).
						Str("tx_id", cp.Tx.ID.String()).
						Msg("failed to co-attest XMR outbound")
				}
			})
		}
	}

	if shouldValidateAndCoAttestInbound {
		s.scheduleInboundXMRKeyImageValidation(ctx, obsTx, tx.AllowFutureObservation)
	}
}

func hasLocalObservedTxAttestation(state *AttestationState[*common.ObservedTx], localPubKey []byte) bool {
	if state == nil {
		return false
	}
	for _, att := range state.attestations {
		if bytes.Equal(att.attestation.PubKey, localPubKey) {
			return true
		}
	}
	return false
}

func (s *AttestationGossip) validateAndCoAttestInboundXMRKeyImages(ctx context.Context, obsTx common.ObservedTx, allowFutureObservation bool) {
	if s.observerValidateInboundXMRKeyImages == nil {
		s.logger.Debug().
			Str("tx_id", obsTx.Tx.ID.String()).
			Msg("skipping XMR inbound co-attest: no key-image validator configured")
		return
	}
	if err := s.observerValidateInboundXMRKeyImages(obsTx); err != nil {
		s.logger.Err(err).
			Str("tx_id", obsTx.Tx.ID.String()).
			Msg("failed to validate XMR inbound key-images before co-attest")
		return
	}
	if err := s.attestObservedTxWithFutureFlag(ctx, &obsTx, true, allowFutureObservation); err != nil {
		s.logger.Err(err).
			Str("tx_id", obsTx.Tx.ID.String()).
			Msg("failed to co-attest XMR inbound final observation")
	}
}

func (s *AttestationGossip) validateAndCoAttestOutboundXMRKeyImages(ctx context.Context, obsTx common.ObservedTx, allowFutureObservation bool) {
	if s.observerValidateXMRTx == nil {
		s.logger.Debug().
			Str("tx_id", obsTx.Tx.ID.String()).
			Msg("skipping XMR outbound co-attest: no validator configured")
		return
	}
	if err := s.observerValidateXMRTx(obsTx); err != nil {
		s.logger.Err(err).
			Str("tx_id", obsTx.Tx.ID.String()).
			Msg("failed to validate XMR outbound key-images before co-attest")
		return
	}
	if err := s.attestObservedTxWithFutureFlag(ctx, &obsTx, false, allowFutureObservation); err != nil {
		s.logger.Err(err).
			Str("tx_id", obsTx.Tx.ID.String()).
			Msg("failed to co-attest XMR outbound final observation")
	}
}

// attestObservedTxWithFutureFlag creates and broadcasts a local attestation for
// an observed tx, preserving the AllowFutureObservation flag from the original
// signer. This is used by non-signing nodes to co-attest XMR outbounds.
func (s *AttestationGossip) attestObservedTxWithFutureFlag(ctx context.Context, obsTx *common.ObservedTx, inbound, allowFutureObservation bool) error {
	if !s.isActiveValidator(s.host.ID()) {
		return fmt.Errorf("skipping co-attest: not active")
	}
	if err := requireXMRAttestationMetadata(*obsTx, inbound); err != nil {
		return err
	}

	signBz, err := obsTx.GetSignablePayload()
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

	s.handleObservedTxAttestation(ctx, msg)
	s.batcher.AddObservedTx(msg)

	return nil
}

// sendObservedTxAttestationsToThornode sends attestations to thornode via gRPC
func (s *AttestationGossip) sendObservedTxAttestationsToThornode(
	ctx context.Context,
	tx common.ObservedTx,
	state *AttestationState[*common.ObservedTx],
	inbound, allowFutureObservation, isQuorum bool,
) {
	if err := requireXMRAttestationMetadata(tx, inbound); err != nil {
		s.logger.Error().
			Err(err).
			Str("tx_id", tx.Tx.ID.String()).
			Msg("refusing to send xmr quorum tx without required metadata")
		return
	}
	if err := s.requireXMRFinalOutboundPropagation(tx, inbound); err != nil {
		s.logger.Error().
			Err(err).
			Str("tx_id", tx.Tx.ID.String()).
			Msg("refusing to send finalized xmr outbound before tx metadata is fully propagated")
		return
	}
	unsent := state.UnsentAttestations()
	if len(unsent) == 0 {
		s.logger.Debug().Msg("no unsent observed tx attestations")
		return
	}
	// Send via gRPC to thornode
	if _, err := s.grpcClient.SendQuorumTx(ctx, &common.QuorumTx{
		ObsTx:                  tx,
		Attestations:           unsent,
		Inbound:                inbound,
		AllowFutureObservation: allowFutureObservation,
	}); err != nil {
		s.logger.Error().Err(err).Msg("fail to send quorum tx")
		return
	}

	s.logger.Info().Msgf("sent quorum tx to thornode - %s, id: %s, inbound: %t, final: %t, attestations: %s",
		tx.Tx.Chain, tx.Tx.ID, inbound, tx.IsFinal(), state.State())

	// Mark attestations as sent
	state.MarkAttestationsSent(isQuorum)
}

// handleNetworkFeeAttestation processes attestations for network fees
func (s *AttestationGossip) handleNetworkFeeAttestation(ctx context.Context, anf common.AttestNetworkFee) {
	// Use the network fee as the map key
	k := *anf.NetworkFee

	s.mu.Lock()
	state, ok := s.networkFees[k]
	if !ok {
		// Create a new attestation state
		state = s.networkFeesPool.NewAttestationState(anf.NetworkFee)
		s.networkFees[k] = state
	}

	// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
	// This order must be maintained everywhere to avoid deadlock.
	state.mu.Lock()
	s.mu.Unlock()

	defer state.mu.Unlock()

	// Add the attestation
	if err := state.AddAttestation(anf.Attestation); err != nil {
		s.logger.Error().Err(err).Msg("fail to add attestation")
		return
	}

	// Get the active validator count
	activeValCount := s.activeValidatorCount()
	hasSuperMajority := types.HasSuperMajority(state.AttestationCount(), activeValCount)

	// If we have a supermajority, send to thornode
	if hasSuperMajority {
		s.logger.Debug().Msgf("has supermajority: %d/%d", state.AttestationCount(), activeValCount)
		s.sendNetworkFeeAttestationsToThornode(ctx, *state.Item, state, true)
	} else {
		s.logger.Debug().Msgf("network fee attestation received - %s, height: %d, quorum: %d/%d",
			k.Chain, k.Height, state.AttestationCount(), activeValCount)
	}
}

// sendNetworkFeeAttestationsToThornode sends network fee attestations to thornode via gRPC
func (s *AttestationGossip) sendNetworkFeeAttestationsToThornode(ctx context.Context, networkFee common.NetworkFee, state *AttestationState[*common.NetworkFee], isQuorum bool) {
	unsent := state.UnsentAttestations()
	if len(unsent) == 0 {
		s.logger.Debug().Msg("no unsent network fee attestations")
		return
	}
	// Send via gRPC to thornode
	if _, err := s.grpcClient.SendQuorumNetworkFee(ctx, &common.QuorumNetworkFee{
		NetworkFee:   &networkFee,
		Attestations: unsent,
	}); err != nil {
		s.logger.Error().Err(err).Msg("fail to send quorum network fee")
		return
	}

	s.logger.Info().Msgf("sent quorum network fee to thornode - %s, height: %d, attestations: %s",
		networkFee.Chain, networkFee.Height, state.State())

	// Mark attestations as sent
	state.MarkAttestationsSent(isQuorum)
}

// handleSolvencyAttestation processes attestations for solvency proofs
func (s *AttestationGossip) handleSolvencyAttestation(ctx context.Context, ats common.AttestSolvency) {
	// Calculate the hash for the solvency to use as key
	k, err := ats.Solvency.Hash()
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to hash solvency")
		return
	}

	s.mu.Lock()
	state, ok := s.solvencies[k]
	if !ok {
		// Create a new attestation state
		state = s.solvenciesPool.NewAttestationState(ats.Solvency)
		s.solvencies[k] = state
	}

	// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
	// This order must be maintained everywhere to avoid deadlock.
	state.mu.Lock()
	s.mu.Unlock()

	defer state.mu.Unlock()

	// Add the attestation
	if err := state.AddAttestation(ats.Attestation); err != nil {
		s.logger.Error().Err(err).Msg("fail to add attestation")
		return
	}

	// Get the active validator count
	activeValCount := s.activeValidatorCount()
	hasSuperMajority := types.HasSuperMajority(state.AttestationCount(), activeValCount)

	// If we have a supermajority, send to thornode
	if hasSuperMajority {
		s.logger.Debug().Msgf("has supermajority: %d/%d", state.AttestationCount(), activeValCount)
		s.sendSolvencyAttestationsToThornode(ctx, *state.Item, state, true)
	} else {
		s.logger.Debug().Msgf("solvency attestation received - %s, height: %d, quorum: %d/%d",
			ats.Solvency.Chain, ats.Solvency.Height, state.AttestationCount(), activeValCount)
	}
}

// sendSolvencyAttestationsToThornode sends solvency attestations to thornode via gRPC
func (s *AttestationGossip) sendSolvencyAttestationsToThornode(ctx context.Context, solvency common.Solvency, state *AttestationState[*common.Solvency], isQuorum bool) {
	unsent := state.UnsentAttestations()
	if len(unsent) == 0 {
		s.logger.Debug().Msg("no unsent solvency attestations")
		return
	}
	// Send via gRPC to thornode
	if _, err := s.grpcClient.SendQuorumSolvency(ctx, &common.QuorumSolvency{
		Solvency:     &solvency,
		Attestations: unsent,
	}); err != nil {
		s.logger.Error().Err(err).Msg("fail to send quorum solvency")
		return
	}

	s.logger.Info().Msgf("sent quorum solvency to thornode - %s, height: %d, coins: %s, pubkey: %s, attestations: %s",
		solvency.Chain, solvency.Height, solvency.Coins.String(), solvency.PubKey.String(), state.State())

	// Mark attestations as sent
	state.MarkAttestationsSent(isQuorum)
}

// handleErrataAttestation processes attestations for errata transactions
func (s *AttestationGossip) handleErrataAttestation(ctx context.Context, aet common.AttestErrataTx) {
	// Use the errata tx as the map key
	k := *aet.ErrataTx

	s.mu.Lock()
	state, ok := s.errataTxs[k]
	if !ok {
		// Create a new attestation state
		state = s.errataTxsPool.NewAttestationState(aet.ErrataTx)
		s.errataTxs[k] = state
	}

	// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
	// This order must be maintained everywhere to avoid deadlock.
	state.mu.Lock()
	s.mu.Unlock()

	defer state.mu.Unlock()

	// Add the attestation
	if err := state.AddAttestation(aet.Attestation); err != nil {
		s.logger.Error().Err(err).Msg("fail to add attestation")
		return
	}

	// Get the active validator count
	activeValCount := s.activeValidatorCount()
	hasSuperMajority := types.HasSuperMajority(state.AttestationCount(), activeValCount)

	// If we have a supermajority, send to thornode
	if hasSuperMajority {
		s.logger.Debug().Msgf("has supermajority: %d/%d", state.AttestationCount(), activeValCount)
		s.sendErrataAttestationsToThornode(ctx, *state.Item, state, true)
	} else {
		s.logger.Debug().Msgf("errata attestation received - %s, id: %s, quorum: %d/%d",
			k.Chain, k.Id, state.AttestationCount(), activeValCount)
	}
}

// sendErrataAttestationsToThornode sends errata attestations to thornode via gRPC
func (s *AttestationGossip) sendErrataAttestationsToThornode(ctx context.Context, errata common.ErrataTx, state *AttestationState[*common.ErrataTx], isQuorum bool) {
	unsent := state.UnsentAttestations()
	if len(unsent) == 0 {
		s.logger.Debug().Msg("no unsent errata attestations")
		return
	}
	// Send via gRPC to thornode
	if _, err := s.grpcClient.SendQuorumErrataTx(ctx, &common.QuorumErrataTx{
		ErrataTx:     &errata,
		Attestations: unsent,
	}); err != nil {
		s.logger.Error().Err(err).Msg("fail to send quorum errata")
		return
	}

	s.logger.Info().Msgf("sent quorum errata to thornode - %s - ID: %s - attestations: %s", errata.Chain, errata.Id, state.State())

	// Mark attestations as sent
	state.MarkAttestationsSent(isQuorum)
}

// handlePriceFeedAttestation processes attestations for price feeds
func (s *AttestationGossip) handlePriceFeedAttestation(ctx context.Context, apf common.AttestPriceFeed) {
	// Use the pubkey as key
	k := hex.EncodeToString(apf.Attestation.PubKey)

	// Get the marshaled data for signature verification
	signBz, err := apf.PriceFeed.GetSignablePayload()
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to get signable payload")
		return
	}

	// Verify the signature
	att := apf.Attestation
	err = verifySignature(signBz, att.Signature, att.PubKey)
	if err != nil {
		s.logger.Error().Err(err).Msgf("signature verification failed for %x", att.PubKey)
		return
	}

	// We don't collect multiple attestations for price feeds, rather want
	// to store the most recent price we received for every node
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.priceFeeds[k]
	if !ok || state.Item.Time < apf.PriceFeed.Time {
		// Set new attestation state
		state = s.priceFeedsPool.NewAttestationState(apf.PriceFeed)
		state.attestations = []attestationSentState{
			{attestation: apf.Attestation, sent: false},
		}
		s.priceFeeds[k] = state

		// we received an updated price feed, start delay to maybe collect
		// more updated values before sending them to thornode
		if s.priceFeedsDelay.IsRunning() {
			return
		}

		s.priceFeedsDelay.Start()
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(priceFeedsUpdateDelay):
				s.sendPriceFeedAttestationsToThornode(ctx)
				s.priceFeedsDelay.Done()
			}
		}()
	}
}

// sendPriceFeedAttestationsToThornode sends price feed attestations
// to thornode via gRPC
//
// To avoid calling the endpoint hundred times per update, all
// (quorum) price feeds are batched into a single request
func (s *AttestationGossip) sendPriceFeedAttestationsToThornode(
	ctx context.Context,
) {
	qpfb := common.QuorumPriceFeedBatch{
		QuorumPriceFeeds: []*common.QuorumPriceFeed{},
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, state := range s.priceFeeds {
		state.mu.Lock()
		defer state.mu.Unlock()

		qpfb.QuorumPriceFeeds = append(qpfb.QuorumPriceFeeds, &common.QuorumPriceFeed{
			PriceFeed:    state.Item,
			Attestations: []*common.Attestation{state.attestations[0].attestation},
		})
	}

	if len(qpfb.QuorumPriceFeeds) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	_, err := s.grpcClient.SendQuorumPriceFeedBatch(ctx, &qpfb)
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to send price feed")
		return
	}

	// delete price feeds
	for k := range s.priceFeeds {
		delete(s.priceFeeds, k)
	}
}
