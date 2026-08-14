package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	tcrypto "github.com/cometbft/cometbft/crypto"
	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	peerstorecore "github.com/libp2p/go-libp2p-core/peerstore"
	"github.com/libp2p/go-libp2p-core/protocol"
	discovery "github.com/libp2p/go-libp2p-discovery"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p-peerstore/addr"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	maddr "github.com/multiformats/go-multiaddr"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
)

var (
	joinPartyProtocol           protocol.ID = "/p2p/join-party"
	joinPartyProtocolWithLeader protocol.ID = "/p2p/join-party-leader"
)

// TSSProtocolID protocol id used for tss
var (
	TSSProtocolID        protocol.ID = "/p2p/tss"
	ObservedTxProtocolID protocol.ID = "/p2p/observed-tx"
)

const (
	// TimeoutConnecting maximum time for wait for peers to connect
	TimeoutConnecting      = time.Second * 20
	bootstrapRetryInterval = time.Second * 5

	StreamUnknown = "UNKNOWN"
	StreamMsgDone = "done"

	// FrostMaxSessionPayload bounds a single FROST outer frame before its full
	// allocation. Monero transactions and DKG maps are well below this ceiling;
	// keeping a distinct limit prevents the 20 MiB legacy TSS frame allowance
	// from becoming the FROST session allowance.
	FrostMaxSessionPayload = 1 * 1024 * 1024
	// FrostSessionQueueSize bounds the shared coordinator queue. Together with
	// the per-frame limit, at most 64 MiB can be retained while the coordinator
	// is stalled.
	FrostSessionQueueSize = 64
	// FrostMaxMessagesPerPeer is the baseline retained/processed traffic bound
	// per peer for one message type and session. Shared FROST verification topics
	// use FrostVerificationMessagesPerPeer because one peer legitimately echoes
	// every participant's transcript in every ceremony round.
	FrostMaxMessagesPerPeer = uint32(128)
	// frostVerificationMessageHeadroom permits a small, bounded amount of
	// delivery overlap without weakening the cumulative byte limits below.
	frostVerificationMessageHeadroom = uint32(16)
	// FrostMaxBytesPerPeer is a per-peer MEMORY backstop, not a fairness
	// guarantee. It is a fixed constant, so it does not scale down with party
	// size: at the maximum ~128-member vault it is ~16x a peer's fair share of
	// FrostMaxSessionBytes, which means a handful of authenticated members
	// (FrostMaxSessionBytes/FrostMaxBytesPerPeer = 16) can spend the whole
	// session budget and starve the rest. This is tolerated because such an
	// attacker must already be an authorized party member, who can fail the
	// ceremony by other means (bad share, refusing to sign) and is attributable
	// via equivocation evidence. Honest full-size load stays far under the
	// session cap (see TestFrostFullPartyCompletesWithinSessionBudget); the
	// starvation characteristic is pinned by
	// TestFrostPerPeerCapIsAMemoryBackstopNotFairness.
	// FrostMaxSessionBytes bounds cumulative bytes once across every topic in a
	// FROST ceremony. FrostMaxNodeBytes applies once across all ceremonies; the
	// concurrency caps additionally bound slow/truncated in-flight readers.
	FrostMaxBytesPerPeer = uint64(8 * 1024 * 1024)
	FrostMaxSessionBytes = uint64(128 * 1024 * 1024)
	FrostMaxNodeBytes    = uint64(256 * 1024 * 1024)

	// A ceremony may retain one full 64-frame coordinator queue plus an equal
	// amount of bounded delivery backpressure. The node-wide count admits that
	// full allowance for each of the default ten concurrent signers; byte and
	// reader limits remain the hard live-memory bounds.
	FrostMaxConcurrentFramesPerSession = uint32(2 * FrostSessionQueueSize)
	FrostMaxConcurrentFrames           = uint32(10) * FrostMaxConcurrentFramesPerSession
	TSSMaxConcurrentReaders            = uint32(256)
	TSSMaxReadersPerPeer               = uint32(16)
	// Each broadcast fans out one writer per remote party member. Per-ceremony
	// isolation prevents one stalled party from monopolizing the bounded
	// node-wide pool; FrostBroadcastMsgChan supplies coordinator backpressure
	// without blocking legacy TSS traffic.
	FrostMaxConcurrentBroadcastsPerSession = uint32(2)
	FrostMaxConcurrentBroadcasts           = uint32(8)
	// One maximum-party DKG can emit roughly three verification broadcasts per
	// remote participant. Keep enough pending room to discover and fairly
	// schedule another ceremony behind that legitimate single-session burst.
	FrostMaxPendingBroadcasts = 512
	// Terminal results have a dedicated ingress so scheduler backpressure from
	// obsolete round traffic cannot prevent keyless/standby members receiving
	// the completed DKG or keysign outcome.
	FrostMaxPendingResults = 32
	// A small local holding queue preserves terminal priority and cancellation
	// ordering while the main scheduler is full. It is a separate hard bound;
	// once full, the dedicated result channel supplies producer backpressure.
	FrostMaxDeferredBroadcasts   = FrostMaxPendingResults
	FrostBroadcastEnqueueTimeout = 250 * time.Millisecond
)

// FrostVerificationMessagesPerPeer sizes the shared verification-topic count
// for a complete ceremony. The baseline preserves the existing allowance for
// small parties; larger parties receive exactly one echo per participant and
// round plus bounded delivery headroom. Byte budgets remain the hard memory
// bound even if the participant set grows unexpectedly.
func FrostVerificationMessagesPerPeer(partySize, rounds int) uint32 {
	if partySize <= 0 || rounds <= 0 {
		return FrostMaxMessagesPerPeer
	}
	max := ^uint32(0)
	party := uint64(partySize)
	roundCount := uint64(rounds)
	if party > uint64(max-frostVerificationMessageHeadroom)/roundCount {
		return max
	}
	limit := uint32(party*roundCount) + frostVerificationMessageHeadroom
	if limit < FrostMaxMessagesPerPeer {
		return FrostMaxMessagesPerPeer
	}
	return limit
}

// Message that get transfer across the wire
type Message struct {
	PeerID  peer.ID
	Payload []byte

	reservation *messageReservation
}

// Accept commits a fully read frame when the protocol coordinator removes it
// from the bounded queue. It returns false if party narrowing revoked the
// authorization epoch while the frame was queued or in flight.
func (m *Message) Accept() bool {
	if m == nil || m.reservation == nil {
		return m != nil
	}
	return m.reservation.accept()
}

// Reject refunds a queued reservation and resets its inbound stream. It is
// idempotent so party narrowing and handler teardown may race safely.
func (m *Message) Reject() {
	if m == nil || m.reservation == nil {
		return
	}
	m.reservation.reject()
}

const maxTSSFrameMsgIDBytes = 256

type tssFrameHeader struct {
	MessageType messages.THORChainTSSMessageType
	MsgID       string
}

// decodeTSSFrameHeader parses only the canonical fields emitted before the
// base64 payload in WrappedMessage. Non-canonical/oversized headers fail
// closed instead of forcing allocation of the declared outer frame.
func decodeTSSFrameHeader(prefix []byte) (tssFrameHeader, error) {
	decoder := json.NewDecoder(bytes.NewReader(prefix))
	token, err := decoder.Token()
	if err != nil {
		return tssFrameHeader{}, fmt.Errorf("invalid TSS frame header: %w", err)
	}
	opening, ok := token.(json.Delim)
	if !ok || opening != '{' {
		return tssFrameHeader{}, fmt.Errorf("TSS frame is not a JSON object")
	}

	var header tssFrameHeader
	seenType := false
	seenMsgID := false
	for {
		token, err = decoder.Token()
		if err != nil {
			return tssFrameHeader{}, fmt.Errorf("TSS frame header exceeds bounded prefix")
		}
		if closing, ok := token.(json.Delim); ok && closing == '}' {
			return tssFrameHeader{}, fmt.Errorf("TSS frame header is incomplete")
		}
		field, ok := token.(string)
		if !ok {
			return tssFrameHeader{}, fmt.Errorf("invalid TSS frame header field")
		}
		switch field {
		case "message_type":
			if seenType {
				return tssFrameHeader{}, fmt.Errorf("duplicate TSS message_type header")
			}
			if err := decoder.Decode(&header.MessageType); err != nil {
				return tssFrameHeader{}, fmt.Errorf("invalid TSS message_type header")
			}
			seenType = true
		case "message_id":
			if seenMsgID {
				return tssFrameHeader{}, fmt.Errorf("duplicate TSS message_id header")
			}
			if err := decoder.Decode(&header.MsgID); err != nil {
				return tssFrameHeader{}, fmt.Errorf("invalid TSS message_id header")
			}
			if !validTSSFrameMsgID(header.MsgID) {
				return tssFrameHeader{}, fmt.Errorf("invalid TSS message_id header")
			}
			seenMsgID = true
		case "payload":
			return tssFrameHeader{}, fmt.Errorf("TSS payload appeared before complete session header")
		default:
			return tssFrameHeader{}, fmt.Errorf("unexpected TSS frame header field")
		}
		if seenType && seenMsgID {
			return header, nil
		}
	}
}

func validTSSFrameMsgID(msgID string) bool {
	if msgID == "" || len(msgID) > maxTSSFrameMsgIDBytes {
		return false
	}
	for i := 0; i < len(msgID); i++ {
		if msgID[i] < 0x21 || msgID[i] > 0x7e {
			return false
		}
	}
	return true
}

// preflightTSSFrame authenticates and reserves exactly one message allowance
// before the outer frame is allocated. Legacy subscriptions retain their
// existing 20 MiB frame limit only when type+msgID actually match.
func (c *Communication) preflightTSSFrame(
	remotePeer peer.ID,
	length uint32,
	prefix []byte,
) (tssFrameHeader, *MessageSubscription, *messageReservation, error) {
	header, err := decodeTSSFrameHeader(prefix)
	if err != nil {
		return tssFrameHeader{}, nil, nil, err
	}
	subscription := c.getSubscriber(header.MessageType, header.MsgID)
	if subscription == nil {
		return tssFrameHeader{}, nil, nil, fmt.Errorf("no matching TSS session subscription")
	}
	reservation := subscription.reserve(remotePeer, int(length))
	if reservation == nil {
		return tssFrameHeader{}, nil, nil, fmt.Errorf("unauthorized, over-limit, or exhausted TSS session frame")
	}
	return header, subscription, reservation, nil
}

// Communication use p2p to broadcast messages among all the TSS nodes
type Communication struct {
	config           P2PConfig
	stateManager     *storage.FileStateMgr
	logger           zerolog.Logger
	listenAddr       maddr.Multiaddr
	host             host.Host
	wg               *sync.WaitGroup
	stopChan         chan struct{} // channel to indicate whether we should stop
	subscribers      map[messages.THORChainTSSMessageType]*MessageIDSubscriber
	subscriberLocker *sync.Mutex
	streamCount      int64
	BroadcastMsgChan chan *messages.BroadcastMsgChan
	// FrostBroadcastMsgChan is intentionally separate from the legacy TSS
	// channel. Saturated/offline XMR ceremonies must not stop BTC and other
	// legacy keygen/keysign traffic from being discovered and dispatched.
	FrostBroadcastMsgChan       chan *messages.BroadcastMsgChan
	FrostResultBroadcastMsgChan chan *messages.BroadcastMsgChan
	// frostBroadcastIngressMu lets ordinary producers enqueue concurrently while
	// cancellation takes exclusive ownership of the queued prefix.
	frostBroadcastIngressMu  sync.RWMutex
	cancelFrostBroadcastChan chan frostBroadcastCancelRequest
	externalAddr             maddr.Multiaddr
	streamMgr                *StreamMgr
	nodeGater                *NodeGater // gater to restrict connections to allowed nodes only
	peerFinder               peerFinder
	frostBudget              *frostIngressBudget
	tssReaders               *tssReaderLimiter

	// broadcastToPeersFn is a deterministic test seam. Production leaves it
	// nil and uses broadcastToPeers.
	broadcastToPeersFn func([]peer.ID, []byte, string, bool)
}

type frostBroadcastCancelRequest struct {
	msgID string
	done  chan struct{}
}

type frostBroadcastCompletion struct {
	msgID    string
	terminal bool
}

type peerFinder interface {
	FindPeer(context.Context, peer.ID) (peer.AddrInfo, error)
}

type P2PConfig interface {
	GetBootstrapPeers() ([]maddr.Multiaddr, error)
	GetP2PPort() int
	GetExternalIP() string
	GetRendezvous() string
}

func StartP2P(
	cfg P2PConfig,
	priKey tcrypto.PrivKey,
	baseFolder string,
) (*Communication, *storage.FileStateMgr, error) {
	return StartP2PWithBridge(cfg, priKey, baseFolder, nil)
}

// StartP2PWithBridge starts the P2P communication layer with optional node gating.
// If bridge is provided, only active or ready nodes will be allowed for inbound connections.
func StartP2PWithBridge(
	cfg P2PConfig,
	priKey tcrypto.PrivKey,
	baseFolder string,
	bridge thorclient.ThorchainBridge,
) (*Communication, *storage.FileStateMgr, error) {
	var stateOpts []storage.FileStateMgrOption
	if seedPhrase, err := common.GetSignerSeedPhrase(); err == nil {
		stateOpts = append(stateOpts, storage.WithAtRestEncryption(seedPhrase))
	} else {
		log.Warn().Err(err).Msg("signer seed phrase unavailable, keyshare at-rest encryption disabled")
	}
	stateManager, err := storage.NewFileStateMgr(baseFolder, stateOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("fail to create file state manager")
	}

	comm, err := NewCommunication(cfg, stateManager, bridge)
	if err != nil {
		return nil, nil, fmt.Errorf("fail to create communication layer: %w", err)
	}

	priKeyRawBytes, err := conversion.GetPriKeyRawBytes(priKey)
	if err != nil {
		return nil, nil, fmt.Errorf("fail to get private key")
	}
	if err := comm.Start(priKeyRawBytes); nil != err {
		return nil, nil, fmt.Errorf("fail to start p2p network: %w", err)
	}

	return comm, stateManager, nil
}

// NewCommunication create a new instance of Communication
func NewCommunication(cfg P2PConfig, stateManager *storage.FileStateMgr, bridge thorclient.ThorchainBridge) (*Communication, error) {
	port, externalIP := cfg.GetP2PPort(), cfg.GetExternalIP()
	addr, err := maddr.NewMultiaddr(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port))
	if err != nil {
		return nil, fmt.Errorf("fail to create listen addr: %w", err)
	}
	var externalAddr maddr.Multiaddr = nil
	if len(externalIP) != 0 {
		externalAddr, err = maddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/%d", externalIP, port))
		if err != nil {
			return nil, fmt.Errorf("fail to create listen with given external IP: %w", err)
		}
	}

	comm := &Communication{
		config:           cfg,
		stateManager:     stateManager,
		logger:           log.With().Str("module", "communication").Logger(),
		listenAddr:       addr,
		wg:               &sync.WaitGroup{},
		stopChan:         make(chan struct{}),
		subscribers:      make(map[messages.THORChainTSSMessageType]*MessageIDSubscriber),
		subscriberLocker: &sync.Mutex{},
		streamCount:      0,
		BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 1024),
		FrostBroadcastMsgChan: make(
			chan *messages.BroadcastMsgChan,
			FrostMaxConcurrentBroadcasts,
		),
		FrostResultBroadcastMsgChan: make(
			chan *messages.BroadcastMsgChan,
			FrostMaxPendingResults,
		),
		cancelFrostBroadcastChan: make(chan frostBroadcastCancelRequest, 128),
		externalAddr:             externalAddr,
		streamMgr:                NewStreamMgr(),
		frostBudget:              newDefaultFrostIngressBudget(),
		tssReaders:               newTSSReaderLimiter(TSSMaxConcurrentReaders, TSSMaxReadersPerPeer),
	}

	// If bridge is provided, create the node gater
	if bridge != nil {
		// Create gater with 60 second refresh interval
		gater := NewNodeGater(bridge, 60*time.Second)
		comm.nodeGater = gater
		comm.logger.Info().Msg("node gater enabled - only allowed nodes can connect")
	}

	return comm, nil
}

// GetHost return the host
func (c *Communication) GetHost() host.Host {
	return c.host
}

// GetLocalPeerID from p2p host
func (c *Communication) GetLocalPeerID() string {
	return c.host.ID().String()
}

const (
	streamWriteAttempts = 3

	// streamWriteRetryDelay is the base backoff between write attempts; it is
	// multiplied by the attempt number, giving 2s then 4s. Deliberately longer
	// than a network round trip: libp2p keeps its own dial backoff per peer, so
	// a sub-second retry lands inside that window and fails instantly. Spacing
	// at 2s/4s gives the backoff a chance to clear.
	streamWriteRetryDelay = 2 * time.Second

	// streamWriteRetryBudget caps total time spent retrying one message so a
	// retry cannot outlive the ceremony that produced it. A single attempt can
	// take up to TimeoutWritePayload (40s), so three attempts could otherwise
	// run past 120s — well beyond keysign_timeout (45s).
	streamWriteRetryBudget = 60 * time.Second
)

// Broadcast message to Peers. msgType decides whether a failed write is
// retried (only keygen traffic is retried; see writeAttemptsFor).
func (c *Communication) Broadcast(peers []peer.ID, msg []byte, msgID string, msgType messages.THORChainTSSMessageType) {
	c.broadcast(peers, msg, msgID, msgType, false)
}

func (c *Communication) broadcast(peers []peer.ID, msg []byte, msgID string, msgType messages.THORChainTSSMessageType, closeAfterWrite bool) {
	if len(peers) == 0 {
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.dispatchBroadcastToPeers(peers, msg, msgID, msgType, closeAfterWrite)
	}()
}

func (c *Communication) dispatchBroadcastToPeers(peers []peer.ID, msg []byte, msgID string, msgType messages.THORChainTSSMessageType, closeAfterWrite bool) {
	if c.broadcastToPeersFn != nil {
		c.broadcastToPeersFn(peers, msg, msgID, closeAfterWrite)
		return
	}
	c.broadcastToPeers(peers, msg, msgID, msgType, closeAfterWrite)
}

// writeAttemptsFor returns how many times a write of this message type may be
// attempted. Retries are scoped to keygen: it has a 5m budget and its unicast
// round has no receiver-side recovery. Keysign has a 45s budget and recovers
// lost broadcasts through the hash-confirmation path.
func writeAttemptsFor(msgType messages.THORChainTSSMessageType) int {
	switch msgType {
	case messages.TSSKeyGenMsg, messages.TSSKeyGenVerMsg:
		return streamWriteAttempts
	default:
		return 1
	}
}

func (c *Communication) broadcastToPeers(peers []peer.ID, msg []byte, msgID string, msgType messages.THORChainTSSMessageType, closeAfterWrite bool) {
	defer func() {
		c.logger.Debug().Msgf("finished sending message to peer(%v)", peers)
	}()
	var (
		wgSend    sync.WaitGroup
		failed    int64
		recovered int64
	)
	start := time.Now()
	maxAttempts := writeAttemptsFor(msgType)
	wgSend.Add(len(peers))
	for _, p := range peers {
		go func(p peer.ID) {
			defer wgSend.Done()
			attempts, err := c.writeToStreamWithRetry(p, msg, msgID, maxAttempts, closeAfterWrite)
			if err != nil {
				atomic.AddInt64(&failed, 1)
				c.logger.Error().Err(err).
					Str("peer", p.String()).
					Str("msgID", msgID).
					Int("attempts", attempts).
					Msg("fail to write to stream")
				return
			}
			if attempts > 1 {
				atomic.AddInt64(&recovered, 1)
			}
		}(p)
	}
	wgSend.Wait()

	if f, r := atomic.LoadInt64(&failed), atomic.LoadInt64(&recovered); f > 0 || r > 0 {
		c.logger.Info().
			Str("msgID", msgID).
			Str("msgType", msgType.String()).
			Int("peers", len(peers)).
			Int("bytes", len(msg)).
			Int64("recovered", r).
			Int64("failed", f).
			Dur("elapsed", time.Since(start)).
			Msg("tss fanout retry summary")
	}
}

// writeToStreamWithRetry pushes msg to a single peer, retrying on a fresh
// stream up to maxAttempts times within the streamWriteRetryBudget.
func (c *Communication) writeToStreamWithRetry(pID peer.ID, msg []byte, msgID string, maxAttempts int, closeAfterWrite bool) (int, error) {
	var err error
	deadline := time.Now().Add(streamWriteRetryBudget)
	attempt := 0
	for attempt < maxAttempts {
		attempt++
		err = c.writeToStream(pID, msg, msgID, closeAfterWrite)
		if err == nil {
			if attempt > 1 {
				c.logger.Info().
					Str("peer", pID.String()).
					Str("msgID", msgID).
					Int("attempt", attempt).
					Msg("stream write recovered on retry")
			}
			return attempt, nil
		}
		if attempt >= maxAttempts || time.Now().After(deadline) {
			break
		}
		c.logger.Debug().Err(err).
			Str("peer", pID.String()).
			Str("msgID", msgID).
			Msg("stream write failed, retrying on a new stream")
		select {
		case <-c.stopChan:
			return attempt, err
		case <-time.After(streamWriteRetryDelay * time.Duration(attempt)):
		}
	}
	return attempt, err
}

func (c *Communication) writeToStream(pID peer.ID, msg []byte, msgID string, closeAfterWrite bool) error {
	// don't send to ourselves
	if pID == c.host.ID() {
		return nil
	}
	stream, err := c.connectToOnePeer(pID)
	if err != nil {
		return fmt.Errorf("fail to open stream to peer(%s): %w", pID, err)
	}
	if nil == stream {
		return nil
	}

	c.logger.Debug().Msgf(">>>writing messages to peer(%s)", pID)

	if closeAfterWrite {
		// connectToOnePeer bounds discovery/opening. Bound the write itself as
		// well so a cancellation acknowledgment can wait for all old round
		// writers to quiesce without trusting the remote peer to read.
		if err := stream.SetWriteDeadline(time.Now().Add(TimeoutConnecting)); err != nil {
			_ = stream.Reset()
			return fmt.Errorf("fail to set FROST stream write deadline: %w", err)
		}
	}
	if err := WriteStreamWithBuffer(msg, stream); err != nil {
		if closeAfterWrite {
			_ = stream.Reset()
		} else {
			c.streamMgr.AddStream(msgID, stream)
		}
		return err
	}
	return c.finishWrittenTSSStream(stream, msgID, closeAfterWrite)
}

func (c *Communication) finishWrittenTSSStream(stream network.Stream, msgID string, closeAfterWrite bool) error {
	if closeAfterWrite {
		if err := stream.Close(); err != nil {
			_ = stream.Reset()
			return fmt.Errorf("fail to gracefully close FROST stream: %w", err)
		}
		return nil
	}
	c.streamMgr.AddStream(msgID, stream)
	return nil
}

func (c *Communication) handleStreamTss(stream network.Stream) {
	remotePeer := stream.Conn().RemotePeer()
	if !c.acquireTSSReadSlot(remotePeer) {
		if err := stream.Reset(); err != nil {
			c.logger.Debug().Err(err).Msg("fail to reset TSS stream rejected by reader concurrency bound")
		}
		return
	}
	readerSlotHeld := true
	defer func() {
		if readerSlotHeld {
			c.releaseTSSReadSlot(remotePeer)
		}
	}()

	peerID := remotePeer.String()
	c.logger.Debug().Msgf("reading from tss stream of peer: %s", peerID)

	var reservation *messageReservation
	delivered := false
	defer func() {
		if delivered {
			return
		}
		if reservation != nil {
			reservation.reject()
			return
		}
		if err := stream.Reset(); err != nil {
			c.logger.Debug().Err(err).Msg("fail to reset rejected or truncated TSS stream")
		}
	}()

	select {
	case <-c.stopChan:
		return
	default:
		var preflightHeader tssFrameHeader
		var subscription *MessageSubscription
		dataBuf, err := readStreamWithBufferPreflight(stream, TSSFramePreflightBytes, func(length uint32, prefix []byte) error {
			var preflightErr error
			preflightHeader, subscription, reservation, preflightErr = c.preflightTSSFrame(remotePeer, length, prefix)
			if reservation != nil {
				reservation.attachStream(stream, c.streamMgr, preflightHeader.MsgID)
			}
			if preflightErr == nil &&
				subscription != nil &&
				subscription.sessionBudget != nil &&
				readerSlotHeld {
				// A valid FROST prefix has already authenticated the peer,
				// session, declared frame size, and aggregate reservation.
				// From here the FROST byte/frame budgets—not the scarce
				// pre-auth reader slots—bound a slow body.
				c.releaseTSSReadSlot(remotePeer)
				readerSlotHeld = false
			}
			return preflightErr
		})
		if err != nil {
			c.logger.Error().Err(err).Msgf("fail to read from stream,peerID: %s", peerID)
			return
		}
		// The reader limiter protects only unauthenticated/preflight I/O. Once
		// the bounded frame has been read and charged to an authenticated
		// ceremony reservation, release it before JSON work or coordinator
		// backpressure. Holding this slot until delivery lets one full session
		// exhaust the global reader pool and reject every concurrent ceremony.
		if readerSlotHeld {
			c.releaseTSSReadSlot(remotePeer)
			readerSlotHeld = false
		}
		var wrappedMsg messages.WrappedMessage
		if err := json.Unmarshal(dataBuf, &wrappedMsg); nil != err {
			c.logger.Error().Err(err).Msg("fail to unmarshal wrapped message bytes")
			return
		}
		if wrappedMsg.MessageType != preflightHeader.MessageType || wrappedMsg.MsgID != preflightHeader.MsgID {
			c.logger.Warn().Msg("dropping TSS frame whose full message changed its preflight session header")
			return
		}
		if current := c.getSubscriber(wrappedMsg.MessageType, wrappedMsg.MsgID); current != subscription ||
			!reservation.current() {
			c.logger.Warn().Msg("dropping TSS frame whose subscription changed after preflight")
			return
		}
		c.logger.Debug().
			Str("message_type", wrappedMsg.MessageType.String()).
			Str("msg_id", wrappedMsg.MsgID).
			Int("payload_bytes", len(wrappedMsg.Payload)).
			Msg("received TSS message")
		message := &Message{
			PeerID:      remotePeer,
			Payload:     dataBuf,
			reservation: reservation,
		}
		// Park before delivery so a consumer cannot race ReleaseStream ahead of
		// this stream being tracked. parkStream also closes the revocation race
		// between the current() check above and retaining the stream.
		if !reservation.parkStream() {
			message.Reject()
			return
		}
		// Bounded FROST queues apply backpressure rather than dropping a valid
		// ceremony frame during a burst.
		if !subscription.deliver(message, c.stopChan) {
			c.logger.Warn().
				Str("peer", remotePeer.String()).
				Str("msg_id", wrappedMsg.MsgID).
				Str("message_type", wrappedMsg.MessageType.String()).
				Msg("stopped before TSS session message delivery completed")
			message.Reject()
			return
		}
		delivered = true

	}
}

func (c *Communication) acquireTSSReadSlot(id peer.ID) bool {
	if c == nil || c.tssReaders == nil {
		return false
	}
	return c.tssReaders.acquire(id)
}

func (c *Communication) releaseTSSReadSlot(id peer.ID) {
	if c == nil || c.tssReaders == nil {
		return
	}
	c.tssReaders.release(id)
}

func (c *Communication) getPeers() addr.AddrList {
	var bootstrapPeers addr.AddrList

	cfgBoostrapPeers, err := c.config.GetBootstrapPeers()
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to get bootstrap peers from config")
	} else {
		bootstrapPeers = cfgBoostrapPeers
	}

	if c.stateManager != nil {
		savedPeers, err := c.stateManager.RetrieveP2PAddresses()
		if err != nil {
			c.logger.Error().Err(err).Msg("fail to get saved peers from state manager")
		} else {
			bootstrapPeers = append(bootstrapPeers, savedPeers...)
		}
	}

	// Apply the same allowlist used by the inbound connection gater so that
	// outbound bootstrap dials are restricted to bonded validator peers.
	if c.nodeGater != nil {
		bootstrapPeers = c.nodeGater.FilterAllowedPeers(bootstrapPeers)
	}

	return bootstrapPeers
}

func (c *Communication) bootstrapConnectivityRequired() bool {
	cfgBootstrapPeers, err := c.config.GetBootstrapPeers()
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to get bootstrap peers from config")
		return true
	}
	if len(cfgBootstrapPeers) == 0 {
		return false
	}
	return !allowUnreachableConfiguredBootstrapPeers
}

func (c *Communication) bootStrapConnectivityCheck() error {
	bootstrapPeers := c.getPeers()

	if len(bootstrapPeers) == 0 {
		c.logger.Error().Msg("we do not have the bootstrap node set, quit the connectivity check")
		return nil
	}

	var onlineNodes uint32
	var wg sync.WaitGroup
	for _, el := range bootstrapPeers {
		peer, err := peer.AddrInfoFromP2pAddr(el)
		if err != nil {
			c.logger.Error().Err(err).Msg("error in decode the bootstrap node, skip it")
			continue
		}
		wg.Add(1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
			defer cancel()
			defer wg.Done()
			outChan := ping.Ping(ctx, c.host, peer.ID)
			select {
			case ret, ok := <-outChan:
				if !ok {
					return
				}
				if ret.Error == nil {
					c.logger.Debug().Msgf("connect to peer %v with RTT %v\n", peer.ID, ret.RTT)
					atomic.AddUint32(&onlineNodes, 1)
				}
			case <-ctx.Done():
				c.logger.Error().Msgf("fail to ping the node %s within 2 seconds", peer.ID)
			}
		}()
	}
	wg.Wait()

	if onlineNodes > 0 {
		c.logger.Info().Msgf("we have successfully ping pong %d nodes", onlineNodes)
		return nil
	}
	c.logger.Error().Msg("fail to ping any bootstrap node")
	return errors.New("the node cannot ping any bootstrap node")
}

func (c *Communication) startChannel(privKeyBytes []byte) error {
	ctx := context.Background()
	p2pPriKey, err := crypto.UnmarshalSecp256k1PrivateKey(privKeyBytes)
	if err != nil {
		c.logger.Error().Msgf("error is %f", err)
		return err
	}

	addressFactory := func(addrs []maddr.Multiaddr) []maddr.Multiaddr {
		if c.externalAddr != nil {
			return []maddr.Multiaddr{c.externalAddr}
		}
		return addrs
	}

	// Build libp2p options
	opts := []libp2p.Option{
		libp2p.ListenAddrs([]maddr.Multiaddr{c.listenAddr}...),
		libp2p.Identity(p2pPriKey),
		libp2p.AddrsFactory(addressFactory),
	}

	// Add connection gater if configured
	if c.nodeGater != nil {
		c.logger.Info().Msg("configuring libp2p with node gater")
		opts = append(opts, libp2p.ConnectionGater(c.nodeGater))
	}

	h, err := libp2p.New(ctx, opts...)
	if err != nil {
		return fmt.Errorf("fail to create p2p host: %w", err)
	}
	c.host = h
	c.logger.Info().Msgf("Host created, we are: %s, at: %s", h.ID(), h.Addrs())
	h.SetStreamHandler(TSSProtocolID, c.handleStreamTss)
	// Start a DHT, for use in peer discovery. We can't just make a new DHT
	// client because we want each peer to maintain its own local copy of the
	// DHT, so that the bootstrapping node of the DHT can go down without
	// inhibiting future peer discovery.
	kademliaDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		return fmt.Errorf("fail to create DHT: %w", err)
	}
	c.peerFinder = kademliaDHT
	c.logger.Debug().Msg("Bootstrapping the DHT")
	if err = kademliaDHT.Bootstrap(ctx); err != nil {
		return fmt.Errorf("fail to bootstrap DHT: %w", err)
	}

	requireBootstrapConnectivity := c.bootstrapConnectivityRequired()
	var connectionErr error
	for i := 0; i < c.bootstrapRetryAttempts(); i++ {
		connectionErr = c.connectToBootstrapPeers()
		if connectionErr == nil {
			break
		}
		c.logger.Warn().Err(connectionErr).Msgf("cannot connect to any bootstrap node, retry in %s", bootstrapRetryInterval)
		time.Sleep(bootstrapRetryInterval)
	}
	if connectionErr != nil {
		if requireBootstrapConnectivity {
			return fmt.Errorf("fail to connect to bootstrap peer: %w", connectionErr)
		}
		// Mocknet/simulation simultaneous restarts can temporarily make every
		// configured bootstrap peer unreachable. Continue there so the HTTP
		// /p2pid endpoint comes up and peers can later recover through DHT.
		c.logger.Warn().Err(connectionErr).Msg("bootstrap peers unreachable at startup, continuing - will discover via DHT")
	}

	// We use a rendezvous point "meet me here" to announce our location.
	// This is like telling your friends to meet you at the Eiffel Tower.
	routingDiscovery := discovery.NewRoutingDiscovery(kademliaDHT)
	discovery.Advertise(ctx, routingDiscovery, c.config.GetRendezvous())
	if err = c.bootStrapConnectivityCheck(); err != nil {
		if requireBootstrapConnectivity {
			return err
		}
		c.logger.Warn().Err(err).Msg("bootstrap connectivity check failed, continuing")
	}

	c.logger.Info().Msg("Successfully announced!")
	return nil
}

func (c *Communication) bootstrapRetryAttempts() int {
	attempts := 5
	if c == nil || c.nodeGater == nil || c.nodeGater.refreshInterval <= 0 {
		return attempts
	}

	// Give bootstrap peers at least one full node-gater refresh window plus a
	// little extra time so simultaneous bifrost restarts do not fail before the
	// bootstrap node has refreshed its allowlist to include the other nodes.
	gaterAttempts := int(c.nodeGater.refreshInterval/bootstrapRetryInterval) + 2
	if gaterAttempts > attempts {
		return gaterAttempts
	}
	return attempts
}

func (c *Communication) connectToOnePeer(pID peer.ID) (network.Stream, error) {
	c.logger.Debug().Msgf("peer:%s,current:%s", pID, c.host.ID())
	// dont connect to itself
	if pID == c.host.ID() {
		return nil, nil
	}
	c.logger.Debug().Msgf("connect to peer : %s", pID.String())
	ctx, cancel := context.WithTimeout(context.Background(), TimeoutConnecting)
	defer cancel()
	if len(c.host.Peerstore().Addrs(pID)) == 0 {
		if err := c.discoverPeer(ctx, pID); err != nil {
			c.logger.Debug().Err(err).Msg("fail to discover peer before opening stream")
		}
	}
	stream, err := c.host.NewStream(ctx, pID, TSSProtocolID)
	if err != nil {
		return nil, fmt.Errorf("fail to create new stream to peer: %s, %w", pID, err)
	}
	return stream, nil
}

func (c *Communication) discoverPeer(ctx context.Context, pID peer.ID) error {
	if c == nil || c.host == nil {
		return errors.New("communication host is not initialized")
	}
	if len(c.host.Peerstore().Addrs(pID)) > 0 {
		return nil
	}
	if c.peerFinder == nil {
		return errors.New("peer discovery is not initialized")
	}

	info, err := c.peerFinder.FindPeer(ctx, pID)
	if err != nil {
		return err
	}
	if info.ID != pID {
		return fmt.Errorf("discovered unexpected peer %s while looking for %s", info.ID, pID)
	}
	if len(info.Addrs) == 0 {
		return fmt.Errorf("discovered peer %s without addresses", pID)
	}

	c.host.Peerstore().AddAddrs(info.ID, info.Addrs, peerstorecore.TempAddrTTL)
	return nil
}

func (c *Communication) connectToBootstrapPeers() error {
	bootstrapPeers := c.getPeers()
	// Let's connect to the bootstrap nodes first. They will tell us about the
	// other nodes in the network.
	if len(bootstrapPeers) == 0 {
		c.logger.Info().Msg("no bootstrap node set, we skip the connection")
		return nil
	}
	var wg sync.WaitGroup
	connRet := make(chan bool, len(bootstrapPeers))
	dialAttempts := 0
	for _, peerAddr := range bootstrapPeers {
		pi, err := peer.AddrInfoFromP2pAddr(peerAddr)
		if err != nil {
			return fmt.Errorf("fail to add peer: %w", err)
		}
		// Skip connecting to ourselves
		if pi.ID == c.host.ID() {
			c.logger.Debug().Msgf("skipping connection to self: %s", pi.ID)
			continue
		}
		dialAttempts++
		wg.Add(1)
		go func(connRet chan bool) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), TimeoutConnecting)
			defer cancel()
			if err := c.host.Connect(ctx, *pi); err != nil {
				c.logger.Error().Err(err).Msgf("fail to connect to %s", pi.String())
				connRet <- false
				return
			}
			connRet <- true
			c.logger.Info().Msgf("Connection established with bootstrap node: %s", *pi)
		}(connRet)
	}
	wg.Wait()
	// Read exactly dialAttempts results, not len(bootstrapPeers). Self entries
	// are skipped above without spawning a goroutine, so reading more than
	// dialAttempts blocks forever waiting for sends that never come.
	if dialAttempts == 0 {
		return nil
	}
	for i := 0; i < dialAttempts; i++ {
		if <-connRet {
			return nil
		}
	}
	return errors.New("fail to connect to any peer")
}

// Start will start the communication
func (c *Communication) Start(priKeyBytes []byte) error {
	// Start the node gater if configured
	if c.nodeGater != nil {
		c.nodeGater.Start()
	}

	err := c.startChannel(priKeyBytes)
	if err == nil {
		c.wg.Add(1)
		go c.ProcessBroadcast()
	} else {
		if c.host != nil {
			if closeErr := c.host.Close(); closeErr != nil {
				c.logger.Err(closeErr).Msg("fail to close partially started host")
			}
			c.host = nil
		}
		if c.nodeGater != nil {
			c.nodeGater.Stop()
		}
	}
	return err
}

// Stop communication
func (c *Communication) Stop() error {
	// Stop the node gater if configured
	if c.nodeGater != nil {
		c.nodeGater.Stop()
	}

	// we need to stop the handler and the p2p services firstly, then terminate the our communication threads
	// host is nil when Start failed and already tore it down
	if c.host != nil {
		if err := c.host.Close(); err != nil {
			c.logger.Err(err).Msg("fail to close host network")
		}
	}

	close(c.stopChan)
	c.wg.Wait()
	return nil
}

func (c *Communication) SetSubscribe(topic messages.THORChainTSSMessageType, msgID string, channel chan *Message) {
	c.subscriberLocker.Lock()
	defer c.subscriberLocker.Unlock()

	if c.tssReaders == nil {
		c.tssReaders = newTSSReaderLimiter(TSSMaxConcurrentReaders, TSSMaxReadersPerPeer)
	}
	messageIDSubscribers, ok := c.subscribers[topic]
	if !ok {
		messageIDSubscribers = NewMessageIDSubscriber()
		c.subscribers[topic] = messageIDSubscribers
	}
	messageIDSubscribers.Subscribe(msgID, channel)
}

// SetSubscribeWithPeers creates a session subscription that accepts messages
// only from the supplied authenticated libp2p peers and applies hard per-
// session resource limits before delivery to the protocol coordinator.
func (c *Communication) SetSubscribeWithPeers(
	topic messages.THORChainTSSMessageType,
	msgID string,
	channel chan *Message,
	allowedPeers []peer.ID,
	maxPayloadBytes int,
	maxMessagesPerPeer uint32,
) {
	c.subscriberLocker.Lock()
	defer c.subscriberLocker.Unlock()

	messageIDSubscribers, ok := c.subscribers[topic]
	if !ok {
		messageIDSubscribers = NewMessageIDSubscriber()
		c.subscribers[topic] = messageIDSubscribers
	}
	if c.frostBudget == nil {
		c.frostBudget = newDefaultFrostIngressBudget()
	}
	if c.tssReaders == nil {
		c.tssReaders = newTSSReaderLimiter(TSSMaxConcurrentReaders, TSSMaxReadersPerPeer)
	}
	// Copy the caller-owned slice so later party filtering cannot mutate the
	// authorization decision of an already registered subscription.
	peers := append([]peer.ID(nil), allowedPeers...)
	sessionBudget := c.frostBudget.attach(msgID)
	messageIDSubscribers.subscribeRestrictedWithBudgets(
		msgID,
		channel,
		peers,
		maxPayloadBytes,
		maxMessagesPerPeer,
		FrostMaxBytesPerPeer,
		FrostMaxSessionBytes,
		sessionBudget,
	)
}

// UpdateSubscribePeers narrows (or replaces) the authenticated allow-list for
// an existing session after party formation. It never creates a subscription.
func (c *Communication) UpdateSubscribePeers(topic messages.THORChainTSSMessageType, msgID string, allowedPeers []peer.ID) bool {
	c.subscriberLocker.Lock()
	messageIDSubscribers := c.subscribers[topic]
	c.subscriberLocker.Unlock()
	if messageIDSubscribers == nil {
		return false
	}
	return messageIDSubscribers.UpdateAllowedPeers(msgID, append([]peer.ID(nil), allowedPeers...))
}

func (c *Communication) getSubscriber(topic messages.THORChainTSSMessageType, msgID string) *MessageSubscription {
	c.subscriberLocker.Lock()
	defer c.subscriberLocker.Unlock()
	messageIDSubscribers, ok := c.subscribers[topic]
	if !ok {
		c.logger.Debug().Msgf("fail to find subscribers for %s", topic)
		return nil
	}
	return messageIDSubscribers.GetSubscriber(msgID)
}

func (c *Communication) CancelSubscribe(topic messages.THORChainTSSMessageType, msgID string) {
	c.subscriberLocker.Lock()
	defer c.subscriberLocker.Unlock()

	messageIDSubscribers, ok := c.subscribers[topic]
	if !ok {
		c.logger.Debug().Msgf("cannot find the given channels %s", topic.String())
		return
	}
	if nil == messageIDSubscribers {
		return
	}
	messageIDSubscribers.UnSubscribe(msgID)
	if messageIDSubscribers.IsEmpty() {
		delete(c.subscribers, topic)
	}
}

func (c *Communication) ProcessBroadcast() {
	c.logger.Debug().Msg("start to process broadcast message channel")
	defer c.logger.Debug().Msg("stop process broadcast message channel")
	defer c.wg.Done()
	scheduler := newFrostBroadcastScheduler(
		FrostMaxConcurrentBroadcasts,
		FrostMaxConcurrentBroadcastsPerSession,
		FrostMaxPendingBroadcasts,
	)
	completed := make(chan frostBroadcastCompletion, FrostMaxConcurrentBroadcasts)
	cancelWaiters := make(map[string][]chan struct{})
	deferredFrost := make([]frostBroadcastTask, 0, FrostMaxConcurrentBroadcasts)
	hasDeferredCapacity := func() bool {
		return len(deferredFrost) < FrostMaxDeferredBroadcasts
	}
	deferTask := func(task frostBroadcastTask) bool {
		// Cancellation and legacy routing may need to retain work while the
		// scheduler is full. Never turn that small holding queue into another
		// unbounded ingress.
		if !hasDeferredCapacity() {
			return false
		}
		if !task.terminal {
			deferredFrost = append(deferredFrost, task)
			return true
		}
		// Terminal results retain FIFO relative to each other, but are admitted
		// before deferred round traffic as soon as the scheduler has capacity.
		insertAt := 0
		for insertAt < len(deferredFrost) && deferredFrost[insertAt].terminal {
			insertAt++
		}
		deferredFrost = append(deferredFrost, frostBroadcastTask{})
		copy(deferredFrost[insertAt+1:], deferredFrost[insertAt:])
		deferredFrost[insertAt] = task
		return true
	}
	for {
		for {
			task, ok := scheduler.next()
			if !ok {
				break
			}
			c.wg.Add(1)
			go func(task frostBroadcastTask) {
				defer c.wg.Done()
				c.dispatchBroadcastToPeers(task.peers, task.msg, task.msgID, task.msgType, true)
				select {
				case completed <- frostBroadcastCompletion{
					msgID:    task.msgID,
					terminal: task.terminal,
				}:
				case <-c.stopChan:
				}
			}(task)
		}

		fedDeferred := false
		for len(deferredFrost) > 0 &&
			(scheduler.maxPending <= 0 || scheduler.pending < scheduler.maxPending) {
			task := deferredFrost[0]
			deferredFrost[0] = frostBroadcastTask{}
			deferredFrost = deferredFrost[1:]
			if !scheduler.enqueue(task) {
				// The loop condition and single-goroutine scheduler ownership
				// make this unreachable, but retain the task fail closed.
				deferredFrost = append([]frostBroadcastTask{task}, deferredFrost...)
				break
			}
			fedDeferred = true
		}
		if fedDeferred {
			continue
		}

		schedulerCapacity := scheduler.maxPending <= 0 || scheduler.pending < scheduler.maxPending
		resultInput := (<-chan *messages.BroadcastMsgChan)(c.FrostResultBroadcastMsgChan)
		if !schedulerCapacity && !hasDeferredCapacity() {
			// Do not drain the bounded result channel into local memory while
			// both fixed pending budgets are full. QueueFrostBroadcast will then
			// apply its bounded enqueue timeout.
			resultInput = nil
		}
		frostInput := (<-chan *messages.BroadcastMsgChan)(c.FrostBroadcastMsgChan)
		if len(deferredFrost) > 0 || !schedulerCapacity {
			frostInput = nil
		}
		// Once pending capacity opens, give a queued terminal result the next
		// scheduler slot instead of racing it against more ordinary round work.
		if resultInput != nil && len(c.FrostResultBroadcastMsgChan) > 0 {
			frostInput = nil
		}
		select {
		case msg := <-resultInput:
			task, err := frostBroadcastTaskFromMessage(msg)
			if err != nil {
				c.logger.Error().Err(err).Msg("invalid message on FROST result broadcast channel")
				continue
			}
			if !task.terminal {
				c.logger.Error().
					Str("message_type", msg.WrappedMessage.MessageType.String()).
					Msg("non-terminal message received on FROST result broadcast channel")
				continue
			}
			if !scheduler.enqueue(task) {
				if !deferTask(task) {
					// resultInput is disabled when both bounded destinations are
					// full, so this indicates an invariant bug.
					c.logger.Error().
						Str("msg_id", task.msgID).
						Msg("failed to admit terminal FROST result despite bounded backlog capacity")
				}
			}
		case msg := <-frostInput:
			task, err := frostBroadcastTaskFromMessage(msg)
			if err != nil {
				c.logger.Error().Err(err).Msg("invalid message on FROST broadcast channel")
				continue
			}
			if !scheduler.enqueue(task) {
				c.logger.Error().
					Str("msg_id", task.msgID).
					Msg("bounded FROST broadcast scheduler rejected a task")
			}
		case msg := <-c.BroadcastMsgChan:
			wrappedMsgBytes, err := json.Marshal(msg.WrappedMessage)
			if err != nil {
				c.logger.Error().Err(err).Msg("fail to marshal a wrapped message to json bytes")
				continue
			}
			c.logger.Debug().Msgf("broadcast message %s to %+v", msg.WrappedMessage, msg.PeersID)
			if isFrostMessageType(msg.WrappedMessage.MessageType) {
				task, taskErr := frostBroadcastTaskFromMessage(msg)
				if taskErr != nil {
					c.logger.Error().Err(taskErr).Msg("invalid FROST message on legacy broadcast channel")
					continue
				}
				if !scheduler.enqueue(task) {
					if task.terminal {
						if !deferTask(task) {
							c.logger.Error().
								Str("msg_id", task.msgID).
								Msg("bounded FROST backlog rejected terminal result from legacy channel")
						}
						continue
					}
					c.logger.Error().
						Str("msg_id", msg.WrappedMessage.MsgID).
						Msg("bounded FROST broadcast scheduler rejected a task")
				}
				continue
			}
			c.broadcast(
				msg.PeersID,
				wrappedMsgBytes,
				msg.WrappedMessage.MsgID,
				msg.WrappedMessage.MessageType,
				false,
			)

		case request := <-c.cancelFrostBroadcastChan:
			dropped := scheduler.cancel(request.msgID)

			// A coordinator queues all of its messages before synchronously
			// canceling. Drain the isolated ingress channel as part of that
			// cancellation barrier so stale round frames cannot be selected
			// after the acknowledgment. Preserve other ceremonies and final
			// result propagation in a small local deferred queue.
			filteredDeferred := deferredFrost[:0]
			for i := range deferredFrost {
				task := deferredFrost[i]
				if task.msgID == request.msgID && !task.terminal {
					dropped++
					deferredFrost[i] = frostBroadcastTask{}
					continue
				}
				filteredDeferred = append(filteredDeferred, task)
			}
			clear(deferredFrost[len(filteredDeferred):])
			deferredFrost = filteredDeferred

			dropped += c.drainCanceledFrostIngress(request.msgID, deferTask)
			if dropped > 0 {
				c.logger.Info().
					Str("msg_id", request.msgID).
					Int("dropped", dropped).
					Msg("canceled pending FROST broadcasts for completed ceremony")
			}
			if scheduler.activeOrdinary(request.msgID) == 0 {
				close(request.done)
			} else {
				// Do not let a retry reuse the deterministic msgID until every
				// old ordinary writer has either closed or hit its write
				// deadline. Terminal result propagation is intentionally
				// independent and does not delay the cancellation barrier.
				cancelWaiters[request.msgID] = append(
					cancelWaiters[request.msgID],
					request.done,
				)
			}
		case completion := <-completed:
			scheduler.complete(completion.msgID, completion.terminal)
			if scheduler.activeOrdinary(completion.msgID) == 0 {
				for _, done := range cancelWaiters[completion.msgID] {
					close(done)
				}
				delete(cancelWaiters, completion.msgID)
			}
		case <-c.stopChan:
			return
		}
	}
}

// drainCanceledFrostIngress removes queued non-terminal work for msgID. Other
// valid tasks are handed to preserve or requeued in FIFO order while the
// ingress lock keeps producers from taking the reclaimed slots.
func (c *Communication) drainCanceledFrostIngress(
	msgID string,
	preserve func(frostBroadcastTask) bool,
) int {
	c.frostBroadcastIngressMu.Lock()
	defer c.frostBroadcastIngressMu.Unlock()

	// ProcessBroadcast is the sole consumer. With producers excluded, len is a
	// stable prefix and every receive reserves a slot for lossless requeue.
	drainCount := len(c.FrostBroadcastMsgChan)
	requeue := make([]*messages.BroadcastMsgChan, 0, drainCount)
	dropped := 0
	for range drainCount {
		msg := <-c.FrostBroadcastMsgChan
		task, err := frostBroadcastTaskFromMessage(msg)
		if err != nil {
			c.logger.Error().Err(err).Msg("invalid message on FROST broadcast channel")
			continue
		}
		if task.msgID == msgID && !task.terminal {
			dropped++
			continue
		}
		if preserve(task) {
			continue
		}
		requeue = append(requeue, msg)
	}
	for _, msg := range requeue {
		c.FrostBroadcastMsgChan <- msg
	}
	return dropped
}

// CancelFrostBroadcasts removes pending fanout work when a coordinator exits.
// Already-active writers remain bounded to two per ceremony and report their
// ordinary completion; hundreds of stale queued writes cannot outlive a timed
// out DKG/keysign and backpressure later XMR work.
func (c *Communication) CancelFrostBroadcasts(msgID string) {
	if c == nil || msgID == "" || c.cancelFrostBroadcastChan == nil {
		return
	}
	request := frostBroadcastCancelRequest{
		msgID: msgID,
		done:  make(chan struct{}),
	}
	select {
	case c.cancelFrostBroadcastChan <- request:
	case <-c.stopChan:
		return
	}
	select {
	case <-request.done:
	case <-c.stopChan:
	}
}

func frostBroadcastTaskFromMessage(msg *messages.BroadcastMsgChan) (frostBroadcastTask, error) {
	if msg == nil {
		return frostBroadcastTask{}, errors.New("nil FROST broadcast message")
	}
	if !validTSSFrameMsgID(msg.WrappedMessage.MsgID) {
		return frostBroadcastTask{}, errors.New("invalid FROST broadcast message ID")
	}
	if !isFrostMessageType(msg.WrappedMessage.MessageType) {
		return frostBroadcastTask{}, fmt.Errorf(
			"non-FROST message type %s",
			msg.WrappedMessage.MessageType,
		)
	}
	wrappedMsgBytes, err := json.Marshal(msg.WrappedMessage)
	if err != nil {
		return frostBroadcastTask{}, fmt.Errorf("marshal FROST wrapped message: %w", err)
	}
	// The receiver applies this limit to the encoded outer frame. JSON base64
	// expansion means checking Payload itself would undercount.
	if len(wrappedMsgBytes) > FrostMaxSessionPayload {
		return frostBroadcastTask{}, fmt.Errorf(
			"encoded FROST frame is %d bytes, maximum is %d",
			len(wrappedMsgBytes),
			FrostMaxSessionPayload,
		)
	}
	return frostBroadcastTask{
		peers:    append([]peer.ID(nil), msg.PeersID...),
		msg:      wrappedMsgBytes,
		msgID:    msg.WrappedMessage.MsgID,
		msgType:  msg.WrappedMessage.MessageType,
		terminal: isFrostTerminalResult(msg.WrappedMessage.MessageType),
	}, nil
}

// QueueFrostBroadcast routes FROST traffic to its isolated, bounded ingress.
// The legacy fallback preserves focused coordinator tests which construct a
// minimal Communication without starting the production P2P service.
func (c *Communication) QueueFrostBroadcast(msg *messages.BroadcastMsgChan) error {
	if c == nil || msg == nil {
		return errors.New("FROST broadcast is not configured")
	}
	// Queue an owned snapshot so caller mutations after return cannot change
	// the frame or peer set that passed synchronous validation.
	queued := &messages.BroadcastMsgChan{
		WrappedMessage: msg.WrappedMessage,
		PeersID:        append([]peer.ID(nil), msg.PeersID...),
	}
	queued.WrappedMessage.Payload = bytes.Clone(msg.WrappedMessage.Payload)
	task, err := frostBroadcastTaskFromMessage(queued)
	if err != nil {
		return err
	}
	out := c.FrostBroadcastMsgChan
	if task.terminal &&
		c.FrostResultBroadcastMsgChan != nil {
		out = c.FrostResultBroadcastMsgChan
	}
	if out == nil {
		out = c.BroadcastMsgChan
	}
	if out == nil {
		return errors.New("FROST broadcast channel is not configured")
	}
	// Hold the ordinary ingress read lock through the bounded send so
	// cancellation cannot drain while admission is in flight. Terminal and
	// legacy queues are never selectively drained.
	if out == c.FrostBroadcastMsgChan {
		c.frostBroadcastIngressMu.RLock()
		defer c.frostBroadcastIngressMu.RUnlock()
	}
	select {
	case out <- queued:
		return nil
	case <-c.stopChan:
		return errors.New("P2P communication stopped before FROST broadcast was queued")
	default:
	}
	// Briefly absorb legitimate verification bursts, but never let scheduler
	// saturation pin a coordinator past its ceremony timeout or prevent its
	// deferred cancellation from running.
	timer := time.NewTimer(FrostBroadcastEnqueueTimeout)
	defer timer.Stop()
	select {
	case out <- queued:
		return nil
	case <-c.stopChan:
		return errors.New("P2P communication stopped before FROST broadcast was queued")
	case <-timer.C:
		return errors.New("bounded FROST broadcast ingress remained full")
	}
}

func isFrostMessageType(msgType messages.THORChainTSSMessageType) bool {
	switch msgType {
	case messages.TSSFrostPreprocessMsg,
		messages.TSSFrostShareMsg,
		messages.TSSFrostVerifyMsg,
		messages.TSSFrostResultMsg,
		messages.TSSFrostDKGRound1Msg,
		messages.TSSFrostDKGRound2Msg,
		messages.TSSFrostDKGRound3Msg,
		messages.TSSFrostDKGResultMsg:
		return true
	default:
		return false
	}
}

func isFrostTerminalResult(msgType messages.THORChainTSSMessageType) bool {
	return msgType == messages.TSSFrostResultMsg ||
		msgType == messages.TSSFrostDKGResultMsg
}

func (c *Communication) ReleaseStream(msgID string) {
	c.streamMgr.ReleaseStream(msgID)
}
