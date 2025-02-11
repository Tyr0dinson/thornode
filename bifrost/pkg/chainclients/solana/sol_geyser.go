package solana

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	pb "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/geyser/proto"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

type SolGeyser struct {
	cfg                config.BifrostBlockScannerConfiguration
	scanner            *SOLScanner
	logger             zerolog.Logger
	endpoint           string
	token              string
	client             pb.GeyserClient
	currentBlockHeight uint64

	// fee rate
	feeRateCache        []uint64
	feeRate             uint64
	lastReportedFeeRate uint64

	// metrics
	totalDuration time.Duration
	blockCount    int
	avgDuration   time.Duration

	// local state
	latestBlockHash string
}

var kacp = keepalive.ClientParameters{
	Time:                10 * time.Second, // send pings every 10 seconds if there is no activity
	Timeout:             5 * time.Second,  // wait 5 second for ping ack before considering the connection dead
	PermitWithoutStream: true,             // send pings even without active streams
}

func NewSolGeyser(cfg config.BifrostBlockScannerConfiguration, scanner *SOLScanner, endpoint string, token string) *SolGeyser {
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
		logger.Error().Err(err).Msgf("Failed to connect: %v", err)
	}
	//defer conn.Close()

	logger.Info().Msg("Connection created")

	client := pb.NewGeyserClient(conn)

	logger.Info().Msg("Geyser client created")

	return &SolGeyser{
		cfg:                 cfg,
		scanner:             scanner,
		logger:              logger,
		endpoint:            endpoint,
		token:               token,
		client:              client,
		latestBlockHash:     "",
		feeRate:             0,
		lastReportedFeeRate: 0,
		feeRateCache:        []uint64{},
	}
}

func (g *SolGeyser) Run() (stypes.TxIn, error) {
	//resub := uint(1)
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
		return stypes.TxIn{}, err
	}

	if err = stream.Send(subReq); err != nil {
		g.logger.Error().Err(err).Msg("Failed to send subscription request.")
		return stypes.TxIn{}, err
	}

	//var i uint = 0
	for {
		// i += 1
		// if i == resub {
		// 	subReq = &pb.SubscribeRequest{
		// 		Commitment: &commitment,
		// 		Blocks: map[string]*pb.SubscribeRequestFilterBlocks{
		// 			"blocks": {},
		// 		},
		// 	}
		// 	stream.Send(subReq)
		// }

		resp, err := stream.Recv()
		timestamp := time.Now().UnixNano()

		// if err == io.EOF {
		// 	return stypes.TxIn{}, err
		// }
		if err != nil {
			g.logger.Error().Err(err).Msg("Error in receiving update.")
		}

		g.logger.Printf("%v %v", timestamp, resp)
		block := resp.GetBlock()
		height := block.GetBlockHeight().GetBlockHeight()
		g.logger.Info().Msgf("%s: Height: %d", time.Now().Format(time.UnixDate), height)
		if block.GetTransactions() != nil {
			g.FetchTxs(block)
		}
	}

	//return stypes.TxIn{}, nil
}

func (g *SolGeyser) FetchTxs(block *pb.SubscribeUpdateBlock) (stypes.TxIn, error) {
	rpcBlock := GeyserToRPCBlock(block)

	txIn, err := g.scanner.ProcessBlock(&rpcBlock)
	g.logger.Info().Msgf("Count of transactions: %d", len(txIn.TxArray))
	if err != nil {
		return stypes.TxIn{}, err
	}

	g.scanner.UpdateNetworkFee(int64(block.BlockHeight.BlockHeight))

	return txIn, nil
}

// GetLatestBlockHash returns the latest block hash
func (g *SolGeyser) GetLatestBlockHash() string {
	return g.latestBlockHash
}

func Stop() {

}
