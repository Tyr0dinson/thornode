package thorscan

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
	coretypes "github.com/tendermint/tendermint/rpc/core/types"
	tendertypes "github.com/tendermint/tendermint/types"
	tctypes "gitlab.com/thorchain/thornode/x/thorchain/types"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/std"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"
	"github.com/rs/zerolog/log"
	tmhttp "github.com/tendermint/tendermint/rpc/client/http"
)

// -------------------------------------------------------------------------------------
// Config
// -------------------------------------------------------------------------------------

const (
	// environment keys
	EnvPreFetch    = "PREFETCH"
	EnvRPCEndpoint = "RPC_ENDPOINT"

	// static configuration
	RPCWebsocketPath = "/websocket"
)

// -------------------------------------------------------------------------------------
// Init
// -------------------------------------------------------------------------------------

var (
	// environment config
	PreFetch    = 32
	RPCEndpoint = "http://rpc-http.ninerealms.com:80" // current tendermint missing tls

	// decoders
	DecodeTx     sdktypes.TxDecoder
	EncodeTx     sdktypes.TxEncoder
	DecodeTxJSON sdktypes.TxDecoder
	EncodeTxJSON sdktypes.TxEncoder

	// internal
	client *tmhttp.HTTP
)

func init() {
	var err error

	// set config from env
	if e := os.Getenv(EnvRPCEndpoint); e != "" {
		log.Info().Str("endpoint", e).Msg("setting rpc endpoint")
		RPCEndpoint = e
	}
	if e := os.Getenv(EnvPreFetch); e != "" {
		log.Info().Str("prefetch", e).Msg("setting prefetch blocks")
		PreFetch, err = strconv.Atoi(e)
		if err != nil {
			log.Fatal().Err(err).Msg("bad prefetch value")
		}
	}

	// setup serializers
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	std.RegisterInterfaces(interfaceRegistry)
	tctypes.RegisterInterfaces(interfaceRegistry)
	cdc := codec.NewProtoCodec(interfaceRegistry)
	DecodeTx = tx.DefaultTxDecoder(cdc)
	EncodeTx = tx.DefaultTxEncoder()
	DecodeTxJSON = tx.DefaultJSONTxDecoder(cdc)
	EncodeTxJSON = tx.DefaultJSONTxEncoder(cdc)

	// create new client with better connection reuse
	httpClient := &http.Client{
		Transport: http.DefaultTransport.(*http.Transport).Clone(),
	}
	httpClient.Transport.(*http.Transport).MaxIdleConns = PreFetch
	httpClient.Transport.(*http.Transport).MaxIdleConnsPerHost = PreFetch

	// setup client
	client, err = tmhttp.NewWithClient(RPCEndpoint, RPCWebsocketPath, httpClient)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create tendermint client")
	}

	err = client.Start()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to start tendermint client")
	}
}

// -------------------------------------------------------------------------------------
// Exported
// -------------------------------------------------------------------------------------

func ScanBlocks(start int) <-chan *coretypes.ResultBlock {
	ctx := context.Background()

	// create queue for block heights to fetch
	queue := make(chan int64)
	go func() {
		for height := int64(start); ; height++ {
			queue <- height
		}
	}()

	// setup ring buffer for block prefetching with routine per slot
	ring := make([]chan *coretypes.ResultBlock, PreFetch)
	shutdown := make(chan struct{}, PreFetch-1)
	for i := 0; i < PreFetch; i++ {
		ring[i] = make(chan *coretypes.ResultBlock)
		go func() {
			for height := range queue {
				for {
					b, err := client.Block(ctx, &height)
					if err != nil {
						log.Error().Err(err).Int64("height", height).Msg("failed to fetch block")
						time.Sleep(time.Second)
						continue
					}
					ring[int(height)%PreFetch] <- b

					// allow all but one routine to exit once we reach tip
					if b.Block.Header.Time.After(time.Now().Add(-time.Minute)) {
						select {
						case shutdown <- struct{}{}:
							return
						default:
						}
					}

					break
				}
			}
		}()
	}

	// start sequential reader to send to blocks channel
	out := make(chan *coretypes.ResultBlock)
	go func() {
		for height := int64(start); ; height++ {
			out <- <-ring[int(height)%PreFetch]
		}
	}()

	return out
}

func ScanBlockResults(start int) <-chan *coretypes.ResultBlockResults {
	ctx := context.Background()

	// create queue for block heights to fetch
	queue := make(chan int64)
	go func() {
		for height := int64(start); ; height++ {
			queue <- height
		}
	}()

	// setup ring buffer for block prefetching with routine per slot
	ring := make([]chan *coretypes.ResultBlockResults, PreFetch)
	for i := 0; i < PreFetch; i++ {
		ring[i] = make(chan *coretypes.ResultBlockResults)
		go func() {
			for height := range queue {
				for {
					b, err := client.BlockResults(ctx, &height)
					if err != nil {
						log.Error().Err(err).Int64("height", height).Msg("failed to fetch block results")
						time.Sleep(time.Second)
						continue
					}
					ring[int(height)%PreFetch] <- b

					break
				}
			}
		}()
	}

	// start sequential reader to send to blocks channel
	out := make(chan *coretypes.ResultBlockResults)
	go func() {
		for height := int64(start); ; height++ {
			out <- <-ring[int(height)%PreFetch]
		}
	}()

	return out
}

func SubscribeBlocks() <-chan *tendertypes.EventDataNewBlock {
	ctx := context.Background()

	// subscribe to blocks
	sub, err := client.Subscribe(ctx, "", "tm.event = 'NewBlock'", PreFetch)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to subscribe to new blocks")
	}

	// start routine to extract data from subscription
	out := make(chan *tendertypes.EventDataNewBlock)
	go func() {
		for {
			b := (<-sub).Data.(tendertypes.EventDataNewBlock)
			out <- &b
		}
	}()

	return out
}

func SubscribeTxs() <-chan *tendertypes.EventDataTx {
	ctx := context.Background()

	// subscribe to txs
	sub, err := client.Subscribe(ctx, "", "tm.event = 'Tx'", PreFetch)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to subscribe to new transactions")
	}

	// start routine to extract data from subscription
	out := make(chan *tendertypes.EventDataTx)
	go func() {
		for {
			b := (<-sub).Data.(tendertypes.EventDataTx)
			out <- &b
		}
	}()

	return out
}
