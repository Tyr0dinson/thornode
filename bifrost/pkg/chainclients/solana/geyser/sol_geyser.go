package geyser

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	pb "github.com/rpcpool/yellowstone-grpc/examples/golang/proto"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

const (
	LatestHashUpdateInterval = 100  // update latest block hash every 100 blocks
	BaseFeePerSigLamports    = 5000 // "Base" fee per signature of a transaction
)

type SolGeyser struct {
	cfg                config.BifrostBlockScannerConfiguration
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

func NewSolGeyser(cfg config.BifrostBlockScannerConfiguration, endpoint string, token string) *SolGeyser {
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

		//return g.FetchTxs(block)
	}

	//return stypes.TxIn{}, nil
}

// FetchTxs extracts all relevant transactions from the block at the provided height.
func (g *SolGeyser) FetchTxs(block *pb.SubscribeUpdateBlock) (stypes.TxIn, error) {
	height := block.GetBlockHeight().BlockHeight

	// log height every 1000 blocks
	if height%1000 == 0 {
		g.logger.Info().Uint64("height", height).Msg("fetching txs for height")
	}

	// Update latest block hash every LatestHashUpdateInterval blocks
	if height%LatestHashUpdateInterval == 0 {
		g.latestBlockHash = block.GetBlockhash()
	}

	// time how long it takes to process the block
	start := time.Now()

	// process all transactions in the block
	g.currentBlockHeight = height
	if len(block.Transactions) == 0 {
		// skip empty blocks
		return stypes.TxIn{}, nil
	}

	txIn, err := g.processBlock(block)
	if err != nil {
		return stypes.TxIn{}, err
	}
	duration := time.Since(start)
	g.totalDuration += duration
	g.blockCount++
	g.avgDuration = g.totalDuration / time.Duration(g.blockCount)

	if height%1000 == 0 {
		// TODO: remove or make debug
		g.logger.Info().Str("duration", duration.String()).Str("avg_duration", g.avgDuration.String()).Msg("sol block processed")
	}

	g.updateNetworkFee(height)

	return txIn, nil
}

// GetLatestBlockHash returns the latest block hash
func (g *SolGeyser) GetLatestBlockHash() string {
	return g.latestBlockHash
}

// --------------------------------- extraction ---------------------------------

func (s *SolGeyser) processBlock(block *pb.SubscribeUpdateBlock) (stypes.TxIn, error) {
	// skip empty blocks
	if len(block.Transactions) == 0 {
		return stypes.TxIn{}, nil
	}

	txIn := s.getTxIn(block)

	return txIn, nil
}

func (g *SolGeyser) getTxIn(block *pb.SubscribeUpdateBlock) stypes.TxIn {
	txIn := stypes.TxIn{
		Chain:                g.cfg.ChainID,
		TxArray:              []stypes.TxInItem{},
		Filtered:             false,
		MemPool:              false,
		SentUnFinalised:      false,
		Finalised:            true, // Assuming transactions in a block are finalised
		ConfirmationRequired: 1,    // Assuming 1 confirmation is required
	}

	// Stores each transaction's fee rate (lamports per signature)
	txFeeRates := []uint64{}

	// Stores each transaction's priority fee (txn.Meta.Fee - baseFee)
	//priorityFees := []uint64{}

	for _, txn := range block.Transactions {
		// Skip transactions with no signatures or no fee
		if len(txn.Transaction.Signatures) == 0 || txn.Meta.Fee == 0 {
			continue
		}

		// txItem := g.getTxInItem(txn, int64(block.ParentSlot))
		// if txItem == nil {
		// 	continue
		// }

		// txIn.TxArray = append(txIn.TxArray, *txItem)

		// // Calculate the fee rate (lamports per signature)
		// numSigs := uint64(len(txn.Transaction.Signatures))
		// baseFee := BaseFeePerSigLamports * numSigs
		// feeRate := txn.Meta.Fee / numSigs
		// if feeRate < BaseFeePerSigLamports {
		// 	feeRate = BaseFeePerSigLamports
		// }

		// txFeeRates = append(txFeeRates, feeRate)

		// // Priority fee is any fee in excess of the base fee
		// var priorityFee uint64
		// if txn.Meta.Fee == baseFee || txn.Meta.Fee < baseFee {
		// 	priorityFee = 0
		// } else {
		// 	priorityFee = txn.Meta.Fee - baseFee
		// }

		// priorityFees = append(priorityFees, priorityFee)
	}

	if len(txFeeRates) == 1 {
		g.feeRateCache = append(g.feeRateCache, txFeeRates[0])
	} else if len(txFeeRates) > 1 {
		medianFeeRate := calculateMedian(txFeeRates)
		g.feeRateCache = append(g.feeRateCache, medianFeeRate)
	} else {
		// If no transactions in the block, just add the base fee
		g.feeRateCache = append(g.feeRateCache, BaseFeePerSigLamports)
	}

	txIn.Count = strconv.Itoa(len(txIn.TxArray))
	return txIn
}

func (g *SolGeyser) updateNetworkFee(height uint64) {
	// Not enough blocks yet
	if len(g.feeRateCache) < g.cfg.GasCacheBlocks {
		return
	}

	// Calculate the median fee rate from the gas cache
	medianFeeRate := calculateMedian(g.feeRateCache)

	// round medianFeeRate to the nearest resolution
	resolution := uint64(g.cfg.GasPriceResolution)
	roundedFeeRate := ((medianFeeRate + (resolution / 2)) / resolution) * resolution

	// Update the fee rate if it has changed
	if roundedFeeRate != g.feeRate {
		// post to thorchain
		// if _, err := g.bridge.PostNetworkFee(height, g.cfg.ChainID, 1, roundedFeeRate); err != nil {
		// 	g.logger.Err(err).Msg("failed to post SOL chain single transfer fee to THORNode")
		// } else {
		// 	g.feeRate = roundedFeeRate
		// 	g.logger.Info().Int("feeRate", int(g.feeRate)).Msg("Updated fee rate")
		// }
	}
}

// Calculates the median of a slice of uint64
func calculateMedian(arr []uint64) uint64 {
	sort.Slice(arr, func(i, j int) bool { return arr[i] < arr[j] })
	n := len(arr)
	if n%2 == 0 {
		return (arr[n/2-1] + arr[n/2]) / 2
	}
	return arr[n/2]
}

func Stop() {

}
