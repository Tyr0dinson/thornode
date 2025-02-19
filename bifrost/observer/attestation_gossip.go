package observer

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	// validators can get credit for observing a tx for up to this amount of time after it is committed, after which it count against a slash penalty.
	defaultLateObserveTimeout = 5 * time.Minute

	// minTimeBetweenAttestations is the minimum time between sending batches of attestations for a quorum tx to thornode.
	defaultMinTimeBetweenAttestations = 30 * time.Second

	// how often to prune old observed txs and check if late attestations should be sent.
	// should be less than lateObserveTimeout and minTimeBetweenAttestations by at least a factor of 2.
	defaultObserveReconcileInterval = 15 * time.Second

	// defaultAskPeers is the number of random peers to ask for their attestation state on startup.
	defaultAskPeers = 3

	// defaultAskPeersDelay is the delay before asking peers for their attestation state on startup.
	defaultAskPeersDelay = 5 * time.Second
)

var (
	observeTxProtocol      protocol.ID = "/p2p/observed-tx"
	prefixSendState                    = []byte{0x01}
	prefixAttestation                  = []byte{0x02}
	prefixAttestationState             = []byte{0x03}
)

type attestationState struct {
	attestation *common.Attestation

	sent bool
}

type observedTxState struct {
	tx      *common.ObservedTx
	inbound bool

	attestations []attestationState

	// firstAttestationObserved is set when the first attestation is observed for the tx.
	firstAttestationObserved time.Time

	// initialAttestationsSent is set when the first batch of attestations were sent to thornode, either by reaching quorum or by timeout.
	initialAttestationsSent time.Time

	// quorumAttestationsSent is set when we first sent enough attestations to reach quorum.
	quorumAttestationsSent time.Time

	// lastAttestationsSent is set when the last batch of tx attestations were sent to thornode.
	lastAttestationsSent time.Time
	mu                   sync.Mutex
}

func (ots *observedTxState) unsentAttestations() []*common.Attestation {
	unsent := make([]*common.Attestation, 0)
	for _, item := range ots.attestations {
		if !item.sent {
			unsent = append(unsent, item.attestation)
		}
	}
	return unsent
}

func (ots *observedTxState) attestationsCopy() []*common.Attestation {
	atts := make([]*common.Attestation, len(ots.attestations))
	for i, item := range ots.attestations {
		atts[i] = &common.Attestation{
			PubKey:    append([]byte(nil), item.attestation.PubKey...),
			Signature: append([]byte(nil), item.attestation.Signature...),
		}
	}
	return atts
}

func (ots *observedTxState) unsentCount() int {
	count := 0
	for _, item := range ots.attestations {
		if !item.sent {
			count++
		}
	}
	return count
}

func (ots *observedTxState) shouldSendLate(minTimeBetweenAttestations time.Duration) bool {
	if ots.unsentCount() == 0 {
		// nothing to send
		return false
	}

	if !ots.lastAttestationsSent.IsZero() && time.Since(ots.lastAttestationsSent) > minTimeBetweenAttestations {
		// we have sent attestations before, and it's been long enough to send again
		return true
	}

	if ots.initialAttestationsSent.IsZero() && time.Since(ots.firstAttestationObserved) > minTimeBetweenAttestations {
		// we haven't sent any attestations yet, and it's been long enough to send the first batch
		return true
	}

	return false
}

func (ots *observedTxState) expiredAfterQuorum(lateObserveTimeout time.Duration) bool {
	if ots.quorumAttestationsSent.IsZero() {
		// we haven't reached quorum yet, so we can't expire
		return false
	}

	if time.Since(ots.quorumAttestationsSent) > lateObserveTimeout {
		// we have reached quorum, but it's been too long. Stop tracking this tx.
		return true
	}

	return false
}

// addAttestation will add a validators signature to the observed tx state. If a signature is already present for the validator (by pubkey), it will be ignored.
func (ots *observedTxState) addAttestation(txIn *common.AttestTx) error {
	for _, item := range ots.attestations {
		if bytes.Equal(item.attestation.Signature, txIn.Attestation.Signature) {
			// already have the signature, ignore
			return nil
		}
		if bytes.Equal(item.attestation.PubKey, txIn.Attestation.PubKey) {
			// Unexpected: we should never have a different signature from the same pubkey for the same tx.
			return fmt.Errorf("signature already present for %s", txIn.Attestation.PubKey)
		}
	}

	signBz, err := txIn.Tx.Tx.Marshal()
	if err != nil {
		return fmt.Errorf("fail to marshal tx key: %w", err)
	}

	if err := verifySignature(signBz, txIn.Attestation.Signature, txIn.Attestation.PubKey); err != nil {
		return fmt.Errorf("signature verification failed for %s - %w", txIn.Attestation.PubKey, err)
	}

	ots.attestations = append(ots.attestations, attestationState{attestation: txIn.Attestation, sent: false})

	return nil
}

func verifySignature(signBz []byte, signature []byte, attester []byte) error {
	pub := secp256k1.PubKey{Key: attester}
	if !pub.VerifySignature(signBz, signature) {
		return fmt.Errorf("signature verification failed")
	}

	return nil
}

type TxKey struct {
	Chain     common.Chain
	ID        common.TxID
	Finalized bool
}

// AttestationGossip handles observed tx attestations to/from other nodes
type AttestationGossip struct {
	logger      zerolog.Logger
	host        host.Host
	keys        *thorclient.Keys
	grpcClient  ebifrost.LocalhostBifrostClient
	eventClient *EventClient

	pubKey []byte // our public key, cached for performance

	config config.BifrostAttestationGossipConfig

	// map of tx id (plus chain and finalized) to the observed tx state
	observedTxs map[TxKey]*observedTxState
	mu          sync.Mutex

	activeValCount int
	avMu           sync.Mutex

	observerHandleObservedTxCommitted func(tx common.ObservedTx)
}

func (s *AttestationGossip) SetActiveValidatorCount(count int) {
	s.avMu.Lock()
	defer s.avMu.Unlock()
	s.activeValCount = count
}

func (s *AttestationGossip) GetActiveValidatorCount() int {
	s.avMu.Lock()
	defer s.avMu.Unlock()
	return s.activeValCount
}

// normalizeConfig ensures that the config has values for all fields.
func normalizeConfig(config *config.BifrostAttestationGossipConfig) {
	if config.ObserveReconcileInterval == 0 {
		config.ObserveReconcileInterval = defaultObserveReconcileInterval
	}
	if config.LateObserveTimeout == 0 {
		config.LateObserveTimeout = defaultLateObserveTimeout
	}
	if config.MinTimeBetweenAttestations == 0 {
		config.MinTimeBetweenAttestations = defaultMinTimeBetweenAttestations
	}
	if config.AskPeers == 0 {
		config.AskPeers = defaultAskPeers
	}
	if config.AskPeersDelay == 0 {
		config.AskPeersDelay = defaultAskPeersDelay
	}
}

// NewAttestationGossip create a new instance of AttestationGossip
func NewAttestationGossip(
	host host.Host,
	keys *thorclient.Keys,
	thornodeBifrostGRPCAddress string,
	config config.BifrostAttestationGossipConfig,
) (*AttestationGossip, error) {
	cc, err := grpc.NewClient(thornodeBifrostGRPCAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}

	normalizeConfig(&config)

	grpcClient := ebifrost.NewLocalhostBifrostClient(cc)
	eventClient := NewEventClient(grpcClient)

	pk, err := keys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}

	s := &AttestationGossip{
		logger:      log.With().Str("module", "attestation_gossip").Logger(),
		host:        host,
		keys:        keys,
		pubKey:      pk.PubKey().Bytes(),
		observedTxs: make(map[TxKey]*observedTxState),
		grpcClient:  grpcClient,
		config:      config,
		eventClient: eventClient,
	}
	eventClient.RegisterHandler(ebifrost.EventQuorumTxCommited, s.handleQuorumTxCommited)
	host.SetStreamHandler(observeTxProtocol, s.handleStream)
	return s, nil
}

func (s *AttestationGossip) SetObserverHandleObservedTxCommitted(o *Observer) {
	s.observerHandleObservedTxCommitted = o.handleObservedTxCommitted
}

func (s *AttestationGossip) handleQuorumTxCommited(en *ebifrost.EventNotification) {
	s.logger.Debug().Msg("handling quorum tx committed event")

	if s.observerHandleObservedTxCommitted == nil {
		// nothing to do
		return
	}

	var tx common.QuorumTx
	if err := tx.Unmarshal(en.Payload); err != nil {
		s.logger.Error().Err(err).Msg("fail to unmarshal quorum tx")
		return
	}

	// if our attestation is in the quorum tx, we can remove it from our observer deck.
	for _, att := range tx.Attestations {
		if bytes.Equal(att.PubKey, s.pubKey) {
			// we have attested to this tx, and it has been committed to the chain.
			s.logger.Debug().Msg("our attestation is in the quorum tx, passing to observer to remove from ondeck")
			s.observerHandleObservedTxCommitted(tx.Tx)
			return
		}
	}
}

// Start will run a loop to prune old observed txs
func (s *AttestationGossip) Start(ctx context.Context) {
	ticker := time.NewTicker(s.config.ObserveReconcileInterval)
	defer ticker.Stop()

	startupDelay := s.config.AskPeersDelay
	delayTimer := time.NewTimer(startupDelay)
	defer delayTimer.Stop()

	for {
		select {
		case <-ticker.C:
			// prune old observed txs
			s.mu.Lock()
			for hash, ots := range s.observedTxs {
				ots.mu.Lock()
				if ots.expiredAfterQuorum(s.config.LateObserveTimeout) {
					delete(s.observedTxs, hash)
				} else if ots.shouldSendLate(s.config.MinTimeBetweenAttestations) {
					s.logger.Info().Msg("sending late")
					// we have attestations after timeout, send to thornode
					s.sendAttestationsToThornode(ctx, *ots.tx, ots, false)
				}
				ots.mu.Unlock()
			}
			s.mu.Unlock()
		case <-delayTimer.C:
			s.eventClient.Start()

			s.logger.Debug().Msg("asking for attestation state")
			s.AskForAttestationState(ctx)
		case <-ctx.Done():
			s.eventClient.Stop()
			return
		}
	}
}

// RandomElements returns n unique random elements from the provided slice.
// If n is greater than the length of the slice, it returns all elements.
func RandomElements[T any](slice []T, n int) []T {
	if len(slice) == 0 {
		return []T{}
	}

	// If n is greater than slice length, return all elements
	if n >= len(slice) {
		result := make([]T, len(slice))
		copy(result, slice)
		return result
	}

	// Create a copy to avoid modifying the original slice
	temp := make([]T, len(slice))
	copy(temp, slice)

	// Create a random number generator with the current time as seed
	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), uint64(time.Now().UnixNano()))) //nolint:gosec

	// Create result slice
	result := make([]T, n)

	// Pick n random elements
	for i := 0; i < n; i++ {
		// Generate random index
		idx := rng.IntN(len(temp))

		// Add element to result
		result[i] = temp[idx]

		// Remove element from temp slice to avoid duplicates
		// (by replacing it with the last element and shrinking the slice)
		temp[idx] = temp[len(temp)-1]
		temp = temp[:len(temp)-1]
	}

	return result
}

// AskForAttestationState will ask other validators for their attestation state
func (s *AttestationGossip) AskForAttestationState(ctx context.Context) {
	allPeers := s.host.Peerstore().Peers()
	if len(allPeers) == 0 {
		s.logger.Debug().Msg("no peers to ask for attestation state")
		return
	}
	peersWithoutMe := make([]peer.ID, 0, len(allPeers)-1)
	for _, peer := range allPeers {
		if peer != s.host.ID() {
			peersWithoutMe = append(peersWithoutMe, peer)
		}
	}
	if len(peersWithoutMe) == 0 {
		s.logger.Debug().Msg("no peers without me to ask for attestation state")
		return
	}
	// ask 3 random peers for their attestation state
	peers := RandomElements(peersWithoutMe, 3)
	var wg sync.WaitGroup
	wg.Add(len(peers))
	for _, peer := range peers {
		if peer == s.host.ID() {
			// don't ask ourselves
			continue
		}
		go s.askPeerForState(ctx, peer, &wg)
	}
	wg.Wait()
}

// Attest will attest to an observed tx, sending the attestation to all connected peers.
// If the attestation pushes the observed tx to quorum, it will be sent to thornode via grpc for tx proposal injection.
func (s *AttestationGossip) Attest(ctx context.Context, tx *common.ObservedTx, inbound bool) error {
	pk, err := s.keys.GetPrivateKey()
	if err != nil {
		return fmt.Errorf("fail to get private key: %w", err)
	}

	signBz, err := tx.Tx.Marshal()
	if err != nil {
		return fmt.Errorf("fail to marshal tx sign payload: %w", err)
	}

	signature, err := pk.Sign(signBz)
	if err != nil {
		return fmt.Errorf("fail to sign tx sign payload: %w", err)
	}

	msg := common.AttestTx{
		Tx:      *tx,
		Inbound: inbound,
		Attestation: &common.Attestation{
			PubKey:    pk.PubKey().Bytes(),
			Signature: signature,
		},
	}

	// account for the attestation locally
	s.logger.Debug().Msg("handling attestation locally")
	s.handleAttestation(ctx, msg)

	payload, err := msg.Marshal()
	if err != nil {
		return fmt.Errorf("fail to marshal tx in: %w", err)
	}

	// send the message to all connected peers
	peers := s.host.Peerstore().Peers()

	var wg sync.WaitGroup
	wg.Add(len(peers) - 1)
	for _, peer := range peers {
		if peer == s.host.ID() {
			// don't send the message to ourselves
			continue
		}
		go s.sendAttestationToPeer(ctx, peer, payload, &wg)
	}
	wg.Wait()

	return nil
}

func (s *AttestationGossip) sendAttestationState(stream network.Stream) {
	s.mu.Lock()
	defer s.mu.Unlock()

	quorumTxs := common.QuorumTxs{
		Txs: make([]*common.QuorumTx, 0, len(s.observedTxs)),
	}

	// send the attestation state
	for _, ots := range s.observedTxs {
		ots.mu.Lock()

		quorumTxs.Txs = append(quorumTxs.Txs, &common.QuorumTx{
			Tx:           *ots.tx,
			Inbound:      ots.inbound,
			Attestations: ots.attestationsCopy(),
		})

		ots.mu.Unlock()
	}

	// TODO: have a back and forth for large payloads rather than all at once.
	payload, err := quorumTxs.Marshal()
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to marshal attestation state")
		return
	}

	if err := p2p.WriteStreamWithBuffer(append(prefixAttestationState, payload...), stream); err != nil {
		s.logger.Error().Err(err).Msg("fail to write attestation state to stream")
		return
	}

	// wait for the reply
	reply, err := p2p.ReadStreamWithBuffer(stream)
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to read reply from stream")
		return
	}

	if string(reply) != "done" {
		s.logger.Error().Msg("unexpected reply from peer")
	}
}

func (s *AttestationGossip) askPeerForState(ctx context.Context, peer peer.ID, wg *sync.WaitGroup) {
	defer wg.Done()
	stream, err := s.host.NewStream(ctx, peer, observeTxProtocol)
	if err != nil {
		s.logger.Error().Err(err).Msgf("fail to create stream to peer: %s", peer)
		return
	}

	defer func() {
		if err := stream.Close(); err != nil {
			s.logger.Error().Err(err).Msgf("fail to close stream to peer: %s", peer)
		}
	}()

	if err := p2p.WriteStreamWithBuffer(prefixSendState, stream); err != nil {
		s.logger.Error().Err(err).Msgf("fail to write payload to peer: %s", peer)
		return
	}

	// wait for the reply
	payload, err := p2p.ReadStreamWithBuffer(stream)
	if err != nil {
		s.logger.Error().Err(err).Msgf("fail to read reply from peer: %s", peer)
		return
	}

	// we tell the sender we have received the message
	err = p2p.WriteStreamWithBuffer([]byte("done"), stream)
	if err != nil {
		s.logger.Error().Err(err).Msgf("fail to write the reply to peer: %s", peer)
	}

	if !bytes.HasPrefix(payload, prefixAttestationState) {
		s.logger.Error().Msgf("unexpected attestation state reply from peer: %s", peer)
		return
	}
	var msg common.QuorumTxs
	// TODO: have a back and forth for large payloads rather than all at once.
	if err := msg.Unmarshal(payload[1:]); err != nil {
		s.logger.Err(err).Msg("fail to unmarshal attestation state")
		return
	}

	s.logger.Debug().Msg("handling incoming attestation state")

	for _, qt := range msg.Txs {
		for _, att := range qt.Attestations {
			s.logger.Debug().Msg("handling attestation from peer state dump")
			s.handleAttestation(context.TODO(), common.AttestTx{
				Tx:          qt.Tx,
				Inbound:     qt.Inbound,
				Attestation: att,
			})
		}
	}
}

func (s *AttestationGossip) sendAttestationToPeer(ctx context.Context, peer peer.ID, payload []byte, wg *sync.WaitGroup) {
	defer wg.Done()
	stream, err := s.host.NewStream(ctx, peer, observeTxProtocol)
	if err != nil {
		s.logger.Error().Err(err).Msgf("fail to create stream to peer: %s", peer)
		return
	}

	defer func() {
		if err := stream.Close(); err != nil {
			s.logger.Error().Err(err).Msgf("fail to close stream to peer: %s", peer)
		}
	}()

	if err := p2p.WriteStreamWithBuffer(append(prefixAttestation, payload...), stream); err != nil {
		s.logger.Error().Err(err).Msgf("fail to write payload to peer: %s", peer)
		return
	}

	// wait for the reply
	reply, err := p2p.ReadStreamWithBuffer(stream)
	if err != nil {
		s.logger.Error().Err(err).Msgf("fail to read reply from peer: %s", peer)
		return
	}

	if string(reply) != "done" {
		s.logger.Error().Msgf("unexpected reply from peer: %s", peer)
		return
	}

	s.logger.Debug().Msgf("attestation sent to peer: %s", peer)
}

// handleStream handle observed tx notify stream
func (s *AttestationGossip) handleStream(stream network.Stream) {
	remotePeer := stream.Conn().RemotePeer()
	logger := s.logger.With().Str("remote peer", remotePeer.String()).Logger()
	logger.Debug().Msg("reading attestation gossip message")
	payload, err := p2p.ReadStreamWithBuffer(stream)
	if err != nil {
		logger.Err(err).Msgf("fail to read payload from stream")
		return
	}

	if bytes.HasPrefix(payload, prefixSendState) {
		if len(payload) != 1 {
			logger.Error().Msg("unexpected payload length")
			return
		}
		logger.Debug().Msg("handling send state request")
		s.sendAttestationState(stream)
		return
	}

	// we tell the sender we have received the message
	err = p2p.WriteStreamWithBuffer([]byte("done"), stream)
	if err != nil {
		logger.Error().Err(err).Msgf("fail to write the reply to peer: %s", remotePeer)
	}

	if len(payload) == 0 {
		logger.Error().Msg("empty payload")
		return
	}

	if bytes.HasPrefix(payload, prefixAttestation) {
		var msg common.AttestTx
		if err := msg.Unmarshal(payload[1:]); err != nil {
			logger.Err(err).Msg("fail to unmarshal attestation")
			return
		}
		logger.Debug().Msg("handling incoming attestation from peer")
		s.handleAttestation(context.TODO(), msg)
		return
	}
}

// handleAttestation accounts for an attestation to an observed tx.
// If the attestation pushes the observed tx to quorum, it will be sent to thornode via grpc for tx proposal injection.
func (s *AttestationGossip) handleAttestation(ctx context.Context, tx common.AttestTx) {
	k := TxKey{
		Chain:     tx.Tx.Tx.Chain,
		ID:        tx.Tx.Tx.ID,
		Finalized: tx.Tx.IsFinal(),
	}

	s.mu.Lock()
	ots, ok := s.observedTxs[k]
	if !ok {
		ots = &observedTxState{
			tx:                       &tx.Tx,
			inbound:                  tx.Inbound,
			attestations:             make([]attestationState, 0),
			firstAttestationObserved: time.Now(),
		}
		s.observedTxs[k] = ots
	}
	s.mu.Unlock()

	ots.mu.Lock()
	defer ots.mu.Unlock()

	if err := ots.addAttestation(&tx); err != nil {
		s.logger.Error().Err(err).Msg("fail to add signature")
		return
	}

	hasSuperMajority := types.HasSuperMajority(len(ots.attestations), s.GetActiveValidatorCount())

	// check if we have quorum. If so, send to thornode via grpc for tx proposal injection.
	if ots.quorumAttestationsSent.IsZero() && hasSuperMajority {
		s.logger.Debug().Msgf("has supermajority: %d/%d", len(ots.attestations), s.GetActiveValidatorCount())
		s.sendAttestationsToThornode(ctx, tx.Tx, ots, true)
	} else {
		s.logger.Debug().Msgf("attestation received - %s - ID: %s - Final: %t - Quorum:  %d/%d", tx.Tx.Tx.Chain, tx.Tx.Tx.ID, tx.Tx.IsFinal(), len(ots.attestations), s.GetActiveValidatorCount())
	}
}

func (s *AttestationGossip) sendAttestationsToThornode(ctx context.Context, tx common.ObservedTx, ots *observedTxState, quorum bool) {
	// send via grpc to our thornode to cache for tx proposal injection.
	if _, err := s.grpcClient.SendQuorumTx(ctx, &common.QuorumTx{
		Tx:           tx,
		Attestations: ots.unsentAttestations(),
		Inbound:      ots.inbound,
	}); err != nil {
		s.logger.Error().Err(err).Msg("fail to send quorum tx")
		return
	}

	s.logger.Debug().Msgf("sent quorum tx to thornode - %s - ID: %s - Final: %t", tx.Tx.Chain, tx.Tx.ID, tx.IsFinal())

	// thornode will handle the attestations from here, so we can clear them.
	// We will still keep the observed tx state until pruned in case of late attestations.
	timestamp := time.Now()

	ots.lastAttestationsSent = timestamp
	if ots.initialAttestationsSent.IsZero() {
		ots.initialAttestationsSent = timestamp
	}
	if quorum {
		ots.quorumAttestationsSent = timestamp
	}

	// Mark attestations as sent. Any late attestations will be processed as needed.
	for i := range ots.attestations {
		ots.attestations[i].sent = true
	}
}
