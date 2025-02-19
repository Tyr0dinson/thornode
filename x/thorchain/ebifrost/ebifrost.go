package ebifrost

import (
	"context"
	fmt "fmt"
	"net"
	"sync"
	"time"

	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"

	common "gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	"google.golang.org/grpc"
)

const (
	ebifrostSigner        = "thor1zxhfu0qmmq6gmgq4sgz0xgq69h0nhqx5yrseu5"
	cachedBlocks          = 10
	EventQuorumTxCommited = "quorum_tx_committed"
)

var notifyEvents = []string{EventQuorumTxCommited}

var _, ebifrostSignerAcc, _ = bech32.DecodeAndConvert(ebifrostSigner)

// EnshrinedBifrost is a thornode service that is used to communicate with bifrost
// for the purpose of observing transactions and processing quorum attestations.
type EnshrinedBifrost struct {
	cdc    codec.Codec
	s      *grpc.Server
	logger log.Logger

	cfg EBifrostConfig

	// txs is a list of quorum txs that are ready to be included in the next block proposal if we are the proposer.
	txs []*common.QuorumTx
	mu  sync.Mutex

	// recentBlockTxs is a map of block height to quorum txs that were included in that block.
	// This is used to keep track of recently processed attestations so we don't reprocess them.
	recentBlockTxs map[int64][]*common.QuorumTx

	subscribers   map[string][]chan *EventNotification
	subscribersMu sync.Mutex
}

func NewEnshrinedBifrost(cdc codec.Codec, logger log.Logger, config EBifrostConfig) *EnshrinedBifrost {
	if !config.Enable {
		return nil
	}

	s := grpc.NewServer()
	ebs := &EnshrinedBifrost{
		cdc:            cdc,
		logger:         logger.With("module", "ebifrost"),
		s:              s,
		recentBlockTxs: make(map[int64][]*common.QuorumTx),
		subscribers:    make(map[string][]chan *EventNotification),
		cfg:            config,
	}

	RegisterLocalhostBifrostServer(s, ebs)
	return ebs
}

func (b *EnshrinedBifrost) Start() error {
	lis, err := net.Listen("tcp", b.cfg.Address)
	if err != nil {
		return err
	}
	go func() {
		if err := b.s.Serve(lis); err != nil {
			panic(fmt.Errorf("failed to start enshrined bifrost grpc server: %w", err))
		}
	}()

	return nil
}

func (b *EnshrinedBifrost) Stop() {
	if b == nil {
		return
	}
	b.s.Stop()
}

func (b *EnshrinedBifrost) SendQuorumTx(ctx context.Context, tx *common.QuorumTx) (*SendQuorumTxResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// search recent blocks for these attestations and only add the new ones.
	newAttestations := make([]*common.Attestation, 0)
	for _, a := range tx.Attestations {
		found := false
		// analyze-ignore(map-iteration)
		for _, txs := range b.recentBlockTxs {
			for _, t := range txs {
				if t.Tx.Equals(tx.Tx) {
					for _, at := range t.Attestations {
						if a.Equals(at) {
							found = true
							break
						}
					}
					// don't break here, we need to check all blocks because there
					// could be multiple quorum txs, each with different attestations.
				}
			}
		}
		if !found {
			newAttestations = append(newAttestations, a)
		}
	}

	if len(newAttestations) == 0 {
		// No new attestations to add
		return &SendQuorumTxResult{}, nil
	}

	// There might already be an equal quorum tx that has not been injected into a proposal yet.
	// We can add the new attestations to that one if so.
	found := false
	for _, qtx := range b.txs {
		if qtx.Tx.Equals(tx.Tx) {
			// Only append attestations that don't already exist in the transaction
			for _, newAtt := range newAttestations {
				attExists := false
				for _, existingAtt := range qtx.Attestations {
					if newAtt.Equals(existingAtt) {
						attExists = true
						break
					}
				}
				if !attExists {
					qtx.Attestations = append(qtx.Attestations, newAtt)
				}
			}
			found = true
			break
		}
	}

	if !found {
		// Create a new QuorumTx with only the new attestations
		newTx := &common.QuorumTx{
			Tx:           tx.Tx,
			Attestations: newAttestations,
			Inbound:      tx.Inbound,
		}
		b.txs = append(b.txs, newTx)
	}

	return &SendQuorumTxResult{}, nil
}

func (b *EnshrinedBifrost) SubscribeToEvents(req *SubscribeRequest, stream LocalhostBifrost_SubscribeToEventsServer) error {
	for _, eventType := range req.EventTypes {
		found := false
		for _, notifyEvent := range notifyEvents {
			if eventType == notifyEvent {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown event type: %s", eventType)
		}
	}

	eventCh := make(chan *EventNotification)

	// Register this client
	b.subscribersMu.Lock()
	for _, eventType := range req.EventTypes {
		b.subscribers[eventType] = append(b.subscribers[eventType], eventCh)
	}
	b.subscribersMu.Unlock()

	b.logger.Info("Client subscribed to events", "event_types", req.EventTypes)

	// Keep the connection open and forward events to client
	for {
		select {
		case event := <-eventCh:
			if err := stream.Send(event); err != nil {
				return err
			}
		case <-stream.Context().Done():
			// Client disconnected, clean up
			b.removeSubscriber(eventCh)
			return nil
		}
	}
}

// removeSubscriber removes the given channel from all event type subscriptions
func (b *EnshrinedBifrost) removeSubscriber(ch chan *EventNotification) {
	b.subscribersMu.Lock()
	defer b.subscribersMu.Unlock()

	// Iterate through all event types
	// analyze-ignore(map-iteration)
	for eventType, subscribers := range b.subscribers {
		// Create a new slice without the channel we're removing
		newSubscribers := make([]chan *EventNotification, 0, len(subscribers))

		for _, subCh := range subscribers {
			if subCh != ch {
				newSubscribers = append(newSubscribers, subCh)
			}
		}

		// Update the subscribers list for this event type
		if len(newSubscribers) == 0 {
			// No subscribers left for this event type, remove the key
			delete(b.subscribers, eventType)
		} else {
			b.subscribers[eventType] = newSubscribers
		}
	}

	// Close the channel to signal subscribers they won't receive more events
	close(ch)
}

func (b *EnshrinedBifrost) broadcastEvent(eventType string, payload []byte) {
	event := &EventNotification{
		EventType: eventType,
		Payload:   payload,
		Timestamp: time.Now().Unix(),
	}

	b.subscribersMu.Lock()
	subscribers := b.subscribers[eventType]
	b.subscribersMu.Unlock()

	for _, ch := range subscribers {
		select {
		case ch <- event:
			b.logger.Debug("Event sent to subscriber", "event", eventType)
			// Event sent successfully
		default:
			b.logger.Error("Failed to send event to subscriber", "event", eventType)
			// Channel is full or closed, could implement cleanup here
		}
	}
}

func (b *EnshrinedBifrost) broadcastQuorumTxEvent(tx *common.QuorumTx) {
	txBz, err := tx.Marshal()
	if err != nil {
		b.logger.Error("Failed to marshal quorum tx", "error", err)
		return
	}

	b.broadcastEvent(EventQuorumTxCommited, txBz)
}

// MarkQuorumTxAttestationsConfirmed is intended to be called by the bifrost post handler after a tx has been processed.
// It will look for any matching quorum txs, remove the confirmed attestations from them, and remove the quorum txs if all attestations have been confirmed.
func (b *EnshrinedBifrost) MarkQuorumTxAttestationsConfirmed(ctx context.Context, tx *common.QuorumTx) {
	if b == nil {
		return
	}

	go b.broadcastQuorumTxEvent(tx)

	b.mu.Lock()
	defer b.mu.Unlock()

	found := false
	for i := 0; i < len(b.txs); i++ {
		qtx := b.txs[i]
		if qtx.Tx.Equals(tx.Tx) {
			found = true
			b.logger.Debug("Marking quorum tx attestations confirmed", "chain", tx.Tx.Tx.Chain.String(), "hash", tx.Tx.Tx.ID, "attestations", len(tx.Attestations))
			if empty := qtx.RemoveAttestations(tx.Attestations); empty {
				b.logger.Debug("All attestations removed, removing quorum tx", "chain", tx.Tx.Tx.Chain.String(), "hash", tx.Tx.Tx.ID)
				// Remove the element at index i
				b.txs = append(b.txs[:i], b.txs[i+1:]...)
			}
			break
		}
	}
	if !found {
		b.logger.Error("Failed to find quorum tx to mark attestations confirmed", "chain", tx.Tx.Tx.Chain.String(), "hash", tx.Tx.Tx.ID)
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	height := sdkCtx.BlockHeight()

	b.recentBlockTxs[height] = append(b.recentBlockTxs[height], tx)

	// Keep only the last n blocks of quorum tx history
	if len(b.recentBlockTxs) > cachedBlocks {
		// analyze-ignore(map-iteration)
		for h := range b.recentBlockTxs {
			if h < height-cachedBlocks {
				delete(b.recentBlockTxs, h)
			}
		}
	}
}

// ProposalInjectTxs is intended to be called by the current proposing validator during PrepareProposal
// and will return a list of in-quorum transactions to be included in the next block.
func (b *EnshrinedBifrost) ProposalInjectTxs(ctx sdk.Context) [][]byte {
	if b == nil {
		return nil
	}

	var injectTxs [][]byte

	b.mu.Lock()
	defer b.mu.Unlock()

	for _, tx := range b.txs {
		var msg sdk.Msg
		if tx.Inbound {
			msg = &types.MsgObservedTxInQuorum{Tx: tx, Signer: ebifrostSignerAcc}
		} else {
			msg = &types.MsgObservedTxOutQuorum{Tx: tx, Signer: ebifrostSignerAcc}
		}
		itx := NewInjectTx(b.cdc, []sdk.Msg{msg})

		txBz, err := itx.Tx.Marshal()
		if err != nil {
			b.logger.Error("Failed to marshal tx", "error", err)
			continue
		}

		injectTxs = append(injectTxs, txBz)

		b.logger.Debug("Injecting quorum tx", "chain", tx.Tx.Tx.Chain.String(), "hash", tx.Tx.Tx.ID, "finalized", tx.Tx.IsFinal(), "attestations", len(tx.Attestations))
	}

	return injectTxs
}

// Test use only
func (b *EnshrinedBifrost) GetInjectedMsgs(ctx sdk.Context, txs [][]byte) ([]sdk.Msg, error) {
	var msgs []sdk.Msg

	for _, txbz := range txs {
		tx, err := TxDecoder(b.cdc, nil)(txbz)
		if err != nil {
			return nil, err
		}

		msgs = append(msgs, tx.GetMsgs()...)
	}

	return msgs, nil
}
