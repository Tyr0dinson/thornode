package observer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
)

const (
	// validators can get credit for observing a tx for up to this amount of time after it is committed, after which it count against a slash penalty.
	defaultLateObserveTimeout = 2 * time.Minute

	// Prune observed txs after this amount of time, even if they are not yet committed.
	// Gives some time for longer chain halts.
	// If chain halts for longer than this, validators will need to restart their bifrosts to re-share their attestations.
	defaultNonQuorumTimeout = 10 * time.Hour

	// minTimeBetweenAttestations is the minimum time between sending batches of attestations for a quorum tx to thornode.
	defaultMinTimeBetweenAttestations = 30 * time.Second

	// how often to prune old observed txs and check if late attestations should be sent.
	// should be less than lateObserveTimeout and minTimeBetweenAttestations by at least a factor of 2.
	defaultObserveReconcileInterval = 15 * time.Second

	// defaultAskPeers is the number of random peers to ask for their attestation state on startup.
	defaultAskPeers = 10

	// defaultAskPeersDelay is the delay before asking peers for their attestation state on startup.
	defaultAskPeersDelay = 5 * time.Second

	cachedKeysignPartyTTL = 1 * time.Minute

	defaultPeerTimeout = 20 * time.Second

	defaultPeerConcurrentSends    = 4
	defaultPeerConcurrentReceives = 5
	defaultMaxBatchSize           = 100
	defaultBatchInterval          = 2 * time.Second

	streamAckBegin  = "ack_begin"
	streamAckHeader = "ack_header"
	streamAckData   = "ack_data"
)

var (
	attestationStateProtocol   protocol.ID = "/p2p/attestation-state/v2"
	batchedAttestationProtocol protocol.ID = "/p2p/batched-attestations"

	// AttestationState protocol prefixes
	prefixSendState = []byte{0x01} // request

	prefixBatchBegin  = []byte{0x02} // start of a batch
	prefixBatchHeader = []byte{0x03} // header of a batch
	prefixBatchData   = []byte{0x04} // data of a batch
	prefixBatchEnd    = []byte{0x05} // end of a batch

	// Maximum number of QuorumTxs to send in a single batch when sending attestation state.
	maxQuorumTxsPerBatch = 100
)

// txKey contains the properties that are required to uniquely identify an observed tx
type txKey struct {
	Chain                  common.Chain
	ID                     common.TxID
	UniqueHash             string
	PayloadHash            string
	AllowFutureObservation bool
	Finalized              bool
	Inbound                bool
}

type pendingInboundXMRKeyImageAttestation struct {
	key                    txKey
	obsTx                  common.ObservedTx
	allowFutureObservation bool
	inbound                bool
}

type KeysInterface interface {
	GetPrivateKey() (*secp256k1.PrivKey, error)
}

type EventClientInterface interface {
	Start()
	Stop()
	RegisterHandler(eventType string, handler func(*ebifrost.EventNotification))
}

// AttestationGossip handles observed tx attestations to/from other nodes
type AttestationGossip struct {
	logger zerolog.Logger
	host   host.Host

	grpcClient  ebifrost.LocalhostBifrostClient
	eventClient EventClientInterface
	bridge      thorclient.ThorchainBridge

	privKey cryptotypes.PrivKey // our private key, cached for performance
	pubKey  []byte              // our public key, cached for performance

	config config.BifrostAttestationGossipConfig

	// Generic maps for different attestation types
	observedTxs map[txKey]*AttestationState[*common.ObservedTx]
	networkFees map[common.NetworkFee]*AttestationState[*common.NetworkFee]
	solvencies  map[common.TxID]*AttestationState[*common.Solvency]
	errataTxs   map[common.ErrataTx]*AttestationState[*common.ErrataTx]
	priceFeeds  map[string]*AttestationState[*common.PriceFeed]
	mu          sync.Mutex

	observedTxsPool *AttestationStatePool[*common.ObservedTx]
	networkFeesPool *AttestationStatePool[*common.NetworkFee]
	solvenciesPool  *AttestationStatePool[*common.Solvency]
	errataTxsPool   *AttestationStatePool[*common.ErrataTx]
	priceFeedsPool  *AttestationStatePool[*common.PriceFeed]

	priceFeedsDelay *Delay

	activeVals map[peer.ID]bool // active val peer IDs
	avMu       sync.Mutex

	observerHandleObservedTxCommitted   func(tx common.ObservedTx)
	observerValidateXMRTx               func(tx common.ObservedTx) error
	observerValidateInboundXMRKeyImages func(tx common.ObservedTx) error
	// validateXMRTxSem bounds outbound tx-key validation calls into the XMR
	// sidecar, which may need daemon RPC access before returning.
	validateXMRTxSem chan struct{}
	// inbound XMR key-image validation/co-attest work must run serially so the
	// same node does not compete with itself across ceremonies. We keep a FIFO
	// queue and a de-duplication set keyed by the observed tx payload.
	inboundXMRKeyImageMu            sync.Mutex
	inboundXMRKeyImageInFlight      map[txKey]struct{}
	inboundXMRKeyImageQueue         []pendingInboundXMRKeyImageAttestation
	inboundXMRKeyImageWorkerRunning bool

	cachedKeySignParties map[common.PubKey]cachedKeySignParty
	cachedKeySignMu      sync.Mutex

	batcher *AttestationBatcher

	// peerManager is used to limit the number of concurrent receives from a peer
	peerMgr *peerManager

	stateInitPeers map[peer.ID]bool // peers that we have asked for their attestation state
	stateInitMu    sync.Mutex
}

type cachedKeySignParty struct {
	keySignParty common.PubKeys
	lastUpdated  time.Time
}

func observedTxKey(obsTx common.ObservedTx, allowFutureObservation, inbound bool) (txKey, error) {
	k := txKey{
		Chain:                  obsTx.Tx.Chain,
		ID:                     obsTx.Tx.ID,
		UniqueHash:             obsTx.Tx.Hash(obsTx.BlockHeight),
		AllowFutureObservation: allowFutureObservation,
		Finalized:              obsTx.IsFinal(),
		Inbound:                inbound,
	}

	if len(obsTx.TxKeys) == 0 && len(obsTx.XmrKeyImages) == 0 {
		return k, nil
	}

	signBz, err := obsTx.GetSignablePayload()
	if err != nil {
		return txKey{}, fmt.Errorf("fail to hash observed tx payload: %w", err)
	}
	sum := sha256.Sum256(signBz)
	k.PayloadHash = hex.EncodeToString(sum[:])
	return k, nil
}

// NewAttestationGossip create a new instance of AttestationGossip
func NewAttestationGossip(
	host host.Host,
	keys *thorclient.Keys,
	thornodeBifrostGRPCAddress string,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
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

	batcher := NewAttestationBatcher(
		host,
		log.With().Str("module", "attestation_batcher").Logger(),
		m,
		config.BatchInterval,
		config.MaxBatchSize,
		config.PeerTimeout,
		config.PeerConcurrentSends,
	)

	logger := log.With().Str("module", "attestation_gossip").Logger()

	s := &AttestationGossip{
		logger:      logger,
		host:        host,
		privKey:     pk,
		pubKey:      pk.PubKey().Bytes(),
		grpcClient:  grpcClient,
		config:      config,
		bridge:      bridge,
		eventClient: eventClient,

		// Initialize generic maps
		observedTxs: make(map[txKey]*AttestationState[*common.ObservedTx]),
		networkFees: make(map[common.NetworkFee]*AttestationState[*common.NetworkFee]),
		solvencies:  make(map[common.TxID]*AttestationState[*common.Solvency]),
		errataTxs:   make(map[common.ErrataTx]*AttestationState[*common.ErrataTx]),
		priceFeeds:  make(map[string]*AttestationState[*common.PriceFeed]),

		peerMgr: newPeerManager(logger, config.PeerConcurrentReceives),

		observedTxsPool: NewAttestationStatePool[*common.ObservedTx](),
		networkFeesPool: NewAttestationStatePool[*common.NetworkFee](),
		solvenciesPool:  NewAttestationStatePool[*common.Solvency](),
		errataTxsPool:   NewAttestationStatePool[*common.ErrataTx](),
		priceFeedsPool:  NewAttestationStatePool[*common.PriceFeed](),

		priceFeedsDelay: NewDelay(),

		cachedKeySignParties: make(map[common.PubKey]cachedKeySignParty),

		batcher: batcher,

		validateXMRTxSem:           make(chan struct{}, 8),
		inboundXMRKeyImageInFlight: make(map[txKey]struct{}),
	}
	batcher.setActiveValGetter(s.getActiveValidators)
	// Register event handlers
	eventClient.RegisterHandler(ebifrost.EventQuorumTxCommitted, s.handleQuorumTxCommitted)
	eventClient.RegisterHandler(ebifrost.EventQuorumNetworkFeeCommitted, s.handleQuorumNetworkFeeCommitted)
	eventClient.RegisterHandler(ebifrost.EventQuorumSolvencyCommitted, s.handleQuorumSolvencyCommitted)
	eventClient.RegisterHandler(ebifrost.EventQuorumErrataTxCommitted, s.handleQuorumErrataTxCommitted)

	// Register stream handlers
	host.SetStreamHandler(attestationStateProtocol, s.handleStreamAttestationState)
	host.SetStreamHandler(batchedAttestationProtocol, s.handleStreamBatchedAttestations)

	return s, nil
}

// normalizeConfig ensures that the config has values for all fields.
func normalizeConfig(config *config.BifrostAttestationGossipConfig) {
	if config.ObserveReconcileInterval == 0 {
		config.ObserveReconcileInterval = defaultObserveReconcileInterval
	}
	if config.LateObserveTimeout == 0 {
		config.LateObserveTimeout = defaultLateObserveTimeout
	}
	if config.NonQuorumTimeout == 0 {
		config.NonQuorumTimeout = defaultNonQuorumTimeout
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
	if config.PeerTimeout == 0 {
		config.PeerTimeout = defaultPeerTimeout
	}
	if config.MaxBatchSize == 0 {
		config.MaxBatchSize = defaultMaxBatchSize
	}
	if config.PeerConcurrentSends == 0 {
		config.PeerConcurrentSends = defaultPeerConcurrentSends
	}
	if config.PeerConcurrentReceives == 0 {
		config.PeerConcurrentReceives = defaultPeerConcurrentReceives
	}
	if config.PeerConcurrentReceives < config.PeerConcurrentSends {
		// ensure that the number of concurrent receives is at least as large as the number of concurrent sends
		config.PeerConcurrentReceives = config.PeerConcurrentSends
	}
	if config.BatchInterval == 0 {
		config.BatchInterval = defaultBatchInterval
	}
}

// Set the active validators list
func (s *AttestationGossip) setActiveValidators(activeVals common.PubKeys) {
	s.avMu.Lock()
	defer s.avMu.Unlock()
	activePeers := make(map[peer.ID]bool, len(activeVals))
	for _, pub := range activeVals {
		pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeAccPub, pub.String())
		if err != nil {
			s.logger.Error().Err(err).Msg("fail to convert bech32 pubkey to raw secp256k1 pubkey")
			continue
		}
		peerID, err := conversion.GetPeerIDFromSecp256PubKey(pk.Bytes())
		if err != nil {
			s.logger.Error().Err(err).Msg("fail to convert secp256k1 pubkey to peer ID")
			continue
		}
		activePeers[peerID] = true
	}

	s.activeVals = activePeers
}

// Get the number of active validators
func (s *AttestationGossip) activeValidatorCount() int {
	s.avMu.Lock()
	defer s.avMu.Unlock()
	return len(s.activeVals)
}

// Check if a public key belongs to an active validator
func (s *AttestationGossip) isActiveValidator(p peer.ID) bool {
	s.avMu.Lock()
	defer s.avMu.Unlock()
	_, ok := s.activeVals[p]
	return ok
}

func (s *AttestationGossip) getActiveValidators() map[peer.ID]bool {
	s.avMu.Lock()
	defer s.avMu.Unlock()
	return s.activeVals
}

// Get the keysign party for a specific public key
func (s *AttestationGossip) getKeysignParty(pubKey common.PubKey) (common.PubKeys, error) {
	s.cachedKeySignMu.Lock()
	defer s.cachedKeySignMu.Unlock()

	if cached, ok := s.cachedKeySignParties[pubKey]; ok {
		return cached.keySignParty, nil
	}

	keySignParty, err := s.bridge.GetKeysignParty(pubKey)
	if err != nil {
		return nil, fmt.Errorf("fail to get key sign party: %w", err)
	}

	s.cachedKeySignParties[pubKey] = cachedKeySignParty{
		keySignParty: keySignParty,
		lastUpdated:  time.Now(),
	}

	return keySignParty, nil
}

func (s *AttestationGossip) SetObserverHandleObservedTxCommitted(o *Observer) {
	s.observerHandleObservedTxCommitted = o.handleObservedTxCommitted
}

func (s *AttestationGossip) SetObserverValidateXMRTx(fn func(tx common.ObservedTx) error) {
	s.observerValidateXMRTx = fn
}

func (s *AttestationGossip) SetObserverValidateInboundXMRKeyImages(o *Observer) {
	if o == nil {
		s.observerValidateInboundXMRKeyImages = nil
		return
	}
	s.observerValidateInboundXMRKeyImages = o.ValidateInboundXMRKeyImages
}

func (s *AttestationGossip) retryPendingInboundXMRKeyImageAttestations(ctx context.Context) {
	if s.observerValidateInboundXMRKeyImages == nil && s.observerValidateXMRTx == nil {
		return
	}

	candidates := make([]pendingInboundXMRKeyImageAttestation, 0)

	s.mu.Lock()
	for k, state := range s.observedTxs {
		state.mu.Lock()

		shouldRetry := k.Finalized &&
			state.Item != nil &&
			state.Item.Tx.Chain.Equals(common.XMRChain) &&
			len(state.Item.XmrKeyImages) > 0 &&
			len(state.Item.XmrKeyImages) <= maxXMRKeyImages &&
			!hasLocalObservedTxAttestation(state, s.pubKey) &&
			((k.Inbound && s.observerValidateInboundXMRKeyImages != nil) || (!k.Inbound && s.observerValidateXMRTx != nil))
		if shouldRetry {
			candidates = append(candidates, pendingInboundXMRKeyImageAttestation{
				key:                    k,
				obsTx:                  *state.Item,
				allowFutureObservation: k.AllowFutureObservation,
				inbound:                k.Inbound,
			})
		}

		state.mu.Unlock()
	}
	s.mu.Unlock()

	for _, candidate := range candidates {
		s.scheduleXMRKeyImageValidation(ctx, candidate.obsTx, candidate.allowFutureObservation, candidate.inbound)
	}
}

func (s *AttestationGossip) validateXMRObservationAsync(tx common.ObservedTx, onSuccess func()) {
	if s.observerValidateXMRTx == nil {
		return
	}
	if s.validateXMRTxSem == nil {
		go func() {
			if err := s.observerValidateXMRTx(tx); err == nil && onSuccess != nil {
				onSuccess()
			}
		}()
		return
	}
	select {
	case s.validateXMRTxSem <- struct{}{}:
		go func() {
			defer func() { <-s.validateXMRTxSem }()
			if err := s.observerValidateXMRTx(tx); err != nil {
				s.logger.Warn().
					Err(err).
					Str("tx_id", tx.Tx.ID.String()).
					Msg("failed to validate xmr outbound via sidecar")
				return
			}
			if onSuccess != nil {
				onSuccess()
			}
		}()
	default:
		s.logger.Warn().
			Str("tx_id", tx.Tx.ID.String()).
			Msg("xmr outbound validation queue full, dropping")
	}
}

func (s *AttestationGossip) scheduleInboundXMRKeyImageValidation(ctx context.Context, obsTx common.ObservedTx, allowFutureObservation bool) {
	s.scheduleXMRKeyImageValidation(ctx, obsTx, allowFutureObservation, true)
}

func (s *AttestationGossip) scheduleOutboundXMRKeyImageValidation(ctx context.Context, obsTx common.ObservedTx, allowFutureObservation bool) {
	s.scheduleXMRKeyImageValidation(ctx, obsTx, allowFutureObservation, false)
}

func (s *AttestationGossip) scheduleXMRKeyImageValidation(ctx context.Context, obsTx common.ObservedTx, allowFutureObservation bool, inbound bool) {
	if inbound && s.observerValidateInboundXMRKeyImages == nil {
		return
	}
	if !inbound && s.observerValidateXMRTx == nil {
		return
	}

	key, err := observedTxKey(obsTx, allowFutureObservation, inbound)
	if err != nil {
		s.logger.Error().
			Err(err).
			Str("tx_id", obsTx.Tx.ID.String()).
			Msg("failed to derive XMR key-image validation key")
		return
	}

	s.inboundXMRKeyImageMu.Lock()
	if s.inboundXMRKeyImageInFlight == nil {
		s.inboundXMRKeyImageInFlight = make(map[txKey]struct{})
	}
	if _, exists := s.inboundXMRKeyImageInFlight[key]; exists {
		s.inboundXMRKeyImageMu.Unlock()
		return
	}
	s.inboundXMRKeyImageInFlight[key] = struct{}{}
	s.inboundXMRKeyImageQueue = append(s.inboundXMRKeyImageQueue, pendingInboundXMRKeyImageAttestation{
		key:                    key,
		obsTx:                  obsTx,
		allowFutureObservation: allowFutureObservation,
		inbound:                inbound,
	})
	if s.inboundXMRKeyImageWorkerRunning {
		s.inboundXMRKeyImageMu.Unlock()
		return
	}
	s.inboundXMRKeyImageWorkerRunning = true
	s.inboundXMRKeyImageMu.Unlock()

	go s.runInboundXMRKeyImageValidationQueue(ctx)
}

func (s *AttestationGossip) runInboundXMRKeyImageValidationQueue(ctx context.Context) {
	for {
		s.inboundXMRKeyImageMu.Lock()
		if len(s.inboundXMRKeyImageQueue) == 0 {
			s.inboundXMRKeyImageWorkerRunning = false
			s.inboundXMRKeyImageMu.Unlock()
			return
		}
		next := s.inboundXMRKeyImageQueue[0]
		s.inboundXMRKeyImageQueue = s.inboundXMRKeyImageQueue[1:]
		s.inboundXMRKeyImageMu.Unlock()

		if next.inbound {
			s.validateAndCoAttestInboundXMRKeyImages(ctx, next.obsTx, next.allowFutureObservation)
		} else {
			s.validateAndCoAttestOutboundXMRKeyImages(ctx, next.obsTx, next.allowFutureObservation)
		}

		s.inboundXMRKeyImageMu.Lock()
		delete(s.inboundXMRKeyImageInFlight, next.key)
		s.inboundXMRKeyImageMu.Unlock()
	}
}

// Handle a committed quorum transaction event
func (s *AttestationGossip) handleQuorumTxCommitted(en *ebifrost.EventNotification) {
	s.logger.Debug().Msg("handling quorum tx committed event")

	var qtx common.QuorumTx
	if err := qtx.Unmarshal(en.Payload); err != nil {
		s.logger.Error().Err(err).Msg("fail to unmarshal quorum tx")
		return
	}

	if s.observerHandleObservedTxCommitted != nil {
		// if our attestation is in the quorum tx, we can remove it from our observer deck.
		for _, att := range qtx.Attestations {
			if bytes.Equal(att.PubKey, s.pubKey) {
				// we have attested to this tx, and it has been committed to the chain.
				s.logger.Debug().Msg("our attestation is in the quorum tx, passing to observer to remove from ondeck")
				s.observerHandleObservedTxCommitted(qtx.ObsTx)
				break
			}
		}
	}

	if s.observerValidateXMRTx != nil &&
		!qtx.Inbound &&
		qtx.ObsTx.Tx.Chain.Equals(common.XMRChain) &&
		len(qtx.ObsTx.TxKeys) > 0 {
		s.validateXMRObservationAsync(qtx.ObsTx, nil)
	}

	k, err := observedTxKey(qtx.ObsTx, qtx.AllowFutureObservation, qtx.Inbound)
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to derive committed observed tx key")
		return
	}

	s.mu.Lock()
	as, ok := s.observedTxs[k]
	if ok {
		// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
		// This order must be maintained everywhere to avoid deadlock.
		as.mu.Lock()
	}
	s.mu.Unlock()
	if !ok {
		return
	}

	defer as.mu.Unlock()
	as.MarkAttestationsCommitted(qtx.Attestations)
}

// Handle a committed quorum network fee event
func (s *AttestationGossip) handleQuorumNetworkFeeCommitted(en *ebifrost.EventNotification) {
	s.logger.Debug().Msg("handling quorum network fee committed event")

	var qnf common.QuorumNetworkFee
	if err := qnf.Unmarshal(en.Payload); err != nil {
		s.logger.Error().Err(err).Msg("fail to unmarshal quorum network fee")
		return
	}

	s.mu.Lock()
	as, ok := s.networkFees[*qnf.NetworkFee]
	if ok {
		// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
		// This order must be maintained everywhere to avoid deadlock.
		as.mu.Lock()
	}
	s.mu.Unlock()
	if !ok {
		return
	}

	defer as.mu.Unlock()
	as.MarkAttestationsCommitted(qnf.Attestations)
}

// Handle a committed quorum solvency event
func (s *AttestationGossip) handleQuorumSolvencyCommitted(en *ebifrost.EventNotification) {
	s.logger.Debug().Msg("handling quorum solvency committed event")

	var qs common.QuorumSolvency
	if err := qs.Unmarshal(en.Payload); err != nil {
		s.logger.Error().Err(err).Msg("fail to unmarshal quorum solvency")
		return
	}

	s.mu.Lock()
	as, ok := s.solvencies[qs.Solvency.Id]
	if ok {
		// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
		// This order must be maintained everywhere to avoid deadlock.
		as.mu.Lock()
	}
	s.mu.Unlock()
	if !ok {
		return
	}

	defer as.mu.Unlock()
	as.MarkAttestationsCommitted(qs.Attestations)
}

// Handle a committed quorum errata tx event
func (s *AttestationGossip) handleQuorumErrataTxCommitted(en *ebifrost.EventNotification) {
	s.logger.Debug().Msg("handling quorum errata tx committed event")

	var qe common.QuorumErrataTx
	if err := qe.Unmarshal(en.Payload); err != nil {
		s.logger.Error().Err(err).Msg("fail to unmarshal quorum errata")
		return
	}

	s.mu.Lock()
	as, ok := s.errataTxs[*qe.ErrataTx]
	if ok {
		// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
		// This order must be maintained everywhere to avoid deadlock.
		as.mu.Lock()
	}
	s.mu.Unlock()
	if !ok {
		return
	}

	defer as.mu.Unlock()
	as.MarkAttestationsCommitted(qe.Attestations)
}

// Start the attestation gossip service
func (s *AttestationGossip) Start(ctx context.Context) {
	ticker := time.NewTicker(s.config.ObserveReconcileInterval)

	startupDelay := s.config.AskPeersDelay
	delayTimer := time.NewTimer(startupDelay)
	semPruneTicker := time.NewTicker(semaphorePruneInterval)

	defer func() {
		ticker.Stop()
		delayTimer.Stop()
		semPruneTicker.Stop()
	}()

	s.reconcileMimirConfigs()

	go s.batcher.Start(ctx)

	for {
		select {
		case <-ticker.C:
			// prune old attestations and check for late ones to send
			s.mu.Lock()

			// Prune observed transactions
			for k, state := range s.observedTxs {
				state.mu.Lock()
				if state.ExpiredAfterQuorum(s.config.LateObserveTimeout, s.config.NonQuorumTimeout) {
					delete(s.observedTxs, k)
					s.observedTxsPool.PutAttestationState(state)
				} else if state.ShouldSendLate(s.config.MinTimeBetweenAttestations) {
					s.logger.Debug().Msg("sending late observed tx attestations")

					obsTx := state.Item
					s.sendObservedTxAttestationsToThornode(ctx, *obsTx, state, k.Inbound, k.AllowFutureObservation, false)
				}
				state.mu.Unlock()
			}

			// Prune network fees
			for k, state := range s.networkFees {
				state.mu.Lock()
				if state.ExpiredAfterQuorum(s.config.LateObserveTimeout, s.config.NonQuorumTimeout) {
					delete(s.networkFees, k)
					s.networkFeesPool.PutAttestationState(state)
				} else if state.ShouldSendLate(s.config.MinTimeBetweenAttestations) {
					s.logger.Debug().Msg("sending late network fee attestations")
					s.sendNetworkFeeAttestationsToThornode(ctx, *state.Item, state, false)
				}
				state.mu.Unlock()
			}

			// Prune solvencies
			for k, state := range s.solvencies {
				state.mu.Lock()
				if state.ExpiredAfterQuorum(s.config.LateObserveTimeout, s.config.NonQuorumTimeout) {
					delete(s.solvencies, k)
					s.solvenciesPool.PutAttestationState(state)
				} else if state.ShouldSendLate(s.config.MinTimeBetweenAttestations) {
					s.logger.Debug().Msg("sending late solvency attestations")
					s.sendSolvencyAttestationsToThornode(ctx, *state.Item, state, false)
				}
				state.mu.Unlock()
			}

			// Prune errata transactions
			for k, state := range s.errataTxs {
				state.mu.Lock()
				if state.ExpiredAfterQuorum(s.config.LateObserveTimeout, s.config.NonQuorumTimeout) {
					delete(s.errataTxs, k)
					s.errataTxsPool.PutAttestationState(state)
				} else if state.ShouldSendLate(s.config.MinTimeBetweenAttestations) {
					s.logger.Debug().Msg("sending late errata attestations")
					s.sendErrataAttestationsToThornode(ctx, *state.Item, state, false)
				}
				state.mu.Unlock()
			}

			// Prune price feeds
			s.priceFeeds = make(map[string]*AttestationState[*common.PriceFeed])

			s.mu.Unlock()

			s.retryPendingXMROutboundTxKeyAttestations(ctx)
			s.retryPendingInboundXMRKeyImageAttestations(ctx)

			// Prune cached keysign parties
			s.cachedKeySignMu.Lock()
			for pk, cached := range s.cachedKeySignParties {
				if time.Since(cached.lastUpdated) > cachedKeysignPartyTTL {
					delete(s.cachedKeySignParties, pk)
				}
			}
			s.cachedKeySignMu.Unlock()

			s.reconcileMimirConfigs()

		case <-delayTimer.C:
			s.eventClient.Start()

		case <-semPruneTicker.C:
			// Periodically prune semaphores that have been idle for a while
			s.peerMgr.prune()
			s.batcher.peerMgr.prune()
		case <-ctx.Done():
			s.eventClient.Stop()
			return
		}
	}
}

func (s *AttestationGossip) reconcileMimirConfigs() {
	maxBatchSize, err := s.bridge.GetMimir(constants.MimirMaxBatchSize)
	if err != nil {
		s.logger.Error().Str("mimir", constants.MimirMaxBatchSize).Err(err).Msg("fail to get mimir value")
	} else if maxBatchSize > 0 {
		if maxBatchSize != s.batcher.getMaxBatchSize() {
			s.batcher.updateMaxBatchSize(maxBatchSize)
		}
	}

	peerConcurrentSends, err := s.bridge.GetMimir(constants.MimirPeerConcurrentSends)
	if err != nil {
		s.logger.Error().Str("mimir", constants.MimirPeerConcurrentSends).Err(err).Msg("fail to get mimir value")
	} else if peerConcurrentSends > 0 {
		if peerConcurrentSends != int64(s.batcher.peerMgr.getLimit()) {
			s.batcher.peerMgr.updateLimit(int(peerConcurrentSends)) //nolint:staticcheck
		}
	}

	peerConcurrentReceives, err := s.bridge.GetMimir(constants.MimirPeerConcurrentReceives)
	if err != nil {
		s.logger.Error().Str("mimir", constants.MimirPeerConcurrentReceives).Err(err).Msg("fail to get mimir value")
	} else if peerConcurrentReceives > 0 {
		if peerConcurrentReceives != int64(s.peerMgr.getLimit()) {
			s.peerMgr.updateLimit(int(peerConcurrentReceives)) //nolint:staticcheck
		}
	}
}
