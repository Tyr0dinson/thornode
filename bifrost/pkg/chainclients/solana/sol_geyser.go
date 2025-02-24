package solana

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	pb "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/geyser/proto"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

// SolGeyser manages a connection to a Solana Geyser service and maintains a block cache
type SolGeyser struct {
	cfg      config.BifrostBlockScannerConfiguration
	scanner  *SOLScanner
	logger   zerolog.Logger
	endpoint string
	token    string
	client   pb.GeyserClient

	// Block cache
	blockCache    map[uint64]*rpc.BlockResult
	blockQueueMux sync.RWMutex
	slotQueue     []uint64
	maxCacheSize  int

	// Stream and mutex
	streamMux sync.RWMutex
	stream    pb.Geyser_SubscribeClient
	wg        sync.WaitGroup
}

var kacp = keepalive.ClientParameters{
	Time:                10 * time.Second, // send pings every 10 seconds if there is no activity
	Timeout:             5 * time.Second,  // wait 5 second for ping ack before considering the connection dead
	PermitWithoutStream: true,             // send pings even without active streams
}

// NewSolGeyser creates a new SolGeyser instance with the provided configuration
// and establishes a gRPC connection to the Geyser endpoint
func NewSolGeyser(cfg config.BifrostBlockScannerConfiguration, scanner *SOLScanner, endpoint string, token string) (*SolGeyser, error) {
	logger := log.Logger.With().Stringer("chain", cfg.ChainID).Logger()

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})),
		grpc.WithKeepaliveParams(kacp),
		grpc.WithPerRPCCredentials(tokenAuth{token: token}),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(1024 * 1024 * 8),
		),
	}

	conn, err := grpc.NewClient(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to geyser: %w", err)
	}

	client := pb.NewGeyserClient(conn)

	logger.Info().Msg("Geyser client created")

	return &SolGeyser{
		cfg:          cfg,
		scanner:      scanner,
		logger:       logger,
		endpoint:     endpoint,
		token:        token,
		client:       client,
		blockCache:   make(map[uint64]*rpc.BlockResult),
		slotQueue:    make([]uint64, 0),
		maxCacheSize: 1000, // We can adjust this value based on our needs
	}, nil
}

func (g *SolGeyser) createSubscription() (pb.Geyser_SubscribeClient, error) {
	commitment := pb.CommitmentLevel_FINALIZED
	subReq := &pb.SubscribeRequest{
		Commitment: &commitment,
		Blocks: map[string]*pb.SubscribeRequestFilterBlocks{
			"blocks": {},
		},
	}

	d, _ := json.Marshal(subReq)
	g.logger.Info().Msgf("Subscription request: %s\n", string(d))

	stream, err := g.client.Subscribe(context.Background())
	if err != nil {
		g.logger.Error().Err(err).Msg("Failed to subscribe to yellowstone geyser.")
		return nil, err
	}

	if err = stream.Send(subReq); err != nil {
		g.logger.Error().Err(err).Msg("Failed to send subscription request.")
		return nil, err
	}

	return stream, nil
}

// Run starts the Geyser subscription and block processing goroutine
// It handles stream reconnection with exponential backoff on errors
func (g *SolGeyser) Run() {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()

		stream, err := g.createSubscription()
		if err != nil {
			g.logger.Error().Err(err).Msg("Initial subscription failed")
			return
		}

		// Store stream reference
		g.streamMux.Lock()
		g.stream = stream
		g.streamMux.Unlock()

		for {
			resp, err := stream.Recv()

			if err != nil {
				g.logger.Error().Err(err).Msg("Error in receiving update.")
				// Close existing stream
				stream.CloseSend()

				// Attempt to create new stream with exponential backoff
				var newStream pb.Geyser_SubscribeClient
				for retries := 0; retries < 3; retries++ {
					newStream, err = g.createSubscription()
					if err == nil {
						stream = newStream
						// Update stream reference
						g.streamMux.Lock()
						g.stream = newStream
						g.streamMux.Unlock()
						g.logger.Info().Msg("Successfully reconnected to geyser stream")
						break
					}
					backoffDuration := time.Second * time.Duration(math.Pow(2, float64(retries)))
					time.Sleep(backoffDuration)
				}
				if err != nil {
					g.logger.Error().Err(err).Msg("Failed to reconnect after multiple attempts")
					return
				}
				continue
			}

			block := resp.GetBlock()
			height := block.GetBlockHeight().GetBlockHeight()
			if height == 0 {
				continue
			}
			if height%100 == 0 {
				g.logger.Info().Msgf("%s: Height: %d, Cache size: %d", time.Now().Format(time.UnixDate), height, len(g.blockCache))
			}
			if block.GetTransactions() != nil {
				slot := block.GetSlot()
				g.logger.Debug().Msgf("Cached slot height: %d", slot)

				// Convert and store block
				rpcBlock := GeyserToRPCBlock(block)
				g.blockQueueMux.Lock()
				g.blockCache[slot] = &rpcBlock
				g.slotQueue = append(g.slotQueue, slot)

				// Cleanup old blocks if cache is too large
				if len(g.slotQueue) > g.maxCacheSize {
					oldestSlot := g.slotQueue[0]
					delete(g.blockCache, oldestSlot)
					g.slotQueue = g.slotQueue[1:]
					g.logger.Warn().Msgf("Dropped block from cache at slot: %d", oldestSlot)
				}
				g.blockQueueMux.Unlock()
			}
		}
	}()
}

// GetBlock retrieves a block from the cache by slot number
// Returns the block and a boolean indicating if it was found
func (g *SolGeyser) GetBlock(slot uint64) (*rpc.BlockResult, bool) {
	g.blockQueueMux.RLock()
	defer g.blockQueueMux.RUnlock()
	block, exists := g.blockCache[slot]
	return block, exists
}

// RemoveBlock removes and returns a block from the cache by slot number
// Returns the removed block and a boolean indicating if it was found
func (g *SolGeyser) RemoveBlock(slot uint64) (*rpc.BlockResult, bool) {
	g.blockQueueMux.Lock()
	defer g.blockQueueMux.Unlock()

	block, exists := g.blockCache[slot]
	if !exists {
		return nil, false
	}

	delete(g.blockCache, slot)

	// Remove from queue
	for i, s := range g.slotQueue {
		if s == slot {
			g.slotQueue = append(g.slotQueue[:i], g.slotQueue[i+1:]...)
			break
		}
	}

	return block, true
}

// Stop gracefully shuts down the Geyser connection and waits for goroutines to complete
func (g *SolGeyser) Stop() {
	g.streamMux.RLock()
	if g.stream != nil {
		if err := g.stream.CloseSend(); err != nil {
			g.logger.Error().Err(err).Msg("Error closing geyser stream")
		} else {
			g.logger.Info().Msg("Geyser stream closed successfully")
		}
	}
	g.streamMux.RUnlock()

	g.wg.Wait() // Wait for goroutine to finish
}
